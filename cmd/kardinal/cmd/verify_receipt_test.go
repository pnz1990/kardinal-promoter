// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"crypto"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt/receipttest"
)

const receiptDigest = "sha256:9f2c000000000000000000000000000000000000000000000000000000000001"

// signedReceipt is a PromotionReceipt for checkout's prod, signed with a
// fresh key, its Bundle, and the public key file.
func signedReceipt(t *testing.T) (*v1alpha1.PromotionReceipt, *v1alpha1.Bundle, string) {
	t.Helper()
	priv, pub, err := receipttest.Key()
	require.NoError(t, err)
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-5-0", Namespace: "default", UID: "bundle-uid"},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: "checkout",
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/acme/checkout", Digest: receiptDigest}},
			Actor:  &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer", Agent: "fixer", Sponsor: "alice@example.com"}},
		Status: v1alpha1.BundleStatus{Version: "1.5.0"},
	}
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec:       v1alpha1.PromotionStepSpec{PipelineName: "checkout", BundleName: b.Name, Environment: "prod"},
		Status: v1alpha1.PromotionStepStatus{Steps: []v1alpha1.StepStatus{
			{Name: "health-check", State: v1alpha1.StepExecutionCompleted}}},
	}
	gates := []v1alpha1.DecisionGate{{Name: "approve", Ready: true, Reason: "1 of 1", ApprovalRequired: true,
		Approvals: []v1alpha1.GateApprovalStatus{{User: "carol@example.com", Decision: "approve", Counted: true}}}}
	stmt, digest, err := receipt.Build(receipt.Input{Name: "checkout-1-5-0-prod", ClusterID: "c1", Now: time.Now(),
		Bundle: b, Step: ps, Snapshot: true, Decision: &v1alpha1.PromotionDecision{Gates: gates, PolicyDigest: receipt.PolicyDigest(1, "h", gates)},
		PR: &receipt.PR{URL: "https://github.com/acme/deploy/pull/412", Number: 412}})
	require.NoError(t, err)
	s := &receipt.Signer{Mode: v1alpha1.ReceiptSigningKey, Key: func(context.Context) (crypto.Signer, string, error) {
		k, err := receipt.ParsePrivateKey(priv)
		return k, "kardinal-system/receipt-key", err
	}}
	signed, err := s.Sign(context.Background(), stmt)
	require.NoError(t, err)
	rc := &v1alpha1.PromotionReceipt{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-5-0-prod", Namespace: "default", Labels: map[string]string{
			"kardinal.io/pipeline": "checkout", "kardinal.io/bundle": b.Name, "kardinal.io/environment": "prod"}},
		Spec:   v1alpha1.PromotionReceiptSpec{Statement: string(stmt), StatementDigest: digest},
		Status: v1alpha1.PromotionReceiptStatus{Phase: v1alpha1.ReceiptStored, Bundle: string(signed.Bundle)},
	}
	keyFile := filepath.Join(t.TempDir(), "receipt.pub")
	require.NoError(t, os.WriteFile(keyFile, pub, 0o600))
	return rc, b, keyFile
}

func receiptClient(t *testing.T, objs ...sigs_client.Object) sigs_client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(objs...).Build()
}

// TestVerifyReceipt_FromCluster: a signed receipt verifies from the cluster
// and prints the version, the actor, the signer, the gates, the approvers,
// the levels and the policy digest; -o json carries the statement.
//
// Covers RECEIPT-VERIFY-01.
func TestVerifyReceipt_FromCluster(t *testing.T) {
	keepStyle(t)
	rc, b, key := signedReceipt(t)
	c := receiptClient(t, rc, b)
	out, _, err := runOutput(t, c, "verify-receipt", "checkout", "prod", "--key", key, "--require", "GATES_PASSED,APPROVED,!OVERRIDDEN")
	require.NoError(t, err)
	for _, want := range []string{
		"VERIFIED receipt checkout-1-5-0-prod",
		"version:     checkout-1-5-0 (1.5.0)",
		"actor:       agent fixer for alice@example.com",
		"signed by:   key",
		"change:      https://github.com/acme/deploy/pull/412",
		"gate approve: pass (1 of 1)",
		"approved by: carol@example.com",
		"levels:      KARDINAL_GATES_PASSED, KARDINAL_APPROVED, KARDINAL_HEALTHY",
		"policy:      sha256:",
	} {
		assert.Contains(t, out, want)
	}

	out, _, err = runOutput(t, c, "verify-receipt", "checkout", "prod", "--bundle", "checkout-1-5-0", "--key", key, "-o", "json")
	require.NoError(t, err)
	var res verifyReceiptResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.True(t, res.Verified)
	assert.Equal(t, "checkout-1-5-0-prod", res.Receipt)
	assert.Equal(t, "bundle-uid", res.Statement.Predicate.Kardinal.Version.BundleUID)
}

