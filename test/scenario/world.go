// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/meta/testrestmapper"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/admission"
	clienttesting "k8s.io/client-go/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	airec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
	arrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentrun"
	bundlerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
	cwrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
	mcrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
	pipelinerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
	pgrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	prsrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
	rrrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/renderrun"
	rbprec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/rollbackpolicy"
	strec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/steptemplate"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
)

// Third-party kinds the scenario cluster serves, as unstructured objects:
// what kardinal reads (health) or writes (the argocd strategy, refresh
// requests).
var (
	GraphGVK         = schema.GroupVersionKind{Group: graph.GraphGVR.Group, Version: graph.GraphGVR.Version, Kind: "Graph"}
	ApplicationGVK   = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}
	RolloutGVK       = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout"}
	KustomizationGVK = schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"}
	GitRepositoryGVK = schema.GroupVersionKind{Group: "source.toolkit.fluxcd.io", Version: "v1", Kind: "GitRepository"}
	CanaryGVK        = schema.GroupVersionKind{Group: "flagger.app", Version: "v1beta1", Kind: "Canary"}
)

var unstructuredKinds = map[schema.GroupVersionResource]schema.GroupVersionKind{
	graph.GraphGVR: GraphGVK,
	{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}:             ApplicationGVK,
	{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}:                 RolloutGVK,
	{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}: KustomizationGVK,
	{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}:   GitRepositoryGVK,
	{Group: "flagger.app", Version: "v1beta1", Resource: "canaries"}:                  CanaryGVK,
	{Group: "apps", Version: "v1", Resource: "deployments"}:                           appsv1.SchemeGroupVersion.WithKind("Deployment"),
	{Group: "apps", Version: "v1", Resource: "replicasets"}:                           appsv1.SchemeGroupVersion.WithKind("ReplicaSet"),
	{Group: "", Version: "v1", Resource: "pods"}:                                      corev1.SchemeGroupVersion.WithKind("Pod"),
	{Group: "", Version: "v1", Resource: "events"}:                                    corev1.SchemeGroupVersion.WithKind("Event"),
}

// Scheme returns the scenario cluster's scheme: client-go, kardinal, and
// the third-party kinds as unstructured.
func Scheme() (*k8sruntime.Scheme, error) {
	s := k8sruntime.NewScheme()
	for _, add := range []func(*k8sruntime.Scheme) error{
		corev1.AddToScheme, appsv1.AddToScheme, batchv1.AddToScheme, rbacv1.AddToScheme, v1alpha1.AddToScheme,
		networkingv1.AddToScheme, discoveryv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			return nil, fmt.Errorf("scheme: %w", err)
		}
	}
	for _, gvk := range []schema.GroupVersionKind{GraphGVK, ApplicationGVK, RolloutGVK, KustomizationGVK, GitRepositoryGVK, CanaryGVK} {
		s.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		s.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	return s, nil
}

var (
	mapperOnce sync.Once
	mapper     apimeta.RESTMapper
)

// restMapper is the scheme's REST mapper, built once: every World shares
// the scheme's kinds.
func restMapper(s *k8sruntime.Scheme) apimeta.RESTMapper {
	mapperOnce.Do(func() { mapper = testrestmapper.TestOnlyStaticRESTMapper(s) })
	return mapper
}

// RepoRoot is the repository root (for the CRDs).
func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// Options configure a World.
type Options struct {
	// SCM is the provider kind: SCMGitHub (default), SCMGitLab or SCMForgejo.
	SCM string
	// CompactAbove is the Builder's threshold (graph.DefaultCompactAbove
	// when zero; -1 makes every Graph compact).
	CompactAbove int
	// Tick is how far the clock moves per round (default 20s).
	Tick time.Duration
	// PolicyNamespaces are the controller's org policy namespaces.
	PolicyNamespaces []string
	// Start is the clock's start (default: a Wednesday, 10:00 UTC).
	Start time.Time
	// Watch reconciles an object only when a watch would wake it: it, or
	// any object of its namespace, a cluster-scoped object or an object of
	// an org policy namespace, changed since its last reconcile, or its
	// requeue time came. Without it, every object is reconciled every round,
	// which also checks that every reconcile is idempotent.
	Watch bool
	// Actors runs the agent registry (the AgentIdentity and AgentIndex
	// reconcilers, the index built at start as the controller does) and
	// admits every write of a kind the chart's identity and actor policies
	// match through the API server's ValidatingAdmissionPolicy plugin
	// (APIServer), as the user of the write's context (AsUser; the
	// controller's ServiceAccount by default, kro's Graph ServiceAccount for
	// kro). The index is the policies' param, as the plugin's informer sees
	// it at the time of the write.
	Actors bool
	// MaxIndexBytes bounds the agent index (0: actor.MaxIndexBytes).
	MaxIndexBytes int
}

