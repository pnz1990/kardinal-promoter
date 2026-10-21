// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentrun_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentrun"
)

var runNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func scheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

type fixture struct {
	run      *v1alpha1.AgentRun
	bundle   *v1alpha1.Bundle
	tmpl     *v1alpha1.StepTemplate
	agent    *v1alpha1.AgentIdentity
	extra    []client.Object
	notKro   bool
	recorded string
	// noReceipts: the controller may not list receipts (receipts.enabled
	// is false: no RBAC for them).
	noReceipts bool
}

func newFixture() *fixture {
	bundle := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "payments", UID: "uid-1"},
		Spec:   v1alpha1.BundleSpec{Pipeline: "app", Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/x/app", Tag: "v1"}}},
		Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
	return &fixture{
		bundle: bundle,
		tmpl: &v1alpha1.StepTemplate{ObjectMeta: metav1.ObjectMeta{Name: "qa"},
			Spec: v1alpha1.StepTemplateSpec{Image: "ghcr.io/x/qa@sha256:abc", Agent: "qa", Timeout: "10m",
				Egress: []v1alpha1.StepTemplateEgress{{CIDR: "10.20.0.0/16", Ports: []int32{443}}},
				Env:    []v1alpha1.StepTemplateEnv{{Name: "MODEL", Value: "m"}}}},
		agent: &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "qa"},
			Spec: v1alpha1.AgentIdentitySpec{Sponsor: v1alpha1.AgentSponsor{User: "bob"},
				Scope: v1alpha1.AgentScope{Namespaces: []string{"payments"}, Pipelines: []string{"*"}, Environments: []string{"*"}}},
			Status: v1alpha1.AgentIdentityStatus{Ready: true}},
		extra: []client.Object{&discoveryv1.EndpointSlice{
			ObjectMeta:  metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints:   []discoveryv1.Endpoint{{Addresses: []string{"172.18.0.2"}}},
			Ports:       []discoveryv1.EndpointPort{{Port: ptr(int32(6443))}},
		}},
	}
}

func (f *fixture) build(t *testing.T) (*agentrun.Reconciler, client.Client, ctrl.Request) {
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "app-app-v1-uat-explore-r1-a1", Namespace: "payments", UID: "run-uid",
			Labels: map[string]string{graph.LabelKRONodeID: "AgentRuns", graph.LabelBundleUID: "uid-1"}},
		Spec: v1alpha1.AgentRunSpec{PipelineName: "app", BundleName: "app-v1", BundleUID: "uid-1", Version: "v1",
			Environment: "uat", TargetNamespace: "uat", Check: "explore", Template: "qa", Run: 1, Attempt: 1,
			Recorded: f.recorded},
	}
	if f.notKro {
		delete(run.Labels, graph.LabelKRONodeID)
	}
	f.run = run
	objs := append([]client.Object{run, f.bundle, f.tmpl, f.agent}, f.extra...)
	// The API server sets a UID on create; the fake client does not.
	uids := 0
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).
		WithStatusSubresource(run, f.tmpl, &batchv1.Job{}).
		WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if obj.GetUID() == "" {
				uids++
				obj.SetUID(types.UID(fmt.Sprintf("uid-%d", uids)))
			}
			return cl.Create(ctx, obj, opts...)
		}, List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, receipts := list.(*v1alpha1.PromotionReceiptList); receipts && f.noReceipts {
				return apierrors.NewForbidden(v1alpha1.GroupVersion.WithResource("promotionreceipts").GroupResource(), "", fmt.Errorf("no RBAC"))
			}
			return cl.List(ctx, list, opts...)
		}}).Build()
	r := &agentrun.Reconciler{Client: c, WriterClusterRole: "kardinal-agentrun-writer", ReaderClusterRole: "kardinal-agentrun-reader",
		ControllerNamespace: "kardinal-system", NowFn: func() time.Time { return runNow }}
	return r, c, ctrl.Request{NamespacedName: types.NamespacedName{Name: run.Name, Namespace: run.Namespace}}
}

