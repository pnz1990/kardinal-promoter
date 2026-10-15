// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

var (
	applicationGVR   = schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	kustomizationGVR = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	gitRepositoryGVR = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
)

// refreshCase is a HealthChecking step "step" of Pipeline "p" with the
// environment env, reconciled by a Reconciler whose health adapters read
// dyn.
type refreshCase struct {
	env      v1alpha1.EnvironmentSpec
	status   v1alpha1.PromotionStepStatus
	prsRef   string
	objs     []client.Object
	dynObjs  []runtime.Object
	disabled bool
	remote   *health.RemoteClusters
	scope    health.RefreshScope
	// statusPatch, when set, intercepts the PromotionStep status patches.
	statusPatch func(n int) error
}

type refreshRig struct {
	c   client.Client
	dyn *dynfake.FakeDynamicClient
	r   *promotionstep.Reconciler
	rec *events.FakeRecorder
}

func (rc refreshCase) rig(t *testing.T) *refreshRig {
	t.Helper()
	pipeline := makePipeline("p")
	pipeline.Spec.Environments = []v1alpha1.EnvironmentSpec{rc.env}
	ps := labelled(makeStep("step", "p", "b1", rc.env.Name))
	ps.Spec.PRStatusRef = rc.prsRef
	ps.Status = rc.status
	if ps.Status.Steps == nil {
		ps.Status.Steps = recordedSteps(rc.env.Approval)
	}
	ps.Status.State = "HealthChecking"
	c := newClient(t, append([]client.Object{pipeline, makeBundle("b1", "p"), ps}, rc.objs...)...)
	reader := c
	if rc.statusPatch != nil {
		n := 0
		reader = interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, cl client.Client, sub string, obj client.Object,
				patch client.Patch, opts ...client.SubResourcePatchOption) error {
				n++
				if err := rc.statusPatch(n); err != nil {
					return err
				}
				return cl.SubResource(sub).Patch(ctx, obj, patch, opts...)
			}})
	}
	dyn := dynfake.NewSimpleDynamicClient(runtime.NewScheme(), rc.dynObjs...)
	rec := events.NewFakeRecorder(20)
	r := &promotionstep.Reconciler{Client: reader, SCM: &mockSCM{}, GitClient: &mockGit{},
		HealthDetector: health.NewAutoDetector(c, dyn), Recorder: rec, RefreshDisabled: rc.disabled, RemoteClusters: rc.remote,
		RefreshScope: rc.scope,
		NowFn:        func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 500, time.UTC) }}
	return &refreshRig{c: c, dyn: dyn, r: r, rec: rec}
}

func (g *refreshRig) reconcile(t *testing.T) error {
	t.Helper()
	_, err := g.r.Reconcile(context.Background(), reqFor("step"))
	return err
}

// patches lists the dynamic patches sent, as "resource ns/name".
func (g *refreshRig) patches() []string {
	var out []string
	for _, a := range g.dyn.Actions() {
		if p, ok := a.(k8stesting.PatchAction); ok {
			out = append(out, p.GetResource().Resource+" "+p.GetNamespace()+"/"+p.GetName())
		}
	}
	return out
}

func (g *refreshRig) annotation(t *testing.T, gvr schema.GroupVersionResource, ns, name, key string) string {
	t.Helper()
	obj, err := g.dyn.Resource(gvr).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	return obj.GetAnnotations()[key]
}