// World is one kardinal controller and the cluster, git host and GitOps
// engine around it, all in process. Every kardinal reconciler and the
// translator run as cmd/kardinal-controller wires them; kro (Kro), the
// GitOps engine and kubelet (GitOps) and the git host (GitServer,
// SCMServer) are the stand-ins.
type World struct {
	T      testing.TB
	Ctx    context.Context
	Client client.Client
	Scheme *k8sruntime.Scheme
	Kro    *Kro
	Git    *GitServer
	SCM    *SCMServer
	Admit  *Admission
	GitOps *GitOps
	Dir    string

	clockMu sync.Mutex
	clock   time.Time
	tick    time.Duration

	pipelines *pipelinerec.Reconciler
	bundles   *bundlerec.Reconciler
	steps     *psrec.Reconciler
	gates     *pgrec.Reconciler
	prs       *prsrec.Reconciler
	windows   *cwrec.Reconciler
	metrics   *mcrec.Reconciler
	rollbacks *rbprec.Reconciler
	renders   *rrrec.Reconciler
	gitClient *RewritingGit

	// The agent registry and admission (Options.Actors).
	api         *APIServer
	index       *airec.IndexReconciler
	indexDirty  atomic.Bool
	indexPaused atomic.Bool
	agentRuns   *arrec.Reconciler
	templates   *strec.Reconciler
	// agentRefusals are agents' Verdicts admission refused.
	agentRefusals []AgentVerdictRefusal
	store         client.WithWatch
	mapper        apimeta.RESTMapper

	// Metrics are the fake metric backend's values by query.
	Metrics *FakeMetrics

	// Agents are the in-cluster verifiers by StepTemplate: what each attempt
	// of an AgentRun does (default: write a pass).
	Agents map[string]Agent

	// Actors run at the start of every round: the people and systems
	// around kardinal (reviewers, approvers, CI).
	Actors []func(w *World) error

	writes atomic.Int64
	// graphWrites counts the writes of Graphs (Kro.GraphVersion).
	graphWrites atomic.Int64
	uids        atomic.Int64
	mu          sync.Mutex
	reconciles  map[string]int
	// perObject counts the reconciles of each object (kind/namespace/name).
	perObject  map[string]int
	errors     map[string]int
	lastErrors map[string]string
	rounds     int
	watch      bool
	policyNS   map[string]bool
	// keyWrites counts writes per watch key (emitKeys).
	keyWrites map[string]int64
	cache     *readCache
	seen      map[string]reconcileState
	// verified records, per namespace/environment, the image tag of the
	// Bundle most recently seen Verified there, and the steps seen.
	verified     map[string]string
	seenVerified map[string]bool
}