func reconcileUntil(t *testing.T, r *agentrun.Reconciler, c client.Client, req ctrl.Request, n int) v1alpha1.AgentRun {
	t.Helper()
	for range n {
		_, err := r.Reconcile(context.Background(), req)
		require.NoError(t, err)
	}
	var got v1alpha1.AgentRun
	require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
	return got
}

// TestAgentRun_RunsBounded: a bounded run creates its NetworkPolicy and a
// restricted Job with the run's context, owned by the AgentRun; reconciling
// again changes nothing (idempotent); the Job's completion is Succeeded.
func TestAgentRun_RunsBounded(t *testing.T) {
	f := newFixture()
	r, c, req := f.build(t)
	got := reconcileUntil(t, r, c, req, 4)
	assert.Equal(t, v1alpha1.AgentRunRunning, got.Status.Phase, got.Status.Message)
	assert.Equal(t, "qa", got.Status.Agent)
	assert.Equal(t, "bob", got.Status.Sponsor)
	sa := "kr-" + got.Name
	assert.Equal(t, sa, got.Status.ServiceAccount)
	assert.Contains(t, got.Finalizers, agentrun.FinalizerAccess)

	// The run's own identity, owned by the run, bound only to the chart's
	// AgentRun roles: the writer in the Pipeline's namespace, the reader in
	// the namespace under test (labelled, as the run cannot own it).
	var account corev1.ServiceAccount
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "payments", Name: sa}, &account))
	assert.Equal(t, "run-uid", string(metav1.GetControllerOf(&account).UID))
	var writer, reader rbacv1.RoleBinding
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "payments", Name: sa}, &writer))
	assert.Equal(t, "kardinal-agentrun-writer", writer.RoleRef.Name)
	assert.Equal(t, []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa, Namespace: "payments"}}, writer.Subjects)
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "uat", Name: sa}, &reader))
	assert.Equal(t, "kardinal-agentrun-reader", reader.RoleRef.Name)
	assert.Equal(t, "run-uid", reader.Labels[agentrun.LabelAgentRunUID])
	assert.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, v1alpha1.AgentRunConditionBounded))
	assert.True(t, runNow.Add(10*time.Minute).Equal(got.Status.Deadline.Time))

	var job batchv1.Job
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: got.Name, Namespace: "payments"}, &job))
	assert.Equal(t, "run-uid", string(metav1.GetControllerOf(&job).UID))
	assert.Equal(t, int64(600), *job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, int32(0), *job.Spec.BackoffLimit)
	pod := job.Spec.Template.Spec
	assert.Equal(t, sa, pod.ServiceAccountName, "never one of the agent's own identities")
	assert.False(t, *pod.AutomountServiceAccountToken, "the token is projected, short-lived")
	assert.True(t, *pod.SecurityContext.RunAsNonRoot)
	ctr := pod.Containers[0]
	assert.False(t, *ctr.SecurityContext.AllowPrivilegeEscalation)
	assert.True(t, *ctr.SecurityContext.ReadOnlyRootFilesystem)
	assert.Equal(t, []corev1.Capability{"ALL"}, ctr.SecurityContext.Capabilities.Drop)
	env := map[string]string{}
	for _, e := range ctr.Env {
		env[e.Name] = e.Value
	}
	assert.Equal(t, "uat", env["KARDINAL_TARGET_NAMESPACE"])
	assert.Equal(t, "explore", env["KARDINAL_CHECK"])
	assert.Equal(t, "m", env["MODEL"])
	assert.Equal(t, agentrun.ContextPath, env["KARDINAL_CONTEXT"])
	for _, v := range pod.Volumes {
		assert.Nil(t, v.Secret, "no Secret volumes: no git credentials")
	}
	var rc map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(job.Spec.Template.Annotations[agentrun.AnnotationContext]), &rc))
	verdict := rc["verdict"].(map[string]interface{})
	md := verdict["metadata"].(map[string]interface{})
	assert.Equal(t, got.Name, md["name"], "the Verdict is named after the run")
	spec := verdict["spec"].(map[string]interface{})
	assert.Equal(t, "system:serviceaccount:payments:"+sa, spec["by"])
	assert.NotContains(t, spec, "actor", "the Graph names the run's agent from the AgentRun")

	var np networkingv1.NetworkPolicy
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: got.Name, Namespace: "payments"}, &np))
	assert.Empty(t, np.Spec.Ingress)
	var cidrs []string
	for _, e := range np.Spec.Egress {
		for _, to := range e.To {
			if to.IPBlock != nil {
				cidrs = append(cidrs, to.IPBlock.CIDR)
			}
		}
	}
	assert.ElementsMatch(t, []string{"172.18.0.2/32", "10.20.0.0/16"}, cidrs, "the API server and the model endpoint")

	// Idempotent: another reconcile writes nothing.
	again := reconcileUntil(t, r, c, req, 1)
	assert.Equal(t, got.ResourceVersion, again.ResourceVersion)

	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	require.NoError(t, c.Status().Update(context.Background(), &job))
	got = reconcileUntil(t, r, c, req, 1)
	assert.Equal(t, v1alpha1.AgentRunSucceeded, got.Status.Phase)
	got = reconcileUntil(t, r, c, req, 1)
	assert.Equal(t, v1alpha1.AgentRunSucceeded, got.Status.Phase, "terminal")
	// A finished run keeps no access.
	assert.NotContains(t, got.Finalizers, agentrun.FinalizerAccess)
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), types.NamespacedName{Namespace: "payments", Name: sa}, &account)))
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), types.NamespacedName{Namespace: "uat", Name: sa}, &reader)))
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), types.NamespacedName{Namespace: "payments", Name: sa}, &writer)))
}

