//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// signatures is a test's signature repository in the suite's registry:
// signatures are pushed from the test process (host) and read by the
// controller (in cluster), as with cosign's COSIGN_REPOSITORY.
type signatures struct {
	host, inCluster string
}

func newSignatures(t *testing.T, ns string) signatures {
	t.Helper()
	r := framework.NewRegistry(t)
	repo := "e2e/signatures-" + ns[len(ns)-8:]
	return signatures{host: r.HostRepository(repo), inCluster: r.Repository(repo)}
}

func (s signatures) registryHost() string { return strings.SplitN(s.inCluster, "/", 2)[0] }

func (s signatures) pushBundle(t *testing.T, digest string, bundle []byte) {
	t.Helper()
	require.NoError(t, signtest.PushBundle(context.Background(), s.host, digest, bundle,
		[]name.Option{name.Insecure}, remote.WithTransport(http.DefaultTransport)))
}

func (s signatures) pushLegacy(t *testing.T, digest string, payload, sig []byte) {
	t.Helper()
	require.NoError(t, signtest.PushLegacy(context.Background(), s.host, digest, payload, sig,
		[]name.Option{name.Insecure}, remote.WithTransport(http.DefaultTransport)))
}

// keyPolicy is an image policy with one key authority whose PEM public key
// is in Secret cosign (created here), signatures in s.
func keyPolicy(t *testing.T, e *framework.Env, ns string, s signatures, pem []byte, timeout string) *v1alpha1.ImageVerificationPolicy {
	t.Helper()
	require.NoError(t, e.Client.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cosign", Namespace: ns,
			Labels: map[string]string{"kardinal.io/referenceable": "true"}},
		Data: map[string][]byte{"cosign.pub": pem},
	}))
	return &v1alpha1.ImageVerificationPolicy{
		Authorities: []v1alpha1.SignatureAuthority{{Name: "release",
			Key: &v1alpha1.KeyAuthority{SecretRef: v1alpha1.SecretKeyName{Name: "cosign", Key: "cosign.pub"}}}},
		SignatureRepository: s.inCluster,
		InsecureRegistries:  []string{s.registryHost()},
		Timeout:             timeout,
	}
}

// imageVerification reads a Bundle's ImageVerification (its name carries a
// hash of its spec), the one its root step waits for.
func imageVerification(ctx context.Context, e *framework.Env, ns, bundle string) (*v1alpha1.ImageVerification, error) {
	var list v1alpha1.ImageVerificationList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": bundle}); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no ImageVerification for %s yet", bundle)
	}
	newest := list.Items[0]
	for _, iv := range list.Items[1:] {
		if newest.CreationTimestamp.Before(&iv.CreationTimestamp) {
			newest = iv
		}
	}
	return &newest, nil
}

func waitImageVerification(t *testing.T, e *framework.Env, ns, bundle, phase string) *v1alpha1.ImageVerification {
	t.Helper()
	var got *v1alpha1.ImageVerification
	framework.Eventually(t, 3*time.Minute, "ImageVerification "+phase, func(ctx context.Context) (bool, string) {
		iv, err := imageVerification(ctx, e, ns, bundle)
		if err != nil {
			return false, err.Error()
		}
		got = iv
		if p := iv.Status.Phase; p != phase && (p == "Verified" || p == "Failed") {
			t.Fatalf("ImageVerification is %s, want %s: %s", p, phase, iv.Status.Message)
		}
		return iv.Status.Phase == phase, fmt.Sprintf("phase=%q message=%q", iv.Status.Phase, iv.Status.Message)
	})
	return got
}

// TestStep_ImageVerificationPromotesSigned signs podinfo's digest with a
// key into a Sigstore bundle, attached as an OCI referrer in the suite's
// registry, and promotes it by digest through test and prod with a policy
// naming that key: the Bundle's ImageVerification is Verified (signed by the
// release authority) and both environments run the digest.
//
// Covers IMGV-KEY-01, IMGV-SIGREPO-01.
func TestStep_ImageVerificationPromotesSigned(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	s := newSignatures(t, a.ns)
	bundleJSON, pem, err := signtest.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	s.pushBundle(t, fixtures.V2Digest, bundleJSON)
	p := a.pipeline(nil)
	p.Spec.ImageVerification = keyPolicy(t, e, a.ns, s, pem, "")
	a.apply(t, p)
	byDigest := fixtures.Image + "@" + fixtures.V2Digest
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", byDigest)

	iv := waitImageVerification(t, e, a.ns, bundle, "Verified")
	require.Len(t, iv.Status.Images, 1)
	assert.Equal(t, byDigest, iv.Status.Images[0].Image)
	assert.Equal(t, "release", iv.Status.Images[0].Authority)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("prod"), byDigest, syncTimeout)
}

