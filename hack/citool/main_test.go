// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestSplit(t *testing.T) {
	durations := map[string]float64{"a": 90, "b": 60, "c": 30, "d": 30, "e": 20, "f": 10}
	tests := []struct {
		name string
		pkgs []string
		n    int
		want [][]string
	}{
		{"one shard takes everything", []string{"b", "a"}, 1, [][]string{{"a", "b"}}},
		{"longest first to the least loaded", []string{"a", "b", "c", "d", "e", "f"}, 2,
			[][]string{{"a", "d"}, {"b", "c", "e", "f"}}},
		{"more shards than packages", []string{"a"}, 3, [][]string{{"a"}, nil, nil}},
		// A new package counts as the median (30), here after a, b.
		{"an unmeasured package", []string{"a", "b", "new"}, 2, [][]string{{"a"}, {"b", "new"}}},
		{"duplicates and blanks", []string{"a", "a", " ", "b"}, 2, [][]string{{"a"}, {"b"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, split(tt.pkgs, durations, tt.n))
		})
	}
}

// Every package lands in exactly one shard, whatever the input order, and the
// split of the committed durations is balanced.
func TestSplitCoversEveryPackageOnce(t *testing.T) {
	durations, err := readDurations(filepath.Join("..", "ci", "test-durations.txt"))
	require.NoError(t, err)
	require.NotEmpty(t, durations)
	var pkgs []string
	for p := range durations {
		pkgs = append(pkgs, p)
	}
	pkgs = append(pkgs, "github.com/kardinal-promoter/kardinal-promoter/pkg/not-measured-yet")
	for _, n := range []int{2, 3, 4, 6} {
		shards := split(pkgs, durations, n)
		reversed := make([]string, len(pkgs))
		for i, p := range pkgs {
			reversed[len(pkgs)-1-i] = p
		}
		assert.Equal(t, shards, split(reversed, durations, n), "the split does not depend on input order")
		seen := map[string]int{}
		for _, s := range shards {
			for _, p := range s {
				seen[p]++
			}
		}
		for _, p := range pkgs {
			assert.Equal(t, 1, seen[p], "%s in exactly one of %d shards", p, n)
		}
		assert.Less(t, skew(n, durations, durations), 1.25, "%d shards balanced", n)
	}
}

func TestShardCommand(t *testing.T) {
	durations := writeFile(t, "d.txt", "# comment\na 90\nb 60 # trailing\nc 30\n\n")
	tests := []struct {
		args    []string
		want    string
		wantErr string
	}{
		{args: []string{"shard", "1/2", durations}, want: "a\n"},
		{args: []string{"shard", "2/2", durations}, want: "b\nc\n"},
		{args: []string{"shard", "3/2", durations}, wantErr: "want I/N"},
		{args: []string{"shard", "0/2", durations}, wantErr: "want I/N"},
		{args: []string{"shard", "x", durations}, wantErr: "want I/N"},
		{args: []string{"shard", "1/2", "/nonexistent"}, wantErr: "open durations"},
		{args: []string{"shard", "1/2", writeFile(t, "bad.txt", "a b c\n")}, wantErr: "want \"package seconds\""},
		{args: []string{"shard", "1/2", writeFile(t, "neg.txt", "a -1\n")}, wantErr: "bad seconds"},
		{args: []string{"nope"}, wantErr: "unknown command"},
		{args: nil, wantErr: "usage"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var out bytes.Buffer
			err := run(tt.args, strings.NewReader("a\nb\nc\n"), &out)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, out.String())
		})
	}
}

const ghLog = `2026-10-13T22:01:26.4382619Z ok  	example.com/m/a	26.555s
2026-10-13T22:01:27.0000000Z ok  	example.com/m/b	1.000s
2026-10-13T22:01:27.0000000Z ok  	example.com/m/cached	(cached)
2026-10-13T22:01:27.0000000Z ?   	example.com/m/none	[no test files]
2026-10-13T22:01:27.0000000Z ok  	example.com/m/notests	0.5s [no tests to run]
`

