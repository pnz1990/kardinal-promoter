// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
)

// fakeProvider is a MetricsProvider implementation for testing.
type fakeProvider struct {
	value float64
	err   error
}

func (f *fakeProvider) QueryScalar(_ context.Context, _, _ string) (float64, error) {
	return f.value, f.err
}

// fixedNow is the reference time used throughout tests.
var fixedNow = time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

// buildScheme returns a runtime.Scheme with kardinal types registered.
func buildScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = kardinalv1alpha1.AddToScheme(s)
	return s
}

// newMetricCheck builds a MetricCheck with the given operator and threshold.
func newMetricCheck(name, op string, threshold float64) *kardinalv1alpha1.MetricCheck {
	return &kardinalv1alpha1.MetricCheck{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: kardinalv1alpha1.MetricCheckSpec{
			Provider:      "prometheus",
			PrometheusURL: "http://prometheus:9090",
			Query:         `http_requests_error_rate`,
			Interval:      "30s",
			Threshold: kardinalv1alpha1.MetricThreshold{
				Value:    threshold,
				Operator: op,
			},
		},
	}
}

// key returns the NamespacedName for the given MetricCheck.
func key(mc *kardinalv1alpha1.MetricCheck) types.NamespacedName {
	return types.NamespacedName{Name: mc.Name, Namespace: mc.Namespace}
}

// reconcileOnce creates a fake client with mc and runs one reconcile cycle.
func reconcileOnce(t *testing.T, mc *kardinalv1alpha1.MetricCheck, provider metriccheck.MetricsProvider) *kardinalv1alpha1.MetricCheck {
	t.Helper()
	s := buildScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(mc).WithObjects(mc).Build()

	r := &metriccheck.Reconciler{
		Client:   fakeClient,
		Provider: provider,
		NowFn:    func() time.Time { return fixedNow },
	}

	req := ctrl.Request{NamespacedName: key(mc)}
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var updated kardinalv1alpha1.MetricCheck
	require.NoError(t, fakeClient.Get(context.Background(), key(mc), &updated))
	return &updated
}

// --- Test: metric below threshold passes "lt" gate ---

func TestReconciler_LtOperator_Pass(t *testing.T) {
	mc := newMetricCheck("error-rate", "lt", 0.01)
	result := reconcileOnce(t, mc, &fakeProvider{value: 0.005})

	assert.Equal(t, "Pass", result.Status.Result)
	assert.Equal(t, "0.005", result.Status.LastValue)
	require.NotNil(t, result.Status.LastEvaluatedAt)
	assert.Equal(t, fixedNow.UTC(), result.Status.LastEvaluatedAt.UTC())
}

// --- Test: metric above threshold fails "lt" gate ---

func TestReconciler_LtOperator_Fail(t *testing.T) {
	mc := newMetricCheck("error-rate", "lt", 0.01)
	result := reconcileOnce(t, mc, &fakeProvider{value: 0.05})

	assert.Equal(t, "Fail", result.Status.Result)
	assert.Equal(t, "0.05", result.Status.LastValue)
}

// --- Test: gt operator ---

func TestReconciler_GtOperator_Pass(t *testing.T) {
	mc := newMetricCheck("availability", "gt", 0.99)
	result := reconcileOnce(t, mc, &fakeProvider{value: 0.999})
	assert.Equal(t, "Pass", result.Status.Result)
}

func TestReconciler_GtOperator_Fail(t *testing.T) {
	mc := newMetricCheck("availability", "gt", 0.99)
	result := reconcileOnce(t, mc, &fakeProvider{value: 0.9})
	assert.Equal(t, "Fail", result.Status.Result)
}

// --- Test: lte operator ---

func TestReconciler_LteOperator_PassEqual(t *testing.T) {
	mc := newMetricCheck("latency", "lte", 100.0)
	result := reconcileOnce(t, mc, &fakeProvider{value: 100.0})
	assert.Equal(t, "Pass", result.Status.Result)
}

