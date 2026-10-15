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

package promotionreceipt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt/receipttest"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/observability"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// commentSCM records PR comments; every other SCM call panics (nil
// embedded interface): the receipt reconciler makes none.
type commentSCM struct {
	scm.SCMProvider
	comments []string
	err      error
}

func (c *commentSCM) CommentOnPR(_ context.Context, repo string, n int, body string) error {
	if c.err != nil {
		return c.err
	}
	c.comments = append(c.comments, fmt.Sprintf("%s#%d: %s", repo, n, body))
	return nil
}

var now = time.Date(2026, 10, 14, 10, 30, 0, 0, time.UTC)

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// newReceipt is a PromotionReceipt as the PromotionStep reconciler creates
// it, for an image at repo@digest.
func newReceipt(t *testing.T, repo, digest string) *v1alpha1.PromotionReceipt {
	t.Helper()
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-5-0", Namespace: "payments", UID: "u1"},
		Spec:       v1alpha1.BundleSpec{Type: "image", Images: []v1alpha1.ImageRef{{Repository: repo, Digest: digest}}},
	}
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Namespace: "payments"},
		Spec:       v1alpha1.PromotionStepSpec{PipelineName: "checkout", BundleName: b.Name, Environment: "prod"},
	}
	stmt, d, err := receipt.Build(receipt.Input{Name: "checkout-1-5-0-prod", ClusterID: "c1", Now: now, Bundle: b, Step: ps})
	require.NoError(t, err)
	return &v1alpha1.PromotionReceipt{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-1-5-0-prod", Namespace: "payments", Labels: map[string]string{
			"kardinal.io/pipeline": "checkout", "kardinal.io/bundle": b.Name, "kardinal.io/environment": "prod"}},
		Spec: v1alpha1.PromotionReceiptSpec{Statement: string(stmt), StatementDigest: d,
			PullRequest: &v1alpha1.ReceiptPullRequest{Repo: "acme/deploy", Number: 412}},
	}
}

func keySecret(t *testing.T) (*corev1.Secret, []byte) {
	t.Helper()
	priv, pub, err := receipttest.Key()
	require.NoError(t, err)
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "receipt-key", Namespace: "kardinal-system"},
		Data: map[string][]byte{"cosign.key": priv}}, pub
}

type harness struct {
	c   client.Client
	r   *Reconciler
	scm *commentSCM
}

func newHarness(t *testing.T, objs ...client.Object) harness {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.PromotionReceipt{}).Build()
	s := &commentSCM{}
	r := &Reconciler{
		Client: c, SCM: s, NowFn: func() time.Time { return now },
		Signer: &receipt.Signer{Mode: v1alpha1.ReceiptSigningKey, Key: SecretKey(c, "kardinal-system", "receipt-key", "cosign.key")},
	}
	return harness{c: c, r: r, scm: s}
}

func (h harness) run(t *testing.T, times int) *v1alpha1.PromotionReceipt {
	t.Helper()
	for i := 0; i < times; i++ {
		_, err := h.r.Reconcile(context.Background(), ctrlruntime.Request{NamespacedName: types.NamespacedName{
			Namespace: "payments", Name: "checkout-1-5-0-prod"}})
		require.NoError(t, err)
	}
	var rc v1alpha1.PromotionReceipt
	require.NoError(t, h.c.Get(context.Background(), types.NamespacedName{Namespace: "payments", Name: "checkout-1-5-0-prod"}, &rc))
	return &rc
}

