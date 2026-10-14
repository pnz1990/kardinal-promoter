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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentrun"
)

var runNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

const qaUser = "system:serviceaccount:payments:qa"

// fakeAuthz allows exactly the listed "verb resource namespace" triples.
type fakeAuthz map[string]bool

func (f fakeAuthz) Allowed(_ context.Context, u actor.User, verb, _, resource, sub, ns string) (bool, error) {
	if sub != "" {
		resource += "/" + sub
	}
	return f[u.Username+" "+verb+" "+resource+" "+ns], nil
}

func readOnlyAgent() fakeAuthz {
	return fakeAuthz{qaUser + " create verdicts payments": true}
}

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
	index    *v1alpha1.AgentIndex
	authz    fakeAuthz
	extra    []client.Object
	notKro   bool
	recorded string
}

func newFixture() *fixture {
	bundle := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "payments", UID: "uid-1"},
		Spec:   v1alpha1.BundleSpec{Pipeline: "app", Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/x/app", Tag: "v1"}}},
		Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
	return &fixture{
		bundle: bundle,
		tmpl: &v1alpha1.StepTemplate{ObjectMeta: metav1.ObjectMeta{Name: "qa"},
			Spec: v1alpha1.StepTemplateSpec{Image: "ghcr.io/x/qa@sha256:abc", ServiceAccountName: "qa", Timeout: "10m",
				Egress: []v1alpha1.StepTemplateEgress{{CIDR: "10.20.0.0/16", Ports: []int32{443}}},
				Env:    []v1alpha1.StepTemplateEnv{{Name: "MODEL", Value: "m"}}}},
		index: &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal"},
			Status: v1alpha1.AgentIndexStatus{Users: map[string]v1alpha1.AgentIndexEntry{
				qaUser: {Agent: "qa", Sponsor: "bob", Namespaces: []string{"payments"}, Pipelines: []string{"*"}, Environments: []string{"*"}},
			}}},
		authz: readOnlyAgent(),
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
	objs := append([]client.Object{run, f.bundle, f.tmpl, f.index}, f.extra...)
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
		}}).Build()
	r := &agentrun.Reconciler{Client: c, Agents: &actor.Source{Reader: c, Name: "kardinal"}, Authorizer: f.authz,
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
	got := reconcileUntil(t, r, c, req, 3)
	assert.Equal(t, v1alpha1.AgentRunRunning, got.Status.Phase, got.Status.Message)
	assert.Equal(t, "qa", got.Status.Agent)
	assert.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, v1alpha1.AgentRunConditionBounded))
	assert.True(t, runNow.Add(10*time.Minute).Equal(got.Status.Deadline.Time))

	var job batchv1.Job
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: got.Name, Namespace: "payments"}, &job))
	assert.Equal(t, "run-uid", string(metav1.GetControllerOf(&job).UID))
	assert.Equal(t, int64(600), *job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, int32(0), *job.Spec.BackoffLimit)
	pod := job.Spec.Template.Spec
	assert.Equal(t, "qa", pod.ServiceAccountName)
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
	assert.Equal(t, qaUser, spec["by"])
	assert.Equal(t, "qa", spec["actor"].(map[string]interface{})["agent"])
	assert.Equal(t, "bob", spec["actor"].(map[string]interface{})["sponsor"])

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
}

// TestAgentRun_Refused: every bounds check refuses the run, Failed with
// Bounded=False and the reason, and creates no Job.
func TestAgentRun_Refused(t *testing.T) {
	cases := []struct {
		name   string
		modify func(f *fixture)
		want   string
	}{
		{"not an agent", func(f *fixture) { f.index.Status.Users = nil }, "is not a registered agent"},
		{"blocked agent", func(f *fixture) {
			e := f.index.Status.Users[qaUser]
			e.Blocked = "suspended"
			f.index.Status.Users[qaUser] = e
		}, "suspended"},
		{"out of scope", func(f *fixture) {
			e := f.index.Status.Users[qaUser]
			e.Pipelines = []string{"other"}
			f.index.Status.Users[qaUser] = e
		}, "may not act on pipeline app"},
		{"may patch deployments", func(f *fixture) { f.authz[qaUser+" patch deployments uat"] = true }, "may patch deployments in uat"},
		{"may read secrets", func(f *fixture) { f.authz[qaUser+" get secrets payments"] = true }, "may get secrets in payments"},
		{"may create bundles", func(f *fixture) { f.authz[qaUser+" create bundles payments"] = true }, "may create bundles"},
		{"may not write verdicts", func(f *fixture) { f.authz = fakeAuthz{} }, "may not create verdicts"},
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
	got := reconcileUntil(t, r, c, req, 3)
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

// TestAgentRun_Deadline: a Job past its deadline is deleted and the run
// fails; a deleted Job fails the run, which is not run again.
func TestAgentRun_Deadline(t *testing.T) {
	f := newFixture()
	r, c, req := f.build(t)
	reconcileUntil(t, r, c, req, 3)
	r.NowFn = func() time.Time { return runNow.Add(11 * time.Minute) }
	got := reconcileUntil(t, r, c, req, 1)
	assert.Equal(t, v1alpha1.AgentRunFailed, got.Status.Phase)
	assert.Contains(t, got.Status.Message, "did not finish before the 10m0s timeout")

	f = newFixture()
	r, c, req = f.build(t)
	got = reconcileUntil(t, r, c, req, 3)
	require.NoError(t, c.Delete(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: got.Name, Namespace: "payments"}}))
	got = reconcileUntil(t, r, c, req, 1)
	assert.Equal(t, v1alpha1.AgentRunFailed, got.Status.Phase)
	assert.True(t, strings.Contains(got.Status.Message, "deleted before it finished"))
}

func ptr[T any](v T) *T { return &v }
