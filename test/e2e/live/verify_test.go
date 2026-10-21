//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// verifierRole is the chart's verifier ClusterRole (user-roles.yaml): read,
// create and revoke Verdicts.
const verifierRole = framework.ControllerDeployment + "-verifier"

// agentRunTimeout bounds an AgentRun from its environment's Verified to its
// Verdict: the Job's Pod starts from a loaded image and probes for seconds.
const agentRunTimeout = 3 * time.Minute

// registerAgent registers agent <name>-<ns> sponsored by sponsor (its
// subject is a placeholder user: an AgentRun runs as its own ServiceAccount,
// not as one of the agent's), and the test agent image as StepTemplate
// <name>-<ns> for it, with env. It returns the agent's (and the template's)
// name. The cleanup suspends the agent before deleting it, as admission
// requires.
func registerAgent(t *testing.T, e *framework.Env, ns, name, sponsor string, env map[string]string) string {
	t.Helper()
	ctx := context.Background()
	image := os.Getenv("KARDINAL_E2E_TEST_AGENT_IMAGE")
	require.NotEmpty(t, image, "KARDINAL_E2E_TEST_AGENT_IMAGE: hack/e2e/components/test-agent.sh did not run")
	agent := name + "-" + ns
	id := &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: agent},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "User", Name: agent + "@agents.example.com"}},
			Sponsor: v1alpha1.AgentSponsor{User: sponsor}, Scope: v1alpha1.AgentScope{Namespaces: []string{ns}},
			Autonomy: v1alpha1.AutonomyApprove}}
	require.NoError(t, e.Client.Create(ctx, id))
	t.Cleanup(func() {
		var cur v1alpha1.AgentIdentity
		if err := e.Client.Get(context.Background(), client.ObjectKeyFromObject(id), &cur); err != nil {
			return
		}
		cur.Spec.Suspended = true
		_ = e.Client.Update(context.Background(), &cur)
		_ = e.Client.Delete(context.Background(), &cur)
	})
	var envs []v1alpha1.StepTemplateEnv
	for k, v := range env {
		envs = append(envs, v1alpha1.StepTemplateEnv{Name: k, Value: v})
	}
	tmpl := &v1alpha1.StepTemplate{ObjectMeta: metav1.ObjectMeta{Name: agent},
		Spec: v1alpha1.StepTemplateSpec{Image: image, Agent: agent, Timeout: "5m", Env: envs}}
	require.NoError(t, e.Client.Create(ctx, tmpl))
	t.Cleanup(func() { _ = e.Client.Delete(context.Background(), tmpl) })
	framework.Eventually(t, time.Minute, "agent "+agent+" registered and its StepTemplate ready", func(ctx context.Context) (bool, string) {
		var got v1alpha1.AgentIdentity
		var st v1alpha1.StepTemplate
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(id), &got); err != nil {
			return false, err.Error()
		}
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(tmpl), &st); err != nil {
			return false, err.Error()
		}
		return got.Status.Ready && st.Status.Ready, fmt.Sprintf("agent ready=%v (%s), template ready=%v (%s)",
			got.Status.Ready, got.Status.Reason, st.Status.Ready, st.Status.Reason)
	})
	return agent
}

