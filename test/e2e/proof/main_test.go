// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/coverage"
)

type testResult = struct {
	Test     string `json:"test"`
	Action   string `json:"action"`
	KnownBug int    `json:"knownBug,omitempty"`
}

// suiteRun is a suite's summary; action "known-bug" is an expected failure
// of known bug #1473.
func suiteRun(suite string, results map[string]string) summary {
	s := summary{Suite: suite}
	for name, action := range results {
		r := testResult{Test: name, Action: action}
		if action == "known-bug" {
			r.Action, r.KnownBug = "xfail", 1473
		}
		s.Results = append(s.Results, r)
	}
	return s
}

func TestProve(t *testing.T) {
	rows := []coverage.Row{
		{ID: "CORE-01", Tier: "live"},
		{ID: "CORE-02", Tier: "live", Status: "known-bug"},
		{ID: "CORE-03", Tier: "live"},
		{ID: "SCM-01", Tier: "live"},
		{ID: "BB-01", Tier: "contract"},
		{ID: "TODO-01", Tier: "live"},
		{ID: "TWO-01", Tier: "live"},
	}
	tests := []coverage.Test{
		{Name: "TestCore_A", Live: true, IDs: []string{"CORE-01", "TWO-01"}},
		{Name: "TestCore_B", Live: true, IDs: []string{"CORE-02"}},
		{Name: "TestCore_C", Live: true, IDs: []string{"CORE-03"}},
		{Name: "TestSCM_A", Live: true, IDs: []string{"SCM-01", "TWO-01"}},
		{Name: "TestBitbucket", IDs: []string{"BB-01"}},
	}
	runs := map[string]*regexp.Regexp{
		"core": regexp.MustCompile(`^TestCore_`),
		"scm":  regexp.MustCompile(`^TestSCM_`),
	}

	cases := []struct {
		name      string
		summaries []summary
		isOpen    func(int) (bool, error)
		want      map[string]string
	}{{
		name: "only core ran",
		summaries: []summary{suiteRun("core", map[string]string{
			"TestCore_A": "pass", "TestCore_B": "fail",
		})},
		want: map[string]string{
			"CORE-01": passed, "CORE-02": failed, "CORE-03": missing, "SCM-01": notRun,
			"BB-01": unit, "TODO-01": todo,
			// TestSCM_A's suite did not run, so TestCore_A proves the row alone.
			"TWO-01": passed,
		},
	}, {
		name: "a skip is not a pass",
		summaries: []summary{suiteRun("core", map[string]string{
			"TestCore_A": "pass", "TestCore_B": "skip", "TestCore_C": "pass",
		})},
		want: map[string]string{"CORE-01": passed, "CORE-02": failed, "CORE-03": passed},
	}, {
		name: "an expected failure is a known bug, not a failure",
		summaries: []summary{suiteRun("core", map[string]string{
			"TestCore_A": "pass", "TestCore_B": "known-bug", "TestCore_C": "skip",
		})},
		isOpen: func(n int) (bool, error) { return n == 1473, nil },
		want:   map[string]string{"CORE-01": passed, "CORE-02": known, "CORE-03": failed},
	}, {
		name: "a known bug whose issue is closed fails",
		summaries: []summary{suiteRun("core", map[string]string{
			"TestCore_A": "pass", "TestCore_B": "known-bug",
		})},
		isOpen: func(int) (bool, error) { return false, nil },
		want:   map[string]string{"CORE-02": failed},
	}, {
		name: "a known bug whose issue cannot be read fails",
		summaries: []summary{suiteRun("core", map[string]string{
			"TestCore_A": "pass", "TestCore_B": "known-bug",
		})},
		isOpen: func(int) (bool, error) { return false, errors.New("HTTP 500") },
		want:   map[string]string{"CORE-02": failed},
	}, {
		name: "a covered row whose test is an expected failure fails",
		summaries: []summary{suiteRun("core", map[string]string{
			"TestCore_A": "known-bug",
		})},
		want: map[string]string{"CORE-01": failed},
	}, {
		name: "every suite ran",
		summaries: []summary{
			suiteRun("core", map[string]string{"TestCore_A": "pass", "TestCore_B": "pass", "TestCore_C": "pass"}),
			suiteRun("scm", map[string]string{}),
		},
		// TestSCM_A should have run and did not.
		want: map[string]string{"SCM-01": missing, "TWO-01": missing},
	}, {
		name: "a pass in one suite run and a fail in another",
		summaries: []summary{
			suiteRun("core", map[string]string{"TestCore_A": "pass"}),
			suiteRun("core", map[string]string{"TestCore_A": "fail"}),
		},
		want: map[string]string{"CORE-01": failed},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := map[string]string{}
			for _, r := range prove(rows, tests, runs, tc.summaries, tc.isOpen) {
				got[r.ID] = r.Result
			}
			for id, want := range tc.want {
				assert.Equal(t, want, got[id], id)
			}
		})
	}
}

