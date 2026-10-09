// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
)

// TestIdentityPolicyCheck (#1503): overrides count as verified only while
// the gate-overrides policy and a binding of the same name with Deny exist.
func TestIdentityPolicyCheck(t *testing.T) {
	const name = "kardinal-promoter-gate-overrides"
	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: name}}
	binding := func(policyName string, actions ...admissionregistrationv1.ValidationAction) client.Object {
		return &admissionregistrationv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{PolicyName: policyName, ValidationActions: actions}}
	}
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	for _, tc := range []struct {
		name    string
		objects []client.Object
		check   string
		want    bool
	}{
		{name: "bound", objects: []client.Object{policy, binding(name, admissionregistrationv1.Deny)}, check: name, want: true},
		{name: "no binding", objects: []client.Object{policy}, check: name},
		{name: "no policy", objects: []client.Object{binding(name, admissionregistrationv1.Deny)}, check: name},
		{name: "audit only", objects: []client.Object{policy, binding(name, admissionregistrationv1.Audit)}, check: name},
		{name: "binds another policy", objects: []client.Object{policy, binding("other", admissionregistrationv1.Deny)}, check: name},
		{name: "flag unset", objects: []client.Object{policy, binding(name, admissionregistrationv1.Deny)}, check: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.objects...).Build()
			check := &policygate.IdentityPolicyCheck{Reader: c, Name: tc.check}
			assert.Equal(t, tc.want, check.Active(context.Background()))
		})
	}
	var nilCheck *policygate.IdentityPolicyCheck
	assert.False(t, nilCheck.Active(context.Background()))
}

// TestIdentityPolicyCheck_TTL: an answer is kept for TTL on the injected
// clock, then read again: a binding deleted meanwhile turns verification off.
func TestIdentityPolicyCheck_TTL(t *testing.T) {
	const name = "kardinal-promoter-gate-overrides"
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{PolicyName: name,
			ValidationActions: []admissionregistrationv1.ValidationAction{admissionregistrationv1.Deny}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&admissionregistrationv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: name}}, binding).Build()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	check := &policygate.IdentityPolicyCheck{Reader: c, Name: name, TTL: time.Minute, NowFn: func() time.Time { return now }}
	require.True(t, check.Active(context.Background()))
	require.NoError(t, c.Delete(context.Background(), binding))
	now = now.Add(30 * time.Second)
	assert.True(t, check.Active(context.Background()), "kept for the TTL")
	now = now.Add(31 * time.Second)
	assert.False(t, check.Active(context.Background()), "read again after the TTL")
}
