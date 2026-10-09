// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	"sigs.k8s.io/controller-runtime/pkg/event"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// TestMetricCheckResultChanged covers C04-gates-36: the MetricCheck reconciler
// writes lastEvaluatedAt on every query, and only a change of what a gate can
// read (result, value) or of the spec re-evaluates the gates.
func TestMetricCheckResultChanged(t *testing.T) {
	base := func() *kardinalv1alpha1.MetricCheck {
		t0 := metav1.NewTime(time.Date(2026, 4, 7, 10, 0, 0, 0, time.UTC))
		validUntil := metav1.NewTime(t0.Add(3 * time.Minute))
		return &kardinalv1alpha1.MetricCheck{
			ObjectMeta: metav1.ObjectMeta{Name: "error-rate", Namespace: "default", Generation: 1},
			Status: kardinalv1alpha1.MetricCheckStatus{
				Result: "Pass", LastValue: "0.01", LastEvaluatedAt: &t0, ValidUntil: &validUntil,
			},
		}
	}
	tests := []struct {
		name   string
		old    func(mc *kardinalv1alpha1.MetricCheck)
		mutate func(mc *kardinalv1alpha1.MetricCheck)
		want   bool
	}{
		{name: "only lastEvaluatedAt", want: false, mutate: func(mc *kardinalv1alpha1.MetricCheck) {
			t1 := metav1.NewTime(mc.Status.LastEvaluatedAt.Add(time.Minute))
			mc.Status.LastEvaluatedAt = &t1
		}},
		{name: "result", want: true, mutate: func(mc *kardinalv1alpha1.MetricCheck) { mc.Status.Result = "Fail" }},
		{name: "value", want: true, mutate: func(mc *kardinalv1alpha1.MetricCheck) { mc.Status.LastValue = "0.02" }},
		{name: "spec", want: true, mutate: func(mc *kardinalv1alpha1.MetricCheck) { mc.Generation = 2 }},
		// #1302: an evaluation that makes a stale result fresh passes, even
		// with the same result and value.
		{name: "stale to fresh", want: true, mutate: func(mc *kardinalv1alpha1.MetricCheck) {
			t1 := metav1.NewTime(mc.Status.LastEvaluatedAt.Add(4 * time.Minute))
			mc.Status.LastEvaluatedAt = &t1
		}},
		{name: "no validUntil before (upgrade)", want: true, old: func(mc *kardinalv1alpha1.MetricCheck) {
			mc.Status.ValidUntil = nil
		}, mutate: func(mc *kardinalv1alpha1.MetricCheck) {
			t1 := metav1.NewTime(mc.Status.LastEvaluatedAt.Add(time.Minute))
			mc.Status.LastEvaluatedAt = &t1
		}},
		{name: "fresh at validUntil", want: false, mutate: func(mc *kardinalv1alpha1.MetricCheck) {
			t1 := *mc.Status.ValidUntil
			mc.Status.LastEvaluatedAt = &t1
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldMC, newMC := base(), base()
			if tt.old != nil {
				tt.old(oldMC)
			}
			tt.mutate(newMC)
			assert.Equal(t, tt.want, metricCheckResultChanged.Update(event.UpdateEvent{ObjectOld: oldMC, ObjectNew: newMC}))
		})
	}
	assert.True(t, metricCheckResultChanged.Create(event.CreateEvent{Object: base()}))
	assert.True(t, metricCheckResultChanged.Delete(event.DeleteEvent{Object: base()}))
}

// TestMetricCheckRequests covers C04-gates-36: a MetricCheck change enqueues
// only the gate instances in its namespace that read metrics.
func TestMetricCheckRequests(t *testing.T) {
	gate := func(name, ns, expr string, instance bool) client.Object {
		g := &kardinalv1alpha1.PolicyGate{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: expr},
		}
		if instance {
			g.Labels = map[string]string{labelBundle: "app-v1"}
		}
		return g
	}
	scheme := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		gate("app-v1-prod-error-rate", "default", `metrics["error-rate"].result == "Pass"`, true),
		gate("app-v1-prod-no-weekend", "default", `!schedule.isWeekend`, true),
		gate("error-rate", "default", `metrics["error-rate"].result == "Pass"`, false),
		gate("app-v1-prod-error-rate", "other", `metrics["error-rate"].result == "Pass"`, true),
	).Build()
	r := &Reconciler{Client: c}

	mc := &kardinalv1alpha1.MetricCheck{ObjectMeta: metav1.ObjectMeta{Name: "error-rate", Namespace: "default"}}
	reqs := r.metricCheckRequests(context.Background(), mc)
	require.Len(t, reqs, 1)
	assert.Equal(t, "default", reqs[0].Namespace)
	assert.Equal(t, "app-v1-prod-error-rate", reqs[0].Name)
}

