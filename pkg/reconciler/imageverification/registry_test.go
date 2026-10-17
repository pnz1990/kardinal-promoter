// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification_test

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	iv "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
)

// TestOCIRegistry_Signatures pushes an image to an in-memory OCI registry,
// attaches a Sigstore bundle referrer and a legacy .sig, and reads both
// back, next to the image and from a separate signature repository.
func TestOCIRegistry_Signatures(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := mustHost(t, srv.URL)
	ctx := context.Background()

	img, err := random.Image(256, 1)
	require.NoError(t, err)
	d, err := img.Digest()
	require.NoError(t, err)
	repo := host + "/app"
	ref, err := name.NewDigest(repo+"@"+d.String(), name.Insecure)
	require.NoError(t, err)
	require.NoError(t, remote.Write(ref, img))

	insecure := []name.Option{name.Insecure}
	bundle, _, err := signtest.Bundle(repo, d.String())
	require.NoError(t, err)
	require.NoError(t, signtest.PushBundle(ctx, repo, d.String(), bundle, insecure))
	key, err := signtest.NewKey()
	require.NoError(t, err)
	payload := signtest.LegacyPayload(repo, d.String())
	sig, err := key.SignLegacy(payload)
	require.NoError(t, err)
	require.NoError(t, signtest.PushLegacy(ctx, repo, d.String(), payload, sig, insecure))

	// The test server is on loopback, which the egress guard refuses.
	r := &iv.OCIRegistry{Transport: http.DefaultTransport}
	got, err := r.Signatures(ctx, repo, d.String(), iv.RegistryOptions{Insecure: []string{host}})
	require.NoError(t, err)
	require.Len(t, got.Bundles, 1)
	assert.JSONEq(t, string(bundle), string(got.Bundles[0]))
	require.Len(t, got.Legacy, 1)
	assert.Equal(t, payload, got.Legacy[0].Payload)
	assert.Equal(t, sig, got.Legacy[0].Signature)

	// An image with no signatures: none, and no error.
	other, err := random.Image(128, 1)
	require.NoError(t, err)
	od, err := other.Digest()
	require.NoError(t, err)
	none, err := r.Signatures(ctx, repo, od.String(), iv.RegistryOptions{Insecure: []string{host}})
	require.NoError(t, err)
	assert.Empty(t, none.Bundles)
	assert.Empty(t, none.Legacy)

	// Signatures in another repository (cosign's COSIGN_REPOSITORY).
	sigRepo := host + "/signatures"
	require.NoError(t, signtest.PushBundle(ctx, sigRepo, od.String(), bundle, insecure))
	got, err = r.Signatures(ctx, "ghcr.io/org/app", od.String(),
		iv.RegistryOptions{Insecure: []string{host}, SignatureRepository: sigRepo})
	require.NoError(t, err)
	assert.Len(t, got.Bundles, 1)
}

