// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package uiauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/accesslog"
)

// DefaultCacheTTL is how long TokenReview and SubjectAccessReview results are
// reused. It bounds the API server load of a polling UI (one review per token
// and per action per TTL instead of one per request) and how long a revoked
// token or RoleBinding keeps working through the UI.
const DefaultCacheTTL = 30 * time.Second

// maxCacheEntries bounds memory use. A caller sending many distinct invalid
// tokens can fill the cache; when full, expired entries are dropped and, if
// that frees nothing, the cache is cleared.
const maxCacheEntries = 4096

type cacheEntry[V any] struct {
	value   V
	expires time.Time
	// started orders the reviews behind the entries (see ttlCache.start).
	started uint64
}

// ttlCache caches review results. Each review takes a ticket from start
// before it asks the API server, and put stores its result only when no
// review that started later has stored one: an "allowed" that started
// before an RBAC revoke but returned after the "denied" of a later review
// must not overwrite the denial for another TTL.
type ttlCache[V any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]cacheEntry[V]
	tickets uint64
}

func newTTLCache[V any](ttl time.Duration) *ttlCache[V] {
	return &ttlCache[V]{ttl: ttl, now: time.Now, entries: map[string]cacheEntry[V]{}}
}

func (c *ttlCache[V]) get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !c.now().Before(e.expires) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// start returns the ticket of a review about to be asked.
func (c *ttlCache[V]) start() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tickets++
	return c.tickets
}

// put stores v, the result of the review with ticket started, unless a
// review that started later has stored its result for key already.
func (c *ttlCache[V]) put(key string, v V, started uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	// Expired or not: a newer review's result, even an expired one, is
	// fresher than this one, which then is not stored at all.
	if e, ok := c.entries[key]; ok && e.started > started {
		return
	}
	if len(c.entries) >= maxCacheEntries {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= maxCacheEntries {
			c.entries = map[string]cacheEntry[V]{}
		}
	}
	c.entries[key] = cacheEntry[V]{value: v, expires: now.Add(c.ttl), started: started}
}

func hashKey(parts ...any) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, p := range parts {
		_ = enc.Encode(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// cachedTokenReviewer caches TokenReview results by token hash. Errors are
// not cached, so an API server outage fails closed and recovers immediately.
type cachedTokenReviewer struct {
	inner TokenReviewer
	cache *ttlCache[authv1.TokenReviewStatus]
}

// NewCachedTokenReviewer wraps inner with a TTL cache keyed on a SHA-256 of
// the token (the raw token is never stored).
func NewCachedTokenReviewer(inner TokenReviewer, ttl time.Duration) TokenReviewer {
	return &cachedTokenReviewer{inner: inner, cache: newTTLCache[authv1.TokenReviewStatus](ttl)}
}

func (c *cachedTokenReviewer) Review(ctx context.Context, token string) (*authv1.TokenReviewStatus, error) {
	key := hashKey(token)
	if st, ok := c.cache.get(key); ok {
		return &st, nil
	}
	ticket := c.cache.start()
	st, err := c.inner.Review(ctx, token)
	if err != nil {
		return nil, err
	}
	// A review the API server answered, not the cache: a login.
	accesslog.FromContext(ctx).Login = true
	c.cache.put(key, *st.DeepCopy(), ticket)
	return st, nil
}

type accessDecision struct {
	allowed bool
	reason  string
}

// cachedAccessReviewer caches SubjectAccessReview decisions per user and
// attributes. Errors are not cached.
type cachedAccessReviewer struct {
	inner AccessReviewer
	cache *ttlCache[accessDecision]
}

// NewCachedAccessReviewer wraps inner with a TTL cache.
func NewCachedAccessReviewer(inner AccessReviewer, ttl time.Duration) AccessReviewer {
	return &cachedAccessReviewer{inner: inner, cache: newTTLCache[accessDecision](ttl)}
}

func (c *cachedAccessReviewer) Allowed(ctx context.Context, user authv1.UserInfo, attrs authzv1.ResourceAttributes) (bool, string, error) {
	key := hashKey(user, attrs)
	if d, ok := c.cache.get(key); ok {
		return d.allowed, d.reason, nil
	}
	ticket := c.cache.start()
	allowed, reason, err := c.inner.Allowed(ctx, user, attrs)
	if err != nil {
		return false, "", err
	}
	c.cache.put(key, accessDecision{allowed: allowed, reason: reason}, ticket)
	return allowed, reason, nil
}
