// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"bytes"
	"crypto"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/sigstore/sigstore/pkg/signature"
)

// Signatures is the signature material a registry holds for one image.
type Signatures struct {
	// Bundles are Sigstore bundles (JSON) attached to the image as OCI
	// referrers (cosign v3, or cosign v2 --new-bundle-format).
	Bundles [][]byte
	// Legacy are cosign "simple signing" signatures from the
	// sha256-<digest>.sig tag (cosign v2 sign --key).
	Legacy []LegacySignature
}

// LegacySignature is one cosign simple-signing signature.
type LegacySignature struct {
	// Payload is the signed simple-signing JSON.
	Payload []byte
	// Signature is the raw signature over Payload.
	Signature []byte
	// Certificate is the PEM signing certificate of a keyless signature.
	Certificate []byte
}

func (s Signatures) empty() bool { return len(s.Bundles) == 0 && len(s.Legacy) == 0 }

// authority is a policy authority with its key or trusted root loaded.
type authority struct {
	name string
	// key, for a key authority.
	key         signature.Verifier
	requireTlog bool
	// keyless, for a keyless authority.
	trusted  root.TrustedMaterial
	identity *verify.CertificateIdentity
	// tlogs is the trusted material for a key authority that requires a
	// transparency log entry (the Rekor keys).
	tlogs root.TrustedMaterial
}

// loadKey parses a PEM public key into a verifier.
func loadKey(pemKey []byte) (signature.Verifier, error) {
	pub, err := cryptoutils.UnmarshalPEMToPublicKey(pemKey)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	v, err := signature.LoadVerifier(pub, crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("load public key: %w", err)
	}
	return v, nil
}

// verdict is the outcome for one image.
type verdict struct {
	verified  bool
	authority string
	signer    string
	// reasons say why each signature did not verify (empty when there was
	// no signature).
	reasons []string
	// wrongDigest is set when a cosign signature verified with a policy key
	// names another image digest: a signature that is ours and wrong, which
	// fails at once instead of waiting for another one.
	wrongDigest bool
}

// CosignSignPredicate is the in-toto predicate type of a cosign image
// signature in a Sigstore bundle. A bundle with another predicate (an SBOM
// or provenance attestation) or a plain message signature is not an image
// signature, whoever signed it.
const CosignSignPredicate = "https://sigstore.dev/cosign/sign/v1"

// verifyImage checks sigs of the image with digest against the authorities:
// the image is verified when one signature verifies against one authority.
func verifyImage(digest string, sigs Signatures, authorities []authority) verdict {
	var v verdict
	raw, err := digestBytes(digest)
	if err != nil {
		v.reasons = append(v.reasons, err.Error())
		return v
	}
	for i, b := range sigs.Bundles {
		var bun bundle.Bundle
		if err := bun.UnmarshalJSON(b); err != nil {
			v.reasons = append(v.reasons, fmt.Sprintf("bundle %d: %v", i+1, err))
			continue
		}
		for _, a := range authorities {
			signer, err := a.verifyEntity(&bun, raw)
			if err == nil {
				return verdict{verified: true, authority: a.name, signer: signer}
			}
			v.reasons = append(v.reasons, fmt.Sprintf("bundle %d, authority %s: %v", i+1, a.name, err))
		}
	}
	for i, s := range sigs.Legacy {
		for _, a := range authorities {
			err := a.verifyLegacy(s, digest)
			if err == nil {
				return verdict{verified: true, authority: a.name, signer: a.name}
			}
			var wd *wrongDigestError
			if errors.As(err, &wd) {
				v.wrongDigest = true
			}
			v.reasons = append(v.reasons, fmt.Sprintf("signature %d, authority %s: %v", i+1, a.name, err))
		}
	}
	return v
}

