//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// jaegerSpan is a span as Jaeger's v3 query API returns it (OTLP JSON).
type jaegerSpan struct {
	TraceID      string `json:"traceId"`
	SpanID       string `json:"spanId"`
	ParentSpanID string `json:"parentSpanId"`
	Name         string `json:"name"`
	Attributes   []struct {
		Key   string                 `json:"key"`
		Value map[string]interface{} `json:"value"`
	} `json:"attributes"`
}

// tag is the attribute key's value as a string ("" when absent).
func (s jaegerSpan) tag(key string) string {
	for _, a := range s.Attributes {
		if a.Key == key {
			for _, v := range a.Value {
				return fmt.Sprint(v)
			}
		}
	}
	return ""
}

type jaegerTrace struct {
	TraceID string
	Spans   []jaegerSpan
}

// jaegerTraces queries Jaeger's v3 API for the controller's traces of the
// last hour, or for one trace when traceID is set, grouped by trace.
func jaegerTraces(ctx context.Context, api, traceID string) ([]jaegerTrace, error) {
	now := time.Now().UTC()
	u := api + "/api/v3/traces?" + url.Values{
		"query.service_name":   {"kardinal-controller"},
		"query.start_time_min": {now.Add(-time.Hour).Format(time.RFC3339)},
		"query.start_time_max": {now.Add(time.Minute).Format(time.RFC3339)},
		"query.num_traces":     {"1000"},
	}.Encode()
	if traceID != "" {
		u = api + "/api/v3/traces/" + traceID
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // no trace (yet)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("jaeger %s: HTTP %d %s", u, resp.StatusCode, body)
	}
	byTrace := map[string]*jaegerTrace{}
	var order []string
	// The API may stream several result objects, one per chunk.
	dec := json.NewDecoder(resp.Body)
	for {
		var chunk struct {
			Result struct {
				ResourceSpans []struct {
					ScopeSpans []struct {
						Spans []jaegerSpan `json:"spans"`
					} `json:"scopeSpans"`
				} `json:"resourceSpans"`
			} `json:"result"`
		}
		if err := dec.Decode(&chunk); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode jaeger traces: %w", err)
		}
		for _, rs := range chunk.Result.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, sp := range ss.Spans {
					tr := byTrace[sp.TraceID]
					if tr == nil {
						tr = &jaegerTrace{TraceID: sp.TraceID}
						byTrace[sp.TraceID] = tr
						order = append(order, sp.TraceID)
					}
					tr.Spans = append(tr.Spans, sp)
				}
			}
		}
	}
	out := make([]jaegerTrace, 0, len(order))
	for _, id := range order {
		out = append(out, *byTrace[id])
	}
	return out, nil
}

