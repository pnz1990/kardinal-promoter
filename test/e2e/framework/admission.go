// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// denyAdmission creates a ValidatingAdmissionPolicy and binding named policy
// that deny, with message, every request rule matches and the CEL match
// condition expression selects. They are deleted when the test ends; remove
// deletes them sooner.
func (e *Env) denyAdmission(t *testing.T, policy string, rule admissionv1.RuleWithOperations,
	expression, message string) (remove func() error) {
	t.Helper()
	fail := admissionv1.Fail
	vap := &admissionv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: policy, Labels: map[string]string{"kardinal.io/e2e": "true"}},
		Spec: admissionv1.ValidatingAdmissionPolicySpec{
			FailurePolicy: &fail,
			MatchConstraints: &admissionv1.MatchResources{ResourceRules: []admissionv1.NamedRuleWithOperations{{
				RuleWithOperations: rule,
			}}},
			MatchConditions: []admissionv1.MatchCondition{{Name: "selected", Expression: expression}},
			Validations:     []admissionv1.Validation{{Expression: "false", Message: message}},
		},
	}
	binding := &admissionv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: policy, Labels: map[string]string{"kardinal.io/e2e": "true"}},
		Spec: admissionv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName:        policy,
			ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adm := e.Kube.AdmissionregistrationV1()
	if _, err := adm.ValidatingAdmissionPolicies().Create(ctx, vap, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create ValidatingAdmissionPolicy %s: %v", policy, err)
	}
	if _, err := adm.ValidatingAdmissionPolicyBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create ValidatingAdmissionPolicyBinding %s: %v", policy, err)
	}
	remove = func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, del := range []func(context.Context, string, metav1.DeleteOptions) error{
			adm.ValidatingAdmissionPolicyBindings().Delete, adm.ValidatingAdmissionPolicies().Delete,
		} {
			if err := del(ctx, policy, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
		return nil
	}
	t.Cleanup(func() {
		if err := remove(); err != nil {
			t.Errorf("delete admission policy %s: %v", policy, err)
		}
	})
	return remove
}