// TestAgentRun_Refused: every bounds check refuses the run, Failed with
// Bounded=False and the reason, and creates no Job.
func TestAgentRun_Refused(t *testing.T) {
	cases := []struct {
		name   string
		modify func(f *fixture)
		want   string
	}{
		{"not registered", func(f *fixture) { f.agent.Name = "other" }, "agent qa (StepTemplate qa) is not registered"},
		{"suspended", func(f *fixture) { f.agent.Spec.Suspended = true }, "agent qa is suspended"},
		{"not ready", func(f *fixture) { f.agent.Status = v1alpha1.AgentIdentityStatus{Reason: "SubjectConflict"} }, "is not ready: SubjectConflict"},
		{"namespace out of scope", func(f *fixture) { f.agent.Spec.Scope.Namespaces = []string{"billing"} }, "may not act in namespace payments"},
		{"pipeline out of scope", func(f *fixture) { f.agent.Spec.Scope.Pipelines = []string{"other"} }, "may not act on pipeline app"},
		{"environment out of scope", func(f *fixture) { f.agent.Spec.Scope.Environments = []string{"test"} }, "may not act in environment uat"},
		{"no template", func(f *fixture) { f.tmpl.Name = "other" }, "StepTemplate qa does not exist"},
		{"unlabelled secret", func(f *fixture) {
			f.tmpl.Spec.SecretEnv = []v1alpha1.StepTemplateSecretEnv{{Name: "KEY", Secret: "git", Key: "token"}}
			f.extra = append(f.extra, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "git", Namespace: "payments"}})
		}, "is not labelled kardinal.io/step-template=qa"},
		{"reserved env", func(f *fixture) { f.tmpl.Spec.Env = []v1alpha1.StepTemplateEnv{{Name: "KARDINAL_CHECK", Value: "x"}} },
			"reserved"},
		{"controller namespace", func(*fixture) {}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			tc.modify(f)
			if tc.name == "controller namespace" {
				// The run tests the controller's namespace.
				r, c, req := f.build(t)
				var run v1alpha1.AgentRun
				require.NoError(t, c.Get(context.Background(), req.NamespacedName, &run))
				run.Spec.TargetNamespace = "kardinal-system"
				require.NoError(t, c.Update(context.Background(), &run))
				got := reconcileUntil(t, r, c, req, 2)
				assert.Contains(t, got.Status.Message, "controller's namespace")
				return
			}
			if tc.want == "reserved" {
				tc.want = "names starting with KARDINAL_"
			}
			r, c, req := f.build(t)
			got := reconcileUntil(t, r, c, req, 2)
			assert.Equal(t, v1alpha1.AgentRunFailed, got.Status.Phase)
			assert.Contains(t, got.Status.Message, tc.want)
			assert.True(t, meta.IsStatusConditionFalse(got.Status.Conditions, v1alpha1.AgentRunConditionBounded))
			var jobs batchv1.JobList
			require.NoError(t, c.List(context.Background(), &jobs))
			assert.Empty(t, jobs.Items)
		})
	}
}