func TestDurations(t *testing.T) {
	second := "ok  \texample.com/m/a\t20.000s\nok  \texample.com/m/b\t3.0s\n"
	third := "ok  \texample.com/m/a\t30.0s\n"
	d, err := measure(strings.NewReader(ghLog), strings.NewReader(second), strings.NewReader(third))
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{
		"example.com/m/a":       26.555, // median of 20, 26.555, 30
		"example.com/m/b":       2,      // mean of the middle two
		"example.com/m/notests": 0.5,
	}, d)

	var out bytes.Buffer
	require.NoError(t, run([]string{"durations", writeFile(t, "a.log", ghLog)}, nil, &out))
	assert.True(t, strings.HasPrefix(out.String(), durationsHeader))
	assert.Contains(t, out.String(), "\nexample.com/m/a 26.6\nexample.com/m/b 1.0\n")
	back, err := readDurations(writeFile(t, "d.txt", out.String()))
	require.NoError(t, err, "the output reads back as a durations file")
	assert.Len(t, back, 3)

	assert.Error(t, run([]string{"durations"}, nil, io.Discard))
	assert.Error(t, run([]string{"durations", "/nonexistent"}, nil, io.Discard))
}

func TestSkew(t *testing.T) {
	current := map[string]float64{"a": 10, "b": 10, "c": 10, "d": 10}
	tests := []struct {
		name     string
		measured map[string]float64
		want     float64
	}{
		{"unchanged", current, 1},
		{"one package got slow", map[string]float64{"a": 70, "b": 10, "c": 10, "d": 10}, 1.6},
		// e is split as the median (10) and measures 40: a, c, e = 60 of 80.
		{"a new package", map[string]float64{"a": 10, "b": 10, "c": 10, "d": 10, "e": 40}, 1.5},
		{"nothing measured", map[string]float64{}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, skew(2, current, tt.measured), 0.001)
		})
	}
	var out bytes.Buffer
	cur := writeFile(t, "c.txt", "a 10\nb 10\n")
	require.NoError(t, run([]string{"skew", "2", cur, writeFile(t, "m.txt", "a 30\nb 10\n")}, nil, &out))
	assert.Equal(t, "1.50\n", out.String())
	assert.Error(t, run([]string{"skew", "0", cur, cur}, nil, io.Discard))
	assert.Error(t, run([]string{"skew", "2", cur}, nil, io.Discard))
}

func TestParseFailures(t *testing.T) {
	tests := []struct {
		name string
		log  string
		want []Failure
	}{
		{"a passing run", "ok  \tm/a\t1.0s\nok  \tm/b\t2.0s\n", nil},
		{"a failing test and a passing package", `--- FAIL: TestX (0.01s)
    x_test.go:10: boom
FAIL
FAIL	m/a	0.5s
ok  	m/b	2.0s
`, []Failure{{"m/a", "TestX"}}},
		{"subtests: the leaf only", `--- FAIL: TestX (0.01s)
    --- FAIL: TestX/case_one (0.00s)
    --- FAIL: TestX/case_two (0.00s)
--- FAIL: TestY (0.00s)
FAIL
FAIL	m/a	0.5s
`, []Failure{{"m/a", "TestX/case_one"}, {"m/a", "TestX/case_two"}, {"m/a", "TestY"}}},
		{"the GitHub timestamps", "2026-10-13T22:01:26.1Z --- FAIL: TestX (0.01s)\n2026-10-13T22:01:26.2Z FAIL\tm/a\t0.5s\n",
			[]Failure{{"m/a", "TestX"}}},
		{"a build failure", "# m/a\na/x.go:3:1: syntax error\nFAIL\tm/a [build failed]\n", []Failure{{"m/a", "-"}}},
		{"a timeout names the running test", `panic: test timed out after 2m0s
	running tests:
		TestSlow (2m0s)

goroutine 1 [running]:
FAIL	m/a	120.1s
`, []Failure{{"m/a", "TestSlow (timeout)"}}},
		{"a timeout in a subtest names the subtest", `panic: test timed out after 2m0s
	running tests:
		TestSlow (2m0s)
		TestSlow/big_file (1m59s)
FAIL	m/a	120.1s
`, []Failure{{"m/a", "TestSlow/big_file (timeout)"}}},
		{"a passing package resets what came before", "--- FAIL: TestNoise (0.0s)\nok  \tm/a\t1.0s\nFAIL\tm/b\t1.0s\n",
			[]Failure{{"m/b", "-"}}},
		{"the same failure twice", "--- FAIL: TestX (0.0s)\nFAIL\tm/a\t1.0s\n--- FAIL: TestX (0.0s)\nFAIL\tm/a\t1.0s\n",
			[]Failure{{"m/a", "TestX"}}},
		{"not a go test log", "error: lint failed\nProcess completed with exit code 1.\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseFailures(strings.NewReader(tt.log)))
		})
	}
	var out bytes.Buffer
	require.NoError(t, run([]string{"failures"}, strings.NewReader("--- FAIL: TestX (0.0s)\nFAIL\tm/a\t1.0s\n"), &out))
	assert.Equal(t, "m/a\tTestX\n", out.String())
}

