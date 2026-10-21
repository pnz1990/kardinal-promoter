// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// policyNamespace is a Namespace, labelled kardinal.io/policy-shareable with
// shareable when it is not "".
func policyNamespace(name, shareable string) *corev1.Namespace {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if shareable != "" {
		ns.Labels = map[string]string{kardinalv1alpha1.LabelPolicyShareable: shareable}
	}
	return ns
}

// policyNamespaceEvents drains rec and counts its PolicyNamespaceNotShared
// Warning Events.
func policyNamespaceEvents(rec *events.FakeRecorder) int {
	n := 0
	for len(rec.Events) > 0 {
		if ev := <-rec.Events; strings.Contains(ev, "Warning PolicyNamespaceNotShared") {
			n++
		}
	}
	return n
}

// TestPipelineReconciler_PolicyNamespacesShared (#2286): a spec.policyNamespaces
// entry needs its namespace's consent. The Pipeline's own namespace, an org
// policy namespace (the default platform-policies, or --policy-namespaces),
// a namespace labelled kardinal.io/policy-shareable=true and one listed in
// --policy-shareable-namespaces need nothing. Any other namespace, one
// labelled otherwise ("True" too) and one that does not exist set
// PolicyNamespacesShared=False (NotShared) naming each (sorted, once, a
// missing one like the others), with one Warning Event, and the Pipeline is
// refused (#2286): Ready=False (PolicyNamespaceNotShared), and
// status.policyNamespaces names them for this generation, which every case
// writes. A namespace that needed a read is read again
// within 5 minutes, agreed or not; with none to read there is no recheck.
//
// Covers GATE-POLNS-02.
func TestPipelineReconciler_PolicyNamespacesShared(t *testing.T) {
	objs := []client.Object{policyNamespace("shared", "true"), policyNamespace("team-b", ""),
		policyNamespace("labelled-no", "false"), policyNamespace("labelled-cap", "True"), policyNamespace("listed", ""),
		policyNamespace("org-gates", "")}
	tests := []struct {
		name       string
		namespaces []string
		orgNS      []string
		want       string // the message's list; "" is no condition
		recheck    bool
	}{
		{name: "none"},
		{name: "own, default org namespace, listed", namespaces: []string{"default", "platform-policies", "listed"}},
		{name: "labelled", namespaces: []string{"shared"}, recheck: true},
		{name: "a custom org namespace", namespaces: []string{"org-gates"}, orgNS: []string{"org-gates"}},
		{name: "the default org namespace is not org with a custom list", namespaces: []string{"platform-policies"},
			orgNS: []string{"org-gates"}, want: "platform-policies", recheck: true},
		{name: "not shared, sorted, once, missing like the others",
			namespaces: []string{"team-b", "shared", "labelled-no", "team-b", "missing", "labelled-cap"},
			want:       "labelled-cap, labelled-no, missing, team-b", recheck: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := makePipelineWithEnvs("app", "default", "test", "prod")
			p.Spec.PolicyNamespaces = tt.namespaces
			c := newClientWithIndex(newPipelineScheme(), append([]client.Object{p}, objs...)...)
			rec := events.NewFakeRecorder(20)
			r := &pipeline.Reconciler{Client: c, Recorder: rec, PolicyNamespaces: tt.orgNS,
				PolicyShareableNamespaces: []string{"listed"}}
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)}
			var res ctrl.Result
			for range 2 {
				var err error
				res, err = r.Reconcile(context.Background(), req)
				require.NoError(t, err)
			}
			if tt.recheck {
				assert.Positive(t, res.RequeueAfter, "read again: Namespaces are not watched")
				assert.LessOrEqual(t, res.RequeueAfter, 5*time.Minute)
			} else {
				assert.Zero(t, res.RequeueAfter, "nothing to read again")
			}
			var got kardinalv1alpha1.Pipeline
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			require.NotNil(t, ready)
			// status.policyNamespaces, what the Bundle reconciler reads: for
			// this generation, naming the namespaces that have not agreed.
			require.NotNil(t, got.Status.PolicyNamespaces)
			assert.Equal(t, got.Generation, got.Status.PolicyNamespaces.ObservedGeneration)
			if tt.want == "" {
				assert.Empty(t, got.Status.PolicyNamespaces.NotShared)
				assert.Equal(t, metav1.ConditionTrue, ready.Status, ready.Message)
			} else {
				assert.Equal(t, strings.Split(tt.want, ", "), got.Status.PolicyNamespaces.NotShared)
				// Refused (#2286 v0.14): Ready=False, new versions wait.
				assert.Equal(t, metav1.ConditionFalse, ready.Status)
				assert.Equal(t, pipeline.ReasonPolicyNamespaceNotShared, ready.Reason)
				assert.Contains(t, ready.Message, strings.Split(tt.want, ", ")[0])
				assert.Contains(t, ready.Message, "new versions wait")
			}
			cond := meta.FindStatusCondition(got.Status.Conditions, "PolicyNamespacesShared")
			// What kardinal doctor reads from the condition (#2286).
			flagged, names := pipeline.NotSharedPolicyNamespaces(&got)
			if tt.want == "" {
				assert.Nil(t, cond)
				assert.Zero(t, policyNamespaceEvents(rec))
				assert.False(t, flagged)
				assert.Empty(t, names)
				return
			}
			assert.True(t, flagged)
			assert.Equal(t, strings.Split(tt.want, ", "), names)
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, "NotShared", cond.Reason)
			assert.Equal(t, got.Generation, cond.ObservedGeneration)
			assert.Contains(t, cond.Message, "have not agreed to share their PolicyGates: "+tt.want+". ")
			assert.NotContains(t, cond.Message, "not found", "the message does not tell which namespaces exist")
			assert.Contains(t, cond.Message, "kardinal.io/policy-shareable=true")
			assert.Contains(t, cond.Message, "--policy-shareable-namespaces")
			assert.Contains(t, cond.Message, "They are refused")
			assert.Contains(t, cond.Message, "keeps the gates it started with")
			assert.Equal(t, 1, policyNamespaceEvents(rec), "one Warning Event over two reconciles")
		})
	}
}