// --- Test: gte operator ---

func TestReconciler_GteOperator_PassEqual(t *testing.T) {
	mc := newMetricCheck("uptime", "gte", 99.9)
	result := reconcileOnce(t, mc, &fakeProvider{value: 99.9})
	assert.Equal(t, "Pass", result.Status.Result)
}

// --- Test: eq operator ---

func TestReconciler_EqOperator_Pass(t *testing.T) {
	mc := newMetricCheck("replicas", "eq", 3.0)
	result := reconcileOnce(t, mc, &fakeProvider{value: 3.0})
	assert.Equal(t, "Pass", result.Status.Result)
}

// --- Test: prometheus query error → Fail with empty lastValue ---

func TestReconciler_QueryError_SetsFail(t *testing.T) {
	mc := newMetricCheck("error-rate", "lt", 0.01)
	result := reconcileOnce(t, mc, &fakeProvider{err: fmt.Errorf("connection refused")})

	assert.Equal(t, "Fail", result.Status.Result)
	assert.Empty(t, result.Status.LastValue)
	assert.Contains(t, result.Status.Reason, "prometheus query error")
}

// --- Test: unknown operator → Fail ---

func TestReconciler_UnknownOperator_SetsFail(t *testing.T) {
	mc := newMetricCheck("error-rate", "neq", 0.0)
	result := reconcileOnce(t, mc, &fakeProvider{value: 0.0})
	assert.Equal(t, "Fail", result.Status.Result)
	assert.Contains(t, result.Status.Reason, "unknown operator")
}

// --- Test: idempotency — reconcile twice gives same result ---

func TestReconciler_Idempotent(t *testing.T) {
	mc := newMetricCheck("error-rate", "lt", 0.01)
	s := buildScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(mc).WithObjects(mc).Build()
	r := &metriccheck.Reconciler{
		Client:   fakeClient,
		Provider: &fakeProvider{value: 0.005},
		NowFn:    func() time.Time { return fixedNow },
	}
	req := ctrl.Request{NamespacedName: key(mc)}

	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)

	var updated kardinalv1alpha1.MetricCheck
	require.NoError(t, fakeClient.Get(context.Background(), key(mc), &updated))
	assert.Equal(t, "Pass", updated.Status.Result)
}

// --- Test: not-found is a no-op (no error) ---

func TestReconciler_NotFound_NoOp(t *testing.T) {
	s := buildScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(s).Build()
	r := &metriccheck.Reconciler{
		Client:   fakeClient,
		Provider: &fakeProvider{value: 0.0},
	}
	req := ctrl.Request{NamespacedName: client.ObjectKey{Name: "missing", Namespace: "default"}}
	result, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// --- Test: requeue interval from spec ---

func TestReconciler_RequeueAfterInterval(t *testing.T) {
	mc := newMetricCheck("error-rate", "lt", 0.01)
	s := buildScheme()
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(mc).WithObjects(mc).Build()
	r := &metriccheck.Reconciler{
		Client:   fakeClient,
		Provider: &fakeProvider{value: 0.005},
		NowFn:    func() time.Time { return fixedNow },
	}
	req := ctrl.Request{NamespacedName: key(mc)}
	res, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, res.RequeueAfter, "should requeue after spec.interval=30s")
}

// Each evaluation writes status.validUntil = lastEvaluatedAt + max(3 x interval,
// 30s), after which PolicyGates treat the result as stale (#1302). A failed
// query writes it too: Fail is a fresh result.
func TestReconciler_WritesValidUntil(t *testing.T) {
	tests := []struct {
		name     string
		interval string
		provider *fakeProvider
		want     time.Duration
	}{
		{name: "3 x interval", interval: "5m", provider: &fakeProvider{value: 0.005}, want: 15 * time.Minute},
		{name: "default interval", interval: "", provider: &fakeProvider{value: 0.005}, want: 3 * time.Minute},
		{name: "raised minimum interval", interval: "1s", provider: &fakeProvider{value: 0.005}, want: 30 * time.Second},
		{name: "query error", interval: "1m", provider: &fakeProvider{err: fmt.Errorf("connection refused")}, want: 3 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMetricCheck("error-rate", "lt", 0.01)
			mc.Spec.Interval = tt.interval
			result := reconcileOnce(t, mc, tt.provider)

			require.NotNil(t, result.Status.ValidUntil)
			assert.Equal(t, fixedNow.Add(tt.want), result.Status.ValidUntil.UTC())
		})
	}
}

