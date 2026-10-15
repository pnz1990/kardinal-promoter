// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
// (test/scenario/matrix/part1..16): each is a go test process of its own,
// so CI's shards spread the matrix, and each part takes at most 45 s under
// -race on CI (test/hack TestScenarioDurations holds every scenario package
// under 60 s in hack/ci/test-durations.txt).
const MatrixParts = 16

// RunMatrix runs part (1-based) of MatrixRows, each row in a World of its
// own and in parallel (RunCombo). The rows are split into the parts by
// their measured cost (matrixWeights): longest first, each to the part with
// the least so far, so the parts take about as long.
func RunMatrix(t *testing.T, part int) {
	rows := MatrixRows(t)
	weights, err := matrixWeights()
	require.NoError(t, err)
	combos := make([]Combo, len(rows))
	for i, row := range rows {
		combos[i] = ComboOf(row, i%2 == 1)
	}
	assigned := splitByWeight(combos, weights, MatrixParts)
	n := 0
	for i, c := range combos {
		if assigned[i] != part-1 {
			continue
		}
		n++
		t.Run(c.Name(), func(t *testing.T) {
			t.Parallel()
			RunCombo(t, c)
		})
	}
	t.Logf("%d of %d combinations", n, len(rows))
}

// matrixWeights reads test/scenario/matrix_weights.txt: seconds by row name.
func matrixWeights() (map[string]float64, error) {
	raw, err := os.ReadFile(filepath.Join(RepoRoot(), "test", "scenario", "matrix_weights.txt"))
	if err != nil {
		return nil, fmt.Errorf("matrix weights: %w", err)
	}
	out := map[string]float64{}
	for _, line := range strings.Split(string(raw), "\n") {
		secs, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || strings.HasPrefix(secs, "#") {
			continue
		}
		v, err := strconv.ParseFloat(secs, 64)
		if err != nil {
			return nil, fmt.Errorf("matrix weights: %q: %w", line, err)
		}
		out[name] = v
	}
	return out, nil
}

// splitByWeight assigns each combo to one of parts parts (its index in the
// result): longest first, each to the part with the least weight so far; a
// combo without a weight counts as the median.
func splitByWeight(combos []Combo, weights map[string]float64, parts int) []int {
	var known []float64
	for _, w := range weights {
		known = append(known, w)
	}
	sort.Float64s(known)
	median := 1.0
	if len(known) > 0 {
		median = known[len(known)/2]
	}
	order := make([]int, len(combos))
	cost := make([]float64, len(combos))
	for i, c := range combos {
		order[i] = i
		cost[i] = median
		if w, ok := weights[c.Name()]; ok {
			cost[i] = w
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		if cost[order[a]] != cost[order[b]] {
			return cost[order[a]] > cost[order[b]]
		}
		return combos[order[a]].Name() < combos[order[b]].Name()
	})
	load := make([]float64, parts)
	out := make([]int, len(combos))
	for _, i := range order {
		best := 0
		for p := 1; p < parts; p++ {
			if load[p] < load[best] {
				best = p
			}
		}
		out[i] = best
		load[best] += cost[i]
	}
	return out
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
