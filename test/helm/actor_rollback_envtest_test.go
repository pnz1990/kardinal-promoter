//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// TestOlderActorPoliciesOnAPIServer (QA-3 on #2372) is the helm rollback's
// window on a real API server: this release's CRDs (a rollback leaves
// them), the actor policies of an older release (v0.12's, and v0.13's
// with #2384) bound as the chart binds them, and an index this release's
// controller wrote in the compact format. A person's and an agent's pause
// stamped with actor.LegacyReaderGuard's agent and sponsor, the stamp v0.12's
// -actor-records would admit, are refused by the CRD schema before
// admission; an honest stamp is refused by the older policies; an
// unstamped pause goes on.
// Covers ACTOR-INDEX-01, ACTOR-INDEX-05.
func TestOlderActorPoliciesOnAPIServer(t *testing.T) {
	for _, release := range []string{"v0.12", "v0.13"} {
		t.Run(release, func(t *testing.T) { olderPoliciesOnAPIServer(t, release) })
	}
}

func olderPoliciesOnAPIServer(t *testing.T, release string) {
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(repoRoot(t), "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, v1alpha1.AddToScheme(sch))
	admin, err := client.New(cfg, client.Options{Scheme: sch})
	require.NoError(t, err)
	ctx := context.Background()
	for _, ns := range []string{releaseNS, "payments-a"} {
		require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	}
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "all-admin"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: "Group", APIGroup: rbacv1.GroupName, Name: "system:authenticated"}}}))
	// The chart's policies and bindings, the three actor policies the older
	// release's (same names, so the chart's bindings bind them).
	older := olderActorPolicies(t, release)
	for _, d := range helmInstallOrder(render(t, "kardinal-promoter")) {
		if d.Kind != "AgentIndex" && d.Kind != "ValidatingAdmissionPolicy" && d.Kind != "ValidatingAdmissionPolicyBinding" {
			continue
		}
		var obj unstructured.Unstructured
		if vap, ok := older[strings.TrimPrefix(d.Name, "kardinal-promoter-")]; ok && d.Kind == "ValidatingAdmissionPolicy" {
			raw, err := json.Marshal(vap)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &obj.Object))
			obj.SetAPIVersion("admissionregistration.k8s.io/v1")
			obj.SetKind("ValidatingAdmissionPolicy")
		} else {
			require.NoError(t, json.Unmarshal(d.raw, &obj.Object))
		}
		require.NoError(t, admin.Create(ctx, &obj), d.Name)
	}
	// The compact index, as this release's controller writes it.
	ctrl := asController(t, cfg, sch)
	var idx v1alpha1.AgentIndex
	require.NoError(t, admin.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	reg := actor.EvaluateCompact([]v1alpha1.AgentIdentity{{ObjectMeta: metav1.ObjectMeta{Name: "fixer"},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"}},
			Sponsor: v1alpha1.AgentSponsor{User: "alice"}, Scope: v1alpha1.AgentScope{Namespaces: []string{"*"}}}}}, actor.MaxIndexBytes)
	idx.Status = reg.Index
	idx.Status.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: actor.ReasonCompactFormat, LastTransitionTime: metav1.Now()},
		{Type: actor.ConditionCompactReady, Status: metav1.ConditionTrue, Reason: "Built", LastTransitionTime: metav1.Now()},
	}
	require.NoError(t, ctrl.Status().Update(ctx, &idx))
	require.Contains(t, idx.Status.Groups, actor.LegacyReaderGroup)

	as := func(user string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: []string{"system:authenticated"}}
		cl, err := client.New(c, client.Options{Scheme: sch})
		require.NoError(t, err)
		return cl
	}
	app := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "payments-a"},
		Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "https://github.com/acme/app"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}}}}
	require.NoError(t, admin.Create(ctx, app))
	n := 0
	pauseAs := func(user string, stamp *v1alpha1.Actor) error {
		var cur v1alpha1.Pipeline
		require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(app), &cur))
		cur.Spec.Paused = !cur.Spec.Paused
		// A new record each time: one equal to the last adds no stamp.
		n++
		cur.Spec.LastChange = &v1alpha1.PipelineChange{Action: "Pause", By: user, Actor: stamp, Reason: fmt.Sprintf("pause %d", n)}
		cur.Labels = map[string]string{"kardinal.io/actor-check": "true"}
		return as(user).Update(ctx, &cur)
	}
	guard := func(p string) *v1alpha1.Actor {
		return &v1alpha1.Actor{Kind: "agent", Principal: p, Agent: actor.LegacyReaderGuard.Agent, Sponsor: actor.LegacyReaderGuard.Sponsor}
	}
	fixerSA := "system:serviceaccount:ci:fixer"
	// The policies' param informer sees the index (and the policies) within
	// a second or two: wait until an honest stamp is refused.
	var last error
	require.Eventually(t, func() bool {
		last = pauseAs("alice", &v1alpha1.Actor{Kind: "human", Principal: "alice"})
		return last != nil && strings.Contains(last.Error(), "ValidatingAdmissionPolicy")
	}, 30*time.Second, 250*time.Millisecond, "the older policies refuse a person's stamped pause")
	t.Logf("%s a person's honest pause: %s", release, last)
	for _, tc := range []struct {
		name, user string
		stamp      *v1alpha1.Actor
	}{
		{"a person's pause stamped with the guard", "alice", guard("alice")},
		{"an agent's pause stamped with the guard", fixerSA, guard(fixerSA)},
	} {
		err := pauseAs(tc.user, tc.stamp)
		require.Error(t, err, tc.name)
		require.Contains(t, err.Error(), "Too long", "%s: the schema refuses the guard's names: %v", tc.name, err)
		t.Logf("%s %s: %s", release, tc.name, err)
	}
	err = pauseAs(fixerSA, &v1alpha1.Actor{Kind: "agent", Principal: fixerSA, Agent: "fixer", Sponsor: "alice"})
	require.Error(t, err, "an agent's honest stamp")
	require.Contains(t, err.Error(), "ValidatingAdmissionPolicy 'kardinal-promoter-actor-records'")
	require.NoError(t, pauseAs("bob", nil), "an unstamped pause")
}
