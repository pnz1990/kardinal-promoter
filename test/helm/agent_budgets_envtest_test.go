//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestAgentBudgetsOnAPIServer (docs/design/24-agent-budgets.md section 4,
// W1): on a real kube-apiserver, the AgentIdentity CRD refuses every invalid
// budget section 4.1 lists and admits valid ones; an AgentIdentity without
// budgets round-trips unchanged; the status ledger (status.usage) and a
// PromotionStep's status.budget are written through the status subresource,
// within their bounds (at most 16 ledgers, 2,000 grants each and 3,000
// between them).
//
// Covers BUDGET-API-01.
func TestAgentBudgetsOnAPIServer(t *testing.T) {
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"), "KUBEBUILDER_ASSETS names the envtest binaries (make envtest-install)")
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(repoRoot(t), "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, v1alpha1.AddToScheme(sch))
	c, err := client.New(cfg, client.Options{Scheme: sch})
	require.NoError(t, err)
	ctx := context.Background()

	// Each case is sent as written (unstructured), so a zero or an empty
	// list reaches the API server rather than being dropped by omitempty.
	n := 0
	identity := func(t *testing.T, extra string) *unstructured.Unstructured {
		t.Helper()
		n++
		doc := fmt.Sprintf(`
apiVersion: kardinal.io/v1alpha1
kind: AgentIdentity
metadata: {name: fixer-%d}
spec:
  subjects: [{kind: ServiceAccount, namespace: ci, name: fixer}]
  sponsor: {user: alice}
  scope: {namespaces: ["*"], pipelines: ["*"], environments: ["*"]}
  autonomy: auto
%s`, n, extra)
		var obj map[string]interface{}
		require.NoError(t, yaml.Unmarshal([]byte(doc), &obj))
		return &unstructured.Unstructured{Object: obj}
	}
	rules := func(k int) string {
		var b strings.Builder
		b.WriteString("  budgets:\n")
		for i := range k {
			fmt.Fprintf(&b, "  - {name: r%d, environments: [env-%d], maxConcurrent: 1}\n", i, i)
		}
		return b.String()
	}
	exemptions := func(k int) string {
		var b strings.Builder
		b.WriteString("  budgetExemptions:\n")
		for i := range k {
			fmt.Fprintf(&b, "  - {name: e%d, namespace: payments, pipeline: checkout, environment: prod, reason: hotfix, "+
				"expiresAt: \"2026-10-15T12:00:00Z\", createdBy: bob}\n", i)
		}
		return b.String()
	}
	// patterns is a rule with k environments, or k namespaces.
	patterns := func(field string, k int) string {
		p := make([]string, k)
		for i := range p {
			p[i] = fmt.Sprintf("p-%d", i)
		}
		if field == "environments" {
			return "  budgets: [{name: prod, environments: [" + strings.Join(p, ", ") + "], maxConcurrent: 1}]"
		}
		return "  budgets: [{name: prod, environments: [prod], namespaces: [" + strings.Join(p, ", ") + "], maxConcurrent: 1}]"
	}
	const exemption = `  budgetExemptions:
  - {name: inc-4211, namespace: payments, pipeline: checkout, environment: prod, bundle: checkout-7f3c2a,
     reason: "hotfix for INC-4211", expiresAt: "2026-10-15T12:00:00Z", createdBy: bob@example.com,
     actor: {kind: human, principal: bob@example.com}}
`
	for _, tc := range []struct{ name, spec, want string }{
		{"the spec's example", `  budgets:
  - {name: prod, environments: [prod, "prod-*"], namespaces: ["*"], versionsPerDay: 5, maxConcurrent: 2,
     fleetTargetsPerHour: 40, maxUnavailable: 1, fleetShare: "25%"}
  - {name: pre-prod, environments: [uat, staging], versionsPerDay: 50}
` + exemption, ""},
		{"fleetShare alone", `  budgets: [{name: fleet, environments: [prod], fleetShare: "100%"}]`, ""},
		{"16 rules", rules(16), ""},
		{"64 environments", patterns("environments", 64), ""},
		{"64 namespaces", patterns("namespaces", 64), ""},
		{"32 exemptions", exemptions(32), ""},
		{"the sum at 1500", `  budgets:
  - {name: a, environments: [prod], fleetTargetsPerHour: 1000}
  - {name: b, environments: [uat], versionsPerDay: 400, maxConcurrent: 100}`, ""},

		{"17 rules", rules(17), "spec.budgets: Too many: 17: must have at most 16 items"},
		{"33 exemptions", exemptions(33), "spec.budgetExemptions: Too many: 33: must have at most 32 items"},
		{"a rule name twice", `  budgets:
  - {name: prod, environments: [prod], maxConcurrent: 1}
  - {name: prod, environments: [uat], maxConcurrent: 1}`, "Duplicate value"},
		{"an exemption name twice", exemption + `  - {name: inc-4211, namespace: payments, pipeline: other, environment: prod,
     reason: again, expiresAt: "2026-10-15T12:00:00Z", createdBy: bob}`, "Duplicate value"},
		{"65 environments", patterns("environments", 65), "spec.budgets[0].environments: Too many: 65: must have at most 64 items"},
		{"65 namespaces", patterns("namespaces", 65), "spec.budgets[0].namespaces: Too many: 65: must have at most 64 items"},
		{"a rule with no limit", `  budgets: [{name: prod, environments: [prod]}]`, "a budget rule sets at least one limit"},
		{"a rule with no environments", `  budgets: [{name: prod, environments: [], maxConcurrent: 1}]`, "spec.budgets[0].environments: Invalid value"},
		{"an empty namespaces list", `  budgets: [{name: prod, environments: [prod], namespaces: [], maxConcurrent: 1}]`,
			"spec.budgets[0].namespaces: Invalid value"},
		{"a star inside an environment", `  budgets: [{name: prod, environments: ["pr*d"], maxConcurrent: 1}]`,
			"spec.budgets[0].environments[0]: Invalid value"},
		{"a star inside a namespace", `  budgets: [{name: prod, environments: [prod], namespaces: ["pay*ments"], maxConcurrent: 1}]`,
			"spec.budgets[0].namespaces[0]: Invalid value"},
		{"a rule name that is not a DNS label", `  budgets: [{name: Prod, environments: [prod], maxConcurrent: 1}]`,
			"spec.budgets[0].name: Invalid value"},
		{"versionsPerDay 0", `  budgets: [{name: prod, environments: [prod], versionsPerDay: 0}]`, "spec.budgets[0].versionsPerDay: Invalid value"},
		{"versionsPerDay 501", `  budgets: [{name: prod, environments: [prod], versionsPerDay: 501}]`, "spec.budgets[0].versionsPerDay: Invalid value"},
		{"maxConcurrent 0", `  budgets: [{name: prod, environments: [prod], maxConcurrent: 0}]`, "spec.budgets[0].maxConcurrent: Invalid value"},
		{"maxConcurrent 101", `  budgets: [{name: prod, environments: [prod], maxConcurrent: 101}]`, "spec.budgets[0].maxConcurrent: Invalid value"},
		{"fleetTargetsPerHour 0", `  budgets: [{name: prod, environments: [prod], fleetTargetsPerHour: 0}]`,
			"spec.budgets[0].fleetTargetsPerHour: Invalid value"},
		{"fleetTargetsPerHour 1001", `  budgets: [{name: prod, environments: [prod], fleetTargetsPerHour: 1001}]`,
			"spec.budgets[0].fleetTargetsPerHour: Invalid value"},
		{"maxUnavailable 0", `  budgets: [{name: prod, environments: [prod], maxUnavailable: 0}]`, "spec.budgets[0].maxUnavailable: Invalid value"},
		{"maxUnavailable 101", `  budgets: [{name: prod, environments: [prod], maxUnavailable: 101}]`, "spec.budgets[0].maxUnavailable: Invalid value"},
		{"fleetShare 0%", `  budgets: [{name: prod, environments: [prod], fleetShare: "0%"}]`, "spec.budgets[0].fleetShare: Invalid value"},
		{"fleetShare 101%", `  budgets: [{name: prod, environments: [prod], fleetShare: "101%"}]`, "spec.budgets[0].fleetShare: Invalid value"},
		{"fleetShare 05%", `  budgets: [{name: prod, environments: [prod], fleetShare: "05%"}]`, "spec.budgets[0].fleetShare: Invalid value"},
		{"fleetShare without %", `  budgets: [{name: prod, environments: [prod], fleetShare: "25"}]`, "spec.budgets[0].fleetShare: Invalid value"},
		{"the sum over 1500", `  budgets:
  - {name: a, environments: [prod], fleetTargetsPerHour: 1000}
  - {name: b, environments: [uat], versionsPerDay: 401, maxConcurrent: 100}`, "sum to at most 1500 over all rules"},
		{"an exemption without a reason", `  budgetExemptions:
  - {name: x, namespace: payments, pipeline: checkout, environment: prod, expiresAt: "2026-10-15T12:00:00Z", createdBy: bob}`,
			"spec.budgetExemptions[0].reason: Required value"},
		{"an exemption without an expiry", `  budgetExemptions:
  - {name: x, namespace: payments, pipeline: checkout, environment: prod, reason: r, createdBy: bob}`,
			"spec.budgetExemptions[0].expiresAt: Required value"},
		{"an exemption without createdBy", `  budgetExemptions:
  - {name: x, namespace: payments, pipeline: checkout, environment: prod, reason: r, expiresAt: "2026-10-15T12:00:00Z"}`,
			"spec.budgetExemptions[0].createdBy: Required value"},
		{"an exemption without a namespace", `  budgetExemptions:
  - {name: x, pipeline: checkout, environment: prod, reason: r, expiresAt: "2026-10-15T12:00:00Z", createdBy: bob}`,
			"spec.budgetExemptions[0].namespace: Required value"},
		{"an exemption without a pipeline", `  budgetExemptions:
  - {name: x, namespace: payments, environment: prod, reason: r, expiresAt: "2026-10-15T12:00:00Z", createdBy: bob}`,
			"spec.budgetExemptions[0].pipeline: Required value"},
		{"an exemption without an environment", `  budgetExemptions:
  - {name: x, namespace: payments, pipeline: checkout, reason: r, expiresAt: "2026-10-15T12:00:00Z", createdBy: bob}`,
			"spec.budgetExemptions[0].environment: Required value"},
		{"an exemption stamped agent without its agent and sponsor", `  budgetExemptions:
  - {name: x, namespace: payments, pipeline: checkout, environment: prod, reason: r, expiresAt: "2026-10-15T12:00:00Z",
     createdBy: bob, actor: {kind: agent, principal: bob}}`, "actor.agent and actor.sponsor are set exactly when actor.kind is agent"},
		{"an exemption reason over 256 characters", `  budgetExemptions:
  - {name: x, namespace: payments, pipeline: checkout, environment: prod, reason: "` + strings.Repeat("r", 257) + `",
     expiresAt: "2026-10-15T12:00:00Z", createdBy: bob}`, "spec.budgetExemptions[0].reason: Too long"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := identity(t, tc.spec)
			err := c.Create(ctx, obj)
			if tc.want != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
				return
			}
			require.NoError(t, err)
			var got v1alpha1.AgentIdentity
			require.NoError(t, c.Get(ctx, client.ObjectKey{Name: obj.GetName()}, &got))
			for _, r := range got.Spec.Budgets {
				assert.NotEmpty(t, r.Namespaces, "rule %s: namespaces default to every namespace", r.Name)
			}
		})
	}

	t.Run("a rule's namespaces default to every namespace", func(t *testing.T) {
		obj := identity(t, `  budgets: [{name: prod, environments: [prod], maxConcurrent: 1}]`)
		require.NoError(t, c.Create(ctx, obj))
		var got v1alpha1.AgentIdentity
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: obj.GetName()}, &got))
		assert.Equal(t, []string{"*"}, got.Spec.Budgets[0].Namespaces)
	})

	t.Run("an AgentIdentity without budgets round-trips unchanged", func(t *testing.T) {
		obj := identity(t, `  description: fixes flaky tests
  signingIdentities: [{issuer: "https://token.actions.githubusercontent.com", subject: "repo:acme/fixer"}]`)
		sent, _, err := unstructured.NestedMap(obj.Object, "spec")
		require.NoError(t, err)
		require.NoError(t, c.Create(ctx, obj))
		got := &unstructured.Unstructured{}
		got.SetGroupVersionKind(obj.GroupVersionKind())
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: obj.GetName()}, got))
		stored, _, err := unstructured.NestedMap(got.Object, "spec")
		require.NoError(t, err)
		assert.Equal(t, sent, stored, "the stored spec is the one sent: budgets add no default to an AgentIdentity without them")
		_, has := got.Object["status"]
		assert.False(t, has, "no status, so no status.usage, until a controller writes one")
	})

	// The ledger and the step's place: status subresources, within bounds.
	// ledger is ledgers ledgers of grants grants each.
	ledger := func(grants, ledgers int) map[string]v1alpha1.AgentUsage {
		at := metav1.Now()
		out := map[string]v1alpha1.AgentUsage{}
		for l := range ledgers {
			u := v1alpha1.AgentUsage{ObservedGeneration: 1, EvaluatedAt: &at,
				Rules: []v1alpha1.AgentBudgetRuleUsage{{Name: "prod", Environments: []string{"prod"}, Namespaces: []string{"*"},
					Versions:   &v1alpha1.AgentBudgetCount{Limit: 5, Used: 5, WindowStart: &at, FreesAt: &at},
					Concurrent: &v1alpha1.AgentBudgetCount{Limit: 2, Used: 1}, FleetShare: "25%", Waiting: 3,
					Exhausted: []string{"versionsPerDay"}}},
				Exemptions: []v1alpha1.AgentBudgetExemptionState{{Name: "inc-4211", FirstSeenAt: at, ExpiresAt: at}},
				Conditions: []metav1.Condition{{Type: "Current", Status: metav1.ConditionTrue, Reason: "Evaluated", LastTransitionTime: at}}}
			for g := range grants {
				u.Grants = append(u.Grants, v1alpha1.AgentBudgetGrant{BundleUID: fmt.Sprintf("6b1f0000-0000-0000-0000-%012d", g),
					Environment: "prod-eu", Fleet: "prod", Rules: []string{"prod"}, GrantedAt: at, Version: g == 0})
			}
			out[fmt.Sprintf("kardinal-%d", l)] = u
		}
		return out
	}
	for _, tc := range []struct {
		name            string
		grants, ledgers int
		want            string
	}{
		{"a ledger", 3, 1, ""},
		{"2,000 grants", 2000, 1, ""},
		{"2,001 grants", 2001, 1, "status.usage.kardinal-0.grants: Too many: 2001: must have at most 2000 items"},
		{"17 ledgers", 1, 17, "status.usage: Too many: 17: must have at most 16 items"},
		{"3,000 grants between two ledgers", 1500, 2, ""},
		{"3,002 grants between two ledgers", 1501, 2, "the ledgers hold at most 3000 grants between them"},
	} {
		t.Run("status.usage: "+tc.name, func(t *testing.T) {
			obj := identity(t, `  budgets: [{name: prod, environments: [prod], versionsPerDay: 5, maxConcurrent: 2}]`)
			require.NoError(t, c.Create(ctx, obj))
			var id v1alpha1.AgentIdentity
			require.NoError(t, c.Get(ctx, client.ObjectKey{Name: obj.GetName()}, &id))
			id.Status.Usage = ledger(tc.grants, tc.ledgers)
			err := c.Status().Update(ctx, &id)
			if tc.want != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
				return
			}
			require.NoError(t, err)
			var got v1alpha1.AgentIdentity
			require.NoError(t, c.Get(ctx, client.ObjectKey{Name: obj.GetName()}, &got))
			require.Len(t, got.Status.Usage, tc.ledgers)
			assert.Len(t, got.Status.Usage["kardinal-0"].Grants, tc.grants)
		})
	}

	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}}))
	for _, tc := range []struct{ name, state, want string }{
		{"Waiting", "Waiting", ""},
		{"Granted", "Granted", ""},
		{"an unknown state", "Exempt", `status.budget.state: Unsupported value: "Exempt"`},
	} {
		t.Run("PromotionStep status.budget: "+tc.name, func(t *testing.T) {
			step := &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: "checkout-prod-" + strings.ToLower(tc.name[:2]), Namespace: "payments"},
				Spec: v1alpha1.PromotionStepSpec{PipelineName: "checkout", BundleName: "checkout-7f3c2a", Environment: "prod",
					StepType: "kustomize-set-image"}}
			require.NoError(t, c.Create(ctx, step))
			since := metav1.Now()
			step.Status.Budget = &v1alpha1.PromotionStepBudget{Agent: "fixer", State: tc.state, Since: &since, Rules: []string{"prod"}}
			err := c.Status().Update(ctx, step)
			if tc.want != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
				return
			}
			require.NoError(t, err)
			var got v1alpha1.PromotionStep
			require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(step), &got))
			require.NotNil(t, got.Status.Budget)
			assert.Equal(t, tc.state, got.Status.Budget.State)
		})
	}
}
