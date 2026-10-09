// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPublicGoodRoot_RefreshedOnTTL: the trusted root is fetched once, used
// until PublicGoodRootTTL, then fetched again; a failed refresh keeps the
// previous root and is retried after publicGoodRetry (QA #1521: it was
// fetched once and kept forever).
func TestPublicGoodRoot_RefreshedOnTTL(t *testing.T) {
	first, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	second, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	var fail atomic.Bool
	next := root.TrustedMaterial(first)
	c := newPublicGoodCache(func() (root.TrustedMaterial, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("tuf: 503")
		}
		return next, nil
	})

	got, err := c.get(context.Background(), now)
	require.NoError(t, err)
	assert.Same(t, first, got)
	now = now.Add(PublicGoodRootTTL - time.Minute)
	got, _ = c.get(context.Background(), now)
	assert.Same(t, first, got)
	assert.Equal(t, int32(1), calls.Load(), "cached within the TTL")

	// Past the TTL with TUF failing: the previous root, then no new fetch
	// until publicGoodRetry.
	now = now.Add(2 * time.Minute)
	fail.Store(true)
	got, err = c.get(context.Background(), now)
	require.NoError(t, err)
	assert.Same(t, first, got)
	assert.Equal(t, int32(2), calls.Load())
	got, _ = c.get(context.Background(), now)
	assert.Same(t, first, got)
	assert.Equal(t, int32(2), calls.Load(), "backing off after a failed refresh")

	now = now.Add(publicGoodRetry)
	fail.Store(false)
	next = second
	got, err = c.get(context.Background(), now)
	require.NoError(t, err)
	assert.Same(t, second, got, "refreshed")
}

// TestPublicGoodRoot_OneFetchNoLockHeld: concurrent callers share one fetch
// (singleflight), and a hung fetch does not hold them: they return when
// their context ends (QA #1521: the fetch ran under a global mutex).
func TestPublicGoodRoot_OneFetchNoLockHeld(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	release := make(chan struct{})
	var calls atomic.Int32
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	c := newPublicGoodCache(func() (root.TrustedMaterial, error) {
		calls.Add(1)
		<-release
		return vs, nil
	})

	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_, errs[i] = c.get(ctx, now)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "still being fetched")
	}
	assert.Equal(t, int32(1), calls.Load(), "one fetch for every caller")
	close(release)
	require.Eventually(t, func() bool {
		got, err := c.get(context.Background(), now)
		return err == nil && got == root.TrustedMaterial(vs)
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, int32(1), calls.Load())
}

// TestDockerConfigKeychain_DockerHub: the usual Docker Hub entry
// ("https://index.docker.io/v1/") authenticates docker.io images, and a host
// with :443 or upper case matches its entry (QA #1521: the key kept "/v1"
// and never matched).
func TestDockerConfigKeychain_DockerHub(t *testing.T) {
	kc, err := dockerConfigKeychain([]byte(`{"auths":{
		"https://index.docker.io/v1/":{"username":"hub","password":"p1"},
		"Registry.Example:443":{"username":"ex","password":"p2"}}}`))
	require.NoError(t, err)
	for ref, user := range map[string]string{
		"nginx":                          "hub",
		"index.docker.io/library/nginx":  "hub",
		"registry-1.docker.io/myorg/app": "hub",
		"docker.io/myorg/app":            "hub",
		"registry.example/app":           "ex",
		"registry.example:443/app":       "ex",
		"other.example/app":              "",
	} {
		r, err := name.NewRepository(ref)
		require.NoError(t, err)
		a, err := kc.Resolve(r)
		require.NoError(t, err)
		cfg, err := a.Authorization()
		require.NoError(t, err)
		if user == "" {
			assert.Equal(t, authn.Anonymous, a, ref)
			continue
		}
		assert.Equal(t, user, cfg.Username, ref)
	}
	_, err = dockerConfigKeychain([]byte(`{"auths": SECRET`))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SECRET")
}

// TestDockerConfigKeychain_DockerHubAliases: every spelling of Docker Hub in
// a .dockerconfigjson (docker.io, registry-1.docker.io, index.docker.io,
// with or without scheme and path) authenticates every spelling of a Docker
// Hub image (QA #1521 round 2).
func TestDockerConfigKeychain_DockerHubAliases(t *testing.T) {
	for _, key := range []string{"docker.io", "registry-1.docker.io", "https://index.docker.io/v1/", "https://docker.io", "DOCKER.IO:443"} {
		kc, err := dockerConfigKeychain([]byte(`{"auths":{"` + key + `":{"username":"hub","password":"p"}}}`))
		require.NoError(t, err)
		for _, ref := range []string{"nginx", "docker.io/library/nginx", "index.docker.io/myorg/app", "registry-1.docker.io/myorg/app"} {
			r, err := name.NewRepository(ref)
			require.NoError(t, err)
			a, err := kc.Resolve(r)
			require.NoError(t, err)
			cfg, err := a.Authorization()
			require.NoError(t, err)
			assert.Equal(t, "hub", cfg.Username, "%s for %s", key, ref)
		}
	}
}
