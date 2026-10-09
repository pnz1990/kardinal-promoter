// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package uiauth

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	authv1 "k8s.io/api/authentication/v1"
)

// DefaultReviewsPerClientPerMinute bounds the TokenReviews one client address
// can cause. Reviews come after the cache, so a client reusing its token
// costs one review per DefaultCacheTTL; only a client sending new tokens, such
// as one guessing tokens, reaches the limit.
const DefaultReviewsPerClientPerMinute = 60

// ErrRateLimited is returned by a rate-limited reviewer when the client has
// used its reviews for the current minute. Middleware answers 429.
var ErrRateLimited = errors.New("uiauth: too many token reviews from this client address")

type clientAddrKey struct{}

// withClientAddr records the request's peer address (host part of
// RemoteAddr) for the rate-limited reviewer.
func withClientAddr(ctx context.Context, remoteAddr string) context.Context {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return context.WithValue(ctx, clientAddrKey{}, host)
}

// rateLimitedReviewer allows perMinute reviews per client address in each
// one-minute window. The window map is replaced every minute, so its size is
// bounded by the addresses seen in one minute.
type rateLimitedReviewer struct {
	inner     TokenReviewer
	perMinute int
	now       func() time.Time

	mu      sync.Mutex
	counts  map[string]int
	resetAt time.Time
}

// NewRateLimitedTokenReviewer limits the reviews inner performs to perMinute
// per client address. Wrap it in NewCachedTokenReviewer so cached tokens do
// not count. Behind an Ingress or a mesh sidecar every client has the proxy's
// address and shares one budget.
func NewRateLimitedTokenReviewer(inner TokenReviewer, perMinute int) TokenReviewer {
	return &rateLimitedReviewer{inner: inner, perMinute: perMinute, now: time.Now, counts: map[string]int{}}
}

func (r *rateLimitedReviewer) Review(ctx context.Context, token string) (*authv1.TokenReviewStatus, error) {
	addr, _ := ctx.Value(clientAddrKey{}).(string)
	if !r.allow(addr) {
		return nil, ErrRateLimited
	}
	return r.inner.Review(ctx, token)
}

func (r *rateLimitedReviewer) allow(addr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if !now.Before(r.resetAt) {
		r.counts = map[string]int{}
		r.resetAt = now.Add(time.Minute)
	}
	r.counts[addr]++
	return r.counts[addr] <= r.perMinute
}