// TestReconcile_StaticKey: a receipt is signed once with the key from the
// Secret, stored in the CR, and the PR gets one comment naming the receipt
// and the verify command. Re-running writes nothing new: the bundle stays
// the same bytes and no second comment is posted.
//
// Covers RECEIPT-SIGN-01, RECEIPT-PR-01.
func TestReconcile_StaticKey(t *testing.T) {
	secret, pub := keySecret(t)
	h := newHarness(t, secret, newReceipt(t, "ghcr.io/acme/checkout", "sha256:"+strings.Repeat("a", 64)))
	rc := h.run(t, 3)
	assert.Equal(t, v1alpha1.ReceiptStored, rc.Status.Phase)
	require.NotEmpty(t, rc.Status.Bundle)
	assert.Equal(t, &v1alpha1.ReceiptSigner{Mode: "key", Identity: "kardinal-system/receipt-key", KeyHint: rc.Status.SignedBy.KeyHint}, rc.Status.SignedBy)
	require.Len(t, rc.Status.Stored, 1)
	assert.Equal(t, v1alpha1.ReceiptSinkCRD, rc.Status.Stored[0].Sink)
	assert.True(t, rc.Status.PRComment)
	require.Len(t, h.scm.comments, 1)
	assert.Contains(t, h.scm.comments[0], "acme/deploy#412: Promotion receipt: `checkout-1-5-0-prod`, signed by kardinal-system/receipt-key (key), stored in crd.")
	assert.Contains(t, h.scm.comments[0], "kardinal verify-receipt checkout prod --bundle checkout-1-5-0")

	_, err := receipt.Verify([]byte(rc.Status.Bundle), receipt.VerifyOptions{Key: pub, Statement: []byte(rc.Spec.Statement), Environment: "prod"})
	require.NoError(t, err)

	first := rc.Status.Bundle
	rc = h.run(t, 3)
	assert.Equal(t, first, rc.Status.Bundle, "signed once")
	assert.Len(t, h.scm.comments, 1, "commented once")
}

// TestReconcile_TamperedStatement: a statement that does not match its
// digest is never signed; the receipt is Failed and stays Failed.
//
// Covers RECEIPT-VERIFY-02.
func TestReconcile_TamperedStatement(t *testing.T) {
	secret, _ := keySecret(t)
	rc := newReceipt(t, "ghcr.io/acme/checkout", "sha256:"+strings.Repeat("a", 64))
	rc.Spec.Statement = strings.Replace(rc.Spec.Statement, `"prod"`, `"uat"`, 1)
	h := newHarness(t, secret, rc)
	got := h.run(t, 2)
	assert.Equal(t, v1alpha1.ReceiptFailed, got.Status.Phase)
	assert.Empty(t, got.Status.Bundle)
	assert.Equal(t, "StatementMismatch", got.Status.Conditions[0].Reason)
	assert.Empty(t, h.scm.comments)
}

// TestReconcile_SignRetry: a failed signature is retried with backoff
// (attempts, nextRetryAt), counted in kardinal_receipts_failed_total, and
// a reconcile before nextRetryAt does nothing.
//
// Covers RECEIPT-SIGN-04.
func TestReconcile_SignRetry(t *testing.T) {
	h := newHarness(t, newReceipt(t, "ghcr.io/acme/checkout", "sha256:"+strings.Repeat("a", 64))) // no key Secret
	before := testutil.ToFloat64(observability.ReceiptsFailedTotal.WithLabelValues("signing"))
	rc := h.run(t, 1)
	assert.Equal(t, v1alpha1.ReceiptPending, rc.Status.Phase)
	assert.Equal(t, 1, rc.Status.Attempts)
	require.NotNil(t, rc.Status.NextRetryAt)
	assert.Equal(t, now.Add(retryBase), rc.Status.NextRetryAt.UTC())
	assert.Equal(t, "SignFailed", rc.Status.Conditions[0].Reason)
	rc = h.run(t, 3)
	assert.Equal(t, 1, rc.Status.Attempts, "waits for nextRetryAt")
	assert.Equal(t, before+1, testutil.ToFloat64(observability.ReceiptsFailedTotal.WithLabelValues("signing")))

	h.r.NowFn = func() time.Time { return now.Add(time.Minute) }
	rc = h.run(t, 1)
	assert.Equal(t, 2, rc.Status.Attempts)
	assert.Equal(t, now.Add(time.Minute+2*retryBase), rc.Status.NextRetryAt.UTC())
	assert.Equal(t, []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, time.Hour}, []time.Duration{backoff(1), backoff(2), backoff(3), backoff(20)})
}

