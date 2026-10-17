// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// paginated serves inner (an OCI registry), with its referrers index cut
// into pages of size, linked by Link: rel="next", as registries that
// paginate do.
func paginated(inner http.Handler, size int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/referrers/") {
			inner.ServeHTTP(w, r)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		q := r.URL.Query()
		q.Del("page")
		r2 := r.Clone(r.Context())
		r2.URL.RawQuery = q.Encode()
		rec := httptest.NewRecorder()
		inner.ServeHTTP(rec, r2)
		var idx v1.IndexManifest
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &idx) != nil {
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		// The registry lists referrers in no stable order: sorted, the
		// pages of successive requests neither repeat nor skip one.
		all := idx.Manifests
		sort.Slice(all, func(i, j int) bool { return all[i].Digest.String() < all[j].Digest.String() })
		lo, hi := page*size, page*size+size
		if lo > len(all) {
			lo = len(all)
		}
		if hi > len(all) {
			hi = len(all)
		}
		idx.Manifests = all[lo:hi]
		if hi < len(all) {
			q.Set("page", strconv.Itoa(page+1))
			next := url.URL{Path: r.URL.Path, RawQuery: q.Encode()}
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, next.String()))
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		_ = json.NewEncoder(w).Encode(idx)
	})
}

// receiptsFor pushes n receipts of resource for repo@digest.
func receiptsFor(t *testing.T, r *Registry, repo, digest, resource string, n int) {
	t.Helper()
	s, _ := keySigner(t)
	for i := 0; i < n; i++ {
		in := input("image")
		in.Name = fmt.Sprintf("%s-%d", resource, i)
		raw, _, err := Build(in)
		require.NoError(t, err)
		signed, err := s.Sign(context.Background(), raw)
		require.NoError(t, err)
		_, err = r.PushReferrer(context.Background(), repo, repo, digest, resource, signed.Bundle)
		require.NoError(t, err)
	}
}

// TestReferrers_PagesAndResource (#1951 N3): Referrers follows the
// Referrers API's pages, reads only the receipts of the resource asked
// for (another environment's are skipped before they are fetched, so they
// cannot crowd it out), and says the result is truncated past the
// ceiling: a receipt not found then is unknown, not absent.
//
// Covers RECEIPT-OCI-03.
func TestReferrers_PagesAndResource(t *testing.T) {
	srv := httptest.NewServer(paginated(registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0))), 2))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/checkout"
	img, err := random.Image(128, 1)
	require.NoError(t, err)
	require.NoError(t, remote.Write(mustRef(t, repo+":1"), img))
	d, err := img.Digest()
	require.NoError(t, err)
	r := &Registry{Insecure: true}
	const test, prod = "kardinal://c1/namespaces/payments/pipelines/checkout/environments/test",
		"kardinal://c1/namespaces/payments/pipelines/checkout/environments/prod"
	receiptsFor(t, r, repo, d.String(), test, 5)
	receiptsFor(t, r, repo, d.String(), prod, 2)

	isProd := func(res string) bool { return res == prod }
	got, err := r.Referrers(context.Background(), repo, d.String(), isProd)
	require.NoError(t, err)
	assert.Equal(t, 7, got.Entries, "every page read")
	assert.Len(t, got.Bundles, 2, "only prod's receipts")
	assert.False(t, got.Truncated)

	all, err := r.Referrers(context.Background(), repo, d.String(), nil)
	require.NoError(t, err)
	assert.Len(t, all.Bundles, 7)

	old := maxReferrerEntries
	maxReferrerEntries = 4
	t.Cleanup(func() { maxReferrerEntries = old })
	got, err = r.Referrers(context.Background(), repo, d.String(), isProd)
	require.NoError(t, err)
	assert.True(t, got.Truncated, "more referrers than the ceiling")
	assert.Equal(t, 4, got.Entries)
}

// TestReferrers_TagSchema: a registry without the Referrers API is read
// through the referrers tag schema, with the same filter.
func TestReferrers_TagSchema(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/checkout"
	img, err := random.Image(128, 1)
	require.NoError(t, err)
	require.NoError(t, remote.Write(mustRef(t, repo+":1"), img))
	d, err := img.Digest()
	require.NoError(t, err)
	r := &Registry{Insecure: true}
	receiptsFor(t, r, repo, d.String(), "kardinal://c1/namespaces/p/pipelines/a/environments/test", 2)
	receiptsFor(t, r, repo, d.String(), "kardinal://c1/namespaces/p/pipelines/a/environments/prod", 1)
	got, err := r.Referrers(context.Background(), repo, d.String(), func(res string) bool { return strings.HasSuffix(res, "/prod") })
	require.NoError(t, err)
	assert.Len(t, got.Bundles, 1)
	assert.False(t, got.Truncated)
}

