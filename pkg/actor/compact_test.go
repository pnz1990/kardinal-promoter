// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// TestEncodeEntry: an entry survives the compact format, the blocked text
// whole (it is the last field), and a field holding a separator is refused.
// Covers ACTOR-INDEX-01.
func TestEncodeEntry(t *testing.T) {
	for _, e := range []v1alpha1.AgentIndexEntry{
		{Agent: "fixer", Sponsor: "alice", Autonomy: "approve", Namespaces: []string{"payments-*", "billing"},
			Pipelines: []string{"*"}, Environments: []string{"test", "uat"}},
		{Agent: "parked", Sponsor: "auth0|bob,eng", Blocked: "suspended"},
		{Agent: "odd", Sponsor: "carol", Pipelines: []string{"*"}, Environments: []string{"*"},
			Blocked: "not ready: a reason with\x7fa separator\x1fand more"},
	} {
		s, ok := actor.EncodeEntry(e)
		require.True(t, ok, "%+v", e)
		assert.Equal(t, e, actor.DecodeEntry(s))
	}
	for _, e := range []v1alpha1.AgentIndexEntry{
		{Agent: "a", Sponsor: "x\x7fy"},
		{Agent: "a", Sponsor: "x\x1fy"},
		{Agent: "a", Namespaces: []string{"ns\x1fother"}},
		{Agent: "a", Environments: []string{"test\x7fprod"}},
		{Agent: "a", Pipelines: []string{""}},
	} {
		_, ok := actor.EncodeEntry(e)
		assert.False(t, ok, "%q", e)
	}
	assert.Equal(t, "the agent index entry is malformed", actor.DecodeEntry("fixer\x7falice").Blocked)
}

func ids() []v1alpha1.AgentIdentity {
	at := func(m int) metav1.Time { return metav1.NewTime(time.Date(2026, 10, 1, 0, m, 0, 0, time.UTC)) }
	id := func(name string, m int, sponsor string, scope v1alpha1.AgentScope, subjects ...v1alpha1.AgentSubject) v1alpha1.AgentIdentity {
		return v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: at(m)},
			Spec: v1alpha1.AgentIdentitySpec{Subjects: subjects, Sponsor: v1alpha1.AgentSponsor{User: sponsor}, Scope: scope}}
	}
	scope := v1alpha1.AgentScope{Namespaces: []string{"payments-*"}, Pipelines: []string{"checkout"}, Environments: []string{"test"}}
	parked := id("parked", 2, "bob", scope, v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: "ci", Name: "parked"})
	parked.Spec.Suspended = true
	return []v1alpha1.AgentIdentity{
		id("fixer", 0, "alice", scope, v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"},
			v1alpha1.AgentSubject{Kind: "Group", Name: "oidc:agents-fixer"}),
		id("reviewer", 1, "alice", v1alpha1.AgentScope{Namespaces: []string{"*"}}, v1alpha1.AgentSubject{Kind: "User", Name: "oidc:reviewer"}),
		parked,
		id("late", 3, "carol", scope, v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: "ci", Name: "fixer"}),
		id("odd", 4, "x\x7fy", scope, v1alpha1.AgentSubject{Kind: "User", Name: "oidc:odd"}),
	}
}

