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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// verifierRole is the chart's verifier ClusterRole (user-roles.yaml): read
// and create Verdicts.
const verifierRole = framework.ControllerDeployment + "-verifier"

// agentRunTimeout bounds an AgentRun from its environment's Verified to its
// Verdict: the Job's Pod starts from a loaded image and probes for seconds.
const agentRunTimeout = 3 * time.Minute

// registerAgent registers ServiceAccount name of ns as agent <name>-<ns>
// sponsored by sponsor, binds it to the verifier role and lets it list the
// namespace's Services (what the test agent probes), plus extra rules, and
// registers the test agent image as StepTemplate <name>-<ns> running as it,
// with env. It returns the agent's and the template's name. The cleanup
// suspends the agent before deleting it, as admission requires.
func registerAgent(t *testing.T, e *framework.Env, ns, name, sponsor string, env map[string]string, extra ...rbacv1.PolicyRule) string {
	t.Helper()
	ctx := context.Background()
	image := os.Getenv("KARDINAL_E2E_TEST_AGENT_IMAGE")
	require.NotEmpty(t, image, "KARDINAL_E2E_TEST_AGENT_IMAGE: hack/e2e/components/test-agent.sh did not run")
	agent := name + "-" + ns
	_, err := e.Kube.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = e.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-verifier", Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: verifierRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: ns}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	rules := append([]rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"services"}, Verbs: []string{"get", "list"}}}, extra...)
	_, err = e.Kube.RbacV1().Roles(ns).Create(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name + "-probe", Namespace: ns},
		Rules: rules}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = e.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-probe", Namespace: ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name + "-probe"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: ns}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	id := &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: agent},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: ns, Name: name}},
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
		Spec: v1alpha1.StepTemplateSpec{Image: image, ServiceAccountName: name, Timeout: "5m", Env: envs}}
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

// verifyGate is a prod gate waiting for check explore in uat.
func verifyGate(ns string, p v1alpha1.GateVerifyPolicy) *v1alpha1.PolicyGate {
	g := framework.Gate(ns, "explore-qa", "prod", "true", recheck)
	p.Check = "explore"
	g.Spec.Verify = &p
	return g
}

