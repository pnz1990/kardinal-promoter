// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	iv "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

const (
	ns   = "team-a"
	repo = "registry.example/app"
)

var digestA = "sha256:" + strings.Repeat("a", 64)

// fakeRegistry serves signatures by digest and counts calls.
type fakeRegistry struct {
	mu    sync.Mutex
	sigs  map[string]iv.Signatures
	err   error
	calls int
}

func (f *fakeRegistry) Signatures(_ context.Context, _, digest string, _ iv.RegistryOptions) (iv.Signatures, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return iv.Signatures{}, f.err
	}
	return f.sigs[digest], nil
}

// fakeSCM verifies commits from a table.
type fakeSCM struct {
	scm.SCMProvider
	sig scm.CommitSignature
	err error
}

func (f *fakeSCM) VerifyCommit(context.Context, string, string) (scm.CommitSignature, error) {
	return f.sig, f.err
}

// plainSCM implements no CommitVerifier.
type plainSCM struct{ scm.SCMProvider }

func scheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

func keySecret(name string, pem []byte) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: map[string][]byte{"cosign.pub": pem}}
}

func newIV(images []v1alpha1.VerifiedImage, commit *v1alpha1.VerifiedCommit, timeout string) *v1alpha1.ImageVerification {
	p := v1alpha1.ImageVerificationPolicy{Timeout: timeout}
	if len(images) > 0 {
		p.Authorities = []v1alpha1.SignatureAuthority{{Name: "release",
			Key: &v1alpha1.KeyAuthority{SecretRef: v1alpha1.SecretKeyName{Name: "cosign", Key: "cosign.pub"}}}}
	}
	if commit != nil {
		p.Commits = &v1alpha1.CommitSignaturePolicy{RequireSigned: true}
	}
	return &v1alpha1.ImageVerification{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v1-verify", Namespace: ns},
		Spec:       v1alpha1.ImageVerificationSpec{PipelineName: "app", BundleName: "v1", Images: images, Commit: commit, Policy: p},
	}
}

type harness struct {
	t   *testing.T
	c   client.Client
	r   *iv.Reconciler
	reg *fakeRegistry
	now time.Time
}

func newHarness(t *testing.T, s scm.SCMProvider, objs ...client.Object) *harness {
	h := &harness{t: t, reg: &fakeRegistry{sigs: map[string]iv.Signatures{}}, now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	h.c = fake.NewClientBuilder().WithScheme(scheme(t)).WithStatusSubresource(&v1alpha1.ImageVerification{}).WithObjects(objs...).Build()
	h.r = &iv.Reconciler{Client: h.c, Registry: h.reg, SCM: s, NowFn: func() time.Time { return h.now }}
	return h
}

func (h *harness) reconcile() (*v1alpha1.ImageVerification, ctrl.Result) {
	h.t.Helper()
	res, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "app-v1-verify"}})
	require.NoError(h.t, err)
	var got v1alpha1.ImageVerification
	require.NoError(h.t, h.c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "app-v1-verify"}, &got))
	return &got, res
}

func signed(t *testing.T) (iv.Signatures, []byte) {
	t.Helper()
	b, pem, err := signtest.Bundle(repo, digestA)
	require.NoError(t, err)
	return iv.Signatures{Bundles: [][]byte{b}}, pem
}

// TestImageVerification_Verified: a Sigstore bundle signed by the policy's
// key verifies the image; the result is latched and the registry is not
// asked again.
func TestImageVerification_Verified(t *testing.T) {
	sigs, pem := signed(t)
	h := newHarness(t, nil, newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, ""), keySecret("cosign", pem))
	h.reg.sigs[digestA] = sigs
	got, _ := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
	require.Len(t, got.Status.Images, 1)
	assert.True(t, got.Status.Images[0].Verified)
	assert.Equal(t, "release", got.Status.Images[0].Authority)
	assert.Contains(t, got.Status.Message, "signed by release")
	calls := h.reg.calls
	again, _ := h.reconcile()
	assert.Equal(t, got.Status, again.Status, "idempotent")
	assert.Equal(t, calls, h.reg.calls, "a terminal ImageVerification asks nothing")
}

