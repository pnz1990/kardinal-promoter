//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// metricCredsSecret is the Secret every provider test reads its credentials
// from: the keys below, set to what the fake metrics APIs accept.
const metricCredsSecret = "metric-creds"

// goodMetricCreds are the values the fake metrics APIs accept.
var goodMetricCreds = map[string]string{
	"dd-api": framework.FakeDatadogAPIKey, "dd-app": framework.FakeDatadogAppKey,
	"nr":     framework.FakeNewRelicAPIKey,
	"aws-id": framework.FakeAWSAccessKeyID, "aws-key": framework.FakeAWSSecretKey,
	"web": framework.FakeWebAuthorization,
}

func credRef(key string) v1alpha1.SecretKeyRef {
	return v1alpha1.SecretKeyRef{Name: metricCredsSecret, Key: key}
}

// providerCheck is a MetricCheck ns/name of provider that passes when the
// value is below 0.5, with credentials from metricCredsSecret and the fake
// metrics APIs as its endpoint.
func providerCheck(t *testing.T, ns, name, provider, query string) *v1alpha1.MetricCheck {
	t.Helper()
	base := framework.MetricsAPIURL(t)
	spec := v1alpha1.MetricCheckSpec{
		Provider: provider, Query: query, Interval: metricInterval,
		Threshold: v1alpha1.MetricThreshold{Operator: "lt", Value: 0.5},
	}
	switch provider {
	case "datadog":
		spec.Datadog = &v1alpha1.DatadogProviderSpec{Address: base + "/datadog", Window: "2m",
			APIKeySecretRef: credRef("dd-api"), ApplicationKeySecretRef: credRef("dd-app")}
	case "newrelic":
		spec.NewRelic = &v1alpha1.NewRelicProviderSpec{AccountID: 4242, Address: base + "/newrelic/graphql",
			APIKeySecretRef: credRef("nr")}
	case "cloudwatch":
		id, key := credRef("aws-id"), credRef("aws-key")
		spec.CloudWatch = &v1alpha1.CloudWatchProviderSpec{Region: "eu-west-1", Endpoint: base + "/cloudwatch/",
			AccessKeyIDSecretRef: &id, SecretAccessKeySecretRef: &key}
	}
	return &v1alpha1.MetricCheck{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"kardinal.io/e2e": "true"}},
		Spec:       spec,
	}
}

func float(v float64) *float64 { return &v }

