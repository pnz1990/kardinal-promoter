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

	"github.com/sigstore/sigstore-go/pkg/root"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	// deadline is the deadline of the last call's context.
	deadline time.Time
	// hang blocks each call until its context ends.
	hang bool
}

func (f *fakeRegistry) Signatures(ctx context.Context, _, digest string, _ iv.RegistryOptions) (iv.Signatures, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.deadline, _ = ctx.Deadline()
	if f.hang {
		<-ctx.Done()
		return iv.Signatures{}, ctx.Err()
	}
	if f.err != nil {
		return iv.Signatures{}, f.err
	}
	return f.sigs[digest], nil
}

// fakeSCM verifies commits from a table.
type fakeSCM struct {
	scm.SCMProvider
	sig  scm.CommitSignature
	err  error
	hang bool
}

func (f *fakeSCM) VerifyCommit(ctx context.Context, _, _ string) (scm.CommitSignature, error) {
	if f.hang {
		<-ctx.Done()
		return scm.CommitSignature{}, ctx.Err()
	}
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
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
		Labels: map[string]string{iv.LabelReferenceable: "true"}}, Data: map[string][]byte{"cosign.pub": pem}}
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
	// A status write with a done context fails, as with a real API server:
	// the reconciler must write status with the reconcile context, not
	// the check deadline.
	h.c = fake.NewClientBuilder().WithScheme(scheme(t)).WithStatusSubresource(&v1alpha1.ImageVerification{}).WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
			patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
		}}).Build()
	h.r = &iv.Reconciler{Client: h.c, Registry: h.reg, SCM: s, SCMHost: "github.com", NowFn: func() time.Time { return h.now }}
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

// TestImageVerification_WrongKeyWaitsThenFails: a signature that is
// present but not ours (another key's) is no verdict: the check waits for a
// verifying one until the deadline, then fails with SignatureNotVerified
// (QA #1521: an image may carry other signers' signatures before ours).
func TestImageVerification_WrongKeyWaitsThenFails(t *testing.T) {
	sigs, _ := signed(t)
	_, otherPEM := signed(t)
	h := newHarness(t, nil, newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, "5m"), keySecret("cosign", otherPEM))
	h.reg.sigs[digestA] = sigs
	got, res := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase)
	assert.Equal(t, iv.ReasonSignatureNotVerified, got.Status.Reason)
	assert.Contains(t, got.Status.Message, "no signature verifies against the policy's authorities yet")
	assert.Contains(t, got.Status.Images[0].Message, "authority release")
	assert.Positive(t, res.RequeueAfter)

	// Our signature arrives: Verified.
	ours, pem := signed(t)
	h2 := newHarness(t, nil, newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, "5m"), keySecret("cosign", pem))
	h2.reg.sigs[digestA] = sigs
	got, _ = h2.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase)
	h2.reg.sigs[digestA] = iv.Signatures{Bundles: append(sigs.Bundles, ours.Bundles...)}
	got, _ = h2.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)

	h.now = h.now.Add(6 * time.Minute)
	got, _ = h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationFailed, got.Status.Phase)
	assert.Equal(t, iv.ReasonSignatureNotVerified, got.Status.Reason)
	assert.Contains(t, got.Status.Message, "not verified within the 5m0s timeout")
}

// TestImageVerification_LegacyWrongDigestFails: a cosign .sig signature
// made with the policy's key for another digest is ours and wrong: it fails
// at once.
func TestImageVerification_LegacyWrongDigestFails(t *testing.T) {
	key, err := signtest.NewKey()
	require.NoError(t, err)
	pem, err := key.PublicPEM()
	require.NoError(t, err)
	payload := signtest.LegacyPayload(repo, "sha256:"+strings.Repeat("b", 64))
	sig, err := key.SignLegacy(payload)
	require.NoError(t, err)
	h := newHarness(t, nil, newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, "5m"), keySecret("cosign", pem))
	h.reg.sigs[digestA] = iv.Signatures{Legacy: []iv.LegacySignature{{Payload: payload, Signature: sig}}}
	got, _ := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationFailed, got.Status.Phase)
	assert.Equal(t, iv.ReasonSignatureNotVerified, got.Status.Reason)
	assert.Contains(t, got.Status.Message, "the signature is for sha256:bbbb")
}

