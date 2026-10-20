//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestReceipts_EveryImageStored (#2331): the receipt of a Bundle with 100
// images (spec.images' bound), written as the controller writes it, is
// pushed to the suite's registry as a referrer of every image and reaches
// Stored. The API server accepts its status.stored, the CR and 100 OCI
// entries; capped at 16 it refused that write every time, and the receipt
// stayed Signed, its referrers pushed again at each retry.
//
// Covers RECEIPT-LIVE-09.
func TestReceipts_EveryImageStored(t *testing.T) {
	const n = 100
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)

	images := make([]v1alpha1.ImageRef, n)
	for i := range images {
		images[i] = v1alpha1.ImageRef{Repository: fmt.Sprintf("registry.example/acme/svc-%03d", i), Digest: fmt.Sprintf("sha256:%064x", i+1)}
	}
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "wide-1-0-0", Namespace: ns, UID: "wide-bundle"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: "wide", Images: images},
	}
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns},
		Spec:       v1alpha1.PromotionStepSpec{PipelineName: "wide", BundleName: b.Name, Environment: "prod"},
	}
	stmt, digest, err := receipt.Build(receipt.Input{Name: "wide-1-0-0-prod", ClusterID: clusterIdentity(t, e), Now: time.Now(), Bundle: b, Step: ps})
	require.NoError(t, err)

	// Only the controller may write receipts (the receipts admission policy).
	cfg := rest.CopyConfig(e.Config)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + framework.ControllerNamespace + ":" + framework.ControllerServiceAccount}
	asController, err := client.New(cfg, client.Options{Scheme: e.Client.Scheme()})
	require.NoError(t, err)
	require.NoError(t, asController.Create(ctx, &v1alpha1.PromotionReceipt{
		ObjectMeta: metav1.ObjectMeta{Name: "wide-1-0-0-prod", Namespace: ns, Labels: map[string]string{
			"kardinal.io/pipeline": "wide", "kardinal.io/bundle": b.Name, "kardinal.io/environment": "prod"}},
		Spec: v1alpha1.PromotionReceiptSpec{Statement: string(stmt), StatementDigest: digest},
	}))

	rc := waitReceipt(t, e, ns, "wide-1-0-0-prod", v1alpha1.ReceiptStored)
	subjects := map[string]bool{}
	crd := 0
	for _, s := range rc.Status.Stored {
		switch s.Sink {
		case v1alpha1.ReceiptSinkOCI:
			assert.Contains(t, s.Ref, "/"+receiptsRepository+"@sha256:", "pushed to the suite's receipts repository")
			subjects[s.Subject] = true
		case v1alpha1.ReceiptSinkCRD:
			crd++
		}
	}
	assert.Equal(t, 1, crd, "stored in the CR")
	assert.Len(t, subjects, n, "a referrer for each of the %d images", n)
	t.Logf("receipt %s: %d entries in status.stored", rc.Name, len(rc.Status.Stored))
}
