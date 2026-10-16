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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
)

// TestActorsOnAPIServer runs the admission cases of docs/design/17-actors.md
// section 13 on a real kube-apiserver (envtest; CI runs it on the oldest and
// the newest supported Kubernetes), with the chart's policies and bindings,
// an AgentIndex built by the controller's own reconcilers, and impersonated
// users: forged and missing stamps, kind claims, sponsors, scope, suspension,
// conflicts, group agents, the controller exemption, via, overrides,
// audit-actor, deletion without suspension, a stamp added on update; and the
// registry failing closed while the index is missing, written without status
// or not Ready. (Found by QA on #1857, F1 and F3.)
func TestActorsOnAPIServer(t *testing.T) {
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"))
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

	for _, d := range render(t, "kardinal-promoter") {
		if d.Kind != "ValidatingAdmissionPolicy" && d.Kind != "ValidatingAdmissionPolicyBinding" {
			continue
		}
		var obj unstructured.Unstructured
		require.NoError(t, json.Unmarshal(d.raw, &obj.Object))
		require.NoError(t, admin.Create(ctx, &obj), d.Name)
	}
	for _, ns := range []string{"payments-a", "other", "ci"} {
		require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	}
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "qa-all"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: "Group", APIGroup: rbacv1.GroupName, Name: "system:authenticated"}}}))

	scope := v1alpha1.AgentScope{Namespaces: []string{"payments-*"}, Pipelines: []string{"checkout"}, Environments: []string{"test", "uat"}}
	mk := func(name, sponsor string, sc v1alpha1.AgentScope, susp bool, subs ...v1alpha1.AgentSubject) {
		require.NoError(t, admin.Create(ctx, &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.AgentIdentitySpec{Subjects: subs, Sponsor: v1alpha1.AgentSponsor{User: sponsor}, Scope: sc, Suspended: susp}}))
		time.Sleep(1100 * time.Millisecond) // distinct creationTimestamps
	}
	sa := func(ns, n string) v1alpha1.AgentSubject {
		return v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: ns, Name: n}
	}
	mk("fixer", "alice", scope, false, sa("ci", "fixer"), v1alpha1.AgentSubject{Kind: "Group", Name: "oidc:agents-fixer"})
	mk("reviewer", "alice", v1alpha1.AgentScope{Namespaces: []string{"*"}}, false, v1alpha1.AgentSubject{Kind: "User", Name: "oidc:reviewer"})
	mk("parked", "bob", scope, true, sa("ci", "parked"))
	mk("late", "carol", scope, false, sa("ci", "fixer"), sa("ci", "late"))
	mk("grpb", "dave", v1alpha1.AgentScope{Namespaces: []string{"*"}}, false, v1alpha1.AgentSubject{Kind: "Group", Name: "oidc:agents-b"})

	idxR := &agentidentity.IndexReconciler{Client: admin, Name: "kardinal-promoter"}
	idR := &agentidentity.Reconciler{Client: admin}
	build := func() {
		_, err := idxR.Reconcile(ctx, reconcile.Request{})
		require.NoError(t, err)
		var l v1alpha1.AgentIdentityList
		require.NoError(t, admin.List(ctx, &l))
		for _, id := range l.Items {
			_, err := idR.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Name: id.Name}})
			require.NoError(t, err)
		}
	}
	build()
	// Determinism / idempotency: a second build writes nothing.
	var idx1, idx2 v1alpha1.AgentIndex
	require.NoError(t, admin.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx1))
	build()
	require.NoError(t, admin.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx2))
	require.Equal(t, idx1.ResourceVersion, idx2.ResourceVersion, "re-run writes nothing")

	as := func(user string, groups ...string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: groups}
		cl, err := client.New(c, client.Options{Scheme: sch})
		require.NoError(t, err)
		return cl
	}
	n := 0
	bundle := func(ns, pipeline string, a *v1alpha1.Actor) *v1alpha1.Bundle {
		n++
		return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("b%d", n), Namespace: ns},
			Spec: v1alpha1.BundleSpec{Pipeline: pipeline, Type: "image", Actor: a}}
	}
	ag := func(p, agent, sponsor string) *v1alpha1.Actor {
		return &v1alpha1.Actor{Kind: "agent", Principal: p, Agent: agent, Sponsor: sponsor}
	}
	fx := "system:serviceaccount:ci:fixer"
	type tc struct {
		name  string
		c     client.Client
		obj   client.Object
		admit bool
		want  string
	}
	approval := func(ns, env, user string, a *v1alpha1.Actor) *v1alpha1.Approval {
		n++
		return &v1alpha1.Approval{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("a%d", n), Namespace: ns,
			Labels: map[string]string{"kardinal.io/bundle": "bx", "kardinal.io/environment": env}},
			Spec: v1alpha1.ApprovalSpec{Bundle: "bx", BundleUID: "u", Environment: env, User: user, Decision: "approve", Actor: a}}
	}
	run := func(cases []tc) {
		for _, c := range cases {
			err := c.c.Create(ctx, c.obj)
			if c.admit {
				require.NoError(t, err, c.name)
			} else {
				require.Error(t, err, c.name)
				if c.want != "" {
					require.Contains(t, err.Error(), c.want, c.name)
				}
			}
			t.Logf("ok %-55s admit=%v %v", c.name, c.admit, err)
		}
	}
	// Wait for the policies to load.
	require.Eventually(t, func() bool {
		return as(fx).Create(ctx, bundle("payments-a", "checkout", nil)) != nil
	}, 30*time.Second, 500*time.Millisecond)

	run([]tc{
		{"1 human no stamp", as("alice"), bundle("payments-a", "checkout", nil), true, ""},
		{"2 human own stamp", as("alice"), bundle("payments-a", "checkout", &v1alpha1.Actor{Kind: "human", Principal: "alice"}), true, ""},
		{"3 human forged principal", as("alice"), bundle("payments-a", "checkout", &v1alpha1.Actor{Kind: "human", Principal: "bob"}), false, "actor must be"},
		{"4 human claims agent", as("alice"), bundle("payments-a", "checkout", ag("alice", "fixer", "alice")), false, "actor must be"},
		{"5 agent omits stamp", as(fx), bundle("payments-a", "checkout", nil), false, "you are agent fixer"},
		{"6 agent correct", as(fx), bundle("payments-a", "checkout", ag(fx, "fixer", "alice")), true, ""},
		{"7 agent wrong sponsor", as(fx), bundle("payments-a", "checkout", ag(fx, "fixer", "mallory")), false, "actor must be"},
		{"8 agent claims human", as(fx), bundle("payments-a", "checkout", &v1alpha1.Actor{Kind: "human", Principal: fx}), false, "actor must be"},
		{"8b agent claims bot", as(fx), bundle("payments-a", "checkout", &v1alpha1.Actor{Kind: "bot", Principal: fx}), false, "actor must be"},
		{"8c agent claims other agent", as(fx), bundle("payments-a", "checkout", ag(fx, "reviewer", "alice")), false, "actor must be"},
		{"8d agent claims controller", as(fx), bundle("payments-a", "checkout", &v1alpha1.Actor{Kind: "controller", Principal: fx, Via: "subscription"}), false, ""},
		{"9 group agent correct", as("oidc:bot1", "oidc:agents-fixer"), bundle("payments-a", "checkout", ag("oidc:bot1", "fixer", "alice")), true, ""},
		{"9b group agent no stamp", as("oidc:bot1", "oidc:agents-fixer"), bundle("payments-a", "checkout", nil), false, "you are agent"},
		{"9c group agent stamps human", as("oidc:bot1", "oidc:agents-fixer"), bundle("payments-a", "checkout", &v1alpha1.Actor{Kind: "human", Principal: "oidc:bot1"}), false, ""},
		{"10 agent ns out of scope", as(fx), bundle("other", "checkout", ag(fx, "fixer", "alice")), false, "may not act here"},
		{"11 agent pipeline out of scope", as(fx), bundle("payments-a", "cart", ag(fx, "fixer", "alice")), false, "may not act here"},
		{"12 approval env out of scope", as(fx), approval("payments-a", "prod", fx, ag(fx, "fixer", "alice")), false, "may not act here"},
		{"12b approval env in scope", as(fx), approval("payments-a", "test", fx, ag(fx, "fixer", "alice")), true, ""},
		{"12c approval agent no stamp", as(fx), approval("payments-a", "test", fx, nil), false, "you are agent"},
		{"13 suspended agent stamped", as("system:serviceaccount:ci:parked"), bundle("payments-a", "checkout", ag("system:serviceaccount:ci:parked", "parked", "bob")), false, "suspended"},
		{"13b suspended agent unstamped", as("system:serviceaccount:ci:parked"), bundle("payments-a", "checkout", nil), false, ""},
		{"14 conflicting (newer) identity blocked", as("system:serviceaccount:ci:late"), bundle("payments-a", "checkout", nil), false, "not ready"},
		{"14b conflict user+group", as("oidc:reviewer", "oidc:agents-b"), bundle("payments-a", "checkout", ag("oidc:reviewer", "reviewer", "alice")), false, "more than one"},
		{"15 unregistered SA bot", as("system:serviceaccount:ci:builder"), bundle("payments-a", "checkout", &v1alpha1.Actor{Kind: "bot", Principal: "system:serviceaccount:ci:builder"}), true, ""},
		{"15b unregistered SA as human", as("system:serviceaccount:ci:builder"), bundle("payments-a", "checkout", &v1alpha1.Actor{Kind: "human", Principal: "system:serviceaccount:ci:builder"}), false, ""},
		{"16 controller on behalf", as(controllerUser), bundle("payments-a", "checkout", &v1alpha1.Actor{Kind: "human", Principal: "alice", Via: "ui"}), true, ""},
		{"17 non-controller via", as("alice"), bundle("payments-a", "checkout", &v1alpha1.Actor{Kind: "human", Principal: "alice", Via: "ui"}), false, "no via"},
		{"17b same-named SA other ns via", as("system:serviceaccount:other:kardinal-promoter"), bundle("payments-a", "checkout", &v1alpha1.Actor{Kind: "bot", Principal: "system:serviceaccount:other:kardinal-promoter", Via: "api"}), false, ""},
		{"17c controller group member not exempt", as("mallory", "system:serviceaccounts:kardinal-system"), bundle("payments-a", "checkout", &v1alpha1.Actor{Kind: "human", Principal: "alice", Via: "ui"}), false, ""},
	})

	// 18 override entries on gate instances (created as the controller).
	gate := func(name, env string) *v1alpha1.PolicyGate {
		g := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "payments-a",
			Labels: map[string]string{"kardinal.io/bundle": "bx", "kardinal.io/pipeline": "checkout", "kardinal.io/environment": env}},
			Spec: v1alpha1.PolicyGateSpec{Expression: "true"}}
		require.NoError(t, as(controllerUser).Create(ctx, g))
		return g
	}
	override := func(c client.Client, g *v1alpha1.PolicyGate, by string, a *v1alpha1.Actor) error {
		var cur v1alpha1.PolicyGate
		require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(g), &cur))
		cur.Spec.Overrides = append(cur.Spec.Overrides, v1alpha1.PolicyGateOverride{Reason: "qa", CreatedBy: by,
			ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)), Actor: a})
		return c.Update(ctx, &cur)
	}
	gt, gp := gate("g-test", "test"), gate("g-prod", "prod")
	require.NoError(t, override(as(fx), gt, fx, ag(fx, "fixer", "alice")), "18 override correct")
	require.Error(t, override(as(fx), gt, fx, ag(fx, "fixer", "mallory")), "18 override forged sponsor")
	require.Error(t, override(as(fx), gt, fx, nil), "18 override unstamped agent")
	require.Error(t, override(as(fx), gp, fx, ag(fx, "fixer", "alice")), "18 override env out of scope")
	require.NoError(t, override(as("alice"), gp, "alice", nil), "18 human unstamped override")
	// An agent rewriting an existing entry's stamp (keeps createdBy).
	{
		var cur v1alpha1.PolicyGate
		require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(gt), &cur))
		cur.Spec.Overrides[0].Actor = ag(fx, "fixer", "mallory")
		require.Error(t, as(fx).Update(ctx, &cur), "18 agent rewrites existing stamp")
	}
	t.Log("ok 18 overrides")

	// 18b freeze exemptions (docs/design/18-actor-policy.md section 6): the
	// stamp is checked as for overrides, and an agent may not add one at all,
	// stamped or not: break-glass is for people.
	fz := &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-freeze-everyone"},
		Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.Now(), Reason: "INC-1",
			Freeze: &v1alpha1.ChangeWindowFreeze{Actors: "everyone", CreatedBy: "alice"}}}
	require.NoError(t, as("alice").Create(ctx, fz), "18b a person freezes")
	exemptAs := func(c client.Client, by string, a *v1alpha1.Actor) error {
		var cur v1alpha1.ChangeWindow
		require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(fz), &cur))
		cur.Spec.Freeze.Exemptions = append(cur.Spec.Freeze.Exemptions, v1alpha1.FreezeExemption{Namespace: "payments-a",
			Pipeline: "checkout", Reason: "qa", CreatedBy: by, ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)), Actor: a})
		return c.Update(ctx, &cur)
	}
	err = exemptAs(as(fx), fx, ag(fx, "fixer", "alice"))
	require.Error(t, err, "18b an agent's stamped exemption")
	require.Contains(t, err.Error(), "break-glass is for people")
	err = exemptAs(as(fx), fx, nil)
	require.Error(t, err, "18b an agent's unstamped exemption")
	require.Contains(t, err.Error(), "break-glass is for people")
	require.Error(t, exemptAs(as("alice"), "alice", ag("alice", "fixer", "alice")), "18b a person stamped as an agent")
	require.NoError(t, exemptAs(as("alice"), "alice", &v1alpha1.Actor{Kind: "human", Principal: "alice"}), "18b a person's stamped exemption")
	require.NoError(t, exemptAs(as("bob"), "bob", nil), "18b a person may leave the stamp out")
	t.Log("ok 18b freeze exemptions")

	// 18c who paused, released, rejected, froze and lifted (#1939): the
	// record names the requester, and its stamp is checked like any other.
	hu := func(p string) *v1alpha1.Actor { return &v1alpha1.Actor{Kind: "human", Principal: p} }
	app := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "payments-a"},
		Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "https://github.com/acme/app"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}}}}
	require.NoError(t, admin.Create(ctx, app))
	pauseAs := func(c client.Client, ch *v1alpha1.PipelineChange) error {
		var cur v1alpha1.Pipeline
		require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(app), &cur))
		cur.Spec.Paused = !cur.Spec.Paused
		cur.Spec.LastChange = ch
		return c.Update(ctx, &cur)
	}
	// Only by is wrong: the stamp is alice's own, so only the hold-writes
	// policy refuses (two refusing policies answer in no fixed order).
	err = pauseAs(as("alice"), &v1alpha1.PipelineChange{Action: "Pause", By: "bob", Actor: hu("alice")})
	require.Error(t, err, "18c a pause recorded in someone else's name")
	require.Contains(t, err.Error(), "spec.lastChange.by must be your own username")
	require.Error(t, pauseAs(as("alice"), &v1alpha1.PipelineChange{Action: "Pause", By: "alice", Actor: hu("bob")}),
		"18c a forged stamp")
	require.Error(t, pauseAs(as(fx), &v1alpha1.PipelineChange{Action: "Pause", By: fx, Actor: hu(fx)}),
		"18c an agent stamped human")
	require.NoError(t, pauseAs(as("alice"), &v1alpha1.PipelineChange{Action: "Pause", By: "alice", Reason: "INC-7", Actor: hu("alice")}),
		"18c a person's pause")
	require.NoError(t, pauseAs(as(fx), &v1alpha1.PipelineChange{Action: "Resume", By: fx, Actor: ag(fx, "fixer", "alice")}),
		"18c an agent's resume, stamped")
	require.NoError(t, pauseAs(as("bob"), nil), "18c kubectl without a record: admitted, audited with no actor")

	rb := bundle("payments-a", "checkout", nil)
	require.NoError(t, as("alice").Create(ctx, rb))
	rb2 := bundle("payments-a", "checkout", nil) // rejected in 23b, with the index missing
	require.NoError(t, as("alice").Create(ctx, rb2))
	rb.Spec.Rejected = &v1alpha1.BundleRejection{By: "alice", Reason: "bad", Actor: hu("bob")}
	require.Error(t, as("alice").Update(ctx, rb), "18c a rejection with a forged stamp")
	rb.Spec.Rejected.Actor = hu("alice")
	require.NoError(t, as("alice").Update(ctx, rb), "18c a rejection stamped")

	fz2 := &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-freeze-agents"},
		Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.Now(), Reason: "INC-2",
			Freeze: &v1alpha1.ChangeWindowFreeze{Actors: "agents", CreatedBy: fx, Actor: hu(fx)}}}
	require.Error(t, as(fx).Create(ctx, fz2), "18c an agent's freeze stamped human")
	fz2.Spec.Freeze.Actor = ag(fx, "fixer", "alice")
	require.NoError(t, as(fx).Create(ctx, fz2), "18c an agent may freeze (stop the line is not break-glass)")
	var cur v1alpha1.ChangeWindow
	require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(fz2), &cur))
	cur.Spec.Freeze.Lifted = &v1alpha1.FreezeLift{By: "bob", Actor: hu("alice")}
	err = as("alice").Update(ctx, &cur)
	require.Error(t, err, "18c a lift recorded in someone else's name")
	require.Contains(t, err.Error(), "spec.freeze.lifted.by must be your own username")
	cur.Spec.Freeze.Lifted = &v1alpha1.FreezeLift{By: "alice", Actor: hu("alice")}
	require.NoError(t, as("alice").Update(ctx, &cur), "18c a person's lift")
	t.Log("ok 18c who changed what")

	// 19 AuditEvent with actor.
	ae := func(name string, a *v1alpha1.Actor) *v1alpha1.AuditEvent {
		return &v1alpha1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "payments-a"},
			Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.Now(), PipelineName: "checkout", Action: "APIWrite", Outcome: "Success", Actor: a}}
	}
	require.Error(t, as("alice").Create(ctx, ae("e1", &v1alpha1.Actor{Kind: "human", Principal: "alice"})), "19 non-controller audit actor")
	require.NoError(t, as(controllerUser).Create(ctx, ae("e2", &v1alpha1.Actor{Kind: "human", Principal: "alice", Via: "ui"})), "19 controller audit actor")
	require.NoError(t, as("alice").Create(ctx, ae("e3", nil)), "19 audit without actor (pre-existing)")
	// 20 delete without suspend.
	require.Error(t, as("alice").Delete(ctx, &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer"}}), "20 delete unsuspended")
	// 22 adding a stamp to an existing Bundle is refused by the CRD.
	{
		b := bundle("payments-a", "checkout", nil)
		require.NoError(t, as("alice").Create(ctx, b))
		b.Spec.Actor = ag(fx, "fixer", "alice")
		err := as("alice").Update(ctx, b)
		require.Error(t, err, "22 add stamp on update")
		t.Logf("ok 22 %v", err)
	}
	// 23 index deleted: fail closed, even for the controller.
	require.NoError(t, admin.Delete(ctx, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}}))
	require.Eventually(t, func() bool {
		return as("alice").Create(ctx, bundle("payments-a", "checkout", nil)) != nil
	}, 30*time.Second, 500*time.Millisecond, "23 missing index denies")
	t.Logf("ok 23 missing index: %v", as(controllerUser).Create(ctx, bundle("payments-a", "checkout", nil)))

	// 23b the index missing (#1939): a Pipeline write still goes through
	// (actor-records admits), but a stamp still names the requester, with no
	// via and a kind that fits (bot or agent for a ServiceAccount, human or
	// agent for a person): hold-writes, bundle-rejection and freezes check it
	// without the index. Only an agent's agent and sponsor go unchecked.
	err = pauseAs(as("alice"), &v1alpha1.PipelineChange{Action: "Pause", By: "alice", Actor: hu("bob")})
	require.Error(t, err, "23b a stamp naming someone else, index missing")
	require.Contains(t, err.Error(), "an actor stamp must name you")
	require.Error(t, pauseAs(as(fx), &v1alpha1.PipelineChange{Action: "Pause", By: fx, Actor: hu(fx)}),
		"23b a ServiceAccount stamped human, index missing")
	require.Error(t, pauseAs(as("alice"), &v1alpha1.PipelineChange{Action: "Pause", By: "alice",
		Actor: &v1alpha1.Actor{Kind: "human", Principal: "alice", Via: "ui"}}), "23b via, index missing")
	require.NoError(t, pauseAs(as("alice"), &v1alpha1.PipelineChange{Action: "Resume", By: "alice", Actor: hu("alice")}),
		"23b a person's own stamp, index missing")
	// QA's case on #2024: a person stamping agent fixer of carol. Admission
	// cannot see the index, so it admits the write (refusing it would need
	// the index, which would stop every Pipeline write during an outage);
	// the controller does not record the unverified agent stamp
	// (actor.VerifyStamp; TestPipelinePauseAudit_AgentStampVerified).
	require.NoError(t, pauseAs(as("alice"), &v1alpha1.PipelineChange{Action: "Pause", By: "alice",
		Actor: &v1alpha1.Actor{Kind: "agent", Principal: "alice", Agent: "fixer", Sponsor: "carol"}}),
		"23b an agent stamp is admitted while the index is missing; the controller does not record it")
	rb2.Spec.Rejected = &v1alpha1.BundleRejection{By: "alice", Reason: "bad", Actor: hu("bob")}
	require.Error(t, as("alice").Update(ctx, rb2), "23b a rejection stamped with someone else, index missing")
	var fz3 v1alpha1.ChangeWindow
	require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(fz2), &fz3))
	fz3.Spec.Freeze.Lifted = &v1alpha1.FreezeLift{By: "alice", Actor: hu("bob")}
	require.Error(t, as("alice").Update(ctx, &fz3), "23b a lift stamped with someone else, index missing")
	fz3.Spec.Freeze.Lifted = &v1alpha1.FreezeLift{By: "alice", Actor: hu("alice")}
	err = as("alice").Update(ctx, &fz3)
	require.Error(t, err, "23b a stamped lift waits for the index (no window for freezes)")
	require.True(t, strings.Contains(err.Error(), "not ready") || strings.Contains(err.Error(), "no params found"), err.Error())
	t.Log("ok 23b stamps without the index")

	// 24 index recreated, status not yet written (what a replica does first):
	// still closed; the controller, exempt, refuses on its own (503).
	require.NoError(t, admin.Create(ctx, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}}))
	require.Eventually(t, func() bool {
		var idx v1alpha1.AgentIndex
		return admin.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx) == nil
	}, 10*time.Second, 200*time.Millisecond)
	time.Sleep(2 * time.Second) // the policy's param informer sees the new index
	for name, try := range map[string]func() error{
		"suspended agent, no stamp": func() error {
			return as("system:serviceaccount:ci:parked").Create(ctx, bundle("payments-a", "checkout", nil))
		},
		"agent out of scope, no stamp": func() error { return as(fx).Create(ctx, bundle("other", "anything", nil)) },
		"human, no stamp":              func() error { return as("alice").Create(ctx, bundle("payments-a", "checkout", nil)) },
	} {
		err := try()
		require.Error(t, err, "24 an index without status denies: %s", name)
		require.Contains(t, err.Error(), "agent index is not ready", name)
	}
	build()
	require.Eventually(t, func() bool {
		return as("alice").Create(ctx, bundle("payments-a", "checkout", nil)) == nil
	}, 30*time.Second, 500*time.Millisecond, "after the build people pass again")
	require.Error(t, as("system:serviceaccount:ci:parked").Create(ctx, bundle("payments-a", "checkout", nil)),
		"and the suspended agent is denied")
}