// TestImageVerification_SecretsMustBeReferenceable: the key, trusted root
// and registry Secrets need kardinal.io/referenceable: "true"; without it
// the check waits with reason SecretNotReferenceable and never reads the
// content (QA #1521).
func TestImageVerification_SecretsMustBeReferenceable(t *testing.T) {
	sigs, pem := signed(t)
	unlabelled := keySecret("cosign", pem)
	unlabelled.Labels = nil
	h := newHarness(t, nil, newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, ""), unlabelled)
	h.reg.sigs[digestA] = sigs
	got, _ := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase)
	assert.Equal(t, iv.ReasonSecretNotReferenceable, got.Status.Reason)
	assert.Contains(t, got.Status.Message, `secret cosign does not have the label kardinal.io/referenceable: "true"`)
	assert.Zero(t, h.reg.calls, "no registry call without the key")

	// Labelled later: used.
	unlabelled.Labels = map[string]string{iv.LabelReferenceable: "true"}
	require.NoError(t, h.c.Update(context.Background(), unlabelled))
	got, _ = h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)

	// Registry credentials and trusted roots too.
	reg := newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, "")
	reg.Spec.Policy.RegistrySecretRef = &v1alpha1.LocalObjectName{Name: "pull"}
	pull := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "pull", Namespace: ns},
		Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{}}`)}}
	h = newHarness(t, nil, reg, keySecret("cosign", pem), pull)
	got, _ = h.reconcile()
	assert.Equal(t, iv.ReasonSecretNotReferenceable, got.Status.Reason)
	assert.Contains(t, got.Status.Message, "registrySecretRef: SecretNotReferenceable: secret pull")
}

// TestImageVerification_SecretContentNeverEchoed: a key, trusted root or
// .dockerconfigjson that does not parse is reported by Secret and key name
// with a generic message; nothing of its content (or the parser's error,
// which can quote it) is in the status (QA #1521).
func TestImageVerification_SecretContentNeverEchoed(t *testing.T) {
	const secretText = "TOP-SECRET-VALUE-1234"
	labels := map[string]string{iv.LabelReferenceable: "true"}
	cases := []struct {
		name   string
		mutate func(*v1alpha1.ImageVerification)
		secret *corev1.Secret
		reason string
		want   string
	}{
		{"public key", func(*v1alpha1.ImageVerification) {},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cosign", Namespace: ns, Labels: labels},
				Data: map[string][]byte{"cosign.pub": []byte("-----BEGIN PUBLIC KEY-----\n" + secretText + "\n-----END PUBLIC KEY-----\n")}},
			iv.ReasonInvalidPublicKey, "secret cosign key cosign.pub is not a PEM public key"},
		{"trusted root", func(v *v1alpha1.ImageVerification) {
			v.Spec.Policy.Authorities = []v1alpha1.SignatureAuthority{{Name: "ci", Keyless: &v1alpha1.KeylessAuthority{
				Issuer: "https://issuer", Subject: "s", TrustedRootRef: &v1alpha1.SecretKeyName{Name: "root", Key: "trusted_root.json"}}}}
		}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "root", Namespace: ns, Labels: labels},
			Data: map[string][]byte{"trusted_root.json": []byte(`{"mediaType": "` + secretText + `"`)}},
			iv.ReasonInvalidTrustedRoot, "secret root key trusted_root.json is not a valid Sigstore trusted root"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, "")
			tc.mutate(v)
			h := newHarness(t, nil, v, tc.secret)
			got, _ := h.reconcile()
			assert.Equal(t, tc.reason, got.Status.Reason)
			assert.Contains(t, got.Status.Message, tc.want)
			assert.NotContains(t, got.Status.Message, secretText)
			assert.NotContains(t, got.Status.Images[0].Message, secretText)
		})
	}

	_, pem := signed(t)
	v := newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, "")
	v.Spec.Policy.RegistrySecretRef = &v1alpha1.LocalObjectName{Name: "pull"}
	pull := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "pull", Namespace: ns, Labels: labels},
		Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths": ` + secretText)}}
	h := newHarness(t, nil, v, keySecret("cosign", pem), pull)
	got, _ := h.reconcile()
	assert.Equal(t, iv.ReasonInvalidRegistryCredentials, got.Status.Reason)
	assert.Contains(t, got.Status.Message, "secret pull is not a valid .dockerconfigjson")
	assert.NotContains(t, got.Status.Message, secretText)
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
	assert.Contains(t, got.Status.Message, "secret cosign not found")
	assert.Equal(t, iv.ReasonSecretNotFound, got.Status.Reason)
	require.NoError(t, h.c.Create(context.Background(), keySecret("cosign", pem)))
	got, _ = h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase)
}

