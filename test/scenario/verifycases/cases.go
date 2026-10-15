// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package verifycases is the cases of the verify scenarios
// (test/scenario/verify/part*): verify gates end to end on the scenario World.
package verifycases

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

// verifyRun is a verify scenario: a linear Pipeline dev → staging → prod
// whose prod gate verifies staging (policy), a version shipped by shipper,
// the in-cluster agent's attempts (by attempt; past the list, the last),
// and what the people and agents around do (act, once the agents ran).
type verifyRun struct {
	name     string
	policy   v1alpha1.GateVerifyPolicy
	shipper  string
	attempts []scenario.AgentResult
	// agent, when set, decides every attempt instead.
	agent scenario.Agent
	// setup changes the cluster before the version ships.
	setup func(t *testing.T, w *scenario.World, p *scenario.Plan)
	// act runs once staging is Verified and the agents had their rounds.
	act func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle)
	// verified: prod ends Verified; else the gate holds it, with reason.
	verified bool
	reason   string
	check    func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle, g v1alpha1.PolicyGate)
}

func pass(s string) scenario.AgentResult {
	return scenario.AgentResult{Result: v1alpha1.VerdictPass, Summary: s}
}
func fail(s string) scenario.AgentResult {
	return scenario.AgentResult{Result: v1alpha1.VerdictFail, Summary: s}
}
func inconclusive(s string) scenario.AgentResult {
	return scenario.AgentResult{Result: v1alpha1.VerdictInconclusive, Summary: s}
}

// The verifiers around a verify scenario: QAAgent (sponsor carol, run in
// the cluster), agent bob-qa (sponsor bob, who also sponsors fixer, the
// matrix's agent shipper), QA (QAAgent from outside the cluster, by its own
// ServiceAccount) and QALead, a person.
var (
	bobQA      = scenario.Identity{Username: scenario.SAUser("bob-qa"), Groups: []string{"system:serviceaccounts", "system:authenticated"}}
	qaOutside  = scenario.Identity{Username: scenario.SAUser(scenario.QAAgent), Groups: []string{"system:serviceaccounts", "system:authenticated"}}
	qaLeadUser = scenario.Identity{Username: scenario.QALead, Groups: []string{"system:authenticated"}}
)

func explore(required, maxFailures, count int, inconclusive string, retries int) v1alpha1.GateVerifyPolicy {
	p := v1alpha1.GateVerifyPolicy{Check: scenario.VerifyCheck, Required: required, MaxFailures: maxFailures,
		AllowedAgents: []string{scenario.QAAgent, "bob-qa", "fixer"}, AllowedUsers: []string{scenario.QALead}, OnInconclusive: inconclusive, Retries: retries}
	if count > 0 {
		p.Run = &v1alpha1.GateVerifyRun{Template: scenario.QAAgent, Count: count}
	}
	return p
}

func gateReason(g v1alpha1.PolicyGate) string { return g.Status.Reason }

// smokeCheck is the second check of the two-check case, whose own run
// votes once smokeVotes is set.
const smokeCheck = "smoke"

var smokeVotes atomic.Bool

// misdirectingAgent runs both checks: the explore run writes its Verdict for
// smoke in dev (labels and spec; admission checks neither), and smoke's own
// run waits until smokeVotes, then passes.
func misdirectingAgent(run v1alpha1.AgentRun) scenario.AgentResult {
	if run.Spec.Check == smokeCheck {
		if !smokeVotes.Load() {
			return scenario.AgentResult{Wait: true}
		}
		return pass("smoke passes")
	}
	r := pass("smoke, said the explore run")
	r.Edit = func(v map[string]interface{}) {
		spec := v["spec"].(map[string]interface{})
		spec["check"], spec["environment"] = smokeCheck, "dev"
		labels := v["metadata"].(map[string]interface{})["labels"].(map[string]interface{})
		labels["kardinal.io/check"], labels["kardinal.io/environment"] = smokeCheck, "dev"
	}
	return r
}

// gateByCheck is bundle's verify gate instance for check.
func gateByCheck(t *testing.T, w *scenario.World, ns, bundle, check string) v1alpha1.PolicyGate {
	t.Helper()
	for _, g := range w.GateInstances(ns, bundle) {
		if g.Spec.Verify != nil && g.Spec.Verify.Check == check {
			return g
		}
	}
	t.Fatalf("no %s gate instance for %s", check, bundle)
	return v1alpha1.PolicyGate{}
}

