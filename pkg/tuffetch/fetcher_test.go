// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package tuffetch_test

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sigroot "github.com/sigstore/sigstore-go/pkg/root"
	sigtuf "github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/theupdateframework/go-tuf/v2/metadata"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/tuffetch"
)

// within runs download, failing the test after limit: a regression that
// retries or waits without end fails at once, not at go test's timeout.
func within(t *testing.T, limit time.Duration, download func() ([]byte, error)) ([]byte, error) {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := download()
		done <- result{data, err}
	}()
	select {
	case r := <-done:
		return r.data, r.err
	case <-time.After(limit):
		t.Fatalf("the download did not end within %s", limit)
		return nil, nil
	}
}

// TestFetcher_Retries (#2218): a 404 or another 4xx ends the download at
// once, with the status as a *metadata.ErrDownloadHTTP for go-tuf's
// updater; a 5xx, a 429 or a transport error is tried again, at most Tries
// times; a body over maxLength is not; the Deadline bounds a download whose
// server never answers.
//
// Covers TUF-FETCH-01.
func TestFetcher_Retries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []int // per request, the last repeating
		wantReqs int32
		wantCode int // 0: success
	}{
		{name: "404 is final", statuses: []int{404}, wantReqs: 1, wantCode: 404},
		{name: "403 is final", statuses: []int{403}, wantReqs: 1, wantCode: 403},
		{name: "503 then 200", statuses: []int{503, 503, 200}, wantReqs: 3},
		{name: "429 then 200", statuses: []int{429, 200}, wantReqs: 2},
		{name: "500 every time", statuses: []int{500}, wantReqs: 3, wantCode: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reqs atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := int(reqs.Add(1)) - 1
				code := tc.statuses[min(n, len(tc.statuses)-1)]
				w.WriteHeader(code)
				if code == 200 {
					_, _ = w.Write([]byte("ok"))
				}
			}))
			defer srv.Close()
			f := &tuffetch.Fetcher{Backoff: time.Millisecond}
			start := time.Now()
			data, err := within(t, 5*time.Second, func() ([]byte, error) { return f.DownloadFile(srv.URL+"/2.root.json", 1024, 0) })
			assert.Less(t, time.Since(start), 2*time.Second)
			assert.Equal(t, tc.wantReqs, reqs.Load())
			if tc.wantCode == 0 {
				require.NoError(t, err)
				assert.Equal(t, "ok", string(data))
				return
			}
			var he *metadata.ErrDownloadHTTP
			require.True(t, errors.As(err, &he), "%v", err)
			assert.Equal(t, tc.wantCode, he.StatusCode)
		})
	}
	t.Run("a body over maxLength is not tried again", func(t *testing.T) {
		var reqs atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reqs.Add(1)
			_, _ = w.Write([]byte(strings.Repeat("x", 100)))
		}))
		defer srv.Close()
		_, err := (&tuffetch.Fetcher{Backoff: time.Millisecond}).DownloadFile(srv.URL, 10, 0)
		var lm *metadata.ErrDownloadLengthMismatch
		require.True(t, errors.As(err, &lm), "%v", err)
		assert.EqualValues(t, 1, reqs.Load())
	})
	// QA-2 on #2236: a body without a length, and without an end, is read
	// only to the limit and refused.
	t.Run("an endless body stops at the limit", func(t *testing.T) {
		var sent atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			chunk := []byte(strings.Repeat("x", 4096))
			for r.Context().Err() == nil {
				n, err := w.Write(chunk) // chunked: no Content-Length
				sent.Add(int64(n))
				if err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}))
		defer srv.Close()
		const limit = 64 << 10
		_, err := within(t, 5*time.Second, func() ([]byte, error) {
			return (&tuffetch.Fetcher{Backoff: time.Millisecond, Deadline: 3 * time.Second}).DownloadFile(srv.URL, limit, 0)
		})
		var lm *metadata.ErrDownloadLengthMismatch
		require.True(t, errors.As(err, &lm), "%v", err)
		assert.Contains(t, lm.Msg, fmt.Sprintf("length %d is larger than expected %d", limit+1, limit), "read to the limit and one byte more")
	})
	t.Run("a transport error is tried again", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close() // connection refused from now on
		start := time.Now()
		_, err := within(t, 5*time.Second, func() ([]byte, error) {
			return (&tuffetch.Fetcher{Backoff: 10 * time.Millisecond}).DownloadFile(url, 10, 0)
		})
		require.Error(t, err)
		assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond, "two waits: three tries")
	})
	t.Run("the deadline bounds a server that never answers", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		defer close(release)
		start := time.Now()
		_, err := within(t, 5*time.Second, func() ([]byte, error) {
			return (&tuffetch.Fetcher{Deadline: 300 * time.Millisecond}).DownloadFile(srv.URL, 10, 0)
		})
		require.Error(t, err)
		assert.Less(t, time.Since(start), 2*time.Second)
	})
	t.Run("the parent context ends it", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := (&tuffetch.Fetcher{Context: ctx}).DownloadFile("http://127.0.0.1:1/x", 10, 0)
		require.ErrorIs(t, err, context.Canceled)
	})
}

