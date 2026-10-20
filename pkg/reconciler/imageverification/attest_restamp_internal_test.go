// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protodsse "github.com/sigstore/protobuf-specs/gen/pb-go/dsse"
	rekorpb "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/sigstore/sigstore/pkg/signature"
	sigdsse "github.com/sigstore/sigstore/pkg/signature/dsse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestVerifyStatements_ReStampedKeylessRefused (QA-3 on #2121, why keyless
// attestations keep their verified time with maxAge): a keyless signature
// stamped anew after its Fulcio certificate expired (a fresh Rekor entry and
// a fresh timestamp over the old signature) does not verify, because every
// verified time must fall inside the certificate's validity. The same
// signature with a certificate valid now verifies: the refusal is the
// window, nothing else.
func TestVerifyStatements_ReStampedKeylessRefused(t *testing.T) {
	f := newCertFixture(t)
	found, reasons := verifyStatements(f.digest, [][]byte{f.stamped(time.Now().Add(-time.Minute), 10*time.Minute)}, f.predicateType, f.auths)
	require.Len(t, found, 1, "the control: a certificate valid now verifies: %v", reasons)
	assert.True(t, found[0].shortCert, "a ten-minute certificate dates by its verified time")
	found, reasons = verifyStatements(f.digest, [][]byte{f.stamped(time.Now().Add(-3*time.Hour), 10*time.Minute)}, f.predicateType, f.auths)
	assert.Empty(t, found, "an old keyless signature stamped anew after its certificate expired does not verify")
	assert.NotEmpty(t, reasons)
	t.Logf("refused: %v", reasons)
}

// TestVerifyStatements_LongLivedCertificate (QA-3 on #2121): a keyless
// attestation whose certificate lives 24 hours (a private CA) verifies, but
// its verified time bounds nothing useful: it is dated like a key
// signature, so without a statement time it cannot be dated (stale), and
// with datedBy: logTime it counts from its log time.
func TestVerifyStatements_LongLivedCertificate(t *testing.T) {
	f := newCertFixture(t)
	found, reasons := verifyStatements(f.digest, [][]byte{f.stamped(time.Now().Add(-time.Hour), 24*time.Hour)}, f.predicateType, f.auths)
	require.Len(t, found, 1, "%v", reasons)
	assert.False(t, found[0].shortCert)
	p := v1alpha1.AttestationPolicy{Name: "custom", PredicateType: f.predicateType, MaxAge: "1h"}
	_, source := dating(found[0], p)
	assert.Empty(t, source, "a 24h certificate does not date it")
	p.DatedBy = datedByLogTime
	_, source = dating(found[0], p)
	assert.Equal(t, timeLog, source)
}

// certFixture signs statements the keyless way with certificates of a CA
// of its own, a VirtualSigstore's Rekor and timestamp authority logging and
// stamping them now.
type certFixture struct {
	t                     *testing.T
	digest, predicateType string
	statement             []byte
	vs                    *ca.VirtualSigstore
	interCert             *x509.Certificate
	interKey              crypto.Signer
	auths                 []authority
}

const (
	testCertIssuer   = "https://token.actions.githubusercontent.com"
	testCertIdentity = "release@example.com"
)

func newCertFixture(t *testing.T) *certFixture {
	t.Helper()
	const issuer, identity = testCertIssuer, testCertIdentity
	digest := "sha256:" + "cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12cd34ab12"
	vs, err := ca.NewVirtualSigstore() // the trusted Rekor and timestamp authority
	require.NoError(t, err)
	rootCert, rootKey, err := ca.GenerateRootCa()
	require.NoError(t, err)
	interCert, interKey, err := ca.GenerateFulcioIntermediate(rootCert, rootKey)
	require.NoError(t, err)
	fulcio, err := root.NewTrustedRoot(root.TrustedRootMediaType01, []root.CertificateAuthority{&root.FulcioCertificateAuthority{
		Root: rootCert, Intermediates: []*x509.Certificate{interCert}}}, nil, nil, nil)
	require.NoError(t, err)
	trusted := root.TrustedMaterialCollection{fulcio, vs}
	id, err := verify.NewShortCertificateIdentity(issuer, "", identity, "")
	require.NoError(t, err)
	f := &certFixture{t: t, digest: digest, predicateType: "https://example.com/custom/v1", vs: vs, interCert: interCert, interKey: interKey,
		auths: []authority{{name: "ci", trusted: trusted, identity: &id}}}
	statement, err := json.Marshal(map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []any{map[string]any{"name": "r.example/app", "digest": map[string]string{"sha256": digest[len("sha256:"):]}}},
		"predicateType": "https://example.com/custom/v1",
		"predicate":     map[string]any{"ok": true},
	})
	require.NoError(t, err)
	f.statement = statement
	return f
}