func (g *refreshRig) events() []string {
	var out []string
	for {
		select {
		case e := <-g.rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func fluxKustomizationFrom(ns, name, sourceKind, sourceName, sourceNS string) *unstructured.Unstructured {
	ks := fluxKustomization(name, oldSHA)
	ks.SetNamespace(ns)
	ref := map[string]interface{}{"kind": sourceKind, "name": sourceName}
	if sourceNS != "" {
		ref["namespace"] = sourceNS
	}
	ks.Object["spec"] = map[string]interface{}{"sourceRef": ref, "targetNamespace": "default"}
	return ks
}

func gitRepository(ns, name string) *unstructured.Unstructured {
	return unstructuredObj("source.toolkit.fluxcd.io/v1", "GitRepository", ns, name, map[string]interface{}{})
}

var pushed = v1alpha1.PromotionStepStatus{Outputs: map[string]string{"commitSHA": newSHA}}

// TestRefreshRequestedOnce proves that a step that pushed a commit asks the
// GitOps object its health check names to sync it, records the request on
// its own status and does not ask again on later reconciles.
func TestRefreshRequestedOnce(t *testing.T) {
	argo := v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "argocd"}}
	flux := v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "flux"}}
	at := "2026-10-11T12:00:00Z"
	tests := []struct {
		name        string
		rc          refreshCase
		wantPatches []string
		wantAnn     func(t *testing.T, g *refreshRig)
		wantMsg     string
	}{
		{name: "argocd Application",
			rc:          refreshCase{env: argo, status: pushed, dynObjs: []runtime.Object{argoApplication("p-test", oldSHA)}},
			wantPatches: []string{"applications argocd/p-test"},
			wantAnn: func(t *testing.T, g *refreshRig) {
				assert.Equal(t, "normal", g.annotation(t, applicationGVR, "argocd", "p-test", health.ArgoCDRefreshAnnotation))
			},
			wantMsg: "refresh requested: Application argocd/p-test"},
		{name: "flux Kustomization and its GitRepository, source first",
			rc: refreshCase{env: flux, status: pushed, dynObjs: []runtime.Object{
				fluxKustomizationFrom("flux-system", "p-test", "GitRepository", "repo", ""), gitRepository("flux-system", "repo")}},
			wantPatches: []string{"gitrepositories flux-system/repo", "kustomizations flux-system/p-test"},
			wantAnn: func(t *testing.T, g *refreshRig) {
				// The recorded time and the step: unique per step (fan-out).
				assert.Equal(t, at+" default/step", g.annotation(t, gitRepositoryGVR, "flux-system", "repo", health.FluxRequestedAtAnnotation))
				assert.Equal(t, at+" default/step", g.annotation(t, kustomizationGVR, "flux-system", "p-test", health.FluxRequestedAtAnnotation))
			},
			wantMsg: "refresh requested: GitRepository flux-system/repo, Kustomization flux-system/p-test"},
		{name: "flux source in another namespace, admitted by a rule",
			rc: refreshCase{env: flux, status: pushed, scope: mustScope(t, "default=sources"), dynObjs: []runtime.Object{
				fluxKustomizationFrom("flux-system", "p-test", "GitRepository", "repo", "sources"), gitRepository("sources", "repo")}},
			wantPatches: []string{"gitrepositories sources/repo", "kustomizations flux-system/p-test"},
			wantMsg:     "GitRepository sources/repo"},
		{name: "flux OCIRepository source: only the Kustomization",
			rc: refreshCase{env: flux, status: pushed, dynObjs: []runtime.Object{
				fluxKustomizationFrom("flux-system", "p-test", "OCIRepository", "oci", "")}},
			wantPatches: []string{"kustomizations flux-system/p-test"},
			wantMsg:     "refresh requested: Kustomization flux-system/p-test"},
		{name: "merge commit from the PRStatus",
			rc: refreshCase{env: v1alpha1.EnvironmentSpec{Name: "prod", Approval: "pr-review", Health: v1alpha1.HealthConfig{Type: "argocd"}},
				prsRef: "prs", objs: []client.Object{mergedPRStatus(newSHA, false)},
				dynObjs: []runtime.Object{argoApplication("p-prod", oldSHA)}},
			wantPatches: []string{"applications argocd/p-prod"},
			wantMsg:     "refresh requested: Application argocd/p-prod"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := tt.rc.rig(t)
			require.NoError(t, g.reconcile(t))
			assert.Equal(t, tt.wantPatches, g.patches())
			if tt.wantAnn != nil {
				tt.wantAnn(t, g)
			}
			got := getStep(t, g.c, "step")
			require.NotNil(t, got.Status.RefreshRequestedAt)
			assert.Equal(t, at, got.Status.RefreshRequestedAt.UTC().Format(time.RFC3339))
			assert.Equal(t, newSHA, got.Status.RefreshRevision)
			assert.Contains(t, got.Status.RefreshMessage, tt.wantMsg)
			assert.Equal(t, "HealthChecking", got.Status.State)
			ev := g.events()
			require.Len(t, ev, 1)
			assert.Contains(t, ev[0], "Normal "+promotionstep.ReasonRefreshRequested)

			// Idempotent: later reconciles (the health check spacing is
			// skipped by clearing lastHealthCheckAt) send nothing more.
			for range 2 {
				got = getStep(t, g.c, "step")
				got.Status.LastHealthCheckAt = nil
				require.NoError(t, g.c.Status().Update(context.Background(), &got))
				require.NoError(t, g.reconcile(t))
			}
			assert.Equal(t, tt.wantPatches, g.patches())
			assert.Empty(t, g.events())
		})
	}
}

