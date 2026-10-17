// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSelfIDs_Bounded: 10,000 providers each caching their identity never
// hold more than maxSelfIDs entries; the most recent ones stay cached.
//
// Covers SCM-IDENTITY-01.
func TestSelfIDs_Bounded(t *testing.T) {
	selfIDs.Clear()
	t.Cleanup(selfIDs.Clear)
	ps := make([]*ForgejoProvider, 10000)
	lookups := 0
	for i := range ps {
		ps[i] = &ForgejoProvider{}
		id, err := self(context.Background(), ps[i], func(context.Context) (string, error) {
			lookups++
			return fmt.Sprint(i), nil
		})
		require.NoError(t, err)
		require.Equal(t, fmt.Sprint(i), id)
	}
	assert.Equal(t, 10000, lookups)
	assert.Equal(t, maxSelfIDs, selfIDs.Len(), "bounded")
	_, err := self(context.Background(), ps[len(ps)-1], func(context.Context) (string, error) {
		lookups++
		return "", nil
	})
	require.NoError(t, err)
	assert.Equal(t, 10000, lookups, "the newest is still cached")
	_, ok := selfIDs.Get(ps[0])
	assert.False(t, ok, "the oldest was evicted")
}

// TestSelfIDs_ForgottenOnReload: a token rotation (DynamicProvider
// reload) forgets the replaced provider's identity, so 10,000 rotations
// leave one entry, not 10,000.
//
// Covers SCM-IDENTITY-01.
func TestSelfIDs_ForgottenOnReload(t *testing.T) {
	selfIDs.Clear()
	t.Cleanup(selfIDs.Clear)
	d, err := NewDynamicProvider("forgejo", "t0", "https://forgejo.example", "")
	require.NoError(t, err)
	for i := 0; i < 10000; i++ {
		_, err := self(context.Background(), d.Current(), func(context.Context) (string, error) { return "me", nil })
		require.NoError(t, err)
		require.NoError(t, d.Reload(fmt.Sprintf("t%d", i+1)))
	}
	assert.Equal(t, 0, selfIDs.Len(), "every replaced provider was forgotten")
}

// TestForgetIdentity_Unwraps: forgetting a guarded or dynamic provider
// forgets the provider it wraps, whose identity the search cached.
func TestForgetIdentity_Unwraps(t *testing.T) {
	selfIDs.Clear()
	t.Cleanup(selfIDs.Clear)
	inner := &ForgejoProvider{}
	selfIDs.Add(inner, "me")
	ForgetIdentity(&guardedProvider{inner: inner})
	assert.Equal(t, 0, selfIDs.Len())
}

// TestRegistry_EvictionForgetsIdentity: a client the ScmProvider registry
// drops (Evict, or least recently used past its bound) is forgotten.
func TestRegistry_EvictionForgetsIdentity(t *testing.T) {
	selfIDs.Clear()
	t.Cleanup(selfIDs.Clear)
	r := &Registry{}
	r.init()
	inner := &ForgejoProvider{}
	selfIDs.Add(inner, "me")
	r.clients.Add("uid-1", registryEntry{provider: inner})
	r.clients.Remove("uid-1")
	assert.Equal(t, 0, selfIDs.Len())
}
