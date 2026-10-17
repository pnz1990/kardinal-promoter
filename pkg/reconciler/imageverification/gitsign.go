// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // git object IDs of SHA-1 repositories are SHA-1
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/digitorus/pkcs7"
	"github.com/go-openapi/strfmt"
	"github.com/go-openapi/swag/conv"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	rekorpb "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/rekor/pkg/generated/models"
	"github.com/sigstore/rekor/pkg/types"
	"github.com/sigstore/rekor/pkg/types/hashedrekord"
	hashedrekordv001 "github.com/sigstore/rekor/pkg/types/hashedrekord/v0.0.1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"google.golang.org/protobuf/proto"
)

// A gitsign commit signature (docs/design/20-attestations.md §3.5) is a
// detached CMS SignedData over the commit payload, PEM-armored as
// "SIGNED MESSAGE", signed by a short-lived Fulcio certificate. Its Rekor
// entry is a hashedrekord of the DER signed attributes, embedded as a
// serialized TransparencyLogEntry in an unsigned attribute. The entry is
// verified offline: kardinal rebuilds the entry's body from the signature
// and checks the log's inclusion proof or signed timestamp against the
// authority's trusted root, with the same sigstore-go verifier as images.

const gitsignPEMType = "SIGNED MESSAGE"

var (
	// oidRekorTransparencyLogEntry is gitsign's unsigned attribute holding
	// the serialized Rekor TransparencyLogEntry (rekor#1390).
	oidRekorTransparencyLogEntry = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 3, 1}
	oidAttributeMessageDigest    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidDigestSHA256              = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
)

// errNotGitsign is returned for a signature that is not a gitsign one: no
// signature, a GPG or SSH signature, or another PEM type.
var errNotGitsign = errors.New("not a gitsign (Sigstore) signature")

// gitsignCommit is a parsed gitsign commit signature, ready to verify.
type gitsignCommit struct {
	// bundle is the signature as a Sigstore bundle: the certificate, the
	// embedded log entry, and a message signature over digest.
	bundle *bundle.Bundle
	// digest is the SHA-256 of the DER signed attributes: what the
	// signature and the Rekor entry are over.
	digest []byte
	leaf   *x509.Certificate
}

// commitObjectID is the git object ID of the commit whose signed payload
// is payload and whose signature is sig, as git hashes it: git inserts the
// signature as the last header ("gpgsig", or "gpgsig-sha256" in a SHA-256
// repository), before the blank line that ends the headers, each line
// after the first indented by a space (commit.c, add_header_signature).
func commitObjectID(payload, sig []byte, sha256Repo bool) (string, error) {
	end := bytes.Index(payload, []byte("\n\n"))
	if end < 0 {
		return "", fmt.Errorf("the commit payload has no blank line after its headers")
	}
	s := strings.TrimRight(string(sig), "\n")
	if s == "" {
		return "", fmt.Errorf("empty signature")
	}
	header := "gpgsig"
	if sha256Repo {
		header = "gpgsig-sha256"
	}
	raw := make([]byte, 0, len(payload)+len(s)+len(header)+16)
	raw = append(raw, payload[:end+1]...)
	raw = append(raw, header+" "+strings.ReplaceAll(s, "\n", "\n ")+"\n"...)
	raw = append(raw, payload[end+1:]...)
	prefix := fmt.Sprintf("commit %d\x00", len(raw))
	if sha256Repo {
		sum := sha256.Sum256(append([]byte(prefix), raw...))
		return hex.EncodeToString(sum[:]), nil
	}
	sum := sha1.Sum(append([]byte(prefix), raw...)) //nolint:gosec // the repository's object ID
	return hex.EncodeToString(sum[:]), nil
}