// TestMetric_ProvidersWithSecretAuth runs a Datadog, a New Relic and a
// CloudWatch MetricCheck against the fake metrics APIs, which check the
// credentials as the real services do (DD-API-KEY and DD-APPLICATION-KEY,
// API-Key, a SigV4 signature over the request). Each passes and fails with
// the value its query returns. The credentials come only from a Secret: when
// the Secret is rotated to wrong values the next evaluation is refused and
// fails closed, with no credential in the reason; a Secret that does not
// exist fails too, and so does one without the label
// kardinal.io/referenceable: "true", before any request is sent.
//
// Covers METRIC-DD-01, METRIC-NR-01, METRIC-CW-01, METRIC-AUTH-01.
func TestMetric_ProvidersWithSecretAuth(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	e.CreateSecretData(t, ns, metricCredsSecret, true, goodMetricCreds)

	checks := []struct{ name, provider, query, refused string }{
		{"dd", "datadog", fmt.Sprintf("avg:e2e.errors{ns:%s}", ns), "datadog returned HTTP 403: Forbidden"},
		{"nr", "newrelic", fmt.Sprintf("SELECT average(errors) FROM E2E WHERE ns = '%s'", ns), "newrelic returned HTTP 401: Invalid API key"},
		{"cw", "cloudwatch", fmt.Sprintf(`SELECT AVG(Errors) FROM "E2E/%s"`, ns), "cloudwatch returned HTTP 403: SignatureDoesNotMatch"},
	}
	for _, c := range checks {
		e.SetFakeMetric(t, c.provider, c.query, float(0.2))
		e.CreateMetricCheck(t, providerCheck(t, ns, c.name, c.provider, c.query))
	}
	for _, c := range checks {
		e.WaitMetricCheck(t, ns, c.name, metricTimeout, "passing", framework.MetricResult("Pass", "0.2 lt 0.5 = true"))
		recs, err := e.FakeMetricRecords(ctx, c.provider, c.query)
		require.NoError(t, err)
		require.NotEmpty(t, recs, "%s: the fake got the query", c.provider)
		assert.Equal(t, "ok", recs[len(recs)-1].Auth, "%s: credentials accepted", c.provider)
	}

	for _, c := range checks {
		e.SetFakeMetric(t, c.provider, c.query, float(0.9))
	}
	for _, c := range checks {
		e.WaitMetricCheck(t, ns, c.name, metricTimeout, "failing", framework.MetricResult("Fail", "0.9 lt 0.5 = false"))
	}

	bad := map[string]string{}
	for k := range goodMetricCreds {
		bad[k] = "rotated-wrong-" + k
	}
	bad["aws-id"] = framework.FakeAWSAccessKeyID // a known key ID with the wrong secret: the signature is refused
	for _, c := range checks {
		e.SetFakeMetric(t, c.provider, c.query, float(0.2))
	}
	e.UpdateSecretData(t, ns, metricCredsSecret, bad)
	for _, c := range checks {
		mc := e.WaitMetricCheck(t, ns, c.name, metricTimeout, "refused with the rotated Secret",
			framework.MetricResult("Fail", c.refused))
		assert.Empty(t, mc.Status.LastValue)
		for _, v := range bad {
			assert.NotContains(t, mc.Status.Reason, v, "no credential in the reason")
		}
	}

	missing := providerCheck(t, ns, "missing-secret", "datadog", checks[0].query)
	missing.Spec.Datadog.APIKeySecretRef = v1alpha1.SecretKeyRef{Name: "nope", Key: "k"}
	e.CreateMetricCheck(t, missing)
	e.WaitMetricCheck(t, ns, "missing-secret", metricTimeout, "failing on the missing Secret",
		framework.MetricResult("Fail", `datadog query error: datadog API key: secret "nope" not found`))

	// A Secret its owner did not label kardinal.io/referenceable: "true" is
	// never sent anywhere.
	e.CreateSecretData(t, ns, "unlabelled", false, goodMetricCreds)
	unlabelledQuery := fmt.Sprintf("avg:e2e.unlabelled{ns:%s}", ns)
	unlabelled := providerCheck(t, ns, "unlabelled-secret", "datadog", unlabelledQuery)
	unlabelled.Spec.Datadog.APIKeySecretRef = v1alpha1.SecretKeyRef{Name: "unlabelled", Key: "dd-api"}
	unlabelled.Spec.Datadog.ApplicationKeySecretRef = v1alpha1.SecretKeyRef{Name: "unlabelled", Key: "dd-app"}
	e.CreateMetricCheck(t, unlabelled)
	e.WaitMetricCheck(t, ns, "unlabelled-secret", metricTimeout, "refusing the unlabelled Secret",
		framework.MetricResult("Fail", `SecretNotReferenceable: secret "unlabelled" does not have the label kardinal.io/referenceable: "true"`))
	sent, err := e.FakeMetricRecords(ctx, "datadog", unlabelledQuery)
	require.NoError(t, err)
	assert.Empty(t, sent, "no request was sent with the unlabelled Secret")
}