func mergedPRStatus(sha string, unavailable bool) *v1alpha1.PRStatus {
	prs := openPRStatus("prs", "org/repo", 42)
	prs.Status.Open, prs.Status.Merged, prs.Status.MergeCommitSHA = false, true, sha
	prs.Status.MergeCommitUnavailable = unavailable
	return prs
}

// TestRefreshNotRequested covers the cases that send nothing: refresh turned
// off by the controller or the environment, health types with no GitOps
// object, a step with no changes or no commit, a step that already asked,
// and a merge commit the PRStatus may still record.
func TestRefreshNotRequested(t *testing.T) {
	off := false
	argo := v1alpha1.HealthConfig{Type: "argocd"}
	app := []runtime.Object{argoApplication("p-test", oldSHA)}
	asked := pushed
	asked.RefreshRequestedAt = &metav1.Time{Time: time.Now()}
	tests := []struct {
		name string
		rc   refreshCase
	}{
		{name: "controller --health-refresh=false",
			rc: refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: argo}, status: pushed, dynObjs: app, disabled: true}},
		{name: "environment health.refresh: false",
			rc: refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "argocd", Refresh: &off}},
				status: pushed, dynObjs: app}},
		{name: "resource health type",
			rc: refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "resource"}},
				status: pushed, objs: []client.Object{healthyDeployment("p", "test")}}},
		{name: "already asked (a re-run after a crash once the request was recorded)",
			rc: refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: argo}, status: asked, dynObjs: app}},
		{name: "no changes, and the Application already synced the head",
			rc: refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: argo},
				status: noChanges, dynObjs: []runtime.Object{argoApplication("p-test", newSHA)}}},
		{name: "no changes, health.refresh: false",
			rc: refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{Type: "argocd", Refresh: &off}},
				status: noChanges, dynObjs: app}},
		{name: "no changes and no head read",
			rc: refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Health: argo},
				status: v1alpha1.PromotionStepStatus{Outputs: map[string]string{"noChanges": "true"}}, dynObjs: app}},
		{name: "no commit and no PR (update.strategy argocd)",
			rc: refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: argo}, dynObjs: app}},
		{name: "merge commit not recorded yet",
			rc: refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Approval: "pr-review", Health: argo},
				prsRef: "prs", objs: []client.Object{mergedPRStatus("", false)}, dynObjs: app}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := tt.rc.rig(t)
			require.NoError(t, g.reconcile(t))
			assert.Empty(t, g.patches())
			got := getStep(t, g.c, "step")
			if tt.rc.status.RefreshRequestedAt == nil {
				assert.Nil(t, got.Status.RefreshRequestedAt)
			}
			for _, e := range g.events() {
				assert.NotContains(t, e, "GitOpsRefresh")
			}
		})
	}
}

// noChanges is a step whose git-commit found nothing to commit: it waits for
// the branch head it cloned, which another writer pushed (#1669).
var noChanges = v1alpha1.PromotionStepStatus{Outputs: map[string]string{"noChanges": "true", "commitSHA": newSHA}}