// tufRepo writes a one-key TUF repository whose only target is a Sigstore
// trusted root, as the public-good mirror serves one, and returns its
// directory and its 1.root.json.
func tufRepo(t *testing.T) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	expires := time.Now().Add(24 * time.Hour)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	key, err := metadata.KeyFromPublicKey(pub)
	require.NoError(t, err)
	signer, err := signature.LoadSigner(priv, crypto.Hash(0))
	require.NoError(t, err)

	trusted := []byte(`{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1"}`)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "targets"), 0o755))
	sum := sha256.Sum256(trusted) // a consistent snapshot names targets by their hash
	require.NoError(t, os.WriteFile(filepath.Join(dir, "targets", hex.EncodeToString(sum[:])+".trusted_root.json"), trusted, 0o600))
	targets := metadata.Targets(expires)
	tf, err := metadata.TargetFile().FromBytes("trusted_root.json", trusted, "sha256")
	require.NoError(t, err)
	targets.Signed.Targets["trusted_root.json"] = tf
	snapshot := metadata.Snapshot(expires)
	timestamp := metadata.Timestamp(expires)
	root := metadata.Root(expires)
	for _, role := range []string{metadata.ROOT, metadata.TARGETS, metadata.SNAPSHOT, metadata.TIMESTAMP} {
		require.NoError(t, root.Signed.AddKey(key, role))
	}
	_, err = targets.Sign(signer)
	require.NoError(t, err)
	_, err = snapshot.Sign(signer)
	require.NoError(t, err)
	_, err = timestamp.Sign(signer)
	require.NoError(t, err)
	_, err = root.Sign(signer)
	require.NoError(t, err)
	require.NoError(t, targets.ToFile(filepath.Join(dir, "1.targets.json"), true))
	require.NoError(t, snapshot.ToFile(filepath.Join(dir, "1.snapshot.json"), true))
	require.NoError(t, timestamp.ToFile(filepath.Join(dir, "timestamp.json"), true))
	require.NoError(t, root.ToFile(filepath.Join(dir, "1.root.json"), true))
	raw, err := os.ReadFile(filepath.Join(dir, "1.root.json"))
	require.NoError(t, err)
	return dir, raw
}

// TestFetcher_TrustedRootFromLocalMirror (#2218): fetching a Sigstore
// trusted root from a TUF mirror, as the controller does for image
// verification and kardinal verify-receipt does, ends its root update on
// the mirror's 404 for 2.root.json at once: the whole fetch takes well
// under a second, where go-tuf's NewFetcherWithHTTPClient retried that 404
// for up to 15 minutes. The mirror is local, never the public one.
//
// Covers TUF-FETCH-02.
func TestFetcher_TrustedRootFromLocalMirror(t *testing.T) {
	dir, rootJSON := tufRepo(t)
	var mu sync.Mutex
	var paths []string
	files := http.FileServer(http.Dir(dir))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		files.ServeHTTP(w, r)
	}))
	defer srv.Close()

	opts := sigtuf.DefaultOptions().WithDisableLocalCache().WithRoot(rootJSON).WithRepositoryBaseURL(srv.URL)
	opts.Fetcher = &tuffetch.Fetcher{Client: &http.Client{Timeout: 5 * time.Second}}
	start := time.Now()
	tr, err := sigroot.FetchTrustedRootWithOptions(opts)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.NotNil(t, tr)
	assert.Less(t, elapsed, 5*time.Second, "the root update's 404 ends it at once")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, strings.Count(strings.Join(paths, " "), "/2.root.json"), "the 404 is asked for once: %v", paths)
}
