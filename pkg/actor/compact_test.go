// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// compactEntry is e in the compact format a later release writes (#2071):
// its seven fields joined with DEL, each list's items with \x1f. It is
// spelled out here, not taken from the writer, so the format this release
// reads is pinned.
func compactEntry(e v1alpha1.AgentIndexEntry) string {
	return strings.Join([]string{e.Agent, e.Sponsor, e.Autonomy, strings.Join(e.Namespaces, "\x1f"),
		strings.Join(e.Pipelines, "\x1f"), strings.Join(e.Environments, "\x1f"), e.Blocked}, "\x7f")
}

// laterGuard is the legacy Groups entry a later release writes beside a
// compact index, for its own older readers: every authenticated requester
// matches it as a blocked agent. This release reads the compact maps
// instead, so it never matches it.
var laterGuard = v1alpha1.AgentIndexEntry{Agent: "agent-index-in-a-newer-format", Sponsor: "upgrade-kardinal-to-read-it",
	Blocked: "kardinal's agent index is in the compact format of a newer release, which this release cannot read: " +
		"finish the upgrade (or the rollback) and retry"}

// compactOf is legacy's entries in the compact format, as a later release's
// controller writes the index: Ready False (CompactFormat), CompactReady
// True, the legacy maps holding only its guard.
func compactOf(legacy v1alpha1.AgentIndexStatus) v1alpha1.AgentIndexStatus {
	st := v1alpha1.AgentIndexStatus{CompactUsers: map[string]string{}, CompactGroups: map[string]string{},
		Groups: map[string]v1alpha1.AgentIndexEntry{"system:authenticated": laterGuard}}
	for k, e := range legacy.Users {
		st.CompactUsers[k] = compactEntry(e)
	}
	for k, e := range legacy.Groups {
		st.CompactGroups[k] = compactEntry(e)
	}
	st.Conditions = []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: actor.ReasonCompactFormat},
		{Type: actor.ConditionCompactReady, Status: metav1.ConditionTrue, Reason: "Built"},
	}
	return st
}

// readRegistry is a legacy index: a ServiceAccount agent, a group agent, a
// User agent whose sponsor holds "|" and ",", and a suspended agent.
func readRegistry() v1alpha1.AgentIndexStatus {
	return v1alpha1.AgentIndexStatus{
		Users: map[string]v1alpha1.AgentIndexEntry{
			"system:serviceaccount:ci:fixer": {Agent: "fixer", Sponsor: "alice", Autonomy: "auto",
				Namespaces: []string{"payments-*", "billing"}, Pipelines: []string{"checkout"}, Environments: []string{"test", "uat"}},
			"oidc:robot": {Agent: "robot", Sponsor: "auth0|bob,eng", Autonomy: "approve",
				Namespaces: []string{"*"}, Pipelines: []string{"*"}, Environments: []string{"*"}},
			"system:serviceaccount:ci:parked": {Agent: "parked", Sponsor: "carol", Namespaces: []string{"*"},
				Blocked: "suspended"},
		},
		Groups: map[string]v1alpha1.AgentIndexEntry{
			"oidc:agents": {Agent: "grouped", Sponsor: "dave", Namespaces: []string{"team-a"}},
		},
		Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Built"}},
	}
}

// TestDecodeEntry pins the compact format a later release writes: the
// seven fields in order, lists split on \x1f (an empty one is no items), the
// blocked text whole whatever it holds, and an entry short of a field read
// as blocked.
// Covers ACTOR-INDEX-04.
func TestDecodeEntry(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want v1alpha1.AgentIndexEntry
	}{
		{"fixer\x7falice@example.com\x7fapprove\x7f*\x7f*\x7f*\x7f",
			v1alpha1.AgentIndexEntry{Agent: "fixer", Sponsor: "alice@example.com", Autonomy: "approve",
				Namespaces: []string{"*"}, Pipelines: []string{"*"}, Environments: []string{"*"}}},
		{"fixer\x7fauth0|bob,eng\x7f\x7fpayments-*\x1fbilling\x7f\x7ftest\x1fuat\x7f",
			v1alpha1.AgentIndexEntry{Agent: "fixer", Sponsor: "auth0|bob,eng",
				Namespaces: []string{"payments-*", "billing"}, Environments: []string{"test", "uat"}}},
		{"parked\x7fcarol\x7f\x7f*\x7f\x7f\x7fsuspended: by\x7fops\x1fat noon",
			v1alpha1.AgentIndexEntry{Agent: "parked", Sponsor: "carol", Namespaces: []string{"*"},
				Blocked: "suspended: by\x7fops\x1fat noon"}},
		{"fixer\x7falice", v1alpha1.AgentIndexEntry{Blocked: "the agent index entry is malformed"}},
		{"", v1alpha1.AgentIndexEntry{Blocked: "the agent index entry is malformed"}},
	} {
		assert.Equal(t, tc.want, actor.DecodeEntry(tc.in), "%q", tc.in)
	}
	for _, e := range readRegistry().Users {
		assert.Equal(t, e, actor.DecodeEntry(compactEntry(e)), "%+v", e)
	}
}

