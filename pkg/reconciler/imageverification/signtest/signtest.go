// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package signtest signs image digests and attaches the signatures to an
// OCI registry the way cosign does, for tests: a Sigstore bundle as an OCI
// referrer (cosign v3, or v2 --new-bundle-format) and a legacy simple-signing
// signature in the sha256-<hex>.sig tag (cosign v2 sign --key). Nothing is
// sent to a Sigstore service: key signatures carry no transparency log
// entry. It depends on no Sigstore client library, only on the formats.
package signtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// BundleMediaType is the media and artifact type of a Sigstore bundle.
const BundleMediaType = "application/vnd.dev.sigstore.bundle.v0.3+json"

// Key is a signing key.
type Key struct {
	priv *ecdsa.PrivateKey
}

// NewKey returns a new ECDSA P-256 key.
func NewKey() (*Key, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return &Key{priv: k}, nil
}

// PublicPEM is the PEM public key (cosign.pub).
func (k *Key) PublicPEM() ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(&k.priv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// Bundle returns a Sigstore bundle (JSON, v0.3) with a DSSE in-toto
// statement whose subject is repository@digest, signed with a new key, and
// that key's PEM public key. The bundle has a public-key hint as its
// verification material and no transparency log entry, like
// `cosign sign --key --tlog-upload=false --new-bundle-format`.
func Bundle(repository, digest string) (bundleJSON, publicPEM []byte, err error) {
	k, err := NewKey()
	if err != nil {
		return nil, nil, err
	}
	b, err := k.Bundle(repository, digest)
	if err != nil {
		return nil, nil, err
	}
	pemKey, err := k.PublicPEM()
	if err != nil {
		return nil, nil, err
	}
	return b, pemKey, nil
}

// Bundle is the package-level Bundle signed with k.
func (k *Key) Bundle(repository, digest string) ([]byte, error) {
	return k.BundleWithPredicate(repository, digest, "https://sigstore.dev/cosign/sign/v1")
}

// BundleWithPredicate is Bundle with another in-toto predicate type (an
// attestation, which is not an image signature).
func (k *Key) BundleWithPredicate(repository, digest, predicateType string) ([]byte, error) {
	hexPart, ok := strings.CutPrefix(digest, "sha256:")
	if !ok {
		return nil, fmt.Errorf("digest %q is not sha256", digest)
	}
	statement, err := json.Marshal(map[string]interface{}{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []interface{}{map[string]interface{}{"name": repository, "digest": map[string]string{"sha256": hexPart}}},
		"predicateType": predicateType,
		"predicate":     map[string]interface{}{},
	})
	if err != nil {
		return nil, err
	}
	const payloadType = "application/vnd.in-toto+json"
	// DSSE pre-authentication encoding.
	pae := fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(statement), statement)
	sum := sha256.Sum256([]byte(pae))
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, sum[:])
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.priv.PublicKey)
	if err != nil {
		return nil, err
	}
	hint := sha256.Sum256(der)
	return json.Marshal(map[string]interface{}{
		"mediaType": BundleMediaType,
		"verificationMaterial": map[string]interface{}{
			"publicKey": map[string]interface{}{"hint": base64.StdEncoding.EncodeToString(hint[:])},
		},
		"dsseEnvelope": map[string]interface{}{
			"payload":     base64.StdEncoding.EncodeToString(statement),
			"payloadType": payloadType,
			"signatures":  []interface{}{map[string]interface{}{"sig": base64.StdEncoding.EncodeToString(sig)}},
		},
	})
}

// LegacyPayload is the cosign simple-signing payload for repository@digest.
func LegacyPayload(repository, digest string) []byte {
	return []byte(fmt.Sprintf(`{"critical":{"identity":{"docker-reference":%q},"image":{"docker-manifest-digest":%q},`+
		`"type":"cosign container image signature"},"optional":null}`, repository, digest))
}

// SignLegacy signs payload with k (ECDSA over SHA-256, ASN.1).
func (k *Key) SignLegacy(payload []byte) ([]byte, error) {
	sum := sha256.Sum256(payload)
	return ecdsa.SignASN1(rand.Reader, k.priv, sum[:])
}

// PushBundle attaches bundleJSON to digest in sigRepo as an OCI referrer.
// opts are the remote options (auth, transport); nameOpts the name options
// (name.Insecure for a plain-HTTP registry).
func PushBundle(ctx context.Context, sigRepo, digest string, bundleJSON []byte,
	nameOpts []name.Option, opts ...remote.Option) error {
	subject, err := v1.NewHash(digest)
	if err != nil {
		return err
	}
	layer := static.NewLayer(bundleJSON, types.MediaType(BundleMediaType))
	img, err := mutate.Append(empty.Image, mutate.Addendum{Layer: layer})
	if err != nil {
		return err
	}
	img = mutate.MediaType(img, types.OCIManifestSchema1)
	// The referrer's artifact type is its config media type when the
	// manifest has no artifactType (OCI image spec 1.1).
	img = mutate.ConfigMediaType(img, types.MediaType(BundleMediaType))
	img = mutate.Subject(img, v1.Descriptor{MediaType: types.OCIImageIndex, Digest: subject, Size: 1}).(v1.Image)
	d, err := img.Digest()
	if err != nil {
		return err
	}
	ref, err := name.NewDigest(sigRepo+"@"+d.String(), nameOpts...)
	if err != nil {
		return err
	}
	return remote.Write(ref, img, append(opts, remote.WithContext(ctx))...)
}

// PushLegacy pushes a cosign .sig tag for digest in sigRepo holding payload
// signed with sig.
func PushLegacy(ctx context.Context, sigRepo, digest string, payload, sig []byte,
	nameOpts []name.Option, opts ...remote.Option) error {
	layer := static.NewLayer(payload, "application/vnd.dev.cosign.simplesigning.v1+json")
	img, err := mutate.Append(empty.Image, mutate.Addendum{Layer: layer, Annotations: map[string]string{
		"dev.cosignproject.cosign/signature": base64.StdEncoding.EncodeToString(sig),
	}})
	if err != nil {
		return err
	}
	img = mutate.MediaType(img, types.OCIManifestSchema1)
	hexPart := strings.TrimPrefix(digest, "sha256:")
	ref, err := name.NewTag(sigRepo+":sha256-"+hexPart+".sig", nameOpts...)
	if err != nil {
		return err
	}
	return remote.Write(ref, img, append(opts, remote.WithContext(ctx))...)
}

// HexDigest is "sha256:" plus the hex sha256 of b.
func HexDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
