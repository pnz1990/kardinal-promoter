// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// MatrixRows returns the scenario matrix: the pairwise rows of Dimensions
// that the API server admits, then one row per Rejection, which it must
// refuse, then one row per ActorRefusal, whose version it must refuse until
// the cluster is put right.
func MatrixRows(t *testing.T) [][]string {
	rows := Pairwise(Dimensions, func(row []string) bool { return Rejected(row) != nil })
	require.Empty(t, Covers(Dimensions, rows, func(row []string) bool { return Rejected(row) != nil }),
		"the rows miss pairs")
	for _, rej := range Rejections {
		row := append([]string(nil), rows[0]...)
		for d, v := range rej.Example {
			row[d] = v
		}
		require.True(t, rej.Matches(row), "the example of %q", rej.Message)
		rows = append(rows, row)
	}
	for _, r := range ActorRefusals {
		row := append([]string(nil), rows[0]...)
		row[DimActor], row[DimOutcome] = r.Actor, "success"
		rows = append(rows, row)
	}
	return rows
}

// TestScenarioMatrix runs the scenario matrix (MatrixRows), each row in a
// World of its own and in parallel (RunCombo). Covers SCEN-MATRIX-01,
// SCEN-REJECT-01, SCEN-ACTOR-01, SCEN-VERIFY-01.
func TestScenarioMatrix(t *testing.T) {
	rows := MatrixRows(t)
	for i, row := range rows {
		c := ComboOf(row, i%2 == 1)
		t.Run(c.Name(), func(t *testing.T) {
			t.Parallel()
			RunCombo(t, c)
		})
	}
	t.Logf("%d combinations", len(rows))
}