// parseGitsign parses sig, a gitsign signature over payload, into a
// Sigstore bundle. It checks the CMS structure and that the signature
// covers payload (the messageDigest attribute); the certificate, the
// signature and the log entry are verified by the authority (verifyBundle).
func parseGitsign(ctx context.Context, payload, sig []byte) (*gitsignCommit, error) {
	switch {
	case len(bytes.TrimSpace(sig)) == 0:
		return nil, fmt.Errorf("unsigned: %w", errNotGitsign)
	case bytes.Contains(sig, []byte("-----BEGIN PGP SIGNATURE-----")):
		return nil, fmt.Errorf("a GPG signature, %w: GPG and SSH signatures are checked by the SCM (commits.requireSigned without authorities)", errNotGitsign)
	case bytes.Contains(sig, []byte("-----BEGIN SSH SIGNATURE-----")):
		return nil, fmt.Errorf("an SSH signature, %w: GPG and SSH signatures are checked by the SCM (commits.requireSigned without authorities)", errNotGitsign)
	}
	block, _ := pem.Decode(sig)
	if block == nil || block.Type != gitsignPEMType {
		return nil, fmt.Errorf("no %q PEM block: %w", gitsignPEMType, errNotGitsign)
	}
	p7, err := pkcs7.Parse(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse the CMS signature: %w", err)
	}
	if len(p7.Signers) != 1 {
		return nil, fmt.Errorf("the CMS signature has %d signers, want 1", len(p7.Signers))
	}
	if len(p7.Content) != 0 {
		return nil, fmt.Errorf("the CMS signature carries its content; a commit signature is detached")
	}
	si := p7.Signers[0]
	if !si.DigestAlgorithm.Algorithm.Equal(oidDigestSHA256) {
		return nil, fmt.Errorf("digest algorithm %v, want SHA-256", si.DigestAlgorithm.Algorithm)
	}
	var md []byte
	if err := p7.UnmarshalSignedAttribute(oidAttributeMessageDigest, &md); err != nil {
		return nil, fmt.Errorf("the CMS signature has no messageDigest attribute: %w", err)
	}
	sum := sha256.Sum256(payload)
	if subtle.ConstantTimeCompare(md, sum[:]) != 1 {
		return nil, fmt.Errorf("the signature is not over this commit's payload (messageDigest mismatch)")
	}
	leaf := p7.GetOnlySigner()
	if leaf == nil {
		return nil, fmt.Errorf("the CMS signature does not carry its signer's certificate")
	}
	// Any intermediates the signature carries are ignored: the chain comes
	// from the authority's trusted root.
	signed, err := signedAttributes(si.AuthenticatedAttributes)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(signed)

	var tleBytes []byte
	found := 0
	for _, a := range si.UnauthenticatedAttributes {
		if !a.Type.Equal(oidRekorTransparencyLogEntry) {
			continue
		}
		found++
		if rest, err := asn1.Unmarshal(a.Value.Bytes, &tleBytes); err != nil || len(rest) != 0 {
			return nil, fmt.Errorf("the embedded Rekor entry attribute is not one OCTET STRING")
		}
	}
	switch found {
	case 0:
		return nil, fmt.Errorf("no embedded Rekor entry (OID %v): sign with gitsign's offline Rekor mode, the default since gitsign v0.8", oidRekorTransparencyLogEntry)
	case 1:
	default:
		return nil, fmt.Errorf("%d embedded Rekor entries, want 1", found)
	}
	tle := new(rekorpb.TransparencyLogEntry)
	if err := proto.Unmarshal(tleBytes, tle); err != nil {
		return nil, fmt.Errorf("the embedded Rekor entry: %w", err)
	}
	if kv := tle.GetKindVersion(); kv.GetKind() != hashedrekord.KIND || kv.GetVersion() != hashedrekordv001.APIVERSION {
		return nil, fmt.Errorf("the embedded Rekor entry is %s %s, want %s %s",
			kv.GetKind(), kv.GetVersion(), hashedrekord.KIND, hashedrekordv001.APIVERSION)
	}
	// The body is never stored in the attribute: rebuilt from this
	// signature, the log's proof covers exactly what was signed here.
	if tle.CanonicalizedBody, err = hashedRekordBody(ctx, digest[:], si.EncryptedDigest, leaf); err != nil {
		return nil, err
	}
	// A v0.3 bundle carries the leaf alone and needs the inclusion proof;
	// an entry with only a signed entry timestamp has a v0.1 bundle's
	// shape, and sigstore-go then checks that promise instead.
	material := &protobundle.VerificationMaterial{
		Content:     &protobundle.VerificationMaterial_Certificate{Certificate: &protocommon.X509Certificate{RawBytes: leaf.Raw}},
		TlogEntries: []*rekorpb.TransparencyLogEntry{tle},
	}
	mediaType := "application/vnd.dev.sigstore.bundle.v0.3+json"
	if tle.GetInclusionProof() == nil {
		mediaType = "application/vnd.dev.sigstore.bundle+json;version=0.1"
		material.Content = &protobundle.VerificationMaterial_X509CertificateChain{X509CertificateChain: &protocommon.X509CertificateChain{
			Certificates: []*protocommon.X509Certificate{{RawBytes: leaf.Raw}},
		}}
	}
	pb := &protobundle.Bundle{
		MediaType:            mediaType,
		VerificationMaterial: material,
		Content: &protobundle.Bundle_MessageSignature{MessageSignature: &protocommon.MessageSignature{
			MessageDigest: &protocommon.HashOutput{Algorithm: protocommon.HashAlgorithm_SHA2_256, Digest: digest[:]},
			Signature:     si.EncryptedDigest,
		}},
	}
	b, err := bundle.NewBundle(pb)
	if err != nil {
		return nil, fmt.Errorf("the signature as a Sigstore bundle: %w", err)
	}
	return &gitsignCommit{bundle: b, digest: digest[:], leaf: leaf}, nil
}