// TestGate_VerifyAgentRuns registers a test agent (hack/e2e/testagent) as an
// agent and a StepTemplate, and puts a verify gate on prod that runs it twice
// and needs both runs to pass. Once uat is Verified the Graph creates two
// AgentRuns; each runs a restricted Job as the agent, with a NetworkPolicy
// that blocks every address but DNS, the API server and the namespace under
// test (the agent finds the webhook receiver unreachable); each run's Pod
// writes one run-bound Verdict. The gate waits ("waiting for verdicts"), then
// passes with "verified: 2 of 2", and prod promotes. The verdicts are in the
// gate's status.verdicts, each written by the agent and bound to its run.
//
// Covers GATE-VERIFY-01, GATE-VERIFY-02, GATE-VERIFY-03.
func TestGate_VerifyAgentRuns(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "uat", "prod")
	forbidden := strings.TrimRight(os.Getenv(framework.EnvReceiverURL), "/") + "/_healthz"
	require.NotEqual(t, "/_healthz", forbidden, "the webhook receiver is installed in the core suite")
	agent := registerAgent(t, e, a.ns, "qa", "bob@example.com", map[string]string{"PROBE_FORBIDDEN": forbidden})
	e.CreateGate(t, verifyGate(a.ns, v1alpha1.GateVerifyPolicy{Required: 2, AllowedAgents: []string{agent},
		Run: &v1alpha1.GateVerifyRun{Template: agent, Count: 2, Namespace: a.ns}}))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "Verified", promoteTimeout)

	framework.Eventually(t, agentRunTimeout, "both AgentRuns finished", func(ctx context.Context) (bool, string) {
		runs, err := agentRuns(ctx, e, a.ns, bundle)
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
		return len(runs) == 2 && done == 2, strings.Join(states, ", ")
	})
	e.WaitGateReady(t, a.ns, bundle, "prod", "explore-qa", true, "verified: 2 of 2 ("+agent+" ×2)", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("prod"), fixtures.Image+":"+fixtures.V2, syncTimeout)

	// Each run: bounded, its Job run as the agent with the restricted Pod, its
	// NetworkPolicy, and one Verdict bound to the run, written by the agent.
	runs, err := agentRuns(ctx, e, a.ns, bundle)
	require.NoError(t, err)
	for _, r := range runs {
		assert.True(t, meta.IsStatusConditionTrue(r.Status.Conditions, v1alpha1.AgentRunConditionBounded), r.Name)
		assert.Equal(t, agent, r.Status.Agent)
		var job batchv1.Job
		require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: r.Name}, &job))
		assert.Equal(t, "qa", job.Spec.Template.Spec.ServiceAccountName)
		assert.False(t, *job.Spec.Template.Spec.AutomountServiceAccountToken)
		var np networkingv1.NetworkPolicy
		require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: r.Name}, &np))
		assert.Equal(t, r.UID, metav1.GetControllerOf(&np).UID)
		var v v1alpha1.Verdict
		require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: r.Name}, &v), "the run's Verdict is named after it")
		assert.Equal(t, "pass", v.Spec.Result, v.Spec.Summary)
		assert.Equal(t, "system:serviceaccount:"+a.ns+":qa", v.Spec.By)
		require.NotNil(t, v.Spec.Actor)
		assert.Equal(t, agent, v.Spec.Actor.Agent)
		require.NotNil(t, v.Spec.Run)
		assert.Equal(t, r.Spec.Run, v.Spec.Run.Index)
		titles := []string{}
		for _, f := range v.Spec.Findings {
			titles = append(titles, f.Severity+": "+f.Title)
		}
		assert.Contains(t, titles, "info: egress blocked: "+forbidden, "the NetworkPolicy blocks the receiver")
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

// TestGate_VerifyRefusesWideAgent: an agent whose RBAC lets it patch
// Deployments in the namespace under test is not run: its AgentRun fails
// with Bounded=False naming the grant, no Job is created, and the gate keeps
// waiting.
//
// Covers GATE-VERIFY-04.
func TestGate_VerifyRefusesWideAgent(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "uat", "prod")
	agent := registerAgent(t, e, a.ns, "wide", "bob@example.com", nil,
		rbacv1.PolicyRule{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"patch"}})
	e.CreateGate(t, verifyGate(a.ns, v1alpha1.GateVerifyPolicy{Required: 1, AllowedAgents: []string{agent},
		Run: &v1alpha1.GateVerifyRun{Template: agent, Count: 1, Namespace: a.ns}}))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "uat", "Verified", promoteTimeout)
	framework.Eventually(t, agentRunTimeout, "the AgentRun is refused", func(ctx context.Context) (bool, string) {
		runs, err := agentRuns(ctx, e, a.ns, bundle)
		if err != nil || len(runs) != 1 {
			return false, fmt.Sprintf("%d AgentRuns (%v)", len(runs), err)
		}
		r := runs[0]
		return r.Status.Phase == v1alpha1.AgentRunFailed &&
			meta.IsStatusConditionFalse(r.Status.Conditions, v1alpha1.AgentRunConditionBounded) &&
			strings.Contains(r.Status.Message, "may patch deployments in "+a.ns), r.Status.Phase + ": " + r.Status.Message
	})
	runs, err := agentRuns(ctx, e, a.ns, bundle)
	require.NoError(t, err)
	var job batchv1.Job
	err = e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: runs[0].Name}, &job)
	assert.True(t, apierrors.IsNotFound(err), "no Job for an agent that is not read-only: %v", err)
	e.WaitGateReady(t, a.ns, bundle, "prod", "explore-qa", false, "waiting for verdicts: 0 of 1", gateTimeout)
	a.noStep(t, bundle, "prod", 15*time.Second)
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
