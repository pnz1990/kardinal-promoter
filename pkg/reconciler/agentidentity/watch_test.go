// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentidentity_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllertest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
)

// handlerAdded is a fake informer that says when a watch registers its
// handler on it.
type handlerAdded struct {
	*controllertest.FakeInformer
	added chan struct{}
}

func (i *handlerAdded) AddEventHandlerWithOptions(h toolscache.ResourceEventHandler,
	opts toolscache.HandlerOptions) (toolscache.ResourceEventHandlerRegistration, error) {
	reg, err := i.FakeInformer.AddEventHandlerWithOptions(h, opts)
	close(i.added)
	return reg, err
}

// TestIndexWatchCarriesIdentityChanged checks the AgentIdentity watch that
// SetupWithManager registers (docs/design/24-agent-budgets.md W0): a label,
// an annotation or another writer's status field reaches no index rebuild,
// and a spec change, a create and a delete do. TestIdentityChanged covers the
// predicate; this covers that the watch carries it. Without it every
// AgentIdentity write rebuilt the whole index: at v0.14's 7,000 identities,
// once per budget usage write.
// Covers ACTOR-INDEX-06.
func TestIndexWatchCarriesIdentityChanged(t *testing.T) {
	sch := scheme(t)
	gv := v1alpha1.GroupVersion
	identities := &handlerAdded{FakeInformer: controllertest.NewFakeInformer(controllertest.Synced), added: make(chan struct{})}
	informers := &informertest.FakeInformers{Scheme: sch, InformersByGVK: map[schema.GroupVersionKind]toolscache.SharedIndexInformer{
		gv.WithKind("AgentIdentity"): identities,
		gv.WithKind("AgentIndex"):    controllertest.NewFakeInformer(controllertest.Synced),
	}}
	fixer := id("fixer", 0, "alice", sa("ci", "fixer"))
	var builds atomic.Int32 // each build lists the AgentIdentities
	c := fake.NewClientBuilder().WithScheme(sch).WithObjects(fixer, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "k"}}).
		WithStatusSubresource(&v1alpha1.AgentIdentity{}, &v1alpha1.AgentIndex{}).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*v1alpha1.AgentIdentityList); ok {
					builds.Add(1)
				}
				return c.List(ctx, list, opts...)
			},
		}).Build()
	mgr, err := ctrl.NewManager(&rest.Config{Host: "https://127.0.0.1:1"}, ctrl.Options{
		Scheme:                 sch,
		NewCache:               func(*rest.Config, cache.Options) (cache.Cache, error) { return informers, nil },
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	require.NoError(t, (&agentidentity.IndexReconciler{Client: c, Name: "k"}).SetupWithManager(mgr))
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		assert.NoError(t, <-stopped)
	})
	select {
	case <-identities.added:
	case <-time.After(30 * time.Second):
		t.Fatal("the AgentIdentity watch did not start")
	}
	// The start-up build, and the reconcile its status write may wake, end.
	settle := func() int32 {
		t.Helper()
		last := builds.Load()
		for {
			time.Sleep(300 * time.Millisecond)
			if n := builds.Load(); n == last {
				return n
			}
			last = builds.Load()
		}
	}
	require.Eventually(t, func() bool { return builds.Load() > 0 }, 30*time.Second, 50*time.Millisecond, "the start-up build")
	before := settle()

	cur := fixer.DeepCopy()
	cur.ResourceVersion, cur.Generation = "10", 1
	next := func(f func(*v1alpha1.AgentIdentity)) *v1alpha1.AgentIdentity {
		n := cur.DeepCopy()
		n.ResourceVersion = "11"
		f(n)
		return n
	}
	identities.Update(cur, next(func(a *v1alpha1.AgentIdentity) { a.Labels = map[string]string{"team": "a"} }))
	identities.Update(cur, next(func(a *v1alpha1.AgentIdentity) { a.Annotations = map[string]string{"note": "x"} }))
	identities.Update(cur, next(func(a *v1alpha1.AgentIdentity) {
		a.Status.Conditions = append(a.Status.Conditions, metav1.Condition{Type: "BudgetCurrent", Status: metav1.ConditionTrue, Reason: "Evaluated"})
	}))
	assert.Equal(t, before, settle(), "a label, an annotation or another writer's status field rebuilds nothing")

	identities.Update(cur, next(func(a *v1alpha1.AgentIdentity) { a.Generation, a.Spec.Suspended = 2, true }))
	require.Eventually(t, func() bool { return builds.Load() > before }, 10*time.Second, 50*time.Millisecond, "a spec change rebuilds the index")
	before = settle()
	identities.Add(id("reviewer", 1, "alice", v1alpha1.AgentSubject{Kind: "User", Name: "oidc:reviewer"}))
	require.Eventually(t, func() bool { return builds.Load() > before }, 10*time.Second, 50*time.Millisecond, "a create rebuilds the index")
	before = settle()
	identities.Delete(cur)
	require.Eventually(t, func() bool { return builds.Load() > before }, 10*time.Second, 50*time.Millisecond, "a delete rebuilds the index")
}
