//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	receiptrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionreceipt"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestReceipts_OCIPerNamespace (#2285): the release pushes referrers to
// each image's own repository under the controller-wide bound e2e/ of the
// suite's registry, and three namespaces receipt an image of team A's
// repository e2e/team-a: A, listed in namespaceRepositories; B, with no
// repositories; C, with the kardinal.io/receipt-oci-repositories
// annotation. Per perNamespace mode, a receipt written as the controller
// writes it gets its referrer or not:
//   - off: every namespace, as in v0.13;
//   - narrow: A, B (none of its own: the controller-wide list) and C, but
//     not A's receipt of an image in e2e/team-b, outside A's list;
//   - required: A and C, not B, whose receipt is signed and kept, Stored
//     False RepositoryNotAllowed naming B.
//
// The release's values are restored when the test ends.
//
// Covers RECEIPT-LIVE-10, RECEIPT-LIVE-11, RECEIPT-LIVE-12.
func TestReceipts_OCIPerNamespace(t *testing.T) {
	e := framework.New(t)
	ctx := context.Background()
	host := framework.NewRegistry(t).Host()
	nsA, nsB, nsC := e.Namespace(t), e.Namespace(t), e.Namespace(t)
	var c corev1.Namespace
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Name: nsC}, &c))
	c.Annotations = map[string]string{receiptrecon.AnnotationOCIRepositories: host + "/e2e/team-a/"}
	require.NoError(t, e.Client.Update(ctx, &c))

	// The release's values now, restored when the test ends.
	saved := filepath.Join(t.TempDir(), "values.yaml")
	run := e.Shell(t, `helm get values kardinal-promoter -n kardinal-system -o yaml > '`+saved+`'`)
	require.Zero(t, run.Code, run.Output)
	rel := e.ExistingRelease(t, "kardinal-promoter", framework.ControllerNamespace)
	t.Cleanup(func() {
		run := e.Shell(t, `helm upgrade kardinal-promoter "$KARDINAL_E2E_CHART" -n kardinal-system --reset-values -f '`+saved+`' --wait --timeout 5m`)
		if run.Code != 0 {
			t.Errorf("restore the release's values: %s", run.Output)
		}
		rel.WaitRolledOut(t, 5*time.Minute)
	})
	upgrade := func(mode string) {
		t.Helper()
		run := e.Shell(t, `helm upgrade kardinal-promoter "$KARDINAL_E2E_CHART" -n kardinal-system --reuse-values `+
			`--set receipts.sinks.oci.repository= --set 'receipts.sinks.oci.allowedRepositories={`+host+`/e2e/}' `+
			`--set receipts.sinks.oci.perNamespace=`+mode+` --set receipts.sinks.oci.namespaceAnnotation=true `+
			`--set 'receipts.sinks.oci.namespaceRepositories.`+nsA+`={`+host+`/e2e/team-a/}' --wait --timeout 5m`)
		require.Zero(t, run.Code, run.Output)
		rel.WaitRolledOut(t, 5*time.Minute)
	}

	cfg := rest.CopyConfig(e.Config)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + framework.ControllerNamespace + ":" + framework.ControllerServiceAccount}
	asController, err := client.New(cfg, client.Options{Scheme: e.Client.Scheme()})
	require.NoError(t, err)
	n := 0
	// receiptOf writes a receipt of image repo in ns as the controller does,
	// and waits for it to be Stored.
	receiptOf := func(ns, repo string) *v1alpha1.PromotionReceipt {
		t.Helper()
		n++
		name := fmt.Sprintf("per-ns-%02d-prod", n)
		b := &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("per-ns-%02d", n), Namespace: ns, UID: types.UID(name)},
			Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "app",
				Images: []v1alpha1.ImageRef{{Repository: repo, Digest: fmt.Sprintf("sha256:%064x", n)}}},
		}
		ps := &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Namespace: ns},
			Spec: v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: b.Name, Environment: "prod"}}
		stmt, digest, err := receipt.Build(receipt.Input{Name: name, ClusterID: clusterIdentity(t, e), Now: time.Now(), Bundle: b, Step: ps})
		require.NoError(t, err)
		require.NoError(t, asController.Create(ctx, &v1alpha1.PromotionReceipt{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{
				"kardinal.io/pipeline": "app", "kardinal.io/bundle": b.Name, "kardinal.io/environment": "prod"}},
			Spec: v1alpha1.PromotionReceiptSpec{Statement: string(stmt), StatementDigest: digest},
		}))
		return waitReceipt(t, e, ns, name, v1alpha1.ReceiptStored)
	}
	pushed := func(rc *v1alpha1.PromotionReceipt) bool {
		for _, s := range rc.Status.Stored {
			if s.Sink == v1alpha1.ReceiptSinkOCI {
				return true
			}
		}
		return false
	}
	stored := func(rc *v1alpha1.PromotionReceipt) metav1.Condition {
		for _, cond := range rc.Status.Conditions {
			if cond.Type == "Stored" {
				return cond
			}
		}
		return metav1.Condition{}
	}
	teamA, teamB := host+"/e2e/team-a/app", host+"/e2e/team-b/app"

	t.Run("off", func(t *testing.T) {
		upgrade(receiptrecon.PerNamespaceOff)
		for _, ns := range []string{nsA, nsB, nsC} {
			rc := receiptOf(ns, teamA)
			assert.True(t, pushed(rc), "%s: pushed, under the controller-wide list (%v)", ns, rc.Status.Conditions)
		}
	})
	t.Run("narrow", func(t *testing.T) {
		upgrade(receiptrecon.PerNamespaceNarrow)
		for _, ns := range []string{nsA, nsB, nsC} {
			rc := receiptOf(ns, teamA)
			assert.True(t, pushed(rc), "%s: pushed (%v)", ns, rc.Status.Conditions)
		}
		rc := receiptOf(nsA, teamB)
		assert.False(t, pushed(rc), "A's receipt of an image outside A's list")
		assert.Equal(t, "RepositoryNotAllowed", stored(rc).Reason)
		assert.Contains(t, stored(rc).Message, "not in namespace "+nsA+"'s receipt OCI repositories")
	})
	t.Run("required", func(t *testing.T) {
		upgrade(receiptrecon.PerNamespaceRequired)
		assert.True(t, pushed(receiptOf(nsA, teamA)), "A: its map entry")
		assert.True(t, pushed(receiptOf(nsC, teamA)), "C: its annotation")
		rc := receiptOf(nsB, teamA)
		assert.False(t, pushed(rc), "B: no repositories of its own")
		cond := stored(rc)
		assert.Equal(t, "RepositoryNotAllowed", cond.Reason)
		assert.Contains(t, cond.Message, "namespace "+nsB+" has no receipt OCI repositories")
		assert.NotEmpty(t, rc.Status.Bundle, "signed and kept in the CR")
		assert.True(t, strings.Contains(cond.Message, teamA), cond.Message)
	})
}
