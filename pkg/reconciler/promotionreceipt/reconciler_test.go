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
	"crypto"
	"encoding/json"
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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrlruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
		Client: c, SCM: s, NowFn: func() time.Time { return now }, JitterFn: noJitter,
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
	signedBefore := testutil.ToFloat64(observability.ReceiptsTotal.WithLabelValues("key"))
	bytesBefore := testutil.ToFloat64(observability.ReceiptBytesTotal)
	rc := h.run(t, 3)
	assert.Equal(t, v1alpha1.ReceiptStored, rc.Status.Phase)
	require.NotEmpty(t, rc.Status.Bundle)
	// The size is recorded with the signature (#1951 N6), and counted.
	size := int64(len(rc.Spec.Statement) + len(rc.Status.Bundle))
	assert.Equal(t, size, rc.Status.Bytes)
	assert.Equal(t, signedBefore+1, testutil.ToFloat64(observability.ReceiptsTotal.WithLabelValues("key")))
	assert.Equal(t, bytesBefore+float64(size), testutil.ToFloat64(observability.ReceiptBytesTotal))
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
	assert.Equal(t, signedBefore+1, testutil.ToFloat64(observability.ReceiptsTotal.WithLabelValues("key")), "counted once")
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

	got, err := bundlesOf((&receipt.Registry{Insecure: true}).Referrers(context.Background(), repo, d.String(), nil))
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

// TestReconcile_OCIAllowedRepositories: the push credential is the
// admin's and an image's repository is the tenant's choice. With
// AllowedRepositories set, tenant A's image under an allowed prefix gets its
// referrer; tenant B's image in another repository gets none, though B's
// receipt is signed and kept: its Stored condition is False,
// RepositoryNotAllowed, and it is not retried.
//
// Covers RECEIPT-OCI-02.
func TestReconcile_OCIAllowedRepositories(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0))))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	push := func(repo string) string {
		img, err := random.Image(64, 1)
		require.NoError(t, err)
		ref, err := name.ParseReference(repo+":1", name.Insecure)
		require.NoError(t, err)
		require.NoError(t, remote.Write(ref, img))
		d, err := img.Digest()
		require.NoError(t, err)
		return d.String()
	}
	repoA, repoB := host+"/team-a/checkout", host+"/platform/base"
	dA, dB := push(repoA), push(repoB)
	secret, _ := keySecret(t)
	a := newReceipt(t, repoA, dA)
	b := newReceipt(t, repoB, dB)
	b.Namespace = "team-b"
	h := newHarness(t, secret, a, b)
	h.r.OCI = &OCISink{Registry: receipt.Registry{Insecure: true}, AllowedRepositories: []string{host + "/team-a/"}}
	get := func(ns string) *v1alpha1.PromotionReceipt {
		key := types.NamespacedName{Namespace: ns, Name: "checkout-1-5-0-prod"}
		for i := 0; i < 5; i++ {
			_, err := h.r.Reconcile(context.Background(), ctrlruntime.Request{NamespacedName: key})
			require.NoError(t, err)
		}
		var rc v1alpha1.PromotionReceipt
		require.NoError(t, h.c.Get(context.Background(), key, &rc))
		return &rc
	}

	rcA := get("payments")
	assert.Equal(t, metav1.ConditionTrue, conditionOf(rcA, ConditionStored).Status)
	got, err := bundlesOf((&receipt.Registry{Insecure: true}).Referrers(context.Background(), repoA, dA, nil))
	require.NoError(t, err)
	assert.Len(t, got, 1, "team A's image has its referrer")

	rcB := get("team-b")
	assert.Equal(t, v1alpha1.ReceiptStored, rcB.Status.Phase, "signed and kept in the CR")
	c := conditionOf(rcB, ConditionStored)
	assert.Equal(t, metav1.ConditionFalse, c.Status)
	assert.Equal(t, "RepositoryNotAllowed", c.Reason)
	assert.Contains(t, c.Message, repoB)
	assert.Zero(t, rcB.Status.Attempts, "not retried")
	for _, s := range rcB.Status.Stored {
		assert.NotEqual(t, v1alpha1.ReceiptSinkOCI, s.Sink)
	}
	got, err = bundlesOf((&receipt.Registry{Insecure: true}).Referrers(context.Background(), repoB, dB, nil))
	require.NoError(t, err)
	assert.Empty(t, got, "nothing written to a repository outside the allowlist")

	assert.True(t, (&OCISink{AllowedRepositories: []string{"ghcr.io/acme"}}).allowed("ghcr.io/acme/app"))
	assert.False(t, (&OCISink{AllowedRepositories: []string{"ghcr.io/acme"}}).allowed("ghcr.io/acme-evil/app"), "a path prefix, not a string prefix")
	assert.True(t, (&OCISink{Repository: "ghcr.io/acme/receipts", AllowedRepositories: []string{"x/"}}).allowed("anything"),
		"a fixed repository is the admin's own")

	// QA-3 B1: the repository is parsed as a registry would see it, and a
	// path a registry or proxy could normalise into another repository is
	// refused.
	sink := &OCISink{AllowedRepositories: []string{"allowed.example.com/team-a"}}
	for repo, want := range map[string]bool{
		"allowed.example.com/team-a/app":              true,
		"allowed.example.com/team-a":                  true,
		"ALLOWED.example.com/team-a/app":              true, // host names are case-insensitive
		"allowed.example.com/team-a/../team-b/x":      false,
		"allowed.example.com/team-a/./../../team-b/x": false,
		"allowed.example.com/team-a//x":               false,
		"allowed.example.com/team-a/.":                false,
		"allowed.example.com.evil.com/team-a/x":       false,
		"evil.allowed.example.com/team-a/x":           false,
		"allowed.example.com:443/team-a/x":            false,
		"allowed.example.com/team-a-evil/x":           false,
		"allowed.example.com/Team-A/x":                false, // not a valid path component
		"allowed.example.com/team-b/x":                false,
		"team-a/app":                                  false, // docker.io
	} {
		assert.Equal(t, want, sink.allowed(repo), repo)
	}
	hub := &OCISink{AllowedRepositories: []string{"docker.io/acme"}}
	assert.True(t, hub.allowed("acme/app"), "docker.io/acme and acme are one repository")
	assert.True(t, hub.allowed("index.docker.io/acme/app"))
	assert.False(t, hub.allowed("acme-evil/app"))
}

