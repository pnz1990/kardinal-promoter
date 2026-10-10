// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestGitHubAppTokenSource_CachedLocked (#1636 QA): the answer Token and the
// mint flight both give from the cache. A fresh token is used; while a
// failed mint's backoff lasts, a still-valid token is used, or the mint's
// error is returned, so a caller that joins the flight just after a failure
// does not mint again; otherwise a mint is due.
func TestGitHubAppTokenSource_CachedLocked(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	mintErr := errors.New("mint: 404 Integration not found")
	for _, tc := range []struct {
		name     string
		src      *GitHubAppTokenSource
		wantTok  string
		wantDone bool
		wantErr  bool
	}{
		{"empty: mint", &GitHubAppTokenSource{}, "", false, false},
		{"fresh token", &GitHubAppTokenSource{token: "a", expires: now.Add(time.Hour), refreshAt: now.Add(50 * time.Minute)}, "a", true, false},
		{"due for refresh: mint", &GitHubAppTokenSource{token: "a", expires: now.Add(5 * time.Minute), refreshAt: now.Add(-time.Minute)}, "", false, false},
		{"backoff, token still valid", &GitHubAppTokenSource{token: "a", expires: now.Add(5 * time.Minute), refreshAt: now.Add(-time.Minute),
			lastErr: mintErr, retryAt: now.Add(time.Minute)}, "a", true, false},
		{"backoff, no valid token: the error", &GitHubAppTokenSource{lastErr: mintErr, retryAt: now.Add(time.Minute)}, "", true, true},
		{"backoff over: mint", &GitHubAppTokenSource{lastErr: mintErr, retryAt: now.Add(-time.Second)}, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.src
			s.now = func() time.Time { return now }
			s.mu.Lock()
			tok, done, err := s.cachedLocked()
			s.mu.Unlock()
			assert.Equal(t, tc.wantTok, tok)
			assert.Equal(t, tc.wantDone, done)
			if tc.wantErr {
				assert.ErrorIs(t, err, mintErr)
				assert.ErrorContains(t, err, "not retried before")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
