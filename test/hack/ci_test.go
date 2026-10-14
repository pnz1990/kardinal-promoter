// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// TestCITestShards (#1836): ci.yml runs go test -race in SHARDS shards, the
// required check "build" waits for the build checks and every shard and
// fails unless all passed, every Go job restores a Go build cache that the
// go-cache job saves on main only, and main runs are never cancelled.
func TestCITestShards(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	require.NoError(t, err)
	type step struct {
		Name string         `json:"name"`
		Uses string         `json:"uses"`
		Run  string         `json:"run"`
		If   string         `json:"if"`
		With map[string]any `json:"with"`
	}
	var wf struct {
		Concurrency struct {
			Group  string `json:"group"`
			Cancel string `json:"cancel-in-progress"`
		} `json:"concurrency"`
		Jobs map[string]struct {
			Name     string            `json:"name"`
			Needs    []string          `json:"needs"`
			If       string            `json:"if"`
			Env      map[string]string `json:"env"`
			Strategy struct {
				Matrix map[string][]any `json:"matrix"`
			} `json:"strategy"`
			Steps []step `json:"steps"`
		} `json:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &wf))

	test, ok := wf.Jobs["test"]
	require.True(t, ok, "ci.yml has a test job")
	n, err := strconv.Atoi(test.Env["SHARDS"])
	require.NoError(t, err, "test job env SHARDS")
	var shards []string
	for _, s := range test.Strategy.Matrix["shard"] {
		shards = append(shards, fmt.Sprint(s))
	}
	var want []string
	for i := 1; i <= n; i++ {
		want = append(want, strconv.Itoa(i))
	}
	assert.Equal(t, want, shards, "the matrix runs shards 1..SHARDS")
	assert.Contains(t, test.Name, fmt.Sprintf("/%d)", n), "the shard job's name says how many shards")
	var runsShard bool
	for _, s := range test.Steps {
		runsShard = runsShard || (strings.Contains(s.Run, "hack/citool shard \"$SHARD/$SHARDS\" hack/ci/test-durations.txt") &&
			strings.Contains(s.Run, "go test -race"))
	}
	assert.True(t, runsShard, "the shard job tests the packages hack/citool shard picks, with -race")

	build := wf.Jobs["build"]
	assert.Equal(t, "build", build.Name, "branch protection requires the check named build")
	assert.ElementsMatch(t, []string{"checks", "test"}, build.Needs)
	assert.Equal(t, "${{ !cancelled() }}", build.If, "build runs, and fails, when a shard fails")
	require.Len(t, build.Steps, 1)
	assert.Contains(t, build.Steps[0].Run, `[ "$CHECKS" != success ] || [ "$TESTS" != success ]`)

	for id, j := range wf.Jobs {
		for _, s := range j.Steps {
			if strings.HasPrefix(s.Uses, "actions/setup-go@") {
				assert.Equal(t, false, s.With["cache"], "%s: setup-go's own cache is off; the job restores go-race or go-build", id)
			}
			if strings.HasPrefix(s.Uses, "actions/cache/save@") {
				assert.Equal(t, "go-cache", id, "only the go-cache job saves a Go cache")
			}
		}
	}
	assert.Equal(t, "github.event_name == 'push' && github.ref == 'refs/heads/main'", wf.Jobs["go-cache"].If)

	assert.Equal(t, "ci-${{ github.ref == 'refs/heads/main' && github.run_id || github.ref }}", wf.Concurrency.Group,
		"each main run is its own group, so no main run is cancelled or replaced while queued")
	assert.Equal(t, "${{ github.ref != 'refs/heads/main' }}", wf.Concurrency.Cancel)
}

// fakeGH records each call in FAKE_LOG and answers from $FAKE_GH/routes:
// "pattern<TAB>file[,file...]" lines, the first pattern found in the
// arguments wins, the Nth matching call gets the Nth file (the last one
// after that), FAIL exits 1. --jq is applied with jq. A --body-file is
// copied to $FAKE_GH/body.md. Calls with no route (writes) print nothing.
const fakeGH = `#!/usr/bin/env bash
printf 'gh %s\n' "$*" >> "$FAKE_LOG"
args="$*" expr="" prev=""
for a in "$@"; do
  [ "$prev" = --jq ] && expr=$a
  [ "$prev" = --body-file ] && cp "$a" "$FAKE_GH/body.md"
  prev=$a
done
while IFS=$'\t' read -r pattern files; do
  case "$args" in *"$pattern"*) ;; *) continue ;; esac
  count="$FAKE_GH/count.$(printf '%s' "$pattern" | md5sum | cut -c1-12)"
  n=$(( $(cat "$count" 2>/dev/null || echo 0) + 1 ))
  echo "$n" > "$count"
  IFS=, read -ra list <<< "$files"
  i=$(( n <= ${#list[@]} ? n - 1 : ${#list[@]} - 1 ))
  f=${list[$i]}
  [ "$f" = FAIL ] && exit 1
  if [ -n "$expr" ]; then jq -r "$expr" "$FAKE_GH/$f"; else cat "$FAKE_GH/$f"; fi
  exit 0
done < "$FAKE_GH/routes"
`

var (
	citoolOnce sync.Once
	citoolPath string
	citoolErr  error
)

// buildCitool builds hack/citool once for the script tests.
func buildCitool(t *testing.T) string {
	t.Helper()
	citoolOnce.Do(func() {
		dir, err := os.MkdirTemp("", "citool")
		if err != nil {
			citoolErr = err
			return
		}
		citoolPath = filepath.Join(dir, "citool")
		cmd := exec.Command("go", "build", "-o", citoolPath, "./hack/citool")
		cmd.Dir = repoRoot(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			citoolErr = fmt.Errorf("go build ./hack/citool: %w: %s", err, out)
		}
	})
	require.NoError(t, citoolErr)
	return citoolPath
}

type ghRoute struct {
	pattern string
	files   []string
}

// runCIScript runs hack/ci/<script> against fakeGH with the fixture files and
// routes, and returns its output, the gh calls and the last --body-file.
func runCIScript(t *testing.T, script string, fixtures map[string]string, routes []ghRoute, env ...string) (string, string, string, error) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("the fake gh needs jq")
	}
	citool := buildCitool(t)
	dir, bin := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755))
	for name, body := range fixtures {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	var r strings.Builder
	for _, rt := range routes {
		fmt.Fprintf(&r, "%s\t%s\n", rt.pattern, strings.Join(rt.files, ","))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "routes"), []byte(r.String()), 0o600))
	logPath := filepath.Join(dir, "calls.log")

	cmd := exec.Command("bash", filepath.Join(repoRoot(t), "hack", "ci", script))
	cmd.Dir = repoRoot(t)
	cmd.Env = append([]string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"FAKE_GH=" + dir, "FAKE_LOG=" + logPath, "CITOOL=" + citool,
		"GH_REPO=o/r", "GH_TOKEN=",
	}, env...)
	out, err := cmd.CombinedOutput()
	calls, _ := os.ReadFile(logPath)
	body, _ := os.ReadFile(filepath.Join(dir, "body.md"))
	return string(out), string(calls), string(body), err
}

const (
	shaGreen = "aaaaaaaa11111111111111111111111111111111"
	shaRed   = "bbbbbbbb22222222222222222222222222222222"
)

func mainRuns(conclusion string, attempt int) string {
	return fmt.Sprintf(`{"workflow_runs": [
  {"id": 100, "head_sha": %q, "conclusion": %q, "run_attempt": %d, "created_at": "2026-10-14T10:00:00Z"},
  {"id": 95, "head_sha": "cccccccc", "conclusion": "cancelled", "run_attempt": 1, "created_at": "2026-10-14T09:30:00Z"},
  {"id": 90, "head_sha": %q, "conclusion": "success", "run_attempt": 1, "created_at": "2026-10-14T09:00:00Z"}]}`,
		shaRed, conclusion, attempt, shaGreen)
}

var redMainFixtures = map[string]string{
	"runs-red1.json":   mainRuns("failure", 1),
	"runs-red2.json":   mainRuns("failure", 2),
	"runs-green2.json": mainRuns("success", 2),
	"jobs.json": `{"jobs": [
  {"id": 5001, "name": "go test (2/4)", "html_url": "https://x/job/5001", "conclusion": "failure",
   "steps": [{"name": "go test -race (this shard's packages)", "conclusion": "failure"}]},
  {"id": 5002, "name": "build", "html_url": "https://x/job/5002", "conclusion": "failure", "steps": []},
  {"id": 5003, "name": "go test (1/4)", "html_url": "https://x/job/5003", "conclusion": "success", "steps": []}]}`,
	"5001.log": "2026-10-14T10:01:00.1Z --- FAIL: TestY (0.10s)\n" +
		"2026-10-14T10:01:00.2Z FAIL\tgithub.com/kardinal-promoter/kardinal-promoter/pkg/x\t1.0s\n",
	"compare.json": `{"commits": [
  {"sha": "dddddddd33", "commit": {"message": "feat(x): a thing (#12)\n\nbody", "author": {"name": "A"}}, "author": {"login": "alice"}},
  {"sha": "` + shaRed + `", "commit": {"message": "fix(y): another (#13)", "author": {"name": "Bob B"}}, "author": null}]}`,
	"no-issue.json":    `[]`,
	"issue-7.json":     `[{"number": 7}]`,
	"in-progress.json": `{"status": "in_progress"}`,
	"completed.json":   `{"status": "completed"}`,
	"same-body.json":   `{"body": "<!-- red-main run=100 attempt=2 -->\nold"}`,
	"other-body.json":  `{"body": "<!-- red-main run=88 attempt=2 -->\nold"}`,
}

func redMainRoutes(runs []string, issues, view string) []ghRoute {
	return []ghRoute{
		{"actions/workflows/ci.yml/runs?branch=main&event=push&status=completed", runs},
		{"actions/runs/100/attempts/2/jobs", []string{"jobs.json"}},
		{"actions/jobs/5001/logs", []string{"5001.log"}},
		{"actions/jobs/", []string{"FAIL"}},
		{"compare/" + shaGreen + "..." + shaRed, []string{"compare.json"}},
		{"actions/runs/100 --jq .status", []string{"in-progress.json", "completed.json"}},
		{"issue list", []string{issues}},
		{"issue view 7", []string{view}},
	}
}

// TestRedMain (#1836) runs hack/ci/red-main.sh against a fake gh.
func TestRedMain(t *testing.T) {
	tests := []struct {
		name      string
		runs      []string
		issues    string
		view      string
		env       []string
		calls     []string
		notCalls  []string
		bodyParts []string
	}{
		{
			name: "a failure on a re-run opens the issue", runs: []string{"runs-red2.json"}, issues: "no-issue.json",
			calls:    []string{"gh label create red-main", "gh issue create --repo o/r --title CI is red on main", "--label red-main"},
			notCalls: []string{"gh run rerun", "gh issue edit", "gh issue close"},
			bodyParts: []string{
				"<!-- red-main run=100 attempt=2 -->\n",
				"CI is red on main at [bbbbbbbb](https://github.com/o/r/commit/" + shaRed + "): https://github.com/o/r/actions/runs/100/attempts/2 (attempt 2).",
				"- [go test (2/4)](https://x/job/5001): go test -race (this shard's packages)\n  - `pkg/x` `TestY`\n",
				"### Merges since the last green main run ([aaaaaaaa](https://github.com/o/r/commit/" + shaGreen + "))",
				"- dddddddd feat(x): a thing (#12) (@alice)\n- bbbbbbbb fix(y): another (#13) (@Bob B)\n",
				"Fix forward within 30 minutes, or revert",
			},
		},
		{
			name: "a first failure is re-run once, and green closes the issue", runs: []string{"runs-red1.json", "runs-green2.json"},
			issues: "issue-7.json",
			calls: []string{"gh run rerun 100 --failed --repo o/r", "gh issue close 7 --repo o/r --comment main is green again",
				"It passed on attempt 2: what failed before is flaky"},
			notCalls: []string{"gh issue create", "gh issue edit"},
		},
		{
			name: "RERUN=0 reports a first failure at once", runs: []string{"runs-red1.json"}, issues: "no-issue.json",
			env: []string{"RERUN=0"}, calls: []string{"gh issue create"}, notCalls: []string{"gh run rerun"},
			// attempts/1/jobs has no route: no failing job is listed, the merges still are.
			bodyParts: []string{"(attempt 1)", "- dddddddd feat(x): a thing (#12) (@alice)"},
		},
		{
			name: "the same failure again edits the issue and does not comment", runs: []string{"runs-red2.json"},
			issues: "issue-7.json", view: "same-body.json",
			calls: []string{"gh issue edit 7 --repo o/r --body-file"}, notCalls: []string{"gh issue comment", "gh issue create"},
		},
		{
			name: "a new failure updates the issue and comments", runs: []string{"runs-red2.json"},
			issues: "issue-7.json", view: "other-body.json",
			calls: []string{"gh issue edit 7", "gh issue comment 7 --repo o/r --body Still red at [bbbbbbbb]"},
		},
		{
			name: "green with no issue writes nothing", runs: []string{"runs-green2.json"}, issues: "no-issue.json",
			notCalls: []string{"gh issue close", "gh issue create", "gh issue edit", "gh run rerun"},
		},
		{
			name: "DRY_RUN writes nothing", runs: []string{"runs-red1.json"}, issues: "no-issue.json", env: []string{"DRY_RUN=1"},
			notCalls: []string{"gh issue create", "gh run rerun", "gh label create"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := tt.view
			if view == "" {
				view = "same-body.json"
			}
			out, calls, body, err := runCIScript(t, "red-main.sh", redMainFixtures, redMainRoutes(tt.runs, tt.issues, view), tt.env...)
			require.NoError(t, err, out)
			for _, c := range tt.calls {
				assert.Contains(t, calls, c, out)
			}
			for _, c := range tt.notCalls {
				assert.NotContains(t, calls, c, out)
			}
			for _, p := range tt.bodyParts {
				assert.Contains(t, body, p)
			}
			if len(tt.bodyParts) > 0 {
				assert.NotContains(t, body, "[build]", "the shard collector is not listed")
			}
		})
	}
}

// TestFlakyReport (#1836) runs hack/ci/flaky-report.sh against a fake gh: a
// job that failed on attempt 1 and passed on attempt 2 is reported; a week
// with none closes the issue.
func TestFlakyReport(t *testing.T) {
	fixtures := map[string]string{
		"runs.json": `{"workflow_runs": [
  {"id": 200, "head_sha": "s1", "run_attempt": 2},
  {"id": 201, "head_sha": "s9", "run_attempt": 1}]}`,
		"attempt1.json": `{"jobs": [{"id": 11, "name": "go test (1/4)", "conclusion": "failure"},
  {"id": 13, "name": "golangci-lint", "conclusion": "success"}]}`,
		"attempt2.json": `{"jobs": [{"id": 12, "name": "go test (1/4)", "conclusion": "success"}]}`,
		"11.log":        "--- FAIL: TestFlaky (0.1s)\nFAIL\tgithub.com/kardinal-promoter/kardinal-promoter/pkg/a\t1.0s\n",
		"none.json":     `{"workflow_runs": [{"id": 201, "head_sha": "s9", "run_attempt": 1}]}`,
		"no-issue.json": `[]`,
		"issue-9.json":  `[{"number": 9}]`,
	}
	routes := func(runs, issues string) []ghRoute {
		return []ghRoute{
			{"actions/workflows/ci.yml/runs?status=completed", []string{runs}},
			{"actions/runs/200/attempts/1/jobs", []string{"attempt1.json"}},
			{"actions/runs/200/attempts/2/jobs", []string{"attempt2.json"}},
			{"actions/jobs/11/logs", []string{"11.log"}},
			{"actions/jobs/", []string{"FAIL"}},
			{"issue list", []string{issues}},
		}
	}

	out, calls, body, err := runCIScript(t, "flaky-report.sh", fixtures, routes("runs.json", "no-issue.json"))
	require.NoError(t, err, out)
	assert.Contains(t, calls, "gh issue create --repo o/r --title Flaky tests")
	assert.Contains(t, calls, "--label flaky-test")
	assert.NotContains(t, calls, "actions/runs/201/", "a run with one attempt and its own commit is not read")
	assert.Contains(t, body, "(1 runs re-run or sharing a commit, of 2).")
	assert.Contains(t, body, "| `TestFlaky` | `pkg/a` | go test (1/4) | 1 | [200#1](https://github.com/o/r/actions/runs/200/attempts/1) |")

	out, calls, _, err = runCIScript(t, "flaky-report.sh", fixtures, routes("runs.json", "issue-9.json"))
	require.NoError(t, err, out)
	assert.Contains(t, calls, "gh issue edit 9 --repo o/r --body-file")

	out, calls, _, err = runCIScript(t, "flaky-report.sh", fixtures, routes("none.json", "issue-9.json"))
	require.NoError(t, err, out)
	assert.Contains(t, calls, "gh issue close 9 --repo o/r --comment No flaky CI job")
	assert.NotContains(t, calls, "gh issue create")
}

// TestRefreshDurations (#1836): hack/ci/refresh-durations.sh measures the
// shard jobs' logs and proposes a refresh only when the split is off.
func TestRefreshDurations(t *testing.T) {
	durations, err := os.ReadFile(filepath.Join(repoRoot(t), "hack", "ci", "test-durations.txt"))
	require.NoError(t, err)
	var same strings.Builder
	for _, l := range strings.Split(string(durations), "\n") {
		if f := strings.Fields(l); len(f) == 2 && !strings.HasPrefix(f[0], "#") {
			fmt.Fprintf(&same, "ok  \t%s\t%ss\n", f[0], f[1])
		}
	}
	fixtures := map[string]string{
		"runs.json":  `{"workflow_runs": [{"id": 1}]}`,
		"jobs.json":  `{"jobs": [{"id": 21, "name": "go test (1/4)"}, {"id": 22, "name": "build checks"}]}`,
		"same.log":   same.String(),
		"slower.log": same.String() + "ok  \tgithub.com/kardinal-promoter/kardinal-promoter/pkg/brand-new\t900.0s\n",
	}
	routes := func(log string) []ghRoute {
		return []ghRoute{
			{"actions/workflows/ci.yml/runs?branch=main&event=push&status=success", []string{"runs.json"}},
			{"actions/runs/1/jobs", []string{"jobs.json"}},
			{"actions/jobs/21/logs", []string{log}},
			{"actions/jobs/", []string{"FAIL"}},
		}
	}
	out, calls, _, err := runCIScript(t, "refresh-durations.sh", fixtures, routes("same.log"), "DRY_RUN=1")
	require.NoError(t, err, out)
	assert.Contains(t, out, "slowest shard at 1.")
	assert.Contains(t, out, "0 packages not in the file, 0 in it that no longer run")
	assert.Contains(t, out, "balanced: nothing to refresh")
	assert.NotContains(t, calls, "actions/jobs/22/", "only the go test shard logs are read")

	out, _, _, err = runCIScript(t, "refresh-durations.sh", fixtures, routes("slower.log"), "DRY_RUN=1")
	require.NoError(t, err, out)
	assert.Contains(t, out, "1 packages not in the file")
	assert.Contains(t, out, "> github.com/kardinal-promoter/kardinal-promoter/pkg/brand-new 900.0")
	assert.NotContains(t, out, "balanced")
}
