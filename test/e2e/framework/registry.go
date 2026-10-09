// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
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
	// EnvPrivateRegistryAPI is the private registry from the host.
	EnvPrivateRegistryAPI = "KARDINAL_E2E_PRIVATE_REGISTRY_API"
	// EnvPrivateRegistryUser and EnvPrivateRegistryPassword are its login.
	EnvPrivateRegistryUser     = "KARDINAL_E2E_PRIVATE_REGISTRY_USER"
	EnvPrivateRegistryPassword = "KARDINAL_E2E_PRIVATE_REGISTRY_PASSWORD"
)

// SeedTags are the podinfo tags registry.sh seeds (their linux/amd64
// images). They are real podinfo releases with distinct build times.
var SeedTags = []string{"6.13.0", "6.14.0", "6.15.0"}

const manifestTypes = "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

// Registry is one of the suite's in-cluster OCI registries.
type Registry struct {
	api, inCluster, seed string
	// user and password are the private registry's login; empty for the
	// public one.
	user, password string
	// from is the public registry the private one copies seed tags from.
	from *Registry
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

// NewPrivateRegistry reads the private registry (htpasswd login) from the
// env file. Copy pushes seed tags into it from the public registry.
func NewPrivateRegistry(t *testing.T) *Registry {
	t.Helper()
	pub := NewRegistry(t)
	r := &Registry{
		api:       strings.TrimRight(os.Getenv(EnvPrivateRegistryAPI), "/"),
		inCluster: strings.TrimRight(os.Getenv(EnvPrivateRegistry), "/"),
		seed:      pub.seed,
		user:      os.Getenv(EnvPrivateRegistryUser),
		password:  os.Getenv(EnvPrivateRegistryPassword),
		from:      pub,
	}
	if r.api == "" || r.inCluster == "" || r.user == "" || r.password == "" {
		t.Fatalf("%s, %s, %s and %s must be set; the core suite runs hack/e2e/components/registry.sh",
			EnvPrivateRegistryAPI, EnvPrivateRegistry, EnvPrivateRegistryUser, EnvPrivateRegistryPassword)
	}
	return r
}

// Login is the private registry's username and password ("" for the public one).
func (r *Registry) Login() (string, string) { return r.user, r.password }

// Host is the registry's in-cluster host:port.
func (r *Registry) Host() string {
	return strings.TrimPrefix(strings.TrimPrefix(r.inCluster, "http://"), "https://")
}

// headers adds the login, when the registry has one, to h.
func (r *Registry) headers(h map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		out[k] = v
	}
	if r.user != "" {
		out["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(r.user+":"+r.password))
	}
	return out
}

// pushBlob uploads content to repo (monolithic upload) unless the registry
// has it, and returns its digest.
func (r *Registry) pushBlob(t *testing.T, repo string, content []byte) string {
	t.Helper()
	ctx := context.Background()
	sum := sha256.Sum256(content)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if head, err := doHTTP(ctx, http.MethodHead, r.api+"/v2/"+repo+"/blobs/"+digest, r.headers(nil), nil); err == nil && head.Status == http.StatusOK {
		return digest
	}
	start, err := doHTTP(ctx, http.MethodPost, r.api+"/v2/"+repo+"/blobs/uploads/", r.headers(nil), nil)
	if err != nil || start.Status != http.StatusAccepted {
		t.Fatalf("start blob upload to %s: %v HTTP %d %s", repo, err, start.Status, clip(start.Body))
	}
	loc, err := url.Parse(start.Header.Get("Location"))
	if err != nil {
		t.Fatalf("blob upload Location %q: %v", start.Header.Get("Location"), err)
	}
	base, _ := url.Parse(r.api)
	loc = base.ResolveReference(loc)
	q := loc.Query()
	q.Set("digest", digest)
	loc.RawQuery = q.Encode()
	put, err := doHTTP(ctx, http.MethodPut, loc.String(), r.headers(map[string]string{"Content-Type": "application/octet-stream"}), content)
	if err != nil || put.Status != http.StatusCreated {
		t.Fatalf("upload blob %s to %s: %v HTTP %d %s", digest, repo, err, put.Status, clip(put.Body))
	}
	return digest
}

// putManifest puts a manifest as tag and returns its digest.
func (r *Registry) putManifest(t *testing.T, repo, tag, mediaType string, body []byte) string {
	t.Helper()
	put, err := doHTTP(context.Background(), http.MethodPut, r.api+"/v2/"+repo+"/manifests/"+tag,
		r.headers(map[string]string{"Content-Type": mediaType}), body)
	if err != nil || put.Status != http.StatusCreated {
		t.Fatalf("push %s:%s: %v HTTP %d %s", repo, tag, err, put.Status, clip(put.Body))
	}
	digest := put.Header.Get("Docker-Content-Digest")
	if digest == "" {
		t.Fatalf("push %s:%s: no Docker-Content-Digest in the response", repo, tag)
	}
	return digest
}

// PushChart pushes a minimal Helm chart, name at version, to the OCI
// repository repo/name the way helm push does (config
// application/vnd.cncf.helm.config.v1+json, one chart layer; "+" in the
// version becomes "_" in the tag) and returns the manifest digest.
func (r *Registry) PushChart(t *testing.T, repo, name, version string) string {
	t.Helper()
	chartYAML := fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", name, version)
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name + "/Chart.yaml", Mode: 0o644, Size: int64(len(chartYAML))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(chartYAML)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := []byte(fmt.Sprintf(`{"apiVersion":"v2","name":%q,"version":%q}`, name, version))
	full := repo + "/" + name
	cfgDigest := r.pushBlob(t, full, cfg)
	layerDigest := r.pushBlob(t, full, tgz.Bytes())
	manifest := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",`+
		`"config":{"mediaType":"application/vnd.cncf.helm.config.v1+json","digest":%q,"size":%d},`+
		`"layers":[{"mediaType":"application/vnd.cncf.helm.chart.content.v1.tar+gzip","digest":%q,"size":%d}]}`,
		cfgDigest, len(cfg), layerDigest, tgz.Len())
	digest := r.putManifest(t, full, strings.ReplaceAll(version, "+", "_"), "application/vnd.oci.image.manifest.v1+json", []byte(manifest))
	t.Logf("pushed chart %s:%s %s", full, version, digest)
	return digest
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
	if r.from != nil {
		return r.copyFrom(t, src, repo, dst)
	}
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

// copyFrom copies seed tag src of the public registry into r's repo as tag
// dst, blob by blob (a mount cannot cross registries), and returns the
// manifest digest, the same as the public copy's.
func (r *Registry) copyFrom(t *testing.T, src, repo, dst string) string {
	t.Helper()
	ctx := context.Background()
	pub := r.from
	get, err := doHTTP(ctx, http.MethodGet, pub.api+"/v2/"+pub.seed+"/manifests/"+src, map[string]string{"Accept": manifestTypes}, nil)
	if err != nil || get.Status != http.StatusOK {
		t.Fatalf("read seed manifest %s:%s: %v HTTP %d %s", pub.seed, src, err, get.Status, clip(get.Body))
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
		t.Fatalf("decode seed manifest %s:%s: %v", pub.seed, src, err)
	}
	blobs := []string{m.Config.Digest}
	for _, l := range m.Layers {
		blobs = append(blobs, l.Digest)
	}
	for _, d := range blobs {
		body, err := readBlob(ctx, pub.api+"/v2/"+pub.seed+"/blobs/"+d)
		if err != nil {
			t.Fatalf("read seed blob %s: %v", d, err)
		}
		if got := r.pushBlob(t, repo, body); got != d {
			t.Fatalf("blob %s uploaded as %s", d, got)
		}
	}
	digest := r.putManifest(t, repo, dst, get.Header.Get("Content-Type"), []byte(get.Body))
	t.Logf("pushed %s:%s (podinfo %s) to the private registry %s", repo, dst, src, digest)
	return digest
}

// readBlob reads a whole blob (image layers are larger than doHTTP's limit).
func readBlob(ctx context.Context, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
}