// TestRefreshNoChanges: a step with no changes asks the GitOps object to sync
// the branch head it waits for when the health check finds the object on
// another revision (#1910), once, and not when it already synced the head
// (TestRefreshNotRequested). Covers HEALTH-REFRESH-06.
func TestRefreshNoChanges(t *testing.T) {
	for _, tt := range []struct {
		name, healthType string
		dynObjs          []runtime.Object
		wantPatches      []string
	}{
		{name: "argocd", healthType: "argocd", dynObjs: []runtime.Object{argoApplication("p-test", oldSHA)},
			wantPatches: []string{"applications argocd/p-test"}},
		{name: "flux", healthType: "flux", dynObjs: []runtime.Object{
			fluxKustomizationFrom("flux-system", "p-test", "GitRepository", "repo", ""), gitRepository("flux-system", "repo")},
			wantPatches: []string{"gitrepositories flux-system/repo", "kustomizations flux-system/p-test"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			g := refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: tt.healthType}},
				status: noChanges, dynObjs: tt.dynObjs}.rig(t)
			require.NoError(t, g.reconcile(t))
			assert.Equal(t, tt.wantPatches, g.patches())
			got := getStep(t, g.c, "step")
			require.NotNil(t, got.Status.RefreshRequestedAt)
			assert.Equal(t, newSHA, got.Status.RefreshRevision)
			assert.Contains(t, got.Status.RefreshMessage, "refresh requested: ")
			assert.Equal(t, "HealthChecking", got.Status.State)
			assert.Contains(t, got.Status.Message, "waiting for "+tt.healthType)
			require.NotNil(t, got.Status.LastHealthCheckAt, "the health check result is recorded with the request")
			ev := g.events()
			require.Len(t, ev, 1)
			assert.Contains(t, ev[0], "Normal "+promotionstep.ReasonRefreshRequested)

			for range 2 {
				got = getStep(t, g.c, "step")
				got.Status.LastHealthCheckAt = nil
				require.NoError(t, g.c.Status().Update(context.Background(), &got))
				require.NoError(t, g.reconcile(t))
			}
			assert.Equal(t, tt.wantPatches, g.patches(), "asked once")
			assert.Empty(t, g.events())
		})
	}
}