// TestEvaluateCompact: the compact registry holds the same entries as the
// legacy one (decoded), in fewer bytes, with the same verdicts; its legacy
// maps hold only the guard older readers fail closed on; an entry that
// cannot be encoded denies its agent.
// Covers ACTOR-INDEX-01.
func TestEvaluateCompact(t *testing.T) {
	legacy := actor.Evaluate(ids(), actor.MaxIndexBytes)
	compact := actor.EvaluateCompact(ids(), actor.MaxIndexBytes)
	assert.Equal(t, legacy.Verdicts, compact.Verdicts)
	assert.Empty(t, compact.Index.Users)
	assert.Equal(t, map[string]v1alpha1.AgentIndexEntry{actor.LegacyReaderGroup: actor.LegacyReaderGuard}, compact.Index.Groups)
	require.Len(t, compact.Index.CompactUsers, len(legacy.Index.Users))
	for k, want := range legacy.Index.Users {
		got := actor.DecodeEntry(compact.Index.CompactUsers[k])
		if k == "oidc:odd" {
			assert.Equal(t, "odd", got.Agent)
			assert.Contains(t, got.Blocked, "cannot encode", "a sponsor with a separator denies the agent")
			continue
		}
		assert.Equal(t, want, got, k)
	}
	for k, want := range legacy.Index.Groups {
		assert.Equal(t, want, actor.DecodeEntry(compact.Index.CompactGroups[k]), k)
	}
	assert.Less(t, compact.Bytes, legacy.Bytes)
	t.Logf("legacy %d bytes, compact %d bytes for %d identities", legacy.Bytes, compact.Bytes, len(ids()))

	// The bound counts each entry as the API server stores it: its key and
	// its value in JSON, a colon and a comma. The field separator stays one
	// byte in JSON; a list separator is escaped.
	stored := 0
	for _, m := range []map[string]string{compact.Index.CompactUsers, compact.Index.CompactGroups} {
		for k, v := range m {
			key, err := json.Marshal(k)
			require.NoError(t, err)
			val, err := json.Marshal(v)
			require.NoError(t, err)
			stored += len(key) + len(val) + 2
		}
	}
	assert.Equal(t, stored, compact.Bytes)
	one, ok := actor.EncodeEntry(v1alpha1.AgentIndexEntry{Agent: "fixer", Sponsor: "alice", Namespaces: []string{"payments-*"}})
	require.True(t, ok)
	raw, err := json.Marshal(one)
	require.NoError(t, err)
	assert.Equal(t, len(one)+2, len(raw), "an entry with lists of one item is stored unescaped: %s", raw)

	// Over the bound: written empty, the guard kept, every verdict IndexFull.
	full := actor.EvaluateCompact(ids(), 100)
	assert.True(t, full.Full)
	assert.Empty(t, full.Index.CompactUsers)
	assert.Equal(t, actor.LegacyReaderGuard, full.Index.Groups[actor.LegacyReaderGroup])
	for name, v := range full.Verdicts {
		assert.Equal(t, actor.ReasonIndexFull, v.Reason, name)
	}
}

func cond(typ string, status metav1.ConditionStatus, reason string) metav1.Condition {
	return metav1.Condition{Type: typ, Status: status, Reason: reason}
}

// TestReadable: the current format is the one Ready's reason names, so a
// compact index is decoded, a legacy one read as is, stale compact maps an
// older writer left behind are ignored, and an index not built is not read.
// Covers ACTOR-INDEX-01.
func TestReadable(t *testing.T) {
	legacy := actor.Evaluate(ids(), actor.MaxIndexBytes).Index
	compact := actor.EvaluateCompact(ids(), actor.MaxIndexBytes).Index
	for _, tc := range []struct {
		name  string
		st    v1alpha1.AgentIndexStatus
		conds []metav1.Condition
		ok    bool
		want  map[string]v1alpha1.AgentIndexEntry
	}{
		{"legacy, built", legacy, []metav1.Condition{cond("Ready", "True", "Built")}, true, legacy.Users},
		{"legacy, full", legacy, []metav1.Condition{cond("Ready", "False", actor.ReasonIndexFull)}, false, nil},
		{"compact, built", compact, []metav1.Condition{cond("Ready", "False", actor.ReasonCompactFormat),
			cond(actor.ConditionCompactReady, "True", "Built")}, true, nil},
		{"compact, full", compact, []metav1.Condition{cond("Ready", "False", actor.ReasonCompactFormat),
			cond(actor.ConditionCompactReady, "False", actor.ReasonIndexFull)}, false, nil},
		{"stale compact maps: an older writer rewrote Ready", func() v1alpha1.AgentIndexStatus {
			s := legacy
			s.CompactUsers = map[string]string{"system:serviceaccount:ci:fixer": "stale\x7f\x7f\x7f\x7f\x7f\x7f"}
			return s
		}(), []metav1.Condition{cond("Ready", "True", "Built"), cond(actor.ConditionCompactReady, "True", "Built")}, true, legacy.Users},
		{"never built", v1alpha1.AgentIndexStatus{}, nil, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.st
			st.Conditions = tc.conds
			got, ok := actor.Readable(&st)
			require.Equal(t, tc.ok, ok)
			if !ok {
				return
			}
			want := tc.want
			if want == nil {
				want = map[string]v1alpha1.AgentIndexEntry{}
				for k, v := range compact.CompactUsers {
					want[k] = actor.DecodeEntry(v)
				}
			}
			assert.Equal(t, want, got.Users)
			assert.NotContains(t, got.Groups, actor.LegacyReaderGroup, "the guard is for older readers only")
			assert.Empty(t, got.CompactUsers)
		})
	}
}

