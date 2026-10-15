// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
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

// MatrixParts is how many packages the matrix runs in
// (test/scenario/matrix/part1..12): each is a go test process of its own,
// so CI's shards spread the matrix, and each part takes at most 45 s under
// -race on CI (test/hack TestScenarioDurations holds every scenario package
// under 60 s in hack/ci/test-durations.txt).
const MatrixParts = 12

// RunMatrix runs part (1-based) of MatrixRows: every MatrixParts-th row,
// each in a World of its own and in parallel (RunCombo).
func RunMatrix(t *testing.T, part int) {
	rows := MatrixRows(t)
	n := 0
	for i, row := range rows {
		if i%MatrixParts != part-1 {
			continue
		}
		n++
		c := ComboOf(row, i%2 == 1)
		t.Run(c.Name(), func(t *testing.T) {
			t.Parallel()
			RunCombo(t, c)
		})
	}
	t.Logf("%d of %d combinations", n, len(rows))
}

// MainWithRenderJobs is a scenario package's TestMain: it marks the process
// as a render Job (steps.RenderJobEnv), as the kardinal-render image does,
// since the World's kubelet runs render Jobs in this process
// (World.runJobs). The controller's own step sequences never contain
// render-manifests, so the mark changes nothing for them.
func MainWithRenderJobs(m *testing.M) {
	if err := os.Setenv(steps.RenderJobEnv, "1"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