// Parts is how many packages the verify scenarios run in
// (test/scenario/verify/part1..3), so each fits a CI shard's budget.
const Parts = 3

// Run runs part (1-based) of parts of the verify scenarios: verify gates
// end to end. The Graph plans the
// AgentRuns, the AgentRun reconciler runs them bounded, the agent's Pod
// writes its Verdict as the run's own ServiceAccount through the chart's
// admission policies, the Graph copies the Verdicts into the gate and the
// gate counts them.
func Run(t *testing.T, part, parts int) {
	runs := []verifyRun{
		{name: "quorum: 2 of 3 runs, one fail tolerated", policy: explore(2, 1, 3, "", 0), shipper: "human",
			attempts: nil, // by run below
			verified: true, check: func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle, g v1alpha1.PolicyGate) {
				assert.Contains(t, gateReason(g), "verified: 2 of 2")
				counted := 0
				for _, v := range g.Status.Verdicts {
					if v.Counted {
						counted++
					}
				}
				assert.Equal(t, 3, counted, "every run's verdict counts: %+v", g.Status.Verdicts)
			}},
		{name: "quorum: a fail past maxFailures fails the gate", policy: explore(2, 0, 2, "", 0), shipper: "human",
			reason: "verification failed"},
		{name: "inconclusive holds", policy: explore(1, 0, 1, "hold", 0), shipper: "human",
			attempts: []scenario.AgentResult{inconclusive("the page did not load")}, reason: "waiting for verdicts: 0 of 1",
			check: func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle, g v1alpha1.PolicyGate) {
				assert.Len(t, agentRuns(t, w, p.NS), 1, "hold never retries")
			}},
		{name: "inconclusive fails with onInconclusive fail", policy: explore(1, 0, 1, "fail", 0), shipper: "human",
			attempts: []scenario.AgentResult{inconclusive("the page did not load")}, reason: "verification failed"},
		{name: "a flaky agent: inconclusive, retried, passes", policy: explore(1, 0, 1, "retry", 1), shipper: "human",
			attempts: []scenario.AgentResult{inconclusive("timed out loading"), pass("works on the second try")}, verified: true,
			check: func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle, g v1alpha1.PolicyGate) {
				assert.Len(t, agentRuns(t, w, p.NS), 2, "one retry")
			}},
		{name: "a flaky agent: no verdict, then a crash, is retried like an inconclusive", policy: explore(1, 0, 1, "retry", 2),
			shipper: "human", attempts: []scenario.AgentResult{{}, {Crash: true}, pass("third time")}, verified: true,
			check: func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle, g v1alpha1.PolicyGate) {
				assert.Len(t, agentRuns(t, w, p.NS), 3)
			}},
		{name: "a flaky agent past its retries holds", policy: explore(1, 0, 1, "retry", 1), shipper: "human",
			attempts: []scenario.AgentResult{inconclusive("flaky")}, reason: "waiting for verdicts: 0 of 1",
			check: func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle, g v1alpha1.PolicyGate) {
				assert.Len(t, agentRuns(t, w, p.NS), 2, "the retries are bounded")
			}},
		{name: "a verdict for another version's UID is not counted", policy: explore(1, 0, 0, "", 0), shipper: "human",
			act: func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle) {
				require.NoError(t, w.RecordVerdict(qaLeadUser, p.NS, b.Name, "staging", scenario.VerifyCheck, v1alpha1.VerdictPass,
					func(v *v1alpha1.Verdict) {
						// Written by hand for the version that had this
						// name before: its UID, and no owner (admission
						// refuses an owner other than the Bundle it names).
						v.Spec.BundleUID, v.OwnerReferences = "uid-of-the-previous-version", nil
					}))
			}, reason: "waiting for verdicts: 0 of 1",
			check: func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle, g v1alpha1.PolicyGate) {
				for _, v := range g.Status.Verdicts {
					assert.False(t, v.Counted, "%+v", v)
				}
				// The same verifier, on this version, opens it.
				require.NoError(t, w.RecordVerdict(qaLeadUser, p.NS, b.Name, "staging", scenario.VerifyCheck, v1alpha1.VerdictPass, nil))
				expectProd(t, w, p, b, true)
			}},
		{name: "the author agent's own verdict does not count", policy: explore(1, 0, 0, "", 0), shipper: "agent",
			act: func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle) {
				writeOrRefused(t, w, scenario.ActorIdentities["agent"], p, b)
			}, reason: "waiting for verdicts: 0 of 1",
			check: func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle, g v1alpha1.PolicyGate) {
				// Another party's agent opens it.
				require.NoError(t, w.RecordVerdict(qaOutside, p.NS, b.Name, "staging", scenario.VerifyCheck, v1alpha1.VerdictPass, nil))
				expectProd(t, w, p, b, true)
			}},
		{name: "a verdict by the author's sponsor's other agent does not count", policy: explore(1, 0, 0, "", 0), shipper: "agent",
			act: func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle) {
				writeOrRefused(t, w, bobQA, p, b)
			}, reason: "waiting for verdicts: 0 of 1",
			check: func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle, g v1alpha1.PolicyGate) {
				require.NoError(t, w.RecordVerdict(qaOutside, p.NS, b.Name, "staging", scenario.VerifyCheck, v1alpha1.VerdictPass, nil))
				expectProd(t, w, p, b, true)
			}},
		{name: "a run's Verdict for another check and environment does not count there", policy: explore(1, 0, 1, "", 0),
			shipper: "human", agent: misdirectingAgent,
			setup: func(t *testing.T, w *scenario.World, p *scenario.Plan) {
				// A second verify gate on prod: check smoke on dev, the same
				// agent allowed. Its own run waits until smokeVotes.
				smoke := p.Gate.DeepCopy()
				smoke.Name, smoke.ResourceVersion, smoke.UID = "smoke-gate", "", ""
				sp := explore(1, 0, 1, "", 0)
				sp.Check, sp.Environment = smokeCheck, "dev"
				smoke.Spec.Verify = &sp
				w.MustApply(smoke)
			},
			act: func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle) {
				smoke := gateByCheck(t, w, p.NS, b.Name, smokeCheck)
				assert.False(t, smoke.Status.Ready, "the smoke gate opened on the explore run's Verdict")
				assert.Contains(t, smoke.Status.Reason, "waiting for verdicts: 0 of 1")
				for _, v := range smoke.Status.Verdicts {
					assert.False(t, v.Counted && strings.Contains(v.Name, "-"+scenario.VerifyCheck+"-"), "the explore run's Verdict counted for smoke: %+v", v)
				}
				// The explore run's Verdict was admitted: admission does not
				// check the check or the environment, the Graph does.
				var vl v1alpha1.VerdictList
				require.NoError(t, w.Client.List(w.Ctx, &vl, client.InNamespace(p.NS)))
				misdirected := 0
				for _, v := range vl.Items {
					if v.Spec.Run != nil && strings.Contains(v.Name, "-"+scenario.VerifyCheck+"-") && v.Spec.Check == smokeCheck {
						misdirected++
					}
				}
				assert.Equal(t, 1, misdirected, "the explore run's Verdict for smoke is admitted")
				// Smoke's own run votes: the smoke gate opens.
				smokeVotes.Store(true)
				require.True(t, w.RunUntil(scenario.MaxRounds, func() bool { return gateByCheck(t, w, p.NS, b.Name, smokeCheck).Status.Ready }),
					"the smoke gate opens on its own run's pass\n%s", w.Describe(p.NS))
			},
			reason: "waiting for verdicts: 0 of 1"},
		{name: "bounds: a suspended agent's run never starts", policy: explore(1, 0, 1, "", 0), shipper: "human",
			setup: func(t *testing.T, w *scenario.World, p *scenario.Plan) {
				require.NoError(t, w.EditAgent(scenario.QAAgent, func(a *v1alpha1.AgentIdentity) { a.Spec.Suspended = true }))
			}, reason: "waiting for verdicts: 0 of 1",
			check: expectUnbounded("suspended")},
		{name: "bounds: an agent scoped elsewhere never runs here", policy: explore(1, 0, 1, "", 0), shipper: "human",
			setup: func(t *testing.T, w *scenario.World, p *scenario.Plan) {
				require.NoError(t, w.EditAgent(scenario.QAAgent, func(a *v1alpha1.AgentIdentity) { a.Spec.Scope.Namespaces = []string{"other-team"} }))
			}, reason: "waiting for verdicts: 0 of 1",
			check: expectUnbounded("scope")},
		{name: "bounds: a Secret not labelled for the template is not handed to it", policy: explore(1, 0, 1, "", 0), shipper: "human",
			setup: func(t *testing.T, w *scenario.World, p *scenario.Plan) {
				// The Pipeline's git credentials, by name.
				var tmpl v1alpha1.StepTemplate
				require.NoError(t, w.Client.Get(w.Ctx, client.ObjectKey{Name: scenario.QAAgent}, &tmpl))
				tmpl.Spec.SecretEnv = []v1alpha1.StepTemplateSecretEnv{{Name: "TOKEN", Secret: scenario.GitSecret, Key: "token"}}
				require.NoError(t, w.Client.Update(scenario.AsUser(w.Ctx, scenario.AdminUser, scenario.AdminGroups...), &tmpl))
			}, reason: "waiting for verdicts: 0 of 1",
			check: expectUnbounded(scenario.GitSecret)},
	}
	// The quorum runs: run 1 passes, run 2 fails, run 3 passes.
	byRun := map[string]map[int]scenario.AgentResult{
		runs[0].name: {1: pass("checkout works"), 2: fail("coupon SAVE10 returns 500"), 3: pass("refunds work")},
		runs[1].name: {1: pass("checkout works"), 2: fail("coupon SAVE10 returns 500")},
	}
	for i, vr := range runs {
		if i%parts != part-1 {
			continue
		}
		t.Run(vr.name, func(t *testing.T) {
			t.Parallel()
			w, p, b := startVerify(t, vr, byRun[vr.name])
			if !w.RunUntil(scenario.MaxRounds, func() bool {
				for _, e := range w.Verified(p.NS, b.Name) {
					if e == "staging" {
						return true
					}
				}
				return false
			}) {
				t.Fatalf("staging not Verified\n%s", w.Describe(p.NS))
			}
			for i := 0; i < 2*scenario.BlockRounds; i++ {
				w.Round()
			}
			if vr.act != nil {
				vr.act(t, w, p, b)
			}
			expectProd(t, w, p, b, vr.verified)
			g := verifyGate(t, w, p.NS, b.Name)
			if vr.reason != "" {
				assert.Contains(t, g.Status.Reason, vr.reason, "the gate's reason")
			}
			assert.Empty(t, w.AgentVerdictRefusals(), "every run's own Verdict is admitted")
			if vr.check != nil {
				vr.check(t, w, p, b, g)
			}
			scenario.CheckInvariants(t, w, p)
		})
	}
}

