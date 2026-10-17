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
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
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
	return signedReceiptFor(t, "checkout")
}

// signedReceiptFor is signedReceipt with its statement for pipeline, its
// labels for checkout.
func signedReceiptFor(t *testing.T, pipeline string) (*v1alpha1.PromotionReceipt, *v1alpha1.Bundle, string) {
	t.Helper()
	priv, pub, err := receipttest.Key()
	require.NoError(t, err)
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-5-0", Namespace: "default", UID: "bundle-uid"},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipeline,
			Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/acme/checkout", Digest: receiptDigest}},
			Actor:  &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer", Agent: "fixer", Sponsor: "alice@example.com"}},
		Status: v1alpha1.BundleStatus{Version: "1.5.0"},
	}
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec:       v1alpha1.PromotionStepSpec{PipelineName: pipeline, BundleName: b.Name, Environment: "prod"},
		Status: v1alpha1.PromotionStepStatus{Steps: []v1alpha1.StepStatus{
			{Name: "health-check", State: v1alpha1.StepExecutionCompleted}}},
	}
	gates := []v1alpha1.DecisionGate{{Name: "approve", Ready: true, Reason: "1 of 1", ApprovalRequired: true,
		Approvals:      []v1alpha1.GateApprovalStatus{{User: "carol@example.com", Decision: "approve", Counted: true}},
		VerifyRequired: true, Verdicts: []v1alpha1.GateVerdictStatus{{Name: "v1", Verifier: "agent qa for alice", Result: "pass", Counted: true}}}}
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
		"receipt checkout-1-5-0-prod: every check below that was made passed",
		"✓ signature:        key sha256:",
		"– transparency log: no entry",
		"✓ statement:        the cluster's spec.statement, sha256:",
		"– subject digest:   not pinned (give --image or --subject)",
		"✓ resource:         kardinal://c1/namespaces/default/pipelines/checkout/environments/prod (namespace, pipeline, environment pinned)",
		"✓ levels:           GATES_PASSED, APPROVED, !OVERRIDDEN as required",
		"✓ bundle:           checkout-1-5-0 is the Bundle the receipt was made for (UID bundle-uid)",
		"version:     checkout-1-5-0 (1.5.0)",
		"actor:       agent fixer for alice@example.com",
		"signed by:   key",
		"change:      https://github.com/acme/deploy/pull/412",
		"gate approve: pass (1 of 1)",
		"approved by: carol@example.com",
		"verdicts by: agent qa for alice",
		"levels:      KARDINAL_GATES_PASSED, KARDINAL_APPROVED, KARDINAL_VERIFIED_BY_AGENT, KARDINAL_HEALTHY",
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
	require.NotEmpty(t, res.Checks)
	assert.Equal(t, receipt.Check{Name: "signature", Checked: true, Detail: res.Checks[0].Detail}, res.Checks[0])
	for _, c := range res.Checks {
		assert.NotEqual(t, "verified", strings.ToLower(c.Detail), "never a bare verified")
	}
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
		"level missing": {nil, []string{"--require", "OVERRIDDEN"}, "KARDINAL_OVERRIDDEN is required"},
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
	pin := []string{"-n", "default", "--pipeline", "checkout", "--env", "prod", "--subject", receiptDigest, "--cluster", "c1"}
	out, _, err := runOutput(t, receiptClient(t), append([]string{"verify-receipt", "--file", file, "--key", key}, pin...)...)
	require.NoError(t, err)
	assert.Contains(t, out, "receipt checkout-1-5-0-prod: every check below that was made passed")
	assert.Contains(t, out, "✓ subject digest:   "+receiptDigest)
	assert.Contains(t, out, "(cluster, namespace, pipeline, environment pinned)")
	assert.NotContains(t, out, "VERIFIED receipt", "never a bare verified")

	// QA-3 B2: a file is verified for one namespace, Pipeline, environment
	// and subject, each required; another's is refused.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--pipeline", "checkout", "--env", "prod", "--subject", receiptDigest}, "need -n, --pipeline and --env"},
		{[]string{"-n", "default", "--env", "prod", "--subject", receiptDigest}, "need -n, --pipeline and --env"},
		{[]string{"-n", "default", "--pipeline", "checkout", "--env", "prod", "--cluster", "c1"}, "--file needs --subject"},
		{[]string{"-n", "team-b", "--pipeline", "checkout", "--env", "prod", "--subject", receiptDigest, "--cluster", "c1"}, "not namespace team-b"},
		{[]string{"-n", "default", "--pipeline", "evil", "--env", "prod", "--subject", receiptDigest, "--cluster", "c1"}, "not pipeline evil"},
		{[]string{"-n", "default", "--pipeline", "checkout", "--env", "test", "--subject", receiptDigest, "--cluster", "c1"}, "not environment test"},
		{[]string{"-n", "default", "--pipeline", "checkout", "--env", "prod", "--subject", receiptDigest, "--cluster", "other-cluster"},
			"not cluster other-cluster"},
		// The coordinator's decision on #1961: a receipt that names a cluster
		// needs --cluster (cross-cluster replay in key mode), or --any-cluster.
		{[]string{"-n", "default", "--pipeline", "checkout", "--env", "prod", "--subject", receiptDigest},
			"pin it with --cluster c1, or give --any-cluster"},
		{[]string{"-n", "default", "--pipeline", "checkout", "--env", "prod", "--subject", "sha256:" + strings.Repeat("e", 64), "--cluster", "c1"}, "is not about"},
	} {
		_, _, err := runOutput(t, receiptClient(t), append([]string{"verify-receipt", "--file", file, "--key", key}, tc.args...)...)
		require.Error(t, err, tc.args)
		assert.Contains(t, err.Error(), tc.want, tc.args)
	}
	_, stderr, err := runOutput(t, receiptClient(t), "verify-receipt", "--file", file, "--key", key, "-n", "default",
		"--pipeline", "checkout", "--env", "prod", "--subject", receiptDigest, "--any-cluster")
	require.NoError(t, err, "--any-cluster accepts any cluster")
	assert.Contains(t, stderr, "warning: --any-cluster")

	_, _, other := signedReceipt(t)
	_, _, err = runOutput(t, receiptClient(t), append([]string{"verify-receipt", "--file", file, "--key", other}, pin...)...)
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

	pin := []string{"-n", "default", "--pipeline", "checkout", "--env", "prod", "--subject", receiptDigest, "--cluster", "c1"}
	out, _, err := runOutput(t, receiptClient(t), append([]string{"verify-receipt", "--file", file, "--identity", id, "--issuer", issuer, "--trusted-root", root}, pin...)...)
	require.NoError(t, err)
	assert.Contains(t, out, "signed by:   "+id)
	_, _, err = runOutput(t, receiptClient(t), append([]string{"verify-receipt", "--file", file, "--identity", id + "/other", "--issuer", issuer, "--trusted-root", root}, pin...)...)
	require.Error(t, err)
	_, _, err = runOutput(t, receiptClient(t), append([]string{"verify-receipt", "--file", file, "--identity", id, "--trusted-root", root}, pin...)...)
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
	assert.Contains(t, out, "signed")
	assert.NotContains(t, out, "verified", "the controller signs; verify-receipt verifies (#2135)")
	out, _, err = runOutput(t, c, "history", "checkout", "-o", "json")
	require.NoError(t, err)
	assert.Contains(t, out, `"receipt": {`)
	assert.Contains(t, out, `"signed": true`)
	out, _, err = runOutput(t, c, "why", "checkout", "prod")
	require.NoError(t, err)
	assert.Contains(t, out, "Receipt  checkout-1-5-0-prod, signed: kardinal verify-receipt checkout prod --bundle checkout-1-5-0")

	bare := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(b, step, p).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	out, _, err = runOutput(t, bare, "history", "checkout")
	require.NoError(t, err)
	assert.NotContains(t, out, "RECEIPT")
	out, _, err = runOutput(t, bare, "why", "checkout", "prod")
	require.NoError(t, err)
	assert.NotContains(t, out, "Receipt")
}

