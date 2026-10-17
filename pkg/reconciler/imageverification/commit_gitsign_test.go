// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protodsse "github.com/sigstore/protobuf-specs/gen/pb-go/dsse"
	rekorpb "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// payloadSCM returns a commit's signed payload from a table.
type payloadSCM struct {
	scm.SCMProvider
	p   scm.CommitPayload
	err error
}

func (f *payloadSCM) CommitPayload(context.Context, string, string) (scm.CommitPayload, error) {
	return f.p, f.err
}

// verifyOnlySCM reports commit signatures but no payloads (GitLab).
type verifyOnlySCM struct{ scm.SCMProvider }

func (verifyOnlySCM) VerifyCommit(context.Context, string, string) (scm.CommitSignature, error) {
	return scm.CommitSignature{}, errors.New("not called")
}

func agentIdentity(name string, ready, suspended bool, ids ...v1alpha1.AgentSigningIdentity) *v1alpha1.AgentIdentity {
	return &v1alpha1.AgentIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec:       v1alpha1.AgentIdentitySpec{Suspended: suspended, SigningIdentities: ids},
		Status:     v1alpha1.AgentIdentityStatus{Ready: ready, ObservedGeneration: 1},
	}
}

// editedAgent is a ready AgentIdentity whose status describes generation 1
// of a spec now at generation 2.
func editedAgent(name string, ids ...v1alpha1.AgentSigningIdentity) *v1alpha1.AgentIdentity {
	a := agentIdentity(name, true, false, ids...)
	a.Generation = 2
	return a
}

func gitsignIV(sha string) *v1alpha1.ImageVerification {
	return &v1alpha1.ImageVerification{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v1-verify", Namespace: "team"},
		Spec: v1alpha1.ImageVerificationSpec{PipelineName: "app", BundleName: "v1",
			Commit: &v1alpha1.VerifiedCommit{Repo: "https://github.com/org/config", SHA: sha},
			Policy: v1alpha1.ImageVerificationPolicy{
				Authorities: []v1alpha1.SignatureAuthority{{Name: "agents",
					Keyless: &v1alpha1.KeylessAuthority{Issuer: gitsignIssuer, Subject: gitsignIdentity}}},
				Commits: &v1alpha1.CommitSignaturePolicy{RequireSigned: true, Authorities: []string{"agents"}},
			}},
	}
}

func reconcileGitsign(t *testing.T, provider scm.SCMProvider, vs root.TrustedMaterial, v *v1alpha1.ImageVerification,
	objs ...client.Object) *v1alpha1.ImageVerification {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(s))
	require.NoError(t, v1alpha1.AddToScheme(s))
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&v1alpha1.ImageVerification{}).
		WithObjects(append(objs, v)...).Build()
	r := &Reconciler{Client: c, SCM: provider, SCMHost: "github.com",
		PublicGoodRoot: func(context.Context, time.Time) (root.TrustedMaterial, error) { return vs, nil }}
	key := types.NamespacedName{Namespace: v.Namespace, Name: v.Name}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	var got v1alpha1.ImageVerification
	require.NoError(t, c.Get(context.Background(), key, &got))
	return &got
}