// NewWorld starts a World; t.Cleanup stops it.
func NewWorld(t testing.TB, opts Options) *World {
	t.Helper()
	s, err := Scheme()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mapper := restMapper(s)
	w := &World{T: t, Scheme: s, Dir: dir, reconciles: map[string]int{}, perObject: map[string]int{}, errors: map[string]int{}, lastErrors: map[string]string{},
		verified: map[string]string{}, seenVerified: map[string]bool{},
		watch: opts.Watch, policyNS: map[string]bool{}, keyWrites: map[string]int64{}, seen: map[string]reconcileState{}, mapper: mapper}
	for _, ns := range opts.PolicyNamespaces {
		w.policyNS[ns] = true
	}
	w.Ctx = zerolog.Nop().WithContext(context.Background())
	w.tick = opts.Tick
	if w.tick == 0 {
		w.tick = 20 * time.Second
	}
	w.clock = opts.Start
	if w.clock.IsZero() {
		w.clock = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC) // a Wednesday
	}
	countWrite := func(o client.Object, created bool) {
		switch objKind(o) {
		case "AgentIndex":
			w.indexDirty.Store(true)
		case "Graph":
			w.graphWrites.Add(1)
		}
		w.writes.Add(1)
		w.noteWrite(o)
		if w.cache != nil {
			w.cache.invalidate(objKind(o), o.GetNamespace(), o.GetName(), created)
		}
	}
	// A patch admission refuses is stored and then put back (admitPatched):
	// the object's resourceVersion moved, so the cache drops it, or the next
	// read-modify-write of it conflicts with a version nobody can see.
	refusedPatch := func(o client.Object) {
		if w.cache != nil {
			w.cache.invalidate(objKind(o), o.GetNamespace(), o.GetName(), false)
		}
	}
	statusObjs := []client.Object{
		&v1alpha1.Pipeline{}, &v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}, &v1alpha1.PolicyGate{},
		&v1alpha1.PRStatus{}, &v1alpha1.ChangeWindow{}, &v1alpha1.MetricCheck{}, &v1alpha1.RollbackPolicy{},
		&v1alpha1.RenderRun{}, &v1alpha1.HookRun{}, &v1alpha1.ScheduleClock{}, &appsv1.Deployment{}, &batchv1.Job{},
		&corev1.Pod{}, &v1alpha1.AuditEvent{}, &v1alpha1.AgentIdentity{}, &v1alpha1.AgentIndex{},
		&v1alpha1.AgentRun{}, &v1alpha1.StepTemplate{}, &v1alpha1.Verdict{},
	}
	for _, gvk := range []schema.GroupVersionKind{GraphGVK, ApplicationGVK, RolloutGVK, KustomizationGVK, GitRepositoryGVK, CanaryGVK} {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvk)
		statusObjs = append(statusObjs, u)
	}
	c := fake.NewClientBuilder().WithScheme(s).
		// The plain tracker: kardinal applies nothing server-side, and the
		// field-managed tracker's managedFields bookkeeping was a third of
		// the suite's time.
		WithObjectTracker(clienttesting.NewObjectTracker(s, serializer.NewCodecFactory(s).UniversalDecoder())).
		WithRESTMapper(w.mapper).
		WithGlobalResourceVersionCounter().
		WithStatusSubresource(statusObjs...).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		WithIndex(&v1alpha1.Pipeline{}, pipelinerec.IndexPipelineRepo, pipelinerec.PipelineRepoValues).
		WithIndex(&v1alpha1.PromotionStep{}, "spec.pipelineName", func(o client.Object) []string {
			if ps, ok := o.(*v1alpha1.PromotionStep); ok && ps.Spec.PipelineName != "" {
				return []string{ps.Spec.PipelineName}
			}
			return nil
		}).
		WithInterceptorFuncs(interceptor.Funcs{
			// The API server sets a UID, the creation time and generation
			// 1 on create and moves the generation when the spec changes;
			// the fake client does none of it.
			Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) (err error) {
				defer func() {
					if err == nil {
						countWrite(o, true)
					}
				}()
				if o.GetUID() == "" {
					o.SetUID(types.UID(fmt.Sprintf("uid-%d", w.uids.Add(1))))
				}
				if o.GetGeneration() == 0 {
					o.SetGeneration(1)
				}
				if ts := o.GetCreationTimestamp(); ts.IsZero() {
					o.SetCreationTimestamp(metav1.NewTime(w.Now()))
				}
				if err := w.validMeta(c, o, nil); err != nil {
					return err
				}
				if err := w.validSchema(o, nil); err != nil {
					return err
				}
				if err := w.admitWrite(ctx, c, admission.Create, o, nil); err != nil {
					return err
				}
				return c.Create(ctx, o, opts...)
			},
			Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) (err error) {
				defer func() {
					if err == nil {
						countWrite(o, false)
					}
				}()
				if gerr := bumpGeneration(ctx, c, o, nil); gerr != nil {
					return gerr
				}
				old, gerr := w.stored(ctx, c, o)
				if gerr != nil {
					return gerr
				}
				if err := w.validMeta(c, o, old); err != nil {
					return err
				}
				if err := w.validSchema(o, old); err != nil {
					return err
				}
				if err := w.admitWrite(ctx, c, admission.Update, o, old); err != nil {
					return err
				}
				return c.Update(ctx, o, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) (err error) {
				defer func() {
					if err == nil {
						countWrite(o, false)
					} else {
						refusedPatch(o)
					}
				}()
				before, serr := specOf(ctx, c, o)
				if serr != nil {
					return serr
				}
				old, serr := w.stored(ctx, c, o)
				if serr != nil {
					return serr
				}
				if err = c.Patch(ctx, o, p, opts...); err != nil {
					return err
				}
				if err = bumpGeneration(ctx, c, o, before); err != nil {
					return err
				}
				// The patched object is admitted as the API server would
				// before storing it: refused, the stored one is put back.
				if verr := w.validSchema(o, old); verr != nil && old != nil {
					if cur, gerr := w.stored(ctx, c, o); gerr == nil && cur != nil {
						old.SetResourceVersion(cur.GetResourceVersion())
					}
					if rerr := c.Update(ctx, old); rerr != nil {
						return fmt.Errorf("restore %s %s after a refused patch: %w", objKind(o), o.GetName(), rerr)
					}
					return verr
				}
				return w.admitPatched(ctx, c, "", o, old)
			},
			Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) (err error) {
				defer func() {
					if err == nil {
						countWrite(o, true)
					}
				}()
				old, gerr := w.stored(ctx, c, o)
				if gerr != nil {
					return gerr
				}
				if old != nil {
					if err := w.admitWrite(ctx, c, admission.Delete, nil, old); err != nil {
						return err
					}
				}
				return c.Delete(ctx, o, opts...)
			},
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) (err error) {
				defer func() {
					if err == nil {
						countWrite(o, false)
					}
				}()
				old, gerr := w.stored(ctx, c, o)
				if gerr != nil {
					return gerr
				}
				if old != nil {
					if err := w.admitSub(ctx, c, admission.Update, sub, o, old); err != nil {
						return err
					}
				}
				return c.SubResource(sub).Update(ctx, o, opts...)
			},
			SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) (err error) {
				defer func() {
					if err == nil {
						countWrite(o, false)
					} else {
						refusedPatch(o)
					}
				}()
				old, gerr := w.stored(ctx, c, o)
				if gerr != nil {
					return gerr
				}
				if err = c.SubResource(sub).Patch(ctx, o, p, opts...); err != nil {
					return err
				}
				if ww, ok := c.(client.WithWatch); ok {
					return w.admitPatched(ctx, ww, sub, o, old)
				}
				return nil
			},
		}).
		Build()
	w.cache = newReadCache(c)
	w.Client = w.cache
	w.store = c
	kinds := map[schema.GroupVersionResource]string{}
	for gvr, gvk := range unstructuredKinds {
		kinds[gvr] = gvk.Kind
	}
	dyn := newDynClient(c, kinds)
	w.Kro = NewKro(c)
	w.Kro.Version = w.writes.Load
	w.Kro.GraphVersion = w.graphWrites.Load
	if opts.Watch {
		w.Kro.Skip = w.graphQuiet
	}
	w.Git = NewGitServer(filepath.Join(dir, "git"))
	if err := os.MkdirAll(filepath.Join(dir, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	kind := opts.SCM
	if kind == "" {
		kind = SCMGitHub
	}
	w.SCM = NewSCMServer(kind, w.Git)
	t.Cleanup(w.SCM.Close)
	w.Admit = NewAdmission(RepoRoot())
	w.Metrics = &FakeMetrics{values: map[string]float64{}}

	provider, err := scm.NewProvider(kind, "scm-token", w.SCM.URL(), "")
	if err != nil {
		t.Fatal(err)
	}
	logger := zerolog.Nop()
	builder := graph.NewBuilder()
	switch {
	case opts.CompactAbove < 0:
		builder.CompactAbove = 0
	case opts.CompactAbove > 0:
		builder.CompactAbove = opts.CompactAbove
	}
	compactAbove := builder.CompactAbove
	// The reconcilers read through the cache, as from the manager's
	// informers; APIReader and Reader read the store directly.
	rc := w.Client
	tr := translator.New(graph.NewGraphClient(dyn, logger), builder, rc, opts.PolicyNamespaces, logger)
	now := w.Now
	w.gitClient = &RewritingGit{GoGitClient: scm.NewGoGitClient(), Server: w.Git}
	w.GitOps = &GitOps{w: w}
	w.bundles = &bundlerec.Reconciler{Client: rc, APIReader: c, Translator: tr, GraphChecker: graph.NewGraphClient(dyn, logger),
		PolicyNamespaces: opts.PolicyNamespaces, NowFn: now}
	w.pipelines = &pipelinerec.Reconciler{Client: rc, Reader: c, CompactAbove: &compactAbove, Now: now}
	w.steps = &psrec.Reconciler{Client: rc, APIReader: c, SCM: provider, GitClient: w.gitClient,
		HealthDetector: health.NewAutoDetector(rc, dyn), RemoteClusters: &health.RemoteClusters{},
		WorkDirRoot: filepath.Join(dir, "work"), NowFn: now, ControllerUser: ControllerUser}
	gates, err := pgrec.NewReconciler(rc)
	if err != nil {
		t.Fatal(err)
	}
	gates.NowFn = now
	gates.MetricsNowFn = now
	gates.PolicyNamespaces = opts.PolicyNamespaces
	if opts.Actors {
		gates.AgentIndexName = Release // agent.* reads the release's AgentIndex, as --agent-index sets it
	}
	w.gates = gates
	w.prs = &prsrec.Reconciler{Client: rc, SCM: provider, NowFn: now}
	w.windows = &cwrec.Reconciler{Client: rc, NowFn: now, MaxExemption: cwrec.DefaultMaxExemption}
	w.metrics = &mcrec.Reconciler{Client: rc, Backends: map[string]mcrec.Backend{"prometheus": w.Metrics}, NowFn: now}
	w.rollbacks = &rbprec.Reconciler{Client: rc, NowFn: now, ControllerUser: ControllerUser}
	w.renders = &rrrec.Reconciler{Client: rc, APIReader: c, Image: "kardinal-render:scenario", ControllerNamespace: "kardinal-system",
		AuthorName: "kardinal-promoter", AuthorEmail: "kardinal@kardinal.io", NowFn: now, Timeout: time.Hour}
	if opts.Actors {
		// The release's AgentIndex vouches for the agent stamps the Bundle
		// (rejections) and Pipeline (holds, lastChange) reconcilers record,
		// as main.go wires them.
		agents := &actor.Source{Reader: rc, Name: Release}
		w.bundles.Agents, w.pipelines.Agents = agents, agents
		w.startActors(opts.MaxIndexBytes)
		w.agentRuns = &arrec.Reconciler{Client: rc, APIReader: c, WriterClusterRole: Release + "-agentrun-writer",
			ReaderClusterRole: Release + "-agentrun-reader", ControllerNamespace: ReleaseNamespace, NowFn: now}
		w.templates = &strec.Reconciler{Client: rc}
		// The API server's own EndpointSlice: an AgentRun's NetworkPolicy
		// allows egress to it.
		if err := c.Create(w.Ctx, &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"},
			AddressType: discoveryv1.AddressTypeIPv4, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.96.0.1"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// Now is the World's clock.
func (w *World) Now() time.Time {
	w.clockMu.Lock()
	defer w.clockMu.Unlock()
	return w.clock
}

// Advance moves the clock.
func (w *World) Advance(d time.Duration) {
	w.clockMu.Lock()
	defer w.clockMu.Unlock()
	w.clock = w.clock.Add(d)
}

// Apply admits obj as the API server would (Admission) and creates it.
// It returns the reasons the API server refuses it, or creates it.
func (w *World) Apply(obj client.Object) ([]string, error) {
	return w.ApplyAs(AsUser(w.Ctx, AdminUser, AdminGroups...), obj)
}

// ApplyAs is Apply by the user of ctx (AsUser).
func (w *World) ApplyAs(ctx context.Context, obj client.Object) ([]string, error) {
	if _, kardinal := crdFiles[kindOf(obj)]; kardinal {
		reasons, err := w.Admit.Admit(obj, nil)
		if err != nil {
			return nil, err
		}
		if len(reasons) > 0 {
			return reasons, nil
		}
	}
	if err := w.Client.Create(ctx, obj); err != nil {
		// A policy's denial: Forbidden from a validation with that reason,
		// Invalid (422) from one without, or when a binding cannot be
		// configured (its param is missing).
		var status apierrors.APIStatus
		if errors.As(err, &status) && (status.Status().Code == http.StatusForbidden ||
			status.Status().Code == http.StatusUnprocessableEntity) && strings.Contains(err.Error(), "ValidatingAdmissionPolicy") {
			return []string{err.Error()}, nil
		}
		return nil, fmt.Errorf("create %s %s: %w", kindOf(obj), obj.GetName(), err)
	}
	return nil, nil
}

// MustApply is Apply that fails the test on a refusal.
func (w *World) MustApply(objs ...client.Object) {
	w.T.Helper()
	for _, o := range objs {
		reasons, err := w.Apply(o)
		if err != nil {
			w.T.Fatal(err)
		}
		if len(reasons) > 0 {
			w.T.Fatalf("admission refused %s %s: %s", kindOf(o), o.GetName(), strings.Join(reasons, "; "))
		}
	}
}

// maxImmediate bounds the reconciles of one object in one round.
const maxImmediate = 4

type reconcileKind struct {
	name string
	list client.ObjectList
	rec  func(ctx context.Context, req ctrl.Request) (ctrl.Result, error)
}

func (w *World) kinds() []reconcileKind {
	var registry []reconcileKind
	if w.index != nil {
		// The registry first: the controller builds the index at start and
		// keeps it current, with every AgentIdentity's status (#1937); a
		// write is admitted against it.
		registry = []reconcileKind{
			{"AgentIndex", nil, w.index.Reconcile},
			{"StepTemplate", &v1alpha1.StepTemplateList{}, w.templates.Reconcile},
			{"AgentRun", &v1alpha1.AgentRunList{}, w.agentRuns.Reconcile},
		}
	}
	return append(registry, []reconcileKind{
		{"Pipeline", &v1alpha1.PipelineList{}, w.pipelines.Reconcile},
		{"ChangeWindow", &v1alpha1.ChangeWindowList{}, w.windows.Reconcile},
		{"MetricCheck", &v1alpha1.MetricCheckList{}, w.metrics.Reconcile},
		{"Bundle", &v1alpha1.BundleList{}, w.bundles.Reconcile},
		{"Kro", nil, nil},
		{"PolicyGate", &v1alpha1.PolicyGateList{}, w.gates.Reconcile},
		{"PromotionStep", &v1alpha1.PromotionStepList{}, w.steps.Reconcile},
		{"PRStatus", &v1alpha1.PRStatusList{}, w.prs.Reconcile},
		{"RenderRun", &v1alpha1.RenderRunList{}, w.renders.Reconcile},
		{"RollbackPolicy", &v1alpha1.RollbackPolicyList{}, w.rollbacks.Reconcile},
		{"Bundle", &v1alpha1.BundleList{}, w.bundles.Reconcile},
	}...)
}

// Round runs every actor, kro, every kardinal reconciler on every object of
// its kind, the GitOps engine and the kubelet once, then advances the clock
// by the tick. It returns the number of writes the round made.
func (w *World) Round() int64 {
	w.T.Helper()
	start := w.writes.Load()
	for _, a := range w.Actors {
		if err := a(w); err != nil {
			w.T.Fatalf("actor: %v", err)
		}
	}
	w.syncKro()
	for _, k := range w.kinds() {
		if k.list == nil && k.rec != nil {
			w.reconcileIndex(k)
			continue
		}
		if k.list == nil {
			w.syncKro()
			continue
		}
		if err := w.Client.List(w.Ctx, k.list); err != nil {
			w.T.Fatalf("list %s: %v", k.name, err)
		}
		objs, err := apimetaExtract(k.list)
		if err != nil {
			w.T.Fatal(err)
		}
		sort.Slice(objs, func(i, j int) bool {
			return objs[i].GetNamespace()+"/"+objs[i].GetName() < objs[j].GetNamespace()+"/"+objs[j].GetName()
		})
		for _, o := range objs {
			w.reconcileObject(k, o)
		}
	}
	if err := w.GitOps.Sync(); err != nil {
		w.T.Fatalf("gitops: %v", err)
	}
	if err := w.runJobs(); err != nil {
		w.T.Fatalf("kubelet: %v", err)
	}
	w.observeVerified()
	w.mu.Lock()
	w.rounds++
	w.mu.Unlock()
	w.Advance(w.tick)
	return w.writes.Load() - start
}

// reconcileObject reconciles o with k when a watch would wake it.
func (w *World) reconcileObject(k reconcileKind, o client.Object) {
	key := types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}
	skey := k.name + "/" + key.String()
	if !w.wakes(skey, o) {
		return
	}
	// A reconcile that wrote and asks to run again at once runs
	// again, as the watch event of its own write would make it.
	for i := 0; i < maxImmediate; i++ {
		before := w.writes.Load()
		res, err := k.rec(w.Ctx, ctrl.Request{NamespacedName: key})
		w.mu.Lock()
		w.reconciles[k.name]++
		w.perObject[skey]++
		if err != nil && !apierrors.IsConflict(err) {
			w.errors[k.name]++
			w.lastErrors[k.name] = err.Error()
		}
		w.mu.Unlock()
		w.reconciled(skey, o, res, err)
		if err != nil || w.writes.Load() == before || res.RequeueAfter > 0 {
			break
		}
	}
}

func (w *World) syncKro() {
	if _, err := w.Kro.Sync(w.Ctx); err != nil {
		w.T.Fatalf("kro: %v", err)
	}
}

// reconcileState is what a watch-mode World knows of one object's last
// reconcile: its resourceVersion, the sum of the write counts of the keys it
// listens to (listenKeys), and its requeue.
type reconcileState struct {
	rv         string
	heard      int64
	due        time.Time
	hasDue     bool
	reconciled bool
}

// The keys of watch mode: a write emits keys (emitKeys) and an object
// listens to keys (listenKeys), the way kardinal's controllers map watch
// events (SetupWithManager of each reconciler): a Bundle hears its steps,
// its Graph, its Pipeline, its sibling Bundles and the gate templates; a
// PromotionStep its Bundle, its Bundle's gate instances and PRStatuses and
// its Pipeline; a gate instance its Bundle's steps, its Pipeline and the
// gate inputs (MetricChecks, ChangeWindows, ScheduleClocks); a Pipeline its
// Bundles, steps and gate instances.
func emitKeys(o client.Object, policyNS map[string]bool) []string {
	ns, l := o.GetNamespace(), o.GetLabels()
	b, p := l["kardinal.io/bundle"], l["kardinal.io/pipeline"]
	switch objKind(o) {
	case "Bundle":
		pl := pipelineOfBundle(o)
		return []string{"B:" + ns + "/" + o.GetName(), "PB:" + ns + "/" + pl}
	case "PromotionStep":
		return []string{"S:" + ns + "/" + b, "SP:" + ns + "/" + p}
	case "Pipeline":
		return []string{"P:" + ns + "/" + o.GetName()}
	case "PolicyGate":
		if b == "" {
			if policyNS[ns] {
				return []string{"GT:*"}
			}
			return []string{"GT:" + ns}
		}
		return []string{"GI:" + ns + "/" + b, "GIP:" + ns + "/" + p}
	case "PRStatus":
		return []string{"PR:" + ns + "/" + b}
	case "Approval":
		return []string{"A:" + ns + "/" + b}
	case "MetricCheck", "ChangeWindow", "ScheduleClock":
		return []string{"IN:*"}
	case "AgentIdentity":
		return []string{"AI:*"}
	case "AgentIndex":
		return []string{"IX:*"}
	case "Job":
		if owner := metav1.GetControllerOf(o); owner != nil {
			return []string{"RR:" + ns + "/" + owner.Name}
		}
	}
	if objKind(o) == "Graph" {
		return []string{"G:" + ns + "/" + b}
	}
	return nil
}

// objKind is o's kind: its Go type, or an unstructured object's kind.
func objKind(o client.Object) string {
	if u, ok := o.(*unstructured.Unstructured); ok {
		return u.GetKind()
	}
	return kindOf(o)
}

func listenKeys(o client.Object) []string {
	ns, l := o.GetNamespace(), o.GetLabels()
	b, p := l["kardinal.io/bundle"], l["kardinal.io/pipeline"]
	switch objKind(o) {
	case "Bundle":
		pl := pipelineOfBundle(o)
		return []string{"B:" + ns + "/" + o.GetName(), "PB:" + ns + "/" + pl, "S:" + ns + "/" + o.GetName(),
			"G:" + ns + "/" + o.GetName(), "P:" + ns + "/" + pl, "GT:" + ns, "GT:*"}
	case "PromotionStep":
		return []string{"B:" + ns + "/" + b, "GI:" + ns + "/" + b, "PR:" + ns + "/" + b, "P:" + ns + "/" + p}
	case "PolicyGate":
		if b == "" {
			return nil
		}
		return []string{"S:" + ns + "/" + b, "P:" + ns + "/" + p, "IN:*"}
	case "Pipeline":
		return []string{"PB:" + ns + "/" + o.GetName(), "SP:" + ns + "/" + o.GetName(), "GIP:" + ns + "/" + o.GetName(),
			"GT:" + ns, "GT:*"}
	case "RenderRun":
		return []string{"RR:" + ns + "/" + o.GetName()}
	case "AgentIdentity":
		// A change to one AgentIdentity rebuilds the index and every
		// status (agentidentity.IndexReconciler.SetupWithManager).
		return []string{"AI:*"}
	case "AgentIndex":
		return []string{"AI:*", "IX:*"}
	}
	return nil
}

func pipelineOfBundle(o client.Object) string {
	switch x := o.(type) {
	case *v1alpha1.Bundle:
		return x.Spec.Pipeline
	case *unstructured.Unstructured:
		p, _, _ := unstructured.NestedString(x.Object, "spec", "pipeline")
		return p
	}
	return ""
}

// noteWrite counts a write of o under the keys it emits.
func (w *World) noteWrite(o client.Object) {
	keys := emitKeys(o, w.policyNS)
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, k := range keys {
		w.keyWrites[k]++
	}
}

// heard is the sum of the write counts of the keys o listens to. Callers
// hold w.mu.
func (w *World) heard(o client.Object) int64 {
	var n int64
	for _, k := range listenKeys(o) {
		n += w.keyWrites[k]
	}
	return n
}

// wakes reports whether a watch would reconcile o (as skey) now: it is
// new, it changed, it heard a write, or its requeue time came.
func (w *World) wakes(skey string, o client.Object) bool {
	if !w.watch {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	st, ok := w.seen[skey]
	if !ok || !st.reconciled || st.rv != o.GetResourceVersion() || st.heard != w.heard(o) {
		return true
	}
	return st.hasDue && !w.Now().Before(st.due)
}

// reconciled records a reconcile of skey for wakes.
func (w *World) reconciled(skey string, o client.Object, res ctrl.Result, err error) {
	if !w.watch {
		return
	}
	cur := o.DeepCopyObject().(client.Object)
	rv := ""
	if gerr := w.Client.Get(w.Ctx, client.ObjectKeyFromObject(o), cur); gerr == nil {
		rv = cur.GetResourceVersion()
		o = cur
	}
	now := w.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	st := reconcileState{rv: rv, heard: w.heard(o), reconciled: true}
	//nolint:staticcheck // Requeue is what the reconcilers still return
	switch {
	case err != nil, res.Requeue:
		st.due, st.hasDue = now, true
	case res.RequeueAfter > 0:
		st.due, st.hasDue = now.Add(res.RequeueAfter), true
	}
	w.seen[skey] = st
}

// RunUntil runs rounds until done reports true or max rounds ran. It
// returns whether done held.
func (w *World) RunUntil(max int, done func() bool) bool {
	w.T.Helper()
	for i := 0; i < max; i++ {
		if done() {
			return true
		}
		w.Round()
	}
	return done()
}

// Reconciles returns how many reconciles ran per kind.
func (w *World) Reconciles() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]int{}
	for k, v := range w.reconciles {
		out[k] = v
	}
	return out
}

// ReconcilesPerObject returns how many reconciles each object got, by
// kind/namespace/name.
func (w *World) ReconcilesPerObject() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]int, len(w.perObject))
	for k, v := range w.perObject {
		out[k] = v
	}
	return out
}