// TestRefreshOutsidePipelineNamespace: a Pipeline has the controller refresh
// only an object in its own namespace or one that deploys there, or one a
// --health-refresh-namespaces rule admits (#1781). Another object is not
// patched: the step records why, emits a Warning and waits for the poll,
// without asking again. Covers HEALTH-REFRESH-07.
func TestRefreshOutsidePipelineNamespace(t *testing.T) {
	argo := v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "argocd"}}
	flux := v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "flux"}}
	elsewhere := func(dest string) runtime.Object {
		app := argoApplication("p-test", oldSHA)
		app.Object["spec"] = map[string]interface{}{"destination": map[string]interface{}{"namespace": dest}}
		return app
	}
	ownFlux := v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{
		Type: "flux", Flux: &v1alpha1.HealthTargetRef{Name: "p-test", Namespace: "default"}}}
	spokeApp := func(dest string) runtime.Object {
		app := argoApplication("p-test", oldSHA)
		app.Object["spec"] = map[string]interface{}{"destination": map[string]interface{}{
			"server": "https://spoke.example:6443", "namespace": dest}}
		return app
	}
	// destApp deploys to the Pipeline's namespace, default, of the cluster
	// that dest (destination.server or destination.name) names.
	destApp := func(key, value string) runtime.Object {
		app := argoApplication("p-test", oldSHA)
		app.Object["spec"] = map[string]interface{}{"destination": map[string]interface{}{key: value, "namespace": "default"}}
		return app
	}
	remoteKs := func() runtime.Object {
		ks := tenantKsIn("flux-system")
		spec := ks.Object["spec"].(map[string]interface{})
		spec["targetNamespace"], spec["kubeConfig"] = "default", map[string]interface{}{"secretRef": map[string]interface{}{"name": "spoke"}}
		return ks
	}
	tenantKs := fluxKustomizationFrom("team-b", "p-test", "GitRepository", "repo", "flux-system")
	tenantKs.Object["spec"].(map[string]interface{})["targetNamespace"] = "team-b"
	rule := func(r ...string) health.RefreshScope {
		s, err := health.ParseRefreshScope(r)
		require.NoError(t, err)
		return s
	}
	tests := []struct {
		name        string
		rc          refreshCase
		wantPatches []string
		wantMsg     string
	}{
		{name: "an Application that deploys to another namespace",
			rc: refreshCase{env: argo, status: pushed, dynObjs: []runtime.Object{elsewhere("team-b")}},
			wantMsg: "refresh skipped (Application argocd/p-test (deploys to team-b): outside the Pipeline's namespace default, " +
				"see --health-refresh-namespaces); waiting for Argo CD to poll git"},
		{name: "an Application with no destination namespace",
			rc:      refreshCase{env: argo, status: pushed, dynObjs: []runtime.Object{elsewhere("")}},
			wantMsg: "refresh skipped (Application argocd/p-test: outside the Pipeline's namespace default"},
		{name: "another tenant's Kustomization and its shared source",
			rc: refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{
				Type: "flux", Flux: &v1alpha1.HealthTargetRef{Name: "p-test", Namespace: "team-b"}}},
				status: pushed, dynObjs: []runtime.Object{tenantKs, gitRepository("flux-system", "repo")}},
			wantMsg: "refresh skipped (GitRepository flux-system/repo, Kustomization team-b/p-test (deploys to team-b): " +
				"outside the Pipeline's namespace default, see --health-refresh-namespaces); waiting for Flux to poll git"},
		{name: "a rule admits the Pipeline namespace's prefix",
			rc: refreshCase{env: argo, status: pushed, dynObjs: []runtime.Object{elsewhere("team-b")},
				scope: rule("default=team-*")},
			wantPatches: []string{"applications argocd/p-test"}, wantMsg: "refresh requested: Application argocd/p-test"},
		{name: "a rule for other Pipelines does not",
			rc: refreshCase{env: argo, status: pushed, dynObjs: []runtime.Object{elsewhere("team-b")},
				scope: rule("team-a=team-b")},
			wantMsg: "refresh skipped (Application argocd/p-test (deploys to team-b)"},
		{name: "a Kustomization in the Pipeline's namespace with its source there",
			rc: refreshCase{env: ownFlux, status: pushed, dynObjs: []runtime.Object{
				fluxKustomizationFrom("default", "p-test", "GitRepository", "repo", ""), gitRepository("default", "repo")}},
			wantPatches: []string{"gitrepositories default/repo", "kustomizations default/p-test"},
			wantMsg:     "refresh requested: GitRepository default/repo, Kustomization default/p-test"},
		{name: "another tenant's source, named by the Pipeline's own Kustomization",
			rc: refreshCase{env: ownFlux, status: pushed, dynObjs: []runtime.Object{
				fluxKustomizationFrom("default", "p-test", "GitRepository", "b-repo", "team-b"), gitRepository("team-b", "b-repo")}},
			wantMsg: "refresh skipped (GitRepository team-b/b-repo: outside the Pipeline's namespace default"},
		{name: "a shared flux-system source needs a rule",
			rc: refreshCase{env: ownFlux, status: pushed, dynObjs: []runtime.Object{
				fluxKustomizationFrom("default", "p-test", "GitRepository", "repo", "flux-system"), gitRepository("flux-system", "repo")}},
			wantMsg: "refresh skipped (GitRepository flux-system/repo: outside the Pipeline's namespace default"},
		{name: "a shared flux-system source admitted by default=flux-system",
			rc: refreshCase{env: ownFlux, status: pushed, scope: rule("default=flux-system"), dynObjs: []runtime.Object{
				fluxKustomizationFrom("default", "p-test", "GitRepository", "repo", "flux-system"), gitRepository("flux-system", "repo")}},
			wantPatches: []string{"gitrepositories flux-system/repo", "kustomizations default/p-test"},
			wantMsg:     "refresh requested: GitRepository flux-system/repo, Kustomization default/p-test"},
		{name: "an Application deploying to that namespace of a cluster named by destination.name",
			rc:      refreshCase{env: argo, status: pushed, dynObjs: []runtime.Object{destApp("name", "spoke")}},
			wantMsg: "refresh skipped (Application argocd/p-test: outside the Pipeline's namespace default"},
		{name: "destination.name in-cluster",
			rc:          refreshCase{env: argo, status: pushed, dynObjs: []runtime.Object{destApp("name", "in-cluster")}},
			wantPatches: []string{"applications argocd/p-test"}, wantMsg: "refresh requested: Application argocd/p-test"},
		{name: "destination.server https://kubernetes.default.svc/ (trailing slash)",
			rc:          refreshCase{env: argo, status: pushed, dynObjs: []runtime.Object{destApp("server", "https://kubernetes.default.svc/")}},
			wantPatches: []string{"applications argocd/p-test"}, wantMsg: "refresh requested: Application argocd/p-test"},
		{name: "an Application deploying to that namespace on another cluster",
			rc:      refreshCase{env: argo, status: pushed, dynObjs: []runtime.Object{spokeApp("default")}},
			wantMsg: "refresh skipped (Application argocd/p-test: outside the Pipeline's namespace default"},
		{name: "a Kustomization applying to that namespace on another cluster",
			rc:      refreshCase{env: flux, status: pushed, dynObjs: []runtime.Object{remoteKs()}},
			wantMsg: "refresh skipped (Kustomization flux-system/p-test: outside the Pipeline's namespace default"},
		{name: "*=* admits any object",
			rc:          refreshCase{env: flux, status: pushed, scope: rule("*=*"), dynObjs: []runtime.Object{tenantKsIn("flux-system")}},
			wantPatches: []string{"kustomizations flux-system/p-test"}, wantMsg: "refresh requested: Kustomization flux-system/p-test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := tt.rc.rig(t)
			require.NoError(t, g.reconcile(t))
			assert.Equal(t, tt.wantPatches, g.patches())
			got := getStep(t, g.c, "step")
			require.NotNil(t, got.Status.RefreshRequestedAt, "the one request is spent either way")
			assert.Contains(t, got.Status.RefreshMessage, tt.wantMsg)
			assert.Equal(t, "HealthChecking", got.Status.State, "a skipped refresh is not a health failure")
			assert.Zero(t, got.Status.ConsecutiveHealthFailures)
			ev := g.events()
			require.Len(t, ev, 1)
			if tt.wantPatches == nil {
				assert.Contains(t, ev[0], "Warning "+promotionstep.ReasonRefreshSkipped)
			} else {
				assert.Contains(t, ev[0], "Normal "+promotionstep.ReasonRefreshRequested)
			}

			got.Status.LastHealthCheckAt = nil
			require.NoError(t, g.c.Status().Update(context.Background(), &got))
			require.NoError(t, g.reconcile(t))
			assert.Equal(t, tt.wantPatches, g.patches(), "not asked again")
		})
	}
}

