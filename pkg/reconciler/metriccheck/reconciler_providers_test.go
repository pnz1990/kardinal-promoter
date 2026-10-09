// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package metriccheck_test

import (
	"context"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
)

// valueBackend is a Backend that returns a fixed value and records the spec
// and the Secret value it was given.
type valueBackend struct {
	value  metriccheck.Value
	calls  int
	secret string
}

func (b *valueBackend) Evaluate(ctx context.Context, q metriccheck.Query) (metriccheck.Value, error) {
	b.calls++
	if q.Spec.Web != nil && len(q.Spec.Web.Headers) > 0 && q.Spec.Web.Headers[0].ValueFromSecret != nil {
		v, err := q.Secret(ctx, *q.Spec.Web.Headers[0].ValueFromSecret)
		if err != nil {
			return metriccheck.Value{}, err
		}
		b.secret = v
	}
	return b.value, nil
}

func schemeWithCore() *runtime.Scheme {
	s := buildScheme()
	_ = corev1.AddToScheme(s)
	return s
}

// run reconciles mc twice with backends and returns the stored MetricCheck
// after each reconcile and the results.
func run(t *testing.T, mc *kardinalv1alpha1.MetricCheck, backends map[string]metriccheck.Backend,
	objs ...client.Object) (first, second *kardinalv1alpha1.MetricCheck, res ctrl.Result) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(schemeWithCore()).WithStatusSubresource(mc).
		WithObjects(append([]client.Object{mc}, objs...)...).Build()
	r := &metriccheck.Reconciler{Client: c, Backends: backends, NowFn: func() time.Time { return fixedNow }}
	get := func() *kardinalv1alpha1.MetricCheck {
		var got kardinalv1alpha1.MetricCheck
		require.NoError(t, c.Get(context.Background(), key(mc), &got))
		return &got
	}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(mc)})
	require.NoError(t, err)
	first = get()
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(mc)})
	require.NoError(t, err)
	return first, get(), res
}

func webCheck(op string, text *string, value float64) *kardinalv1alpha1.MetricCheck {
	return &kardinalv1alpha1.MetricCheck{
		ObjectMeta: metav1.ObjectMeta{Name: "smoke", Namespace: "default"},
		Spec: kardinalv1alpha1.MetricCheckSpec{
			Provider: "web", Interval: "30s",
			Web: &kardinalv1alpha1.WebProviderSpec{URL: "http://svc/check", JSONPath: "{.status}",
				Headers: []kardinalv1alpha1.WebHeader{{Name: "Authorization",
					ValueFromSecret: &kardinalv1alpha1.SecretKeyRef{Name: "creds", Key: "auth"}}}},
			Threshold: kardinalv1alpha1.MetricThreshold{Operator: op, Text: text, Value: value},
		},
	}
}