// verifyEntity verifies a Sigstore bundle (or another signed entity) for
// the artifact digest.
func (a authority) verifyEntity(b verify.SignedEntity, digest []byte) (string, error) {
	artifact := verify.WithArtifactDigest("sha256", digest)
	if a.key != nil {
		keyMaterial := root.NewTrustedPublicKeyMaterial(func(string) (root.TimeConstrainedVerifier, error) {
			return root.NewExpiringKey(a.key, time.Time{}, time.Time{}), nil
		})
		var material root.TrustedMaterial = keyMaterial
		opts := []verify.VerifierOption{verify.WithNoObserverTimestamps()}
		if a.requireTlog {
			if a.tlogs == nil {
				return "", fmt.Errorf("no transparency log keys to check the required Rekor entry")
			}
			material = root.TrustedMaterialCollection{keyMaterial, a.tlogs}
			opts = []verify.VerifierOption{verify.WithTransparencyLog(1), verify.WithIntegratedTimestamps(1)}
		}
		v, err := verify.NewVerifier(material, opts...)
		if err != nil {
			return "", fmt.Errorf("verifier: %w", err)
		}
		res, err := v.Verify(b, verify.NewPolicy(artifact, verify.WithKey()))
		if err != nil {
			return "", err
		}
		if err := cosignSignature(res); err != nil {
			return "", err
		}
		return a.name, nil
	}
	v, err := verify.NewVerifier(a.trusted, verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		return "", fmt.Errorf("verifier: %w", err)
	}
	res, err := v.Verify(b, verify.NewPolicy(artifact, verify.WithCertificateIdentity(*a.identity)))
	if err != nil {
		return "", err
	}
	if err := cosignSignature(res); err != nil {
		return "", err
	}
	if res.Signature != nil && res.Signature.Certificate != nil {
		return res.Signature.Certificate.SubjectAlternativeName, nil
	}
	return a.name, nil
}

// cosignSignature checks that a verified bundle is a cosign image
// signature: a DSSE in-toto statement with CosignSignPredicate.
func cosignSignature(res *verify.VerificationResult) error {
	if res == nil || res.Statement == nil {
		return fmt.Errorf("not a cosign image signature (a message signature, not an in-toto statement)")
	}
	if res.Statement.PredicateType != CosignSignPredicate {
		return fmt.Errorf("not a cosign image signature (predicate type %q, want %s)", res.Statement.PredicateType, CosignSignPredicate)
	}
	return nil
}

// wrongDigestError is a cosign signature, verified with a policy key, for
// another image.
type wrongDigestError struct{ got, want string }

func (e *wrongDigestError) Error() string {
	return fmt.Sprintf("the signature is for %s, not %s", e.got, e.want)
}

// simpleSigning is the payload cosign signs for an image.
type simpleSigning struct {
	Critical struct {
		Image struct {
			DockerManifestDigest string `json:"docker-manifest-digest"`
		} `json:"image"`
		Type string `json:"type"`
	} `json:"critical"`
}

// verifyLegacy verifies a cosign simple-signing signature with a key
// authority. Keyless legacy signatures are not supported (use the Sigstore
// bundle format).
func (a authority) verifyLegacy(s LegacySignature, digest string) error {
	if a.key == nil {
		return fmt.Errorf("a keyless signature must be a Sigstore bundle (cosign v3, or cosign v2 --new-bundle-format)")
	}
	if a.requireTlog {
		return fmt.Errorf("the authority requires a transparency log entry; a .sig signature is checked against the key only")
	}
	if err := a.key.VerifySignature(bytes.NewReader(s.Signature), bytes.NewReader(s.Payload)); err != nil {
		return fmt.Errorf("signature does not verify with the key: %w", err)
	}
	var p simpleSigning
	if err := json.Unmarshal(s.Payload, &p); err != nil {
		return fmt.Errorf("payload: %w", err)
	}
	if got := p.Critical.Image.DockerManifestDigest; got != digest {
		return &wrongDigestError{got: got, want: digest}
	}
	return nil
}

func digestBytes(digest string) ([]byte, error) {
	hexPart, ok := strings.CutPrefix(digest, "sha256:")
	if !ok {
		return nil, fmt.Errorf("digest %q is not sha256", digest)
	}
	b, err := hex.DecodeString(hexPart)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("digest %q is not a sha256 digest", digest)
	}
	return b, nil
}
