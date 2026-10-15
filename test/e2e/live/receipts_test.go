//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// receiptsRepository is where the receipts suite pushes referrers
// (components/kardinal.sh, receipts.sinks.oci.repository).
const receiptsRepository = "e2e/receipts"

// receiptPublicKey writes the public key of the suite's receipt key
// (Secret receipt-key, components/kardinal.sh) to a file and returns it.
func receiptPublicKey(t *testing.T, e *framework.Env) string {
	t.Helper()
	var s corev1.Secret
	require.NoError(t, e.Client.Get(context.Background(),
		types.NamespacedName{Namespace: framework.ControllerNamespace, Name: "receipt-key"}, &s))
	key, err := receipt.ParsePrivateKey(s.Data["cosign.key"])
	require.NoError(t, err)
	pub, err := cryptoutils.MarshalPublicKeyToPEM(key.Public())
	require.NoError(t, err)
	f := filepath.Join(t.TempDir(), "receipt.pub")
	require.NoError(t, os.WriteFile(f, pub, 0o600))
	return f
}

// waitReceipt waits for the PromotionReceipt name in ns to reach phase.
func waitReceipt(t *testing.T, e *framework.Env, ns, name, phase string) *v1alpha1.PromotionReceipt {
	t.Helper()
	var rc v1alpha1.PromotionReceipt
	framework.Eventually(t, 3*time.Minute, "receipt "+name+" "+phase, func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &rc); err != nil {
			return false, err.Error()
		}
		return rc.Status.Phase == phase, fmt.Sprintf("phase=%q conditions=%v", rc.Status.Phase, rc.Status.Conditions)
	})
	return &rc
}

// TestReceipts_SignedPromotion promotes a version by digest through test
// and prod (a PR) with receipts signed by a static key. Each environment
// gets a PromotionReceipt, signed, stored in the CR and pushed as a
// referrer to the suite's registry; the prod PR gets one comment naming
// it. kardinal verify-receipt passes from the cluster and from the
// registry (--image), with the levels the promotion earned, and kardinal
// history and why show it.
//
// Covers RECEIPT-LIVE-01, RECEIPT-LIVE-02, RECEIPT-LIVE-03.
func TestReceipts_SignedPromotion(t *testing.T) {
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	pub := receiptPublicKey(t, e)
	byDigest := fixtures.Image + "@" + fixtures.V2Digest
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", byDigest)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, bundle, "prod")
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)

	for _, env := range []string{"test", "prod"} {
		rc := waitReceipt(t, e, a.ns, bundle+"-"+env, v1alpha1.ReceiptStored)
		assert.Equal(t, v1alpha1.ReceiptSigningKey, rc.Status.SignedBy.Mode)
		sinks := map[string]bool{}
		for _, s := range rc.Status.Stored {
			sinks[s.Sink] = true
		}
		assert.True(t, sinks[v1alpha1.ReceiptSinkCRD] && sinks[v1alpha1.ReceiptSinkOCI], "%s stored in %+v", env, rc.Status.Stored)
		assert.Empty(t, rc.OwnerReferences)
	}
	var step v1alpha1.PromotionStep
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: a.ns, Name: bundle + "-prod"}, &step))
	assert.Equal(t, bundle+"-prod", step.Status.Receipt)
	assert.Nil(t, step.Status.Decision, "the snapshot is cleared once the receipt exists")

	out := e.MustKardinal(t, a.ns, "verify-receipt", pipelineName, "prod", "--key", pub,
		"--require", "HEALTHY,!OVERRIDDEN")
	assert.Contains(t, out, "VERIFIED receipt "+bundle+"-prod")
	assert.Contains(t, out, "change:      "+pr.URL)
	out = e.MustKardinal(t, a.ns, "verify-receipt", pipelineName, "prod", "--bundle", bundle, "--key", pub, "-o", "json")
	var res struct {
		Verified  bool               `json:"verified"`
		Statement *receipt.Statement `json:"statement"`
	}
	start := strings.Index(out, "{")
	require.GreaterOrEqual(t, start, 0, out)
	require.NoError(t, json.Unmarshal([]byte(out[start:]), &res), out)
	require.True(t, res.Verified)
	assert.Equal(t, strings.TrimPrefix(fixtures.V2Digest, "sha256:"), res.Statement.Subject[0].Digest["sha256"])
	assert.True(t, res.Statement.Predicate.Kardinal.DecisionSnapshot)
	assert.Equal(t, pr.Number, res.Statement.Predicate.Kardinal.Change.PR.Number)

	reg := framework.NewRegistry(t)
	out = e.MustKardinal(t, a.ns, "verify-receipt", "--image", byDigest, "--env", "prod", "--key", pub,
		"--repository", reg.HostRepository(receiptsRepository), "--insecure-registry")
	assert.Contains(t, out, "VERIFIED receipt")

	framework.Eventually(t, 2*time.Minute, "the receipt comment on the prod PR", func(context.Context) (bool, string) {
		c := e.PRComments(t, a.repo, pr.Number, "Promotion receipt: `"+bundle+"-prod`")
		return len(c) == 1, fmt.Sprintf("%d comments", len(c))
	})
	got := e.PRComments(t, a.repo, pr.Number, "Promotion receipt:")
	require.Len(t, got, 1, "commented once")
	assert.Contains(t, got[0], "kardinal verify-receipt "+pipelineName+" prod --bundle "+bundle)

	hist := e.MustKardinal(t, a.ns, "history", pipelineName)
	assert.Contains(t, hist, "RECEIPT")
	why := e.MustKardinal(t, a.ns, "why", pipelineName, "prod")
	assert.Contains(t, why, "Receipt")
	assert.Contains(t, why, bundle+"-prod")
}