// agentRuns lists the Bundle's AgentRuns.
func agentRuns(ctx context.Context, e *framework.Env, ns, bundle string) ([]v1alpha1.AgentRun, error) {
	var list v1alpha1.AgentRunList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": bundle}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// waitRuns waits until n AgentRuns of bundle finished, every one Succeeded.
func waitRuns(t *testing.T, e *framework.Env, ns, bundle string, n int) []v1alpha1.AgentRun {
	t.Helper()
	var runs []v1alpha1.AgentRun
	framework.Eventually(t, agentRunTimeout, fmt.Sprintf("%d AgentRuns finished", n), func(ctx context.Context) (bool, string) {
		var err error
		runs, err = agentRuns(ctx, e, ns, bundle)
		if err != nil {
			return false, err.Error()
		}
		done := 0
		var states []string
		for _, r := range runs {
			states = append(states, fmt.Sprintf("%s=%s (%s)", r.Name, r.Status.Phase, r.Status.Message))
			if r.Status.Phase == v1alpha1.AgentRunFailed {
				t.Fatalf("AgentRun %s failed: %s", r.Name, r.Status.Message)
			}
			if r.Status.Phase == v1alpha1.AgentRunSucceeded {
				done++
			}
		}
		return len(runs) == n && done == n, strings.Join(states, ", ")
	})
	return runs
}

// verifyGate is a prod gate waiting for check explore in uat.
func verifyGate(ns string, p v1alpha1.GateVerifyPolicy) *v1alpha1.PolicyGate {
	g := framework.Gate(ns, "explore-qa", "prod", "true", recheck)
	p.Check = "explore"
	g.Spec.Verify = &p
	return g
}

// TestGate_VerifyAgentRuns registers a test agent (hack/e2e/testagent) and a
// StepTemplate for it, and puts a verify gate on prod that runs it twice and
// needs both runs to pass. Once uat is Verified the Graph creates two
// AgentRuns. Each runs its Job as its own ServiceAccount (kr-<run>), bound
// only to the chart's AgentRun roles, with a NetworkPolicy that blocks every
// address but DNS, the API server and the namespace under test (the agent
// finds the webhook receiver unreachable). Each run's Pod writes one
// run-bound Verdict; its attempt at a second, unbound Verdict is refused by
// admission. The gate waits, then passes with "verified: 2 of 2", and prod
// promotes. status.verdicts names the run's agent and sponsor, from the
// AgentRun. Each run's ServiceAccount and RoleBindings are gone once it
// finished.
//
// Covers GATE-VERIFY-01, GATE-VERIFY-02, GATE-VERIFY-03.
func TestGate_VerifyAgentRuns(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "uat", "prod")
	forbidden := strings.TrimRight(os.Getenv(framework.EnvReceiverURL), "/") + "/_healthz"
	require.NotEqual(t, "/_healthz", forbidden, "the webhook receiver is installed in the core suite")
	// kindnet enforces a NetworkPolicy for a new Pod about a second after it
	// gets its IP (TestChart_NetworkPolicy): the agent waits before probing.
	agent := registerAgent(t, e, a.ns, "qa", "bob@example.com",
		map[string]string{"PROBE_FORBIDDEN": forbidden, "PROBE_DELAY": "5s", "PROBE_EXTRA": "1"})
	e.CreateGate(t, verifyGate(a.ns, v1alpha1.GateVerifyPolicy{Required: 2, AllowedAgents: []string{agent},
		Run: &v1alpha1.GateVerifyRun{Template: agent, Count: 2, Namespace: a.ns}}))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "Verified", promoteTimeout)
	runs := waitRuns(t, e, a.ns, bundle, 2)
	e.WaitGateReady(t, a.ns, bundle, "prod", "explore-qa", true, "verified: 2 of 2 ("+agent+" ×2)", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("prod"), fixtures.Image+":"+fixtures.V2, syncTimeout)

	for _, r := range runs {
		assert.True(t, meta.IsStatusConditionTrue(r.Status.Conditions, v1alpha1.AgentRunConditionBounded), r.Name)
		assert.Equal(t, agent, r.Status.Agent)
		assert.Equal(t, "kr-"+r.Name, r.Status.ServiceAccount)
		job, err := e.Kube.BatchV1().Jobs(a.ns).Get(ctx, r.Name, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "kr-"+r.Name, job.Spec.Template.Spec.ServiceAccountName)
		assert.False(t, *job.Spec.Template.Spec.AutomountServiceAccountToken)
		np, err := e.Kube.NetworkingV1().NetworkPolicies(a.ns).Get(ctx, r.Name, metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, r.UID, metav1.GetControllerOf(np).UID)
		var v v1alpha1.Verdict
		require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: r.Name}, &v), "the run's Verdict is named after it")
		assert.Equal(t, "pass", v.Spec.Result, v.Spec.Summary)
		assert.Equal(t, "system:serviceaccount:"+a.ns+":kr-"+r.Name, v.Spec.By)
		require.NotNil(t, v.Spec.Run)
		titles := []string{}
		for _, f := range v.Spec.Findings {
			titles = append(titles, f.Severity+": "+f.Title)
		}
		assert.Contains(t, titles, "info: egress blocked: "+forbidden, "the NetworkPolicy blocks the receiver")
		assert.Contains(t, titles, "info: second vote refused", "admission refuses a run's second Verdict")
		// Its access is gone.
		framework.Eventually(t, time.Minute, "run "+r.Name+"'s ServiceAccount deleted", func(ctx context.Context) (bool, string) {
			_, err := e.Kube.CoreV1().ServiceAccounts(a.ns).Get(ctx, "kr-"+r.Name, metav1.GetOptions{})
			return apierrors.IsNotFound(err), fmt.Sprint(err)
		})
	}
	gate, ok, err := e.GateInstance(ctx, a.ns, bundle, "prod", "explore-qa")
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, gate.Status.Verdicts, 2)
	for _, rec := range gate.Status.Verdicts {
		assert.True(t, rec.Counted, rec.Reason)
		assert.Equal(t, "agent "+agent+" for bob@example.com", rec.Verifier)
	}
}

