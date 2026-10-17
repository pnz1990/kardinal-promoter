// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"encoding/base64"
	"testing"
	"time"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protodsse "github.com/sigstore/protobuf-specs/gen/pb-go/dsse"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	rekorTestIdentity = "https://github.com/acme/deploy/.github/workflows/kardinal.yaml@refs/heads/main"
	rekorTestIssuer   = "https://token.actions.githubusercontent.com"
)

// loggedBundle signs statement keyless with vs (a Fulcio leaf for
// rekorTestIdentity), timestamps it with vs's TSA and logs it in vs's
// Rekor v2 as a hashedrekord, as kardinal logs a receipt: the bundle
// carries a real entry with its inclusion proof and signed checkpoint.
// edit, when set, changes the entry before it is put in the bundle.
func loggedBundle(t *testing.T, vs *ca.VirtualSigstore, statement []byte, edit func(*protorekor.TransparencyLogEntry)) []byte {
	t.Helper()
	e, err := vs.AttestAtTimeHashedRekordV2(rekorTestIdentity, rekorTestIssuer, statement, time.Now())
	require.NoError(t, err)
	vc, err := e.VerificationContent()
	require.NoError(t, err)
	cert := vc.Certificate()
	require.NotNil(t, cert)
	sc, err := e.SignatureContent()
	require.NoError(t, err)
	env := sc.(*bundle.Envelope).Envelope
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	require.NoError(t, err)
	sig, err := base64.StdEncoding.DecodeString(env.Signatures[0].Sig)
	require.NoError(t, err)
	entries, err := e.TlogEntries()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.True(t, entries[0].HasInclusionProof())
	require.NotNil(t, entries[0].TransparencyLogEntry())
	tle := proto.Clone(entries[0].TransparencyLogEntry()).(*protorekor.TransparencyLogEntry)
	tsrs, err := e.Timestamps()
	require.NoError(t, err)
	var stamps []*protocommon.RFC3161SignedTimestamp
	for _, tsr := range tsrs {
		stamps = append(stamps, &protocommon.RFC3161SignedTimestamp{SignedTimestamp: tsr})
	}
	if edit != nil {
		edit(tle)
	}
	pb := &protobundle.Bundle{
		MediaType: BundleMediaType,
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content:                   &protobundle.VerificationMaterial_Certificate{Certificate: &protocommon.X509Certificate{RawBytes: cert.Raw}},
			TlogEntries:               []*protorekor.TransparencyLogEntry{tle},
			TimestampVerificationData: &protobundle.TimestampVerificationData{Rfc3161Timestamps: stamps},
		},
		Content: &protobundle.Bundle_DsseEnvelope{DsseEnvelope: &protodsse.Envelope{
			Payload: payload, PayloadType: env.PayloadType,
			Signatures: []*protodsse.Signature{{Sig: sig, Keyid: env.Signatures[0].KeyID}},
		}},
	}
	out, err := protojson.Marshal(pb)
	require.NoError(t, err)
	return out
}

// TestVerify_RekorEntryLogged (QA-3): a keyless receipt whose bundle
// carries a real transparency log entry, from a log in the trusted root,
// verifies and is reported as logged (the entry was checked); the same
// receipt with another entry's inclusion proof, a checkpoint the log did
// not sign, a root without that log, or another identity, fails: logged is never claimed for an
// entry that does not verify.
//
// Covers RECEIPT-VERIFY-04.
func TestVerify_RekorEntryLogged(t *testing.T) {
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	opts := VerifyOptions{Identity: rekorTestIdentity, Issuer: rekorTestIssuer, TrustedRoot: vs}

	v, err := Verify(loggedBundle(t, vs, raw, nil), opts)
	require.NoError(t, err)
	assert.True(t, v.Logged, "the entry was verified against the trusted root")
	assert.False(t, v.LogNotChecked)
	assert.Equal(t, rekorTestIdentity, v.Signer)
	assert.Equal(t, input("image").Step.Spec.PipelineName, v.Statement.Predicate.Kardinal.Pipeline)

	// A Rekor v2 entry is checked by the hash of the bundle's own
	// signature, key and digest: the proof (and signed checkpoint) of
	// another statement's entry does not include this receipt.
	var otherProof *protorekor.InclusionProof
	loggedBundle(t, vs, []byte(`{"_type":"https://in-toto.io/Statement/v1"}`), func(e *protorekor.TransparencyLogEntry) {
		otherProof = e.InclusionProof
	})
	_, err = Verify(loggedBundle(t, vs, raw, func(e *protorekor.TransparencyLogEntry) { e.InclusionProof = otherProof }), opts)
	require.Error(t, err, "the proof of another entry does not include this receipt")
	_, err = Verify(loggedBundle(t, vs, raw, func(e *protorekor.TransparencyLogEntry) {
		cp := []byte(e.InclusionProof.Checkpoint.Envelope)
		cp[len(cp)-3] ^= 1
		e.InclusionProof.Checkpoint.Envelope = string(cp)
	}), opts)
	require.Error(t, err, "a checkpoint the log did not sign")

	other, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	_, err = Verify(loggedBundle(t, vs, raw, nil), VerifyOptions{Identity: rekorTestIdentity, Issuer: rekorTestIssuer, TrustedRoot: other})
	require.Error(t, err, "a log the trusted root does not hold is not trusted")

	_, err = Verify(loggedBundle(t, vs, raw, nil), VerifyOptions{Identity: "https://github.com/mallory/x/.github/workflows/x.yaml@refs/heads/main",
		Issuer: rekorTestIssuer, TrustedRoot: vs})
	require.Error(t, err, "another identity")
}
