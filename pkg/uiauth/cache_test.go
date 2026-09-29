// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package uiauth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
)

type countingReviewer struct {
	calls int
	err   error
}

func (r *countingReviewer) Review(_ context.Context, token string) (*authv1.TokenReviewStatus, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return &authv1.TokenReviewStatus{Authenticated: token == "good", User: authv1.UserInfo{Username: "u-" + token}}, nil
}

type countingAccess struct {
	calls int
	err   error
}

func (a *countingAccess) Allowed(_ context.Context, u authv1.UserInfo, attrs authzv1.ResourceAttributes) (bool, string, error) {
	a.calls++
	if a.err != nil {
		return false, "", a.err
	}
	return u.Username == "alice" && attrs.Verb == "get", "", nil
}

// TestCachedTokenReviewer verifies TokenReview results are reused per token
// for the TTL, errors are not cached, and entries expire. Regression test for
// C07-controller-07: one TokenReview API call per UI request.
func TestCachedTokenReviewer(t *testing.T) {
	inner := &countingReviewer{}
	r := NewCachedTokenReviewer(inner, time.Minute).(*cachedTokenReviewer)
	now := time.Unix(1000, 0)
	r.cache.now = func() time.Time { return now }
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		st, err := r.Review(ctx, "good")
		require.NoError(t, err)
		assert.True(t, st.Authenticated)
		assert.Equal(t, "u-good", st.User.Username)
	}
	assert.Equal(t, 1, inner.calls, "same token within TTL must hit the cache")

	st, err := r.Review(ctx, "bad")
	require.NoError(t, err)
	assert.False(t, st.Authenticated)
	_, _ = r.Review(ctx, "bad")
	assert.Equal(t, 2, inner.calls, "a rejected token is cached too")

	now = now.Add(time.Minute)
	_, _ = r.Review(ctx, "good")
	assert.Equal(t, 3, inner.calls, "expired entry must be reviewed again")

	inner.err = errors.New("apiserver down")
	_, err = r.Review(ctx, "other")
	require.Error(t, err)
	_, err = r.Review(ctx, "other")
	require.Error(t, err)
	assert.Equal(t, 5, inner.calls, "errors must not be cached")

	for k := range r.cache.entries {
		assert.NotContains(t, k, "good", "cache keys must not contain the token")
	}
}

// TestCachedAccessReviewer verifies decisions are cached per user and
// attributes and errors are not cached.
func TestCachedAccessReviewer(t *testing.T) {
	inner := &countingAccess{}
	a := NewCachedAccessReviewer(inner, time.Minute)
	ctx := context.Background()
	alice := authv1.UserInfo{Username: "alice"}
	get := authzv1.ResourceAttributes{Verb: "get", Group: "kardinal.io", Resource: "pipelines", Namespace: "a", Name: "p"}
	update := get
	update.Verb = "update"

	tests := []struct {
		user      authv1.UserInfo
		attrs     authzv1.ResourceAttributes
		wantAllow bool
		wantCalls int
	}{
		{alice, get, true, 1},
		{alice, get, true, 1},
		{alice, update, false, 2},
		{alice, update, false, 2},
		{authv1.UserInfo{Username: "bob"}, get, false, 3},
		{authv1.UserInfo{Username: "alice", Groups: []string{"admins"}}, get, true, 4},
	}
	for i, tt := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			ok, _, err := a.Allowed(ctx, tt.user, tt.attrs)
			require.NoError(t, err)
			assert.Equal(t, tt.wantAllow, ok)
			assert.Equal(t, tt.wantCalls, inner.calls)
		})
	}

	inner.err = errors.New("down")
	other := get
	other.Namespace = "b"
	_, _, err := a.Allowed(ctx, alice, other)
	require.Error(t, err)
	_, _, err = a.Allowed(ctx, alice, other)
	require.Error(t, err)
	assert.Equal(t, 6, inner.calls, "errors must not be cached")
}

// TestTTLCache_Bounded verifies the cache never grows past maxCacheEntries.
func TestTTLCache_Bounded(t *testing.T) {
	c := newTTLCache[int](time.Minute)
	for i := 0; i < maxCacheEntries+10; i++ {
		c.put(fmt.Sprint(i), i)
	}
	assert.LessOrEqual(t, len(c.entries), maxCacheEntries)
	v, ok := c.get(fmt.Sprint(maxCacheEntries + 9))
	assert.True(t, ok)
	assert.Equal(t, maxCacheEntries+9, v)
}