// TestStep_ImageVerificationLegacySignature verifies a cosign v2-style
// signature (the sha256-<digest>.sig tag) made with the policy's key.
//
// Covers IMGV-LEGACY-01.
func TestStep_ImageVerificationLegacySignature(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	s := newSignatures(t, a.ns)
	key, err := signtest.NewKey()
	require.NoError(t, err)
	payload := signtest.LegacyPayload(fixtures.Image, fixtures.V2Digest)
	sig, err := key.SignLegacy(payload)
	require.NoError(t, err)
	s.pushLegacy(t, fixtures.V2Digest, payload, sig)
	pem, err := key.PublicPEM()
	require.NoError(t, err)
	p := a.pipeline(nil)
	p.Spec.ImageVerification = keyPolicy(t, e, a.ns, s, pem, "")
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest)
	waitImageVerification(t, e, a.ns, bundle, "Verified")
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
}

// TestStep_ImageVerificationBlocksUnsigned: with no signature the first
// environment's step waits in Pending (nothing in git, nothing deployed)
// until the policy timeout; then the ImageVerification, the step and the
// Bundle fail.
//
// Covers IMGV-UNSIGNED-01.
func TestStep_ImageVerificationBlocksUnsigned(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	s := newSignatures(t, a.ns)
	_, pem, err := signtest.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	p := a.pipeline(nil)
	p.Spec.ImageVerification = keyPolicy(t, e, a.ns, s, pem, "40s")
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest)

	framework.Eventually(t, time.Minute, "test waits for the verification", func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || !ok {
			return false, fmt.Sprintf("%v %v", ok, err)
		}
		return ps.Status.State == "" && strings.Contains(ps.Status.Message, "no signature found yet"),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	a.fileHas(t, "test", fixtures.V1, "test in git while unverified")
	iv := waitImageVerification(t, e, a.ns, bundle, "Failed")
	assert.Contains(t, iv.Status.Message, "not verified within the 40s timeout")
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", time.Minute)
	assert.Contains(t, ps.Status.Message, "image verification")
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
	a.fileHas(t, "test", fixtures.V1, "test in git after the failure")
	a.running(t, "test", fixtures.Image+":"+fixtures.V1, "test after the failure")
}

// TestStep_ImageVerificationWrongKeyFails: a signature by another key is
// present but not ours: the verification waits for one that verifies until
// the timeout, then fails with reason SignatureNotVerified.
//
// Covers IMGV-WRONGKEY-01.
func TestStep_ImageVerificationWrongKeyFails(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	s := newSignatures(t, a.ns)
	signed, _, err := signtest.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	s.pushBundle(t, fixtures.V2Digest, signed)
	_, otherPEM, err := signtest.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	p := a.pipeline(nil)
	p.Spec.ImageVerification = keyPolicy(t, e, a.ns, s, otherPEM, "40s")
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest)
	iv := waitImageVerification(t, e, a.ns, bundle, "Failed")
	assert.Equal(t, "SignatureNotVerified", iv.Status.Reason)
	assert.Contains(t, iv.Status.Message, "not verified within the 40s timeout")
	assert.Contains(t, iv.Status.Message, "no signature verifies against the policy's authorities yet")
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", time.Minute)
	a.fileHas(t, "test", fixtures.V1, "test in git")
}