// TestMetric_WebProviderGate gates prod on two web MetricChecks against a
// JSON endpoint that needs an Authorization header from a Secret: one
// compares a JSONPath number with a threshold, the other a JSONPath string
// with threshold.text. While the endpoint reports degraded the gates block
// and prod gets no step; once it reports healthy both pass and prod is
// promoted. A check whose Secret holds the wrong token fails with the HTTP
// status, and one whose JSONPath selects nothing fails too.
//
// Covers METRIC-WEB-01, METRIC-WEB-02.
func TestMetric_WebProviderGate(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newFluxApp(t, e, "prod")
	e.CreateSecretData(t, a.ns, metricCredsSecret, true, goodMetricCreds)
	e.CreateSecretData(t, a.ns, "wrong-token", true, map[string]string{"web": "Bearer nope"})
	path := a.ns + "/health"
	e.SetFakeWebDoc(t, path, map[string]interface{}{"status": "degraded", "metrics": map[string]interface{}{"errorRate": 0.9}})

	web := func(name, jsonPath, secret string, threshold v1alpha1.MetricThreshold) *v1alpha1.MetricCheck {
		return &v1alpha1.MetricCheck{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.ns},
			Spec: v1alpha1.MetricCheckSpec{Provider: "web", Interval: metricInterval, Threshold: threshold,
				Web: &v1alpha1.WebProviderSpec{
					URL: framework.MetricsAPIURL(t) + "/web/" + path, JSONPath: jsonPath,
					Headers: []v1alpha1.WebHeader{{Name: "Authorization",
						ValueFromSecret: &v1alpha1.SecretKeyRef{Name: secret, Key: "web"}}},
				}},
		}
	}
	healthy := "healthy"
	e.CreateMetricCheck(t, web("error-rate", "{.metrics.errorRate}", metricCredsSecret, v1alpha1.MetricThreshold{Operator: "lt", Value: 0.5}))
	e.CreateMetricCheck(t, web("status", "{.status}", metricCredsSecret, v1alpha1.MetricThreshold{Operator: "eq", Text: &healthy}))
	e.CreateMetricCheck(t, web("unauthorized", "{.status}", "wrong-token", v1alpha1.MetricThreshold{Operator: "eq", Text: &healthy}))
	e.CreateMetricCheck(t, web("no-match", "{.nothing}", metricCredsSecret, v1alpha1.MetricThreshold{Operator: "eq", Text: &healthy}))

	e.WaitMetricCheck(t, a.ns, "error-rate", metricTimeout, "failing", framework.MetricResult("Fail", "0.9 lt 0.5 = false"))
	e.WaitMetricCheck(t, a.ns, "status", metricTimeout, "failing", framework.MetricResult("Fail", `"degraded" eq "healthy" = false`))
	e.WaitMetricCheck(t, a.ns, "unauthorized", metricTimeout, "refused", framework.MetricResult("Fail", "web query error: web returned HTTP 401"))
	e.WaitMetricCheck(t, a.ns, "no-match", metricTimeout, "failing", framework.MetricResult("Fail", "web jsonPath {.nothing} selected nothing"))

	gates := map[string]string{
		"web-error-rate": `metrics["error-rate"].result == "Pass"`,
		"web-status":     `metrics.status.result == "Pass" && metrics.status.value == "healthy"`,
	}
	for name, expr := range gates {
		e.CreateGate(t, framework.Gate(a.ns, name, "prod", expr, "5m"))
	}
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	for name, expr := range gates {
		e.WaitGateReady(t, a.ns, bundle, "prod", name, false, expr+" = false", gateTimeout)
	}
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)

	e.SetFakeWebDoc(t, path, map[string]interface{}{"status": "healthy", "metrics": map[string]interface{}{"errorRate": 0.01}})
	e.WaitMetricCheck(t, a.ns, "status", metricTimeout, "passing", framework.MetricResult("Pass", `"healthy" eq "healthy" = true`))
	for name, expr := range gates {
		e.WaitGateReady(t, a.ns, bundle, "prod", name, true, expr+" = true", gateTimeout)
	}
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	e.WaitMetricCheck(t, a.ns, "unauthorized", metricTimeout, "still refused", framework.MetricResult("Fail", "HTTP 401"))
}

