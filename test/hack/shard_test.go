// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShardTests (#1680): hack/e2e/run.sh SHARD i/n splits the tests by
// hack/e2e/durations.txt, every test in exactly one part, a test missing
// from the file too, and the parts' estimated wall times (serial seconds
// plus parallel seconds / 3) within 10% of each other, so no core job is
// the one that hits the CI job limit.
func TestShardTests(t *testing.T) {
	root := repoRoot(t)
	durations := filepath.Join(root, "hack", "e2e", "durations.txt")
	sec, serial := map[string]float64{}, map[string]bool{}
	f, err := os.Open(durations)
	require.NoError(t, err)
	defer f.Close()
	var names []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		s, err := strconv.ParseFloat(fields[1], 64)
		require.NoError(t, err, sc.Text())
		sec[fields[0]], serial[fields[0]] = s, fields[2] == "serial"
		names = append(names, fields[0])
	}
	require.NoError(t, sc.Err())
	require.NotEmpty(t, names)
	names = append(names, "TestNotMeasuredYet")

	for _, n := range []int{2, 3} {
		seen := map[string]int{}
		var est []float64
		for i := 1; i <= n; i++ {
			script := `eval "$(sed -n '/^shard_tests() {/,/^}/p' hack/e2e/run.sh)"; shard_tests "$1" "$2" hack/e2e/durations.txt`
			cmd := exec.Command("bash", "-c", script, "shard", strconv.Itoa(i), strconv.Itoa(n))
			cmd.Dir = root
			cmd.Stdin = strings.NewReader(strings.Join(names, "\n") + "\n")
			out, err := cmd.Output()
			require.NoError(t, err)
			var s, p float64
			for _, name := range strings.Fields(string(out)) {
				seen[name]++
				if serial[name] {
					s += sec[name]
				} else {
					p += sec[name]
				}
			}
			est = append(est, s+p/3)
		}
		for _, name := range names {
			assert.Equal(t, 1, seen[name], "%s in exactly one of %d parts", name, n)
		}
		lo, hi := est[0], est[0]
		for _, e := range est {
			lo, hi = min(lo, e), max(hi, e)
		}
		assert.LessOrEqual(t, hi, lo*1.1, "%d parts balanced: %v", n, est)
	}
}