// startVerify starts vr's World and ships its version.
func startVerify(t *testing.T, vr verifyRun, byRun map[int]scenario.AgentResult) (*scenario.World, *scenario.Plan, *v1alpha1.Bundle) {
	t.Helper()
	ns := "team-a"
	c := scenario.Combo{Update: "kustomize", Health: "deployment", Gate: "verify", Topology: "linear", Delivery: "direct", SCM: scenario.SCMGitHub,
		Outcome: "success", Actor: vr.shipper}
	w := scenario.NewWorld(t, scenario.Options{SCM: c.SCM, PolicyNamespaces: []string{scenario.PolicyNS}, Actors: true})
	p := scenario.BuildPlan(c, ns, w.Now(), false)
	policy := vr.policy
	policy.Environment = "staging"
	p.Gate.Spec.Verify = &policy
	w.Agents = map[string]scenario.Agent{scenario.QAAgent: func(run v1alpha1.AgentRun) scenario.AgentResult {
		if vr.agent != nil {
			return vr.agent(run)
		}
		if r, ok := byRun[run.Spec.Run]; ok {
			return r
		}
		if len(vr.attempts) == 0 {
			return pass("it works")
		}
		return vr.attempts[min(run.Spec.Attempt, len(vr.attempts))-1]
	}}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("after %d rounds:\n%s", w.Rounds(), w.Describe(ns))
		}
	})
	require.NoError(t, p.Setup(w))
	bob := scenario.AgentIdentityOf("bob-qa", []v1alpha1.AgentSubject{scenario.SASubject("bob-qa")}, []string{ns}, false)
	w.MustApply(append(scenario.AgentFixture(ns), bob)...)
	if vr.setup != nil {
		w.Round()
		vr.setup(t, w, p)
	}
	w.MustApply(p.Pipeline)
	w.Round()
	w.Round()
	b := scenario.ImageBundle(ns, scenario.PipelineName, "1.1.0")
	p.Ship(t, w, b)
	return w, p, b
}