// TestOCIRegistry_EgressGuard: the default transport refuses loopback
// registries (pkg/egress).
func TestOCIRegistry_EgressGuard(t *testing.T) {
	_, err := (&iv.OCIRegistry{}).Signatures(context.Background(), "127.0.0.1:5000/app",
		"sha256:"+strings.Repeat("a", 64), iv.RegistryOptions{Insecure: []string{"127.0.0.1:5000"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not allowed")
}

// TestOCIRegistry_RefusesPlainHTTP: a registry not listed as insecure is
// never read over plain HTTP, even one go-containerregistry would fall back
// to HTTP for (localhost, RFC 1918).
func TestOCIRegistry_RefusesPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := mustHost(t, srv.URL)
	img, err := random.Image(64, 1)
	require.NoError(t, err)
	d, err := img.Digest()
	require.NoError(t, err)
	_, err = (&iv.OCIRegistry{Transport: http.DefaultTransport}).Signatures(context.Background(), host+"/app", d.String(), iv.RegistryOptions{})
	require.Error(t, err)
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u.Host
}

// TestOCIRegistry_AttestationsDoNotCrowdOutSignatures: an image carrying 40
// Sigstore bundle referrers of another predicate (promotion receipts, one per
// environment it reached) and one cosign signature: the attestations are
// skipped before the maxSignatures bound, so the signature is still read. A
// bundle that names no predicate is read, as before.
func TestOCIRegistry_AttestationsDoNotCrowdOutSignatures(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := mustHost(t, srv.URL)
	ctx := context.Background()
	img, err := random.Image(256, 1)
	require.NoError(t, err)
	d, err := img.Digest()
	require.NoError(t, err)
	repo := host + "/app"
	ref, err := name.NewDigest(repo+"@"+d.String(), name.Insecure)
	require.NoError(t, err)
	require.NoError(t, remote.Write(ref, img))
	insecure := []name.Option{name.Insecure}
	key, err := signtest.NewKey()
	require.NoError(t, err)

	const vsa = "https://slsa.dev/verification_summary/v1"
	for i := range 40 {
		b, err := key.BundleWithPredicate(repo, d.String(), vsa)
		require.NoError(t, err)
		// Each receipt is a distinct referrer.
		require.NoError(t, signtest.PushBundleAnnotated(ctx, repo, d.String(), b,
			map[string]string{"dev.sigstore.bundle.predicateType": vsa, "e2e/n": fmt.Sprint(i)}, insecure))
	}
	sig, err := key.Bundle(repo, d.String())
	require.NoError(t, err)
	require.NoError(t, signtest.PushBundleAnnotated(ctx, repo, d.String(), sig,
		map[string]string{"dev.sigstore.bundle.predicateType": iv.CosignSignPredicate}, insecure))
	unnamed, err := key.Bundle(repo, d.String()+"x")
	require.NoError(t, err)
	require.NoError(t, signtest.PushBundle(ctx, repo, d.String(), unnamed, insecure))

	r := &iv.OCIRegistry{Transport: http.DefaultTransport}
	got, err := r.Signatures(ctx, repo, d.String(), iv.RegistryOptions{Insecure: []string{host}})
	require.NoError(t, err)
	require.Len(t, got.Bundles, 2, "the signature and the unnamed bundle; no receipt")
	var texts []string
	for _, b := range got.Bundles {
		texts = append(texts, string(b))
	}
	assert.Contains(t, texts, string(sig))
}

// TestOCIRegistry_ReceiptsDoNotCrowdOutAttestations: a policy that verifies
// VSAs (to raise a build level) reads the builder's VSA and the image
// signature although the image carries 70 of kardinal's promotion receipts,
// VSAs too (kardinal.io/receipt), and 80 SBOM attestations the policy also
// names: receipts are never read, and attestations have their own budget.
func TestOCIRegistry_ReceiptsDoNotCrowdOutAttestations(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := mustHost(t, srv.URL)
	ctx := context.Background()
	img, err := random.Image(256, 1)
	require.NoError(t, err)
	d, err := img.Digest()
	require.NoError(t, err)
	repo := host + "/app"
	ref, err := name.NewDigest(repo+"@"+d.String(), name.Insecure)
	require.NoError(t, err)
	require.NoError(t, remote.Write(ref, img))
	insecure := []name.Option{name.Insecure}
	key, err := signtest.NewKey()
	require.NoError(t, err)

	const vsa, spdx = "https://slsa.dev/verification_summary/v1", "https://spdx.dev/Document"
	for i := range 70 {
		b, err := key.BundleWithPredicate(repo, d.String(), vsa)
		require.NoError(t, err)
		require.NoError(t, signtest.PushBundleAnnotated(ctx, repo, d.String(), b,
			map[string]string{"dev.sigstore.bundle.predicateType": vsa, "kardinal.io/receipt": "true", "e2e/n": fmt.Sprint(i)}, insecure))
	}
	for i := range 80 {
		b, err := key.BundleWithPredicate(repo, d.String(), spdx)
		require.NoError(t, err)
		require.NoError(t, signtest.PushBundleAnnotated(ctx, repo, d.String(), b,
			map[string]string{"dev.sigstore.bundle.predicateType": spdx, "e2e/n": fmt.Sprint(i)}, insecure))
	}
	builderVSA, err := key.BundleWithPredicate(repo, d.String(), vsa)
	require.NoError(t, err)
	require.NoError(t, signtest.PushBundleAnnotated(ctx, repo, d.String(), builderVSA,
		map[string]string{"dev.sigstore.bundle.predicateType": vsa, "e2e/n": "builder"}, insecure))
	sig, err := key.Bundle(repo, d.String())
	require.NoError(t, err)
	require.NoError(t, signtest.PushBundleAnnotated(ctx, repo, d.String(), sig,
		map[string]string{"dev.sigstore.bundle.predicateType": iv.CosignSignPredicate}, insecure))

	r := &iv.OCIRegistry{Transport: http.DefaultTransport}
	got, err := r.Signatures(ctx, repo, d.String(), iv.RegistryOptions{Insecure: []string{host}, AttestationTypes: []string{vsa}})
	require.NoError(t, err)
	require.Len(t, got.Bundles, 1, "the signature")
	assert.Equal(t, string(sig), string(got.Bundles[0]))
	require.Len(t, got.Attestations, 1, "the builder's VSA; no receipt")
	assert.Equal(t, string(builderVSA), string(got.Attestations[0]))

	got, err = r.Signatures(ctx, repo, d.String(), iv.RegistryOptions{Insecure: []string{host}, AttestationTypes: []string{vsa, spdx}})
	require.NoError(t, err)
	assert.Len(t, got.Bundles, 1, "the signature, whatever the number of attestations")
	assert.Len(t, got.Attestations, 64, "attestations are bounded on their own")
}

// TestOCIRegistry_ReferrersIndexIsBounded: a referrers index over
// MaxIndexBytes fails the read, and so the verification, closed, whether
// the registry declares its length or streams it.
func TestOCIRegistry_ReferrersIndexIsBounded(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	huge := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[],"annotations":{"pad":"` +
		strings.Repeat("x", iv.MaxIndexBytes) + `"}}`
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked=%v", chunked), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v2/":
					w.WriteHeader(http.StatusOK)
				case strings.Contains(r.URL.Path, "/referrers/"):
					w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
					if !chunked {
						w.Header().Set("Content-Length", fmt.Sprint(len(huge)))
					}
					_, _ = io.WriteString(w, huge[:len(huge)/2])
					if f, ok := w.(http.Flusher); ok && chunked {
						f.Flush()
					}
					_, _ = io.WriteString(w, huge[len(huge)/2:])
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			host := mustHost(t, srv.URL)
			r := &iv.OCIRegistry{Transport: http.DefaultTransport}
			_, err := r.Signatures(context.Background(), host+"/app", digest, iv.RegistryOptions{Insecure: []string{host}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "over 4 MiB")
		})
	}
}