// findingSCM is a commentSCM that can search its PR's comments.
type findingSCM struct {
	commentSCM
	searches int
}

func (f *findingSCM) HasPRComment(_ context.Context, _ string, _ int, marker string) (bool, error) {
	f.searches++
	for _, c := range f.comments {
		if strings.Contains(c, marker) {
			return true, nil
		}
	}
	return false, nil
}

// TestReconcile_OnceDespiteConflicts: the status write after a signature,
// and the one after the PR comment, each meet a conflict once. The
// reconciler re-reads the receipt and records what it did: one signature
// (one certificate, one Rekor entry), one comment. A comment whose status
// write never landed is found by the search before posting, and is not
// posted again.
//
// Covers RECEIPT-SIGN-05, RECEIPT-PR-03.
func TestReconcile_OnceDespiteConflicts(t *testing.T) {
	secret, pub := keySecret(t)
	conflicts := map[string]bool{}
	inner := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(secret, newReceipt(t, "ghcr.io/acme/checkout", "sha256:"+strings.Repeat("a", 64))).
		WithStatusSubresource(&v1alpha1.PromotionReceipt{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
			patch client.Patch, opts ...client.SubResourcePatchOption) error {
			rc := obj.(*v1alpha1.PromotionReceipt)
			stage := "sign"
			if rc.Status.PRComment {
				stage = "comment"
			}
			if !conflicts[stage] {
				conflicts[stage] = true
				// Someone else's write lands first.
				var cur v1alpha1.PromotionReceipt
				require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(rc), &cur))
				cur.Annotations = map[string]string{"touched": stage}
				require.NoError(t, c.Update(ctx, &cur))
				return apierrors.NewConflict(schema.GroupResource{Group: "kardinal.io", Resource: "promotionreceipts"}, rc.Name,
					errors.New("the object has been modified"))
			}
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		}}).Build()
	signs := 0
	key := SecretKey(inner, "kardinal-system", "receipt-key", "cosign.key")
	s := &findingSCM{}
	r := &Reconciler{Client: inner, SCM: s, NowFn: func() time.Time { return now }, JitterFn: noJitter,
		Signer: &receipt.Signer{Mode: v1alpha1.ReceiptSigningKey, Key: func(ctx context.Context) (crypto.Signer, string, error) {
			signs++
			return key(ctx)
		}}}
	// Reconciled as the controller does: an error is requeued.
	run := func(times int) *v1alpha1.PromotionReceipt {
		key := types.NamespacedName{Namespace: "payments", Name: "checkout-1-5-0-prod"}
		for i := 0; i < times; i++ {
			_, _ = r.Reconcile(context.Background(), ctrlruntime.Request{NamespacedName: key})
		}
		var rc v1alpha1.PromotionReceipt
		require.NoError(t, inner.Get(context.Background(), key, &rc))
		return &rc
	}
	rc := run(6)
	assert.True(t, conflicts["sign"] && conflicts["comment"], "both writes met a conflict")
	assert.Equal(t, 1, signs, "signed once")
	assert.Len(t, s.comments, 1, "commented once")
	assert.Contains(t, s.comments[0], CommentMarker(rc))
	assert.True(t, rc.Status.PRComment)
	_, err := receipt.Verify([]byte(rc.Status.Bundle), receipt.VerifyOptions{Key: pub})
	require.NoError(t, err)

	// The comment was posted but its status write was lost: the search
	// finds it and nothing is posted.
	base := rc.DeepCopy()
	rc.Status.PRComment = false
	require.NoError(t, inner.Status().Patch(context.Background(), rc, client.MergeFrom(base)))
	rc = run(2)
	assert.Len(t, s.comments, 1, "not posted again")
	assert.True(t, rc.Status.PRComment)
	assert.Contains(t, conditionOf(rc, ConditionPRCommented).Message, "already has the receipt's comment")
}

