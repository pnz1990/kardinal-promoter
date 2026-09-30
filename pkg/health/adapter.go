// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package health provides pluggable health adapters for promotion verification.
// Each adapter checks a different Kubernetes resource type to determine if a
// deployment is healthy after a promotion PR is merged.
//
// Phase 1 adapters: resource (Deployment), argocd (Argo CD Application), flux (Flux Kustomization).
// Phase 2 adapters: argoRollouts (Argo Rollouts Rollout), flagger (Flagger Canary).
package health

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
)

// HealthStatus is the result of a health check.
//
// A result that is not Healthy is one of three kinds:
//   - Progressing: the target is still moving to the promoted revision (the
//     GitOps tool has not synced the commit yet, the rollout is in progress).
//     This is not a health failure and does not count toward auto-rollback.
//   - Terminal: the target reports a failure that will not recover by itself
//     (Deployment ProgressDeadlineExceeded, Flagger canary Failed).
//   - neither: the target is unhealthy (Degraded, Available=False after the
//     rollout finished, not found). Each such check counts as a failure.
type HealthStatus struct {
	// Healthy is true when the workload runs the promoted revision and is fully available.
	Healthy bool
	// Progressing is true when the workload is not healthy yet but still rolling out.
	Progressing bool
	// Terminal is true when the workload failed in a way that will not recover.
	Terminal bool
	// Reason is a human-readable explanation.
	Reason string
	// CheckedAt records when the check was performed.
	CheckedAt time.Time
}

func healthy(reason string) HealthStatus {
	return HealthStatus{Healthy: true, Reason: reason, CheckedAt: time.Now()}
}

func progressing(reason string) HealthStatus {
	return HealthStatus{Progressing: true, Reason: reason, CheckedAt: time.Now()}
}

func unhealthy(reason string) HealthStatus {
	return HealthStatus{Reason: reason, CheckedAt: time.Now()}
}

func terminal(reason string) HealthStatus {
	return HealthStatus{Terminal: true, Reason: reason, CheckedAt: time.Now()}
}

// CheckOptions carries the health check configuration for a specific environment.
type CheckOptions struct {
	// Type selects the adapter: "resource", "argocd", "flux", "argoRollouts", "flagger".
	// OptionsForEnv resolves an empty health.type to DefaultType; AutoDetector.Select
	// itself rejects an empty type.
	Type string

	// Resource configuration (for type: resource).
	Resource ResourceConfig

	// ArgoCD configuration (for type: argocd).
	ArgoCD ArgoCDConfig

	// Flux configuration (for type: flux).
	Flux FluxConfig

	// ArgoRollouts configuration (for type: argoRollouts).
	ArgoRollouts ArgoRolloutsConfig

	// Flagger configuration (for type: flagger).
	Flagger FlaggerConfig

	// Timeout is the maximum time to wait for health. Default: 10 minutes.
	Timeout time.Duration

	// ExpectedRevision is the git commit the promotion delivered (the pushed
	// commit, or the PR merge commit). When set, the argocd and flux adapters
	// report Healthy only once the tool has synced this commit or a later one
	// that it recorded in its history.
	ExpectedRevision string

	// ExpectedImages are the Bundle images. The resource adapter (and the argocd
	// adapter when no ExpectedRevision is known) require every workload container
	// that runs one of these repositories to run the Bundle's tag or digest.
	ExpectedImages []ImageExpectation
}

// ResourceConfig is the health check configuration for a Kubernetes Deployment.
type ResourceConfig struct {
	// Name is the Deployment name. Defaults to pipeline name.
	// Ignored when LabelSelector is set.
	Name string
	// Namespace is the Deployment namespace. Defaults to environment name.
	Namespace string
	// Condition is the condition type to check. Default: "Available".
	Condition string
	// LabelSelector enables WatchKind mode — watches all Deployments with these labels.
	// When non-empty, Name is ignored and a WatchKind node is emitted by the translator.
	LabelSelector map[string]string
}

// ArgoCDConfig is the health check configuration for an Argo CD Application.
type ArgoCDConfig struct {
	// Name is the Application name.
	Name string
	// Namespace is the Application namespace. Default: "argocd".
	Namespace string
}

// FluxConfig is the health check configuration for a Flux Kustomization.
type FluxConfig struct {
	// Name is the Kustomization name.
	Name string
	// Namespace is the Kustomization namespace. Default: "flux-system".
	Namespace string
}

