//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
)

// percentile is the p-th percentile (0-100) of d.
func percentile(d []time.Duration, p int) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[(len(s)-1)*p/100]
}

// TestActorPolicyLatencyOnAPIServer (#1936) measures, on a real API
// server with the chart's policies, the latency of kro's re-apply of a gate
// instance and of a person's version, with 2,000 registered agents and
// with an index near its bound. The actor policy converts the whole index
// for every request it matches; kro's gate writes are no longer matched
// (only gates labelled kardinal.io/override-check are), so their p99 stays
// flat as the registry grows. Opt in: KARDINAL_ACTOR_LATENCY=1 (it creates
// thousands of AgentIdentities).
//
// Covers ACTOR-PERF-01.
func TestActorPolicyLatencyOnAPIServer(t *testing.T) {
	if os.Getenv("KARDINAL_ACTOR_LATENCY") == "" {
		t.Skip("set KARDINAL_ACTOR_LATENCY=1 to measure")
	}
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"))
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(repoRoot(t), "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	cfg.QPS, cfg.Burst = 1000, 2000
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, v1alpha1.AddToScheme(sch))
	admin, err := client.New(cfg, client.Options{Scheme: sch})
	require.NoError(t, err)
	ctx := context.Background()
	for _, d := range helmInstallOrder(render(t, "kardinal-promoter")) {
		if d.Kind != "AgentIndex" && d.Kind != "ValidatingAdmissionPolicy" && d.Kind != "ValidatingAdmissionPolicyBinding" {
			continue
		}
		var obj unstructured.Unstructured
		require.NoError(t, json.Unmarshal(d.raw, &obj.Object))
		require.NoError(t, admin.Create(ctx, &obj), d.Name)
	}
	require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}}))
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "all-admin"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: "Group", APIGroup: rbacv1.GroupName, Name: "system:authenticated"}}}))
	as := func(user string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user}
		cl, err := client.New(c, client.Options{Scheme: sch})
		require.NoError(t, err)
		return cl
	}
	kro, alice := as("system:serviceaccount:team-a:kardinal-graph"), as("alice")
	idxR := &agentidentity.IndexReconciler{Client: asController(t, cfg, sch), Name: "kardinal-promoter"}

	gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "team-a",
		Labels: map[string]string{"kardinal.io/pipeline": "checkout", "kardinal.io/environment": "prod"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true"}}
	const samples = 200
	n, b := 0, 0
	measure := func(label string) (gateP99, bundleP99 time.Duration) {
		var gates, bundles []time.Duration
		for i := 0; i < samples; i++ {
			gate.Spec.Message = fmt.Sprintf("pass %d", i)
			start := time.Now()
			require.NoError(t, kro.Update(ctx, gate))
			gates = append(gates, time.Since(start))
			b++
			start = time.Now()
			require.NoError(t, alice.Create(ctx, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("b%d", b), Namespace: "team-a"},
				Spec: v1alpha1.BundleSpec{Pipeline: "checkout", Type: "image"}}))
			bundles = append(bundles, time.Since(start))
		}
		t.Logf("%-22s kro gate re-apply p50 %6s p99 %6s | person's version p50 %6s p99 %6s", label,
			percentile(gates, 50).Round(10*time.Microsecond), percentile(gates, 99).Round(10*time.Microsecond),
			percentile(bundles, 50).Round(10*time.Microsecond), percentile(bundles, 99).Round(10*time.Microsecond))
		return percentile(gates, 99), percentile(bundles, 99)
	}
	grow := func(to int) {
		for ; n < to; n++ {
			require.NoError(t, admin.Create(ctx, &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("agent-%05d", n)},
				Spec: v1alpha1.AgentIdentitySpec{
					Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: fmt.Sprintf("team-%05d", n), Name: "agent"}},
					Sponsor:  v1alpha1.AgentSponsor{User: "alice@example.com"},
					Scope:    v1alpha1.AgentScope{Namespaces: []string{"team-*"}}}}))
		}
		_, err := idxR.Reconcile(ctx, reconcile.Request{})
		require.NoError(t, err)
		var idx v1alpha1.AgentIndex
		require.NoError(t, admin.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
		require.True(t, meta.IsStatusConditionTrue(idx.Status.Conditions, "Ready"), "index Ready at %d agents", to)
		raw, err := json.Marshal(idx)
		require.NoError(t, err)
		t.Logf("index: %d agents, %d KiB", to, len(raw)/1024)
		time.Sleep(3 * time.Second) // the policies' param informer sees it
	}
	grow(1)
	require.NoError(t, kro.Create(ctx, gate))
	measure("1 agent")
	grow(2000)
	g2000, b2000 := measure("2,000 agents")
	grow(4200)
	gCap, bCap := measure("4,200 agents (bound)")
	assert.Less(t, g2000, 50*time.Millisecond, "kro's gate re-apply at 2,000 agents")
	assert.Less(t, gCap, 50*time.Millisecond, "kro's gate re-apply near the bound")
	t.Logf("a person's version still pays the conversion: p99 %s at 2,000, %s near the bound", b2000, bCap)
}