func TestImageVerification_GitsignCommit(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	payload := []byte(testPayload)
	sig := signCommit(t, vs, gitsignIdentity, gitsignIssuer, payload, signOpts{Proof: true})
	sha, err := commitObjectID(payload, sig, false)
	require.NoError(t, err)
	signed := scm.CommitPayload{SHA: sha, Payload: testPayload, Signature: string(sig)}
	exact := v1alpha1.AgentSigningIdentity{Issuer: gitsignIssuer, Subject: gitsignIdentity}
	pattern := v1alpha1.AgentSigningIdentity{Issuer: gitsignIssuer, SubjectRegExp: `https://github\.com/myorg/agent/.+`}

	cases := []struct {
		name     string
		scm      scm.SCMProvider
		mutate   func(*v1alpha1.ImageVerification)
		objs     []client.Object
		phase    string
		msg      string
		agent    string
		note     string
		verified bool
	}{
		{name: "verified, one agent", scm: &payloadSCM{p: signed}, objs: []client.Object{agentIdentity("fixer", true, false, exact)},
			phase: v1alpha1.ImageVerificationVerified, verified: true, agent: "fixer"},
		{name: "a subject expression", scm: &payloadSCM{p: signed}, objs: []client.Object{agentIdentity("fixer", true, false, pattern)},
			phase: v1alpha1.ImageVerificationVerified, verified: true, agent: "fixer"},
		{name: "no agent's identity", scm: &payloadSCM{p: signed},
			objs:  []client.Object{agentIdentity("other", true, false, v1alpha1.AgentSigningIdentity{Issuer: gitsignIssuer, Subject: "https://elsewhere"})},
			phase: v1alpha1.ImageVerificationVerified, verified: true},
		{name: "two agents' identities", scm: &payloadSCM{p: signed},
			objs:  []client.Object{agentIdentity("fixer", true, false, exact), agentIdentity("copycat", true, false, pattern)},
			phase: v1alpha1.ImageVerificationVerified, verified: true, note: "2 AgentIdentities (copycat, fixer); no agent recorded"},
		{name: "a suspended agent", scm: &payloadSCM{p: signed}, objs: []client.Object{agentIdentity("fixer", true, true, exact)},
			phase: v1alpha1.ImageVerificationVerified, verified: true},
		{name: "an agent not ready", scm: &payloadSCM{p: signed}, objs: []client.Object{agentIdentity("fixer", false, false, exact)},
			phase: v1alpha1.ImageVerificationVerified, verified: true},
		// Ready for generation 1, its spec since edited (generation 2): the
		// status is stale, so the new identities do not map yet.
		{name: "an agent whose status is for an older spec", scm: &payloadSCM{p: signed}, objs: []client.Object{editedAgent("fixer", exact)},
			phase: v1alpha1.ImageVerificationVerified, verified: true},
		{name: "an issuer that differs", scm: &payloadSCM{p: signed},
			objs:  []client.Object{agentIdentity("fixer", true, false, v1alpha1.AgentSigningIdentity{Issuer: "https://accounts.google.com", Subject: gitsignIdentity})},
			phase: v1alpha1.ImageVerificationVerified, verified: true},
		{name: "another identity's signature", scm: &payloadSCM{p: signed}, mutate: func(v *v1alpha1.ImageVerification) {
			v.Spec.Policy.Authorities[0].Keyless.Subject = "https://github.com/myorg/app/.github/workflows/release.yml@refs/heads/main"
		}, phase: v1alpha1.ImageVerificationFailed, msg: "does not verify against commits.authorities"},
		{name: "a payload for another commit", scm: &payloadSCM{p: scm.CommitPayload{SHA: strings.Repeat("ab", 20), Payload: testPayload, Signature: string(sig)}},
			mutate: func(v *v1alpha1.ImageVerification) { v.Spec.Commit.SHA = strings.Repeat("ab", 20) },
			phase:  v1alpha1.ImageVerificationFailed, msg: "are commit " + sha + ", not " + strings.Repeat("ab", 20)},
		{name: "the SCM answered for another commit", scm: &payloadSCM{p: scm.CommitPayload{SHA: strings.Repeat("cd", 20), Payload: testPayload, Signature: string(sig)}},
			phase: v1alpha1.ImageVerificationFailed, msg: "the SCM answered for commit"},
		{name: "unsigned", scm: &payloadSCM{p: scm.CommitPayload{SHA: sha, Payload: testPayload}},
			phase: v1alpha1.ImageVerificationFailed, msg: "is not signed"},
		{name: "a GPG signature", scm: &payloadSCM{p: scm.CommitPayload{SHA: sha, Payload: testPayload,
			Signature: "-----BEGIN PGP SIGNATURE-----\n\niQEz\n-----END PGP SIGNATURE-----\n"}},
			phase: v1alpha1.ImageVerificationFailed, msg: "a GPG signature, not a gitsign"},
		{name: "a provider without payloads", scm: verifyOnlySCM{},
			phase: v1alpha1.ImageVerificationFailed, msg: "does not return a commit's signed payload"},
		{name: "an SCM outage", scm: &payloadSCM{err: errors.New("502 bad gateway")},
			phase: v1alpha1.ImageVerificationPending, msg: "SCM: 502"},
		{name: "a missing commit", scm: &payloadSCM{err: &scm.APIError{Provider: "GitHub", StatusCode: 404}},
			phase: v1alpha1.ImageVerificationFailed, msg: "status 404"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := gitsignIV(sha)
			if tc.mutate != nil {
				tc.mutate(v)
			}
			got := reconcileGitsign(t, tc.scm, vs, v, tc.objs...)
			assert.Equal(t, tc.phase, got.Status.Phase, got.Status.Message)
			assert.Contains(t, got.Status.Message, tc.msg)
			require.NotNil(t, got.Status.Commit)
			c := got.Status.Commit
			assert.Equal(t, CommitMethodGitsign, c.Method)
			assert.Equal(t, tc.verified, c.Verified, c.Message)
			if tc.verified {
				assert.Equal(t, gitsignIdentity, c.Signer)
				assert.Equal(t, gitsignIssuer, c.Issuer)
				assert.Equal(t, "agents", c.Authority)
				assert.Equal(t, tc.agent, c.Agent)
				if tc.note != "" {
					assert.Contains(t, c.Message, tc.note)
				}
			}
		})
	}
}