// ArgoRolloutsConfig is the health check configuration for an Argo Rollouts Rollout.
type ArgoRolloutsConfig struct {
	// Name is the Rollout name. Defaults to pipeline name.
	Name string
	// Namespace is the Rollout namespace. Defaults to environment name.
	Namespace string
}

// FlaggerConfig is the health check configuration for a Flagger Canary.
type FlaggerConfig struct {
	// Name is the Canary name. Defaults to pipeline name.
	Name string
	// Namespace is the Canary namespace. Defaults to environment name.
	Namespace string
}

// Adapter is the interface for health verification backends.
// All implementations must be idempotent and safe to call repeatedly.
type Adapter interface {
	// Check returns the health status of the target workload.
	// Called repeatedly (every 10s) until Healthy, timeout, or error.
	Check(ctx context.Context, opts CheckOptions) (HealthStatus, error)

	// Name returns the adapter identifier.
	Name() string
}

// --- DeploymentAdapter ---

// DeploymentAdapter checks Kubernetes Deployment readiness conditions.
type DeploymentAdapter struct {
	client sigs_client.Client
}

// NewDeploymentAdapter constructs a DeploymentAdapter.
func NewDeploymentAdapter(c sigs_client.Client) *DeploymentAdapter {
	return &DeploymentAdapter{client: c}
}

// Name returns "resource".
func (a *DeploymentAdapter) Name() string { return "resource" }