// TestImageVerification_Commits: a config commit must be signed with a
// signature the SCM verified, for that exact commit, on the controller's SCM
// host, by an allowed signer when the policy lists them (QA #1521).
func TestImageVerification_Commits(t *testing.T) {
	sha := strings.Repeat("abc1", 10)
	commit := &v1alpha1.VerifiedCommit{Repo: "https://github.com/org/config", SHA: sha}
	ok := func(identities ...string) scm.CommitSignature {
		return scm.CommitSignature{Verified: true, Signer: identities[0], SHA: sha, Identities: identities}
	}
	platform := func(id string) scm.CommitSignature {
		return scm.CommitSignature{Verified: true, Signer: id, SHA: sha, Identities: []string{id}, Platform: true}
	}
	cases := []struct {
		name    string
		scm     scm.SCMProvider
		commit  *v1alpha1.VerifiedCommit
		allowed []string
		phase   string
		msg     string
	}{
		{name: "verified", scm: &fakeSCM{sig: ok("alice", "alice@example.com")}, phase: v1alpha1.ImageVerificationVerified, msg: "commit " + sha + " signed by alice"},
		{name: "allowed signer by email", scm: &fakeSCM{sig: ok("alice", "Alice@Example.com")}, allowed: []string{"alice@example.com"}, phase: v1alpha1.ImageVerificationVerified, msg: "signed by alice"},
		{name: "signer not allowed", scm: &fakeSCM{sig: ok("mallory", "m@example.com")}, allowed: []string{"alice"}, phase: v1alpha1.ImageVerificationFailed, msg: "who is not in commits.allowedSigners"},
		{name: "web-flow refused by default", scm: &fakeSCM{sig: platform(scm.PlatformSignerGitHub)}, phase: v1alpha1.ImageVerificationFailed, msg: `signed by the SCM platform (web-flow`},
		{name: "web-flow refused with other signers listed", scm: &fakeSCM{sig: platform(scm.PlatformSignerGitHub)}, allowed: []string{"alice"}, phase: v1alpha1.ImageVerificationFailed, msg: `list "web-flow" in commits.allowedSigners`},
		{name: "web-flow allowed when listed", scm: &fakeSCM{sig: platform(scm.PlatformSignerGitHub)}, allowed: []string{"web-flow"}, phase: v1alpha1.ImageVerificationVerified, msg: "signed by web-flow"},
		{name: "gitlab-system refused by default", scm: &fakeSCM{sig: platform(scm.PlatformSignerGitLab)}, phase: v1alpha1.ImageVerificationFailed, msg: "signed by the SCM platform (gitlab-system"},
		{name: "forgejo instance key refused by default", scm: &fakeSCM{sig: platform(scm.PlatformSignerForgejo)}, phase: v1alpha1.ImageVerificationFailed, msg: "signed by the SCM platform (forgejo-instance"},
		{name: "short SHA", scm: &fakeSCM{sig: ok("alice")}, commit: &v1alpha1.VerifiedCommit{Repo: commit.Repo, SHA: "abc123"}, phase: v1alpha1.ImageVerificationFailed, msg: "not a full 40- or 64-character commit SHA"},
		{name: "SCM answered for another commit", scm: &fakeSCM{sig: scm.CommitSignature{Verified: true, Signer: "alice", SHA: strings.Repeat("f", 40)}}, phase: v1alpha1.ImageVerificationFailed, msg: "the SCM answered for commit"},
		{name: "repository on another host", scm: &fakeSCM{sig: ok("alice")}, commit: &v1alpha1.VerifiedCommit{Repo: "https://gitlab.example/org/config", SHA: sha}, phase: v1alpha1.ImageVerificationFailed, msg: `is on gitlab.example, not on the controller's SCM host "github.com"`},
		{name: "unsigned", scm: &fakeSCM{sig: scm.CommitSignature{Reason: "unsigned", SHA: sha}}, phase: v1alpha1.ImageVerificationFailed, msg: "not signed with a verified signature (unsigned)"},
		{name: "unknown key", scm: &fakeSCM{sig: scm.CommitSignature{Reason: "unknown_key", SHA: sha}}, phase: v1alpha1.ImageVerificationFailed, msg: "unknown_key"},
		{name: "SCM outage", scm: &fakeSCM{err: errors.New("502 bad gateway")}, phase: v1alpha1.ImageVerificationPending, msg: "SCM: 502"},
		{name: "commit not found", scm: &fakeSCM{err: &scm.APIError{Provider: "GitHub", StatusCode: 404}}, phase: v1alpha1.ImageVerificationFailed, msg: "status 404"},
		{name: "provider cannot verify", scm: &plainSCM{}, phase: v1alpha1.ImageVerificationFailed, msg: "does not report commit signatures"},
		{name: "dynamic provider without support", scm: &fakeSCM{err: scm.ErrCommitVerificationUnsupported}, phase: v1alpha1.ImageVerificationFailed, msg: "does not report commit signatures"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := commit
			if tc.commit != nil {
				c = tc.commit
			}
			v := newIV(nil, c, "")
			v.Spec.Policy.Commits.AllowedSigners = tc.allowed
			h := newHarness(t, tc.scm, v)
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

// TestImageVerification_RegistryCallsAreTimeBounded: every registry fetch
// runs under a deadline (QA #1521: a registry that never answered held the
// worker forever).
func TestImageVerification_RegistryCallsAreTimeBounded(t *testing.T) {
	_, pem := signed(t)
	h := newHarness(t, nil, newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, ""), keySecret("cosign", pem))
	start := time.Now()
	h.reconcile()
	require.False(t, h.reg.deadline.IsZero(), "the fetch has a deadline")
	assert.LessOrEqual(t, h.reg.deadline.Sub(start), time.Minute)
}

// TestImageVerification_HungRegistryTimesOut: a registry that never answers
// is cut at the per-fetch timeout; the check records it and retries, and
// the status write still happens (QA #1521 round 2).
func TestImageVerification_HungRegistryTimesOut(t *testing.T) {
	_, pem := signed(t)
	h := newHarness(t, nil, newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, ""), keySecret("cosign", pem))
	h.reg.hang = true
	h.r.RegistryTimeout = 50 * time.Millisecond
	start := time.Now()
	got, res := h.reconcile()
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase)
	assert.Contains(t, got.Status.Message, "context deadline exceeded")
	assert.Positive(t, res.RequeueAfter)
}