// policyNamespaceFixture is a Pipeline naming namespaces, reconciled with a
// Reader whose Namespace reads fail while failing names them, counting the
// status writes.
type policyNamespaceFixture struct {
	t       *testing.T
	c       client.Client
	r       *pipeline.Reconciler
	rec     *events.FakeRecorder
	req     ctrl.Request
	failing map[string]error
	writes  int
}

func newPolicyNamespaceFixture(t *testing.T, namespaces []string, objs ...client.Object) *policyNamespaceFixture {
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.PolicyNamespaces = namespaces
	f := &policyNamespaceFixture{t: t, rec: events.NewFakeRecorder(50), failing: map[string]error{},
		req: ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)}}
	base := newClientWithIndex(newPipelineScheme(), append([]client.Object{p}, objs...)...)
	f.c = interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Namespace); ok && f.failing[key.Name] != nil {
				return f.failing[key.Name]
			}
			return cl.Get(ctx, key, obj, opts...)
		},
		SubResourcePatch: func(ctx context.Context, cl client.Client, sub string, obj client.Object, patch client.Patch,
			opts ...client.SubResourcePatchOption) error {
			f.writes++
			return cl.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	})
	f.r = &pipeline.Reconciler{Client: f.c, Recorder: f.rec}
	return f
}

// reconcile reconciles and returns the condition and the requeue.
func (f *policyNamespaceFixture) reconcile() (*metav1.Condition, time.Duration) {
	f.t.Helper()
	res, err := f.r.Reconcile(context.Background(), f.req)
	require.NoError(f.t, err)
	var got kardinalv1alpha1.Pipeline
	require.NoError(f.t, f.c.Get(context.Background(), f.req.NamespacedName, &got))
	return meta.FindStatusCondition(got.Status.Conditions, "PolicyNamespacesShared"), res.RequeueAfter
}

