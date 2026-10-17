//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestApprovalFieldsOnAPIServer (spec 23 §8.1, §8.2): on a real
// kube-apiserver, the CRDs' shape rules for structured approval fields
// refuse at write what they cover (an Approval's field names, value size and
// count; a gate's declarations), and admit a well-formed Approval and gate,
// with a declared field's type defaulted to string. Whether a value
// satisfies the gate is the tally's decision, not admission's (CR6).
//
// Covers APPROVAL-FIELDS-01.
func TestApprovalFieldsOnAPIServer(t *testing.T) {
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"), "KUBEBUILDER_ASSETS names the envtest binaries (make envtest-install)")
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(repoRoot(t), "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, v1alpha1.AddToScheme(sch))
	c, err := client.New(cfg, client.Options{Scheme: sch})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}}))

	n := 0
	approval := func(fields map[string]string) *v1alpha1.Approval {
		n++
		return &v1alpha1.Approval{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("a-%d", n), Namespace: "team-a"},
			Spec: v1alpha1.ApprovalSpec{Bundle: "app-v1", BundleUID: "uid-1", Environment: "prod", User: "alice",
				Decision: "approve", Fields: fields}}
	}
	many := map[string]string{}
	for i := range 17 {
		many[fmt.Sprintf("f%d", i)] = "x"
	}
	for _, tc := range []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"well formed", map[string]string{"ticketID": "CHG0031337", "riskAcknowledged": "true"}, ""},
		{"a value of 1024 bytes", map[string]string{"notes": strings.Repeat("x", 1024)}, ""},
		{"a name with a dash", map[string]string{"ticket-id": "x"}, "a field name is a letter then letters, digits or underscores"},
		{"a value over 1024 bytes", map[string]string{"notes": strings.Repeat("x", 1025)}, "a field value is at most 1024 bytes"},
		{"17 fields", many, "must have at most 16 items"},
	} {
		t.Run("Approval "+tc.name, func(t *testing.T) {
			a := approval(tc.fields)
			err := c.Create(ctx, a)
			if tc.want == "" {
				require.NoError(t, err)
				var got v1alpha1.Approval
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(a), &got))
				assert.Equal(t, tc.fields, got.Spec.Fields)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}

	g := 0
	gate := func(fields ...v1alpha1.ApprovalField) *v1alpha1.PolicyGate {
		g++
		return &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("g-%d", g), Namespace: "team-a"},
			Spec: v1alpha1.PolicyGateSpec{Expression: "true", Approval: &v1alpha1.GateApprovalPolicy{Required: 1, Fields: fields}}}
	}
	for _, tc := range []struct {
		name   string
		fields []v1alpha1.ApprovalField
		want   string
	}{
		{"well formed", []v1alpha1.ApprovalField{{Name: "ticketID", Required: true, Pattern: "^CHG[0-9]{7}$"},
			{Name: "ack", Type: "bool", MustBe: "true"}, {Name: "tier", Type: "enum", Values: []string{"low", "high"}, MustBe: "low"}}, ""},
		{"an enum without values", []v1alpha1.ApprovalField{{Name: "tier", Type: "enum"}}, "an enum field lists its values"},
		{"a pattern on a bool", []v1alpha1.ApprovalField{{Name: "ack", Type: "bool", Pattern: "x"}}, "a pattern is for a string or url field"},
		{"a bool's mustBe", []v1alpha1.ApprovalField{{Name: "ack", Type: "bool", MustBe: "yes"}}, "a bool field's mustBe is true or false"},
		{"a bad name", []v1alpha1.ApprovalField{{Name: "1st"}}, "should match"},
		{"a name twice", []v1alpha1.ApprovalField{{Name: "x"}, {Name: "x"}}, "Duplicate value"},
		{"an unknown type", []v1alpha1.ApprovalField{{Name: "x", Type: "date"}}, "Unsupported value"},
	} {
		t.Run("PolicyGate "+tc.name, func(t *testing.T) {
			pg := gate(tc.fields...)
			err := c.Create(ctx, pg)
			if tc.want == "" {
				require.NoError(t, err)
				var got v1alpha1.PolicyGate
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(pg), &got))
				assert.Equal(t, "string", got.Spec.Approval.Fields[0].Type, "the type defaults to string")
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