// TestGate_VerifyWithApprovalGates (#2271): prod has a verify gate with no
// approval policy, an approval gate with one, and a gate that only reads
// approvals.* in its expression, in one Graph. Every instance is created
// (none carries a null spec.approval, which the API server refuses): the
// verify gate passes on the agent's run, both approval gates pass on
// carol's approval, and prod promotes.
//
// Covers GATE-VERIFY-09.
func TestGate_VerifyWithApprovalGates(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "uat", "prod")
	agent := registerAgent(t, e, a.ns, "qa", "bob@example.com", nil)
	e.CreateGate(t, verifyGate(a.ns, v1alpha1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{agent},
		Run: &v1alpha1.GateVerifyRun{Template: agent, Count: 1, Namespace: a.ns}}))
	people := framework.Gate(a.ns, "people", "prod", "true", recheck)
	people.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
	e.CreateGate(t, people)
	e.CreateGate(t, framework.Gate(a.ns, "count-one", "prod", "approvals.count >= 1", recheck))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "Verified", promoteTimeout)
	waitRuns(t, e, a.ns, bundle, 1)
	e.WaitGateReady(t, a.ns, bundle, "prod", "explore-qa", true, "verified: 1 of 1", gateTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "people", false, "", gateTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "count-one", false, "", gateTimeout)

	r := c.Exec(framework.CLIOptions{Kubeconfig: userKubeconfig(t, e, a.ns, "carol@example.com")}, c.Args(a.ns, "approve", bundle, "--env", "prod")...)
	require.Zero(t, r.Code, r.Output())
	e.WaitGateReady(t, a.ns, bundle, "prod", "people", true, "", gateTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "count-one", true, "", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}

// TestGate_VerifyRetryAndSuspend: with onInconclusive: retry, a run whose
// first attempt is inconclusive gets a second attempt from the Graph, whose
// pass verifies the gate; a suspended agent's AgentRun is refused
// (Bounded=False) and creates no Job.
//
// Covers GATE-VERIFY-04, GATE-VERIFY-07.
func TestGate_VerifyRetryAndSuspend(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "uat", "prod")
	agent := registerAgent(t, e, a.ns, "flaky", "bob@example.com", map[string]string{"PROBE_RESULTS": "inconclusive,pass"})
	e.CreateGate(t, verifyGate(a.ns, v1alpha1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{agent},
		OnInconclusive: "retry", Retries: 1, Run: &v1alpha1.GateVerifyRun{Template: agent, Count: 1, Namespace: a.ns}}))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "Verified", promoteTimeout)
	runs := waitRuns(t, e, a.ns, bundle, 2)
	attempts := map[int]bool{}
	for _, r := range runs {
		attempts[r.Spec.Attempt] = true
	}
	assert.Equal(t, map[int]bool{1: true, 2: true}, attempts, "attempt 2 after the inconclusive attempt 1")
	e.WaitGateReady(t, a.ns, bundle, "prod", "explore-qa", true, "verified: 1 of 1 ("+agent+")", gateTimeout)

	// The agent is suspended: the next version's run is refused.
	var id v1alpha1.AgentIdentity
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Name: agent}, &id))
	id.Spec.Suspended = true
	require.NoError(t, e.Client.Update(ctx, &id))
	next := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V1)
	e.WaitStepState(t, a.ns, pipelineName, next, "uat", "Verified", promoteTimeout)
	// Its attempt 1 is refused, and so is the retry the Graph admits after
	// an attempt that ended without a verdict.
	framework.Eventually(t, agentRunTimeout, "the suspended agent's AgentRuns are refused", func(ctx context.Context) (bool, string) {
		runs, err := agentRuns(ctx, e, a.ns, next)
		if err != nil || len(runs) == 0 {
			return false, fmt.Sprintf("%d AgentRuns (%v)", len(runs), err)
		}
		var states []string
		ok := true
		for _, r := range runs {
			states = append(states, r.Name+": "+r.Status.Phase+": "+r.Status.Message)
			ok = ok && r.Status.Phase == v1alpha1.AgentRunFailed &&
				meta.IsStatusConditionFalse(r.Status.Conditions, v1alpha1.AgentRunConditionBounded) &&
				strings.Contains(r.Status.Message, "is suspended")
		}
		return ok, strings.Join(states, "; ")
	})
	nextRuns, err := agentRuns(ctx, e, a.ns, next)
	require.NoError(t, err)
	for _, r := range nextRuns {
		_, err = e.Kube.BatchV1().Jobs(a.ns).Get(ctx, r.Name, metav1.GetOptions{})
		assert.True(t, apierrors.IsNotFound(err), "no Job for a suspended agent: %v", err)
	}
}

