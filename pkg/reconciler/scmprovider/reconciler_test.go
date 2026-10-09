// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scmprovider_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/scmprovider"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

func scheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

func secret(ns, name, key, value string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{scm.LabelReferenceable: "true"}},
		Data: map[string][]byte{key: []byte(value)}}
}

// TestReconcile_ScmProvider: Ready follows the Secrets, and a second
// reconcile writes nothing (idempotent).
func TestReconcile_ScmProvider(t *testing.T) {
	tests := []struct {
		name     string
		objs     []client.Object
		webhook  bool
		want     metav1.ConditionStatus
		wantText string
	}{
		{name: "token and webhook secret", objs: []client.Object{secret("team-a", "tok", "token", "t"), secret("team-a", "hook", "secret", "s")},
			webhook: true, want: metav1.ConditionTrue},
		{name: "no token Secret", want: metav1.ConditionFalse, wantText: "spec.secretRef: the Secret team-a/tok is not found"},
		{name: "empty token", objs: []client.Object{secret("team-a", "tok", "token", " ")}, want: metav1.ConditionFalse, wantText: "has no token key"},
		{name: "Secret not referenceable", objs: []client.Object{func() client.Object {
			s := secret("team-a", "tok", "token", "t")
			s.Labels = nil
			return s
		}()}, want: metav1.ConditionFalse, wantText: "not labeled kardinal.io/referenceable=true"},
		{name: "no webhook Secret", objs: []client.Object{secret("team-a", "tok", "token", "t")}, webhook: true,
			want: metav1.ConditionFalse, wantText: "spec.webhookSecretRef: the Secret team-a/hook is not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "gh", Generation: 3},
				Spec: v1alpha1.ScmProviderSpec{Type: "github", SecretRef: v1alpha1.ScmSecretKeyRef{Name: "tok"}}}
			if tt.webhook {
				p.Spec.WebhookSecretRef = &v1alpha1.ScmSecretKeyRef{Name: "hook"}
			}
			c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(append(tt.objs, p)...).WithStatusSubresource(p).Build()
			r := &scmprovider.Reconciler{Client: c}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "gh"}}
			res, err := r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			assert.Positive(t, res.RequeueAfter, "Secrets are not watched, so the check repeats")

			var got v1alpha1.ScmProvider
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &got))
			cond := meta.FindStatusCondition(got.Status.Conditions, scmprovider.ConditionReady)
			require.NotNil(t, cond)
			assert.Equal(t, tt.want, cond.Status)
			assert.Contains(t, cond.Message, tt.wantText)
			assert.Equal(t, int64(3), got.Status.ObservedGeneration)

			_, err = r.Reconcile(context.Background(), req)
			require.NoError(t, err)
			var again v1alpha1.ScmProvider
			require.NoError(t, c.Get(context.Background(), req.NamespacedName, &again))
			assert.Equal(t, got.ResourceVersion, again.ResourceVersion, "an unchanged provider is not written again")
		})
	}
}

// TestReconcile_ClusterScmProvider reads Secrets from secretRef.namespace and
// refuses a selector that does not parse.
func TestReconcile_ClusterScmProvider(t *testing.T) {
	good := &v1alpha1.ClusterScmProvider{ObjectMeta: metav1.ObjectMeta{Name: "shared"},
		Spec: v1alpha1.ClusterScmProviderSpec{
			ScmProviderSpec:   v1alpha1.ScmProviderSpec{Type: "gitlab", SecretRef: v1alpha1.ScmSecretKeyRef{Name: "tok", Namespace: "scm", Key: "pat"}},
			AllowedNamespaces: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}},
		}}
	bad := good.DeepCopy()
	bad.Name = "bad"
	bad.Spec.AllowedNamespaces = &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "team", Operator: "Nope"}}}
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(good, bad, secret("scm", "tok", "pat", "x")).
		WithStatusSubresource(good, bad).Build()
	r := &scmprovider.Reconciler{Client: c, Cluster: true}
	for name, want := range map[string]metav1.ConditionStatus{"shared": metav1.ConditionTrue, "bad": metav1.ConditionFalse} {
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
		require.NoError(t, err)
		var got v1alpha1.ClusterScmProvider
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: name}, &got))
		cond := meta.FindStatusCondition(got.Status.Conditions, scmprovider.ConditionReady)
		require.NotNil(t, cond, name)
		assert.Equal(t, want, cond.Status, name)
		if want == metav1.ConditionFalse {
			assert.Contains(t, cond.Message, "spec.allowedNamespaces")
		}
	}
}

// TestReconcile_Gone: a provider that does not exist ends the reconcile,
// and a deleted provider's Secrets leave the shared Registry's cache: the
// ones its Ready check read are recorded for it (QA #1517), so after the
// delete they are read from the API again instead of served from memory.
func TestReconcile_Gone(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme(t)).Build()
	res, err := (&scmprovider.Reconciler{Client: c}).Reconcile(context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "a", Name: "b"}})
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)

	ctx := context.Background()
	p := &v1alpha1.ScmProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "gh", UID: "u1"},
		Spec: v1alpha1.ScmProviderSpec{Type: "github", SecretRef: v1alpha1.ScmSecretKeyRef{Name: "tok"},
			WebhookSecretRef: &v1alpha1.ScmSecretKeyRef{Name: "hook"}}}
	var secretGets atomic.Int32
	c = fake.NewClientBuilder().WithScheme(scheme(t)).
		WithObjects(p, secret("team-a", "tok", "token", "t"), secret("team-a", "hook", "secret", "s")).
		WithStatusSubresource(p).
		WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Secret); ok {
				secretGets.Add(1)
			}
			return c.Get(ctx, key, obj, opts...)
		}}).Build()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	reg := &scm.Registry{Client: c, Now: func() time.Time { return now }}
	r := &scmprovider.Reconciler{Client: c, Registry: reg}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "gh"}}
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	spec, err := scm.GetProvider(ctx, c, "team-a", v1alpha1.KindScmProvider, "gh")
	require.NoError(t, err)

	reads := secretGets.Load()

	require.NoError(t, c.Delete(ctx, p))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, err = reg.WebhookSecret(ctx, spec)
	require.NoError(t, err)
	assert.Equal(t, reads+1, secretGets.Load(), "evicted with the provider: read again")
}