// status is the Pipeline's status.policyNamespaces as stored.
func (f *policyNamespaceFixture) status() *kardinalv1alpha1.PipelinePolicyNamespaces {
	f.t.Helper()
	var got kardinalv1alpha1.Pipeline
	require.NoError(f.t, f.c.Get(context.Background(), f.req.NamespacedName, &got))
	return got.Status.PolicyNamespaces
}

// setLabel sets or (shareable "") removes the consent label of namespace name.
func (f *policyNamespaceFixture) setLabel(name, shareable string) {
	f.t.Helper()
	var ns corev1.Namespace
	require.NoError(f.t, f.c.Get(context.Background(), client.ObjectKey{Name: name}, &ns))
	ns.Labels = nil
	if shareable != "" {
		ns.Labels = map[string]string{kardinalv1alpha1.LabelPolicyShareable: shareable}
	}
	require.NoError(f.t, f.c.Update(context.Background(), &ns))
}

// TestPipelineReconciler_PolicyNamespacesLabelRemoved: the recheck reads the
// Namespace again. A labelled namespace leaves no condition and a recheck
// within 5 minutes; once the label is removed, that recheck sets the
// condition and warns.
//
// Covers GATE-POLNS-02.
func TestPipelineReconciler_PolicyNamespacesLabelRemoved(t *testing.T) {
	f := newPolicyNamespaceFixture(t, []string{"team-b"}, policyNamespace("team-b", "true"))
	cond, after := f.reconcile()
	assert.Nil(t, cond)
	assert.Positive(t, after)
	assert.LessOrEqual(t, after, 5*time.Minute, "the label is read again within one recheck")
	f.setLabel("team-b", "")
	cond, _ = f.reconcile() // the recheck
	require.NotNil(t, cond)
	assert.Equal(t, "NotShared", cond.Reason)
	assert.Contains(t, cond.Message, "PolicyGates: team-b. ")
	assert.Equal(t, 1, policyNamespaceEvents(f.rec))
}