// TestMetric_PerPromotionAnalysis gates prod on a per-promotion Datadog
// MetricCheck whose query names the Bundle version, the environment and the
// Pipeline. The template itself is never queried. The Bundle's Graph creates
// an instance for prod once test is Verified, with the placeholders replaced;
// while the fake returns no data for that exact query the instance fails, the
// gate blocks and prod gets no step. Once the query returns a passing value
// the gate passes and prod is promoted, and once the Bundle is Verified the
// Graph suspends the instance.
//
// Covers METRIC-TMPL-01, METRIC-TMPL-02, METRIC-TMPL-03.
func TestMetric_PerPromotionAnalysis(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newFluxApp(t, e, "test", "prod")
	e.CreateSecretData(t, a.ns, metricCredsSecret, true, goodMetricCreds)

	tmpl := providerCheck(t, a.ns, "canary", "datadog",
		"avg:e2e.errors{ns:"+a.ns+",version:{{ bundle.version }},env:{{ environment.name }},pipeline:{{ pipeline.name }}}")
	tmpl.Spec.PerPromotion = true
	e.CreateMetricCheck(t, tmpl)
	e.WaitMetricCheck(t, a.ns, "canary", metricTimeout, "marked as a template", func(mc *v1alpha1.MetricCheck) bool {
		return strings.HasPrefix(mc.Status.Reason, "Template: ") && mc.Status.Result == "" && mc.Status.ValidUntil == nil
	})

	const expr = `metrics["canary"].result == "Pass"`
	e.CreateGate(t, framework.Gate(a.ns, "canary-gate", "prod", expr, "5m"))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	rendered := fmt.Sprintf("avg:e2e.errors{ns:%s,version:%s,env:prod,pipeline:%s}", a.ns, fixtures.V2, pipelineName)

	testStep := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	labels := map[string]string{"kardinal.io/metric-template": "canary", "kardinal.io/bundle": bundle, "kardinal.io/environment": "prod"}
	var inst v1alpha1.MetricCheck
	framework.Eventually(t, time.Minute, "the Graph to create the prod instance of canary", func(ctx context.Context) (bool, string) {
		items, err := e.ListMetricChecks(ctx, a.ns, labels)
		if err != nil {
			return false, err.Error()
		}
		if len(items) != 1 {
			return false, fmt.Sprintf("%d instances", len(items))
		}
		inst = items[0]
		return true, ""
	})
	assert.Equal(t, rendered, inst.Spec.Query, "the placeholders are replaced with the Bundle's values")
	assert.False(t, inst.Spec.PerPromotion)
	assert.False(t, inst.Spec.Suspend)
	// The health check that verified test is its last one.
	require.NotNil(t, testStep.Status.LastHealthCheckAt)
	assert.False(t, inst.CreationTimestamp.Time.Before(testStep.Status.LastHealthCheckAt.Truncate(time.Second)),
		"the instance is created once test is Verified: created %s, test verified %s", inst.CreationTimestamp, testStep.Status.LastHealthCheckAt)

	e.WaitMetricCheck(t, a.ns, inst.Name, metricTimeout, "failing on no data", framework.MetricResult("Fail", "datadog query returned no series"))
	e.WaitGateReady(t, a.ns, bundle, "prod", "canary-gate", false, expr+" = false", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	tm, err := e.GetMetricCheck(ctx, a.ns, "canary")
	require.NoError(t, err)
	assert.Empty(t, tm.Status.Result, "the template is still not queried")

	e.SetFakeMetric(t, "datadog", rendered, float(0.1))
	e.WaitMetricCheck(t, a.ns, inst.Name, metricTimeout, "passing", framework.MetricResult("Pass", "0.1 lt 0.5 = true"))
	e.WaitGateReady(t, a.ns, bundle, "prod", "canary-gate", true, expr+" = true", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))

	e.WaitBundlePhase(t, a.ns, bundle, "Verified", promoteTimeout)
	e.WaitMetricCheck(t, a.ns, inst.Name, time.Minute, "suspended by the Graph", func(mc *v1alpha1.MetricCheck) bool {
		return mc.Spec.Suspend && mc.Status.Reason == "Suspended: spec.suspend is set; the last result goes stale at validUntil"
	})
}