// TestVerifyReceipt_Tampered: verify-receipt fails when an admin changed a
// byte of the CR's statement (with or without fixing its digest), when the
// Bundle of that name is another one, for another environment, and when a
// required level is missing.
//
// Covers RECEIPT-VERIFY-02.
func TestVerifyReceipt_Tampered(t *testing.T) {
	keepStyle(t)
	cases := map[string]struct {
		mutate func(*v1alpha1.PromotionReceipt, *v1alpha1.Bundle)
		args   []string
		want   string
	}{
		"statement byte changed": {func(rc *v1alpha1.PromotionReceipt, _ *v1alpha1.Bundle) {
			rc.Spec.Statement = strings.Replace(rc.Spec.Statement, "carol", "carl_", 1)
		}, nil, "does not match spec.statementDigest"},
		"statement and digest changed": {func(rc *v1alpha1.PromotionReceipt, _ *v1alpha1.Bundle) {
			rc.Spec.Statement = strings.Replace(rc.Spec.Statement, "carol", "carl_", 1)
			rc.Spec.StatementDigest = receipt.Digest([]byte(rc.Spec.Statement))
		}, nil, "changed after it was signed"},
		"bundle recreated": {func(_ *v1alpha1.PromotionReceipt, b *v1alpha1.Bundle) { b.UID = "other" }, nil, "now has UID other"},
		"not signed": {func(rc *v1alpha1.PromotionReceipt, _ *v1alpha1.Bundle) {
			rc.Status.Bundle, rc.Status.Phase = "", v1alpha1.ReceiptPending
		}, nil, "is not signed (phase Pending)"},
		"level missing": {nil, []string{"--require", "VERIFIED_BY_AGENT"}, "KARDINAL_VERIFIED_BY_AGENT is required"},
		"no identity":   {nil, []string{"--key", ""}, "either a public key or a certificate identity"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rc, b, key := signedReceipt(t)
			if tc.mutate != nil {
				tc.mutate(rc, b)
			}
			args := append([]string{"verify-receipt", "checkout", "prod", "--key", key}, tc.args...)
			_, _, err := runOutput(t, receiptClient(t, rc, b), args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
	t.Run("another environment", func(t *testing.T) {
		rc, b, key := signedReceipt(t)
		_, _, err := runOutput(t, receiptClient(t, rc, b), "verify-receipt", "checkout", "uat", "--key", key)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no receipt for pipeline checkout, environment uat")
	})
}

// TestVerifyReceipt_File: a receipt exported to a file verifies offline,
// with the environment and the levels; another key fails.
//
// Covers RECEIPT-VERIFY-01.
func TestVerifyReceipt_File(t *testing.T) {
	keepStyle(t)
	rc, _, key := signedReceipt(t)
	file := filepath.Join(t.TempDir(), "receipt.sigstore.json")
	require.NoError(t, os.WriteFile(file, []byte(rc.Status.Bundle), 0o600))
	out, _, err := runOutput(t, receiptClient(t), "verify-receipt", "--file", file, "--key", key, "--env", "prod")
	require.NoError(t, err)
	assert.Contains(t, out, "VERIFIED receipt checkout-1-5-0-prod")

	_, _, other := signedReceipt(t)
	_, _, err = runOutput(t, receiptClient(t), "verify-receipt", "--file", file, "--key", other)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NOT VERIFIED")

	_, _, err = runOutput(t, receiptClient(t), "verify-receipt", "--file", file, "checkout", "prod", "--key", key)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one of them")
}

// TestVerifyReceipt_Keyless: a keyless receipt verifies with --identity,
// --issuer and --trusted-root, and fails with another identity.
//
// Covers RECEIPT-VERIFY-01.
func TestVerifyReceipt_Keyless(t *testing.T) {
	keepStyle(t)
	const id = "https://kubernetes.io/namespaces/kardinal-system/serviceaccounts/kardinal-promoter"
	const issuer = "https://oidc.example.com"
	ss, err := receipttest.NewSigstore(id, issuer)
	require.NoError(t, err)
	defer ss.Close()
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(token, []byte(receipttest.Token("system:serviceaccount:kardinal-system:kardinal-promoter")), 0o600))
	rc, _, _ := signedReceipt(t)
	signed, err := (&receipt.Signer{Mode: v1alpha1.ReceiptSigningKeyless, FulcioURL: ss.Fulcio, TSAURL: ss.TSA, TokenFile: token}).
		Sign(context.Background(), []byte(rc.Spec.Statement))
	require.NoError(t, err)
	file, root := filepath.Join(dir, "receipt.json"), filepath.Join(dir, "trusted_root.json")
	require.NoError(t, os.WriteFile(file, signed.Bundle, 0o600))
	raw, err := ss.Root.MarshalJSON()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(root, raw, 0o600))

	out, _, err := runOutput(t, receiptClient(t), "verify-receipt", "--file", file, "--identity", id, "--issuer", issuer, "--trusted-root", root)
	require.NoError(t, err)
	assert.Contains(t, out, "signed by:   "+id)
	_, _, err = runOutput(t, receiptClient(t), "verify-receipt", "--file", file, "--identity", id+"/other", "--issuer", issuer, "--trusted-root", root)
	require.Error(t, err)
	_, _, err = runOutput(t, receiptClient(t), "verify-receipt", "--file", file, "--identity", id, "--trusted-root", root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs both --identity and --issuer")
}

// TestReceiptMarks: kardinal history shows a RECEIPT column once a step has
// a receipt, and -o json names it; kardinal why names the receipt of the
// version an environment runs, with the verify command. A cluster without
// receipts shows neither.
//
// Covers RECEIPT-PR-02.
func TestReceiptMarks(t *testing.T) {
	keepStyle(t)
	rc, b, _ := signedReceipt(t)
	b.Labels = map[string]string{"kardinal.io/pipeline": "checkout"}
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-5-0-prod", Namespace: "default", Labels: map[string]string{"kardinal.io/pipeline": "checkout"}},
		Spec:       v1alpha1.PromotionStepSpec{PipelineName: "checkout", BundleName: b.Name, Environment: "prod"},
		Status:     v1alpha1.PromotionStepStatus{State: "Verified", Receipt: rc.Name},
	}
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "prod"}}},
		Status: v1alpha1.PipelineStatus{Environments: []v1alpha1.PipelineEnvironmentStatus{
			{Name: "prod", State: "Verified", Bundle: b.Name, Version: "1.5.0"}}}}
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(rc, b, step, p).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()

	out, _, err := runOutput(t, c, "history", "checkout")
	require.NoError(t, err)
	assert.Contains(t, out, "RECEIPT")
	assert.Contains(t, out, "verified")
	out, _, err = runOutput(t, c, "history", "checkout", "-o", "json")
	require.NoError(t, err)
	assert.Contains(t, out, `"receipt": {`)
	assert.Contains(t, out, `"verified": true`)
	out, _, err = runOutput(t, c, "why", "checkout", "prod")
	require.NoError(t, err)
	assert.Contains(t, out, "Receipt  checkout-1-5-0-prod, verified: kardinal verify-receipt checkout prod --bundle checkout-1-5-0")

	bare := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(b, step, p).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	out, _, err = runOutput(t, bare, "history", "checkout")
	require.NoError(t, err)
	assert.NotContains(t, out, "RECEIPT")
	out, _, err = runOutput(t, bare, "why", "checkout", "prod")
	require.NoError(t, err)
	assert.NotContains(t, out, "Receipt")
}
