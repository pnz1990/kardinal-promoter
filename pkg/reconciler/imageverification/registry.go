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
	// predicateTypeAnnotation names the in-toto predicate of a Sigstore
	// bundle referrer (cosign sets it, and so do kardinal's promotion
	// receipts). A referrer that names a predicate the policy does not
	// verify (an image signature's or a named attestation's) is skipped
	// before it is counted. One that names none is read and checked.
	predicateTypeAnnotation = "dev.sigstore.bundle.predicateType"
	// receiptAnnotation marks kardinal's own promotion receipts
	// (docs/receipts.md). They are VSAs, the predicate type a policy may
	// name to raise a build level, and an image gets one per environment it
	// reaches: they are skipped before they are counted, so they never
	// crowd out the builder's attestations or a signature.
	receiptAnnotation = "kardinal.io/receipt"
	// cosignSignatureAnnotation holds a legacy signature, base64.
	cosignSignatureAnnotation = "dev.cosignproject.cosign/signature"
	// cosignCertificateAnnotation holds a legacy keyless certificate.
	cosignCertificateAnnotation = "dev.sigstore.cosign/certificate"
	// maxSignatureBytes bounds what is read per signature blob.
	maxSignatureBytes = 1 << 20
	// MaxIndexBytes bounds what is read of any registry response that is
	// not a blob: the referrers index (the OCI referrers API or its tag
	// fallback) and manifests. go-containerregistry alone would read up to
	// 100 MiB of an index; an image with more referrers than 4 MiB lists
	// (tens of thousands) fails its read, and so its verification, closed.
	MaxIndexBytes = 4 << 20
	// maxSignatures bounds the signature bundles read per image, and
	// maxAttestations the attestation bundles. The budgets are separate,
	// so no number of attestations crowds out a signature. A bundle that
	// names no predicate counts against each budget it is read for.
	maxSignatures   = 32
	maxAttestations = 64
)

// RegistryOptions say how to reach the registries of one ImageVerification.
type RegistryOptions struct {
	// Keychain authenticates; nil is anonymous.
	Keychain authn.Keychain
	// Insecure are the registry hosts that may be read over plain HTTP.
	Insecure []string
	// SignatureRepository, when set, is where signatures are looked up.
	SignatureRepository string
	// AttestationTypes are the in-toto predicate types whose bundles are
	// read as attestations (Signatures.Attestations), besides signatures.
	AttestationTypes []string
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
	return httpsOnly{base: boundedIndexes{base: r.Transport}, insecure: insecure}
}

// errIndexTooLarge is a registry response over MaxIndexBytes.
var errIndexTooLarge = fmt.Errorf("a referrers index or manifest over %d MiB", MaxIndexBytes>>20)

// boundedIndexes caps every response that is not a blob at MaxIndexBytes:
// one declared larger is refused before its body is read, and one whose
// body runs past it fails the read.
type boundedIndexes struct{ base http.RoundTripper }

func (b boundedIndexes) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := b.base.RoundTrip(req)
	if err != nil || strings.Contains(req.URL.Path, "/blobs/") {
		return resp, err
	}
	if resp.ContentLength > MaxIndexBytes {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s %s: %w (Content-Length %d)", req.Method, req.URL.Path, errIndexTooLarge, resp.ContentLength)
	}
	resp.Body = &cappedBody{rc: resp.Body, left: MaxIndexBytes, what: req.URL.Path}
	return resp, nil
}

// cappedBody fails a read past its limit, so a truncated index is never
// parsed as a smaller one.
type cappedBody struct {
	rc   io.ReadCloser
	left int64
	what string
}

func (c *cappedBody) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		n, err := c.rc.Read(one[:])
		if n > 0 {
			return 0, fmt.Errorf("%s: %w", c.what, errIndexTooLarge)
		}
		return 0, err
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.rc.Read(p)
	c.left -= int64(n)
	return n, err
}

func (c *cappedBody) Close() error { return c.rc.Close() }

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
		signatures, attestations := 0, 0
		for _, d := range m.Manifests {
			if !strings.HasPrefix(d.ArtifactType, sigstoreBundlePrefix) || d.Annotations[receiptAnnotation] != "" {
				continue
			}
			// A referrer that names its predicate is read only for the
			// signature or an attestation type the policy names; one that
			// names none may be either, and verification decides.
			p, named := d.Annotations[predicateTypeAnnotation]
			signature := (!named || p == CosignSignPredicate) && signatures < maxSignatures
			attestation := ((!named && len(opts.AttestationTypes) > 0) || (named && slices.Contains(opts.AttestationTypes, p))) &&
				attestations < maxAttestations
			if !signature && !attestation {
				continue
			}
			b, err := firstLayer(ref.Context().Digest(d.Digest.String()), ropts)
			if err != nil {
				return Signatures{}, fmt.Errorf("read bundle %s: %w", d.Digest, err)
			}
			if signature {
				signatures++
				out.Bundles = append(out.Bundles, b)
			}
			if attestation {
				attestations++
				out.Attestations = append(out.Attestations, b)
			}
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