// TestReconcile_OnceDespiteALaggingCache (#2313): the controller's cache
// lags its own status writes, as a busy API server's watch leaves it: the
// reconcile after a write still reads the receipt from before it.
// Every step is decided on the API server's copy (APIReader), so the
// receipt is signed once and commented once, no write conflicts, and no
// step requeues itself (the write's watch event starts the next). Without
// the API server read the same cache gets the receipt signed twice: the
// lag is real, and the test can fail.
//
// Covers RECEIPT-SIGN-08.
func TestReconcile_OnceDespiteALaggingCache(t *testing.T) {
	for _, tc := range []struct {
		name      string
		apiReader bool
		signs     int
	}{
		{name: "the API server's copy decides", apiReader: true, signs: 1},
		{name: "the cache decides", apiReader: false, signs: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret, pub := keySecret(t)
			// written is the receipt from before the last status write;
			// stale is what the cache returns during the reconcile after
			// that write, whose watch event arrives only once it is over.
			var written, stale *v1alpha1.PromotionReceipt
			inner := fake.NewClientBuilder().WithScheme(scheme(t)).WithObjects(secret, newReceipt(t, "ghcr.io/acme/checkout", "sha256:"+strings.Repeat("a", 64))).
				WithStatusSubresource(&v1alpha1.PromotionReceipt{}).
				WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
					patch client.Patch, opts ...client.SubResourcePatchOption) error {
					var before v1alpha1.PromotionReceipt
					if err := c.Get(ctx, client.ObjectKeyFromObject(obj), &before); err != nil {
						return err
					}
					if err := c.SubResource(sub).Patch(ctx, obj, patch, opts...); err != nil {
						return err
					}
					written = before.DeepCopy()
					return nil
				}}).Build()
			cache := interceptor.NewClient(inner, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey,
				obj client.Object, opts ...client.GetOption) error {
				if rc, ok := obj.(*v1alpha1.PromotionReceipt); ok && stale != nil {
					stale.DeepCopyInto(rc)
					return nil
				}
				return c.Get(ctx, key, obj, opts...)
			}})
			signs := 0
			key := SecretKey(inner, "kardinal-system", "receipt-key", "cosign.key")
			s := &commentSCM{}
			r := &Reconciler{Client: cache, SCM: s, NowFn: func() time.Time { return now }, JitterFn: noJitter,
				Signer: &receipt.Signer{Mode: v1alpha1.ReceiptSigningKey, Key: func(ctx context.Context) (crypto.Signer, string, error) {
					signs++
					return key(ctx)
				}}}
			if tc.apiReader {
				r.APIReader = inner
			}
			req := ctrlruntime.Request{NamespacedName: types.NamespacedName{Namespace: "payments", Name: "checkout-1-5-0-prod"}}
			for i := 0; i < 6; i++ {
				stale, written = written, nil
				res, err := r.Reconcile(context.Background(), req)
				if tc.apiReader {
					require.NoError(t, err, "reconcile %d", i)
					assert.Equal(t, ctrlruntime.Result{}, res, "reconcile %d requeues nothing", i)
				}
			}
			var rc v1alpha1.PromotionReceipt
			require.NoError(t, inner.Get(context.Background(), req.NamespacedName, &rc))
			assert.Equal(t, tc.signs, signs, "signatures made")
			if !tc.apiReader {
				return
			}
			assert.Equal(t, v1alpha1.ReceiptStored, rc.Status.Phase)
			assert.True(t, rc.Status.PRComment)
			assert.Len(t, s.comments, 1, "commented once")
			_, err := receipt.Verify([]byte(rc.Status.Bundle), receipt.VerifyOptions{Key: pub})
			require.NoError(t, err)
		})
	}
}