// TestStep_ImageVerificationNeedsDigest: an image policy refuses a Bundle
// that names its image by tag (InvalidSpec, GraphBuildFailed).
//
// Covers IMGV-DIGEST-01.
func TestStep_ImageVerificationNeedsDigest(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	s := newSignatures(t, a.ns)
	_, pem, err := signtest.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	p := a.pipeline(nil)
	p.Spec.ImageVerification = keyPolicy(t, e, a.ns, s, pem, "")
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitBundle(t, a.ns, bundle, time.Minute, "Failed with InvalidSpec",
		failedWith("GraphBuildFailed", "is not pinned by digest"))
	_, ok, err := e.Step(context.Background(), a.ns, pipelineName, bundle, "test")
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestStep_ImageVerificationUnsignedCommit: with commits.requireSigned, a
// config Bundle whose commit is not signed (a user pushed it unsigned)
// fails before its first environment changes.
//
// Covers IMGV-COMMIT-01.
func TestStep_ImageVerificationUnsignedCommit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireGiteaFamily(t, e)
	a := newArgoApp(t, e, "test")
	p := a.pipeline(nil)
	p.Spec.ImageVerification = &v1alpha1.ImageVerificationPolicy{
		Commits: &v1alpha1.CommitSignaturePolicy{RequireSigned: true}}
	a.apply(t, p)
	cfg, _ := a.configRepo(t, "test")
	sha, err := gitserver.CommitAs(context.Background(), e.Git, cfg, "nosig-"+a.ns[len(a.ns)-8:], "README.md", []byte("unsigned\n"), false)
	require.NoError(t, err)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha, "--config-repo", cfg.CloneURL)
	iv := waitImageVerification(t, e, a.ns, bundle, "Failed")
	assert.Contains(t, iv.Status.Message, "is not signed with a verified signature")
	require.NotNil(t, iv.Status.Commit)
	assert.False(t, iv.Status.Commit.Verified)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", time.Minute)
	assert.Contains(t, ps.Status.Message, "image verification")
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)
}

// requireGiteaFamily skips a test that needs Forgejo or Gitea users and GPG
// keys (CommitAs).
func requireGiteaFamily(t *testing.T, e *framework.Env) {
	t.Helper()
	if k := e.Git.Kind(); k != "forgejo" && k != "gitea" {
		t.Skipf("needs Forgejo or Gitea, the suite's git server is %s", k)
	}
}

// TestGiteaFamily_SignedCommitPerson: on Forgejo and on Gitea (whose signer payloads
// differ: Forgejo names the login in signer.name, Gitea in
// signer.username), a config commit a user signed with a GPG key registered
// on the server is a person's signature: the Bundle verifies with
// allowedSigners naming the user, and fails when it names someone else
// (regression, QA #1521 round 3: Forgejo person signatures were taken as
// the instance key).
//
// Covers IMGV-SIGNER-01.
func TestGiteaFamily_SignedCommitPerson(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireGiteaFamily(t, e)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	signer := "signer-" + a.ns[len(a.ns)-8:]
	p := a.pipeline(nil)
	p.Spec.ImageVerification = &v1alpha1.ImageVerificationPolicy{
		Commits: &v1alpha1.CommitSignaturePolicy{RequireSigned: true, AllowedSigners: []string{signer}}}
	a.apply(t, p)
	cfg, _ := a.configRepo(t, "test")
	sha, err := gitserver.CommitAs(ctx, e.Git, cfg, signer, "README.md", []byte("signed\n"), true)
	require.NoError(t, err)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha, "--config-repo", cfg.CloneURL)
	iv := waitImageVerification(t, e, a.ns, bundle, "Verified")
	require.NotNil(t, iv.Status.Commit)
	assert.Equal(t, signer, iv.Status.Commit.Signer)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)

	// Another allowed signer: the same commit is refused.
	var live v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: pipelineName}, &live))
	live.Spec.ImageVerification.Commits.AllowedSigners = []string{"someone-else"}
	require.NoError(t, e.Client.Update(ctx, &live))
	second := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha, "--config-repo", cfg.CloneURL)
	iv = waitImageVerification(t, e, a.ns, second, "Failed")
	assert.Contains(t, iv.Status.Message, "who is not in commits.allowedSigners")
}

// TestGiteaFamily_SignedCommitInstance: a commit the server signed with its
// instance key (an API file edit by a user with a key, with
// repository.signing set up) is a
// platform signature on Forgejo and on Gitea (Gitea reports SIGNING_NAME as
// signer.username): refused by default, accepted when allowedSigners lists
// forgejo-instance (regression, QA #1521 round 3: Gitea instance
// signatures passed as a person).
//
// Covers IMGV-SIGNER-02.
func TestGiteaFamily_SignedCommitInstance(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	requireGiteaFamily(t, e)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	p := a.pipeline(nil)
	p.Spec.ImageVerification = &v1alpha1.ImageVerificationPolicy{
		Commits: &v1alpha1.CommitSignaturePolicy{RequireSigned: true}}
	a.apply(t, p)
	cfg, _ := a.configRepo(t, "test")
	sha, err := gitserver.InstanceCommitAs(ctx, e.Git, cfg, "apiuser-"+a.ns[len(a.ns)-8:], "SIGNED.md", []byte("instance-signed\n"))
	require.NoError(t, err)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha, "--config-repo", cfg.CloneURL)
	iv := waitImageVerification(t, e, a.ns, bundle, "Failed")
	assert.Contains(t, iv.Status.Message, "signed by the SCM platform (forgejo-instance")
	a.fileHas(t, "test", fixtures.V1, "test in git")

	var live v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: pipelineName}, &live))
	live.Spec.ImageVerification.Commits.AllowedSigners = []string{"forgejo-instance"}
	require.NoError(t, e.Client.Update(ctx, &live))
	second := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha, "--config-repo", cfg.CloneURL)
	iv = waitImageVerification(t, e, a.ns, second, "Verified")
	assert.Equal(t, "forgejo-instance", iv.Status.Commit.Signer)
	e.WaitStepState(t, a.ns, pipelineName, second, "test", "Verified", promoteTimeout)
}

