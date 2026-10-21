//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// warnings collects the API server's Warning headers.
type warnings struct {
	mu   sync.Mutex
	seen []string
}

func (w *warnings) HandleWarningHeader(_ int, _ string, text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen = append(w.seen, text)
}

func (w *warnings) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.seen
	w.seen = nil
	return out
}

// TestAgentBudgetPoliciesOnAPIServer runs section 12's admission rules on a
// real kube-apiserver with the chart's policies and the API server's own RBAC
// authorizer: who may change budgets (manage) and add or remove exemptions
// (exempt); createdBy and the actor stamp naming the requester; the agent's
// party refused; an agent, whether a ServiceAccount, a User or a Group
// subject, refused by <release>-actor; an existing exemption immutable; the
// warning while budgets are off; and the controller's stamp-policy check
// following the binding (QA-3 on #2456).
// Covers BUDGET-ADMISSION-01.
func TestAgentBudgetPoliciesOnAPIServer(t *testing.T) {
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
		switch d.Kind {
		case "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding":
		case "ClusterRole":
			if d.Name != "kardinal-promoter-agent-budget-exempter" && d.Name != "kardinal-promoter-admin-extra" {
				continue
			}
		default:
			continue
		}
		var obj unstructured.Unstructured
		require.NoError(t, json.Unmarshal(d.raw, &obj.Object))
		require.NoError(t, admin.Create(ctx, &obj), d.Name)
	}
	bind := func(name, role string, subjects ...rbacv1.Subject) {
		require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name},
			RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}, Subjects: subjects}))
	}
	user := func(n string) rbacv1.Subject {
		return rbacv1.Subject{Kind: "User", APIGroup: rbacv1.GroupName, Name: n}
	}
	group := func(n string) rbacv1.Subject {
		return rbacv1.Subject{Kind: "Group", APIGroup: rbacv1.GroupName, Name: n}
	}
	sa := func(ns, n string) rbacv1.Subject {
		return rbacv1.Subject{Kind: "ServiceAccount", Namespace: ns, Name: n}
	}
	bind("qa-root", "cluster-admin", user("root"), sa(releaseNS, "kardinal-promoter"))
	// Every authenticated user reads AgentIdentities (actors.directory.public).
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "qa-read"},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{"kardinal.io"}, Resources: []string{"agentidentities"},
			Verbs: []string{"get", "list", "watch"}}}}))
	bind("qa-read", "qa-read", group("system:authenticated"))
	bind("qa-exempters", "kardinal-promoter-agent-budget-exempter", user("bob"), user("carol"), user("alice"),
		group("oidc:agents-fixer"), group("oidc:agents-grp"), sa("ci", "fixer"), sa("ci", "other"))
	bind("qa-admin", "kardinal-promoter-admin-extra", user("dana"))

	mk := func(name, sponsor string, subs ...v1alpha1.AgentSubject) {
		require.NoError(t, admin.Create(ctx, &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.AgentIdentitySpec{Subjects: subs, Sponsor: v1alpha1.AgentSponsor{User: sponsor},
				Scope:   v1alpha1.AgentScope{Namespaces: []string{"*"}},
				Budgets: []v1alpha1.AgentBudgetRule{{Name: "prod", Environments: []string{"prod"}, VersionsPerDay: 5}}}}))
		time.Sleep(1100 * time.Millisecond) // distinct creationTimestamps
	}
	mk("fixer", "alice", v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"},
		v1alpha1.AgentSubject{Kind: "Group", Name: "oidc:agents-fixer"})
	mk("other", "carol", v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: "ci", Name: "other"})
	mk("grp", "carol", v1alpha1.AgentSubject{Kind: "Group", Name: "oidc:agents-grp"})
	_, err = (&agentidentity.IndexReconciler{Client: asController(t, cfg, sch), Name: "kardinal-promoter"}).Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	warn := &warnings{}
	as := func(name string, groups ...string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: name, Groups: append(groups, "system:authenticated")}
		c.WarningHandler = warn
		cl, err := client.New(c, client.Options{Scheme: sch})
		require.NoError(t, err)
		return cl
	}
	human := func(p string) *v1alpha1.Actor { return &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: p} }
	exemption := func(name, by string, a *v1alpha1.Actor) v1alpha1.AgentBudgetExemption {
		return v1alpha1.AgentBudgetExemption{Name: name, Namespace: "payments", Pipeline: "checkout", Environment: "prod",
			Reason: "hotfix", ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)), CreatedBy: by, Actor: a}
	}
	// update applies f to fixer as c, from its current state, with a merge
	// patch (the exempter role has patch, not update; kardinal override
	// --budget patches).
	update := func(c client.Client, f func(*v1alpha1.AgentIdentity)) error {
		var cur v1alpha1.AgentIdentity
		require.NoError(t, admin.Get(ctx, client.ObjectKey{Name: "fixer"}, &cur))
		base := cur.DeepCopy()
		f(&cur)
		return c.Patch(ctx, &cur, client.MergeFrom(base))
	}
	addExemption := func(e v1alpha1.AgentBudgetExemption) func(*v1alpha1.AgentIdentity) {
		return func(id *v1alpha1.AgentIdentity) {
			id.Spec.BudgetExemptions = append(id.Spec.BudgetExemptions, e)
			if id.Labels == nil {
				id.Labels = map[string]string{}
			}
			id.Labels["kardinal.io/budget-exemptions"] = "true" // what kardinal override --budget sets
		}
	}
	// Wait for the policies to load: an unstamped exemption is refused.
	require.Eventually(t, func() bool {
		return update(as("bob"), addExemption(exemption("probe", "bob", nil))) != nil
	}, 30*time.Second, 500*time.Millisecond)

	fixerSA, otherSA := "system:serviceaccount:ci:fixer", "system:serviceaccount:ci:other"
	for _, tc := range []struct {
		name string
		c    client.Client
		f    func(*v1alpha1.AgentIdentity)
		want string // "" admits; else one of the "|"-separated messages (two policies may refuse it)
	}{
		{"an admin raises the budget", as("root"), func(id *v1alpha1.AgentIdentity) { id.Spec.Budgets[0].VersionsPerDay = 6 }, ""},
		{"the chart's admin role raises it", as("dana"), func(id *v1alpha1.AgentIdentity) { id.Spec.Budgets[0].VersionsPerDay = 7 }, ""},
		{"an exempter cannot", as("bob"), func(id *v1alpha1.AgentIdentity) { id.Spec.Budgets[0].VersionsPerDay = 50 }, "needs the manage verb"},
		{"an exempter adds an exemption", as("bob"), addExemption(exemption("inc-1", "bob", human("bob"))), ""},
		{"a forged createdBy", as("bob"), addExemption(exemption("inc-2", "carol", human("bob"))), "your own username"},
		{"no actor stamp", as("bob"), addExemption(exemption("inc-2", "bob", nil)), "needs an actor stamp"},
		{"a forged stamp", as("bob"), addExemption(exemption("inc-2", "bob", human("carol"))), "actor stamp naming you|actor must be"},
		{"someone without the exempt verb", as("erin"), addExemption(exemption("inc-2", "erin", human("erin"))), ""},
		{"the sponsor", as("alice"), addExemption(exemption("inc-2", "alice", human("alice"))), "party"},
		{"a member of its Group subject", as("oidc:bot1", "oidc:agents-fixer"),
			addExemption(exemption("inc-2", "oidc:bot1", &v1alpha1.Actor{Kind: "agent", Principal: "oidc:bot1", Agent: "fixer", Sponsor: "alice"})),
			"party|an agent may not let an agent through its budget"},
		{"its ServiceAccount subject", as(fixerSA),
			addExemption(exemption("inc-2", fixerSA, &v1alpha1.Actor{Kind: "agent", Principal: fixerSA, Agent: "fixer", Sponsor: "alice"})),
			"party|an agent may not let an agent through its budget"},
		{"another agent (a ServiceAccount)", as(otherSA),
			addExemption(exemption("inc-2", otherSA, &v1alpha1.Actor{Kind: "agent", Principal: otherSA, Agent: "other", Sponsor: "carol"})),
			"an agent may not let an agent through its budget"},
		{"another agent claiming to be a bot", as(otherSA),
			addExemption(exemption("inc-2", otherSA, &v1alpha1.Actor{Kind: "bot", Principal: otherSA})),
			"actor must be|an agent may not let an agent through its budget"},
		{"another agent (a Group member)", as("oidc:bot2", "oidc:agents-grp"),
			addExemption(exemption("inc-2", "oidc:bot2", &v1alpha1.Actor{Kind: "agent", Principal: "oidc:bot2", Agent: "grp", Sponsor: "carol"})),
			"an agent may not let an agent through its budget"},
		{"a Group member claiming to be a person", as("oidc:bot2", "oidc:agents-grp"),
			addExemption(exemption("inc-2", "oidc:bot2", human("oidc:bot2"))), "actor must be|an agent may not let an agent through its budget"},
		{"change an existing exemption", as("root"), func(id *v1alpha1.AgentIdentity) { id.Spec.BudgetExemptions[0].Reason = "longer" },
			"never changes"},
		{"a second exemption", as("carol"), addExemption(exemption("inc-3", "carol", human("carol"))), ""},
		{"remove one", as("carol"), func(id *v1alpha1.AgentIdentity) { id.Spec.BudgetExemptions = id.Spec.BudgetExemptions[:1] }, ""},
		{"an exemption without the label", as("bob"), func(id *v1alpha1.AgentIdentity) {
			id.Spec.BudgetExemptions = append(id.Spec.BudgetExemptions, exemption("inc-4", "bob", human("bob")))
			delete(id.Labels, "kardinal.io/budget-exemptions")
		}, "carries the label kardinal.io/budget-exemptions"},
	} {
		err := update(tc.c, tc.f)
		switch {
		case tc.name == "someone without the exempt verb":
			require.Error(t, err, tc.name) // RBAC refuses the patch itself
		case tc.want == "":
			require.NoError(t, err, tc.name)
		default:
			require.Error(t, err, tc.name)
			found := false
			for _, w := range strings.Split(tc.want, "|") {
				found = found || strings.Contains(err.Error(), w)
			}
			assert.True(t, found, "%s: %v", tc.name, err)
		}
		t.Logf("ok %-45s %v", tc.name, err)
	}

	// An identity without exemptions needs no AgentIndex: its writes are not
	// matched by the label-scoped actor policy, whose param is resolved first.
	require.NoError(t, asController(t, cfg, sch).Delete(ctx, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}}),
		"the controller may delete it (the agent-index guard refuses anyone else)")
	require.NoError(t, admin.Create(ctx, &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "newcomer"},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "User", Name: "oidc:newcomer"}},
			Sponsor: v1alpha1.AgentSponsor{User: "alice"}, Scope: v1alpha1.AgentScope{Namespaces: []string{"*"}}}}),
		"registering an agent while the index is missing")
	err = update(as("carol"), addExemption(exemption("inc-5", "carol", human("carol"))))
	require.Error(t, err, "an exemption while the index is missing: its stamp cannot be checked")
	_, err = (&agentidentity.IndexReconciler{Client: asController(t, cfg, sch), Name: "kardinal-promoter"}).Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	// Budgets off (the default render): a write that sets budgets is
	// admitted with a warning.
	warn.take()
	require.NoError(t, update(as("root"), func(id *v1alpha1.AgentIdentity) { id.Spec.Budgets[0].VersionsPerDay = 8 }))
	assert.Contains(t, strings.Join(warn.take(), "\n"), "agent budgets are off in this release (actors.budgets.enabled): they are kept but not enforced")
	require.NoError(t, update(as("root"), func(id *v1alpha1.AgentIdentity) { id.Spec.Description = "fixes flaky tests" }))
	assert.Empty(t, warn.take(), "an edit that leaves the budgets alone is not warned")

	// The controller counts a stamped exemption only while the stamp policy
	// and its binding exist.
	check := func() bool {
		for _, n := range []string{"kardinal-promoter-agent-budgets", "kardinal-promoter-actor-budgets"} {
			if !(&policygate.IdentityPolicyCheck{Reader: asController(t, cfg, sch), Name: n, TTL: time.Nanosecond}).Active(ctx) {
				return false
			}
		}
		return true
	}
	assert.True(t, check(), "installed and bound")
	require.NoError(t, admin.Delete(ctx, &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter-agent-budgets"}}))
	assert.False(t, check(), "the binding deleted: no stamped exemption counts")
}