// TestMetricCheckRequests_OrgGates covers bug 5 of the health spike: an org
// MetricCheck re-evaluates the org gate instances that read it, which live in
// the Pipeline namespaces, once each. A team MetricCheck does not reach
// instances in other namespaces.
func TestMetricCheckRequests_OrgGates(t *testing.T) {
	const expr = `metrics["error-rate"].result == "Pass"`
	gate := func(name, ns, templateNS string) client.Object {
		g := &kardinalv1alpha1.PolicyGate{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{labelBundle: "app-v1"}},
			Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: expr},
		}
		if templateNS != "" {
			g.Labels[graph.LabelGateTemplateNamespace] = templateNS
		}
		return g
	}
	scheme := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		gate("app-v1-prod-org", "team-a", "platform-policies"),
		gate("app-v1-prod-org", "team-b", "platform-policies"),
		gate("app-v1-prod-team", "team-a", "team-a"),
		gate("app-v1-prod-local", "platform-policies", "platform-policies"),
	).Build()
	r := &Reconciler{Client: c}
	names := func(ns string) []string {
		mc := &kardinalv1alpha1.MetricCheck{ObjectMeta: metav1.ObjectMeta{Name: "error-rate", Namespace: ns}}
		var out []string
		for _, req := range r.metricCheckRequests(context.Background(), mc) {
			out = append(out, req.Namespace+"/"+req.Name)
		}
		return out
	}
	assert.ElementsMatch(t, []string{
		"platform-policies/app-v1-prod-local", "team-a/app-v1-prod-org", "team-b/app-v1-prod-org",
	}, names("platform-policies"))
	assert.ElementsMatch(t, []string{"team-a/app-v1-prod-org", "team-a/app-v1-prod-team"}, names("team-a"))

	r.PolicyNamespaces = []string{"org-a"}
	assert.ElementsMatch(t, []string{"platform-policies/app-v1-prod-local"}, names("platform-policies"),
		"platform-policies is not an org policy namespace here")
}

// TestExprRefersToMetric covers the stale-metric note in status.reason (#1302):
// it names a metric only when the expression refers to it.
func TestExprRefersToMetric(t *testing.T) {
	tests := []struct {
		expr, name string
		want       bool
	}{
		{expr: `metrics["error-rate"].result == "Pass"`, name: "error-rate", want: true},
		{expr: `metrics['error-rate'].result == "Pass"`, name: "error-rate", want: true},
		{expr: `metrics.latency.result == "Pass"`, name: "latency", want: true},
		{expr: `metrics.latency_p99.result == "Pass"`, name: "latency", want: false},
		{expr: `metrics.latency_p99.result == "Pass" && metrics.latency.stale`, name: "latency", want: true},
		{expr: `metrics["error-rate"].result == "Pass"`, name: "rate", want: false},
		{expr: `!schedule.isWeekend`, name: "error-rate", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.expr+"/"+tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, exprRefersToMetric(tt.expr, tt.name))
		})
	}
}

// TestUnstartedStepCreated covers #1300: a new PromotionStep that has not
// started re-evaluates its required gates, since it starts only on results
// evaluated at or after it was created. Nothing else about steps re-evaluates
// a gate.
func TestUnstartedStepCreated(t *testing.T) {
	step := func(state string, gates ...string) *kardinalv1alpha1.PromotionStep {
		return &kardinalv1alpha1.PromotionStep{
			ObjectMeta: metav1.ObjectMeta{Name: "app-v1-prod", Namespace: "default"},
			Spec:       kardinalv1alpha1.PromotionStepSpec{RequiredGates: gates},
			Status:     kardinalv1alpha1.PromotionStepStatus{State: state},
		}
	}
	tests := []struct {
		name string
		step *kardinalv1alpha1.PromotionStep
		want bool
	}{
		{name: "new step with a gate", step: step("", "app-v1-prod-soak"), want: true},
		{name: "Pending step with a gate", step: step("Pending", "app-v1-prod-soak"), want: true},
		{name: "new step without gates", step: step(""), want: false},
		{name: "started step (initial list at start)", step: step("Promoting", "app-v1-prod-soak"), want: false},
		{name: "Verified step (initial list at start)", step: step("Verified", "app-v1-prod-soak"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, unstartedStepCreated.Create(event.CreateEvent{Object: tt.step}))
		})
	}
	s := step("", "app-v1-prod-soak")
	assert.False(t, unstartedStepCreated.Update(event.UpdateEvent{ObjectOld: s, ObjectNew: s}), "update")
	assert.False(t, unstartedStepCreated.Delete(event.DeleteEvent{Object: s}), "delete")
	assert.False(t, unstartedStepCreated.Generic(event.GenericEvent{Object: s}), "generic")
	assert.False(t, unstartedStepCreated.Create(event.CreateEvent{Object: &kardinalv1alpha1.PolicyGate{}}), "not a step")
}