func TestFindFlakes(t *testing.T) {
	logs := map[string]string{
		"11": "--- FAIL: TestFlaky (0.1s)\nFAIL\tm/a\t1.0s\n",
		"21": "--- FAIL: TestFlaky (0.1s)\nFAIL\tm/a\t1.0s\n",
		"31": "--- FAIL: TestBroken (0.1s)\nFAIL\tm/b\t1.0s\n",
		"41": "Error: golangci-lint found 1 issue\n",
	}
	open := func(id string) (io.ReadCloser, error) {
		l, ok := logs[id]
		if !ok {
			return nil, fmt.Errorf("job %s: %w", id, os.ErrNotExist)
		}
		return io.NopCloser(strings.NewReader(l)), nil
	}
	recs := []Record{
		// Failed on attempt 1, passed on attempt 2: a flake.
		{"s1", "100", "1", "go test (1/4)", "11", "failure"},
		{"s1", "100", "2", "go test (1/4)", "12", "success"},
		// The same test on another commit, rerun green: counted twice.
		{"s2", "200", "1", "go test (1/4)", "21", "failure"},
		{"s2", "200", "2", "go test (1/4)", "22", "success"},
		// Failed on every attempt: broken, not flaky.
		{"s3", "300", "1", "go test (2/4)", "31", "failure"},
		{"s3", "300", "2", "go test (2/4)", "32", "failure"},
		// A non-test job that flaked.
		{"s4", "400", "1", "golangci-lint", "41", "failure"},
		{"s4", "401", "1", "golangci-lint", "42", "success"},
		// A flake whose log has expired is skipped.
		{"s5", "500", "1", "go test (3/4)", "51", "failure"},
		{"s5", "500", "2", "go test (3/4)", "52", "success"},
		// Another job passing on the same commit does not count.
		{"s3", "300", "2", "go test (1/4)", "33", "success"},
	}
	flakes, err := findFlakes(recs, open)
	require.NoError(t, err)
	assert.Equal(t, []Flake{
		{Failure: Failure{"m/a", "TestFlaky"}, Job: "go test (1/4)", Runs: []string{"100/1", "200/1"}},
		{Failure: Failure{"-", "-"}, Job: "golangci-lint", Runs: []string{"400/1"}},
	}, flakes)

	body := renderFlakes("o/r", flakes)
	assert.Contains(t, body, "| `TestFlaky` | `m/a` | go test (1/4) | 2 | [100#1](https://github.com/o/r/actions/runs/100/attempts/1) [200#1]")
	assert.Contains(t, body, "(no failing test in the log)")
	assert.Contains(t, renderFlakes("o/r", nil), "No flaky tests")

	_, err = findFlakes(recs[:2], func(string) (io.ReadCloser, error) { return nil, os.ErrPermission })
	assert.ErrorIs(t, err, os.ErrPermission)
}

func TestFlakyCommand(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "11.log"), []byte("--- FAIL: TestFlaky (0.1s)\nFAIL\tm/a\t1.0s\n"), 0o600))
	recs := writeFile(t, "r.tsv", "s1\t100\t1\tgo test (1/4)\t11\tfailure\ns1\t100\t2\tgo test (1/4)\t12\tsuccess\nshort line\n")
	var out bytes.Buffer
	require.NoError(t, run([]string{"flaky", "-repo", "o/r", recs, dir}, nil, &out))
	assert.Contains(t, out.String(), "`TestFlaky`")
	assert.Error(t, run([]string{"flaky", recs, dir}, nil, io.Discard), "-repo is required")
	assert.Error(t, run([]string{"flaky", "-repo", "o/r", "/nonexistent", dir}, nil, io.Discard))
}