// mustScope parses refresh scope rules.
func mustScope(t *testing.T, rules ...string) health.RefreshScope {
	t.Helper()
	s, err := health.ParseRefreshScope(rules)
	require.NoError(t, err)
	return s
}

// tenantKsIn is a Kustomization p-test in ns with no source to refresh and no
// target namespace.
func tenantKsIn(ns string) *unstructured.Unstructured {
	ks := fluxKustomizationFrom(ns, "p-test", "OCIRepository", "oci", "")
	delete(ks.Object["spec"].(map[string]interface{}), "targetNamespace")
	return ks
}

// TestRefreshWithoutMergeCommit: a PR merged with no merge commit the SCM
// will report (status.mergeCommitUnavailable) is refreshed once, with no
// revision.
func TestRefreshWithoutMergeCommit(t *testing.T) {
	g := refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Approval: "pr-review", Health: v1alpha1.HealthConfig{Type: "argocd"}},
		prsRef: "prs", objs: []client.Object{mergedPRStatus("", true)},
		dynObjs: []runtime.Object{argoApplication("p-test", oldSHA)}}.rig(t)
	require.NoError(t, g.reconcile(t))
	assert.Equal(t, []string{"applications argocd/p-test"}, g.patches())
	got := getStep(t, g.c, "step")
	require.NotNil(t, got.Status.RefreshRequestedAt)
	assert.Empty(t, got.Status.RefreshRevision)
}

