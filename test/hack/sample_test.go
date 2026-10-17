// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleJob is one line of hack/e2e/sample.sh: id suite k8s shard count.
type sampleJob struct{ id, suite, k8s, shard, count string }

func runSample(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{"hack/e2e/sample.sh"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "SLICES=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func sampleJobs(t *testing.T, root, day string) []sampleJob {
	t.Helper()
	out, err := runSample(t, root, day)
	require.NoError(t, err, out)
	var jobs []sampleJob
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		require.Len(t, f, 5, "sample.sh %s: %q", day, line)
		jobs = append(jobs, sampleJob{f[0], f[1], f[2], f[3], f[4]})
	}
	return jobs
}

// matrixMinors returns, per suite, the minors hack/e2e/matrix.txt runs it on.
func matrixMinors(t *testing.T, root string) map[string]map[string]bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "hack/e2e/matrix.txt"))
	require.NoError(t, err)
	on := map[string]map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if on[f[0]] == nil {
			on[f[0]] = map[string]bool{}
		}
		on[f[0]][f[1]] = true
	}
	return on
}

// TestNightlySample (#1837): hack/e2e/sample.sh, the jobs every scheduled
// e2e-live.yml run adds, is a function of the date alone; each night runs
// one suite on a minor matrix.txt does not run it on and a ninth of the
// core suite with COUNT=3; every (suite, minor) pair runs within the pair
// rotation's length and every core slice within nine nights; and
// all.sh -matrix with SAMPLE lists the same jobs, with their count.
func TestNightlySample(t *testing.T) {
	root := repoRoot(t)
	out, err := runSample(t, root, "-cycle")
	require.NoError(t, err, out)
	f := strings.Fields(out)
	require.Len(t, f, 2, out)
	pairs, err := strconv.Atoi(f[0])
	require.NoError(t, err)
	slices, err := strconv.Atoi(f[1])
	require.NoError(t, err)
	assert.Equal(t, 9, slices)
	assert.GreaterOrEqual(t, pairs, 7, "every single-minor suite gets at least one other minor")

	on := matrixMinors(t, root)
	start := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	seenPairs, seenSlices := map[string]bool{}, map[string]bool{}
	for d := 0; d < pairs; d++ {
		day := start.AddDate(0, 0, d).Format("2006-01-02")
		jobs := sampleJobs(t, root, day)
		assert.Equal(t, jobs, sampleJobs(t, root, day), "sample.sh %s is deterministic", day)
		require.Len(t, jobs, 2, day)
		minor, flake := jobs[0], jobs[1]
		assert.False(t, on[minor.suite][minor.k8s], "%s: matrix.txt already runs %s on %s", day, minor.suite, minor.k8s)
		assert.NotContains(t, []string{"core", "scale", "github", "multi-cluster"}, minor.suite, day)
		assert.False(t, strings.HasPrefix(minor.suite, "upgrade"), day)
		assert.Equal(t, "-", minor.shard, day)
		assert.Equal(t, "-", minor.count, day)
		_, err := os.Stat(filepath.Join(root, "hack/e2e/up.sh"))
		require.NoError(t, err)
		seenPairs[minor.suite+" "+minor.k8s] = true

		assert.Equal(t, "core", flake.suite, day)
		assert.Equal(t, "3", flake.count, day)
		assert.Regexp(t, `^[1-9]/9$`, flake.shard, day)
		assert.True(t, on["core"][flake.k8s], "%s: the core slice runs on a minor core runs on", day)
		if d < slices {
			seenSlices[flake.shard] = true
		}
		assert.NotEqual(t, minor.id, flake.id)
	}
	assert.Len(t, seenPairs, pairs, "every (suite, minor) pair runs within %d nights", pairs)
	assert.Len(t, seenSlices, slices, "every core slice runs within %d nights", slices)

	// all.sh -matrix lists the sample jobs after matrix.txt's, with count.
	jobs := sampleJobs(t, root, "2026-10-14")
	cmd := exec.Command("bash", "hack/e2e/all.sh", "-matrix")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "SAMPLE=2026-10-14", "SAMPLE_ONLY=", "SUITES=")
	raw, err := cmd.Output()
	require.NoError(t, err)
	var m struct {
		Include []map[string]string `json:"include"`
	}
	require.NoError(t, json.Unmarshal(raw, &m))
	ids := map[string]map[string]string{}
	for _, e := range m.Include {
		assert.NotContains(t, ids, e["id"], "duplicate job id")
		ids[e["id"]] = e
	}
	for _, j := range jobs {
		e, ok := ids[j.id]
		if !assert.True(t, ok, "all.sh -matrix with SAMPLE lists %s", j.id) {
			continue
		}
		count := j.count
		if count == "-" {
			count = ""
		}
		assert.Equal(t, count, e["count"], j.id)
		assert.Equal(t, j.k8s, e["k8s"], j.id)
	}

	for _, bad := range []string{"2026-13-40", "tomorrow", "2026-10-14 2026-10-15"} {
		out, err := runSample(t, root, strings.Fields(bad)...)
		assert.Error(t, err, "sample.sh %s: %s", bad, out)
	}
}