// TestReadable: the current format is the one Ready's reason names. A
// compact index is decoded (its legacy maps, a later release's guard for
// its older readers, are not read), a legacy one read as is, stale compact
// maps a writer of the legacy format left behind are ignored, and an index
// that is not built is not read.
// Covers ACTOR-INDEX-04.
func TestReadable(t *testing.T) {
	legacy := readRegistry()
	compact := compactOf(legacy)
	notBuilt := compactOf(legacy)
	notBuilt.Conditions[1] = metav1.Condition{Type: actor.ConditionCompactReady, Status: metav1.ConditionFalse, Reason: actor.ReasonIndexFull}
	stale := readRegistry()
	stale.CompactUsers = map[string]string{"system:serviceaccount:ci:fixer": "stale\x7f\x7f\x7f\x7f\x7f\x7f"}
	stale.Conditions = append(stale.Conditions, metav1.Condition{Type: actor.ConditionCompactReady, Status: metav1.ConditionTrue})
	full := readRegistry()
	full.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: actor.ReasonIndexFull}}
	for _, tc := range []struct {
		name string
		st   v1alpha1.AgentIndexStatus
		ok   bool
	}{
		{"legacy, built", legacy, true},
		{"legacy, full", full, false},
		{"compact, built", compact, true},
		{"compact, full", notBuilt, false},
		{"stale compact maps: a writer of the legacy format rewrote Ready", stale, true},
		{"never built", v1alpha1.AgentIndexStatus{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := actor.Readable(&tc.st)
			require.Equal(t, tc.ok, ok)
			if !ok {
				return
			}
			assert.Equal(t, legacy.Users, got.Users)
			assert.Equal(t, legacy.Groups, got.Groups, "a later release's guard is not read")
			assert.Empty(t, got.CompactUsers)
		})
	}
	assert.False(t, actor.IsCompact(&legacy))
	assert.True(t, actor.IsCompact(&compact))
}

// TestSource_CompactIndex: the CLI's Source (the first built index by name)
// and the controller's (its own) read a later release's compact index, and
// Derive and Scope decide on it as on the same entries in the legacy
// format: a v0.13 CLI keeps stamping agents once the cluster runs v0.14.
// Covers ACTOR-INDEX-04.
func TestSource_CompactIndex(t *testing.T) {
	ctx := t.Context()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	legacy := readRegistry()
	idx := &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}, Status: compactOf(legacy)}
	// An index of another release that is not built sorts first: the CLI
	// skips it.
	other := &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "a-other"}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(idx, other).WithStatusSubresource(idx, other).Build()
	for _, src := range []*actor.Source{{Reader: c}, {Reader: c, Name: "kardinal-promoter"}} {
		t.Run(fmt.Sprintf("name %q", src.Name), func(t *testing.T) {
			got, err := src.Index(ctx)
			require.NoError(t, err)
			require.NotNil(t, got, "the compact index is read")
			for _, u := range []actor.User{
				{Username: "system:serviceaccount:ci:fixer", Groups: []string{"system:authenticated"}},
				{Username: "oidc:robot", Groups: []string{"system:authenticated"}},
				{Username: "oidc:someone", Groups: []string{"system:authenticated", "oidc:agents"}},
				{Username: "system:serviceaccount:ci:parked", Groups: []string{"system:authenticated"}},
				{Username: "alice", Groups: []string{"system:authenticated"}},
				{Username: "system:serviceaccount:ci:builder", Groups: []string{"system:authenticated"}},
			} {
				want, wantErr := actor.Derive(u, &legacy)
				r, gotErr := actor.Derive(u, got)
				assert.Equal(t, fmt.Sprint(wantErr), fmt.Sprint(gotErr), u.Username)
				assert.Equal(t, want, r, u.Username)
			}
			r, err := src.Resolve(ctx, actor.User{Username: "system:serviceaccount:ci:fixer"},
				actor.Where{Namespace: "billing", Pipeline: "checkout", Environment: "test"})
			require.NoError(t, err)
			assert.Equal(t, v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer",
				Agent: "fixer", Sponsor: "alice"}, r.Actor)
			_, err = src.Resolve(ctx, actor.User{Username: "system:serviceaccount:ci:fixer"},
				actor.Where{Namespace: "other", Pipeline: "checkout"})
			assert.ErrorIs(t, err, actor.ErrOutOfScope, "the scope is read from the compact entry")
		})
	}
}