// TestDeriveCompact: Derive on a compact index (as actor.Source reads it)
// decides as on the legacy one, for every subject of the registry.
func TestDeriveCompact(t *testing.T) {
	legacy := actor.Evaluate(ids(), actor.MaxIndexBytes).Index
	st := actor.EvaluateCompact(ids(), actor.MaxIndexBytes).Index
	st.Conditions = []metav1.Condition{cond("Ready", "False", actor.ReasonCompactFormat), cond(actor.ConditionCompactReady, "True", "Built")}
	compact, ok := actor.Readable(&st)
	require.True(t, ok)
	for _, u := range []actor.User{
		{Username: "system:serviceaccount:ci:fixer", Groups: []string{"system:authenticated"}},
		{Username: "oidc:someone", Groups: []string{"system:authenticated", "oidc:agents-fixer"}},
		{Username: "oidc:reviewer", Groups: []string{"system:authenticated"}},
		{Username: "system:serviceaccount:ci:parked"},
		{Username: "alice", Groups: []string{"system:authenticated"}},
		{Username: "system:serviceaccount:ci:builder"},
	} {
		t.Run(u.Username, func(t *testing.T) {
			want, wantErr := actor.Derive(u, &legacy)
			got, gotErr := actor.Derive(u, compact)
			assert.Equal(t, fmt.Sprint(wantErr), fmt.Sprint(gotErr))
			assert.Equal(t, want, got)
		})
	}
}

// compactEntry is e in the compact format (#2071): its seven fields joined
// with DEL, each list's items with \x1f. It is spelled out here, not taken
// from EncodeEntry, so the format v0.13's reader decodes is pinned.
func compactEntry(e v1alpha1.AgentIndexEntry) string {
	return strings.Join([]string{e.Agent, e.Sponsor, e.Autonomy, strings.Join(e.Namespaces, "\x1f"),
		strings.Join(e.Pipelines, "\x1f"), strings.Join(e.Environments, "\x1f"), e.Blocked}, "\x7f")
}

// compactOf is legacy's entries in the compact format, as the controller
// writes the index: Ready False (CompactFormat), CompactReady True, the
// legacy maps holding only the guard.
func compactOf(legacy v1alpha1.AgentIndexStatus) v1alpha1.AgentIndexStatus {
	st := v1alpha1.AgentIndexStatus{CompactUsers: map[string]string{}, CompactGroups: map[string]string{},
		Groups: map[string]v1alpha1.AgentIndexEntry{actor.LegacyReaderGroup: actor.LegacyReaderGuard}}
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

// TestDecodeEntry pins the compact format, which v0.13 reads too: the
// seven fields in order, lists split on \x1f (an empty one is no items), the
// blocked text whole whatever it holds, and an entry short of a field read
// as blocked.
// Covers ACTOR-INDEX-01, ACTOR-INDEX-04.
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
		enc, ok := actor.EncodeEntry(e)
		require.True(t, ok)
		assert.Equal(t, compactEntry(e), enc, "the writer writes the pinned format")
	}
}

// TestSource_CompactIndex: the CLI's Source (the first built index by name)
// and the controller's (its own) read the compact index, and Derive and
// Scope decide on it as on the same entries in the legacy format (v0.13's
// Source, which reads the same way, keeps stamping agents against v0.14).
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