// Errors returns the reconcile errors per kind and the last message of each.
func (w *World) Errors() (map[string]int, map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, m := map[string]int{}, map[string]string{}
	for k, v := range w.errors {
		n[k] = v
	}
	for k, v := range w.lastErrors {
		m[k] = v
	}
	return n, m
}

// Rounds is the number of rounds run.
func (w *World) Rounds() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.rounds
}

// specOf returns the stored spec of o (nil when o does not exist or has
// none), as JSON-shaped data.
func specOf(ctx context.Context, c client.Client, o client.Object) (interface{}, error) {
	cur := &unstructured.Unstructured{}
	gvk, err := apiutilGVK(c, o)
	if err != nil {
		return nil, err
	}
	cur.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, client.ObjectKeyFromObject(o), cur); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return cur.Object["spec"], nil
}

// bumpGeneration sets o's generation to the stored one, plus one when o's
// spec differs from before (the stored spec when before is nil). For a
// patch it writes the new generation itself.
func bumpGeneration(ctx context.Context, c client.WithWatch, o client.Object, before interface{}) error {
	patched := before != nil
	if !patched {
		var err error
		if before, err = specOf(ctx, c, o); err != nil || before == nil {
			return err
		}
	}
	u, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		return fmt.Errorf("generation: %w", err)
	}
	cur := &unstructured.Unstructured{}
	gvk, err := apiutilGVK(c, o)
	if err != nil {
		return err
	}
	cur.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, client.ObjectKeyFromObject(o), cur); err != nil {
		return client.IgnoreNotFound(err)
	}
	gen := cur.GetGeneration()
	if gen == 0 {
		gen = 1
	}
	if equalJSON(before, u["spec"]) {
		o.SetGeneration(gen)
		return nil
	}
	o.SetGeneration(gen + 1)
	if !patched {
		return nil
	}
	cur.SetGeneration(gen + 1)
	return c.Update(ctx, cur)
}