func TestOK(t *testing.T) {
	rr := func(tier, result string) rowResult {
		return rowResult{Row: coverage.Row{ID: "X-01", Tier: tier}, Result: result}
	}
	cases := []struct {
		name              string
		results           []rowResult
		partial, complete bool
	}{
		{"all passed", []rowResult{rr("live", passed), rr("contract", unit)}, true, true},
		{"a failed row", []rowResult{rr("live", passed), rr("live", failed)}, false, false},
		{"a missing row", []rowResult{rr("live", missing)}, false, false},
		{"a live row not run", []rowResult{rr("live", passed), rr("live", notRun)}, true, false},
		{"a live row todo", []rowResult{rr("live", todo)}, true, false},
		{"a live row a known bug", []rowResult{rr("live", passed), rr("live", known)}, true, false},
		{"a deprecated row todo", []rowResult{rr("deprecated", todo)}, true, false},
		{"a contract row todo", []rowResult{rr("contract", todo)}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.partial, ok(tc.results, false), "without -complete")
			assert.Equal(t, tc.complete, ok(tc.results, true), "with -complete")
		})
	}
}

func TestMarkdown(t *testing.T) {
	results := []rowResult{
		{Row: coverage.Row{ID: "CORE-01", Tier: "live", Suite: "core"}, Result: passed, Tests: []tally{{Name: "TestCore_A"}}},
		{Row: coverage.Row{ID: "CORE-02", Tier: "live", Suite: "core"}, Result: failed},
		{Row: coverage.Row{ID: "BB-01", Tier: "contract", Suite: "contract"}, Result: unit},
	}
	var b bytes.Buffer
	markdown(&b, results, false)
	out := b.String()
	assert.Contains(t, out, "### Coverage proof: FAILED")
	assert.Contains(t, out, "| contract | 1 | 0 | 0 | 0 | 0 | 0 | 1 | 0 |")
	assert.Contains(t, out, "| live | 2 | 1 | 1 | 0 | 0 | 0 | 0 |")
	assert.Contains(t, out, "| CORE-01 | live | core | passed | `TestCore_A` |")
}

// TestRun runs the command on the repository's coverage.tsv and up.sh with
// a summary that passes every core test.
func TestRun(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	tests, err := coverage.Tests(root)
	require.NoError(t, err)
	runs, err := coverage.SuiteRuns(root)
	require.NoError(t, err)
	core := map[string]string{}
	for _, ct := range tests {
		if ct.Live && runs["core"].MatchString(ct.Name) {
			core[ct.Name] = "pass"
		}
	}
	require.NotEmpty(t, core)

	dir := t.TempDir()
	write := func(name string, s summary) string {
		data, err := json.Marshal(s)
		require.NoError(t, err)
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, data, 0o600))
		return path
	}
	good := write("good.json", suiteRun("core", core))
	stepSummary := filepath.Join(dir, "step-summary.md")
	t.Setenv("GITHUB_STEP_SUMMARY", stepSummary)
	out := filepath.Join(dir, "proof.json")

	require.NoError(t, run(root, []string{good}, false, out))
	var results []rowResult
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &results))
	n := 0
	for _, r := range results {
		if r.Result == passed {
			n++
		}
	}
	assert.Positive(t, n)
	md, err := os.ReadFile(stepSummary)
	require.NoError(t, err)
	assert.Contains(t, string(md), "every row with a live test passed")

	// Rows are still todo, so the full proof fails.
	assert.Error(t, run(root, []string{good}, true, ""))

	for name := range core {
		core[name] = "fail"
		break
	}
	assert.Error(t, run(root, []string{write("bad.json", suiteRun("core", core))}, false, ""))
	assert.ErrorContains(t, run(root, []string{write("unknown.json", suiteRun("nope", nil))}, false, ""), `suite "nope"`)
	assert.Error(t, run(root, nil, false, ""))
}

func TestGitHubIssueOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repos/pnz1990/kardinal-promoter/issues/1":
			_, _ = w.Write([]byte(`{"state":"open"}`))
		case "/repos/pnz1990/kardinal-promoter/issues/2":
			_, _ = w.Write([]byte(`{"state":"closed"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	check := githubIssueOpen(srv.Client(), srv.URL, "tok")
	open, err := check(1)
	require.NoError(t, err)
	assert.True(t, open)
	open, err = check(2)
	require.NoError(t, err)
	assert.False(t, open)
	_, err = check(3)
	assert.ErrorContains(t, err, "HTTP 404")
}
