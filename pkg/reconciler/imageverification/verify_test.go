// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/sign"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// testImage is a fake image: its "manifest" bytes and their digest.
type testImage struct {
	manifest []byte
	digest   string
}

func newTestImage(t *testing.T, seed string) testImage {
	t.Helper()
	m := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","annotations":{"seed":"` + seed + `"}}`)
	sum := sha256.Sum256(m)
	return testImage{manifest: m, digest: "sha256:" + hex.EncodeToString(sum[:])}
}

// keyBundle signs img's manifest (its digest) with a new key into a
// Sigstore bundle with no transparency log entry, as `cosign sign --key
// --tlog-upload=false --new-bundle-format` does. It returns the bundle JSON
// and the key's PEM.
func keyBundle(t *testing.T, img testImage) ([]byte, []byte) {
	t.Helper()
	kp, err := sign.NewEphemeralKeypair(nil)
	require.NoError(t, err)
	pb, err := sign.Bundle(&sign.PlainData{Data: img.manifest}, kp, sign.BundleOptions{})
	require.NoError(t, err)
	b, err := protojson.Marshal(pb)
	require.NoError(t, err)
	pemKey, err := kp.GetPublicKeyPem()
	require.NoError(t, err)
	return b, []byte(pemKey)
}

// legacySig signs a cosign simple-signing payload for digest with a new
// ECDSA key, as `cosign sign --key` (v2) stores it in the .sig tag.
func legacySig(t *testing.T, digest string) (LegacySignature, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	payload := []byte(fmt.Sprintf(`{"critical":{"identity":{"docker-reference":"r.example/app"},`+
		`"image":{"docker-manifest-digest":%q},"type":"cosign container image signature"},"optional":null}`, digest))
	sum := sha256.Sum256(payload)
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	return LegacySignature{Payload: payload, Signature: sig}, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func keyAuthority(t *testing.T, name string, pemKey []byte) authority {
	t.Helper()
	v, err := loadKey(pemKey)
	require.NoError(t, err)
	return authority{name: name, key: v}
}

// TestVerifyImage_Key: a key-signed Sigstore bundle or legacy .sig verifies
// against its key and not against another key, and only for the digest it
// was made for.
func TestVerifyImage_Key(t *testing.T) {
	img := newTestImage(t, "a")
	other := newTestImage(t, "b")
	bun, pemKey := keyBundle(t, img)
	_, otherKey := keyBundle(t, img)
	leg, legKey := legacySig(t, img.digest)

	cases := []struct {
		name        string
		digest      string
		sigs        Signatures
		authorities []authority
		verified    bool
		authority   string
		reason      string
	}{
		{"bundle, right key", img.digest, Signatures{Bundles: [][]byte{bun}},
			[]authority{keyAuthority(t, "release", pemKey)}, true, "release", ""},
		{"bundle, second authority matches", img.digest, Signatures{Bundles: [][]byte{bun}},
			[]authority{keyAuthority(t, "old", otherKey), keyAuthority(t, "release", pemKey)}, true, "release", ""},
		{"bundle, wrong key", img.digest, Signatures{Bundles: [][]byte{bun}},
			[]authority{keyAuthority(t, "other", otherKey)}, false, "", "authority other"},
		{"bundle for another image", other.digest, Signatures{Bundles: [][]byte{bun}},
			[]authority{keyAuthority(t, "release", pemKey)}, false, "", "bundle 1"},
		{"legacy, right key", img.digest, Signatures{Legacy: []LegacySignature{leg}},
			[]authority{keyAuthority(t, "legacy", legKey)}, true, "legacy", ""},
		{"legacy, wrong key", img.digest, Signatures{Legacy: []LegacySignature{leg}},
			[]authority{keyAuthority(t, "other", otherKey)}, false, "", "does not verify with the key"},
		{"legacy for another digest", other.digest, Signatures{Legacy: []LegacySignature{leg}},
			[]authority{keyAuthority(t, "legacy", legKey)}, false, "", "not " + other.digest},
		{"garbage bundle", img.digest, Signatures{Bundles: [][]byte{[]byte("{")}},
			[]authority{keyAuthority(t, "release", pemKey)}, false, "", "bundle 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := verifyImage(tc.digest, tc.sigs, tc.authorities)
			assert.Equal(t, tc.verified, v.verified, "reasons: %v", v.reasons)
			assert.Equal(t, tc.authority, v.authority)
			if tc.reason != "" {
				assert.Contains(t, fmt.Sprint(v.reasons), tc.reason)
			}
		})
	}
}