// TestStep_ImageVerificationAttestationIsNotASignature: an attestation
// (another in-toto predicate) signed with the policy's key, attached first,
// is present but not an image signature: the verification waits; the
// cosign signature pushed later verifies it (regression, QA #1521).
//
// Covers IMGV-PREDICATE-01.
func TestStep_ImageVerificationAttestationIsNotASignature(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	s := newSignatures(t, a.ns)
	key, err := signtest.NewKey()
	require.NoError(t, err)
	attestation, err := key.BundleWithPredicate(fixtures.Image, fixtures.V2Digest, "https://slsa.dev/provenance/v1")
	require.NoError(t, err)
	s.pushBundle(t, fixtures.V2Digest, attestation)
	pem, err := key.PublicPEM()
	require.NoError(t, err)
	p := a.pipeline(nil)
	p.Spec.ImageVerification = keyPolicy(t, e, a.ns, s, pem, "")
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest)

	framework.Eventually(t, 2*time.Minute, "the attestation is no verdict", func(ctx context.Context) (bool, string) {
		iv, err := imageVerification(ctx, e, a.ns, bundle)
		if err != nil {
			return false, err.Error()
		}
		return iv.Status.Phase == "Pending" && strings.Contains(iv.Status.Message, "not a cosign image signature"),
			fmt.Sprintf("phase=%q message=%q", iv.Status.Phase, iv.Status.Message)
	})
	signature, err := key.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	s.pushBundle(t, fixtures.V2Digest, signature)
	waitImageVerification(t, e, a.ns, bundle, "Verified")
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
}

// TestStep_ImageVerificationSecretNotReferenceable: a key Secret without
// kardinal.io/referenceable: "true" is not read: the verification waits
// with reason SecretNotReferenceable, and labelling the Secret lets it
// verify (regression, QA #1521).
//
// Covers IMGV-REFERENCEABLE-01.
func TestStep_ImageVerificationSecretNotReferenceable(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	s := newSignatures(t, a.ns)
	bundleJSON, pem, err := signtest.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	s.pushBundle(t, fixtures.V2Digest, bundleJSON)
	p := a.pipeline(nil)
	p.Spec.ImageVerification = keyPolicy(t, e, a.ns, s, pem, "")
	var secret corev1.Secret
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: "cosign"}, &secret))
	secret.Labels = nil
	require.NoError(t, e.Client.Update(ctx, &secret))
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest)

	framework.Eventually(t, 2*time.Minute, "SecretNotReferenceable", func(ctx context.Context) (bool, string) {
		iv, err := imageVerification(ctx, e, a.ns, bundle)
		if err != nil {
			return false, err.Error()
		}
		return iv.Status.Phase == "Pending" && iv.Status.Reason == "SecretNotReferenceable",
			fmt.Sprintf("phase=%q reason=%q message=%q", iv.Status.Phase, iv.Status.Reason, iv.Status.Message)
	})
	a.fileHas(t, "test", fixtures.V1, "test in git while the key is not referenceable")
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: "cosign"}, &secret))
	secret.Labels = map[string]string{"kardinal.io/referenceable": "true"}
	require.NoError(t, e.Client.Update(ctx, &secret))
	waitImageVerification(t, e, a.ns, bundle, "Verified")
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
}