// TestImageVerification_WaitsThenTimesOut: no signature yet keeps it
// Pending, retried with backoff, until the timeout fails it.
func TestImageVerification_WaitsThenTimesOut(t *testing.T) {
	_, pem := signed(t)
	h := newHarness(t, nil, newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, "5m"), keySecret("cosign", pem))
	got, res := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase)
	assert.Contains(t, got.Status.Message, "no signature found yet")
	assert.Equal(t, 10*time.Second, res.RequeueAfter)
	got, res = h.reconcile()
	assert.Equal(t, 20*time.Second, res.RequeueAfter, "backoff doubles")
	assert.Equal(t, 2, got.Status.Attempts)

	// Past the timeout with still no signature: Failed.
	h.now = h.now.Add(6 * time.Minute)
	got, _ = h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationFailed, got.Status.Phase)
	assert.Contains(t, got.Status.Message, "not verified within the 5m0s timeout")
}

// TestImageVerification_SignatureArrivesLater: a signature pushed after the
// first check verifies on a later one.
func TestImageVerification_SignatureArrivesLater(t *testing.T) {
	sigs, pem := signed(t)
	h := newHarness(t, nil, newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, ""), keySecret("cosign", pem))
	got, _ := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase)
	h.reg.sigs[digestA] = sigs
	got, _ = h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase)
}

// TestImageVerification_WrongKeyFails: a signature that does not verify
// fails at once (no waiting for the timeout).
func TestImageVerification_WrongKeyFails(t *testing.T) {
	sigs, _ := signed(t)
	_, otherPEM := signed(t)
	h := newHarness(t, nil, newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, ""), keySecret("cosign", otherPEM))
	h.reg.sigs[digestA] = sigs
	got, _ := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationFailed, got.Status.Phase)
	assert.Contains(t, got.Status.Message, "no signature verifies against the policy's authorities")
	assert.Contains(t, got.Status.Images[0].Message, "authority release")
}

// TestImageVerification_RegistryOutage: a registry error is retried, never
// Verified, and fails only at the timeout.
func TestImageVerification_RegistryOutage(t *testing.T) {
	_, pem := signed(t)
	h := newHarness(t, nil, newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, ""), keySecret("cosign", pem))
	h.reg.err = errors.New("connection refused")
	got, res := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase)
	assert.Contains(t, got.Status.Message, "registry: connection refused")
	assert.Positive(t, res.RequeueAfter)
}

// TestImageVerification_KeyRotation: the key Secret is read on every check:
// a missing Secret waits, and the key put in it later is used.
func TestImageVerification_KeyRotation(t *testing.T) {
	sigs, pem := signed(t)
	h := newHarness(t, nil, newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, ""))
	h.reg.sigs[digestA] = sigs
	got, _ := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase)
	assert.Contains(t, got.Status.Message, "read secret cosign")
	require.NoError(t, h.c.Create(context.Background(), keySecret("cosign", pem)))
	got, _ = h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase)
}

// TestImageVerification_Commits: a config commit must be signed with a
// signature the SCM verified.
func TestImageVerification_Commits(t *testing.T) {
	commit := &v1alpha1.VerifiedCommit{Repo: "https://github.com/org/config", SHA: "abc123"}
	cases := []struct {
		name  string
		scm   scm.SCMProvider
		phase string
		msg   string
	}{
		{"verified", &fakeSCM{sig: scm.CommitSignature{Verified: true, Signer: "alice"}}, v1alpha1.ImageVerificationVerified, "commit abc123 signed by alice"},
		{"unsigned", &fakeSCM{sig: scm.CommitSignature{Reason: "unsigned"}}, v1alpha1.ImageVerificationFailed, "not signed with a verified signature (unsigned)"},
		{"unknown key", &fakeSCM{sig: scm.CommitSignature{Reason: "unknown_key"}}, v1alpha1.ImageVerificationFailed, "unknown_key"},
		{"SCM outage", &fakeSCM{err: errors.New("502 bad gateway")}, v1alpha1.ImageVerificationPending, "SCM: 502"},
		{"commit not found", &fakeSCM{err: &scm.APIError{Provider: "GitHub", StatusCode: 404}}, v1alpha1.ImageVerificationFailed, "status 404"},
		{"provider cannot verify", &plainSCM{}, v1alpha1.ImageVerificationFailed, "does not report commit signatures"},
		{"dynamic provider without support", &fakeSCM{err: scm.ErrCommitVerificationUnsupported}, v1alpha1.ImageVerificationFailed, "does not report commit signatures"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.scm, newIV(nil, commit, ""))
			got, _ := h.reconcile()
			assert.Equal(t, tc.phase, got.Status.Phase, got.Status.Message)
			assert.Contains(t, got.Status.Message, tc.msg)
		})
	}
}

// TestImageVerification_Gone: a deleted ImageVerification is no error.
func TestImageVerification_Gone(t *testing.T) {
	h := newHarness(t, nil)
	_, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "nope"}})
	require.NoError(t, err)
}