// TestReconciler_TextThreshold: provider web's text values are compared with
// threshold.text (eq, ne); a numeric threshold needs a numeric value. The
// Secret a header names is read from the MetricCheck's namespace, trimmed.
func TestReconciler_TextThreshold(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "default",
		Labels: map[string]string{"kardinal.io/referenceable": "true"}},
		Data: map[string][]byte{"auth": []byte("Bearer t0ken\n")}}
	tests := []struct {
		name       string
		mc         *kardinalv1alpha1.MetricCheck
		value      metriccheck.Value
		wantResult string
		wantReason string
	}{
		{"text eq passes", webCheck("eq", strPtr("healthy"), 0), metriccheck.Value{Text: "healthy"}, "Pass", `"healthy" eq "healthy" = true`},
		{"text eq fails", webCheck("eq", strPtr("healthy"), 0), metriccheck.Value{Text: "degraded"}, "Fail", `"degraded" eq "healthy" = false`},
		{"text ne passes", webCheck("ne", strPtr("down"), 0), metriccheck.Value{Text: "up"}, "Pass", `"up" ne "down" = true`},
		{"text with lt is refused", webCheck("lt", strPtr("x"), 0), metriccheck.Value{Text: "x"}, "Fail", `cannot compare text`},
		{"number ne", webCheck("ne", nil, 0), metriccheck.NumberValue(1), "Pass", "1 ne 0 = true"},
		{"text against a number", webCheck("lt", nil, 1), metriccheck.Value{Text: "healthy"}, "Fail", `value "healthy" is not a number`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &valueBackend{value: tt.value}
			got, _, res := run(t, tt.mc, map[string]metriccheck.Backend{"web": b}, secret)
			assert.Equal(t, tt.wantResult, got.Status.Result)
			assert.Contains(t, got.Status.Reason, tt.wantReason)
			assert.Equal(t, tt.value.Text, got.Status.LastValue)
			assert.Equal(t, 30*time.Second, res.RequeueAfter)
			assert.Equal(t, "Bearer t0ken", b.secret)
		})
	}

	t.Run("missing secret fails closed", func(t *testing.T) {
		b := &valueBackend{value: metriccheck.Value{Text: "healthy"}}
		got, _, _ := run(t, webCheck("eq", strPtr("healthy"), 0), map[string]metriccheck.Backend{"web": b})
		assert.Equal(t, "Fail", got.Status.Result)
		assert.Equal(t, `web query error: secret "creds" not found`, got.Status.Reason)
		require.NotNil(t, got.Status.ValidUntil)
	})
}

// TestReconciler_PerPromotionTemplate: a template is never queried. Its
// status gets ReasonTemplate (any result from before it was a template is
// cleared) and no requeue; a second reconcile writes nothing (idempotent).
// Unknown placeholders are named in the reason.
func TestReconciler_PerPromotionTemplate(t *testing.T) {
	for name, tc := range map[string]struct {
		query, reason string
	}{
		"known placeholders": {`err{v="{{ bundle.version }}"}`, metriccheck.ReasonTemplate},
		"unknown placeholder": {`err{v="{{ bundle.nope }}"}`,
			metriccheck.ReasonTemplate + ". Unknown placeholder {{ bundle.nope }}: instances will fail"},
	} {
		t.Run(name, func(t *testing.T) {
			mc := newMetricCheck("error-rate", "lt", 1)
			mc.Spec.Query = tc.query
			mc.Spec.PerPromotion = true
			mc.Status = kardinalv1alpha1.MetricCheckStatus{Result: "Pass", LastValue: "0", ValidUntil: &metav1.Time{Time: fixedNow}}
			b := &valueBackend{value: metriccheck.NumberValue(0)}
			first, second, res := run(t, mc, map[string]metriccheck.Backend{"prometheus": b})
			assert.Zero(t, b.calls, "a template is not queried")
			assert.Equal(t, ctrl.Result{}, res, "no requeue")
			assert.Equal(t, kardinalv1alpha1.MetricCheckStatus{Reason: tc.reason}, first.Status)
			assert.Equal(t, first.ResourceVersion, second.ResourceVersion, "the second reconcile writes nothing")
		})
	}
}

// TestReconciler_Suspended: a suspended MetricCheck is not queried, keeps its
// last result (it goes stale at validUntil) and is idempotent.
func TestReconciler_Suspended(t *testing.T) {
	mc := newMetricCheck("error-rate", "lt", 1)
	mc.Spec.Suspend = true
	until := metav1.NewTime(fixedNow.Add(time.Minute))
	mc.Status = kardinalv1alpha1.MetricCheckStatus{Result: "Pass", LastValue: "0", ValidUntil: &until}
	b := &valueBackend{value: metriccheck.NumberValue(0)}
	first, second, res := run(t, mc, map[string]metriccheck.Backend{"prometheus": b})
	assert.Zero(t, b.calls)
	assert.Equal(t, ctrl.Result{}, res)
	assert.Equal(t, metriccheck.ReasonSuspended, first.Status.Reason)
	assert.Equal(t, "Pass", first.Status.Result)
	require.NotNil(t, first.Status.ValidUntil)
	assert.True(t, first.Status.ValidUntil.Equal(&until), "validUntil is not extended")
	assert.Equal(t, first.ResourceVersion, second.ResourceVersion)
}