// expectProd checks whether b reaches prod: Verified there, or held.
func expectProd(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle, verified bool) {
	t.Helper()
	if verified {
		require.True(t, w.RunUntil(scenario.MaxRounds, func() bool { return w.BundlePhase(p.NS)[b.Name] == "Verified" }),
			"bundle %s not Verified\n%s", b.Name, w.Describe(p.NS))
		p.ExpectRunning(t, w, "1.1.0", p.Envs...)
		return
	}
	for i := 0; i < scenario.BlockRounds; i++ {
		w.Round()
	}
	assert.NotContains(t, w.Verified(p.NS, b.Name), "prod", "the gate held prod")
	assert.NotEqual(t, "1.1.0", p.Running(w)["prod"], "prod runs the unverified version")
}

func verifyGate(t *testing.T, w *scenario.World, ns, bundle string) v1alpha1.PolicyGate {
	t.Helper()
	for _, g := range w.GateInstances(ns, bundle) {
		if g.Spec.Verify != nil && g.Spec.Verify.Check == scenario.VerifyCheck {
			return g
		}
	}
	t.Fatalf("no verify gate instance for %s", bundle)
	return v1alpha1.PolicyGate{}
}

func agentRuns(t *testing.T, w *scenario.World, ns string) []v1alpha1.AgentRun {
	t.Helper()
	var l v1alpha1.AgentRunList
	require.NoError(t, w.Client.List(w.Ctx, &l, client.InNamespace(ns)))
	return l.Items
}