// TestReconcile_CommentRetry: a failed PR comment is retried and given up
// after maxCommentAttempts; the receipt stays Stored.
//
// Covers RECEIPT-PR-01.
func TestReconcile_CommentRetry(t *testing.T) {
	secret, _ := keySecret(t)
	h := newHarness(t, secret, newReceipt(t, "ghcr.io/acme/checkout", "sha256:"+strings.Repeat("a", 64)))
	h.scm.err = errors.New("502")
	rc := h.run(t, 3)
	assert.Equal(t, v1alpha1.ReceiptStored, rc.Status.Phase)
	assert.False(t, rc.Status.PRComment)
	assert.Equal(t, 1, rc.Status.Attempts)
	for i := 0; i < maxCommentAttempts; i++ {
		at := now.Add(time.Duration(i+1) * 2 * time.Hour)
		h.r.NowFn = func() time.Time { return at }
		rc = h.run(t, 1)
	}
	assert.True(t, rc.Status.PRComment)
	assert.Equal(t, "GaveUp", conditionOf(rc, ConditionPRCommented).Reason)
}

// TestReconcile_OCI: with the OCI sink, the signed receipt is pushed as a
// referrer of its image with the push Secret's credentials, recorded in
// status.stored, and read back by a verifier from the registry.
//
// Covers RECEIPT-OCI-01.
func TestReconcile_OCI(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0))))
	defer srv.Close()
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/checkout"
	img, err := random.Image(64, 1)
	require.NoError(t, err)
	ref, err := name.ParseReference(repo+":1", name.Insecure)
	require.NoError(t, err)
	require.NoError(t, remote.Write(ref, img))
	d, err := img.Digest()
	require.NoError(t, err)

	secret, pub := keySecret(t)
	push := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "receipt-push", Namespace: "kardinal-system"},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{}}`)}}
	h := newHarness(t, secret, push, newReceipt(t, repo, d.String()))
	h.r.OCI = &OCISink{Registry: receipt.Registry{Insecure: true}, SecretNamespace: "kardinal-system", SecretName: "receipt-push"}
	rc := h.run(t, 4)
	assert.Equal(t, v1alpha1.ReceiptStored, rc.Status.Phase)
	require.Len(t, rc.Status.Stored, 2)
	assert.Equal(t, v1alpha1.ReceiptSinkOCI, rc.Status.Stored[1].Sink)
	assert.True(t, strings.HasPrefix(rc.Status.Stored[1].Ref, repo+"@sha256:"))
	assert.Contains(t, h.scm.comments[0], "stored in crd, oci "+repo+"@sha256:")

	got, err := (&receipt.Registry{Insecure: true}).Referrers(context.Background(), repo, d.String())
	require.NoError(t, err)
	require.Len(t, got, 1)
	_, err = receipt.Verify(got[0], receipt.VerifyOptions{Key: pub, Subject: d.String()})
	require.NoError(t, err)

	t.Run("missing push Secret is retried", func(t *testing.T) {
		h := newHarness(t, secret, newReceipt(t, repo, d.String()))
		h.r.OCI = &OCISink{Registry: receipt.Registry{Insecure: true}, SecretNamespace: "kardinal-system", SecretName: "gone"}
		rc := h.run(t, 2)
		assert.Equal(t, v1alpha1.ReceiptSigned, rc.Status.Phase)
		assert.Equal(t, "OCICredentials", conditionOf(rc, ConditionStored).Reason)
		assert.Equal(t, 1, rc.Status.Attempts)
	})
}

func conditionOf(rc *v1alpha1.PromotionReceipt, typ string) metav1.Condition {
	for _, c := range rc.Status.Conditions {
		if c.Type == typ {
			return c
		}
	}
	return metav1.Condition{}
}