// TestReconciler_UnrenderedPlaceholderFails: a queried MetricCheck with a
// placeholder left in it fails without a query, so a value the Graph refused
// to substitute never reaches a backend.
func TestReconciler_UnrenderedPlaceholderFails(t *testing.T) {
	mc := newMetricCheck("error-rate", "lt", 1)
	mc.Spec.Query = `err{tag="{{ bundle.imageTag }}"}`
	b := &valueBackend{value: metriccheck.NumberValue(0)}
	got, _, res := run(t, mc, map[string]metriccheck.Backend{"prometheus": b})
	assert.Zero(t, b.calls)
	assert.Equal(t, "Fail", got.Status.Result)
	assert.Contains(t, got.Status.Reason, "unrendered placeholder {{ bundle.imageTag }}")
	assert.Equal(t, 30*time.Second, res.RequeueAfter)
}

// TestReconciler_ProviderSelection: the backend is chosen by spec.provider;
// without one the check fails; the legacy Provider serves prometheus only
// without authorization.
func TestReconciler_ProviderSelection(t *testing.T) {
	mc := newMetricCheck("m", "lt", 1)
	mc.Spec.Provider = "datadog"
	got, _, _ := run(t, mc, map[string]metriccheck.Backend{"prometheus": &valueBackend{}})
	assert.Equal(t, "Fail", got.Status.Result)
	assert.Equal(t, `datadog query error: provider "datadog" is not available`, got.Status.Reason)

	auth := newMetricCheck("m", "lt", 1)
	auth.Spec.Prometheus = &kardinalv1alpha1.PrometheusProviderSpec{
		AuthorizationSecretRef: &kardinalv1alpha1.SecretKeyRef{Name: "p", Key: "a"}}
	c := fake.NewClientBuilder().WithScheme(schemeWithCore()).WithStatusSubresource(auth).WithObjects(auth).Build()
	r := &metriccheck.Reconciler{Client: c, Provider: &fakeProvider{value: 0}, NowFn: func() time.Time { return fixedNow }}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(auth)})
	require.NoError(t, err)
	var stored kardinalv1alpha1.MetricCheck
	require.NoError(t, c.Get(context.Background(), key(auth), &stored))
	assert.Equal(t, "Fail", stored.Status.Result)
	assert.Contains(t, stored.Status.Reason, "authorization is not supported")
}

// TestPrometheusProvider_Authorization: the Authorization header comes from
// the Secret spec.prometheus.authorizationSecretRef names.
func TestPrometheusProvider_Authorization(t *testing.T) {
	srv, rec := serve(t, 200, `{"status":"success","data":{"resultType":"scalar","result":[1,"0.5"]}}`)
	p := &metriccheck.PrometheusProvider{HTTPClient: plainClient}
	spec := &kardinalv1alpha1.MetricCheckSpec{Provider: "prometheus", PrometheusURL: srv.URL, Query: "up",
		Prometheus: &kardinalv1alpha1.PrometheusProviderSpec{
			AuthorizationSecretRef: &kardinalv1alpha1.SecretKeyRef{Name: "p", Key: "a"}}}
	v, err := p.Evaluate(context.Background(), metriccheck.Query{Spec: spec,
		Secret: secrets(map[string]string{"p/a": "Bearer prom"}), Now: queryNow})
	require.NoError(t, err)
	assert.Equal(t, 0.5, v.Number)
	assert.Equal(t, "Bearer prom", rec.req.Header.Get("Authorization"))
	assert.Equal(t, http.MethodGet, rec.req.Method)

	_, err = p.Evaluate(context.Background(), metriccheck.Query{Spec: spec, Secret: secrets(nil), Now: queryNow})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prometheus authorization")
}

