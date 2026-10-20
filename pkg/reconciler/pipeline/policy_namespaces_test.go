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
// labelled otherwise, one that does not exist and one the controller may not
// read set PolicyNamespacesShared=False naming each (sorted, once), with one
// Warning Event and a recheck within 5 minutes. Ready is unchanged: the
// gates are still read.
func TestPipelineReconciler_PolicyNamespacesShared(t *testing.T) {
	objs := []client.Object{policyNamespace("shared", "true"), policyNamespace("team-b", ""),
		policyNamespace("labelled-no", "false"), policyNamespace("listed", ""), policyNamespace("org-gates", ""),
		policyNamespace("secret-ns", "true")}
	forbidden := func(c client.Client) client.Client {
		return interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch,
			key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Namespace); ok && key.Name == "secret-ns" {
				return apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, key.Name, nil)
			}
			return cl.Get(ctx, key, obj, opts...)
		}})
	}
	tests := []struct {
		name       string
		namespaces []string
		orgNS      []string
		want       string // the message's list; "" is no condition
	}{
		{name: "none"},
		{name: "own, default org namespace, labelled, listed",
			namespaces: []string{"default", "platform-policies", "shared", "listed"}},
		{name: "a custom org namespace", namespaces: []string{"org-gates"}, orgNS: []string{"org-gates"}},
		{name: "the default org namespace is not org with a custom list", namespaces: []string{"platform-policies"},
			orgNS: []string{"org-gates"}, want: "platform-policies (not found)"},
		{name: "not shared, sorted, once",
			namespaces: []string{"team-b", "shared", "labelled-no", "team-b", "missing", "secret-ns"},
			want:       "labelled-no, missing (not found), secret-ns (cannot be read), team-b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := makePipelineWithEnvs("app", "default", "test", "prod")
			p.Spec.PolicyNamespaces = tt.namespaces
			c := newClientWithIndex(newPipelineScheme(), append([]client.Object{p}, objs...)...)
			rec := events.NewFakeRecorder(20)
			r := &pipeline.Reconciler{Client: c, Reader: forbidden(c), Recorder: rec, PolicyNamespaces: tt.orgNS,
				PolicyShareableNamespaces: []string{"listed"}}
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(p)}
			var res ctrl.Result
			for range 2 {
				var err error
				res, err = r.Reconcile(context.Background(), req)
				require.NoError(t, err)
			}
			var got kardinalv1alpha1.Pipeline
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			ready := meta.FindStatusCondition(got.Status.Conditions, "Ready")
			require.NotNil(t, ready)
			assert.Equal(t, metav1.ConditionTrue, ready.Status, "Ready is unchanged: %s", ready.Message)
			cond := meta.FindStatusCondition(got.Status.Conditions, "PolicyNamespacesShared")
			if tt.want == "" {
				assert.Nil(t, cond)
				assert.Zero(t, policyNamespaceEvents(rec))
				return
			}
			require.NotNil(t, cond)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, "NotShared", cond.Reason)
			assert.Equal(t, got.Generation, cond.ObservedGeneration)
			assert.Contains(t, cond.Message, "have not agreed to share their PolicyGates: "+tt.want+". ")
			assert.Contains(t, cond.Message, "kardinal.io/policy-shareable=true")
			assert.Contains(t, cond.Message, "--policy-shareable-namespaces")
			assert.Contains(t, cond.Message, "still read")
			assert.Equal(t, 1, policyNamespaceEvents(rec), "one Warning Event over two reconciles")
			assert.Positive(t, res.RequeueAfter)
			assert.LessOrEqual(t, res.RequeueAfter, 5*time.Minute, "rechecked: Namespaces are not watched")
		})
	}
}

// TestPipelineReconciler_PolicyNamespacesSharedChanges: the condition
// follows the namespaces. Another namespace not shared changes the message
// and emits one more Event; labelling every namespace removes the condition
// with no Event.
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