// TestRefreshFailureFallsBackToPolling proves a failed request is not a
// failure: the step records why, emits a Warning Event, keeps checking
// health and passes once the tool syncs on its own poll, without asking
// again.
func TestRefreshFailureFallsBackToPolling(t *testing.T) {
	argo := v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "argocd"}}
	flux := v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "flux"}}
	tests := []struct {
		name    string
		rc      refreshCase
		forbid  bool
		wantMsg string
		synced  func(g *refreshRig)
	}{
		{name: "patch forbidden (no RBAC)", forbid: true,
			rc:      refreshCase{env: argo, status: pushed, dynObjs: []runtime.Object{argoApplication("p-test", oldSHA)}},
			wantMsg: "refresh request failed (annotate Application argocd/p-test: forbidden (HTTP 403))",
			synced: func(g *refreshRig) {
				_, _ = g.dyn.Resource(applicationGVR).Namespace("argocd").Update(context.Background(),
					argoApplication("p-test", newSHA), metav1.UpdateOptions{})
			}},
		{name: "Kustomization missing",
			rc:      refreshCase{env: flux, status: pushed},
			wantMsg: "refresh request failed (get Kustomization flux-system/p-test: not found (HTTP 404))"},
		{name: "GitRepository missing: the Kustomization is still annotated",
			rc: refreshCase{env: flux, status: pushed, dynObjs: []runtime.Object{
				fluxKustomizationFrom("flux-system", "p-test", "GitRepository", "gone", "")}},
			wantMsg: "annotate GitRepository flux-system/gone: not found (HTTP 404)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := tt.rc.rig(t)
			if tt.forbid {
				g.dyn.PrependReactor("patch", "applications", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(applicationGVR.GroupResource(), "p-test",
						errors.New("SECRET-ANSWER from https://10.1.2.3:6443"))
				})
			}
			require.NoError(t, g.reconcile(t))
			got := getStep(t, g.c, "step")
			require.NotNil(t, got.Status.RefreshRequestedAt)
			assert.Contains(t, got.Status.RefreshMessage, tt.wantMsg)
			assert.Contains(t, got.Status.RefreshMessage, "waiting for")
			assert.NotContains(t, got.Status.RefreshMessage, "SECRET-ANSWER", "the server's answer is not copied")
			assert.NotContains(t, got.Status.RefreshMessage, "10.1.2.3", "no address in status")
			assert.Equal(t, "HealthChecking", got.Status.State)
			if len(tt.rc.dynObjs) > 0 {
				// The refresh failure counts nothing (a missing
				// Kustomization does fail its health check).
				assert.Zero(t, got.Status.ConsecutiveHealthFailures)
			}
			ev := g.events()
			require.Len(t, ev, 1)
			assert.True(t, strings.HasPrefix(ev[0], "Warning "+promotionstep.ReasonRefreshFailed), ev[0])
			assert.NotContains(t, ev[0], "SECRET-ANSWER")
			assert.NotContains(t, ev[0], "10.1.2.3")

			if tt.synced == nil {
				return
			}
			n := len(g.patches())
			tt.synced(g)
			got.Status.LastHealthCheckAt = nil
			require.NoError(t, g.c.Status().Update(context.Background(), &got))
			require.NoError(t, g.reconcile(t))
			got = getStep(t, g.c, "step")
			assert.Equal(t, "Verified", got.Status.State, got.Status.Message)
			assert.Len(t, g.patches(), n, "no second request")
		})
	}
}

// TestRefreshCrashBeforeRecord: when the status patch that records the
// request fails (a crash or an API error before it), no annotation is sent;
// the re-run records and sends it once.
func TestRefreshCrashBeforeRecord(t *testing.T) {
	g := refreshCase{env: v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{Type: "argocd"}},
		status: pushed, dynObjs: []runtime.Object{argoApplication("p-test", oldSHA)},
		statusPatch: func(n int) error {
			if n == 1 {
				return errors.New("etcdserver: request timed out")
			}
			return nil
		}}.rig(t)
	require.Error(t, g.reconcile(t))
	assert.Empty(t, g.patches())
	assert.Nil(t, getStep(t, g.c, "step").Status.RefreshRequestedAt)

	require.NoError(t, g.reconcile(t))
	assert.Equal(t, []string{"applications argocd/p-test"}, g.patches())
	require.NotNil(t, getStep(t, g.c, "step").Status.RefreshRequestedAt)
}

