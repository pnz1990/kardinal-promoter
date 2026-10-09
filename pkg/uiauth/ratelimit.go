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

// DefaultReviewsPerMinute bounds the TokenReviews all clients together can
// cause, so many addresses (or one behind many) cannot flood the API server.
const DefaultReviewsPerMinute = 600

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
	total     int
	now       func() time.Time

	mu      sync.Mutex
	counts  map[string]int
	all     int
	resetAt time.Time
}

// NewRateLimitedTokenReviewer limits the reviews inner performs to perMinute
// per client address and total per minute for all clients. Wrap it in
// NewCachedTokenReviewer so cached tokens do not count. Behind an Ingress or
// a mesh sidecar every client has the proxy's address and shares one budget.
func NewRateLimitedTokenReviewer(inner TokenReviewer, perMinute, total int) TokenReviewer {
	return &rateLimitedReviewer{inner: inner, perMinute: perMinute, total: total, now: time.Now, counts: map[string]int{}}
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
		r.all = 0
		r.resetAt = now.Add(time.Minute)
	}
	if r.counts[addr] >= r.perMinute || (r.total > 0 && r.all >= r.total) {
		return false
	}
	r.counts[addr]++
	r.all++
	return true
}