// writeOrRefused writes id's pass on b and checks the gate does not count
// it, whether admission refused it or the gate did.
func writeOrRefused(t *testing.T, w *scenario.World, id scenario.Identity, p *scenario.Plan, b *v1alpha1.Bundle) {
	t.Helper()
	err := w.RecordVerdict(id, p.NS, b.Name, "staging", scenario.VerifyCheck, v1alpha1.VerdictPass, nil)
	if err != nil {
		assert.Contains(t, err.Error(), "refused", "only admission may refuse it: %v", err)
		return
	}
	for i := 0; i < 3; i++ {
		w.Round()
	}
	g := verifyGate(t, w, p.NS, b.Name)
	found := false
	for _, v := range g.Status.Verdicts {
		if strings.HasSuffix(v.Summary, " by "+id.Username) {
			found = true
			assert.False(t, v.Counted, "the author party's verdict counted: %+v", v)
			assert.NotEmpty(t, v.Reason, "the gate says why it does not count")
		}
	}
	assert.True(t, found, "the gate lists the verdict it did not count: %+v", g.Status.Verdicts)
}

// expectUnbounded checks the gate's AgentRun failed its bounds, with why,
// before anything ran: no Job, no ServiceAccount of its own.
func expectUnbounded(why string) func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle, g v1alpha1.PolicyGate) {
	return func(t *testing.T, w *scenario.World, p *scenario.Plan, b *v1alpha1.Bundle, g v1alpha1.PolicyGate) {
		t.Helper()
		runs := agentRuns(t, w, p.NS)
		require.NotEmpty(t, runs)
		for _, r := range runs {
			assert.Equal(t, v1alpha1.AgentRunFailed, r.Status.Phase, r.Name)
			c := meta.FindStatusCondition(r.Status.Conditions, v1alpha1.AgentRunConditionBounded)
			if assert.NotNil(t, c, "%s has a Bounded condition", r.Name) {
				assert.Equal(t, metav1.ConditionFalse, c.Status)
				assert.Contains(t, c.Message+" "+r.Status.Message, why)
			}
			var job batchv1.Job
			assert.Error(t, w.Client.Get(w.Ctx, client.ObjectKey{Namespace: r.Namespace, Name: r.Name}, &job), "no Job for an unbounded run")
			var sa corev1.ServiceAccount
			assert.Error(t, w.Client.Get(w.Ctx, client.ObjectKey{Namespace: r.Namespace, Name: "kr-" + r.Name}, &sa), "no identity for it")
		}
	}
}