// TestImageVerification_HungSCMTimesOut: an SCM that never answers is cut
// at the check timeout; the commit check retries and the status is written.
func TestImageVerification_HungSCMTimesOut(t *testing.T) {
	commit := &v1alpha1.VerifiedCommit{Repo: "https://github.com/org/config", SHA: strings.Repeat("abc1", 10)}
	h := newHarness(t, &fakeSCM{hang: true}, newIV(nil, commit, ""))
	h.r.CheckTimeout = 50 * time.Millisecond
	start := time.Now()
	got, res := h.reconcile()
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase)
	assert.Contains(t, got.Status.Message, "SCM: context deadline exceeded")
	assert.Positive(t, res.RequeueAfter)
}

// TestImageVerification_StatusWrittenAfterCheckDeadline: when the checks
// run out their deadline, the status is still written (with the reconcile
// context): the harness's client fails a status write whose context is done.
func TestImageVerification_StatusWrittenAfterCheckDeadline(t *testing.T) {
	commit := &v1alpha1.VerifiedCommit{Repo: "https://github.com/org/config", SHA: strings.Repeat("abc1", 10)}
	h := newHarness(t, &fakeSCM{hang: true}, newIV(nil, commit, ""))
	h.r.CheckTimeout = 20 * time.Millisecond
	got, _ := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase, "the status write succeeded")
	assert.Contains(t, got.Status.Message, "context deadline exceeded")
}

// TestImageVerification_TrustedRootUsesReconcilerClock: the public-good
// root is asked for with the reconciler's clock (NowFn), which ages the
// cached root.
func TestImageVerification_TrustedRootUsesReconcilerClock(t *testing.T) {
	sigs, _ := signed(t)
	v := newIV([]v1alpha1.VerifiedImage{{Repository: repo, Digest: digestA}}, nil, "")
	v.Spec.Policy.Authorities = []v1alpha1.SignatureAuthority{{Name: "ci", Keyless: &v1alpha1.KeylessAuthority{
		Issuer: "https://issuer", Subject: "s"}}}
	h := newHarness(t, nil, v)
	h.reg.sigs[digestA] = sigs
	var asked []time.Time
	h.r.PublicGoodRoot = func(_ context.Context, now time.Time) (root.TrustedMaterial, error) {
		asked = append(asked, now)
		return nil, errors.New("offline")
	}
	h.reconcile()
	require.NotEmpty(t, asked)
	assert.Equal(t, h.now, asked[0])
}

// TestImageVerification_InstanceSigners: a commit signed by an
// operator-configured instance identity (--scm-instance-signers) is a
// platform signature even when a user of that name exists: refused unless
// allowedSigners lists forgejo-instance.
func TestImageVerification_InstanceSigners(t *testing.T) {
	sha := strings.Repeat("abc1", 10)
	commit := &v1alpha1.VerifiedCommit{Repo: "https://github.com/org/config", SHA: sha}
	sig := scm.CommitSignature{Verified: true, Signer: "forgejo-bot", SHA: sha, Identities: []string{"forgejo-bot", "bot@forgejo.example"}}
	for _, tc := range []struct {
		allowed []string
		phase   string
	}{{nil, v1alpha1.ImageVerificationFailed}, {[]string{"forgejo-bot"}, v1alpha1.ImageVerificationFailed},
		{[]string{scm.PlatformSignerForgejo}, v1alpha1.ImageVerificationVerified}} {
		v := newIV(nil, commit, "")
		v.Spec.Policy.Commits.AllowedSigners = tc.allowed
		h := newHarness(t, &fakeSCM{sig: sig}, v)
		h.r.InstanceSigners = []string{"BOT@forgejo.example"}
		got, _ := h.reconcile()
		assert.Equal(t, tc.phase, got.Status.Phase, "%v: %s", tc.allowed, got.Status.Message)
	}
}
