// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPairwise: the rows cover every pair of values of two dimensions,
// skip the excluded pairs, contain no excluded row, are the same on every
// call, and are far fewer than the full product. Covers SCEN-MATRIX-01.
func TestPairwise(t *testing.T) {
	dims := []Dimension{
		{Name: "a", Values: []string{"a1", "a2", "a3"}},
		{Name: "b", Values: []string{"b1", "b2"}},
		{Name: "c", Values: []string{"c1", "c2", "c3", "c4"}},
		{Name: "d", Values: []string{"d1", "d2"}},
	}
	excluded := func(row []string) bool { return row[0] == "a1" && row[1] == "b2" }
	rows := Pairwise(dims, excluded)
	assert.Empty(t, Covers(dims, rows, excluded))
	for _, r := range rows {
		assert.False(t, excluded(r), "%v is excluded", r)
	}
	assert.Equal(t, rows, Pairwise(dims, excluded), "deterministic")
	assert.Less(t, len(rows), 3*2*4*2)
	assert.GreaterOrEqual(t, len(rows), 3*4, "at least the largest pair product")

	missing := Covers(dims, rows[:1], excluded)
	require.NotEmpty(t, missing, "one row covers few pairs")
	for _, p := range missing {
		assert.False(t, p.A == 0 && p.B == 1 && p.VA == "a1" && p.VB == "b2", "an excluded pair is never missing")
	}
}

// TestMatrixRows: the scenario matrix covers every admitted pair of
// Dimensions, and has one row per Rejection that hits it. Covers
// SCEN-MATRIX-01, SCEN-REJECT-01.
func TestMatrixRows(t *testing.T) {
	rows := MatrixRows(t)
	rejected := 0
	for _, r := range rows {
		if Rejected(r) != nil {
			rejected++
		}
	}
	assert.Equal(t, len(Rejections), rejected)
	assert.Less(t, len(rows), 100, "pairwise, not the product")
}