// TestScheduleClockTicked checks that only a new status.tick re-evaluates the
// gates: a clock created, deleted, relabelled or with a new interval does not.
func TestScheduleClockTicked(t *testing.T) {
	clock := func(mutate func(c *kardinalv1alpha1.ScheduleClock)) *kardinalv1alpha1.ScheduleClock {
		c := &kardinalv1alpha1.ScheduleClock{
			ObjectMeta: metav1.ObjectMeta{Name: "fast", Namespace: "team-a", Generation: 1, ResourceVersion: "10"},
			Spec:       kardinalv1alpha1.ScheduleClockSpec{Interval: "1s"},
			Status:     kardinalv1alpha1.ScheduleClockStatus{Tick: "2026-10-01T07:27:26Z"},
		}
		if mutate != nil {
			mutate(c)
		}
		return c
	}
	tests := []struct {
		name   string
		old    func(c *kardinalv1alpha1.ScheduleClock)
		mutate func(c *kardinalv1alpha1.ScheduleClock)
		want   bool
	}{
		{name: "tick", want: true, mutate: func(c *kardinalv1alpha1.ScheduleClock) {
			c.Status.Tick = "2026-10-01T07:27:31Z"
			c.ResourceVersion = "11"
		}},
		{name: "first tick", want: true, old: func(c *kardinalv1alpha1.ScheduleClock) {
			c.Status.Tick = ""
			c.ResourceVersion = "9"
		}},
		{name: "resync", want: false},
		{name: "deleting (finalizer removed)", want: false, mutate: func(c *kardinalv1alpha1.ScheduleClock) {
			now := metav1.NewTime(time.Date(2026, 10, 1, 7, 27, 27, 0, time.UTC))
			c.DeletionTimestamp = &now
			c.ResourceVersion = "11"
		}},
		{name: "label", want: false, mutate: func(c *kardinalv1alpha1.ScheduleClock) {
			c.Labels = map[string]string{"team": "a"}
			c.ResourceVersion = "11"
		}},
		{name: "interval", want: false, mutate: func(c *kardinalv1alpha1.ScheduleClock) {
			c.Spec.Interval = "1m"
			c.Generation = 2
			c.ResourceVersion = "11"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldC, newC := clock(tt.old), clock(tt.mutate)
			assert.Equal(t, tt.want, scheduleClockTicked.Update(event.UpdateEvent{ObjectOld: oldC, ObjectNew: newC}))
		})
	}
	c := clock(nil)
	assert.False(t, scheduleClockTicked.Create(event.CreateEvent{Object: c}), "create (also the initial list at start)")
	assert.False(t, scheduleClockTicked.Delete(event.DeleteEvent{Object: c}), "delete")
	assert.False(t, scheduleClockTicked.Generic(event.GenericEvent{Object: c}), "generic")
	assert.False(t, scheduleClockTicked.Update(event.UpdateEvent{ObjectOld: &kardinalv1alpha1.PolicyGate{},
		ObjectNew: &kardinalv1alpha1.PolicyGate{}}), "not a clock")
}

// TestStepRequiredGateRequests covers #1300: a step create enqueues each gate
// in its spec.requiredGates, in the step's namespace.
func TestStepRequiredGateRequests(t *testing.T) {
	ps := &kardinalv1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v1-prod", Namespace: "team-a"},
		Spec:       kardinalv1alpha1.PromotionStepSpec{RequiredGates: []string{"app-v1-prod-soak", "app-v1-prod-hours"}},
	}
	reqs := stepRequiredGateRequests(context.Background(), ps)
	require.Len(t, reqs, 2)
	assert.Equal(t, "team-a", reqs[0].Namespace)
	assert.Equal(t, "app-v1-prod-soak", reqs[0].Name)
	assert.Equal(t, "team-a", reqs[1].Namespace)
	assert.Equal(t, "app-v1-prod-hours", reqs[1].Name)
	assert.Empty(t, stepRequiredGateRequests(context.Background(), &kardinalv1alpha1.PolicyGate{}), "not a step")
}