func apiutilGVK(c client.Client, o client.Object) (schema.GroupVersionKind, error) {
	gvk, err := c.GroupVersionKindFor(o)
	if err != nil {
		return schema.GroupVersionKind{}, fmt.Errorf("gvk of %T: %w", o, err)
	}
	return gvk, nil
}

func equalJSON(a, b interface{}) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ja) == string(jb)
}

// observeVerified records the steps that turned Verified this round.
func (w *World) observeVerified() {
	var steps v1alpha1.PromotionStepList
	if err := w.Client.List(w.Ctx, &steps); err != nil {
		w.T.Fatalf("list steps: %v", err)
	}
	for _, st := range steps.Items {
		key := string(st.UID)
		if st.Status.State != "Verified" || w.seenVerified[key] {
			continue
		}
		w.seenVerified[key] = true
		var b v1alpha1.Bundle
		if err := w.Client.Get(w.Ctx, client.ObjectKey{Namespace: st.Namespace, Name: st.Spec.BundleName}, &b); err != nil {
			continue
		}
		if len(b.Spec.Images) > 0 {
			w.verified[st.Namespace+"/"+st.Spec.Environment] = b.Spec.Images[0].Tag
		}
	}
}

// LastVerified returns, per environment of ns, the image tag of the Bundle
// most recently Verified there.
func (w *World) LastVerified(ns string) map[string]string {
	out := map[string]string{}
	for k, v := range w.verified {
		if e, ok := strings.CutPrefix(k, ns+"/"); ok {
			out[e] = v
		}
	}
	return out
}