// TestNextPage: a Link to the next page on the same registry is followed,
// relative or absolute; one to another host or scheme is an error (the
// registry's credentials go to it only, and the list is not whole without
// it).
func TestNextPage(t *testing.T) {
	cur, err := url.Parse("https://registry.example/v2/a/referrers/sha256:1?artifactType=x")
	require.NoError(t, err)
	for link, want := range map[string]string{
		`</v2/a/referrers/sha256:1?last=b>; rel="next"`:                    "https://registry.example/v2/a/referrers/sha256:1?last=b",
		`<https://registry.example/v2/a/referrers/sha256:1?n=2>; rel=next`: "https://registry.example/v2/a/referrers/sha256:1?n=2",
		`</v2/a/referrers/sha256:1?last=b>; rel="prev"`:                    "",
	} {
		got, err := nextPage(http.Header{"Link": {link}}, cur)
		require.NoError(t, err, link)
		if want == "" {
			assert.Nil(t, got, link)
			continue
		}
		require.NotNil(t, got, link)
		assert.Equal(t, want, got.String())
	}
	for _, link := range []string{
		`<https://evil.example/v2/a/referrers/sha256:1?n=2>; rel="next"`,
		`<http://registry.example/v2/a/referrers/sha256:1?n=2>; rel="next"`,
	} {
		_, err := nextPage(http.Header{"Link": {link}}, cur)
		require.ErrorIs(t, err, ErrReferrersIncomplete, link)
		assert.Contains(t, err.Error(), "off-host next link")
	}
}

// TestReferrers_Unbounded (QA-2 #2174): a registry whose pages never end
// (a page linking to itself, or always a new one), or that links off-host,
// cannot be read whole: Referrers fails, with ErrReferrersIncomplete, after
// a bounded number of requests, never a partial list.
func TestReferrers_Unbounded(t *testing.T) {
	inner := registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0)))
	var requests atomic.Int64
	mode := "self"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/referrers/") {
			inner.ServeHTTP(w, r)
			return
		}
		requests.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		switch mode {
		case "self":
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, r.URL.RequestURI()))
		case "endless":
			w.Header().Set("Link", fmt.Sprintf(`<%s?page=%d>; rel="next"`, r.URL.Path, page+1))
		case "offhost":
			w.Header().Set("Link", `<https://elsewhere.example/v2/x/referrers/sha256:1>; rel="next"`)
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		_ = json.NewEncoder(w).Encode(v1.IndexManifest{SchemaVersion: 2})
	}))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/checkout"
	digest := "sha256:" + strings.Repeat("a", 64)
	r := &Registry{Insecure: true}
	for _, tc := range []struct {
		mode, want string
		most       int64
	}{
		{"self", "linked twice", 2},
		{"endless", "more than 64 pages", maxReferrerPages},
		{"offhost", "off-host next link", 1},
	} {
		mode = tc.mode
		requests.Store(0)
		_, err := r.Referrers(context.Background(), repo, digest, nil)
		require.ErrorIs(t, err, ErrReferrersIncomplete, tc.mode)
		assert.Contains(t, err.Error(), tc.want)
		assert.LessOrEqual(t, requests.Load(), tc.most, "%s: bounded requests", tc.mode)
	}
}

// TestReferrers_SeventyOthers (#2133): an image with 70 receipts of other
// environments (fleet members, rollbacks, or referrers anyone who may push
// planted) and prod's: prod's is read, past the old 64-receipt cap that
// counted every environment's.
//
// Covers RECEIPT-OCI-03.
func TestReferrers_SeventyOthers(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.WithReferrersSupport(true), registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	repo := strings.TrimPrefix(srv.URL, "http://") + "/acme/checkout"
	img, err := random.Image(128, 1)
	require.NoError(t, err)
	require.NoError(t, remote.Write(mustRef(t, repo+":1"), img))
	d, err := img.Digest()
	require.NoError(t, err)
	s, _ := keySigner(t)
	raw, _, err := Build(input("image"))
	require.NoError(t, err)
	signed, err := s.Sign(context.Background(), raw)
	require.NoError(t, err)
	r := &Registry{Insecure: true}
	for i := 0; i < 70; i++ {
		_, err := r.PushReferrer(context.Background(), repo, repo, d.String(),
			fmt.Sprintf("kardinal://c1/namespaces/payments/pipelines/checkout/environments/eu-%02d", i), signed.Bundle)
		require.NoError(t, err)
	}
	const prod = "kardinal://c1/namespaces/payments/pipelines/checkout/environments/prod"
	_, err = r.PushReferrer(context.Background(), repo, repo, d.String(), prod, signed.Bundle)
	require.NoError(t, err)
	got, err := r.Referrers(context.Background(), repo, d.String(), func(res string) bool { return res == prod })
	require.NoError(t, err)
	assert.Equal(t, 71, got.Entries)
	require.Len(t, got.Bundles, 1, "prod's receipt, whatever the others")
	assert.False(t, got.Truncated)
}
