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
	"regexp"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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
	// TargetUpdated is true when the check found the workload's target running
	// the Bundle images (ExpectedImages). The resource and flagger adapters set
	// it; the reconciler records the first such check in
	// status.targetUpdatedAt and passes it back as CheckOptions.TargetUpdatedAt.
	TargetUpdated bool
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
	// The argoRollouts and flagger adapters require the Rollout or Canary
	// target to run them.
	ExpectedImages []ImageExpectation

	// ImagesOnly reports that the Bundle changes only images (Bundle type
	// image): a pod template on the Bundle images is then the promoted state,
	// so the resource and flux adapters fail a Deployment on it at once when
	// its current ReplicaSet is past its progress deadline (see
	// deadlineOfReplicaSet). A config or mixed Bundle may change the pod
	// template in other ways, which the adapters cannot see, so the
	// condition's times decide (see deadlineFromEarlierRollout).
	ImagesOnly bool

	// Since is when the health check of this promotion started. The flagger
	// adapter ignores a Succeeded or Failed phase that Flagger set before it
	// when it cannot compare images, and the resource adapter a
	// ProgressDeadlineExceeded the Deployment controller set before it, when
	// the ReplicaSet it names does not decide. Zero skips that check.
	Since time.Time

	// TargetUpdatedAt is when a check of this promotion first found the
	// target (the Deployment, or the Canary's target) running the Bundle
	// images (HealthStatus.TargetUpdated); zero when no check has yet. With
	// Since set, the flagger adapter counts a Failed phase, and the resource
	// adapter a ProgressDeadlineExceeded, only when set after this time.
	TargetUpdatedAt time.Time
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
	// dynamic reads the ReplicaSet a ProgressDeadlineExceeded names (see
	// deadlineOfReplicaSet), uncached, and lists the pods of the new
	// ReplicaSet while replicas are unavailable (see podProblemLookup); nil
	// leaves the condition's times to decide and the pods unread.
	dynamic dynamic.Interface
}

// NewDeploymentAdapter constructs a DeploymentAdapter. dynClient may be nil.
func NewDeploymentAdapter(c sigs_client.Client, dynClient dynamic.Interface) *DeploymentAdapter {
	return &DeploymentAdapter{client: c, dynamic: dynClient}
}

// Name returns "resource".
func (a *DeploymentAdapter) Name() string { return "resource" }