// TestAgentRun_LabelledSecret: a Secret labelled for the template is read
// into the Pod's environment.
func TestAgentRun_LabelledSecret(t *testing.T) {
	f := newFixture()
	f.tmpl.Spec.SecretEnv = []v1alpha1.StepTemplateSecretEnv{{Name: "KEY", Secret: "model", Key: "token"}}
	f.extra = append(f.extra, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "payments",
		Labels: map[string]string{agentrun.LabelStepTemplateSecret: "qa"}}})
	r, c, req := f.build(t)
	got := reconcileUntil(t, r, c, req, 4)
	require.Equal(t, v1alpha1.AgentRunRunning, got.Status.Phase, got.Status.Message)
	var job batchv1.Job
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: got.Name, Namespace: "payments"}, &job))
	found := false
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "KEY" {
			found = e.ValueFrom.SecretKeyRef.Name == "model"
		}
	}
	assert.True(t, found)
}

// TestAgentRun_NotRunAgain: a run whose Verdict exists (spec.recorded, from
// the Graph), or whose Bundle is Superseded, does not run; one that kro did
// not apply is ignored.
func TestAgentRun_NotRunAgain(t *testing.T) {
	f := newFixture()
	f.recorded = "pass"
	r, c, req := f.build(t)
	got := reconcileUntil(t, r, c, req, 2)
	assert.Equal(t, v1alpha1.AgentRunSucceeded, got.Status.Phase)
	assert.Contains(t, got.Status.Message, "verdict already recorded (pass)")

	f = newFixture()
	f.bundle.Status.Phase = "Superseded"
	r, c, req = f.build(t)
	got = reconcileUntil(t, r, c, req, 2)
	assert.Equal(t, v1alpha1.AgentRunSkipped, got.Status.Phase)

	f = newFixture()
	f.notKro = true
	r, c, req = f.build(t)
	got = reconcileUntil(t, r, c, req, 2)
	assert.Empty(t, got.Status.Phase, "not applied by kro: never run")
}