// Check reports the Deployment (or, with LabelSelector, every matching
// Deployment) healthy only when the rollout of the promoted revision is
// complete, the way `kubectl rollout status` decides it:
//
//  1. the pod template runs the Bundle images (ExpectedImages), else Progressing;
//  2. status.observedGeneration >= metadata.generation, else Progressing;
//  3. no Progressing condition with reason ProgressDeadlineExceeded, else Terminal;
//  4. updatedReplicas == spec.replicas, no old replicas left and every updated
//     replica available, else Progressing (or unhealthy when the rollout had
//     already finished and replicas became unavailable afterwards);
//  5. the configured condition (default Available) is True.
func (a *DeploymentAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.Resource
	if cfg.Condition == "" {
		cfg.Condition = "Available"
	}

	if len(cfg.LabelSelector) > 0 {
		var list appsv1.DeploymentList
		if err := a.client.List(ctx, &list, sigs_client.InNamespace(cfg.Namespace),
			sigs_client.MatchingLabels(cfg.LabelSelector)); err != nil {
			return HealthStatus{}, fmt.Errorf("list deployments in %s matching %v: %w", cfg.Namespace, cfg.LabelSelector, err)
		}
		if len(list.Items) == 0 {
			return unhealthy(fmt.Sprintf("no Deployment in namespace %s matches labels %v", cfg.Namespace, cfg.LabelSelector)), nil
		}
		// Every matching Deployment must be healthy. Report the most severe
		// result: terminal, then unhealthy, then progressing.
		var worst *HealthStatus
		for i := range list.Items {
			st := checkDeployment(&list.Items[i], cfg.Condition, opts.ExpectedImages)
			if st.Healthy {
				continue
			}
			if worst == nil || severity(st) > severity(*worst) {
				worst = &st
			}
		}
		if worst != nil {
			return *worst, nil
		}
		return healthy(fmt.Sprintf("%d Deployments matching %v rolled out and %s", len(list.Items), cfg.LabelSelector, cfg.Condition)), nil
	}

	var deploy appsv1.Deployment
	if err := a.client.Get(ctx, types.NamespacedName{
		Name:      cfg.Name,
		Namespace: cfg.Namespace,
	}, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return unhealthy(fmt.Sprintf("Deployment %s/%s not found", cfg.Namespace, cfg.Name)), nil
		}
		return HealthStatus{}, fmt.Errorf("get deployment %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}
	return checkDeployment(&deploy, cfg.Condition, opts.ExpectedImages), nil
}

func severity(st HealthStatus) int {
	switch {
	case st.Terminal:
		return 3
	case !st.Progressing:
		return 2
	default:
		return 1
	}
}

// checkDeployment applies the rollout checks documented on DeploymentAdapter.Check.
func checkDeployment(d *appsv1.Deployment, condition string, expected []ImageExpectation) HealthStatus {
	id := fmt.Sprintf("Deployment %s/%s", d.Namespace, d.Name)

	var running []string
	for _, c := range d.Spec.Template.Spec.Containers {
		running = append(running, c.Image)
	}
	imagesOK, imageNote := checkImages(expected, running)
	if !imagesOK {
		return progressing(fmt.Sprintf("%s not updated yet: %s", id, imageNote))
	}

	cond := deploymentCondition(d, condition)
	condText := fmt.Sprintf("condition %q not found", condition)
	if cond != nil {
		condText = fmt.Sprintf("%s=%s", cond.Type, cond.Status)
		if cond.Message != "" {
			condText += ": " + cond.Message
		}
	}

	if d.Status.ObservedGeneration < d.Generation {
		return progressing(fmt.Sprintf("%s: waiting for the Deployment controller to observe generation %d (observed %d)",
			id, d.Generation, d.Status.ObservedGeneration))
	}
	prog := deploymentCondition(d, string(appsv1.DeploymentProgressing))
	if prog != nil && prog.Reason == "ProgressDeadlineExceeded" {
		msg := fmt.Sprintf("%s rollout failed: ProgressDeadlineExceeded", id)
		if prog.Message != "" {
			msg += ": " + prog.Message
		}
		return terminal(msg)
	}

	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	st := d.Status
	switch {
	case st.UpdatedReplicas < want:
		return progressing(fmt.Sprintf("%s rolling out: %d of %d replicas updated (%s)", id, st.UpdatedReplicas, want, condText))
	case st.Replicas > st.UpdatedReplicas:
		return progressing(fmt.Sprintf("%s rolling out: %d old replicas pending termination (%s)",
			id, st.Replicas-st.UpdatedReplicas, condText))
	case st.AvailableReplicas < st.UpdatedReplicas:
		msg := fmt.Sprintf("%s: %d of %d updated replicas available (%s)", id, st.AvailableReplicas, st.UpdatedReplicas, condText)
		// Only an active rollout makes unavailable replicas "progressing". Once
		// the new ReplicaSet was available, losing replicas is a health failure.
		if prog != nil && prog.Status == corev1.ConditionTrue && prog.Reason != "NewReplicaSetAvailable" {
			return progressing(msg)
		}
		return unhealthy(msg)
	}

	if cond == nil {
		return unhealthy(fmt.Sprintf("%s: %s", id, condText))
	}
	if cond.Status != corev1.ConditionTrue {
		return unhealthy(fmt.Sprintf("%s: %s", id, condText))
	}
	reason := fmt.Sprintf("%s, %d/%d replicas updated and available", condText, st.AvailableReplicas, want)
	if imageNote != "" {
		reason += " " + imageNote
	}
	return healthy(reason)
}

func deploymentCondition(d *appsv1.Deployment, condType string) *appsv1.DeploymentCondition {
	for i := range d.Status.Conditions {
		if string(d.Status.Conditions[i].Type) == condType {
			return &d.Status.Conditions[i]
		}
	}
	return nil
}

// --- ArgoCDAdapter ---

// ArgoCDAdapter checks Argo CD Application health and sync status.
// Uses the dynamic client to avoid a compile-time dependency on the Argo CD SDK.
type ArgoCDAdapter struct {
	dynamic dynamic.Interface
}

// NewArgoCDAdapter constructs an ArgoCDAdapter.
func NewArgoCDAdapter(dynClient dynamic.Interface) *ArgoCDAdapter {
	return &ArgoCDAdapter{dynamic: dynClient}
}

// Name returns "argocd".
func (a *ArgoCDAdapter) Name() string { return "argocd" }

var argoCDApplicationGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// Check verifies that the Argo CD Application is Healthy and Synced to the
// promoted revision.
//
// With ExpectedRevision set, the Application must report that commit as its
// sync revision (status.sync.revision(s), status.operationState.syncResult.
// revision(s)) or in status.history: "Synced" alone only says the cluster
// matches whatever commit Argo CD last fetched, which can be the previous one
// (E2E-01). Without a revision (the argocd-set-image strategy pushes no
// commit), status.summary.images must carry the Bundle images instead.
//
// Degraded health and a Failed or Error operation are health failures; every
// other not-yet-healthy state (OutOfSync, Progressing, Missing, a running
// operation, an older revision) is Progressing.
func (a *ArgoCDAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.ArgoCD
	if cfg.Namespace == "" {
		cfg.Namespace = "argocd"
	}

	app, err := a.dynamic.Resource(argoCDApplicationGVR).
		Namespace(cfg.Namespace).
		Get(ctx, cfg.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return unhealthy(fmt.Sprintf("Application %s/%s not found", cfg.Namespace, cfg.Name)), nil
		}
		return HealthStatus{}, fmt.Errorf("get argo cd application %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	healthStatus, _, _ := unstructured.NestedString(app.Object, "status", "health", "status")
	syncStatus, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status")
	opPhase, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	state := fmt.Sprintf("health=%s, sync=%s, opPhase=%s", healthStatus, syncStatus, opPhase)

	revisionOK, revNote := argoCDRevision(app, opts)
	if !revisionOK {
		state += ", " + revNote
	}

	switch {
	case healthStatus == "Degraded", opPhase == "Failed", opPhase == "Error":
		return unhealthy(state), nil
	case healthStatus == "Healthy" && syncStatus == "Synced" && (opPhase == "Succeeded" || opPhase == "") && revisionOK:
		reason := fmt.Sprintf("Healthy+Synced (opPhase=%q)", opPhase)
		if revNote != "" {
			reason += " " + revNote
		}
		return healthy(reason), nil
	default:
		return progressing(state), nil
	}
}

// argoCDRevision reports whether the Application has deployed the promoted
// revision, with a note for the status message.
func argoCDRevision(app *unstructured.Unstructured, opts CheckOptions) (bool, string) {
	if want := opts.ExpectedRevision; want != "" {
		var seen []string
		add := func(fields ...string) {
			if v, ok, _ := unstructured.NestedString(app.Object, fields...); ok && v != "" {
				seen = append(seen, v)
			}
		}
		add("status", "sync", "revision")
		add("status", "operationState", "syncResult", "revision")
		for _, path := range [][]string{
			{"status", "sync", "revisions"},
			{"status", "operationState", "syncResult", "revisions"},
		} {
			if vs, ok, _ := unstructured.NestedStringSlice(app.Object, path...); ok {
				seen = append(seen, vs...)
			}
		}
		if history, ok, _ := unstructured.NestedSlice(app.Object, "status", "history"); ok {
			for _, h := range history {
				entry, _ := h.(map[string]interface{})
				if v, _ := entry["revision"].(string); v != "" {
					seen = append(seen, v)
				}
				if vs, _ := entry["revisions"].([]interface{}); vs != nil {
					for _, x := range vs {
						if v, _ := x.(string); v != "" {
							seen = append(seen, v)
						}
					}
				}
			}
		}
		for _, rev := range seen {
			if SameRevision(rev, want) {
				return true, ""
			}
		}
		current, _, _ := unstructured.NestedString(app.Object, "status", "sync", "revision")
		// A later commit on a shared branch (another environment's push) can
		// supersede ours before Argo CD fetches it. Accept that revision only
		// when the Application demonstrably runs the Bundle images.
		if ok, note := argoCDImages(app, opts.ExpectedImages); ok && note == "" && len(opts.ExpectedImages) > 0 {
			return true, fmt.Sprintf("(synced revision %s is not %s, but the Application runs the Bundle images)",
				shortRev(current), shortRev(want))
		}
		return false, fmt.Sprintf("revision=%s, waiting for %s", shortRev(current), shortRev(want))
	}
	if len(opts.ExpectedImages) > 0 {
		return argoCDImages(app, opts.ExpectedImages)
	}
	return true, "(revision not verified)"
}

func argoCDImages(app *unstructured.Unstructured, expected []ImageExpectation) (bool, string) {
	images, _, _ := unstructured.NestedStringSlice(app.Object, "status", "summary", "images")
	return checkImages(expected, images)
}

// --- FluxAdapter ---

// FluxAdapter checks Flux Kustomization Ready condition with generation matching.
type FluxAdapter struct {
	dynamic dynamic.Interface
}

// NewFluxAdapter constructs a FluxAdapter.
func NewFluxAdapter(dynClient dynamic.Interface) *FluxAdapter {
	return &FluxAdapter{dynamic: dynClient}
}

// Name returns "flux".
func (a *FluxAdapter) Name() string { return "flux" }

var fluxKustomizationGVR = schema.GroupVersionResource{
	Group:    "kustomize.toolkit.fluxcd.io",
	Version:  "v1",
	Resource: "kustomizations",
}

// Check verifies that the Flux Kustomization's Ready condition is True, that
// observedGeneration == generation (fully reconciled) and, with
// ExpectedRevision set, that status.lastAppliedRevision is that commit.
// Ready=False is a health failure; Ready=Unknown, a generation not yet
// observed or an older applied revision is Progressing.
func (a *FluxAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.Flux
	if cfg.Namespace == "" {
		cfg.Namespace = "flux-system"
	}

	ks, err := a.dynamic.Resource(fluxKustomizationGVR).
		Namespace(cfg.Namespace).
		Get(ctx, cfg.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return unhealthy(fmt.Sprintf("Kustomization %s/%s not found", cfg.Namespace, cfg.Name)), nil
		}
		return HealthStatus{}, fmt.Errorf("get flux kustomization %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	conditions, _, _ := unstructured.NestedSlice(ks.Object, "status", "conditions")
	observedGen, _, _ := unstructured.NestedInt64(ks.Object, "status", "observedGeneration")
	generation, _, _ := unstructured.NestedInt64(ks.Object, "metadata", "generation")
	applied, _, _ := unstructured.NestedString(ks.Object, "status", "lastAppliedRevision")

	readyCond := findCondition(conditions, "Ready")
	if readyCond == nil {
		return progressing("Ready condition not found"), nil
	}

	readyStatus, _ := readyCond["status"].(string)
	state := fmt.Sprintf("Ready=%s, observedGen=%d, generation=%d", readyStatus, observedGen, generation)
	if msg, _ := readyCond["message"].(string); msg != "" && readyStatus != "True" {
		state += ": " + msg
	}
	if readyStatus == "False" {
		return unhealthy(state), nil
	}
	if readyStatus != "True" || observedGen != generation {
		return progressing(state), nil
	}
	note := ""
	if want := opts.ExpectedRevision; want != "" {
		rev, verifiable := fluxCommit(applied)
		switch {
		case !verifiable:
			note = fmt.Sprintf(" (revision not verified: lastAppliedRevision %q is not a git commit)", applied)
		case !SameRevision(rev, want):
			return progressing(fmt.Sprintf("%s, lastAppliedRevision=%s, waiting for %s", state, shortRev(rev), shortRev(want))), nil
		}
	}
	return healthy(fmt.Sprintf("Ready=True, generation=%d matches%s", generation, note)), nil
}

// fluxCommit extracts the commit from a Flux lastAppliedRevision
// ("main@sha1:<sha>", or "main/<sha>" before Flux 2.0). OCI and bucket
// sources report a digest instead, which cannot be compared with a commit.
func fluxCommit(applied string) (string, bool) {
	if i := strings.LastIndex(applied, "sha1:"); i >= 0 {
		return applied[i+len("sha1:"):], true
	}
	if applied == "" || strings.Contains(applied, "sha256:") {
		return "", applied == ""
	}
	if i := strings.LastIndex(applied, "/"); i >= 0 {
		return applied[i+1:], true
	}
	return applied, true
}

// findCondition searches for a condition by type in the Flux conditions slice.
func findCondition(conditions []interface{}, condType string) map[string]interface{} {
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		t, _ := cond["type"].(string)
		if t == condType {
			return cond
		}
	}
	return nil
}

// --- ArgoRolloutsAdapter ---

// ArgoRolloutsAdapter checks Argo Rollouts Rollout health status.
// A Rollout is healthy when status.phase == "Healthy".
// Uses the dynamic client to avoid a compile-time dependency on the Argo Rollouts SDK.
type ArgoRolloutsAdapter struct {
	dynamic dynamic.Interface
}

// NewArgoRolloutsAdapter constructs an ArgoRolloutsAdapter.
func NewArgoRolloutsAdapter(dynClient dynamic.Interface) *ArgoRolloutsAdapter {
	return &ArgoRolloutsAdapter{dynamic: dynClient}
}

// Name returns "argoRollouts".
func (a *ArgoRolloutsAdapter) Name() string { return "argoRollouts" }

var argoRolloutsGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "rollouts",
}

// Check verifies that the Argo Rollouts Rollout is in the Healthy phase.
func (a *ArgoRolloutsAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.ArgoRollouts
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.Name == "" {
		return HealthStatus{Healthy: false, Reason: "ArgoRollouts.Name not configured", CheckedAt: time.Now()}, nil
	}

	rollout, err := a.dynamic.Resource(argoRolloutsGVR).
		Namespace(cfg.Namespace).
		Get(ctx, cfg.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return HealthStatus{
				Healthy:   false,
				Reason:    fmt.Sprintf("Rollout %s/%s not found", cfg.Namespace, cfg.Name),
				CheckedAt: time.Now(),
			}, nil
		}
		return HealthStatus{}, fmt.Errorf("get rollout %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	phase, _, _ := unstructured.NestedString(rollout.Object, "status", "phase")
	message, _, _ := unstructured.NestedString(rollout.Object, "status", "message")

	reason := fmt.Sprintf("Rollout phase: %s", phase)
	if message != "" {
		reason += " — " + message
	}
	switch phase {
	case "Healthy":
		return healthy(reason), nil
	case "Degraded":
		return unhealthy(reason), nil
	default: // Progressing, Paused, or not reported yet
		return progressing(reason), nil
	}
}

// --- FlaggerAdapter ---

// FlaggerAdapter checks Flagger Canary health status.
// A Canary is healthy when status.phase == "Succeeded".
// Uses the dynamic client to avoid a compile-time dependency on the Flagger SDK.
type FlaggerAdapter struct {
	dynamic dynamic.Interface
}

// NewFlaggerAdapter constructs a FlaggerAdapter.
func NewFlaggerAdapter(dynClient dynamic.Interface) *FlaggerAdapter {
	return &FlaggerAdapter{dynamic: dynClient}
}

// Name returns "flagger".
func (a *FlaggerAdapter) Name() string { return "flagger" }

var flaggerGVR = schema.GroupVersionResource{
	Group:    "flagger.app",
	Version:  "v1beta1",
	Resource: "canaries",
}

// Check verifies that the Flagger Canary is in the Succeeded phase.
func (a *FlaggerAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.Flagger
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.Name == "" {
		return HealthStatus{Healthy: false, Reason: "Flagger.Name not configured", CheckedAt: time.Now()}, nil
	}

	canary, err := a.dynamic.Resource(flaggerGVR).
		Namespace(cfg.Namespace).
		Get(ctx, cfg.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return HealthStatus{
				Healthy:   false,
				Reason:    fmt.Sprintf("Canary %s/%s not found", cfg.Namespace, cfg.Name),
				CheckedAt: time.Now(),
			}, nil
		}
		return HealthStatus{}, fmt.Errorf("get canary %s/%s: %w", cfg.Namespace, cfg.Name, err)
	}

	phase, _, _ := unstructured.NestedString(canary.Object, "status", "phase")
	statusMsg, _, _ := unstructured.NestedString(canary.Object, "status", "lastTransitionTime")

	reason := fmt.Sprintf("Canary phase: %s", phase)
	if statusMsg != "" {
		reason += fmt.Sprintf(" (lastTransition: %s)", statusMsg)
	}
	switch phase {
	case "Succeeded":
		return healthy("Canary phase: Succeeded"), nil
	case "Failed":
		// Flagger rolled the canary back; waiting will not make it succeed.
		return terminal(reason), nil
	default: // Initializing, Initialized, Waiting, Progressing, WaitingPromotion, Promoting, Finalising
		return progressing(reason), nil
	}
}

// --- AutoDetector ---

// AutoDetector returns the health adapter for an explicit health type. Despite
// its name it does not probe the cluster; see Select.
type AutoDetector struct {
	k8s     sigs_client.Client
	dynamic dynamic.Interface
}

// NewAutoDetector constructs an AutoDetector.
func NewAutoDetector(k8s sigs_client.Client, dynClient dynamic.Interface) *AutoDetector {
	return &AutoDetector{k8s: k8s, dynamic: dynClient}
}

// Select returns the adapter for the given health type.
// healthType must be one of: "resource", "argocd", "flux", "argoRollouts", "flagger".
// An empty or unknown healthType returns an error. Callers resolve an omitted
// health.type with EffectiveType (DefaultType "resource") before calling
// Select; there is no auto-detection by CRD probing (HE-4 in
// docs/design/11-graph-purity-tech-debt.md).
func (d *AutoDetector) Select(_ context.Context, healthType string) (Adapter, error) {
	switch healthType {
	case "resource":
		return NewDeploymentAdapter(d.k8s), nil
	case "argocd":
		return NewArgoCDAdapter(d.dynamic), nil
	case "flux":
		return NewFluxAdapter(d.dynamic), nil
	case "argoRollouts":
		return NewArgoRolloutsAdapter(d.dynamic), nil
	case "flagger":
		return NewFlaggerAdapter(d.dynamic), nil
	case "":
		return nil, fmt.Errorf(
			"health.type is required in Pipeline spec environments: " +
				"set health.type to one of [resource, argocd, flux, argoRollouts, flagger]")
	default:
		return nil, fmt.Errorf(
			"unknown health.type %q: must be one of [resource, argocd, flux, argoRollouts, flagger]",
			healthType)
	}
}