// countingProvider counts the queries it answers.
type countingProvider struct {
	fakeProvider
	queries int
}

func (c *countingProvider) QueryScalar(ctx context.Context, url, query string) (float64, error) {
	c.queries++
	return c.fakeProvider.QueryScalar(ctx, url, query)
}

// TestReconciler_StatusWriteFailureRequeuesAtInterval proves bug 10 of the
// health spike fixed: when the status write fails (for example an admission
// policy denies it), the MetricCheck is evaluated again soon, not in
// controller-runtime's error backoff, which retries at once and then doubles
// up to 1000s, querying Prometheus on every retry. The first failure after a
// write that worked is retried after min(interval, 5s), since a conflict or
// a brief API server outage is likely gone by then; while the write keeps
// failing, it is retried at spec.interval. A write that works ends the run,
// so the next failure is retried early again. Each reconcile starts when the
// previous one asked to be requeued. The result the PolicyGates read goes
// stale at status.validUntil, so a gate still fails closed.
func TestReconciler_StatusWriteFailureRequeuesAtInterval(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider *countingProvider
		written  bool // the MetricCheck has a result from a write that worked
	}{
		{name: "evaluated", provider: &countingProvider{fakeProvider: fakeProvider{value: 0.005}}, written: true},
		{name: "query error", provider: &countingProvider{fakeProvider: fakeProvider{err: fmt.Errorf("connection refused")}}, written: true},
		{name: "never written", provider: &countingProvider{fakeProvider: fakeProvider{value: 0.005}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := fixedNow
			mc := newMetricCheck("error-rate", "lt", 0.01)
			mc.CreationTimestamp = metav1.NewTime(now.Add(-time.Hour))
			if tt.written {
				last := metav1.NewTime(now.Add(-30 * time.Second)) // one interval ago
				mc.Status = kardinalv1alpha1.MetricCheckStatus{Result: "Pass", LastEvaluatedAt: &last}
			} else {
				mc.CreationTimestamp = metav1.NewTime(now) // the first attempt runs as it is created
			}
			deny, denied := true, 0
			c := fake.NewClientBuilder().WithScheme(buildScheme()).WithStatusSubresource(mc).WithObjects(mc).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch,
						opts ...client.SubResourcePatchOption) error {
						if !deny {
							return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
						}
						denied++
						return fmt.Errorf("admission webhook denied the request")
					},
				}).Build()
			r := &metriccheck.Reconciler{Client: c, Provider: tt.provider, NowFn: func() time.Time { return now }}
			reconcile := func(want time.Duration, why string) {
				t.Helper()
				res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(mc)})
				require.NoError(t, err, "an error would put the MetricCheck into the error backoff")
				assert.Equal(t, want, res.RequeueAfter, why)
				now = now.Add(res.RequeueAfter)
			}

			reconcile(5*time.Second, "the first failed write is retried after min(30s, 5s)")
			reconcile(30*time.Second, "a write that failed again is retried at spec.interval")
			reconcile(30*time.Second, "and so on while it keeps failing")
			assert.Equal(t, 3, denied)
			assert.Equal(t, 3, tt.provider.queries, "one query per evaluation")

			deny = false
			reconcile(30*time.Second, "a write that works is followed by the next evaluation at spec.interval")
			deny = true
			reconcile(5*time.Second, "after a write that worked, the first failure is retried early again")
			reconcile(30*time.Second, "and the next one at spec.interval")
			assert.Equal(t, 5, denied)
		})
	}
}
