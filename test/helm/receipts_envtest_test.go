//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
)

// TestReceiptsOnAPIServer runs the receipt rules on a real kube-apiserver
// (envtest, the oldest and newest supported Kubernetes): the chart's
// <release>-receipts policy admits only the controller's writes (and the
// namespace controller's deletes), even for a cluster-admin; the CRD keeps
// spec immutable and status.bundle fixed once set; and a statement at the
// 256 KiB cap with a bundle of its size is stored.
//
// Covers RECEIPT-ADMISSION-01.
func TestReceiptsOnAPIServer(t *testing.T) {
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"))
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(repoRoot(t), "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, v1alpha1.AddToScheme(sch))
	admin, err := client.New(cfg, client.Options{Scheme: sch})
	require.NoError(t, err)
	ctx := context.Background()
	for _, d := range render(t, "kardinal-promoter", "--set", "receipts.enabled=true") {
		if d.Kind != "ValidatingAdmissionPolicy" && d.Kind != "ValidatingAdmissionPolicyBinding" {
			continue
		}
		if !strings.HasSuffix(d.Name, "-receipts") {
			continue
		}
		var obj unstructured.Unstructured
		require.NoError(t, json.Unmarshal(d.raw, &obj.Object))
		require.NoError(t, admin.Create(ctx, &obj), d.Name)
	}
	require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}}))
	require.NoError(t, admin.Create(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "all-admin"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects: []rbacv1.Subject{{Kind: "Group", APIGroup: rbacv1.GroupName, Name: "system:authenticated"}}}))
	as := func(user string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user}
		cl, err := client.New(c, client.Options{Scheme: sch})
		require.NoError(t, err)
		return cl
	}
	controller := as("system:serviceaccount:" + releaseNS + ":kardinal-promoter")
	alice := as("alice")

	// A statement at the cap.
	b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "b1", Namespace: "payments", UID: "u"},
		Spec: v1alpha1.BundleSpec{Type: "image", Provenance: &v1alpha1.BundleProvenance{CIRunURL: strings.Repeat("x", 250*1024)}}}
	ps := &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Namespace: "payments"},
		Spec: v1alpha1.PromotionStepSpec{PipelineName: "p", BundleName: "b1", Environment: "prod"}}
	stmt, digest, err := receipt.Build(receipt.Input{Name: "b1-prod", ClusterID: "c", Now: time.Now(), Bundle: b, Step: ps})
	require.NoError(t, err)
	mk := func(name string) *v1alpha1.PromotionReceipt {
		return &v1alpha1.PromotionReceipt{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "payments"},
			Spec: v1alpha1.PromotionReceiptSpec{Statement: string(stmt), StatementDigest: digest}}
	}

	// Wait for the policy to load: alice's create is refused.
	require.Eventually(t, func() bool { return alice.Create(ctx, mk("forged")) != nil }, 30*time.Second, 500*time.Millisecond)
	err = alice.Create(ctx, mk("forged2"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only kardinal writes PromotionReceipts")

	rc := mk("b1-prod")
	require.NoError(t, controller.Create(ctx, rc), "the controller creates")
	rc.Status = v1alpha1.PromotionReceiptStatus{Phase: v1alpha1.ReceiptSigned, Bundle: strings.Repeat("b", 700*1024)}
	require.NoError(t, controller.Status().Update(ctx, rc), "the controller writes status, a bundle of the cap's size")

	// alice, a cluster-admin, may not change or delete it.
	var got v1alpha1.PromotionReceipt
	require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(rc), &got))
	got.Labels = map[string]string{"x": "y"}
	assert.Error(t, alice.Update(ctx, got.DeepCopy()), "update")
	got.Status.Phase = v1alpha1.ReceiptFailed
	assert.Error(t, alice.Status().Update(ctx, got.DeepCopy()), "status update")
	assert.Error(t, alice.Delete(ctx, got.DeepCopy()), "delete")

	// The CRD: spec immutable, status.bundle fixed once set, for the controller too.
	require.NoError(t, admin.Get(ctx, client.ObjectKeyFromObject(rc), &got))
	spec := got.DeepCopy()
	spec.Spec.Statement = strings.Replace(spec.Spec.Statement, "prod", "uat_", 1)
	err = controller.Update(ctx, spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec is immutable")
	bundle := got.DeepCopy()
	bundle.Status.Bundle = "other"
	err = controller.Status().Update(ctx, bundle)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status.bundle cannot change once set")
	ok := got.DeepCopy()
	ok.Status.Phase = v1alpha1.ReceiptStored
	require.NoError(t, controller.Status().Update(ctx, ok), "other status fields still change")

	// The namespace controller deletes it with its namespace.
	require.NoError(t, as("system:serviceaccount:kube-system:namespace-controller").Delete(ctx, ok))
}