// TestReceipts_TamperedReceiptFails: an admin who removed the receipts
// admission policy replaces a receipt with one whose statement differs by
// a byte, and copies the signed bundle onto it: kardinal verify-receipt
// fails, and the controller marks it Failed instead of signing it. A
// receipt exported to a file and changed by a byte fails too. Only the
// controller may write receipts while the policy is in place.
//
// Covers RECEIPT-LIVE-04.
func TestReceipts_TamperedReceiptFails(t *testing.T) {
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	pub := receiptPublicKey(t, e)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	rc := waitReceipt(t, e, a.ns, bundle+"-test", v1alpha1.ReceiptStored)
	e.MustKardinal(t, a.ns, "verify-receipt", pipelineName, "test", "--key", pub)

	// A user may not write receipts while the policy is bound.
	forged := rc.DeepCopy()
	forged.ObjectMeta = metav1.ObjectMeta{Name: bundle + "-forged", Namespace: a.ns, Labels: rc.Labels}
	err := e.Client.Create(ctx, forged)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only kardinal writes PromotionReceipts")

	// A file changed by a byte fails.
	file := filepath.Join(t.TempDir(), "receipt.json")
	var b map[string]any
	require.NoError(t, json.Unmarshal([]byte(rc.Status.Bundle), &b))
	env := b["dsseEnvelope"].(map[string]any)
	payload, err := base64.StdEncoding.DecodeString(env["payload"].(string))
	require.NoError(t, err)
	env["payload"] = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(payload), `"test"`, `"prod"`, 1)))
	raw, err := json.Marshal(b)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, raw, 0o600))
	_, err = e.Kardinal(t, a.ns, "verify-receipt", "--file", file, "--key", pub)
	require.Error(t, err, "a changed payload fails the signature")

	// The admin removes the binding and replaces the receipt.
	binding := framework.ControllerDeployment + "-receipts"
	var vapb admissionregistrationv1.ValidatingAdmissionPolicyBinding
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Name: binding}, &vapb))
	saved := vapb.DeepCopy()
	require.NoError(t, e.Client.Delete(ctx, &vapb))
	t.Cleanup(func() {
		saved.ResourceVersion, saved.UID = "", ""
		_ = e.Client.Create(context.Background(), saved)
	})
	signedBundle := rc.Status.Bundle
	framework.Eventually(t, time.Minute, "the receipt deleted without the policy", func(ctx context.Context) (bool, string) {
		err := e.Client.Delete(ctx, rc)
		return client.IgnoreNotFound(err) == nil, fmt.Sprint(err)
	})
	tampered := &v1alpha1.PromotionReceipt{
		ObjectMeta: metav1.ObjectMeta{Name: rc.Name, Namespace: a.ns, Labels: rc.Labels},
		Spec: v1alpha1.PromotionReceiptSpec{
			Statement:       strings.Replace(rc.Spec.Statement, `"PASSED"`, `"PASSEd"`, 1),
			StatementDigest: rc.Spec.StatementDigest,
		},
	}
	require.NoError(t, e.Client.Create(ctx, tampered))
	got := waitReceipt(t, e, a.ns, rc.Name, v1alpha1.ReceiptFailed)
	assert.Empty(t, got.Status.Bundle, "a changed statement is never signed")
	got.Status.Bundle = signedBundle
	require.NoError(t, e.Client.Status().Update(ctx, got))

	out, err := e.Kardinal(t, a.ns, "verify-receipt", pipelineName, "test", "--key", pub)
	require.Error(t, err, out)
	assert.Contains(t, out, "the receipt was changed")
}