// TestGate_VerifyOnePartyUIDTimeout: the version is created by alice. The
// AgentRun of an agent alice sponsors writes a pass that the gate records
// as not counted (the author's party), a person's pass for another version
// of the Bundle (another UID) is not counted either, and the gate times out
// as configured ("verification timed out after 2m").
//
// Covers GATE-VERIFY-08.
func TestGate_VerifyOnePartyUIDTimeout(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "uat", "prod")
	agent := registerAgent(t, e, a.ns, "selfish", "alice@example.com", nil)
	e.CreateGate(t, verifyGate(a.ns, v1alpha1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{agent}, Timeout: "2m",
		Run: &v1alpha1.GateVerifyRun{Template: agent, Count: 1, Namespace: a.ns}}))
	a.apply(t, a.pipeline(nil))
	alice := userKubeconfig(t, e, a.ns, "alice@example.com")
	_, err := e.Kube.RbacV1().RoleBindings(a.ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-promoter", Namespace: a.ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: framework.ControllerDeployment + "-promoter"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: "alice@example.com"}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	waitBoundNamed(t, e, a.ns, "alice-promoter")
	r := c.Exec(framework.CLIOptions{Kubeconfig: alice}, c.Args(a.ns, "create", "bundle", pipelineName, "--image", fixtures.Image+":"+fixtures.V2)...)
	require.Equal(t, 0, r.Code, r.Output())
	var bundle string
	framework.Eventually(t, time.Minute, "alice's Bundle", func(ctx context.Context) (bool, string) {
		var list v1alpha1.BundleList
		if err := e.Client.List(ctx, &list, client.InNamespace(a.ns)); err != nil || len(list.Items) != 1 {
			return false, fmt.Sprintf("%d Bundles (%v)", len(list.Items), err)
		}
		bundle = list.Items[0].Name
		return true, ""
	})
	e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "Verified", promoteTimeout)
	waitRuns(t, e, a.ns, bundle, 1)

	// carol's pass names this Bundle but another UID.
	carol := impersonating(t, e, a.ns, "carol@example.com", nil, []string{"verdicts"}, "create")
	require.NoError(t, carol.Create(ctx, &v1alpha1.Verdict{ObjectMeta: metav1.ObjectMeta{Name: "other-version", Namespace: a.ns,
		Labels: map[string]string{"kardinal.io/bundle": bundle, "kardinal.io/environment": "uat", "kardinal.io/check": "explore"}},
		Spec: v1alpha1.VerdictSpec{Pipeline: pipelineName, Bundle: bundle, BundleUID: "uid-of-an-earlier-" + bundle,
			Environment: "uat", Check: "explore", Result: "pass", By: "carol@example.com"}}))

	e.WaitGateReady(t, a.ns, bundle, "prod", "explore-qa", false, "verification timed out after 2m: 0 of 1", 4*time.Minute)
	gate, ok, err := e.GateInstance(ctx, a.ns, bundle, "prod", "explore-qa")
	require.NoError(t, err)
	require.True(t, ok)
	reasons := map[string]string{}
	for _, rec := range gate.Status.Verdicts {
		assert.False(t, rec.Counted, rec.Name)
		reasons[rec.Name] = rec.Reason
	}
	assert.NotContains(t, reasons, "other-version", "the Graph does not copy another version's Verdict")
	require.Len(t, reasons, 1)
	for _, why := range reasons {
		assert.Contains(t, why, "the author's party (alice@example.com)")
	}
	a.noStep(t, bundle, "prod", 15*time.Second)
}