// fakeRemoteArgoCD serves the Application argocd/custom as a remote API
// server would, and records the bodies of the PATCH requests. A PATCH is
// answered 403 with an answer that must not reach status when deny is set.
func fakeRemoteArgoCD(t *testing.T, deny bool) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var patches []string
	// It deploys to a namespace of the remote cluster that is not the
	// Pipeline's: the refresh scope (#1781) must not apply to the
	// kubeconfig's identity.
	app := argoApplication("custom", oldSHA)
	app.Object["spec"] = map[string]interface{}{"destination": map[string]interface{}{"namespace": "spoke-ns"}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer remote-token" ||
			r.URL.Path != "/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/custom" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			patches = append(patches, string(body))
			mu.Unlock()
			if deny {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403,` +
					`"message":"SECRET-BODY: user remote-sa cannot patch at https://10.9.8.7:6443"}`))
				return
			}
		}
		_ = json.NewEncoder(w).Encode(app.Object)
	}))
	t.Cleanup(srv.Close)
	remoteCA = base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	return srv, &patches
}

// TestRefreshRemoteCluster: with health.kubeconfigSecretRef the request goes
// to the cluster the health check reads, with the kubeconfig's identity,
// never to the controller's cluster; a refusal there is recorded classified,
// without the remote server's answer or address. Covers HEALTH-REFRESH-05.
func TestRefreshRemoteCluster(t *testing.T) {
	env := v1alpha1.EnvironmentSpec{Name: "test", Approval: "auto", Health: v1alpha1.HealthConfig{
		Type: "argocd", ArgoCD: &v1alpha1.HealthTargetRef{Name: "custom"},
		KubeconfigSecretRef: &v1alpha1.KubeconfigSecretRef{Name: "spoke"}}}
	for _, deny := range []bool{false, true} {
		t.Run(fmt.Sprintf("deny=%t", deny), func(t *testing.T) {
			srv, patches := fakeRemoteArgoCD(t, deny)
			g := refreshCase{env: env, status: pushed, remote: &health.RemoteClusters{Dial: loopbackDial},
				objs: []client.Object{kubeconfigSecret("spoke", remoteKubeconfig(srv.URL, ""))},
				// The controller's cluster has an Application of the same name:
				// it must not be touched. The remote Application deploys to
				// spoke-ns, outside the Pipeline's namespace: the refresh
				// scope (#1781) does not apply to the kubeconfig's identity.
				dynObjs: []runtime.Object{argoApplication("custom", oldSHA)}}.rig(t)
			require.NoError(t, g.reconcile(t))
			assert.Empty(t, g.patches(), "nothing is sent to the controller's cluster")
			require.Len(t, *patches, 1, "one request to the remote cluster")
			assert.JSONEq(t, `{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"normal"}}}`, (*patches)[0])
			got := getStep(t, g.c, "step")
			require.NotNil(t, got.Status.RefreshRequestedAt)
			ev := g.events()
			require.Len(t, ev, 1)
			if !deny {
				assert.Equal(t, "refresh requested: Application argocd/custom", got.Status.RefreshMessage)
				assert.Contains(t, ev[0], "Normal "+promotionstep.ReasonRefreshRequested)
				return
			}
			assert.Equal(t, "refresh request failed (annotate Application argocd/custom: forbidden (HTTP 403)); "+
				"waiting for Argo CD to poll git", got.Status.RefreshMessage)
			assert.Contains(t, ev[0], "Warning "+promotionstep.ReasonRefreshFailed)
			for _, text := range []string{got.Status.RefreshMessage, ev[0]} {
				assert.NotContains(t, text, "SECRET-BODY")
				assert.NotContains(t, text, "10.9.8.7")
				assert.NotContains(t, text, "127.0.0.1")
			}
		})
	}
}
