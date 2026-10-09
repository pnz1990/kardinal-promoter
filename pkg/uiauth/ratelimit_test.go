// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package uiauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
)

// guessReviewer authenticates the token "good" and counts reviews.
type guessReviewer struct{ n atomic.Int32 }

func (c *guessReviewer) Review(_ context.Context, token string) (*authv1.TokenReviewStatus, error) {
	c.n.Add(1)
	return &authv1.TokenReviewStatus{Authenticated: token == "good", User: authv1.UserInfo{Username: "alice"}}, nil
}

// TestRateLimitedReviewer: a client sending new tokens gets 429 once it has
// used its reviews for the minute, before any TokenReview is sent; other
// addresses and cached tokens are not affected; the budget comes back the
// next minute.
func TestRateLimitedReviewer(t *testing.T) {
	inner := &guessReviewer{}
	now := time.Unix(1_000_000, 0)
	limited := NewRateLimitedTokenReviewer(inner, 3).(*rateLimitedReviewer)
	limited.now = func() time.Time { return now }
	h := MiddlewareFor(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), NewCachedTokenReviewer(limited, time.Minute), "/api/", "test")

	do := func(addr, token string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.RemoteAddr = addr
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	tests := []struct {
		name, addr, token string
		want              int
		wantReviews       int32
	}{
		{"valid token", "10.0.0.1:1000", "good", http.StatusOK, 1},
		{"guess 1", "10.0.0.1:1001", "guess-1", http.StatusUnauthorized, 2},
		{"guess 2", "10.0.0.1:1002", "guess-2", http.StatusUnauthorized, 3},
		{"guess 3 is over the limit, no review", "10.0.0.1:1003", "guess-3", http.StatusTooManyRequests, 3},
		{"cached valid token still works", "10.0.0.1:1004", "good", http.StatusOK, 3},
		{"another address has its own budget", "10.0.0.2:1000", "guess-4", http.StatusUnauthorized, 4},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, do(tt.addr, tt.token), tt.name)
		assert.Equal(t, tt.wantReviews, inner.n.Load(), tt.name)
	}
	now = now.Add(time.Minute)
	for i := 0; i < 3; i++ {
		assert.Equal(t, http.StatusUnauthorized, do("10.0.0.1:2000", fmt.Sprintf("next-%d", i)), "the budget is back the next minute")
	}
}