// TestCacheTransform (#1951 finding 5): a done receipt is cached without
// its statement and signed bundle, and the reconciler returns for it before
// parsing anything; a receipt still to be signed, stored or commented is
// cached whole. At 10,000 done receipts of the 256 KiB worst case the cache
// holds kilobytes per receipt, not the ~600 KiB the full object takes.
//
// Covers RECEIPT-SCALE-01.
func TestCacheTransform(t *testing.T) {
	big := strings.Repeat("x", v1alpha1.MaxReceiptStatementBytes)
	mk := func(phase string, commented bool) *v1alpha1.PromotionReceipt {
		rc := newReceipt(t, "ghcr.io/acme/checkout", "sha256:"+strings.Repeat("a", 64))
		rc.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "kardinal"}}
		rc.Spec.Statement, rc.Status.Bundle = big, big
		rc.Status.Phase, rc.Status.PRComment = phase, commented
		return rc
	}
	for _, tc := range []struct {
		phase     string
		commented bool
		stripped  bool
	}{
		{v1alpha1.ReceiptStored, true, true},
		{v1alpha1.ReceiptFailed, false, true},
		{v1alpha1.ReceiptStored, false, false}, // its PR comment is still to post
		{v1alpha1.ReceiptSigned, false, false},
		{v1alpha1.ReceiptPending, false, false},
	} {
		out, err := CacheTransform(mk(tc.phase, tc.commented))
		require.NoError(t, err)
		rc := out.(*v1alpha1.PromotionReceipt)
		assert.Nil(t, rc.ManagedFields)
		assert.Equal(t, tc.stripped, rc.Spec.Statement == "", "%s commented=%v", tc.phase, tc.commented)
		assert.Equal(t, tc.stripped, rc.Status.Bundle == "", "%s commented=%v", tc.phase, tc.commented)
	}

	// The reconciler reads nothing of a done receipt: a stripped one is
	// left alone, not failed for a statement that no longer matches.
	done, _ := CacheTransform(mk(v1alpha1.ReceiptStored, true))
	h := newHarness(t, done.(*v1alpha1.PromotionReceipt))
	rc := h.run(t, 2)
	assert.Equal(t, v1alpha1.ReceiptStored, rc.Status.Phase)
	assert.Empty(t, h.scm.comments)

	var total int
	for i := 0; i < 10000; i++ {
		out, err := CacheTransform(mk(v1alpha1.ReceiptStored, true))
		require.NoError(t, err)
		b, err := json.Marshal(out)
		require.NoError(t, err)
		total += len(b)
	}
	perReceipt := total / 10000
	t.Logf("10,000 done receipts of the worst case: %d bytes each in the cache, %d MiB in all", perReceipt, total>>20)
	assert.Less(t, perReceipt, 8*1024)
}

// bundlesOf is the bundles of a Referrers result.
func bundlesOf(f receipt.Found, err error) ([][]byte, error) { return f.Bundles, err }

// noJitter keeps the tests' retry times exact.
func noJitter(time.Duration) time.Duration { return 0 }
