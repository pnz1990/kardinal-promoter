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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	return receiptPublicKeyIn(t, e, framework.ControllerNamespace)
}

// receiptPublicKeyIn is receiptPublicKey for the release in namespace ns
// (shard b's: components/shard.sh).
func receiptPublicKeyIn(t *testing.T, e *framework.Env, ns string) string {
	t.Helper()
	var s corev1.Secret
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "receipt-key"}, &s))
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

// clusterIdentity is the cluster identity receipts name: the kube-system
// Namespace UID (no receipts.clusterID in the suite).
func clusterIdentity(t *testing.T, e *framework.Env) string {
	t.Helper()
	ns, err := e.Kube.CoreV1().Namespaces().Get(context.Background(), "kube-system", metav1.GetOptions{})
	require.NoError(t, err)
	return string(ns.UID)
}

// promotionReceipt waits for the PromotionReceipt of bundle in env (by its
// labels: its name has a hash suffix, receipt.Name) to reach phase.
func promotionReceipt(t *testing.T, e *framework.Env, ns, bundle, env, phase string) *v1alpha1.PromotionReceipt {
	t.Helper()
	var rc v1alpha1.PromotionReceipt
	framework.Eventually(t, 3*time.Minute, "the receipt of "+bundle+" in "+env+" "+phase, func(ctx context.Context) (bool, string) {
		var list v1alpha1.PromotionReceiptList
		if err := e.Client.List(ctx, &list, client.InNamespace(ns),
			client.MatchingLabels{"kardinal.io/bundle": bundle, "kardinal.io/environment": env}); err != nil {
			return false, err.Error()
		}
		if len(list.Items) != 1 {
			return false, fmt.Sprintf("%d receipts", len(list.Items))
		}
		rc = list.Items[0]
		return rc.Status.Phase == phase, fmt.Sprintf("%s phase=%q conditions=%v", rc.Name, rc.Status.Phase, rc.Status.Conditions)
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

	var prodReceipt string
	for _, env := range []string{"test", "prod"} {
		rc := promotionReceipt(t, e, a.ns, bundle, env, v1alpha1.ReceiptStored)
		if env == "prod" {
			prodReceipt = rc.Name
		}
		assert.Equal(t, v1alpha1.ReceiptSigningKey, rc.Status.SignedBy.Mode)
		sinks := map[string]bool{}
		for _, s := range rc.Status.Stored {
			sinks[s.Sink] = true
		}
		assert.True(t, sinks[v1alpha1.ReceiptSinkCRD] && sinks[v1alpha1.ReceiptSinkOCI], "%s stored in %+v", env, rc.Status.Stored)
		assert.Empty(t, rc.OwnerReferences)
	}
	framework.Eventually(t, time.Minute, "the prod step names its receipt", func(ctx context.Context) (bool, string) {
		step, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		if err != nil || !ok {
			return false, fmt.Sprint(ok, err)
		}
		return step.Status.Receipt == prodReceipt && step.Status.Decision == nil,
			fmt.Sprintf("receipt=%q decision cleared=%v", step.Status.Receipt, step.Status.Decision == nil)
	})

	out := e.MustKardinal(t, a.ns, "verify-receipt", pipelineName, "prod", "--key", pub,
		"--require", "HEALTHY,!OVERRIDDEN")
	assert.Contains(t, out, "receipt "+prodReceipt+": every check below that was made passed")
	assert.Contains(t, out, "✓ statement:")
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
	out = e.MustKardinal(t, a.ns, "verify-receipt", "--image", byDigest, "--pipeline", pipelineName, "--env", "prod",
		"--cluster", clusterIdentity(t, e), "--key", pub,
		"--repository", reg.HostRepository(receiptsRepository), "--insecure-registry")
	assert.Contains(t, out, ": every check below that was made passed")
	assert.Contains(t, out, "✓ subject digest:")

	framework.Eventually(t, 2*time.Minute, "the receipt comment on the prod PR", func(context.Context) (bool, string) {
		c := e.PRComments(t, a.repo, pr.Number, "Promotion receipt: `"+prodReceipt+"`")
		return len(c) == 1, fmt.Sprintf("%d comments", len(c))
	})
	got := e.PRComments(t, a.repo, pr.Number, "Promotion receipt:")
	require.Len(t, got, 1, "commented once")
	assert.Contains(t, got[0], "kardinal verify-receipt "+pipelineName+" prod --bundle "+bundle)

	hist := e.MustKardinal(t, a.ns, "history", pipelineName)
	assert.Contains(t, hist, "RECEIPT")
	why := e.MustKardinal(t, a.ns, "why", pipelineName, "prod")
	assert.Contains(t, why, "Receipt")
	assert.Contains(t, why, prodReceipt)
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
	rc := promotionReceipt(t, e, a.ns, bundle, "test", v1alpha1.ReceiptStored)
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
	_, err = e.Kardinal(t, a.ns, "verify-receipt", "--file", file, "--pipeline", pipelineName, "--env", "test",
		"--subject", fixtures.V2Digest, "--cluster", clusterIdentity(t, e), "--key", pub)
	require.Error(t, err, "a changed payload fails the signature")

	// The admin removes the binding and replaces the receipt.
	bindings := e.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings()
	saved, err := bindings.Get(ctx, framework.ControllerDeployment+"-receipts", metav1.GetOptions{})
	require.NoError(t, err)
	require.NoError(t, bindings.Delete(ctx, saved.Name, metav1.DeleteOptions{}))
	t.Cleanup(func() {
		saved.ResourceVersion, saved.UID = "", ""
		_, _ = bindings.Create(context.Background(), saved, metav1.CreateOptions{})
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

// TestReceipts_TokenInProvenanceRedacted (#2158): the API server refuses a
// Bundle whose provenance.ciRunURL carries a credential parameter however
// it is written (?token=, a percent-encoded ?%74oken=), with kubectl too.
// A token in a parameter the rule cannot know (?ref=ghp_...) is admitted,
// and the Bundle applied with kubectl is promoted, but its receipt holds
// no token: the signed statement has it REDACTED and records the
// redaction, and kardinal verify-receipt passes from the cluster and from
// the registry copy.
//
// Covers RECEIPT-REDACT-03.
func TestReceipts_TokenInProvenanceRedacted(t *testing.T) {
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	pub := receiptPublicKey(t, e)
	const secret = "ghp_e2es3cretvalue0123456789abcdefgh"
	byDigest := fixtures.Image + "@" + fixtures.V2Digest
	repo, digest, _ := strings.Cut(byDigest, "@")

	for _, url := range []string{"https://ci.example.com/run/7?token=" + secret, "https://ci.example.com/run/7?%74oken=" + secret} {
		refused := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "with-token", Namespace: a.ns},
			Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipelineName,
				Images:     []v1alpha1.ImageRef{{Repository: repo, Digest: digest}},
				Provenance: &v1alpha1.BundleProvenance{CIRunURL: url}}}
		err := e.Client.Create(ctx, refused)
		require.Error(t, err, "the CRD refuses %s", url)
		assert.True(t, apierrors.IsInvalid(err), "%v", err)
		assert.Contains(t, err.Error(), "provenance.ciRunURL must not")
	}

	e.Kubectl(t, a.ns, fmt.Sprintf(`apiVersion: kardinal.io/v1alpha1
kind: Bundle
metadata:
  name: token-in-ref
spec:
  type: image
  pipeline: %s
  images:
  - repository: %s
    digest: %s
  provenance:
    author: ci
    ciRunURL: "https://ci.example.com/run/7?ref=%s&attempt=1"
`, pipelineName, repo, digest, secret), "apply", "-f", "-")
	e.WaitStepState(t, a.ns, pipelineName, "token-in-ref", "test", "Verified", promoteTimeout)
	rc := promotionReceipt(t, e, a.ns, "token-in-ref", "test", v1alpha1.ReceiptStored)
	assert.NotContains(t, rc.Spec.Statement, secret)
	st, err := receipt.Parse([]byte(rc.Spec.Statement))
	require.NoError(t, err)
	assert.Equal(t, "https://ci.example.com/run/7?ref=REDACTED&attempt=1", st.Predicate.Kardinal.Version.Provenance.CIRunURL)
	assert.Equal(t, []string{"kardinalPromotion.version.provenance.ciRunURL"}, st.Predicate.Kardinal.Redacted)

	verified := func(out string) *receipt.Statement {
		t.Helper()
		assert.NotContains(t, out, secret)
		var res struct {
			Verified  bool               `json:"verified"`
			Statement *receipt.Statement `json:"statement"`
		}
		start := strings.Index(out, "{")
		require.GreaterOrEqual(t, start, 0, out)
		require.NoError(t, json.Unmarshal([]byte(out[start:]), &res), out)
		require.True(t, res.Verified, out)
		require.NotNil(t, res.Statement)
		return res.Statement
	}
	got := verified(e.MustKardinal(t, a.ns, "verify-receipt", pipelineName, "test", "--bundle", "token-in-ref", "--key", pub, "-o", "json"))
	assert.Equal(t, st.Predicate.Kardinal.Redacted, got.Predicate.Kardinal.Redacted, "the signed statement records the redaction")
	reg := framework.NewRegistry(t)
	got = verified(e.MustKardinal(t, a.ns, "verify-receipt", "--image", byDigest, "--pipeline", pipelineName, "--env", "test",
		"--cluster", clusterIdentity(t, e), "--key", pub,
		"--repository", reg.HostRepository(receiptsRepository), "--insecure-registry", "-o", "json"))
	assert.Equal(t, "https://ci.example.com/run/7?ref=REDACTED&attempt=1", got.Predicate.Kardinal.Version.Provenance.CIRunURL,
		"the registry copy has no token either")
}