// handlerAddedInformer is a fake informer that closes added once the watch
// has added its event handler.
type handlerAddedInformer struct {
	*controllertest.FakeInformer
	added chan struct{}
}

func (i *handlerAddedInformer) AddEventHandlerWithOptions(h toolscache.ResourceEventHandler,
	opts toolscache.HandlerOptions) (toolscache.ResourceEventHandlerRegistration, error) {
	reg, err := i.FakeInformer.AddEventHandlerWithOptions(h, opts)
	close(i.added)
	return reg, err
}

// TestScheduleClockWatchOnlyTicks checks the ScheduleClock watch that
// SetupWithManager registers: a clock event lists the gates to re-evaluate
// only when status.tick changed. TestScheduleClockTicked covers the predicate;
// this covers that the watch carries it. Without it every clock create,
// delete and spec, label or status write re-evaluated every gate.
func TestScheduleClockWatchOnlyTicks(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(scheme))
	gv := kardinalv1alpha1.GroupVersion
	clocks := &handlerAddedInformer{FakeInformer: controllertest.NewFakeInformer(controllertest.Synced),
		added: make(chan struct{})}
	informers := &informertest.FakeInformers{Scheme: scheme,
		InformersByGVK: map[schema.GroupVersionKind]toolscache.SharedIndexInformer{gv.WithKind("ScheduleClock"): clocks}}
	// Every watched kind has its informer up front: the watches start in
	// parallel, and FakeInformers adds a missing informer without a lock.
	for _, kind := range []string{"PolicyGate", "MetricCheck", "ChangeWindow", "PromotionStep", "Pipeline"} {
		informers.InformersByGVK[gv.WithKind(kind)] = controllertest.NewFakeInformer(controllertest.Synced)
	}
	var gateLists atomic.Int32 // the clock mapper lists the gates to enqueue
	c := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*kardinalv1alpha1.PolicyGateList); ok {
				gateLists.Add(1)
			}
			return c.List(ctx, list, opts...)
		},
	}).Build()
	mgr, err := ctrl.NewManager(&rest.Config{Host: "https://127.0.0.1:1"}, ctrl.Options{
		Scheme:                 scheme,
		NewCache:               func(*rest.Config, cache.Options) (cache.Cache, error) { return informers, nil },
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	r, err := NewReconciler(c)
	require.NoError(t, err)
	require.NoError(t, r.SetupWithManager(mgr))

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		assert.NoError(t, <-stopped)
	})
	select {
	case <-clocks.added:
	case <-time.After(30 * time.Second):
		t.Fatal("the ScheduleClock watch did not start")
	}

	clock := func(mutate func(c *kardinalv1alpha1.ScheduleClock)) *kardinalv1alpha1.ScheduleClock {
		c := &kardinalv1alpha1.ScheduleClock{
			ObjectMeta: metav1.ObjectMeta{Name: "fast", Namespace: "team-a", Generation: 1, ResourceVersion: "10"},
			Spec:       kardinalv1alpha1.ScheduleClockSpec{Interval: "1s"},
			Status:     kardinalv1alpha1.ScheduleClockStatus{Tick: "2026-10-01T07:27:26Z"},
		}
		if mutate != nil {
			mutate(c)
		}
		return c
	}
	clocks.Add(clock(nil))
	clocks.Update(clock(nil), clock(func(c *kardinalv1alpha1.ScheduleClock) {
		c.Labels = map[string]string{"team": "a"}
		c.ResourceVersion = "11"
	}))
	clocks.Update(clock(nil), clock(func(c *kardinalv1alpha1.ScheduleClock) {
		c.Spec.Interval = "1m"
		c.Generation = 2
		c.ResourceVersion = "11"
	}))
	clocks.Delete(clock(nil))
	assert.Zero(t, gateLists.Load(), "only a tick re-evaluates the gates")

	clocks.Update(clock(nil), clock(func(c *kardinalv1alpha1.ScheduleClock) {
		c.Status.Tick = "2026-10-01T07:27:31Z"
		c.ResourceVersion = "11"
	}))
	assert.Positive(t, gateLists.Load(), "a tick re-evaluates the gates")
}