// TestImageVerification_SCMCommitMethod: a commit the SCM verified records
// method scm, and commits.authorities is what selects gitsign.
func TestImageVerification_SCMCommitMethod(t *testing.T) {
	sha := strings.Repeat("abc1", 10)
	v := gitsignIV(sha)
	v.Spec.Policy.Commits.Authorities = nil
	got := reconcileGitsign(t, &signedSCM{sig: scm.CommitSignature{Verified: true, Signer: "alice", SHA: sha, Identities: []string{"alice"}}}, nil, v)
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
	assert.Equal(t, CommitMethodSCM, got.Status.Commit.Method)
}

type signedSCM struct {
	scm.SCMProvider
	sig scm.CommitSignature
}

func (f *signedSCM) VerifyCommit(context.Context, string, string) (scm.CommitSignature, error) {
	return f.sig, nil
}

func TestSigningIdentityMatches(t *testing.T) {
	const iss = gitsignIssuer
	cases := []struct {
		id      v1alpha1.AgentSigningIdentity
		subject string
		want    bool
	}{
		{v1alpha1.AgentSigningIdentity{Issuer: iss, Subject: "a"}, "a", true},
		{v1alpha1.AgentSigningIdentity{Issuer: iss, Subject: "a"}, "ab", false},
		{v1alpha1.AgentSigningIdentity{Issuer: "https://other", Subject: "a"}, "a", false},
		{v1alpha1.AgentSigningIdentity{Issuer: iss, SubjectRegExp: `https://github\.com/org/.*`}, "https://github.com/org/x", true},
		// Anchored: an expression matches the whole subject only.
		{v1alpha1.AgentSigningIdentity{Issuer: iss, SubjectRegExp: `github\.com/org/x`}, "https://evil.example/github.com/org/x", false},
		{v1alpha1.AgentSigningIdentity{Issuer: iss, SubjectRegExp: `(`}, "(", false},
		{v1alpha1.AgentSigningIdentity{Issuer: iss}, "", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, SigningIdentityMatches(tc.id, iss, tc.subject), "%+v %q", tc.id, tc.subject)
	}
}

