// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package source_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

const testIndex = `apiVersion: v1
entries:
  podinfo:
    - version: 6.15.0
      digest: aaaaaaaa15
      urls: [charts/podinfo-6.15.0.tgz]
    - version: 6.14.0
      digest: aaaaaaaa14
    - version: 7.0.0-rc.1
      digest: aaaaaaaa70
    - version: latest
      digest: notsemver
  other:
    - version: 1.0.0
`

// helmRepo serves index (mutable) behind optional basic auth.
type helmRepo struct {
	mu         sync.Mutex
	index      string
	user, pass string
}

func (h *helmRepo) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.user != "" {
			if u, p, ok := r.BasicAuth(); !ok || u != h.user || p != h.pass {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if r.URL.Path != "/charts/index.yaml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(h.index))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestHelmWatcher_HTTPIndex covers an HTTP chart repository: the highest
// semantic version wins (pre-releases count without a constraint, a
// constraint without a pre-release excludes them, non-semver versions are
// ignored), the index digest identifies the version, a new version is a
// change, and basic auth comes from the credentials.
func TestHelmWatcher_HTTPIndex(t *testing.T) {
	repo := &helmRepo{index: testIndex, user: "helm", pass: "chart-pass"}
	srv := repo.start(t)
	watch := func(filters source.TagFilters, creds source.Credentials, last string) (*source.WatchResult, error) {
		w := source.NewHelmWatcher(srv.URL+"/charts", "podinfo").WithHTTPClient(srv.Client())
		w.Filters, w.Credentials = filters, creds
		return w.Watch(context.Background(), last)
	}
	login := source.Credentials{Username: "helm", Password: "chart-pass"}

	r, err := watch(source.TagFilters{}, login, "")
	require.NoError(t, err)
	assert.Equal(t, "7.0.0-rc.1", r.Tag)
	assert.Equal(t, "sha256:aaaaaaaa70", r.Digest)
	assert.False(t, r.Changed)

	r, err = watch(source.TagFilters{SemverConstraint: ">=6"}, login, "sha256:aaaaaaaa14")
	require.NoError(t, err)
	assert.Equal(t, "6.15.0", r.Tag)
	assert.True(t, r.Changed)

	r, err = watch(source.TagFilters{SemverConstraint: "~6.14"}, login, "sha256:aaaaaaaa14")
	require.NoError(t, err)
	assert.Equal(t, "6.14.0", r.Tag)
	assert.False(t, r.Changed)

	r, err = watch(source.TagFilters{Ignore: []string{"7.0.0-rc.1", "6.15.0"}}, login, "")
	require.NoError(t, err)
	assert.Equal(t, "6.14.0", r.Tag)

	_, err = watch(source.TagFilters{SemverConstraint: ">=8"}, login, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no version passes the filters (semverConstraint ">=8"; 4 versions listed)`)

	_, err = watch(source.TagFilters{}, source.Credentials{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication required")
	assert.Contains(t, err.Error(), "set secretRef")

	_, err = watch(source.TagFilters{}, source.Credentials{Username: "helm", Password: "nope"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "with the credentials from secretRef")

	w := source.NewHelmWatcher(srv.URL+"/charts", "missing").WithHTTPClient(srv.Client())
	w.Credentials = login
	_, err = w.Watch(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `the index has no chart "missing" (charts: other, podinfo)`)

	repo.mu.Lock()
	repo.index = "apiVersion: v1\nentries:\n  podinfo:\n    - version: 6.16.0\n"
	repo.mu.Unlock()
	r, err = watch(source.TagFilters{}, login, "sha256:aaaaaaaa70")
	require.NoError(t, err)
	assert.Equal(t, "6.16.0", r.Tag)
	assert.True(t, r.Changed)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, r.Digest, "a version without an index digest still gets a stable one")
	again, err := watch(source.TagFilters{}, login, r.Digest)
	require.NoError(t, err)
	assert.False(t, again.Changed)
}

// TestHelmWatcher_OCI covers an OCI chart: the tags are the versions ("_" is
// "+"), the highest version's manifest digest identifies it, and registry
// credentials apply.
func TestHelmWatcher_OCI(t *testing.T) {
	reg := &testRegistry{name: "charts/podinfo", auth: "bearer", user: "u", pass: "p",
		tags:   []string{"6.14.0", "6.15.0", "6.15.0_build.1", "sha256-abc.sig"},
		images: imagesWithDigests("6.14.0", "6.15.0", "6.15.0_build.1")}
	srv := reg.start(t)
	host := hostOf(t, srv.URL)

	w := source.NewHelmWatcher("oci+http://"+host+"/charts", "podinfo").WithHTTPClient(srv.Client())
	w.Credentials = source.Credentials{Username: "u", Password: "p"}
	r, err := w.Watch(context.Background(), "sha256:6.14.0")
	require.NoError(t, err)
	assert.Equal(t, "6.15.0+build.1", r.Tag, "build metadata sorts by name among equal versions")
	assert.Equal(t, "sha256:6.15.0_build.1", r.Digest)
	assert.True(t, r.Changed)

	w.Filters = source.TagFilters{Exclude: `\+`}
	r, err = w.Watch(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "6.15.0", r.Tag)

	w.Credentials = source.Credentials{}
	_, err = w.Watch(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set secretRef")

	for _, bad := range []string{"a/b", "x@y"} {
		_, err := source.NewHelmWatcher("oci://ghcr.io/org", bad).Watch(context.Background(), "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a chart name")
	}
	_, err = source.NewHelmWatcher("ftp://charts", "podinfo").Watch(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repoURL must be an https://")
}
