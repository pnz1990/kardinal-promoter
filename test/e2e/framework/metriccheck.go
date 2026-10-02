// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// MetricCheck is a MetricCheck ns/name that runs query against the e2e
// Prometheus and passes when the value <op> value. interval is
// spec.interval ("" leaves the default).
func MetricCheck(t *testing.T, ns, name, query, op string, value float64, interval string) *v1alpha1.MetricCheck {
	t.Helper()
	return &v1alpha1.MetricCheck{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"kardinal.io/e2e": "true"}},
		Spec: v1alpha1.MetricCheckSpec{
			Provider:      "prometheus",
			PrometheusURL: PrometheusURL(t),
			Query:         query,
			Threshold:     v1alpha1.MetricThreshold{Operator: op, Value: value},
			Interval:      interval,
		},
	}
}

// CreateMetricCheck creates mc. A MetricCheck outside a test namespace (an
// org MetricCheck in PolicyNamespace) is deleted when the test ends.
func (e *Env) CreateMetricCheck(t *testing.T, mc *v1alpha1.MetricCheck) {
	t.Helper()
	if err := e.Client.Create(context.Background(), mc); err != nil {
		t.Fatalf("create MetricCheck %s/%s: %v", mc.Namespace, mc.Name, err)
	}
	if !strings.HasPrefix(mc.Namespace, "e2e-") {
		t.Cleanup(func() {
			if os.Getenv(EnvKeep) == "1" {
				return
			}
			if err := e.Client.Delete(context.Background(), mc); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("delete MetricCheck %s/%s: %v", mc.Namespace, mc.Name, err)
			}
		})
	}
}

// GetMetricCheck gets the MetricCheck ns/name.
func (e *Env) GetMetricCheck(ctx context.Context, ns, name string) (*v1alpha1.MetricCheck, error) {
	var mc v1alpha1.MetricCheck
	if err := e.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &mc); err != nil {
		return nil, err
	}
	return &mc, nil
}

// WaitMetricCheck waits until the MetricCheck ns/name satisfies match and
// returns it.
func (e *Env) WaitMetricCheck(t *testing.T, ns, name string, timeout time.Duration, what string,
	match func(*v1alpha1.MetricCheck) bool) *v1alpha1.MetricCheck {
	t.Helper()
	var got *v1alpha1.MetricCheck
	Eventually(t, timeout, fmt.Sprintf("MetricCheck %s/%s %s", ns, name, what), func(ctx context.Context) (bool, string) {
		mc, err := e.GetMetricCheck(ctx, ns, name)
		if err != nil {
			return false, err.Error()
		}
		got = mc
		return match(mc), DescribeMetricCheck(mc)
	})
	return got
}

// MetricResult matches a MetricCheck whose status.result is result and whose
// reason contains reason.
func MetricResult(result, reason string) func(*v1alpha1.MetricCheck) bool {
	return func(mc *v1alpha1.MetricCheck) bool {
		return mc.Status.LastEvaluatedAt != nil && mc.Status.Result == result && strings.Contains(mc.Status.Reason, reason)
	}
}

// DescribeMetricCheck is a one-line summary of a MetricCheck's status for
// wait messages.
func DescribeMetricCheck(mc *v1alpha1.MetricCheck) string {
	at, until := "never", "unset"
	if mc.Status.LastEvaluatedAt != nil {
		at = mc.Status.LastEvaluatedAt.UTC().Format(time.RFC3339)
	}
	if mc.Status.ValidUntil != nil {
		until = mc.Status.ValidUntil.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("%s result=%q value=%q reason=%q evaluated=%s validUntil=%s",
		mc.Name, mc.Status.Result, mc.Status.LastValue, mc.Status.Reason, at, until)
}

// DenyStatusWrites makes the API server deny every write to the status of
// the MetricCheck ns/name, with a ValidatingAdmissionPolicy and binding named
// policy, and waits until it does. It is how a test makes the controller's
// status patches fail. lift deletes the policy and waits until status writes
// are allowed again; the policy is also deleted when the test ends.
//
// The wait probes with dry-run status patches, which the policy counts as
// denials too: the first denied probe is one of AdmissionDenials(policy).
func (e *Env) DenyStatusWrites(t *testing.T, ns, name string) (policy string, lift func()) {
	t.Helper()
	policy = "e2e-deny-status-" + ns
	remove := e.denyAdmission(t, policy, admissionv1.RuleWithOperations{
		Operations: []admissionv1.OperationType{admissionv1.Update},
		Rule: admissionv1.Rule{
			APIGroups:   []string{v1alpha1.GroupVersion.Group},
			APIVersions: []string{"*"},
			Resources:   []string{"metricchecks/status"},
		},
	}, fmt.Sprintf("request.namespace == %q && request.name == %q", ns, name),
		fmt.Sprintf("e2e: status writes to MetricCheck %s/%s are denied", ns, name))
	Eventually(t, time.Minute, "the API server to deny status writes to "+ns+"/"+name, func(ctx context.Context) (bool, string) {
		err := e.probeStatusWrite(ctx, ns, name)
		return err != nil && strings.Contains(err.Error(), policy), fmt.Sprintf("dry-run status patch: %v", err)
	})
	lift = func() {
		t.Helper()
		if err := remove(); err != nil {
			t.Fatalf("delete admission policy %s: %v", policy, err)
		}
		Eventually(t, time.Minute, "the API server to allow status writes to "+ns+"/"+name, func(ctx context.Context) (bool, string) {
			err := e.probeStatusWrite(ctx, ns, name)
			return err == nil, fmt.Sprintf("dry-run status patch: %v", err)
		})
	}
	return policy, lift
}

// probeStatusWrite is a dry-run status patch of the MetricCheck ns/name: it
// goes through admission but changes nothing.
func (e *Env) probeStatusWrite(ctx context.Context, ns, name string) error {
	mc := &v1alpha1.MetricCheck{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	return e.Client.Status().Patch(ctx, mc,
		client.RawPatch(types.MergePatchType, []byte(`{"status":{"reason":"e2e dry-run probe"}}`)), client.DryRunAll)
}

// APIServerMetrics fetches and parses the API server's /metrics.
func (e *Env) APIServerMetrics(ctx context.Context) (Metrics, error) {
	raw, err := e.Kube.CoreV1().RESTClient().Get().AbsPath("/metrics").DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("get API server metrics: %w", err)
	}
	return ParseMetrics(string(raw))
}

// AdmissionDenials is how many requests the ValidatingAdmissionPolicy policy
// has denied (apiserver_validating_admission_policy_check_total). kind runs
// one API server, so the count is complete.
func (e *Env) AdmissionDenials(ctx context.Context, policy string) (float64, error) {
	m, err := e.APIServerMetrics(ctx)
	if err != nil {
		return 0, err
	}
	return m.Sum("apiserver_validating_admission_policy_check_total",
		map[string]string{"policy": policy, "enforcement_action": "deny"}), nil
}