// TestAgentRun_RecreatedNamesItsAgent (#2345): an AgentRun kro creates again
// after its Verdict was recorded (spec.recorded) does not run. It names the
// agent and sponsor its StepTemplate names, with the run's ServiceAccount,
// only when a record kardinal wrote of the run's Verdict (the
// VerdictRecorded AuditEvent, a receipt) names the same agent and sponsor,
// and every such record does: the Graph counts its Verdict only when it
// names an allowed agent, and the evidence pack traces the Verdict through
// it. When the template's agent or the sponsor changed since the run, no
// record is left, or the agent may not verify the run now, it names none,
// so the Verdict does not count, and says why. A second reconcile writes
// nothing.
func TestAgentRun_RecreatedNamesItsAgent(t *testing.T) {
	const name = "app-app-v1-uat-explore-r1-a1"
	principal := "system:serviceaccount:payments:kr-" + name
	event := func(n, agent, sponsor, who string) client.Object {
		return &v1alpha1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: "payments", Labels: map[string]string{
			"kardinal.io/bundle": "app-v1", "kardinal.io/action": "VerdictRecorded"}},
			Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.NewTime(runNow), BundleName: "app-v1", PipelineName: "app",
				Environment: "prod", Action: "VerdictRecorded", Outcome: "Success",
				Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: who, Agent: agent, Sponsor: sponsor}}}
	}
	recorded := func(agent, sponsor string) client.Object {
		return event("prod-explore-qa-verdict-1", agent, sponsor, principal)
	}
	signed := func(verifier string) client.Object {
		st, err := json.Marshal(receipt.Statement{Type: receipt.StatementType, PredicateType: receipt.PredicateVSA,
			Predicate: receipt.Predicate{Kardinal: receipt.Record{Gates: []receipt.Gate{{Template: "payments/explore-qa",
				Verdicts: []receipt.Verdict{{Name: name, Verifier: verifier, Result: "pass", Counted: true}}}}}}})
		require.NoError(t, err)
		return &v1alpha1.PromotionReceipt{ObjectMeta: metav1.ObjectMeta{Name: "app-v1-prod", Namespace: "payments",
			Labels: map[string]string{"kardinal.io/bundle": "app-v1"}}, Spec: v1alpha1.PromotionReceiptSpec{Statement: string(st)}}
	}
	other := func(f *fixture) { // the template now names agent qa2, registered and ready, of the same sponsor
		f.tmpl.Spec.Agent = "qa2"
		qa2 := f.agent.DeepCopy()
		qa2.Name = "qa2"
		f.extra = append(f.extra, qa2)
	}
	const changed = "it names no agent: agent changed since the run, or unknown"
	cases := []struct {
		name                string
		records             []client.Object
		change              func(f *fixture)
		agent, sponsor, why string
	}{
		{name: "its AuditEvent names its agent", records: []client.Object{recorded("qa", "bob")}, agent: "qa", sponsor: "bob"},
		{name: "its receipt names its agent", records: []client.Object{signed("agent qa for bob")}, agent: "qa", sponsor: "bob"},
		{name: "both name its agent", records: []client.Object{recorded("qa", "bob"), signed("agent qa for bob")}, agent: "qa", sponsor: "bob"},
		{name: "its AuditEvent, with receipts off", records: []client.Object{recorded("qa", "bob")},
			change: func(f *fixture) { f.noReceipts = true }, agent: "qa", sponsor: "bob"},
		{name: "the template's agent changed since the run", records: []client.Object{recorded("qa", "bob")}, change: other,
			why: changed + ": no record of its Verdict names agent qa2 for bob (1 VerdictRecorded AuditEvents, 0 receipts name the run)"},
		{name: "the agent's sponsor changed since the run", records: []client.Object{recorded("qa", "bob")},
			change: func(f *fixture) { f.agent.Spec.Sponsor.User = "carol" }, why: changed},
		{name: "a receipt names another agent", records: []client.Object{recorded("qa", "bob"), signed("agent qa2 for carol")},
			why: changed + ": no record of its Verdict names agent qa for bob (1 VerdictRecorded AuditEvents, 1 receipts name the run)"},
		{name: "no record of the Verdict", why: changed},
		{name: "only another run's AuditEvent", records: []client.Object{event("other", "qa", "bob", principal+"-x")}, why: changed},
		// Labels can be changed after an AuditEvent is written; its spec
		// cannot.
		{name: "another Bundle's AuditEvent, labelled as this one's", change: func(f *fixture) {
			ev := event("other-bundle", "qa", "bob", principal).(*v1alpha1.AuditEvent)
			ev.Spec.BundleName = "app-v0"
			f.extra = append(f.extra, ev)
		}, why: changed},
		{name: "a VerdictRevoked AuditEvent, labelled VerdictRecorded", change: func(f *fixture) {
			ev := event("revoked", "qa", "bob", principal).(*v1alpha1.AuditEvent)
			ev.Spec.Action = "VerdictRevoked"
			f.extra = append(f.extra, ev)
		}, why: changed},
		{name: "a suspended agent", records: []client.Object{recorded("qa", "bob")},
			change: func(f *fixture) { f.agent.Spec.Suspended = true }, why: "it names no agent: agent qa is suspended"},
		{name: "an agent out of scope", records: []client.Object{recorded("qa", "bob")},
			change: func(f *fixture) { f.agent.Spec.Scope.Namespaces = []string{"other"} },
			why:    "it names no agent: agent qa may not act in namespace payments"},
		{name: "no AgentIdentity", records: []client.Object{recorded("qa", "bob")}, change: func(f *fixture) { f.agent.Name = "someone-else" },
			why: "it names no agent: agent qa (StepTemplate qa) is not registered"},
		{name: "no StepTemplate", records: []client.Object{recorded("qa", "bob")}, change: func(f *fixture) { f.tmpl.Name = "other" },
			why: "it names no agent: StepTemplate qa does not exist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()
			f.recorded = "pass"
			f.extra = append(f.extra, tc.records...)
			if tc.change != nil {
				tc.change(f)
			}
			r, c, req := f.build(t)
			got := reconcileUntil(t, r, c, req, 1)
			assert.Equal(t, v1alpha1.AgentRunSucceeded, got.Status.Phase)
			assert.Contains(t, got.Status.Message, "verdict already recorded (pass); not run again")
			assert.Equal(t, tc.agent, got.Status.Agent)
			assert.Equal(t, tc.sponsor, got.Status.Sponsor)
			if tc.why == "" {
				assert.Equal(t, "kr-"+got.Name, got.Status.ServiceAccount, "the ServiceAccount that wrote the Verdict")
				assert.NotContains(t, got.Status.Message, "names no agent")
			} else {
				assert.Contains(t, got.Status.Message, tc.why)
				assert.Empty(t, got.Status.ServiceAccount)
			}
			assert.Empty(t, got.Status.JobName, "not run again: no Job")
			assert.Empty(t, got.Finalizers, "and no access to hold")
			var jobs batchv1.JobList
			require.NoError(t, c.List(context.Background(), &jobs))
			assert.Empty(t, jobs.Items)
			var sas corev1.ServiceAccountList
			require.NoError(t, c.List(context.Background(), &sas))
			assert.Empty(t, sas.Items, "no ServiceAccount is created for it")

			again := reconcileUntil(t, r, c, req, 1)
			assert.Equal(t, got.ResourceVersion, again.ResourceVersion, "idempotent")
		})
	}
}

