// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification_test

import (
	"context"
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
