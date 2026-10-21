//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
)

// TestActorAttacksOnAPIServer is QA's attack run on #1959, kept as a test:
// against the release's AgentIndex, a release admin and a cluster-admin
// ServiceAccount may not delete it (one by one or as a collection) or write
// its status; a cluster-admin of system:masters may not delete it either.
// Removing the guard's binding and deleting the index is the one way left,
// and the controller then recreates the chart's index from the guard's
// labels. Against gate overrides, every way to add or rewrite a forged
// stamp is refused: without the label, with a wrong label value, by
// removing the label in the same write, by JSON patch and server-side apply,
// on gate instances and templates, and on create; a person's unstamped
// override and an agent's correct one pass. While the index is not Ready,
// checked writes wait and unchecked ones go on.
//
// Covers ACTOR-ADMIT-06, ACTOR-PERF-01.
func TestActorAttacksOnAPIServer(t *testing.T) {
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
	for _, ns := range []string{"kardinal-system", "payments-a", "ci"} {
		require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	}
	for _, d := range render(t, "kardinal-promoter") {
		switch d.Kind {
		case "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding", "AgentIndex", "ClusterRole":
		default:
			continue
		}
		var obj unstructured.Unstructured
		require.NoError(t, json.Unmarshal(d.raw, &obj.Object))
		require.NoError(t, admin.Create(ctx, &obj), d.Kind+"/"+d.Name)
	}
	// release-admin: the chart's admin role, cluster-wide; plus impersonation-free SA with cluster-admin.
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "ra"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "kardinal-promoter-admin-extra"},
		Subjects: []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: "reladmin"}}}))
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "ra-status"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "ops", Namespace: "ci"}, {Kind: "ServiceAccount", Name: "kardinal-promoter", Namespace: "kardinal-system"}, {Kind: "ServiceAccount", Name: "fixer", Namespace: "ci"}, {Kind: "ServiceAccount", Name: "parked", Namespace: "ci"}, {Kind: "Group", APIGroup: rbacv1.GroupName, Name: "qa-all"}}}))
	as := func(user string, groups ...string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: groups}
		cl, err := client.New(c, client.Options{Scheme: sch})
		require.NoError(t, err)
		return cl
	}
	ctrl := asController(t, cfg, sch)
	scope := v1alpha1.AgentScope{Namespaces: []string{"payments-*"}, Environments: []string{"test"}}
	require.NoError(t, admin.Create(ctx, &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer"},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"}},
			Sponsor: v1alpha1.AgentSponsor{User: "alice"}, Scope: scope}}))
	require.NoError(t, admin.Create(ctx, &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "parked"},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "ci", Name: "parked"}},
			Sponsor: v1alpha1.AgentSponsor{User: "bob"}, Scope: scope, Suspended: true}}))
	r := &agentidentity.IndexReconciler{Client: ctrl, Name: "kardinal-promoter", ChartCreated: true,
		Guard: "kardinal-promoter-agent-index", Reader: ctrl}
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	key := client.ObjectKey{Name: "kardinal-promoter"}

	// A. The index: delete, deletecollection, status writes by release-admin and a SA.
	res := func(name string, err error, wantErr bool) {
		t.Logf("A %-45s err=%v", name, err)
		if wantErr {
			require.Error(t, err, name)
		} else {
			require.NoError(t, err, name)
		}
	}
	waitIndexGuard(t, admin)
	for _, who := range []client.Client{as("reladmin", "system:authenticated"), as("system:serviceaccount:ci:ops")} {
		res("delete", who.Delete(ctx, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}}), true)
		var idx v1alpha1.AgentIndex
		require.NoError(t, admin.Get(ctx, key, &idx))
		idx.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: "True", Reason: "Forged", LastTransitionTime: metav1.Now()}}
		idx.Status.Users = map[string]v1alpha1.AgentIndexEntry{}
		res("status update", who.Status().Update(ctx, &idx), true)
		res("status merge patch", who.Status().Patch(ctx, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}},
			client.RawPatch(types.MergePatchType, []byte(`{"status":{"users":null}}`))), true)
		res("main patch (labels, harmless)", who.Patch(ctx, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}},
			client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"x":"y"}}}`))), false)
		// A collection delete carries no name (QA's HIGH finding on #1959).
		res("deletecollection", who.DeleteAllOf(ctx, &v1alpha1.AgentIndex{}), true)
		require.NoError(t, admin.Get(ctx, key, &idx), "the index survives a collection delete")
		require.True(t, meta.IsStatusConditionTrue(idx.Status.Conditions, "Ready"))
		require.Contains(t, idx.Status.Users, "system:serviceaccount:ci:parked", "status untouched")
	}
	res("cluster-admin (masters) delete", admin.Delete(ctx, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}}), true)
	res("cluster-admin (masters) deletecollection", admin.DeleteAllOf(ctx, &v1alpha1.AgentIndex{}), true)

	// The one way left: unbind the guard, then delete. The controller
	// recreates the chart's index (the guard policy still exists: Helm
	// created the index before it), with the guard's labels, and builds it.
	require.NoError(t, admin.Delete(ctx, &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter-agent-index"}}))
	require.Eventually(t, func() bool {
		return admin.DeleteAllOf(ctx, &v1alpha1.AgentIndex{}) == nil &&
			apierrors.IsNotFound(admin.Get(ctx, key, &v1alpha1.AgentIndex{}))
	}, 30*time.Second, 500*time.Millisecond)
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err, "the controller recreates the deleted chart index")
	var healed v1alpha1.AgentIndex
	require.NoError(t, admin.Get(ctx, key, &healed))
	require.True(t, meta.IsStatusConditionTrue(healed.Status.Conditions, "Ready"))
	require.Equal(t, "Helm", healed.Labels["app.kubernetes.io/managed-by"], "the chart's labels, so Helm adopts it")
	require.Equal(t, "keep", healed.Annotations["helm.sh/resource-policy"])
	time.Sleep(2 * time.Second)

	// B. Overrides.
	fx := "system:serviceaccount:ci:fixer"
	pk := "system:serviceaccount:ci:parked"
	ag := func(p, a, s string) *v1alpha1.Actor {
		return &v1alpha1.Actor{Kind: "agent", Principal: p, Agent: a, Sponsor: s}
	}
	inst := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "g", Namespace: "payments-a",
		Labels: map[string]string{"kardinal.io/bundle": "bx", "kardinal.io/pipeline": "checkout", "kardinal.io/environment": "test"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true"}}
	require.NoError(t, ctrl.Create(ctx, inst))
	tmpl := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "tmpl", Namespace: "payments-a"}, Spec: v1alpha1.PolicyGateSpec{Expression: "true"}}
	require.NoError(t, admin.Create(ctx, tmpl))
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	seq := 0
	ov := func(by string, a *v1alpha1.Actor) map[string]interface{} {
		seq++
		m := map[string]interface{}{"reason": fmt.Sprintf("qa-%d", seq), "createdBy": by, "expiresAt": exp}
		if a != nil {
			m["actor"] = a
		}
		return m
	}
	merge := func(c client.Client, name string, labels map[string]interface{}, add map[string]interface{}) error {
		var cur v1alpha1.PolicyGate
		require.NoError(t, admin.Get(ctx, client.ObjectKey{Namespace: "payments-a", Name: name}, &cur))
		list := []interface{}{}
		raw, _ := json.Marshal(cur.Spec.Overrides)
		_ = json.Unmarshal(raw, &list)
		if add != nil {
			list = append(list, add)
		}
		body := map[string]interface{}{"spec": map[string]interface{}{"overrides": list}}
		if labels != nil {
			body["metadata"] = map[string]interface{}{"labels": labels}
		}
		p, _ := json.Marshal(body)
		return c.Patch(ctx, &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: "payments-a", Name: name}}, client.RawPatch(types.MergePatchType, p))
	}
	b := func(name string, err error, wantErr bool) {
		t.Logf("B %-60s err=%v", name, err)
		if wantErr {
			require.Error(t, err, name)
		} else {
			require.NoError(t, err, name)
		}
	}
	L := "kardinal.io/override-check"
	for _, g := range []string{"g", "tmpl"} {
		b(g+" forged, no label", merge(as(fx), g, nil, ov(fx, ag(fx, "fixer", "mallory"))), true)
		b(g+" suspended, no stamp, no label", merge(as(pk), g, nil, ov(pk, nil)), true)
		b(g+" forged, label false", merge(as(fx), g, map[string]interface{}{L: "false"}, ov(fx, ag(fx, "fixer", "mallory"))), true)
		b(g+" forged, label True", merge(as(fx), g, map[string]interface{}{L: "True"}, ov(fx, ag(fx, "fixer", "mallory"))), true)
		b(g+" forged, typo label", merge(as(fx), g, map[string]interface{}{"kardinal.io/override-checks": "true"}, ov(fx, ag(fx, "fixer", "mallory"))), true)
		b(g+" forged, label true", merge(as(fx), g, map[string]interface{}{L: "true"}, ov(fx, ag(fx, "fixer", "mallory"))), true)
		b(g+" suspended correct stamp, label", merge(as(pk), g, map[string]interface{}{L: "true"}, ov(pk, ag(pk, "parked", "bob"))), true)
		b(g+" human unstamped, label", merge(as("alice", "qa-all"), g, map[string]interface{}{L: "true"}, ov("alice", nil)), false)
		// gate now carries the label; remove it in the same write as a forged add.
		b(g+" forged + remove label", merge(as(fx), g, map[string]interface{}{L: nil}, ov(fx, ag(fx, "fixer", "mallory"))), true)
		// remove the label alone (allowed?), then add forged without it.
		e := merge(as("alice", "qa-all"), g, map[string]interface{}{L: nil}, nil)
		t.Logf("B %s remove label alone (+null override entry ignored) err=%v", g, e)
		b(g+" correct, label", merge(as(fx), g, map[string]interface{}{L: "true"}, ov(fx, ag(fx, "fixer", "alice"))), false)
	}
	// JSON patch and SSA on the instance.
	jp := []byte(`[{"op":"add","path":"/spec/overrides/-","value":{"reason":"jp","createdBy":"` + fx + `","expiresAt":"` + exp +
		`","actor":{"kind":"agent","principal":"` + fx + `","agent":"fixer","sponsor":"mallory"}}}]`)
	b("json patch forged (label still true)", as(fx).Patch(ctx, &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: "payments-a", Name: "g"}},
		client.RawPatch(types.JSONPatchType, jp)), true)
	jp2 := []byte(`[{"op":"remove","path":"/metadata/labels/kardinal.io~1override-check"},{"op":"add","path":"/spec/overrides/-","value":{"reason":"jp","createdBy":"` + fx + `","expiresAt":"` + exp +
		`","actor":{"kind":"agent","principal":"` + fx + `","agent":"fixer","sponsor":"mallory"}}}]`)
	b("json patch remove label + forged", as(fx).Patch(ctx, &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: "payments-a", Name: "g"}},
		client.RawPatch(types.JSONPatchType, jp2)), true)
	ssa := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "PolicyGate",
		"metadata": map[string]interface{}{"name": "g", "namespace": "payments-a"},
		"spec":     map[string]interface{}{"overrides": []interface{}{ov(fx, ag(fx, "fixer", "mallory"))}}}}
	b("ssa forged (replaces list, no label)", as(fx).Patch(ctx, ssa, client.Apply, client.FieldOwner("qa"), client.ForceOwnership), true)
	// Mutate an existing entry's actor (stamp of entry 0) via JSON patch.
	{
		var cur v1alpha1.PolicyGate
		require.NoError(t, admin.Get(ctx, client.ObjectKey{Namespace: "payments-a", Name: "g"}, &cur))
		last := -1
		for i, o := range cur.Spec.Overrides {
			if o.Actor != nil && o.Actor.Principal == fx {
				last = i
			}
		}
		require.GreaterOrEqual(t, last, 0)
		jp3 := []byte(fmt.Sprintf(`[{"op":"replace","path":"/spec/overrides/%d/actor/sponsor","value":"mallory"}]`, last))
		b("json patch: agent rewrites own stamp (label on)", as(fx).Patch(ctx, &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: "payments-a", Name: "g"}},
			client.RawPatch(types.JSONPatchType, jp3)), true)
		jp4 := []byte(fmt.Sprintf(`[{"op":"remove","path":"/metadata/labels/kardinal.io~1override-check"},{"op":"replace","path":"/spec/overrides/%d/actor/sponsor","value":"mallory"}]`, last))
		b("json patch: agent rewrites own stamp + drops label", as(fx).Patch(ctx, &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: "payments-a", Name: "g"}},
			client.RawPatch(types.JSONPatchType, jp4)), true)
	}
	// CREATE a gate with forged override + label.
	cg := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "new", Namespace: "payments-a", Labels: map[string]string{L: "true"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", Overrides: []v1alpha1.PolicyGateOverride{{Reason: "c", CreatedBy: fx,
			ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)), Actor: ag(fx, "fixer", "mallory")}}}}
	b("create forged + label", as(fx).Create(ctx, cg), true)
	cg.Labels = nil
	b("create forged, no label", as(fx).Create(ctx, cg), true)

	// C. Ready=False: controller writes not-Ready.
	var idx v1alpha1.AgentIndex
	require.NoError(t, admin.Get(ctx, key, &idx))
	meta.SetStatusCondition(&idx.Status.Conditions, metav1.Condition{Type: "Ready", Status: "False", Reason: "IndexFull"})
	require.NoError(t, ctrl.Status().Update(ctx, &idx))
	time.Sleep(2 * time.Second)
	b("not ready: correct override", merge(as(fx), "g", map[string]interface{}{L: "true"}, ov(fx, ag(fx, "fixer", "alice"))), true)
	// A person is never refused for the index (#2451): unstamped or
	// stamped unverified; a human stamp cannot be verified.
	personBundle := func(a *v1alpha1.Actor) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{GenerateName: "b-", Namespace: "payments-a"},
			Spec: v1alpha1.BundleSpec{Pipeline: "p", Type: "image", Actor: a}}
	}
	b("not ready: a person's unstamped bundle", as("alice", "qa-all").Create(ctx, personBundle(nil)), false)
	b("not ready: a person's bundle stamped unverified", as("alice", "qa-all").Create(ctx,
		personBundle(&v1alpha1.Actor{Kind: v1alpha1.ActorUnverified, Principal: "alice"})), false)
	b("not ready: a person's bundle stamped human", as("alice", "qa-all").Create(ctx,
		personBundle(&v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "alice"})), true)
	b("not ready: a ServiceAccount's bundle", as(fx).Create(ctx, personBundle(nil)), true)
	b("not ready: kro-style gate write, no overrides (unlabelled tmpl2)", admin.Create(ctx, &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "tmpl2", Namespace: "payments-a"},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true"}}), false)
	b("not ready: human override on unlabelled gate, no label", merge(as("alice", "qa-all"), "tmpl2", nil, ov("alice", nil)), true)
}