// TestStep_ImageVerificationPolicyChangeReverifies: a policy change while a
// root step still waits gives a new ImageVerification, and the step waits
// for that one (regression, QA #1521: the old verdict was reused).
//
// Covers IMGV-POLICYCHANGE-01.
func TestStep_ImageVerificationPolicyChangeReverifies(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	s := newSignatures(t, a.ns)
	_, pem, err := signtest.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	p := a.pipeline(nil)
	p.Spec.ImageVerification = keyPolicy(t, e, a.ns, s, pem, "")
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest)
	first := waitImageVerificationPending(t, e, a.ns, bundle)

	// Rotate to a new key (a new Secret) that did sign.
	key, err := signtest.NewKey()
	require.NoError(t, err)
	signature, err := key.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	s.pushBundle(t, fixtures.V2Digest, signature)
	newPEM, err := key.PublicPEM()
	require.NoError(t, err)
	require.NoError(t, e.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cosign-2026", Namespace: a.ns, Labels: map[string]string{"kardinal.io/referenceable": "true"}},
		Data:       map[string][]byte{"cosign.pub": newPEM}}))
	var live v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: pipelineName}, &live))
	live.Spec.ImageVerification.Authorities[0].Key.SecretRef.Name = "cosign-2026"
	require.NoError(t, e.Client.Update(ctx, &live))

	iv := waitImageVerification(t, e, a.ns, bundle, "Verified")
	assert.NotEqual(t, first, iv.Name, "a new ImageVerification for the new policy")
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, iv.Name, ps.Spec.ImageVerification)
}

// waitImageVerificationPending returns the name of a Bundle's
// ImageVerification once it is Pending.
func waitImageVerificationPending(t *testing.T, e *framework.Env, ns, bundle string) string {
	t.Helper()
	var name string
	framework.Eventually(t, 2*time.Minute, "a Pending ImageVerification", func(ctx context.Context) (bool, string) {
		iv, err := imageVerification(ctx, e, ns, bundle)
		if err != nil {
			return false, err.Error()
		}
		name = iv.Name
		return iv.Status.Phase == "Pending", "phase " + iv.Status.Phase
	})
	return name
}

// TestStep_ImageVerificationInCompactGraph runs an image policy with the
// compact Graph shape (kardinal.io/graph-shape: compact). Before the
// signature is pushed the root environment's step waits in Pending, its pre
// hook is not created (no migration for an unverified image) and test is
// unchanged in git; once the signature is in the registry the
// ImageVerification is Verified, the hook runs, and the Bundle is promoted
// through test and prod. Only the root step names the ImageVerification.
//
// Covers IMGV-COMPACT-01.
func TestStep_ImageVerificationInCompactGraph(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	s := newSignatures(t, a.ns)
	bundleJSON, pem, err := signtest.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	p := a.pipeline(nil)
	p.Annotations = map[string]string{"kardinal.io/graph-shape": "compact"}
	p.Spec.ImageVerification = keyPolicy(t, e, a.ns, s, pem, "")
	p.Spec.Environments[0].Hooks = []v1alpha1.HookSpec{{Name: "migrate", Phase: "pre", Job: hookJob(t, `echo migrated`, "")}}
	a.apply(t, p)
	byDigest := fixtures.Image + "@" + fixtures.V2Digest
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", byDigest)
	migrate := graph.HookRunName(pipelineName, bundle, "test", "pre", "migrate")

	framework.Eventually(t, time.Minute, "test waits for the verification", func(ctx context.Context) (bool, string) {
		ps, ok, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil || !ok {
			return false, fmt.Sprintf("%v %v", ok, err)
		}
		return ps.Status.State == "" && strings.Contains(ps.Status.Message, "no signature found yet"),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	assert.Equal(t, "compact", bundleGraph(t, e, a.ns, bundle).GetLabels()["kardinal.io/graph-shape"])
	_, exists, err := hookRun(ctx, e, a.ns, migrate)
	require.NoError(t, err)
	assert.False(t, exists, "no pre hook before the image is verified")
	a.fileHas(t, "test", fixtures.V1, "test in git while unverified")

	s.pushBundle(t, fixtures.V2Digest, bundleJSON)
	iv := waitImageVerification(t, e, a.ns, bundle, "Verified")
	waitHookRun(t, e, a.ns, migrate, v1alpha1.HookRunSucceeded)
	test := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, iv.Name, test.Spec.ImageVerification)
	require.NotNil(t, test.Spec.Live)
	require.NotNil(t, test.Spec.Live.ImageVerification)
	assert.Equal(t, "Verified", test.Spec.Live.ImageVerification.Phase)
	prod := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Empty(t, prod.Spec.ImageVerification, "downstream steps need nothing")
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("prod"), byDigest, syncTimeout)
}
