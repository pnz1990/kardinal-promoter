// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// TestParseRefreshScope: rules are pipelineNamespace=namespace, each side a
// namespace, a prefix ending in * or *.
func TestParseRefreshScope(t *testing.T) {
	s, err := health.ParseRefreshScope([]string{" team-a = team-a-* ", "", "*=shared"})
	require.NoError(t, err)
	assert.Equal(t, []string{"team-a=team-a-*", "*=shared"}, s.Rules())
	for _, bad := range []string{"team-a", "=x", "x=", "te*am=x", "x=*y", "a=b=c"} {
		_, err := health.ParseRefreshScope([]string{bad})
		assert.Error(t, err, bad)
	}
}

// TestRefreshScopeAllows: the Pipeline's own namespace (the object's, or the
// one it deploys to) is always in scope; rules admit more, matched against
// the destination, or the object's namespace when it names none.
func TestRefreshScopeAllows(t *testing.T) {
	scope, err := health.ParseRefreshScope([]string{"team-a=team-a-*", "ops-*=*", "default=argocd"})
	require.NoError(t, err)
	app := func(ns, dest string) health.RefreshTarget {
		return health.RefreshTarget{Kind: "Application", Namespace: ns, Name: "web", Destination: dest}
	}
	tests := []struct {
		name     string
		pipeline string
		target   health.RefreshTarget
		want     bool
	}{
		{"deploys to the Pipeline's namespace", "team-a", app("argocd", "team-a"), true},
		{"lives in the Pipeline's namespace", "team-a", app("team-a", "elsewhere"), true},
		{"another tenant's", "team-a", app("argocd", "team-b"), false},
		{"no destination, in argocd", "team-b", app("argocd", ""), false},
		{"rule prefix", "team-a", app("argocd", "team-a-prod"), true},
		{"rule prefix, not a match", "team-a", app("argocd", "team-ab"), false},
		{"rule for any target", "ops-1", app("argocd", "team-b"), true},
		{"rule on the object namespace when no destination", "default", app("argocd", ""), true},
		{"rule on the destination when there is one", "default", app("argocd", "team-b"), false},
		{"zero scope", "x", app("argocd", "y"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := scope
			if tt.name == "zero scope" {
				s = health.RefreshScope{}
			}
			assert.Equal(t, tt.want, s.Allows(tt.pipeline, tt.target))
		})
	}
	assert.Equal(t, []string{"Application argocd/web (deploys to team-b)"},
		scope.OutOfScope("team-a", []health.RefreshTarget{app("argocd", "team-a"), app("argocd", "team-b")}))
}