// TestVerifyReceipt_ClusterBindsPipeline (QA-3): from the cluster, a receipt
// found by its labels for checkout's prod but signed for another pipeline is
// refused: the statement must be the arguments' pipeline, not only the
// labels.
func TestVerifyReceipt_ClusterBindsPipeline(t *testing.T) {
	keepStyle(t)
	rc, b, key := signedReceiptFor(t, "other")
	b.Spec.Pipeline = "checkout"
	_, _, err := runOutput(t, receiptClient(t, rc, b), "verify-receipt", "checkout", "prod", "--key", key)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not pipeline checkout")
}

// TestVerifyReceipt_ImageReferrers (#1951 N3): --image reads the image's
// receipt referrers from a registry and verifies the one of the pinned
// resource, whatever the other environments' receipts; when the image has
// more referrers than are read and none is the pinned resource's, it fails
// closed: whether a receipt exists is unknown, never "not found = OK".
//
// Covers RECEIPT-OCI-03.
func TestVerifyReceipt_ImageReferrers(t *testing.T) {
	keepStyle(t)
	other := "sha256:" + strings.Repeat("e", 64)
	inner := registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Another image whose referrers are 1,025 receipts of test, none of
		// prod: more than are read. Only the index is served: no test
		// receipt is fetched.
		if strings.HasSuffix(r.URL.Path, "/referrers/"+other) {
			idx := v1.IndexManifest{SchemaVersion: 2, MediaType: types.OCIImageIndex}
			for i := 0; i <= receipt.MaxReferrerEntries; i++ {
				idx.Manifests = append(idx.Manifests, v1.Descriptor{MediaType: types.OCIManifestSchema1, Size: 1,
					Digest: v1.Hash{Algorithm: "sha256", Hex: fmt.Sprintf("%064x", i)}, ArtifactType: receipt.BundleMediaType,
					Annotations: map[string]string{receipt.PredicateTypeAnnotation: receipt.PredicateVSA, receipt.ReceiptAnnotation: "true",
						receipt.ResourceAnnotation: fmt.Sprintf("kardinal://c1/namespaces/default/pipelines/checkout/environments/test-%d", i)}})
			}
			w.Header().Set("Content-Type", string(types.OCIImageIndex))
			_ = json.NewEncoder(w).Encode(idx)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	rc, _, key := signedReceipt(t)
	reg := &receipt.Registry{Insecure: true}
	const prod = "kardinal://c1/namespaces/default/pipelines/checkout/environments/prod"
	_, err := reg.PushReferrer(context.Background(), host+"/acme/receipts", "ghcr.io/acme/checkout", receiptDigest, prod, []byte(rc.Status.Bundle))
	require.NoError(t, err)
	args := []string{"verify-receipt", "--image", "ghcr.io/acme/checkout@" + receiptDigest, "-n", "default", "--pipeline", "checkout",
		"--env", "prod", "--cluster", "c1", "--key", key, "--repository", host + "/acme/receipts", "--insecure-registry"}
	out, _, err := runOutput(t, receiptClient(t), args...)
	require.NoError(t, err)
	assert.Contains(t, out, "✓ subject digest:   "+receiptDigest)

	args[2] = "ghcr.io/acme/checkout@" + other
	_, stderr, err := runOutput(t, receiptClient(t), args...)
	require.Error(t, err, "fails closed")
	assert.Contains(t, err.Error(), "whether one exists is unknown")
	assert.Contains(t, stderr, "more receipt referrers than are read")
}
