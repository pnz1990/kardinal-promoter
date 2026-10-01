// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

// Environment variables hack/e2e/components/registry.sh sets.
const (
	// EnvRegistry is the public OCI registry's base URL in the cluster.
	EnvRegistry = "KARDINAL_E2E_REGISTRY"
	// EnvRegistryAPI is the same registry from the host.
	EnvRegistryAPI = "KARDINAL_E2E_REGISTRY_API"
	// EnvRegistrySeed is the repository seeded with podinfo tags.
	EnvRegistrySeed = "KARDINAL_E2E_REGISTRY_SEED"
	// EnvPrivateRegistry is a registry that refuses every anonymous request.
	EnvPrivateRegistry = "KARDINAL_E2E_PRIVATE_REGISTRY"
)

// SeedTags are the podinfo tags registry.sh seeds (their linux/amd64
// images). They are real podinfo releases with distinct build times.
var SeedTags = []string{"6.13.0", "6.14.0", "6.15.0"}

const manifestTypes = "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// Registry is the suite's in-cluster OCI registry.
type Registry struct {
	api, inCluster, seed string
}

// NewRegistry reads the registry from the env file; it fails the test when
// the suite has none.
func NewRegistry(t *testing.T) *Registry {
	t.Helper()
	r := &Registry{
		api:       strings.TrimRight(os.Getenv(EnvRegistryAPI), "/"),
		inCluster: strings.TrimRight(os.Getenv(EnvRegistry), "/"),
		seed:      os.Getenv(EnvRegistrySeed),
	}
	if r.api == "" || r.inCluster == "" || r.seed == "" {
		t.Fatalf("%s, %s and %s must be set; the core suite runs hack/e2e/components/registry.sh",
			EnvRegistryAPI, EnvRegistry, EnvRegistrySeed)
	}
	return r
}

// Ref is repo's reference as a Subscription's spec.image.registry takes it
// (with the http:// scheme, because the registry is plain HTTP).
func (r *Registry) Ref(repo string) string { return r.inCluster + "/" + repo }

// Repository is repo's image repository as a Bundle records it: Ref without
// the scheme.
func (r *Registry) Repository(repo string) string {
	return strings.TrimPrefix(strings.TrimPrefix(r.Ref(repo), "http://"), "https://")
}

// Copy pushes seed tag src into repo as tag dst (mounting the seed's blobs,
// then putting the same manifest) and returns the manifest digest. The digest
// depends only on src: every copy of a tag has the same one.
func (r *Registry) Copy(t *testing.T, src, repo, dst string) string {
	t.Helper()
	ctx := context.Background()
	get, err := doHTTP(ctx, http.MethodGet, r.api+"/v2/"+r.seed+"/manifests/"+src, map[string]string{"Accept": manifestTypes}, nil)
	if err != nil || get.Status != http.StatusOK {
		t.Fatalf("read seed manifest %s:%s: %v HTTP %d %s", r.seed, src, err, get.Status, clip(get.Body))
	}
	var m struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
	}
	if err := json.Unmarshal([]byte(get.Body), &m); err != nil {
		t.Fatalf("decode seed manifest %s:%s: %v", r.seed, src, err)
	}
	blobs := []string{m.Config.Digest}
	for _, l := range m.Layers {
		blobs = append(blobs, l.Digest)
	}
	for _, d := range blobs {
		res, err := doHTTP(ctx, http.MethodPost,
			fmt.Sprintf("%s/v2/%s/blobs/uploads/?mount=%s&from=%s", r.api, repo, d, r.seed), nil, nil)
		if err != nil || res.Status != http.StatusCreated {
			t.Fatalf("mount blob %s into %s: %v HTTP %d %s", d, repo, err, res.Status, clip(res.Body))
		}
	}
	put, err := doHTTP(ctx, http.MethodPut, r.api+"/v2/"+repo+"/manifests/"+dst,
		map[string]string{"Content-Type": get.Header.Get("Content-Type")}, []byte(get.Body))
	if err != nil || put.Status != http.StatusCreated {
		t.Fatalf("push %s:%s: %v HTTP %d %s", repo, dst, err, put.Status, clip(put.Body))
	}
	digest := put.Header.Get("Docker-Content-Digest")
	if digest == "" {
		t.Fatalf("push %s:%s: no Docker-Content-Digest in the response", repo, dst)
	}
	t.Logf("pushed %s:%s (podinfo %s) %s", repo, dst, src, digest)
	return digest
}