// stamped signs the statement with a leaf certificate valid from notBefore
// for lifetime, and gives it a Rekor entry and a timestamp taken now.
func (f *certFixture) stamped(notBefore time.Time, lifetime time.Duration) []byte {
	t, vs, statement := f.t, f.vs, f.statement
	t.Helper()
	{
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		leaf, err := leafCert(testCertIdentity, testCertIssuer, notBefore, lifetime, key, f.interCert, f.interKey)
		require.NoError(t, err)
		signer, err := signature.LoadECDSASignerVerifier(key, crypto.SHA256)
		require.NoError(t, err)
		es, err := dsse.NewEnvelopeSigner(&sigdsse.SignerAdapter{SignatureSigner: signer, Pub: &key.PublicKey})
		require.NoError(t, err)
		env, err := es.SignPayload(context.Background(), "application/vnd.in-toto+json", statement)
		require.NoError(t, err)
		sig, err := base64.StdEncoding.DecodeString(env.Signatures[0].Sig)
		require.NoError(t, err)
		entry, err := vs.GenerateTlogEntry(leaf, env, sig, time.Now().Unix(), true)
		require.NoError(t, err)
		tle := proto.Clone(entry.TransparencyLogEntry()).(*rekorpb.TransparencyLogEntry)
		tle.KindVersion = &rekorpb.KindVersion{Kind: "dsse", Version: "0.0.1"}
		set, err := vs.RekorSignPayload(tlog.RekorPayload{Body: base64.StdEncoding.EncodeToString(tle.CanonicalizedBody),
			IntegratedTime: tle.IntegratedTime, LogIndex: tle.LogIndex, LogID: hex.EncodeToString(tle.LogId.GetKeyId())})
		require.NoError(t, err)
		tle.InclusionPromise = &rekorpb.InclusionPromise{SignedEntryTimestamp: set}
		tsr, err := vs.TimestampResponse(sig)
		require.NoError(t, err)
		payload, err := base64.StdEncoding.DecodeString(env.Payload)
		require.NoError(t, err)
		b, err := protojson.Marshal(&protobundle.Bundle{
			MediaType: "application/vnd.dev.sigstore.bundle.v0.3+json",
			VerificationMaterial: &protobundle.VerificationMaterial{
				Content:                   &protobundle.VerificationMaterial_Certificate{Certificate: &protocommon.X509Certificate{RawBytes: leaf.Raw}},
				TlogEntries:               []*rekorpb.TransparencyLogEntry{tle},
				TimestampVerificationData: &protobundle.TimestampVerificationData{Rfc3161Timestamps: []*protocommon.RFC3161SignedTimestamp{{SignedTimestamp: tsr}}},
			},
			Content: &protobundle.Bundle_DsseEnvelope{DsseEnvelope: &protodsse.Envelope{
				Payload: payload, PayloadType: env.PayloadType, Signatures: []*protodsse.Signature{{Sig: sig}}}},
		})
		require.NoError(t, err)
		return b
	}
}

// leafCert is a Fulcio-like leaf certificate for identity (an e-mail SAN)
// and issuer, valid from notBefore for lifetime, signed by parent.
func leafCert(identity, issuer string, notBefore time.Time, lifetime time.Duration, key *ecdsa.PrivateKey,
	parent *x509.Certificate, parentKey crypto.Signer) (*x509.Certificate, error) {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), EmailAddresses: []string{identity},
		NotBefore: notBefore, NotAfter: notBefore.Add(lifetime),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		// Fulcio's OIDC issuer extension.
		ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}, Value: []byte(issuer)}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}