// cmsAttribute has the shape of a CMS Attribute (pkcs7's own type is not
// exported).
type cmsAttribute struct {
	Type  asn1.ObjectIdentifier
	Value asn1.RawValue `asn1:"set"`
}

// signedAttributes is the DER SET OF the signed attributes: the bytes a
// CMS signature with signed attributes is computed over (RFC 5652 §5.4).
func signedAttributes[T any](attrs []T) ([]byte, error) {
	out := make([]cmsAttribute, 0, len(attrs))
	for _, a := range attrs {
		raw, err := asn1.Marshal(a)
		if err != nil {
			return nil, fmt.Errorf("signed attributes: %w", err)
		}
		var c cmsAttribute
		if _, err := asn1.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("signed attributes: %w", err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the CMS signature has no signed attributes")
	}
	seq, err := asn1.Marshal(struct {
		A []cmsAttribute `asn1:"set"`
	}{A: out})
	if err != nil {
		return nil, fmt.Errorf("signed attributes: %w", err)
	}
	var outer asn1.RawValue
	if _, err := asn1.Unmarshal(seq, &outer); err != nil {
		return nil, fmt.Errorf("signed attributes: %w", err)
	}
	return outer.Bytes, nil
}

// hashedRekordBody is the canonical hashedrekord v0.0.1 body of a
// signature over digest by cert: what Rekor logged for it.
func hashedRekordBody(ctx context.Context, digest, sig []byte, cert *x509.Certificate) ([]byte, error) {
	certPEM, err := cryptoutils.MarshalCertificateToPEM(cert)
	if err != nil {
		return nil, fmt.Errorf("encode the certificate: %w", err)
	}
	e := &hashedrekordv001.V001Entry{HashedRekordObj: models.HashedrekordV001Schema{
		Data: &models.HashedrekordV001SchemaData{Hash: &models.HashedrekordV001SchemaDataHash{
			Algorithm: conv.Pointer(models.HashedrekordV001SchemaDataHashAlgorithmSha256),
			Value:     conv.Pointer(hex.EncodeToString(digest)),
		}},
		Signature: &models.HashedrekordV001SchemaSignature{
			Content:   strfmt.Base64(sig),
			PublicKey: &models.HashedrekordV001SchemaSignaturePublicKey{Content: strfmt.Base64(certPEM)},
		},
	}}
	body, err := types.CanonicalizeEntry(ctx, e)
	if err != nil {
		return nil, fmt.Errorf("the Rekor entry body: %w", err)
	}
	return body, nil
}