// TestAgentRun_Deadline: a Job past its deadline is deleted and the run
// fails; a deleted Job fails the run, which is not run again.
func TestAgentRun_Deadline(t *testing.T) {
	f := newFixture()
	r, c, req := f.build(t)
	reconcileUntil(t, r, c, req, 4)
	r.NowFn = func() time.Time { return runNow.Add(11 * time.Minute) }
	got := reconcileUntil(t, r, c, req, 1)
	assert.Equal(t, v1alpha1.AgentRunFailed, got.Status.Phase)
	assert.Contains(t, got.Status.Message, "did not finish before the 10m0s timeout")

	f = newFixture()
	r, c, req = f.build(t)
	got = reconcileUntil(t, r, c, req, 4)
	require.NoError(t, c.Delete(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: got.Name, Namespace: "payments"}}))
	got = reconcileUntil(t, r, c, req, 1)
	assert.Equal(t, v1alpha1.AgentRunFailed, got.Status.Phase)
	assert.True(t, strings.Contains(got.Status.Message, "deleted before it finished"))
}

func ptr[T any](v T) *T { return &v }

// TestAgentRun_DeletedMidRun: an AgentRun deleted while its Job runs gives
// up its access (ServiceAccount, RoleBindings) before it goes.
func TestAgentRun_DeletedMidRun(t *testing.T) {
	f := newFixture()
	r, c, req := f.build(t)
	got := reconcileUntil(t, r, c, req, 4)
	require.Equal(t, v1alpha1.AgentRunRunning, got.Status.Phase)
	require.NoError(t, c.Delete(context.Background(), &got))
	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var reader rbacv1.RoleBinding
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), types.NamespacedName{Namespace: "uat", Name: "kr-" + got.Name}, &reader)))
	assert.True(t, apierrors.IsNotFound(c.Get(context.Background(), req.NamespacedName, &got)), "the finalizer is gone, so is the run")
}
