//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// receiverNamespace is where hack/e2e/components/webhook-receiver.sh runs
// the receiver.
const receiverNamespace = "webhook-receiver"

// receiverPodIP is the address of the receiver's running Pod.
func receiverPodIP(t *testing.T, e *framework.Env) string {
	t.Helper()
	var pods corev1.PodList
	require.NoError(t, e.Client.List(context.Background(), &pods, client.InNamespace(receiverNamespace),
		client.MatchingLabels{"app": "receiver"}))
	for _, p := range pods.Items {
		if p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning && p.Status.PodIP != "" {
			return p.Status.PodIP
		}
	}
	t.Fatalf("no running receiver Pod in %s", receiverNamespace)
	return ""
}

// TestChart_EgressAllowlist checks egress.allowlist (--egress-allowlist) on a
// namespace-mode release: a hook whose host matches a wildcard entry and one
// that posts to an address inside a CIDR entry are delivered; a hook whose
// host (a short Service name, resolving to the receiver's ClusterIP) matches
// nothing is refused before connecting, with the allowlist in
// failureMessage, and a MetricCheck to the same host fails with it in its
// reason. Loopback stays refused although the allowlist lists 127.0.0.0/8.
// After the controller Pod is deleted, the new Pod sends none of the
// delivered events again (status.processedEventKeys).
//
// Covers NOTIF-ALLOWLIST-01, NOTIF-RESTART-01.
func TestChart_EgressAllowlist(t *testing.T) {
	t.Parallel()
	namespaceScoped(t)
	e := framework.New(t)
	rcv := framework.NewReceiver(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	podIP := receiverPodIP(t, e)
	r := e.InstallChart(t, releaseName(ns), ns, nsValues(ns, framework.Values{
		"egress": framework.Values{"allowlist": []string{
			"*." + receiverNamespace + ".svc.cluster.local", podIP + "/32", "127.0.0.0/8",
		}},
	}))
	assert.Contains(t, r.Deployment(t).Spec.Template.Spec.Containers[0].Args,
		"--egress-allowlist=*."+receiverNamespace+".svc.cluster.local,"+podIP+"/32,127.0.0.0/8")

	a := holdApp(t, e, ns, pipelineName)
	ev := v1alpha1.NotificationEventPolicyGateBlocked
	byName := rcv.URL(ns+"-name", "hook") // http://receiver.webhook-receiver.svc.cluster.local:8080/...
	require.Contains(t, byName, "."+receiverNamespace+".svc.cluster.local:")
	byIP := "http://" + net.JoinHostPort(podIP, "8080") + "/" + ns + "-ip/hook"
	denied := "http://receiver." + receiverNamespace + ":8080/" + ns + "-denied/hook"
	newHook(t, e, ns, "by-name", byName, "", "", ev)
	newHook(t, e, ns, "by-ip", byIP, "", "", ev)
	newHook(t, e, ns, "denied", denied, "", "", ev)
	newHook(t, e, ns, "loopback", "http://127.0.0.1:8082/api/v1/pipelines", "", "", ev)
	mc := &v1alpha1.MetricCheck{
		ObjectMeta: metav1.ObjectMeta{Name: "denied", Namespace: ns},
		Spec: v1alpha1.MetricCheckSpec{
			Provider:      "prometheus",
			PrometheusURL: "http://receiver." + receiverNamespace + ":8080/" + ns + "-metric",
			Query:         "up",
			Threshold:     v1alpha1.MetricThreshold{Operator: "gt", Value: 0},
			Interval:      "30s",
		},
	}
	require.NoError(t, e.Client.Create(ctx, mc))

	_, gate := holdBundle(t, a, pipelineName)
	key := gateKey(gate)
	waitRecords(t, rcv, ns+"-name", 1, time.Minute)
	waitRecords(t, rcv, ns+"-ip", 1, time.Minute)

	attempt1 := fmt.Sprintf("delivery of %s failed (attempt 1 of 10): webhook request failed: ", key)
	h := waitHook(t, e, ns, "denied", "a refused delivery", func(s v1alpha1.NotificationHookStatus) bool { return s.FailedAttempts == 1 })
	assert.Contains(t, h.Status.FailureMessage, attempt1)
	assert.Contains(t, h.Status.FailureMessage, "destination address is not allowed: not in the controller egress allowlist: receiver."+
		receiverNamespace+" (")
	assert.Contains(t, h.Status.FailureMessage, ") matches no entry")
	assert.Empty(t, rcv.MustRecords(t, ns+"-denied"), "refused before connecting")

	h = waitHook(t, e, ns, "loopback", "a refused delivery", func(s v1alpha1.NotificationHookStatus) bool { return s.FailedAttempts == 1 })
	assert.Contains(t, h.Status.FailureMessage, "destination address is not allowed: 127.0.0.1 is loopback",
		"the allowlist cannot open the deny list")

	m := e.WaitMetricCheck(t, ns, "denied", time.Minute, "to fail on the allowlist", func(m *v1alpha1.MetricCheck) bool {
		return m.Status.LastEvaluatedAt != nil && strings.Contains(m.Status.Reason, "not in the controller egress allowlist")
	})
	assert.NotEqual(t, "Pass", m.Status.Result)
	assert.Empty(t, rcv.MustRecords(t, ns+"-metric"))

	for _, name := range []string{"by-name", "by-ip"} {
		h := waitHook(t, e, ns, name, "the delivery recorded", func(s v1alpha1.NotificationHookStatus) bool { return s.LastEventKey == key })
		assert.Empty(t, h.Status.FailureMessage, name)
	}

	// A controller restart: the new Pod reads processedEventKeys and sends
	// nothing again, while the gate keeps being re-evaluated.
	before := getHook(t, e, ns, "denied").Status.FailedAttempts
	old := runningPod(t, r)
	require.NoError(t, e.Client.Delete(ctx, &old))
	framework.Eventually(t, 3*time.Minute, "a new controller Pod", func(context.Context) (bool, string) {
		for _, p := range r.Pods(t) {
			if p.Name != old.Name && p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning {
				return true, p.Name
			}
		}
		return false, "old Pod " + old.Name
	})
	r.WaitRolledOut(t, 3*time.Minute)
	// The new leader reconciles the hooks: the denied hook's next attempt
	// follows the backoff in status.nextRetryAt (30s, then 1m).
	waitHook(t, e, ns, "denied", "another refused attempt after the restart",
		func(s v1alpha1.NotificationHookStatus) bool { return s.FailedAttempts > before })
	keepRecords(t, rcv, ns+"-name", 1, 20*time.Second)
	keepRecords(t, rcv, ns+"-ip", 1, time.Second)
}