// TestGate_VerifyAgentsOnly (#2411): a verify gate that lists only agents
// counts no person's verdict (allowedAgents, allowedUsers and allowedGroups
// are one allowlist). A person with the verifier role records a pass with
// kardinal verify: the Verdict is admitted and kardinal verify says the
// gate does not count it and how to list people; the gate keeps waiting and
// prod gets no step. Once the gate's template lists the person in
// allowedUsers, the change reaches the version in flight: the same pass
// counts and prod promotes.
//
// Covers GATE-VERIFY-11.
func TestGate_VerifyAgentsOnly(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "uat", "prod")
	gate := verifyGate(a.ns, v1alpha1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{"qa-explorer"}})
	e.CreateGate(t, gate)
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "explore-qa", false, "waiting for verdicts: 0 of 1", gateTimeout)

	const carol = "carol@example.com"
	kc := userKubeconfig(t, e, a.ns, carol)
	_, err := e.Kube.RbacV1().RoleBindings(a.ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "carol-verifier", Namespace: a.ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: verifierRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: carol}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	waitBoundNamed(t, e, a.ns, "carol-verifier")
	r := c.Exec(framework.CLIOptions{Kubeconfig: kc},
		c.Args(a.ns, "verify", pipelineName, "uat", "--check", "explore", "--pass", "--summary", "checkout works")...)
	require.Equal(t, 0, r.Code, r.Output())
	assert.Contains(t, r.Stdout, "Recorded: "+carol+" finds "+bundle+" pass in uat for explore")
	assert.Contains(t, r.Stdout, "not counted by gate explore-qa: "+carol+" is not an allowed verifier: the gate counts only "+
		"the agents in verify.allowedAgents (qa-explorer); list people in verify.allowedUsers or allowedGroups to count them")
	var verdicts v1alpha1.VerdictList
	require.NoError(t, e.Client.List(ctx, &verdicts, client.InNamespace(a.ns)))
	require.Len(t, verdicts.Items, 1, "the Verdict is admitted and kept")
	assert.Equal(t, carol, verdicts.Items[0].Spec.By)
	framework.Consistently(t, 20*time.Second, "the gate counts no person's verdict", func(ctx context.Context) (bool, string) {
		g, ok, err := e.GateInstance(ctx, a.ns, bundle, "prod", "explore-qa")
		if err != nil || !ok {
			return false, fmt.Sprint(ok, err)
		}
		return !g.Status.Ready && strings.Contains(g.Status.Reason, "waiting for verdicts: 0 of 1"), framework.DescribeGate(g)
	})
	a.noStep(t, bundle, "prod", 5*time.Second)

	// The gate's template lists carol: the change reaches the version in
	// flight, and her pass counts.
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var g v1alpha1.PolicyGate
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(gate), &g); err != nil {
			return err
		}
		g.Spec.Verify.AllowedUsers = []string{carol}
		return e.Client.Update(ctx, &g)
	}))
	e.WaitGateReady(t, a.ns, bundle, "prod", "explore-qa", true, "verified: 1 of 1 ("+carol+")", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
}