// fakeLiveReportGH is a gh on PATH that serves "issue list" from issues.json in its
// directory and logs every other call, one line each, to calls.log.
const fakeLiveReportGH = `#!/usr/bin/env bash
d=$(dirname "$0")
case "$1 $2" in
"issue list") cat "$d/issues.json" ;;
"issue create") echo "create $4" >>"$d/calls.log"; cp body.md "$d/created-$(echo "$4" | tr -c 'a-z0-9' _).md" ;;
*) echo "$1 $2 $3" >>"$d/calls.log" ;;
esac
`

// TestLiveReport (#1837): hack/e2e/live-report.sh, the report job of a
// scheduled e2e-live.yml run, gives each failed sample job an issue of its
// own with the commands that run it again, keeps that issue open on a run
// where the job did not run, closes it only when the same job passes, and
// keeps the shared issue for the matrix.txt jobs: a sample failure alone
// neither opens nor (through the "e2e live" job it fails) keeps it open.
func TestLiveReport(t *testing.T) {
	root := repoRoot(t)
	open := `[{"number":7,"title":"e2e live sample failed: sample-gitlab-136"},` +
		`{"number":8,"title":"e2e live failed on main (nightly (COUNT=1))"},` +
		`{"number":9,"title":"e2e live sample failed: sample-core-137-5of9x3"}]`
	for _, tc := range []struct {
		name, jobs, e2eLive string
		want                []string
		created             string
	}{
		{
			name: "sample job fails",
			// sample-ui-136 is 2026-11-02's minor job (hack/e2e/sample.sh
			// 2026-11-10); a suite added to matrix.txt shifts the rotation.
			jobs: "controller image\tsuccess\nui on Kubernetes 1.37\tsuccess\n" +
				"sample-ui-136: ui on Kubernetes 1.36\tfailure\n" +
				"sample-core-137-5of9x3: core 5/9 on Kubernetes 1.37 (COUNT=3)\tsuccess\ne2e live\tfailure\n",
			e2eLive: "failure",
			// ui-136 opens its own issue; core 5/9 passed, so its issue
			// closes; gitlab-136 did not run, so its issue stays; the shared
			// issue closes: no matrix.txt job failed.
			want:    []string{"create e2e live sample failed: sample-ui-136", "issue close 9", "issue close 8"},
			created: "e2e live sample failed: sample-ui-136",
		},
		{
			name: "sample job fails again and a matrix job fails",
			jobs: "core 1/3 on Kubernetes 1.35\tfailure\n" +
				"sample-gitlab-136: gitlab on Kubernetes 1.36\tfailure\ne2e live\tfailure\n",
			e2eLive: "failure",
			want:    []string{"issue comment 7", "issue comment 8"},
		},
		{
			name:    "only the proof fails",
			jobs:    "core 1/3 on Kubernetes 1.35\tsuccess\ne2e live\tfailure\n",
			e2eLive: "failure",
			want:    []string{"issue comment 8"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			require.NoError(t, os.Mkdir(bin, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeLiveReportGH), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "issues.json"), []byte(open), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "jobs.tsv"), []byte(tc.jobs), 0o600))
			cmd := exec.Command("bash", filepath.Join(root, "hack/e2e/live-report.sh"), "jobs.tsv")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "KIND=nightly (COUNT=1)",
				"RUN_URL=https://example.com/run/1", "SHA=0123456789abcdef", "SAMPLE=2026-11-02", "E2E_LIVE="+tc.e2eLive)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))
			calls, err := os.ReadFile(filepath.Join(bin, "calls.log"))
			require.NoError(t, err)
			assert.Equal(t, tc.want, strings.Split(strings.TrimSpace(string(calls)), "\n"))
			if tc.created != "" {
				body, err := os.ReadFile(filepath.Join(bin, "created-"+strings.Map(func(r rune) rune {
					if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
						return r
					}
					return '_'
				}, tc.created)+"_.md"))
				require.NoError(t, err)
				assert.Contains(t, string(body), "SAMPLE=2026-11-02 SAMPLE_ONLY=1 SUITES=ui make e2e-all")
				assert.Contains(t, string(body), "gh workflow run e2e-live.yml --ref main -f suites=ui -f sample=2026-11-02")
				assert.Contains(t, string(body), "https://example.com/run/1")
			}
		})
	}
}