// TestActorIndexOverflowOnAPIServer: 700 AgentIdentities of 16 subjects
// each are past the index bound, blocked entries included: the controller
// writes the index empty and not Ready (it fits in etcd), the AgentIdentities
// say IndexFull, and admission refuses the writes it judges until
// AgentIdentities are removed (fail closed). (QA on #1857, F2.)
func TestActorIndexOverflowOnAPIServer(t *testing.T) {
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"))
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
	for _, d := range render(t, "kardinal-promoter") {
		if d.Kind != "ValidatingAdmissionPolicy" && d.Kind != "ValidatingAdmissionPolicyBinding" {
			continue
		}
		var obj unstructured.Unstructured
		require.NoError(t, json.Unmarshal(d.raw, &obj.Object))
		require.NoError(t, admin.Create(ctx, &obj), d.Name)
	}
	require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}}))
	for i := 0; i < 700; i++ {
		subs := make([]v1alpha1.AgentSubject, 16)
		for j := range subs {
			subs[j] = v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: fmt.Sprintf("team-%04d", i), Name: fmt.Sprintf("agent-subject-%02d", j)}
		}
		require.NoError(t, admin.Create(ctx, &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("agent-%04d", i)},
			Spec: v1alpha1.AgentIdentitySpec{Subjects: subs, Sponsor: v1alpha1.AgentSponsor{User: "alice@example.com"},
				Scope: v1alpha1.AgentScope{Namespaces: []string{"team-*"}}}}))
	}
	idxR := &agentidentity.IndexReconciler{Client: admin, Name: "kardinal-promoter"}
	_, err = idxR.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err, "the full index is written: it is small")
	var idx v1alpha1.AgentIndex
	require.NoError(t, admin.Get(ctx, client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	require.Len(t, idx.Status.Conditions, 1)
	require.Equal(t, metav1.ConditionFalse, idx.Status.Conditions[0].Status)
	require.Equal(t, "IndexFull", idx.Status.Conditions[0].Reason)
	_, err = (&agentidentity.Reconciler{Client: admin}).Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKey{Name: "agent-0000"}})
	require.NoError(t, err)
	var id v1alpha1.AgentIdentity
	require.NoError(t, admin.Get(ctx, client.ObjectKey{Name: "agent-0000"}, &id))
	require.False(t, id.Status.Ready)
	require.Equal(t, "IndexFull", id.Status.Conditions[0].Reason)

	c := rest.CopyConfig(cfg)
	c.Impersonate = rest.ImpersonationConfig{UserName: "alice"}
	alice, err := client.New(c, client.Options{Scheme: sch})
	require.NoError(t, err)
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "alice-all"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: "alice"}}}))
	require.Eventually(t, func() bool {
		err := alice.Create(ctx, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{GenerateName: "b-", Namespace: "team-a"},
			Spec: v1alpha1.BundleSpec{Pipeline: "p", Type: "image"}})
		return err != nil && strings.Contains(err.Error(), "agent index is not ready")
	}, 30*time.Second, 500*time.Millisecond, "a full index fails closed")
}