// TestGate_VerifyByPerson: a person records verdicts with kardinal verify. A
// fail blocks prod with "verification failed: <who>: <summary>", --revoke
// and a pass let it promote; a Verdict in someone else's name, and one that
// claims an AgentRun from outside its Pod, are refused by admission.
//
// Covers GATE-VERIFY-05, GATE-VERIFY-06, CLI-VERIFY-01.
func TestGate_VerifyByPerson(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "uat", "prod")
	e.CreateGate(t, verifyGate(a.ns, v1alpha1.GateVerifyPolicy{Required: 1}))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "explore-qa", false, "waiting for verdicts: 0 of 1", gateTimeout)

	carol := userKubeconfig(t, e, a.ns, "carol@example.com")
	_, err := e.Kube.RbacV1().RoleBindings(a.ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "carol-verifier", Namespace: a.ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: verifierRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: "carol@example.com"}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	waitBoundNamed(t, e, a.ns, "carol-verifier")
	as := func(args ...string) framework.CLIResult {
		return c.Exec(framework.CLIOptions{Kubeconfig: carol}, c.Args(a.ns, args...)...)
	}
	r := as("verify", pipelineName, "uat", "--check", "explore", "--fail", "--summary", "the checkout page is blank")
	require.Equal(t, 0, r.Code, r.Output())
	assert.Contains(t, r.Stdout, "Recorded: carol@example.com finds "+bundle+" fail in uat for explore")
	assert.Contains(t, r.Stdout, "counted by gate explore-qa")
	e.WaitGateReady(t, a.ns, bundle, "prod", "explore-qa", false,
		"verification failed: carol@example.com: the checkout page is blank", gateTimeout)
	a.noStep(t, bundle, "prod", 15*time.Second)

	r = as("verify", pipelineName, "uat", "--check", "explore", "--revoke")
	require.Equal(t, 0, r.Code, r.Output())
	r = as("verify", pipelineName, "uat", "--check", "explore", "--pass", "--summary", "fixed by a reload")
	require.Equal(t, 0, r.Code, r.Output())
	e.WaitGateReady(t, a.ns, bundle, "prod", "explore-qa", true, "verified: 1 of 1 (carol@example.com)", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	r = as("verify", "list", bundle)
	require.Equal(t, 0, r.Code, r.Output())
	assert.Contains(t, r.Stdout, "yes (explore-qa)")

	// Admission: not in someone else's name, and no run claim from outside the run's Pod.
	b := getBundle(t, e, a.ns, bundle)
	mallory := impersonating(t, e, a.ns, "mallory@example.com", []string{"devs"}, []string{"verdicts"}, "create", "delete")
	forged := func(name, by string, run *v1alpha1.VerdictRun) *v1alpha1.Verdict {
		return &v1alpha1.Verdict{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.ns,
			Labels: map[string]string{"kardinal.io/bundle": bundle, "kardinal.io/environment": "uat", "kardinal.io/check": "explore"}},
			Spec: v1alpha1.VerdictSpec{Pipeline: pipelineName, Bundle: bundle, BundleUID: string(b.UID), Environment: "uat",
				Check: "explore", Result: "pass", By: by, Run: run}}
	}
	err = mallory.Create(ctx, forged("forged-by", "carol@example.com", nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `spec.by must be your own username "mallory@example.com"`)
	err = mallory.Create(ctx, forged("run-1", "mallory@example.com", &v1alpha1.VerdictRun{Name: "run-1", Index: 1, Attempt: 1}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only the Pod of that run may write it")
	// Someone else cannot revoke carol's verdict.
	list := &v1alpha1.VerdictList{}
	require.NoError(t, e.Client.List(ctx, list, client.InNamespace(a.ns)))
	require.NotEmpty(t, list.Items)
	err = mallory.Delete(ctx, &list.Items[0])
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only carol@example.com can revoke this Verdict")
}

// TestUI_BrowserVerdicts checks a verify gate's verdicts in the UI (#1908).
// prod waits at a verify gate that needs two passing verdicts and holds an
// inconclusive one. carol records a pass with an evidence link and a high
// finding; dave records an inconclusive verdict. The stage drawer and the
// Activity tab show 1 of 2 with one inconclusive waiting, each verdict with
// its verifier's actor badge and its result in words, carol's evidence
// linked, dave's inconclusive saying it holds; both pass axe.
//
// Covers UI-VERDICTS-01.
func TestUI_BrowserVerdicts(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "uat", "prod")
	e.CreateGate(t, verifyGate(a.ns, v1alpha1.GateVerifyPolicy{Required: 2, OnInconclusive: v1alpha1.VerifyHold}))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "explore-qa", false, "waiting for verdicts: 0 of 2", gateTimeout)

	const carol, dave = "carol@example.com", "dave@example.com"
	const evidence = "https://ci.example.com/explore/1"
	verify := func(user string, args ...string) {
		t.Helper()
		rb, err := e.Kube.RbacV1().RoleBindings(a.ns).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "verifier-", Namespace: a.ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: verifierRole},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: user}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		waitBoundNamed(t, e, a.ns, rb.Name)
		r := c.Exec(framework.CLIOptions{Kubeconfig: userKubeconfig(t, e, a.ns, user)},
			c.Args(a.ns, append([]string{"verify", pipelineName, "uat", "--check", "explore"}, args...)...)...)
		require.Equal(t, 0, r.Code, r.Output())
	}
	verify(carol, "--pass", "--summary", "checkout works", "--evidence", evidence, "--finding", "high:slow receipts page")
	verify(dave, "--inconclusive", "--summary", "the sandbox timed out")
	e.WaitGate(t, a.ns, bundle, "prod", "explore-qa", gateTimeout, "two verdicts recorded, one counted", func(g *v1alpha1.PolicyGate) bool {
		return len(g.Status.Verdicts) == 2 && !g.Status.Ready
	})
	framework.Playwright(t, "verdicts.spec.ts", browserEnv(mainUI(t, e), a.ns,
		"KARDINAL_UI_PASSER", carol, "KARDINAL_UI_INCONCLUSIVE", dave, "KARDINAL_UI_EVIDENCE", evidence))
}