// TestEvaluateAttestation_Agent: a keyless attestation's signer maps to its
// agent; a key authority's signature (no certificate) maps to none.
func TestEvaluateAttestation_Agent(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	const digest = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const predicateType = "https://slsa.dev/provenance/v1"
	stmt, err := json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []map[string]any{{"name": "ghcr.io/org/app", "digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}},
		"predicateType": predicateType,
		"predicate":     map[string]any{"buildDefinition": map[string]any{}},
	})
	require.NoError(t, err)
	b := attestationBundle(t, vs, stmt)
	auth := keylessAuthority(t, vs, "builder", gitsignIssuer, gitsignIdentity)
	img := v1alpha1.VerifiedImage{Repository: "ghcr.io/org/app", Digest: digest}
	p := v1alpha1.AttestationPolicy{Name: "provenance", PredicateType: predicateType, Authorities: []string{"builder"}}

	res := evaluateAttestation(img, p, [][]byte{b}, []authority{auth},
		[]v1alpha1.AgentIdentity{*agentIdentity("builder-bot", true, false, v1alpha1.AgentSigningIdentity{Issuer: gitsignIssuer, Subject: gitsignIdentity})})
	require.True(t, res.Verified, res.Message)
	assert.Equal(t, "builder-bot", res.Agent)

	res = evaluateAttestation(img, p, [][]byte{b}, []authority{auth}, nil)
	require.True(t, res.Verified, res.Message)
	assert.Empty(t, res.Agent)
}

// attestationBundle is a v0.1 Sigstore bundle JSON of statement, signed by
// a vs leaf certificate for the gitsign test identity.
func attestationBundle(t *testing.T, vs *ca.VirtualSigstore, statement []byte) []byte {
	t.Helper()
	e, err := vs.Attest(gitsignIdentity, gitsignIssuer, statement)
	require.NoError(t, err)
	vc, err := e.VerificationContent()
	require.NoError(t, err)
	sc, err := e.SignatureContent()
	require.NoError(t, err)
	env := sc.EnvelopeContent().RawEnvelope()
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	require.NoError(t, err)
	var sigs []*protodsse.Signature
	for _, s := range env.Signatures {
		raw, err := base64.StdEncoding.DecodeString(s.Sig)
		require.NoError(t, err)
		sigs = append(sigs, &protodsse.Signature{Sig: raw, Keyid: s.KeyID})
	}
	entries, err := e.TlogEntries()
	require.NoError(t, err)
	var tles []*rekorpb.TransparencyLogEntry
	for _, en := range entries {
		// VirtualSigstore's entries carry neither their kind nor their
		// signed entry timestamp in the proto: add both.
		tle := proto.Clone(en.TransparencyLogEntry()).(*rekorpb.TransparencyLogEntry)
		tle.KindVersion = &rekorpb.KindVersion{Kind: "dsse", Version: "0.0.1"}
		set, err := vs.RekorSignPayload(tlog.RekorPayload{Body: base64.StdEncoding.EncodeToString(tle.CanonicalizedBody),
			IntegratedTime: tle.IntegratedTime, LogIndex: tle.LogIndex, LogID: hex.EncodeToString(tle.LogId.KeyId)})
		require.NoError(t, err)
		tle.InclusionPromise = &rekorpb.InclusionPromise{SignedEntryTimestamp: set}
		tles = append(tles, tle)
	}
	pb := &protobundle.Bundle{
		MediaType: "application/vnd.dev.sigstore.bundle+json;version=0.1",
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content: &protobundle.VerificationMaterial_X509CertificateChain{X509CertificateChain: &protocommon.X509CertificateChain{
				Certificates: []*protocommon.X509Certificate{{RawBytes: vc.Certificate().Raw}},
			}},
			TlogEntries: tles,
		},
		Content: &protobundle.Bundle_DsseEnvelope{DsseEnvelope: &protodsse.Envelope{
			Payload: payload, PayloadType: env.PayloadType, Signatures: sigs,
		}},
	}
	raw, err := protojson.Marshal(pb)
	require.NoError(t, err)
	return raw
}
