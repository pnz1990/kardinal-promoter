// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestContains (#1575 QA): ancestry through every parent, Unknown where the
// graph stops (the shallow boundary, or a revision not in it), and
// abbreviated SHAs.
func TestContains(t *testing.T) {
	// R←B←{L1, P}←M, fetched down to B (R is the boundary).
	g := map[string]scm.GraphCommit{
		"m000000000": {Parents: []string{"l100000000", "p000000000"}, Paths: []string{"m"}},
		"l100000000": {Parents: []string{"b000000000"}, Paths: []string{"env/test/k.yaml"}},
		"p000000000": {Parents: []string{"b000000000"}, Paths: []string{"p"}},
		"b000000000": {Parents: []string{"r000000000"}, Partial: true},
	}
	for _, tc := range []struct {
		name      string
		rev, want string
		got       scm.Ancestry
	}{
		{"a merge contains its second parent", "m000000000", "p000000000", scm.AncestryContains},
		{"a sibling does not", "l100000000", "p000000000", scm.AncestryUnknown},
		{"the boundary commit itself is known", "b000000000", "r000000000", scm.AncestryContains},
		{"itself", "p000000000", "p000000000", scm.AncestryContains},
		{"abbreviated", "m000000", "b000000", scm.AncestryContains},
		{"a revision not in the graph", "x000000000", "b000000000", scm.AncestryUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.got, scm.Contains(g, tc.rev, tc.want)) })
	}
	// The whole history read: not containing is definite.
	full := map[string]scm.GraphCommit{"b": {Parents: []string{"a"}}, "a": {}, "c": {Parents: []string{"a"}}}
	assert.Equal(t, scm.AncestryNotContains, scm.Contains(full, "b", "c"))

	// PathsSince: the commits M has and P does not (M, L1) and their paths;
	// B is P's, so its unknown paths do not matter.
	paths, a := scm.PathsSince(g, "m000000000", "p000000000")
	assert.Equal(t, scm.AncestryContains, a)
	assert.ElementsMatch(t, []string{"m", "env/test/k.yaml"}, paths)
	_, a = scm.PathsSince(g, "l100000000", "p000000000")
	assert.Equal(t, scm.AncestryUnknown, a, "L1 does not contain P")
	// Since B: every commit after it (M, L1, P); B's own unknown paths and
	// R past the boundary are B's history, not walked.
	paths, a = scm.PathsSince(g, "m000000000", "b000000000")
	assert.Equal(t, scm.AncestryContains, a)
	assert.ElementsMatch(t, []string{"m", "env/test/k.yaml", "p"}, paths)
}