func strPtr(s string) *string { return &s }

// TestReconciler_NonFiniteFails (QA #1479): NaN and ±Inf (a division by
// zero) are never a pass, whatever the operator; ne used to pass on NaN.
func TestReconciler_NonFiniteFails(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, op := range []string{"ne", "lt", "gt"} {
			mc := newMetricCheck("m", op, 0)
			got, _, _ := run(t, mc, map[string]metriccheck.Backend{"prometheus": &valueBackend{value: metriccheck.NumberValue(v)}})
			assert.Equal(t, "Fail", got.Status.Result, "%v %s 0", v, op)
			assert.Contains(t, got.Status.Reason, "is not a finite number")
		}
	}
}

// TestReconciler_WaitsForSlot: an overdue check that finds no free query
// slot writes WaitingForSlot (keeping its last result, which still goes
// stale), sends nothing, and asks again in 250ms; once the slot is free it
// queries. A check that is not overdue waits without writing.
func TestReconciler_WaitsForSlot(t *testing.T) {
	mc := newMetricCheck("m", "lt", 1)
	until := metav1.NewTime(fixedNow.Add(time.Minute))
	last := metav1.NewTime(fixedNow.Add(-time.Minute)) // overdue: the interval is 30s
	mc.Status = kardinalv1alpha1.MetricCheckStatus{Result: "Pass", LastValue: "0", ValidUntil: &until, LastEvaluatedAt: &last}
	c := fake.NewClientBuilder().WithScheme(schemeWithCore()).WithStatusSubresource(mc).WithObjects(mc).Build()
	b := &valueBackend{value: metriccheck.NumberValue(0)}
	lim := metriccheck.NewLimiter(1, 1)
	hold, ok := lim.TryAcquire(types.NamespacedName{Namespace: "default", Name: "other"})
	require.True(t, ok)
	r := &metriccheck.Reconciler{Client: c, Backends: map[string]metriccheck.Backend{"prometheus": b},
		Limiter: lim, NowFn: func() time.Time { return fixedNow }}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(mc)})
	require.NoError(t, err)
	assert.Equal(t, 250*time.Millisecond, res.RequeueAfter)
	assert.Zero(t, b.calls, "nothing is sent while waiting")
	var got kardinalv1alpha1.MetricCheck
	require.NoError(t, c.Get(context.Background(), key(mc), &got))
	assert.Equal(t, metriccheck.ReasonWaitingForSlot, got.Status.Reason)
	assert.Equal(t, "Pass", got.Status.Result)
	assert.True(t, got.Status.ValidUntil.Equal(&until), "validUntil is not extended")

	fresh := newMetricCheck("fresh", "lt", 1)
	recent := metav1.NewTime(fixedNow.Add(-5 * time.Second))
	fresh.Status = kardinalv1alpha1.MetricCheckStatus{Result: "Pass", LastEvaluatedAt: &recent, ValidUntil: &until}
	require.NoError(t, c.Create(context.Background(), fresh))
	require.NoError(t, c.Status().Update(context.Background(), fresh))
	var before kardinalv1alpha1.MetricCheck
	require.NoError(t, c.Get(context.Background(), key(fresh), &before))
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(fresh)})
	require.NoError(t, err)
	var after kardinalv1alpha1.MetricCheck
	require.NoError(t, c.Get(context.Background(), key(fresh), &after))
	assert.Equal(t, before.ResourceVersion, after.ResourceVersion, "a short wait writes nothing")

	hold()
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key(mc)})
	require.NoError(t, err)
	assert.Equal(t, 1, b.calls)
	require.NoError(t, c.Get(context.Background(), key(mc), &got))
	assert.Equal(t, "0 lt 1 = true", got.Status.Reason)
}