// Check reports the Deployment (or, with LabelSelector, every matching
// Deployment) healthy only when the rollout of the promoted revision is
// complete, the way `kubectl rollout status` decides it:
//
//  1. the pod template runs the Bundle images (ExpectedImages), else Progressing;
//  2. status.observedGeneration >= metadata.generation, else Progressing;
//  3. no Progressing condition with reason ProgressDeadlineExceeded, else
//     Terminal, or Progressing when the condition is from an earlier rollout
//     (see deadlineOfReplicaSet and deadlineFromEarlierRollout);
//  4. updatedReplicas == spec.replicas, no old replicas left and every updated
//     replica available, else Progressing (or unhealthy when the rollout had
//     already finished and replicas became unavailable afterwards);
//  5. the configured condition (default Available) is True.
func (a *DeploymentAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.Resource
	if cfg.Condition == "" {
		cfg.Condition = "Available"
	}
	rs := replicaSetLookup(ctx, a.dynamic)
	pods := podProblemLookup(ctx, a.dynamic)

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
		// result: terminal, then unhealthy, then progressing. The target is
		// updated when every Deployment whose images can be compared with the
		// Bundle's runs them, and there is one.
		var worst *HealthStatus
		comparable, updated := 0, 0
		for i := range list.Items {
			d := &list.Items[i]
			st := checkDeployment(d, cfg.Condition, opts, rs, pods)
			if runsRepository(opts.ExpectedImages, deploymentImages(d)) {
				comparable++
				if st.TargetUpdated {
					updated++
				}
			}
			if st.Healthy {
				continue
			}
			if worst == nil || severity(st) > severity(*worst) {
				worst = &st
			}
		}
		result := healthy(fmt.Sprintf("%d Deployments matching %v rolled out and %s", len(list.Items), cfg.LabelSelector, cfg.Condition))
		if worst != nil {
			result = *worst
		}
		result.TargetUpdated = comparable > 0 && updated == comparable
		return result, nil
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
	return checkDeployment(&deploy, cfg.Condition, opts, rs, pods), nil
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
// It reads opts.ExpectedImages, ImagesOnly, Since and TargetUpdatedAt, and
// sets TargetUpdated when the pod template runs the Bundle images. A result
// that is not Healthy while replicas are unavailable names why a new pod is
// not ready (see withPodProblem). rs and pods may be nil.
func checkDeployment(d *appsv1.Deployment, condition string, opts CheckOptions, rs replicaSetRevision, pods newPodProblem) HealthStatus {
	running := deploymentImages(d)
	imagesOK, imageNote := checkImages(opts.ExpectedImages, running)
	updated := imagesOK && runsRepository(opts.ExpectedImages, running)
	st := deploymentRollout(d, condition, imagesOK, imageNote, updated, updated && opts.ImagesOnly, opts, rs)
	if imagesOK {
		st = withPodProblem(st, d, pods)
	}
	st.TargetUpdated = updated
	return st
}

func deploymentImages(d *appsv1.Deployment) []string {
	var running []string
	for _, c := range d.Spec.Template.Spec.Containers {
		running = append(running, c.Image)
	}
	return running
}

// deploymentRollout is checkDeployment after the image check: updated
// reports that the pod template was compared with the Bundle images and runs
// them, and promoted that it is the promoted state, so that a
// ProgressDeadlineExceeded of the Deployment's current ReplicaSet is this
// promotion's (see CheckOptions.ImagesOnly).
func deploymentRollout(d *appsv1.Deployment, condition string, imagesOK bool, imageNote string,
	updated, promoted bool, opts CheckOptions, rs replicaSetRevision) HealthStatus {
	id := fmt.Sprintf("Deployment %s/%s", d.Namespace, d.Name)
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
		deadline := "ProgressDeadlineExceeded"
		if prog.Message != "" {
			deadline += " (" + prog.Message + ")"
		}
		// The current ReplicaSet's stall is this promotion's only when the
		// pod template is the promoted state. Before Argo CD applies a
		// rollback of a config change, say, it is the stall of the release
		// the rollback replaces.
		earlier, known := deadlineOfReplicaSet(d, prog, rs)
		if !known || earlier == "" && !promoted {
			earlier = deadlineFromEarlierRollout(prog, updated, opts)
		}
		if earlier != "" {
			return progressing(fmt.Sprintf("%s: %s is from an earlier rollout: %s; "+
				"waiting for the Deployment controller to see this rollout progress", id, deadline, earlier))
		}
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
	// A rolling update replaces old pods only as new ones become available,
	// so a new pod that never starts (an image that cannot be pulled, a crash
	// loop, a failing readiness probe) shows as old replicas that stay.
	waitingFor := ""
	if st.UnavailableReplicas > 0 {
		waitingFor = fmt.Sprintf("; %d of %d replicas unavailable: the rollout waits for new pods to become available "+
			"(if this lasts, check the new pods, for example for an image that cannot be pulled)",
			st.UnavailableReplicas, st.Replicas)
	}
	switch {
	case st.UpdatedReplicas < want:
		return progressing(fmt.Sprintf("%s rolling out: %d of %d replicas updated%s (%s)",
			id, st.UpdatedReplicas, want, waitingFor, condText))
	case st.Replicas > st.UpdatedReplicas:
		return progressing(fmt.Sprintf("%s rolling out: %d old replicas pending termination%s (%s)",
			id, st.Replicas-st.UpdatedReplicas, waitingFor, condText))
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

// replicaSetRevision returns the revision (deployment.kubernetes.io/revision)
// of ReplicaSet namespace/name, and found false when it does not exist.
type replicaSetRevision func(namespace, name string) (revision string, found bool, err error)

var replicaSetGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}

// deploymentRevisionAnnotation is the revision the Deployment controller
// sets on a Deployment and on each of its ReplicaSets.
const deploymentRevisionAnnotation = "deployment.kubernetes.io/revision"

// replicaSetLookup reads ReplicaSets through dyn, or is nil without dyn. A
// health check reads one only for a Deployment past its progress deadline,
// so it does not use the cached client, whose informer would watch every
// ReplicaSet.
func replicaSetLookup(ctx context.Context, dyn dynamic.Interface) replicaSetRevision {
	if dyn == nil {
		return nil
	}
	return func(namespace, name string) (string, bool, error) {
		rs, err := dyn.Resource(replicaSetGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		return rs.GetAnnotations()[deploymentRevisionAnnotation], true, nil
	}
}

// timedOutReplicaSet matches the message of the ProgressDeadlineExceeded the
// Deployment controller sets: it names the Deployment's new ReplicaSet at the
// time (pkg/controller/deployment/progress.go).
var timedOutReplicaSet = regexp.MustCompile(`^ReplicaSet "([a-z0-9.-]+)" has timed out progressing\.$`)

// deadlineOfReplicaSet tells from the ReplicaSet a ProgressDeadlineExceeded
// names whether the condition is from an earlier rollout: it explains why, or
// returns "" when that ReplicaSet is the Deployment's current one, whose
// revision is the Deployment's, so the rollout of the current pod template
// stalled (this promotion's when that template is the promoted state; see
// deploymentRollout). A rollout back to an existing ReplicaSet gives that ReplicaSet the
// next revision, so a condition the controller kept from the stalled rollout
// names a ReplicaSet of another revision (see deadlineFromEarlierRollout), or
// one the controller has since deleted. known is false when the ReplicaSet
// cannot tell: no rs, a message of another form, a Deployment or ReplicaSet
// without a revision, or an error reading it (a chart without get on
// replicasets, say). The condition's times decide then.
func deadlineOfReplicaSet(d *appsv1.Deployment, prog *appsv1.DeploymentCondition, rs replicaSetRevision) (earlier string, known bool) {
	current := d.Annotations[deploymentRevisionAnnotation]
	m := timedOutReplicaSet.FindStringSubmatch(prog.Message)
	if rs == nil || current == "" || m == nil {
		return "", false
	}
	rev, found, err := rs(d.Namespace, m[1])
	switch {
	case err != nil:
		return "", false
	case !found:
		return fmt.Sprintf("ReplicaSet %s no longer exists", m[1]), true
	case rev == "":
		return "", false
	case rev != current:
		return fmt.Sprintf("ReplicaSet %s has revision %s, not the Deployment's revision %s", m[1], rev, current), true
	}
	return "", true
}

// deadlineFromEarlierRollout explains why a ProgressDeadlineExceeded
// condition is from an earlier rollout than this promotion's, or returns ""
// when it may be this promotion's. It decides when deadlineOfReplicaSet
// cannot, or finds the current ReplicaSet stalled while the pod template may
// not be the promoted state yet.
//
// The Deployment controller replaces the condition only when it sees the
// rollout progress, or when it creates a ReplicaSet. A rollout back to an
// existing ReplicaSet (a rollback to the revision that ran before a stalled
// one) creates none: until the controller sees the stalled ReplicaSet's pods
// go, the Deployment observes the new template and still reports the
// ProgressDeadlineExceeded, with the time and message of the stalled one. So,
// like the flagger adapter's Failed phase, the condition counts only when set
// after the health check started and after a check first found the pod
// template on the Bundle images. The controller sets it no sooner than
// progressDeadlineSeconds after the rollout started, and checks run every 10
// seconds. If the rollout of the Bundle stalls again, the condition does not
// change, and the step fails at health.timeout instead of at once.
//
// With Since zero (the promotion changed nothing in git, or the step sequence
// has no health-check step) it takes every ProgressDeadlineExceeded for this
// promotion's.
func deadlineFromEarlierRollout(prog *appsv1.DeploymentCondition, updated bool, opts CheckOptions) string {
	if opts.Since.IsZero() {
		return ""
	}
	at, field := prog.LastUpdateTime.Time, "lastUpdateTime"
	if at.IsZero() {
		at, field = prog.LastTransitionTime.Time, "lastTransitionTime"
	}
	since := opts.Since.Truncate(time.Second)
	switch {
	case at.Before(since):
		return fmt.Sprintf("its %s %s is before this health check started (%s)",
			field, at.UTC().Format(time.RFC3339), since.UTC().Format(time.RFC3339))
	case updated && opts.TargetUpdatedAt.IsZero():
		return "this check is the first to find the pod template running the Bundle images"
	case updated && !at.After(opts.TargetUpdatedAt.Truncate(time.Second)):
		// Both times have whole seconds: one in the same second as the
		// template update may be the earlier rollout's.
		return fmt.Sprintf("its %s %s is not after the health check first found the pod template running the Bundle images (%s)",
			field, at.UTC().Format(time.RFC3339), opts.TargetUpdatedAt.UTC().Format(time.RFC3339))
	}
	return ""
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
// Degraded health and a Failed or Error operation are health failures only
// when they are about the promoted change (B52): until the Application has
// deployed it, Degraded health describes the version before it, and an
// operation counts only when it ran on the promoted commit. An operation
// still running on the promoted commit has not deployed it (a PreSync hook
// can run for minutes), so while it runs Degraded counts only once the
// Application is Synced on the commit or has it in status.history. Without a
// revision, a status.summary.images with none of the Bundle repositories
// shows neither version: Healthy and Synced still pass, unverified, but
// Degraded and a failed operation wait for health.timeout (B67). Every
// other not-yet-healthy state (OutOfSync, Progressing, Missing, a running
// operation, an older revision, a failure from before the change) is
// Progressing, so it never adds to status.consecutiveHealthFailures.
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

	target := argoCDRevision(app, syncStatus, opPhase, opts)
	if !target.deployed || target.unverified {
		state += ", " + target.note
	}
	opFailed := opPhase == "Failed" || opPhase == "Error"
	if opFailed && !target.operated && target.opNote != "" {
		state += ", " + target.opNote
	}
	// An operation on another revision says nothing about the promoted
	// change once it has finished; a running one still changes the cluster.
	opOK := opPhase == "Succeeded" || opPhase == "" || (!target.operated && opFailed)

	switch {
	case !target.unverified && ((healthStatus == "Degraded" && target.deployed) || (opFailed && target.operated)):
		return unhealthy(state), nil
	case healthStatus == "Healthy" && syncStatus == "Synced" && opOK && target.deployed:
		reason := fmt.Sprintf("Healthy+Synced (opPhase=%q)", opPhase)
		if target.note != "" {
			reason += " " + target.note
		}
		return healthy(reason), nil
	default:
		return progressing(state), nil
	}
}

// argoCDTarget is how far an Application has got with the promoted change.
type argoCDTarget struct {
	// deployed: the Application has applied the promoted change, so its
	// health is about it.
	deployed bool
	// operated: status.operationState ran on the promoted change, so its
	// phase counts.
	operated bool
	// unverified: deployed is assumed, not shown (update.strategy argocd,
	// with none of the Bundle repositories in status.summary.images).
	// Healthy passes the change, but Degraded health and a failed operation
	// can be the previous version's, so they do not count (B67).
	unverified bool
	// note is for the status message: why the change is not deployed yet,
	// or how it was verified.
	note string
	// opNote names the revision of an operation that does not count.
	opNote string
}

// argoCDRevision reports whether the Application has deployed the promoted
// revision and whether its last operation ran on it.
//
// With ExpectedRevision set, the change is deployed once the Application is
// Synced on that commit, a finished operation ran on it, or it is in
// status.history.
// status.sync.revision alone is not enough: Argo CD sets it to the newest
// commit it fetched, also while the Application is OutOfSync with auto-sync
// off. The operation's revision is status.operationState.syncResult.
// revision(s), or the requested operation.sync.revision(s) before Argo CD
// records a result.
func argoCDRevision(app *unstructured.Unstructured, syncStatus, opPhase string, opts CheckOptions) argoCDTarget {
	want := opts.ExpectedRevision
	if want == "" {
		if len(opts.ExpectedImages) > 0 {
			// No commit to compare (update.strategy argocd): an operation or
			// health from before the Application runs the Bundle images is
			// about the previous version. A summary with none of the Bundle
			// repositories shows neither version, so only Healthy counts.
			ok, note := argoCDImages(app, opts.ExpectedImages)
			return argoCDTarget{deployed: ok, operated: ok, unverified: ok && note != "", note: note}
		}
		return argoCDTarget{deployed: true, operated: true, note: "(revision not verified)"}
	}

	syncRevs := revisions(app, "status", "sync")
	opRevs := revisions(app, "status", "operationState", "syncResult")
	if len(opRevs) == 0 {
		opRevs = revisions(app, "status", "operationState", "operation", "sync")
	}
	var historyRevs []string
	if history, ok, _ := unstructured.NestedSlice(app.Object, "status", "history"); ok {
		for _, h := range history {
			entry, _ := h.(map[string]interface{})
			historyRevs = append(historyRevs, revisionsOf(entry)...)
		}
	}

	// An operation on the promoted commit deploys it only once it has
	// finished: while it runs, a PreSync hook or an earlier sync wave can
	// still hold the previous version, so only Synced or history count.
	finished := opPhase == "Succeeded" || opPhase == "Failed" || opPhase == "Error"
	t := argoCDTarget{operated: hasRevision(opRevs, want)}
	t.deployed = (t.operated && finished) || (syncStatus == "Synced" && hasRevision(syncRevs, want)) || hasRevision(historyRevs, want)
	current, _, _ := unstructured.NestedString(app.Object, "status", "sync", "revision")
	t.opNote = "ignoring an operation with no revision"
	if len(opRevs) > 0 {
		t.opNote = "ignoring the operation on " + shortRev(opRevs[0])
	}
	if t.deployed {
		return t
	}
	// A later commit on a shared branch (another environment's push) can
	// supersede ours before Argo CD fetches it. Accept that revision only
	// when the Application demonstrably runs the Bundle images; an
	// operation on that revision then counts. An operation still running on
	// the promoted commit means ours is the change in flight, not a
	// superseded one, so it waits for Synced or history as above.
	if ok, note := argoCDImages(app, opts.ExpectedImages); ok && note == "" && len(opts.ExpectedImages) > 0 && !t.operated {
		t.deployed = true
		t.operated = len(syncRevs) > 0 && hasRevision(opRevs, syncRevs...)
		t.note = fmt.Sprintf("(synced revision %s is not %s, but the Application runs the Bundle images)",
			shortRev(current), shortRev(want))
		return t
	}
	if hasRevision(syncRevs, want) {
		t.note = fmt.Sprintf("revision=%s not synced yet", shortRev(current))
	} else {
		t.note = fmt.Sprintf("revision=%s, waiting for %s", shortRev(current), shortRev(want))
	}
	return t
}

// revisions reads revision and revisions under fields of the Application.
func revisions(app *unstructured.Unstructured, fields ...string) []string {
	m, _, _ := unstructured.NestedMap(app.Object, fields...)
	return revisionsOf(m)
}

// revisionsOf reads the revision and revisions keys of m.
func revisionsOf(m map[string]interface{}) []string {
	var out []string
	if v, _ := m["revision"].(string); v != "" {
		out = append(out, v)
	}
	vs, _ := m["revisions"].([]interface{})
	for _, x := range vs {
		if v, _ := x.(string); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// hasRevision reports whether any of revs is the same commit as any of want.
func hasRevision(revs []string, want ...string) bool {
	for _, rev := range revs {
		for _, w := range want {
			if SameRevision(rev, w) {
				return true
			}
		}
	}
	return false
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
// ExpectedRevision set, that status.lastAppliedRevision is that commit, or
// another commit while the Kustomization's Deployments run the Bundle
// images (a sibling environment pushed to the same branch). The adapter
// cannot tell whether that other commit is later than the promoted one: it
// checks the images, not the git history.
// Ready=False is a health failure, and Terminal when Flux gave up on the
// promoted commit because its resources stalled, or on another commit when
// a stalled Deployment itself runs the Bundle images. A stall of a
// Deployment whose ProgressDeadlineExceeded is from an earlier rollout is
// Progressing (see stalledEarlier), on the promoted commit or another, and
// so is Ready=False on another git commit while no Deployment of the
// Kustomization runs the Bundle images: Flux has not applied the promoted
// change. Ready=Unknown, a
// generation not yet observed or another applied revision is Progressing.
// While the Kustomization is suspended (spec.suspend) Flux applies nothing,
// so a Progressing result says so.
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

	st, err := a.check(ctx, ks, opts)
	if err != nil {
		return HealthStatus{}, err
	}
	if suspended, _, _ := unstructured.NestedBool(ks.Object, "spec", "suspend"); suspended && st.Progressing {
		st.Reason = fmt.Sprintf("Kustomization %s/%s is suspended; Flux applies nothing until it is resumed (%s)",
			cfg.Namespace, cfg.Name, st.Reason)
	}
	return st, nil
}

// check is Check on the Kustomization ks, without the suspend note.
func (a *FluxAdapter) check(ctx context.Context, ks *unstructured.Unstructured, opts CheckOptions) (HealthStatus, error) {
	conditions, _, _ := unstructured.NestedSlice(ks.Object, "status", "conditions")
	observedGen, observedFound, _ := unstructured.NestedInt64(ks.Object, "status", "observedGeneration")
	generation, generationFound, _ := unstructured.NestedInt64(ks.Object, "metadata", "generation")
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
		// Flux gives up early on a Deployment past its progress deadline
		// ("failed early due to stalled resources"); like a
		// ProgressDeadlineExceeded of the promoted rollout, that will not
		// recover. Only a
		// stall of the promoted commit fails the step at once, or of another
		// commit on the shared branch (another environment's push) when a
		// Deployment that runs the Bundle images is itself past its progress
		// deadline: Flux applied the Bundle's change and that rollout
		// stalled. Another Deployment's stall is not ours to fail on, nor a
		// ProgressDeadlineExceeded from an earlier rollout.
		// A failure on another commit while no Deployment of the
		// Kustomization runs the Bundle images is not about the promoted
		// change (B94): Flux has not applied it yet, as after a failed
		// release until Flux fetches the fix. That waits, as Argo CD's
		// Degraded from before the change does (B52).
		reason, _ := readyCond["reason"].(string)
		msg, _ := readyCond["message"].(string)
		stalled := reason == "HealthCheckFailed" && strings.Contains(msg, "stalled resources")
		attempted, _, _ := unstructured.NestedString(ks.Object, "status", "lastAttemptedRevision")
		rev, _ := fluxCommit(attempted)
		want := opts.ExpectedRevision
		if want == "" || SameRevision(rev, want) {
			if !stalled {
				return unhealthy(state), nil
			}
			st := fmt.Sprintf("%s (lastAttemptedRevision=%s)", state, shortRev(rev))
			if earlier := a.stalledEarlier(ctx, ks, msg, opts); earlier != "" {
				return progressing(fmt.Sprintf("%s, but %s; waiting for Flux to check again", st, earlier)), nil
			}
			return terminal(st), nil
		}
		w, err := a.workloads(ctx, ks, opts)
		if err != nil {
			return unhealthy(state), nil
		}
		if stalled {
			for _, d := range w {
				if d.bundle && d.status.Terminal {
					return terminal(fmt.Sprintf("%s (lastAttemptedRevision=%s, not %s, but Deployment %s, which runs the Bundle images, stalled)",
						state, shortRev(rev), shortRev(want), d.ref)), nil
				}
			}
		}
		other := fmt.Sprintf("%s (lastAttemptedRevision=%s, not %s", state, shortRev(rev), shortRev(want))
		if rev != "" && !w.anyBundle() {
			return progressing(other + ": Flux has not applied the promoted change, " +
				"and no Deployment of the Kustomization runs the Bundle images)"), nil
		}
		if stalled {
			if earlier := a.stalledEarlier(ctx, ks, msg, opts); earlier != "" {
				return progressing(fmt.Sprintf("%s), but %s; waiting for Flux to check again", other, earlier)), nil
			}
		}
		return unhealthy(state), nil
	}
	// A missing field would read as 0 and make 0 == 0 look reconciled.
	if !generationFound {
		return progressing("metadata.generation not set"), nil
	}
	if !observedFound {
		return progressing("status.observedGeneration not set: Flux has not reconciled this generation"), nil
	}
	attempted, _, _ := unstructured.NestedString(ks.Object, "status", "lastAttemptedRevision")
	if readyStatus == "Unknown" && observedGen == generation && applied != "" && attempted == applied {
		return a.reconcilingAgain(ctx, ks, state, applied, opts), nil
	}
	if readyStatus != "True" || observedGen != generation {
		return progressing(state), nil
	}
	reason := fmt.Sprintf("Ready=True, generation=%d matches", generation)
	rev, verifiable := fluxCommit(applied)
	if verifiable && rev != "" {
		reason += ", lastAppliedRevision=" + shortRev(rev)
	}
	if want := opts.ExpectedRevision; want != "" {
		switch {
		case !verifiable:
			reason += fmt.Sprintf(" (revision not verified: lastAppliedRevision %q is not a git commit)", applied)
		case !SameRevision(rev, want):
			waiting := progressing(fmt.Sprintf("%s, lastAppliedRevision=%s, waiting for %s", state, shortRev(rev), shortRev(want)))
			// Another commit on the shared branch (another environment's
			// push) can supersede ours before Flux fetches it. Accept that
			// revision only when the Kustomization's Deployments
			// demonstrably run the Bundle images and are all rolled out.
			// Nothing here can tell whether that commit is later than ours.
			w, err := a.workloads(ctx, ks, opts)
			if err != nil || !w.runsBundle() {
				return waiting, nil
			}
			if all, _ := w.worst(nil); !all.Healthy {
				return waiting, nil
			}
			return healthy(fmt.Sprintf("%s (not %s, but the Kustomization's Deployments run the Bundle images)",
				reason, shortRev(want))), nil
		}
	}
	return healthy(reason), nil
}

// reconcilingAgain checks a Kustomization that is Ready=Unknown because Flux
// reconciles again the revision it last applied (its interval, `flux
// reconcile` or a webhook receiver): Flux marks every reconcile
// Ready=Unknown until it ends. That revision passed Flux's health checks
// when Flux applied it, so while Flux checks again the Deployments that run
// a Bundle repository decide: the result is theirs, Healthy when they are
// all rolled out and the applied revision is the promoted one. The other
// Deployments are not the promotion's: while one of them is not healthy the
// result is Progressing, never a health failure, and Flux's own result
// decides once the reconcile ends. Without Deployments that run a Bundle
// repository, or for another revision, it is Progressing as before.
func (a *FluxAdapter) reconcilingAgain(ctx context.Context, ks *unstructured.Unstructured, state, applied string,
	opts CheckOptions) HealthStatus {
	w, err := a.workloads(ctx, ks, opts)
	if err != nil {
		return progressing(state)
	}
	ours, n := w.worst(func(d fluxDeployment) bool { return d.bundleRepo })
	if n == 0 {
		return progressing(state)
	}
	rev, verifiable := fluxCommit(applied)
	note := ""
	switch want := opts.ExpectedRevision; {
	case want == "":
	case !verifiable:
		note = fmt.Sprintf(" (revision not verified: lastAppliedRevision %q is not a git commit)", applied)
	case SameRevision(rev, want):
	case w.runsBundle():
		note = fmt.Sprintf(" (not %s, but the Kustomization's Deployments run the Bundle images)", shortRev(want))
	default:
		return progressing(fmt.Sprintf("%s, lastAppliedRevision=%s, waiting for %s", state, shortRev(rev), shortRev(want)))
	}
	prefix := fmt.Sprintf("Ready=Unknown while Flux reconciles lastAppliedRevision=%s again", shortRev(rev))
	if !verifiable {
		prefix = "Ready=Unknown while Flux reconciles lastAppliedRevision again"
	}
	st := ours
	if st.Healthy {
		if other, _ := w.worst(func(d fluxDeployment) bool { return !d.bundleRepo }); !other.Healthy {
			return progressing(fmt.Sprintf("%s: %s; waiting for Flux, because a Deployment that runs no Bundle image is not healthy: %s",
				prefix, st.Reason, other.Reason))
		}
	}
	st.Reason = fmt.Sprintf("%s: %s%s", prefix, st.Reason, note)
	return st
}

// fluxDeployment is one of a Kustomization's Deployments, checked with
// checkDeployment.
type fluxDeployment struct {
	ref    types.NamespacedName
	status HealthStatus
	// bundleRepo is true when a container runs an image of a Bundle
	// repository, whatever its tag.
	bundleRepo bool
	// bundle is true when the Deployment runs the Bundle images: it runs a
	// Bundle repository and every such image matches the Bundle's.
	bundle bool
}

// fluxWorkloads is the result of checking a Kustomization's Deployments.
type fluxWorkloads []fluxDeployment

// runsBundle reports whether at least one Deployment runs the Bundle images
// and none runs another tag or digest of a Bundle repository.
func (w fluxWorkloads) runsBundle() bool {
	found := false
	for _, d := range w {
		if d.bundleRepo && !d.bundle {
			return false
		}
		found = found || d.bundle
	}
	return found
}

// anyBundle reports whether a Deployment runs the Bundle images.
func (w fluxWorkloads) anyBundle() bool {
	for _, d := range w {
		if d.bundle {
			return true
		}
	}
	return false
}

// worst returns the worst result (severity) of the Deployments keep selects
// (all of them when keep is nil), or a healthy summary when every one is
// healthy, and how many it selected. With none selected it is Healthy.
func (w fluxWorkloads) worst(keep func(fluxDeployment) bool) (HealthStatus, int) {
	var worst HealthStatus
	n := 0
	for _, d := range w {
		if keep != nil && !keep(d) {
			continue
		}
		switch {
		case n == 0:
			worst = d.status
		case d.status.Healthy:
		case worst.Healthy || severity(d.status) > severity(worst):
			worst = d.status
		}
		n++
	}
	switch {
	case n == 0:
		return HealthStatus{Healthy: true}, 0
	case n > 1 && worst.Healthy:
		return healthy(fmt.Sprintf("%d Deployments rolled out and Available", n)), n
	}
	return worst, n
}

// workloads checks, with checkDeployment, the Deployments the Kustomization
// applied (status.inventory) or health-checks (spec.healthChecks). A
// Kustomization that applies to another cluster (spec.kubeConfig) has none
// that can be read here.
func (a *FluxAdapter) workloads(ctx context.Context, ks *unstructured.Unstructured, opts CheckOptions) (fluxWorkloads, error) {
	var w fluxWorkloads
	if _, remote, _ := unstructured.NestedMap(ks.Object, "spec", "kubeConfig"); remote {
		return w, nil
	}
	for _, ref := range fluxDeploymentRefs(ks) {
		fd := fluxDeployment{ref: ref}
		d, err := getDeployment(ctx, a.dynamic, ref.Namespace, ref.Name)
		switch {
		case apierrors.IsNotFound(err):
			fd.status = unhealthy(fmt.Sprintf("Deployment %s not found", ref))
		case err != nil:
			return nil, err
		default:
			fd.status = a.checkFluxDeployment(ctx, d, opts)
			if fd.status.Healthy {
				fd.status.Reason = fmt.Sprintf("Deployment %s: %s", ref, fd.status.Reason)
			}
			images := deploymentImages(d)
			fd.bundleRepo = runsRepository(opts.ExpectedImages, images)
			ok, note := checkImages(opts.ExpectedImages, images)
			fd.bundle = fd.bundleRepo && ok && note == ""
		}
		w = append(w, fd)
	}
	return w, nil
}

// checkFluxDeployment is checkDeployment for a Deployment Flux applied or
// health-checks. A ProgressDeadlineExceeded from an earlier rollout is
// Progressing, as for the resource adapter: the ReplicaSet it names tells
// (see deadlineOfReplicaSet), or else the condition counts only when set
// after the health check started (opts.Since). The flux adapter does not
// record when the pod template first ran the Bundle images
// (TargetUpdatedAt), so that time check is the only one (see
// deadlineFromEarlierRollout). As for the resource adapter, a stall of the
// current ReplicaSet fails at once only for an image Bundle
// (opts.ImagesOnly) whose images the pod template runs.
func (a *FluxAdapter) checkFluxDeployment(ctx context.Context, d *appsv1.Deployment, opts CheckOptions) HealthStatus {
	running := deploymentImages(d)
	imagesOK, imageNote := checkImages(opts.ExpectedImages, running)
	promoted := opts.ImagesOnly && imagesOK && runsRepository(opts.ExpectedImages, running)
	return deploymentRollout(d, string(appsv1.DeploymentAvailable), imagesOK, imageNote, false, promoted,
		CheckOptions{Since: opts.Since}, replicaSetLookup(ctx, a.dynamic))
}

// fluxStalledResource matches a resource Flux lists in a "failed early due
// to stalled resources" message: <kind>/<namespace>/<name> status: 'Failed'
// (fluxcd/pkg ssa, WaitForSetWithContext).
var fluxStalledResource = regexp.MustCompile(`([A-Za-z0-9]+)/(?:([a-z0-9.-]+)/)?([a-z0-9.-]+) status: 'Failed'`)

// stalledEarlier explains why Flux's "failed early due to stalled resources"
// (msg) is not a stall of this promotion, or returns "" when it may be.
// Flux fails a Deployment on any ProgressDeadlineExceeded, also one the
// Deployment controller kept from an earlier rollout after a rollback to the
// ReplicaSet before a stalled one (see deadlineOfReplicaSet), and checks
// again only at its next reconcile. So the stall is not this promotion's
// when every resource Flux lists is a Deployment, in this cluster, whose
// ProgressDeadlineExceeded is from an earlier rollout or that is no longer
// past its progress deadline. A resource of
// another kind, one that cannot be read, or a message that names none, is.
func (a *FluxAdapter) stalledEarlier(ctx context.Context, ks *unstructured.Unstructured, msg string, opts CheckOptions) string {
	if _, remote, _ := unstructured.NestedMap(ks.Object, "spec", "kubeConfig"); remote {
		return ""
	}
	listed := fluxStalledResource.FindAllStringSubmatch(msg, -1)
	if len(listed) == 0 {
		return ""
	}
	var now []string
	for _, m := range listed {
		if m[1] != "Deployment" || m[2] == "" {
			return ""
		}
		d, err := getDeployment(ctx, a.dynamic, m[2], m[3])
		if err != nil {
			return ""
		}
		st := a.checkFluxDeployment(ctx, d, opts)
		if st.Terminal {
			return ""
		}
		if st.Healthy {
			st.Reason = fmt.Sprintf("Deployment %s/%s: %s", m[2], m[3], st.Reason)
		}
		now = append(now, st.Reason)
	}
	return "no Deployment Flux lists is past a progress deadline of this promotion's rollout: " + strings.Join(now, "; ")
}

// fluxDeploymentRefs lists the Deployments in the Kustomization's inventory
// and spec.healthChecks, without duplicates.
func fluxDeploymentRefs(ks *unstructured.Unstructured) []types.NamespacedName {
	var refs []types.NamespacedName
	seen := map[types.NamespacedName]bool{}
	add := func(ns, name string) {
		ref := types.NamespacedName{Namespace: ns, Name: name}
		if name != "" && !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	entries, _, _ := unstructured.NestedSlice(ks.Object, "status", "inventory", "entries")
	for _, e := range entries {
		m, _ := e.(map[string]interface{})
		id, _ := m["id"].(string)
		// <namespace>_<name>_<group>_<kind>; names never contain "_".
		if parts := strings.Split(id, "_"); len(parts) == 4 && parts[2] == "apps" && parts[3] == "Deployment" {
			add(parts[0], parts[1])
		}
	}
	checks, _, _ := unstructured.NestedSlice(ks.Object, "spec", "healthChecks")
	for _, c := range checks {
		m, _ := c.(map[string]interface{})
		apiVersion, _ := m["apiVersion"].(string)
		kind, _ := m["kind"].(string)
		name, _ := m["name"].(string)
		ns, _ := m["namespace"].(string)
		if ns == "" {
			ns = ks.GetNamespace()
		}
		if kind == "Deployment" && (apiVersion == "" || apiVersion == "apps/v1") {
			add(ns, name)
		}
	}
	return refs
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

// ArgoRolloutsAdapter checks that an Argo Rollouts Rollout finished rolling
// out the promoted revision (see Check).
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

// Check reports the Rollout healthy only once it runs the promoted revision:
//
//  1. the pod template (spec.template, or the Deployment named by
//     spec.workloadRef) runs the Bundle images (ExpectedImages), else
//     Progressing: the GitOps tool has not applied the change yet;
//  2. status.observedGeneration is metadata.generation (and, with a
//     workloadRef, status.workloadObservedGeneration is the Deployment's
//     generation), else Progressing: the phase describes an older spec;
//  3. status.phase: Healthy with status.stableRS == status.currentPodHash is
//     Healthy, Degraded is unhealthy, any other phase is Progressing.
//
// Without the first two checks, the Healthy or Degraded phase of the previous
// revision would decide the new one's health.
func (a *ArgoRolloutsAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.ArgoRollouts
	if cfg.Namespace == "" {
		// OptionsForEnv always sets it.
		return HealthStatus{}, fmt.Errorf("argoRollouts health: Rollout %q has no namespace", cfg.Name)
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
	id := fmt.Sprintf("Rollout %s/%s", cfg.Namespace, cfg.Name)

	running, workload, err := a.rolloutImages(ctx, rollout)
	if apierrors.IsNotFound(err) {
		return unhealthy(fmt.Sprintf("%s: workloadRef %v", id, err)), nil
	}
	if err != nil {
		return HealthStatus{}, err
	}
	imagesOK, imageNote := checkImages(opts.ExpectedImages, running)
	if !imagesOK {
		return progressing(fmt.Sprintf("%s not updated yet: %s (%s)", id, imageNote, reason)), nil
	}

	observed, known := observedGeneration(rollout.Object, "status", "observedGeneration")
	switch {
	case !known:
		return progressing(fmt.Sprintf("%s: status.observedGeneration not set: the Argo Rollouts controller has not reconciled it yet (%s)",
			id, reason)), nil
	case observed != rollout.GetGeneration():
		return progressing(fmt.Sprintf("%s: waiting for the Argo Rollouts controller to observe generation %d (observed %d; %s)",
			id, rollout.GetGeneration(), observed, reason)), nil
	}
	if workload != nil {
		observed, known := observedGeneration(rollout.Object, "status", "workloadObservedGeneration")
		if !known || observed != workload.Generation {
			return progressing(fmt.Sprintf("%s: waiting for the Argo Rollouts controller to observe generation %d of Deployment %s/%s (%s)",
				id, workload.Generation, workload.Namespace, workload.Name, reason)), nil
		}
	}

	switch phase {
	case "Healthy":
		stable, _, _ := unstructured.NestedString(rollout.Object, "status", "stableRS")
		current, _, _ := unstructured.NestedString(rollout.Object, "status", "currentPodHash")
		if current != "" && stable != current {
			return progressing(fmt.Sprintf("%s: stable ReplicaSet %q is not the current pod template hash %q yet (%s)",
				id, stable, current, reason)), nil
		}
		if !runsRepository(opts.ExpectedImages, running) {
			// The images cannot tell the promoted revision from the previous
			// one (#1422): the phase counts only when set after this check
			// started.
			stale, note := healthySetBefore(rollout, opts.Since)
			if stale != "" {
				return progressing(fmt.Sprintf("%s: Rollout phase: Healthy is for an earlier release: %s; "+
					"waiting for Argo Rollouts to roll out the change", id, stale)), nil
			}
			if note != "" {
				reason += " " + note
			}
		}
		if imageNote != "" {
			reason += " " + imageNote
		}
		return healthy(reason), nil
	case "Degraded":
		return unhealthy(reason), nil
	default: // Progressing, Paused, or not reported yet
		return progressing(reason), nil
	}
}

// healthySetBefore explains why the Rollout's Healthy phase predates since,
// the start of this health check, or returns "" when it does not (or since
// is zero: the promotion changed nothing in git). Argo Rollouts sets its
// Healthy condition False when it starts rolling out a new revision and True
// again once the revision is healthy, so the condition's lastTransitionTime
// is when the current revision became healthy. Both times are compared in
// whole seconds. Without the condition (Argo Rollouts before v1.0) the phase
// decides, with a note.
func healthySetBefore(rollout *unstructured.Unstructured, since time.Time) (stale, note string) {
	if since.IsZero() {
		return "", ""
	}
	conditions, _, _ := unstructured.NestedSlice(rollout.Object, "status", "conditions")
	cond := findCondition(conditions, "Healthy")
	if cond == nil {
		return "", "(no Healthy condition: cannot tell whether the phase is from this release)"
	}
	lt, _ := cond["lastTransitionTime"].(string)
	at, err := time.Parse(time.RFC3339, lt)
	if err != nil {
		return fmt.Sprintf("its Healthy condition's lastTransitionTime %q is not a time", lt), ""
	}
	start := since.Truncate(time.Second)
	if at.Before(start) {
		return fmt.Sprintf("its Healthy condition's lastTransitionTime %s is before this health check started (%s)",
			at.UTC().Format(time.RFC3339), start.UTC().Format(time.RFC3339)), ""
	}
	return "", ""
}

// rolloutImages returns the images of the Rollout's pod template. A Rollout
// with spec.workloadRef takes its template from that Deployment, which is
// returned too; only a Deployment workloadRef is read.
func (a *ArgoRolloutsAdapter) rolloutImages(ctx context.Context, rollout *unstructured.Unstructured) (
	[]string, *appsv1.Deployment, error) {
	ref, hasRef, _ := unstructured.NestedMap(rollout.Object, "spec", "workloadRef")
	if !hasRef {
		return containerImages(rollout.Object, "spec", "template", "spec"), nil, nil
	}
	kind, _ := ref["kind"].(string)
	name, _ := ref["name"].(string)
	if kind != "Deployment" || name == "" {
		return nil, nil, nil
	}
	d, err := getDeployment(ctx, a.dynamic, rollout.GetNamespace(), name)
	if err != nil {
		return nil, nil, err
	}
	var images []string
	for _, c := range d.Spec.Template.Spec.Containers {
		images = append(images, c.Image)
	}
	return images, d, nil
}

// containerImages lists the container images of the pod spec at fields.
func containerImages(obj map[string]interface{}, fields ...string) []string {
	containers, _, _ := unstructured.NestedSlice(obj, append(fields, "containers")...)
	var images []string
	for _, c := range containers {
		m, _ := c.(map[string]interface{})
		if img, _ := m["image"].(string); img != "" {
			images = append(images, img)
		}
	}
	return images
}

// observedGeneration reads a generation that Argo Rollouts writes as a string
// ("4"); an integer is accepted too. A value that is not a number (Argo
// Rollouts before v1.0 wrote a hash) counts as observed, as Argo Rollouts
// itself treats it; a missing field does not.
func observedGeneration(obj map[string]interface{}, fields ...string) (int64, bool) {
	v, found, _ := unstructured.NestedFieldNoCopy(obj, fields...)
	if !found {
		return 0, false
	}
	gen, _, _ := unstructured.NestedInt64(obj, "metadata", "generation")
	switch x := v.(type) {
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return gen, true
		}
		return n, true
	case int64:
		return x, true
	case float64:
		return int64(x), true
	}
	return gen, true
}

var deploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

// getDeployment reads a Deployment through the dynamic client.
func getDeployment(ctx context.Context, dyn dynamic.Interface, namespace, name string) (*appsv1.Deployment, error) {
	u, err := dyn.Resource(deploymentGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	var d appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &d); err != nil {
		return nil, fmt.Errorf("convert deployment %s/%s: %w", namespace, name, err)
	}
	return &d, nil
}

// --- FlaggerAdapter ---

// FlaggerAdapter checks that a Flagger Canary analyzed and promoted the
// promoted revision (see Check).
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

// Check reports the Canary healthy only once Flagger promoted the promoted
// revision. Flagger keeps the phase of its last analysis (Succeeded or
// Failed) until an analysis tick notices that the target changed, so right
// after the GitOps tool applies a change the phase describes the previous
// release.
//
//  1. The target Deployment (spec.targetRef) must run the Bundle images
//     (ExpectedImages), else Progressing: the change is not applied yet.
//  2. Succeeded is Healthy when the primary Deployment (<target>-primary), to
//     which Flagger copies a revision it promotes, runs the Bundle images and
//     is rolled out and Available. A primary on other images means the phase
//     is from an earlier release: Progressing.
//  3. Failed is Terminal (Flagger rolled the canary back) unless it is from
//     an earlier release: Healthy when the primary runs the Bundle images (the
//     Bundle is the revision Flagger last promoted, which it does not analyze
//     again), Progressing when Flagger set the phase before Since (see
//     phaseSetAt) or, with Since set, not after TargetUpdatedAt. The previous
//     release can fail after this health check started but before the GitOps
//     tool applied the Bundle; Flagger notices a new target before it rolls
//     back, so a Failed set once the target ran the Bundle is about the
//     Bundle. Until a check records TargetUpdatedAt, a Failed is Progressing.
//  4. When the images cannot be compared (a Bundle without images, a Bundle
//     image renamed by kustomize, a target that is not a Deployment),
//     Succeeded and Failed count only when Flagger set the phase at or after
//     Since.
//  5. Every other phase is Progressing.
func (a *FlaggerAdapter) Check(ctx context.Context, opts CheckOptions) (HealthStatus, error) {
	cfg := opts.Flagger
	if cfg.Namespace == "" {
		// OptionsForEnv always sets it.
		return HealthStatus{}, fmt.Errorf("flagger health: Canary %q has no namespace", cfg.Name)
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
	conditions, _, _ := unstructured.NestedSlice(canary.Object, "status", "conditions")

	// Flagger explains the phase (for a failure, why it rolled back) in the
	// message of its Promoted condition.
	reason := fmt.Sprintf("Canary phase: %s", phase)
	cond := findCondition(conditions, "Promoted")
	if cond == nil {
		cond = findCondition(conditions, "Ready")
	}
	if msg, _ := cond["message"].(string); msg != "" {
		reason += " — " + msg
	}

	rev, err := a.canaryRevision(ctx, canary, opts.ExpectedImages)
	if err != nil {
		return HealthStatus{}, err
	}
	if rev.status != nil {
		return *rev.status, nil
	}
	st := flaggerPhaseHealth(canary, phase, reason, rev, opts)
	st.TargetUpdated = rev.targetUpdated
	return st, nil
}

// flaggerPhaseHealth is steps 2 to 5 of FlaggerAdapter.Check, for a Canary
// whose target is not known to run other images than the Bundle's.
func flaggerPhaseHealth(canary *unstructured.Unstructured, phase, reason string,
	rev canaryRevision, opts CheckOptions) HealthStatus {
	conditions, _, _ := unstructured.NestedSlice(canary.Object, "status", "conditions")
	// stale explains why a Succeeded or Failed phase predates this health
	// check; staleFailed why a Failed phase predates the Bundle on the target.
	stale, staleFailed := "", ""
	if !opts.Since.IsZero() {
		since := opts.Since.Truncate(time.Second)
		lt, field := phaseSetAt(canary, phase, findCondition(conditions, "Promoted"))
		at, perr := time.Parse(time.RFC3339, lt)
		switch {
		case perr != nil:
			stale = fmt.Sprintf("%s %q is not a time", field, lt)
		case at.Before(since):
			stale = fmt.Sprintf("its %s %s is before this health check started (%s)",
				field, at.UTC().Format(time.RFC3339), since.UTC().Format(time.RFC3339))
		case rev.targetUpdated && opts.TargetUpdatedAt.IsZero():
			staleFailed = "this check is the first to find the target running the Bundle images"
		case rev.targetUpdated && !at.After(opts.TargetUpdatedAt.Truncate(time.Second)):
			// Both times have whole seconds: one in the same second as the
			// target update may be the previous release's.
			staleFailed = fmt.Sprintf("its %s %s is not after the health check first found the target running the Bundle images (%s)",
				field, at.UTC().Format(time.RFC3339), opts.TargetUpdatedAt.UTC().Format(time.RFC3339))
		}
	}
	const wait = "waiting for Flagger to analyze the new revision"

	switch phase {
	case "Succeeded":
		if rev.primary != nil {
			if !rev.primaryRunsBundle {
				return progressing(fmt.Sprintf("Canary phase: Succeeded is for an earlier release: %s; %s", rev.primaryNote, wait))
			}
			return rev.primaryHealth("Canary phase: Succeeded")
		}
		if stale != "" {
			return progressing(fmt.Sprintf("Canary phase: Succeeded is for an earlier release: %s; %s", stale, wait))
		}
		if rev.note != "" {
			return healthy("Canary phase: Succeeded " + rev.note)
		}
		return healthy("Canary phase: Succeeded")
	case "Failed":
		if rev.primary != nil && rev.primaryRunsBundle {
			return rev.primaryHealth("Canary phase: Failed is for an earlier release: the Bundle is the revision Flagger last promoted")
		}
		if stale == "" {
			stale = staleFailed
		}
		if stale != "" {
			return progressing(fmt.Sprintf("Canary phase: Failed is for an earlier release: %s; %s", stale, wait))
		}
		// Flagger rolled the canary back; waiting will not make it succeed.
		return terminal(reason)
	default: // Initializing, Initialized, Waiting, Progressing, WaitingPromotion, Promoting, Finalising
		return progressing(reason)
	}
}

// phaseSetAt is when Flagger set the Canary's current phase, and the field
// that says so. Flagger rewrites status.lastTransitionTime at every analysis
// tick of a Failed Canary (it syncs status.lastAppliedSpec), so a Failed
// phase from an earlier release looks new there. The Promoted condition is
// steady: its reason is the phase, and Flagger sets its lastUpdateTime only
// when the reason or status changes. Without a Promoted condition for the
// phase (an older Flagger), the time is status.lastTransitionTime.
func phaseSetAt(canary *unstructured.Unstructured, phase string, promoted map[string]interface{}) (string, string) {
	if reason, _ := promoted["reason"].(string); reason != "" && reason == phase {
		if at, _ := promoted["lastUpdateTime"].(string); at != "" {
			return at, "Promoted condition's lastUpdateTime"
		}
	}
	lt, _, _ := unstructured.NestedString(canary.Object, "status", "lastTransitionTime")
	return lt, "status.lastTransitionTime"
}

// canaryRevision is what the Canary's Deployments say about the revision
// Flagger works on.
type canaryRevision struct {
	// status, when set, is the result: the target is missing or does not run
	// the Bundle images yet.
	status *HealthStatus
	// primary is the primary Deployment when its images can be compared with
	// the Bundle's; nil otherwise.
	primary *appsv1.Deployment
	// primaryRunsBundle reports whether primary runs the Bundle images;
	// primaryNote says what it runs instead.
	primaryRunsBundle bool
	primaryNote       string
	// note says why the images could not be compared.
	note string
	// targetUpdated reports whether the images of the target were compared
	// with the Bundle's and the target runs them.
	targetUpdated bool
}

// primaryHealth is the health of a primary Deployment that runs the Bundle
// images, prefixed with what the phase says.
func (r canaryRevision) primaryHealth(prefix string) HealthStatus {
	st := checkDeployment(r.primary, "Available", CheckOptions{}, nil, nil)
	id := fmt.Sprintf("primary Deployment %s/%s", r.primary.Namespace, r.primary.Name)
	if st.Healthy {
		return healthy(fmt.Sprintf("%s; %s runs the Bundle images: %s", prefix, id, st.Reason))
	}
	st.Reason = fmt.Sprintf("%s; %s", prefix, st.Reason)
	return st
}

// canaryRevision reads the Canary's target Deployment and its primary.
func (a *FlaggerAdapter) canaryRevision(ctx context.Context, canary *unstructured.Unstructured,
	expected []ImageExpectation) (canaryRevision, error) {
	ns := canary.GetNamespace()
	kind, _, _ := unstructured.NestedString(canary.Object, "spec", "targetRef", "kind")
	name, _, _ := unstructured.NestedString(canary.Object, "spec", "targetRef", "name")
	switch {
	case len(expected) == 0:
		return canaryRevision{}, nil
	case name == "":
		return canaryRevision{note: "(image not verified: the Canary has no spec.targetRef)"}, nil
	case kind != "" && kind != "Deployment":
		return canaryRevision{note: fmt.Sprintf("(image not verified: the Canary target is a %s)", kind)}, nil
	}

	target, err := getDeployment(ctx, a.dynamic, ns, name)
	if apierrors.IsNotFound(err) {
		st := unhealthy(fmt.Sprintf("Canary %s/%s: target Deployment %s/%s not found", ns, canary.GetName(), ns, name))
		return canaryRevision{status: &st}, nil
	}
	if err != nil {
		return canaryRevision{}, err
	}
	var images []string
	for _, c := range target.Spec.Template.Spec.Containers {
		images = append(images, c.Image)
	}
	ok, note := checkImages(expected, images)
	if !ok {
		st := progressing(fmt.Sprintf("Canary %s/%s: target Deployment %s/%s not updated yet: %s",
			ns, canary.GetName(), ns, name, note))
		return canaryRevision{status: &st}, nil
	}
	if note != "" {
		return canaryRevision{note: note}, nil
	}

	primary, err := getDeployment(ctx, a.dynamic, ns, name+"-primary")
	if apierrors.IsNotFound(err) {
		// Flagger has not initialized the Canary yet.
		return canaryRevision{targetUpdated: true,
			note: fmt.Sprintf("(image not verified: no primary Deployment %s/%s-primary)", ns, name)}, nil
	}
	if err != nil {
		return canaryRevision{}, err
	}
	images = images[:0]
	for _, c := range primary.Spec.Template.Spec.Containers {
		images = append(images, c.Image)
	}
	ok, note = checkImages(expected, images)
	rev := canaryRevision{targetUpdated: true, primary: primary, primaryRunsBundle: ok && note == ""}
	if !ok {
		rev.primaryNote = fmt.Sprintf("primary Deployment %s/%s %s", ns, primary.Name, note)
	} else if note != "" {
		rev.primaryNote = fmt.Sprintf("primary Deployment %s/%s %s", ns, primary.Name, note)
	}
	return rev, nil
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
		return NewDeploymentAdapter(d.k8s, d.dynamic), nil
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