// TestPipelineReconciler_PolicyNamespacesUnreadable: a failed read (an API
// error other than NotFound: a timeout, Forbidden) keeps the namespace's last
// result. A labelled namespace whose reads fail every other time leaves no
// condition, writes no status after the first reconcile and emits no Event;
// a namespace that was not shared stays named, with reason
// PolicyNamespaceUnreadable and the same message, and emits no new Event
// when its read fails or recovers; a namespace never read before is named
// only once a read shows it is not labelled. A failed read is retried within
// a minute.
//
// Covers GATE-POLNS-02.
func TestPipelineReconciler_PolicyNamespacesUnreadable(t *testing.T) {
	timeout := apierrors.NewTimeoutError("the API server is slow", 1)
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "team-c", nil)

	t.Run("a labelled namespace does not flap", func(t *testing.T) {
		f := newPolicyNamespaceFixture(t, []string{"team-b"}, policyNamespace("team-b", "true"))
		cond, _ := f.reconcile()
		require.Nil(t, cond)
		f.writes = 0
		for i := range 6 {
			if i%2 == 0 {
				f.failing["team-b"] = timeout
			} else {
				delete(f.failing, "team-b")
			}
			cond, after := f.reconcile()
			assert.Nil(t, cond, "reconcile %d", i)
			if i%2 == 0 {
				assert.Equal(t, time.Minute, after, "a failed read is retried within a minute")
			}
		}
		assert.Zero(t, f.writes, "no status write")
		assert.Zero(t, policyNamespaceEvents(f.rec), "no Warning")
	})

	t.Run("a namespace not shared stays named", func(t *testing.T) {
		f := newPolicyNamespaceFixture(t, []string{"team-b"}, policyNamespace("team-b", ""))
		first, _ := f.reconcile()
		require.NotNil(t, first)
		require.Equal(t, 1, policyNamespaceEvents(f.rec))
		for _, err := range []error{timeout, forbidden} {
			f.failing["team-b"] = err
			cond, _ := f.reconcile()
			require.NotNil(t, cond, "kept on %v", err)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, "PolicyNamespaceUnreadable", cond.Reason)
			assert.Equal(t, first.Message, cond.Message)
		}
		delete(f.failing, "team-b")
		cond, _ := f.reconcile()
		require.NotNil(t, cond)
		assert.Equal(t, "NotShared", cond.Reason)
		assert.Equal(t, first.Message, cond.Message)
		assert.Zero(t, policyNamespaceEvents(f.rec), "the failed reads and the recovery warn nothing new")
	})

	t.Run("a namespace never read is named once read", func(t *testing.T) {
		f := newPolicyNamespaceFixture(t, []string{"team-b", "team-c"}, policyNamespace("team-b", ""), policyNamespace("team-c", ""))
		f.failing["team-c"] = forbidden
		cond, _ := f.reconcile()
		require.NotNil(t, cond)
		assert.Equal(t, "PolicyNamespaceUnreadable", cond.Reason)
		assert.Contains(t, cond.Message, "PolicyGates: team-b. ", "team-c was never read: its last result is not known")
		assert.Equal(t, 1, policyNamespaceEvents(f.rec))
		// Not checked for this generation (#2286): no new version is
		// translated until team-c is read.
		assert.Nil(t, f.status(), "no status.policyNamespaces for a generation not fully read")
		delete(f.failing, "team-c")
		cond, _ = f.reconcile()
		require.NotNil(t, cond)
		assert.Equal(t, "NotShared", cond.Reason)
		assert.Contains(t, cond.Message, "PolicyGates: team-b, team-c. ")
		assert.Equal(t, 1, policyNamespaceEvents(f.rec), "team-c, now read, is news")
		require.NotNil(t, f.status())
		assert.Equal(t, []string{"team-b", "team-c"}, f.status().NotShared)
	})

	t.Run("a kept result stays current; after a spec change it is not", func(t *testing.T) {
		f := newPolicyNamespaceFixture(t, []string{"team-b", "team-c"}, policyNamespace("team-b", "true"), policyNamespace("team-c", ""))
		_, _ = f.reconcile()
		first := f.status()
		require.NotNil(t, first)
		assert.Equal(t, []string{"team-c"}, first.NotShared)
		// The same generation, a failed read: the last result is kept, for
		// this generation.
		f.failing["team-b"], f.failing["team-c"] = timeout, timeout
		_, _ = f.reconcile()
		assert.Equal(t, first, f.status(), "kept: team-b agreed, team-c did not")
		// A spec change while the reads fail: the status stays for the older
		// generation, so the Bundle reconciler waits for a read.
		var p kardinalv1alpha1.Pipeline
		require.NoError(t, f.c.Get(context.Background(), f.req.NamespacedName, &p))
		p.Spec.HistoryLimit = 7
		p.Generation++ // the API server's; the fake client does not bump it
		require.NoError(t, f.c.Update(context.Background(), &p))
		_, after := f.reconcile()
		assert.Equal(t, time.Minute, after)
		require.NoError(t, f.c.Get(context.Background(), f.req.NamespacedName, &p))
		require.NotNil(t, p.Status.PolicyNamespaces)
		assert.Less(t, p.Status.PolicyNamespaces.ObservedGeneration, p.Generation, "not checked for the new generation")
		delete(f.failing, "team-b")
		delete(f.failing, "team-c")
		_, _ = f.reconcile()
		require.NoError(t, f.c.Get(context.Background(), f.req.NamespacedName, &p))
		assert.Equal(t, p.Generation, p.Status.PolicyNamespaces.ObservedGeneration)
		assert.Equal(t, []string{"team-c"}, p.Status.PolicyNamespaces.NotShared)
	})
}