// GateInstances returns the gate instances of bundle in ns and the org
// policy namespaces.
func (w *World) GateInstances(ns, bundle string) []v1alpha1.PolicyGate {
	var list v1alpha1.PolicyGateList
	if err := w.Client.List(w.Ctx, &list, client.MatchingLabels{"kardinal.io/bundle": bundle}); err != nil {
		w.T.Fatal(err)
	}
	var out []v1alpha1.PolicyGate
	for _, g := range list.Items {
		if g.Namespace == ns {
			out = append(out, g)
		}
	}
	return out
}

// graphQuiet reports, in watch mode, whether g needs no pass: its spec and
// status are as at its last pass and none of the objects it reads (its
// Bundle, its Bundle's steps, gate instances, PRStatuses and Approvals)
// changed since. kro reconciles a Graph only on such events.
func (w *World) graphQuiet(g *graph.Graph) bool {
	ns, b := g.Namespace, g.Labels["kardinal.io/bundle"]
	key := "graph/" + ns + "/" + g.Name
	w.mu.Lock()
	defer w.mu.Unlock()
	var heard int64
	for _, k := range []string{"B:" + ns + "/" + b, "S:" + ns + "/" + b, "GI:" + ns + "/" + b, "PR:" + ns + "/" + b,
		"A:" + ns + "/" + b, "G:" + ns + "/" + b, "GT:" + ns, "GT:*"} {
		heard += w.keyWrites[k]
	}
	st, ok := w.seen[key]
	quiet := ok && st.rv == g.ResourceVersion && st.heard == heard
	if !quiet {
		w.seen[key] = reconcileState{rv: g.ResourceVersion, heard: heard, reconciled: true}
	}
	return quiet
}
