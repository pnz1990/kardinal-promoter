// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
)

// Media types and annotations of cosign and Sigstore signatures.
const (
	// sigstoreBundlePrefix prefixes the artifact type of a Sigstore bundle
	// referrer (application/vnd.dev.sigstore.bundle.v0.3+json).
	sigstoreBundlePrefix = "application/vnd.dev.sigstore.bundle"
	// cosignSignatureAnnotation holds a legacy signature, base64.
	cosignSignatureAnnotation = "dev.cosignproject.cosign/signature"
	// cosignCertificateAnnotation holds a legacy keyless certificate.
	cosignCertificateAnnotation = "dev.sigstore.cosign/certificate"
	// maxSignatureBytes bounds what is read per signature blob.
	maxSignatureBytes = 1 << 20
	// maxSignatures bounds the signatures read per image.
	maxSignatures = 32
)

// RegistryOptions say how to reach the registries of one ImageVerification.
type RegistryOptions struct {
	// Keychain authenticates; nil is anonymous.
	Keychain authn.Keychain
	// Insecure are the registry hosts that may be read over plain HTTP.
	Insecure []string
	// SignatureRepository, when set, is where signatures are looked up.
	SignatureRepository string
}

// RegistryClient fetches the signatures of an image.
type RegistryClient interface {
	// Signatures returns the Sigstore bundles attached to repository@digest
	// as OCI referrers and its legacy cosign signatures. Missing signatures
	// are not an error; an unreachable or failing registry is.
	Signatures(ctx context.Context, repository, digest string, opts RegistryOptions) (Signatures, error)
}

// OCIRegistry is the RegistryClient for OCI registries.
type OCIRegistry struct {
	// Transport is the base HTTP transport; nil means the egress-guarded
	// default (no loopback, link-local or cloud metadata addresses), with a
	// response header timeout.
	Transport http.RoundTripper

	once sync.Once
}

// responseHeaderTimeout bounds the wait for a registry's response headers;
// the caller's context bounds the whole fetch.
const responseHeaderTimeout = 20 * time.Second

func (r *OCIRegistry) transport(insecure []string) http.RoundTripper {
	r.once.Do(func() {
		if r.Transport == nil {
			t := egress.NewTransport(http.ProxyFromEnvironment)
			t.ResponseHeaderTimeout = responseHeaderTimeout
			r.Transport = t
		}
	})
	return httpsOnly{base: r.Transport, insecure: insecure}
}

// httpsOnly refuses plain-HTTP requests to hosts not listed as insecure.
// go-containerregistry falls back to HTTP for RFC 1918 and localhost
// registries on its own; a signature check must not.
type httpsOnly struct {
	base     http.RoundTripper
	insecure []string
}

func (h httpsOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "http" && !slices.Contains(h.insecure, req.URL.Host) {
		return nil, fmt.Errorf("refusing plain HTTP to %s: list it in spec.imageVerification.insecureRegistries", req.URL.Host)
	}
	return h.base.RoundTrip(req)
}

// Signatures implements RegistryClient.
func (r *OCIRegistry) Signatures(ctx context.Context, repository, digest string, opts RegistryOptions) (Signatures, error) {
	sigRepo := repository
	if opts.SignatureRepository != "" {
		sigRepo = opts.SignatureRepository
	}
	host := strings.SplitN(sigRepo, "/", 2)[0]
	nameOpts := []name.Option{}
	if slices.Contains(opts.Insecure, host) {
		nameOpts = append(nameOpts, name.Insecure)
	}
	ref, err := name.NewDigest(sigRepo+"@"+digest, nameOpts...)
	if err != nil {
		return Signatures{}, fmt.Errorf("signature reference %s@%s: %w", sigRepo, digest, err)
	}
	ropts := []remote.Option{remote.WithContext(ctx), remote.WithTransport(r.transport(opts.Insecure))}
	if opts.Keychain != nil {
		ropts = append(ropts, remote.WithAuthFromKeychain(opts.Keychain))
	}

	var out Signatures
	idx, err := remote.Referrers(ref, ropts...)
	if err != nil && !notFound(err) {
		return Signatures{}, fmt.Errorf("list referrers of %s: %w", ref, err)
	}
	if idx != nil {
		m, err := idx.IndexManifest()
		if err != nil {
			return Signatures{}, fmt.Errorf("referrers of %s: %w", ref, err)
		}
		for _, d := range m.Manifests {
			if !strings.HasPrefix(d.ArtifactType, sigstoreBundlePrefix) || len(out.Bundles) >= maxSignatures {
				continue
			}
			b, err := firstLayer(ref.Context().Digest(d.Digest.String()), ropts)
			if err != nil {
				return Signatures{}, fmt.Errorf("read bundle %s: %w", d.Digest, err)
			}
			out.Bundles = append(out.Bundles, b)
		}
	}

	legacy, err := legacySignatures(ref, ropts)
	if err != nil {
		return Signatures{}, err
	}
	out.Legacy = legacy
	return out, nil
}

// legacySignatures reads the cosign sha256-<hex>.sig tag.
func legacySignatures(ref name.Digest, ropts []remote.Option) ([]LegacySignature, error) {
	tag := ref.Context().Tag(strings.Replace(ref.DigestStr(), ":", "-", 1) + ".sig")
	img, err := remote.Image(tag, ropts...)
	if notFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", tag, err)
	}
	m, err := img.Manifest()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", tag, err)
	}
	var out []LegacySignature
	for _, l := range m.Layers {
		if len(out) >= maxSignatures {
			break
		}
		sig64, ok := l.Annotations[cosignSignatureAnnotation]
		if !ok {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(sig64)
		if err != nil {
			continue
		}
		payload, err := readBlob(ref.Context().Digest(l.Digest.String()), ropts)
		if err != nil {
			return nil, fmt.Errorf("read signature payload %s: %w", l.Digest, err)
		}
		out = append(out, LegacySignature{Payload: payload, Signature: sig,
			Certificate: []byte(l.Annotations[cosignCertificateAnnotation])})
	}
	return out, nil
}

// firstLayer returns the first layer of the manifest at ref.
func firstLayer(ref name.Digest, ropts []remote.Option) ([]byte, error) {
	img, err := remote.Image(ref, ropts...)
	if err != nil {
		return nil, err
	}
	m, err := img.Manifest()
	if err != nil {
		return nil, err
	}
	if len(m.Layers) == 0 {
		return nil, fmt.Errorf("no layers")
	}
	return readBlob(ref.Context().Digest(m.Layers[0].Digest.String()), ropts)
}

// readBlob reads a blob, at most maxSignatureBytes, and checks its digest.
func readBlob(ref name.Digest, ropts []remote.Option) ([]byte, error) {
	l, err := remote.Layer(ref, ropts...)
	if err != nil {
		return nil, err
	}
	rc, err := l.Compressed()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxSignatureBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSignatureBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxSignatureBytes)
	}
	want, err := v1.NewHash(ref.DigestStr())
	if err != nil {
		return nil, err
	}
	got, _, err := v1.SHA256(strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	if got != want {
		return nil, fmt.Errorf("blob digest %s does not match %s", got, want)
	}
	return b, nil
}

func notFound(err error) bool {
	var te *transport.Error
	if errors.As(err, &te) {
		return te.StatusCode == http.StatusNotFound
	}
	return false
}