// TestPipelineReconciler_PolicyNamespacesSharedChanges: the condition
// follows the namespaces. Another namespace not shared changes the message
// and emits one more Event; labelling every namespace removes the condition
// with no Event.
//
// Covers GATE-POLNS-02.
func TestPipelineReconciler_PolicyNamespacesSharedChanges(t *testing.T) {
	ctx := context.Background()
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.PolicyNamespaces = []string{"team-b"}
	c := newClientWithIndex(newPipelineScheme(), p, policyNamespace("team-b", ""), policyNamespace("team-c", ""))
	rec := events.NewFakeRecorder(20)
	r := &pipeline.Reconciler{Client: c, Recorder: rec}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)}
	reconcile := func() *metav1.Condition {
		t.Helper()
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		var got kardinalv1alpha1.Pipeline
		require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
		return meta.FindStatusCondition(got.Status.Conditions, "PolicyNamespacesShared")
	}
	require.NotNil(t, reconcile())
	assert.Equal(t, 1, policyNamespaceEvents(rec))

	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	got.Spec.PolicyNamespaces = append(got.Spec.PolicyNamespaces, "team-c")
	require.NoError(t, c.Update(ctx, &got))
	cond := reconcile()
	require.NotNil(t, cond)
	assert.Contains(t, cond.Message, "team-b, team-c.")
	assert.Equal(t, 1, policyNamespaceEvents(rec), "the new message is reported once")

	for _, name := range []string{"team-b", "team-c"} {
		var ns corev1.Namespace
		require.NoError(t, c.Get(ctx, client.ObjectKey{Name: name}, &ns))
		ns.Labels = map[string]string{kardinalv1alpha1.LabelPolicyShareable: "true"}
		require.NoError(t, c.Update(ctx, &ns))
	}
	assert.Nil(t, reconcile(), "both namespaces agreed")
	assert.Zero(t, policyNamespaceEvents(rec))
}

// TestPipelineReconciler_PolicyNamespacesReader: the namespaces are read
// with Reader (the API server: the controller has no Namespace cache).
//
// Covers GATE-POLNS-02.
func TestPipelineReconciler_PolicyNamespacesReader(t *testing.T) {
	p := makePipelineWithEnvs("app", "default", "test", "prod")
	p.Spec.PolicyNamespaces = []string{"team-b"}
	c := newClientWithIndex(newPipelineScheme(), p)
	reader := fake.NewClientBuilder().WithScheme(newPipelineScheme()).WithObjects(policyNamespace("team-b", "true")).Build()
	r := &pipeline.Reconciler{Client: c, Reader: reader}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)})
	require.NoError(t, err)
	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(p), &got))
	assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, "PolicyNamespacesShared"),
		"team-b is labelled where Reader reads it")
}

// TestNotSharedPolicyNamespaces (#2286): what kardinal doctor reads, from
// status.policyNamespaces when the controller wrote it (v0.14), else from a
// v0.13 controller's PolicyNamespacesShared condition.
func TestNotSharedPolicyNamespaces(t *testing.T) {
	v013 := metav1.Condition{Type: "PolicyNamespacesShared", Status: metav1.ConditionFalse, Reason: "NotShared",
		Message: "spec.policyNamespaces names namespaces that have not agreed to share their PolicyGates: old-a. Label each kardinal.io/policy-shareable=true"}
	for name, tc := range map[string]struct {
		status     kardinalv1alpha1.PipelineStatus
		flagged    bool
		namespaces []string
	}{
		"status field":       {kardinalv1alpha1.PipelineStatus{PolicyNamespaces: &kardinalv1alpha1.PipelinePolicyNamespaces{NotShared: []string{"team-b"}}}, true, []string{"team-b"}},
		"the field wins":     {kardinalv1alpha1.PipelineStatus{Conditions: []metav1.Condition{v013}, PolicyNamespaces: &kardinalv1alpha1.PipelinePolicyNamespaces{NotShared: []string{"team-b"}}}, true, []string{"team-b"}},
		"field says agreed":  {kardinalv1alpha1.PipelineStatus{Conditions: []metav1.Condition{v013}, PolicyNamespaces: &kardinalv1alpha1.PipelinePolicyNamespaces{}}, false, nil},
		"a v0.13 controller": {kardinalv1alpha1.PipelineStatus{Conditions: []metav1.Condition{v013}}, true, []string{"old-a"}},
		"neither":            {kardinalv1alpha1.PipelineStatus{}, false, nil},
	} {
		t.Run(name, func(t *testing.T) {
			flagged, names := pipeline.NotSharedPolicyNamespaces(&kardinalv1alpha1.Pipeline{Status: tc.status})
			assert.Equal(t, tc.flagged, flagged)
			assert.Equal(t, tc.namespaces, names)
		})
	}
}