// TestVerifyImage_KeyRequiresTlog: a key authority that requires a
// transparency log entry refuses a bundle without one, and a legacy .sig.
func TestVerifyImage_KeyRequiresTlog(t *testing.T) {
	img := newTestImage(t, "a")
	bun, pemKey := keyBundle(t, img)
	a := keyAuthority(t, "release", pemKey)
	a.requireTlog = true
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	a.tlogs = vs
	v := verifyImage(img.digest, Signatures{Bundles: [][]byte{bun}}, []authority{a})
	assert.False(t, v.verified)
	leg, legKey := legacySig(t, img.digest)
	b := keyAuthority(t, "legacy", legKey)
	b.requireTlog = true
	v = verifyImage(img.digest, Signatures{Legacy: []LegacySignature{leg}}, []authority{b})
	assert.False(t, v.verified)
	assert.Contains(t, fmt.Sprint(v.reasons), "requires a transparency log entry")
}

// TestVerifyEntity_Keyless signs with a virtual Sigstore (a Fulcio CA, a
// Rekor log and a TSA in memory, no network) and checks the certificate
// identity: issuer and subject, or a subject regular expression.
func TestVerifyEntity_Keyless(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	img := newTestImage(t, "keyless")
	const subject = "https://github.com/org/app/.github/workflows/release.yml@refs/heads/main"
	const issuer = "https://token.actions.githubusercontent.com"
	entity, err := vs.Sign(subject, issuer, img.manifest)
	require.NoError(t, err)
	digest, err := digestBytes(img.digest)
	require.NoError(t, err)

	cases := []struct {
		name, issuer, subject, subjectRE string
		ok                               bool
	}{
		{"exact identity", issuer, subject, "", true},
		{"subject regexp", issuer, "", `^https://github\.com/org/app/`, true},
		{"other subject", issuer, "https://github.com/evil/app/.github/workflows/x.yml@refs/heads/main", "", false},
		{"other issuer", "https://accounts.google.com", subject, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := verify.NewShortCertificateIdentity(tc.issuer, "", tc.subject, tc.subjectRE)
			require.NoError(t, err)
			a := authority{name: "ci", trusted: vs, identity: &id}
			signer, err := a.verifyEntity(entity, digest)
			if !tc.ok {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, subject, signer)
		})
	}

	// A keyless signature for another artifact does not verify.
	otherImg := newTestImage(t, "other")
	od, err := digestBytes(otherImg.digest)
	require.NoError(t, err)
	id, err := verify.NewShortCertificateIdentity(issuer, "", subject, "")
	require.NoError(t, err)
	_, err = authority{name: "ci", trusted: vs, identity: &id}.verifyEntity(entity, od)
	require.Error(t, err)
}

// TestVerifyLegacy_KeylessRefused: a legacy keyless .sig is reported as
// needing the bundle format.
func TestVerifyLegacy_KeylessRefused(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	id, err := verify.NewShortCertificateIdentity("i", "", "s", "")
	require.NoError(t, err)
	err = authority{name: "ci", trusted: vs, identity: &id}.verifyLegacy(LegacySignature{}, "sha256:00")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Sigstore bundle")
}

func TestDigestBytes(t *testing.T) {
	_, err := digestBytes("sha512:abc")
	require.Error(t, err)
	_, err = digestBytes("sha256:zz")
	require.Error(t, err)
	b, err := digestBytes(newTestImage(t, "x").digest)
	require.NoError(t, err)
	assert.Len(t, b, 32)
}