// TestChart_Tracing checks tracing.enabled with the OTLP/HTTP endpoint of
// the suite's Jaeger: a namespace-mode release promotes through a pr-review
// environment and notifies a NotificationHook. The hook's POST carries a W3C
// traceparent whose trace is in Jaeger with the notificationhook reconcile
// and the client span. The traces of the release's namespace hold
// reconcile spans, a span per promotion step, git clone and git push, and
// SCM API client spans to the git server, and a MetricCheck query's client
// span to its web API (the receiver). A git Subscription's poll is a client
// span under its subscription reconcile. No span carries the hook URL's path
// or the MetricCheck URL's path.
//
// Covers OBS-TRACING-01.
func TestChart_Tracing(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	otlp := framework.MustEnv(t, "KARDINAL_E2E_JAEGER_OTLP")
	api := framework.MustEnv(t, "KARDINAL_E2E_JAEGER_API")

	a := newArgoApp(t, e, "test")
	r := e.InstallChart(t, releaseName(a.ns), a.ns, nsValues(a.ns, framework.Values{
		"tracing": framework.Values{"enabled": true, "endpoint": otlp, "samplingRatio": 1},
	}))
	args := r.Deployment(t).Spec.Template.Spec.Containers[0].Args
	assert.Contains(t, args, "--tracing-enabled=true")
	assert.Contains(t, args, "--tracing-endpoint="+otlp)
	assert.Contains(t, args, "--tracing-sampling-ratio=1")

	// A git Subscription: its polls are traced like the SCM API requests.
	newSub(t, e, a.ns, "traced-sub", v1alpha1.SubscriptionSpec{Type: v1alpha1.SubscriptionTypeGit,
		Git: &v1alpha1.GitSubscriptionSpec{RepoURL: a.repo.CloneURL, Interval: "30s"}})

	bucket := a.ns + "-traced"
	newHook(t, e, a.ns, "traced", rcv.URL(bucket, "services/T0/B0/hookpath"), "", "", v1alpha1.NotificationEventBundleVerified)
	// A web MetricCheck that queries the receiver: its GET is a client span
	// under metriccheck.Reconcile.
	e.CreateMetricCheck(t, &v1alpha1.MetricCheck{
		ObjectMeta: metav1.ObjectMeta{Name: "traced-query", Namespace: a.ns},
		Spec: v1alpha1.MetricCheckSpec{Provider: "web", Interval: "20s",
			Threshold: v1alpha1.MetricThreshold{Operator: "lt", Value: 1},
			Web:       &v1alpha1.WebProviderSpec{URL: rcv.URL(bucket+"-metric", "querypath"), JSONPath: "{.status}"}},
	})
	a.apply(t, a.resourcePipeline(map[string]string{"test": "pr-review"}))
	promote(t, a, fixtures.V2, map[string]bool{"test": true})

	rec := waitRecords(t, rcv, bucket, 1, time.Minute)[0]
	parent := rec.Header("Traceparent")
	require.Regexp(t, `^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`, parent, "the webhook carries a sampled traceparent")
	traceID, spanID := strings.Split(parent, "-")[1], strings.Split(parent, "-")[2]

	var hookTrace jaegerTrace
	framework.Eventually(t, time.Minute, "the webhook's trace in Jaeger", func(ctx context.Context) (bool, string) {
		tr, err := jaegerTraces(ctx, api, traceID)
		if err != nil || len(tr) == 0 {
			return false, fmt.Sprint(err)
		}
		hookTrace = tr[0]
		ops := map[string]bool{}
		for _, s := range hookTrace.Spans {
			ops[s.Name] = true
		}
		return ops["notificationhook.Reconcile"] && ops["HTTP POST"], fmt.Sprintf("%v", ops)
	})
	for _, s := range hookTrace.Spans {
		switch s.Name {
		case "HTTP POST":
			assert.Equal(t, spanID, s.SpanID, "the traceparent names the client span")
			assert.Equal(t, "receiver.webhook-receiver.svc.cluster.local", s.tag("server.address"))
			assert.Equal(t, "200", s.tag("http.response.status_code"))
		case "notificationhook.Reconcile":
			assert.Equal(t, a.ns, s.tag("k8s.namespace.name"))
			assert.Equal(t, "traced", s.tag("kardinal.object.name"))
		}
	}

	scmAPI, err := url.Parse(framework.MustEnv(t, framework.EnvSCMAPI))
	require.NoError(t, err)
	gitHost := scmAPI.Hostname()
	want := []string{"promotionstep.Reconcile", "bundle.Reconcile", "step git-clone", "git clone",
		"step git-push", "git push", "step open-pr", "metriccheck.Reconcile"}
	receiverHost := "receiver.webhook-receiver.svc.cluster.local"
	framework.Eventually(t, time.Minute, "the namespace's promotion spans in Jaeger", func(ctx context.Context) (bool, string) {
		traces, err := jaegerTraces(ctx, api, "")
		if err != nil {
			return false, err.Error()
		}
		ops, scm, metric := map[string]bool{}, 0, 0
		for _, tr := range traces {
			mine := false
			for _, s := range tr.Spans {
				if s.tag("k8s.namespace.name") == a.ns {
					mine = true
				}
			}
			if !mine {
				continue
			}
			for _, s := range tr.Spans {
				ops[s.Name] = true
				if strings.HasPrefix(s.Name, "HTTP ") && s.tag("server.address") == gitHost {
					scm++
				}
				if s.Name == "HTTP GET" && s.tag("server.address") == receiverHost {
					metric++
				}
				for _, at := range s.Attributes {
					assert.NotContains(t, fmt.Sprint(at.Value), "hookpath", "%s attribute %s", s.Name, at.Key)
					assert.NotContains(t, fmt.Sprint(at.Value), "querypath", "%s attribute %s", s.Name, at.Key)
				}
			}
		}
		var missing []string
		for _, op := range want {
			if !ops[op] {
				missing = append(missing, op)
			}
		}
		return len(missing) == 0 && scm > 0 && metric > 0, fmt.Sprintf("missing %v, %d SCM spans to %s, %d MetricCheck spans to %s",
			missing, scm, gitHost, metric, receiverHost)
	})
	cloneURL, err := url.Parse(a.repo.CloneURL)
	require.NoError(t, err)
	framework.Eventually(t, 2*time.Minute, "the Subscription's poll spans in Jaeger", func(ctx context.Context) (bool, string) {
		traces, err := jaegerTraces(ctx, api, "")
		if err != nil {
			return false, err.Error()
		}
		seen := 0
		for _, tr := range traces {
			sub, polls := false, 0
			for _, s := range tr.Spans {
				if s.Name == "subscription.Reconcile" && s.tag("k8s.namespace.name") == a.ns {
					sub = true
				}
				if strings.HasPrefix(s.Name, "HTTP ") && s.tag("server.address") == cloneURL.Hostname() {
					polls++
					assert.Equal(t, cloneURL.Scheme, s.tag("url.scheme"))
				}
			}
			if sub {
				seen++
				if polls > 0 {
					return true, ""
				}
			}
		}
		return false, fmt.Sprintf("%d subscription.Reconcile traces in %s, none with an HTTP span to %s", seen, a.ns, cloneURL.Hostname())
	})
}
