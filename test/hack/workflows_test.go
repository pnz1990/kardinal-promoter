// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

type workflowStep struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Uses string         `json:"uses"`
	Run  string         `json:"run"`
	Env  map[string]any `json:"env"`
}

type workflowFile struct {
	Jobs map[string]struct {
		Steps []workflowStep `json:"steps"`
	} `json:"jobs"`
	Runs struct {
		Steps []workflowStep `json:"steps"`
	} `json:"runs"`
}

// workflowSteps returns the steps of every job in a workflow, or of a
// composite action.
func workflowSteps(t *testing.T, rel string) []workflowStep {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	require.NoError(t, err)
	var wf workflowFile
	require.NoError(t, yaml.Unmarshal(data, &wf), rel)
	steps := wf.Runs.Steps
	for _, j := range wf.Jobs {
		steps = append(steps, j.Steps...)
	}
	return steps
}

func workflowFiles(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	var out []string
	for _, pattern := range []string{".github/workflows/*.yml", ".github/workflows/*.yaml", ".github/actions/*/action.yml"} {
		m, err := filepath.Glob(filepath.Join(root, pattern))
		require.NoError(t, err)
		for _, p := range m {
			rel, err := filepath.Rel(root, p)
			require.NoError(t, err)
			out = append(out, filepath.ToSlash(rel))
		}
	}
	require.NotEmpty(t, out)
	return out
}

// TestWorkflowScriptsExpandNoExpressions checks that no run: script contains a
// ${{ }} expression. Actions substitutes those into the script text before the
// shell runs, so a value such as a tag name or an input becomes code; values
// must reach scripts through env: instead.
func TestWorkflowScriptsExpandNoExpressions(t *testing.T) {
	for _, f := range workflowFiles(t) {
		for _, s := range workflowSteps(t, f) {
			assert.NotContains(t, s.Run, "${{", "%s: step %q expands an expression inside its script", f, s.Name)
		}
	}
}

var (
	kindNodeImage   = regexp.MustCompile(`kindest/node:v1\.(\d+)\.\d+(@sha256:[0-9a-f]{64})?`)
	kindDownload    = regexp.MustCompile(`kind\.sigs\.k8s\.io/dl/(v[0-9.]+)/`)
	kindCreateNoCfg = regexp.MustCompile(`kind create cluster\b[^\n]*`)
)

// TestKindClustersUseOneSupportedNodeImage checks that every kind cluster the
// repository creates uses the node image in test/e2e/kind-config.yaml, that
// the image is digest-pinned and new enough for kro's Graph CRD (Kubernetes
// 1.29 rejects its CEL rule as over the cost budget), and that the workflows
// install one kind version.
func TestKindClustersUseOneSupportedNodeImage(t *testing.T) {
	root := repoRoot(t)
	var files []string
	for _, pattern := range []string{".github/workflows/*.yml", "hack/*.sh", "demo/scripts/*.sh", "scripts/*.sh", "test/e2e/*.yaml", "Makefile"} {
		m, err := filepath.Glob(filepath.Join(root, pattern))
		require.NoError(t, err)
		files = append(files, m...)
	}
	kindVersions := map[string][]string{}
	for _, p := range files {
		rel, err := filepath.Rel(root, p)
		require.NoError(t, err)
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		text := string(data)

		images := kindNodeImage.FindAllStringSubmatch(text, -1)
		if rel == "test/e2e/kind-config.yaml" {
			require.Len(t, images, 1, "kind-config.yaml must set exactly one node image")
			minor, err := strconv.Atoi(images[0][1])
			require.NoError(t, err)
			assert.GreaterOrEqual(t, minor, 30, "kro's graphs.kro.run CRD needs Kubernetes 1.30 or newer: %s", images[0][0])
			assert.NotEmpty(t, images[0][2], "pin the node image by digest, as the kind release notes list it: %s", images[0][0])
		} else {
			for _, img := range images {
				t.Errorf("%s: sets node image %s; use test/e2e/kind-config.yaml instead", rel, img[0])
			}
		}
		for _, m := range kindDownload.FindAllStringSubmatch(text, -1) {
			kindVersions[m[1]] = append(kindVersions[m[1]], rel)
		}
		for _, line := range kindCreateNoCfg.FindAllString(text, -1) {
			assert.Contains(t, line, "--config", "%s: %q must use a kind config so it gets the pinned node image", rel, line)
		}
	}
	assert.Len(t, kindVersions, 1, "the workflows install different kind versions: %v", kindVersions)
}

// workflowStepNamed returns the step of workflow rel whose name starts with
// prefix (case-insensitively).
func workflowStepNamed(t *testing.T, rel, prefix string) workflowStep {
	t.Helper()
	for _, s := range workflowSteps(t, rel) {
		if strings.HasPrefix(strings.ToLower(s.Name), strings.ToLower(prefix)) {
			return s
		}
	}
	t.Fatalf("%s has no step named %q...", rel, prefix)
	return workflowStep{}
}

// pdcaStep returns the pdca.yml step whose name starts with prefix
// (case-insensitively).
func pdcaStep(t *testing.T, prefix string) workflowStep {
	t.Helper()
	return workflowStepNamed(t, ".github/workflows/pdca.yml", prefix)
}

// runPDCAScript runs a pdca.yml run: script the way Actions does (bash -e -o
// pipefail) with fake tools first on PATH. sleep only advances $SECONDS, so
// the wait loops run to their deadlines instantly.
func runPDCAScript(t *testing.T, script string, fakes map[string]string, env ...string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "step.sh")
	require.NoError(t, os.WriteFile(path, []byte("sleep() { SECONDS=$((SECONDS + ${1%%.*})); }\n"+script), 0o600))
	bin := t.TempDir()
	for name, body := range fakes {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755))
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", path)
	cmd.Dir = repoRoot(t)
	cmd.Env = append([]string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"KUBECONFIG=" + os.DevNull,
		"GITHUB_TOKEN=",
	}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const fakeFailing = `#!/usr/bin/env bash
echo "error: $(basename "$0") cannot reach the cluster" >&2
exit 1
`

// fakeGateCLI answers policy simulate and explain the way a correct
// controller does for the quickstart gates; everything else fails.
const fakeGateCLI = `#!/usr/bin/env bash
case "$*" in
  *"policy simulate"*Saturday*"--soak-minutes 60"*) printf 'RESULT: BLOCKED\nBlocked by: no-weekend-deploys\n' ;;
  *"policy simulate"*Saturday*) printf 'RESULT: BLOCKED\nBlocked by: no-weekend-deploys\nBlocked by: require-uat-soak\n' ;;
  *"policy simulate"*"--soak-minutes 60"*) echo 'RESULT: PASS' ;;
  *"policy simulate"*) printf 'RESULT: BLOCKED\nBlocked by: require-uat-soak\n' ;;
  "explain "*) printf 'ENVIRONMENT  TYPE  NAME  STATE  EXPRESSION\nprod  gate  no-weekend-deploys  Pass  !schedule.isWeekend\n' ;;
  *) echo "error: unexpected call: $*" >&2; exit 1 ;;
esac
`

// fakeAlwaysPass is a CLI that says every gate passes: the weekend gate is
// broken, and the checks must notice.
const fakeAlwaysPass = `#!/usr/bin/env bash
echo 'RESULT: PASS'
`

func readResults(t *testing.T, path string) (pass, fail []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "the scenario step wrote no results file")
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		switch {
		case l == "":
		case strings.HasPrefix(l, "✅"):
			pass = append(pass, l)
		case strings.HasPrefix(l, "❌"):
			fail = append(fail, l)
		default:
			t.Errorf("result line is neither a pass nor a failure: %q", l)
		}
	}
	return pass, fail
}

// TestPDCAScenarioChecksCanFail runs the PDCA scenario step against fake
// tools. A check passes only when the tools report the expected behavior;
// when they fail or misbehave, every selected check records a failure.
func TestPDCAScenarioChecksCanFail(t *testing.T) {
	scenarios := pdcaStep(t, "Run PDCA scenarios")
	tests := []struct {
		name     string
		scenario string
		kardinal string
		wantPass int
		minFail  int
	}{
		{"every check fails when the cluster is unreachable", "all", fakeFailing, 0, 20},
		{"scenario 3 passes on correct gate answers", "3", fakeGateCLI, 4, 0},
		{"scenario 3 fails when the weekend gate never blocks", "3", fakeAlwaysPass, 1, 3},
		{"scenario 4 passes when explain shows the gate", "4", fakeGateCLI, 1, 1}, // S1 still needs a cluster
		{"scenario 2 selects only the pause check", "2", fakeFailing, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := filepath.Join(t.TempDir(), "results.txt")
			out, err := runPDCAScript(t, scenarios.Run,
				map[string]string{"kardinal": tt.kardinal, "kubectl": fakeFailing},
				"SCENARIO="+tt.scenario, "RESULTS_FILE="+results,
				"TEST_IMAGE=ghcr.io/pnz1990/kardinal-test-app:sha-abc1234")
			require.NoError(t, err, "the scenario step must run every check to the end:\n%s", out)
			pass, fail := readResults(t, results)
			assert.Len(t, pass, tt.wantPass, "passes:\n%s", strings.Join(pass, "\n"))
			assert.GreaterOrEqual(t, len(fail), tt.minFail, "failures:\n%s", strings.Join(fail, "\n"))
		})
	}
}

func TestPDCAScenarioSelection(t *testing.T) {
	scenarios := pdcaStep(t, "Run PDCA scenarios")
	want := map[string][]string{
		"1": {"S1:", "S8:"},
		"2": {"S2:"},
		"3": {"S3:", "S3b:", "S13:", "S14:"},
		"4": {"S1:", "S4:"},
		"5": {"S1:", "S5:"},
		"6": {"S6:", "S19:"},
	}
	for scenario, ids := range want {
		t.Run("scenario "+scenario, func(t *testing.T) {
			results := filepath.Join(t.TempDir(), "results.txt")
			_, err := runPDCAScript(t, scenarios.Run,
				map[string]string{"kardinal": fakeFailing, "kubectl": fakeFailing},
				"SCENARIO="+scenario, "RESULTS_FILE="+results,
				"TEST_IMAGE=ghcr.io/pnz1990/kardinal-test-app:sha-abc1234")
			require.NoError(t, err)
			pass, fail := readResults(t, results)
			var got []string
			for _, l := range append(pass, fail...) {
				got = append(got, strings.Fields(l)[1])
			}
			assert.Equal(t, ids, got)
		})
	}
}

// TestPDCAReportFailsUnlessEveryCheckPasses runs the PDCA report step. It
// writes the evidence to the job summary (Issue #1, where it used to post, is
// closed), never calls gh, and fails the job unless every check passed.
func TestPDCAReportFailsUnlessEveryCheckPasses(t *testing.T) {
	report := pdcaStep(t, "Write PDCA evidence to the job summary")
	// Any gh call is a bug: the report no longer posts anywhere.
	fakeGH := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_LOG"
exit 1
`
	tests := []struct {
		name       string
		results    string
		scenarios  string
		ui         string
		wantOK     bool
		wantStatus string
	}{
		{"all checks pass", "✅ S1: ok\n✅ S3: ok\n", "success", "success", true, "ALL PASS"},
		{"UI skipped for one scenario", "✅ S3: ok\n", "success", "skipped", true, "ALL PASS"},
		{"a check failed", "✅ S1: ok\n❌ S3: blocked\n", "success", "success", false, "1 FAILED"},
		{"no checks ran", "", "success", "skipped", false, "NO CHECKS RAN"},
		{"scenario step stopped early", "✅ S1: ok\n", "failure", "skipped", false, "INCOMPLETE"},
		{"scenario step cancelled", "✅ S1: ok\n", "cancelled", "skipped", false, "INCOMPLETE"},
		{"UI step failed", "✅ S1: ok\n", "success", "failure", false, "INCOMPLETE"},
		{"setup failed before the scenario step", "", "", "", false, "NO CHECKS RAN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			results := filepath.Join(dir, "results.txt")
			if tt.results != "" {
				require.NoError(t, os.WriteFile(results, []byte(tt.results), 0o600))
			}
			log := filepath.Join(dir, "gh.log")
			summary := filepath.Join(dir, "summary.md")
			out, err := runPDCAScript(t, report.Run, map[string]string{"gh": fakeGH},
				"RESULTS_FILE="+results, "SCENARIO=all", "FAKE_LOG="+log,
				"GITHUB_STEP_SUMMARY="+summary,
				"SCENARIOS_OUTCOME="+tt.scenarios, "UI_OUTCOME="+tt.ui,
				"GH_REPO=pnz1990/kardinal-promoter", "RUN_ID=1", "TRIGGER=schedule",
				"TEST_IMAGE=ghcr.io/pnz1990/kardinal-test-app:sha-abc1234")
			if tt.wantOK {
				assert.NoError(t, err, out)
			} else {
				assert.Error(t, err, "the job must fail: %s", out)
			}
			assert.Contains(t, out, "Status: "+tt.wantStatus)
			written, readErr := os.ReadFile(summary)
			require.NoError(t, readErr, "the report must reach the job summary even when the job fails")
			assert.Contains(t, string(written), "Status: "+tt.wantStatus)
			assert.Contains(t, string(written), "https://github.com/pnz1990/kardinal-promoter/actions/runs/1")
			for _, l := range strings.Split(strings.TrimSpace(tt.results), "\n") {
				if l != "" {
					assert.Contains(t, string(written), "- "+l)
				}
			}
			_, statErr := os.Stat(log)
			assert.True(t, os.IsNotExist(statErr), "the report must not call gh (no Issue comment)")
		})
	}

	var wf struct {
		Jobs map[string]struct {
			Permissions map[string]string `json:"permissions"`
		} `json:"jobs"`
	}
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "pdca.yml"))
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &wf))
	assert.Equal(t, map[string]string{"contents": "read"}, wf.Jobs["pdca"].Permissions,
		"PDCA posts nothing, so it needs no issues: write")
}

// TestPDCAPreflightFailsWhenTheDemoTokenCannotRead runs the first pdca.yml
// step. An expired or missing KARDINAL_DEMO_PAT fails it with one message
// that names the fix, and the token never reaches the log, even when gh
// prints it.
func TestPDCAPreflightFailsWhenTheDemoTokenCannotRead(t *testing.T) {
	steps := workflowSteps(t, ".github/workflows/pdca.yml")
	require.NotEmpty(t, steps)
	preflight := steps[0]
	require.True(t, strings.HasPrefix(preflight.Name, "Preflight"), "the preflight must be the first step, before any setup: %q", preflight.Name)
	assert.Equal(t, "${{ secrets.KARDINAL_DEMO_PAT }}", preflight.Env["DEMO_TOKEN"])

	// The fake gh logs its arguments and the token it was given, and prints
	// the token the way a careless tool might.
	fakeGH := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_LOG"
printf '%s' "$GH_TOKEN" > "$FAKE_TOKEN_SEEN"
echo "token $GH_TOKEN: HTTP $FAKE_STATUS" >&2
[ "$FAKE_STATUS" = 200 ]
`
	const token = "github_pat_FAKE0123456789secret"
	const msg = "KARDINAL_DEMO_PAT cannot read pnz1990/kardinal-demo: rotate it"
	tests := []struct {
		name      string
		token     string
		status    string
		wantOK    bool
		wantGHRun bool
	}{
		{"the token can read the repo", token, "200", true, true},
		{"the token is expired or revoked", token, "401", false, true},
		{"the secret is not set", "", "200", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "gh.log")
			seen := filepath.Join(dir, "token")
			out, err := runPDCAScript(t, preflight.Run, map[string]string{"gh": fakeGH},
				"DEMO_TOKEN="+tt.token, "GITHUB_TOKEN=job-token-must-not-be-used",
				"FAKE_LOG="+log, "FAKE_TOKEN_SEEN="+seen, "FAKE_STATUS="+tt.status)
			if tt.wantOK {
				require.NoError(t, err, out)
				assert.NotContains(t, out, msg)
			} else {
				require.Error(t, err, out)
				assert.Contains(t, out, "::error::"+msg)
			}
			assert.NotContains(t, out, token, "the step must never print the token")
			calls, readErr := os.ReadFile(log)
			if !tt.wantGHRun {
				assert.True(t, os.IsNotExist(readErr), "an empty token must fail before gh can fall back to GITHUB_TOKEN")
				return
			}
			require.NoError(t, readErr)
			assert.Contains(t, string(calls), "api repos/pnz1990/kardinal-demo")
			used, readErr := os.ReadFile(seen)
			require.NoError(t, readErr)
			assert.Equal(t, tt.token, string(used), "gh must use KARDINAL_DEMO_PAT, not the job's GITHUB_TOKEN")
		})
	}
}

// TestAgentInstructionsGuardIsSafeForForks checks the shape that makes a
// pull_request_target workflow safe: it runs the base branch's copy, never
// checks out or runs PR code, has read-only permissions, and runs on every PR.
func TestAgentInstructionsGuardIsSafeForForks(t *testing.T) {
	const rel = ".github/workflows/agent-instructions-guard.yml"
	data, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	require.NoError(t, err)
	var wf map[string]any
	require.NoError(t, yaml.Unmarshal(data, &wf))

	// YAML 1.1 reads the key "on" as true.
	on, ok := wf["on"]
	if !ok {
		on = wf["true"]
	}
	triggers, ok := on.(map[string]any)
	require.True(t, ok, "on: must be a map of triggers: %v", on)
	require.Len(t, triggers, 1, "pull_request_target must be the only trigger: %v", triggers)
	prt, ok := triggers["pull_request_target"].(map[string]any)
	require.True(t, ok, "the guard must run on pull_request_target so a PR cannot edit it: %v", triggers)
	assert.Equal(t, []any{"opened", "synchronize", "reopened"}, prt["types"])
	for _, filter := range []string{"paths", "paths-ignore", "branches", "branches-ignore"} {
		assert.NotContains(t, prt, filter, "a required check must run on every PR")
	}

	assert.Equal(t, map[string]any{"pull-requests": "read", "contents": "read"}, wf["permissions"])
	jobs, ok := wf["jobs"].(map[string]any)
	require.True(t, ok)
	require.Len(t, jobs, 1)
	job, ok := jobs["guard"].(map[string]any)
	require.True(t, ok, "jobs: %v", jobs)
	assert.Equal(t, "agent instructions guard", job["name"], "branch protection requires this check by name")
	assert.NotContains(t, job, "permissions", "the job must not widen the workflow's read-only permissions")

	assert.NotContains(t, string(data), "actions/checkout")
	for _, s := range workflowSteps(t, rel) {
		assert.Empty(t, s.Uses, "step %q runs an action; the guard must only call the API", s.Name)
		assert.NotContains(t, s.Run, "git ", "step %q must not fetch PR code", s.Name)
	}
}

// TestAgentInstructionsGuard runs the guard's script with a fake gh that
// applies the step's real --jq filter to a pulls/N/files response. A PR from
// anyone but the owner or Dependabot fails when it touches agent
// instructions, workflows or actions, including by renaming one away.
func TestAgentInstructionsGuard(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed")
	}
	guard := workflowStepNamed(t, ".github/workflows/agent-instructions-guard.yml", "Check who changes")
	// The fake gh logs its arguments and runs the --jq filter over FAKE_FILES
	// with jq, the way gh would over the API response.
	fakeGH := `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_LOG"
[ -n "$FAKE_FAIL" ] && { echo "gh: HTTP 502" >&2; exit 1; }
while [ $# -gt 0 ]; do
  if [ "$1" = --jq ]; then exec jq -r "$2" "$FAKE_FILES"; fi
  shift
done
echo "gh: no --jq" >&2
exit 1
`
	files := func(entries ...string) string {
		var parts []string
		for _, e := range entries {
			if name, prev, renamed := strings.Cut(e, "<-"); renamed {
				parts = append(parts, `{"filename":"`+name+`","status":"renamed","previous_filename":"`+prev+`"}`)
			} else {
				parts = append(parts, `{"filename":"`+e+`","status":"modified"}`)
			}
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	const outsider = "someone-else"
	tests := []struct {
		name     string
		author   string
		json     string
		changed  string
		ghFails  bool
		wantOK   bool
		wantFile string
		wantGH   bool
	}{
		{"outsider changes AGENTS.md", outsider, files("docs/a.md", "AGENTS.md"), "2", false, false, "AGENTS.md", true},
		{"outsider changes CLAUDE.md", outsider, files("CLAUDE.md"), "1", false, false, "CLAUDE.md", true},
		{"outsider changes a nested CLAUDE.md", outsider, files("pkg/graph/CLAUDE.md"), "1", false, false, "pkg/graph/CLAUDE.md", true},
		{"outsider changes .claude/", outsider, files(".claude/settings.json"), "1", false, false, ".claude/settings.json", true},
		{"outsider changes a workflow", outsider, files(".github/workflows/ci.yml"), "1", false, false, ".github/workflows/ci.yml", true},
		{"outsider changes an action", outsider, files(".github/actions/create-bundle/action.yml"), "1", false, false, ".github/actions/create-bundle/action.yml", true},
		{"outsider renames AGENTS.md away", outsider, files("docs/old-agents.md<-AGENTS.md"), "1", false, false, "AGENTS.md", true},
		{"a line break in a file name cannot inject a workflow command", outsider, files(`.claude/x\n::warning::injected`), "1", false, false, ".claude/x?::warning::injected", true},
		{"outsider changes only docs and code", outsider, files("docs/quickstart.md", "pkg/graph/builder.go", "AGENTS.md.txt"), "3", false, true, "", true},
		{"owner changes AGENTS.md", "pnz1990", files("AGENTS.md", ".github/workflows/ci.yml"), "2", false, true, "", false},
		{"Dependabot bumps a workflow action", "dependabot[bot]", files(".github/workflows/ci.yml"), "1", false, true, "", false},
		{"too many files to list", outsider, files("docs/a.md"), "3001", false, false, "", false},
		{"the files API fails", outsider, files("docs/a.md"), "1", true, false, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "gh.log")
			resp := filepath.Join(dir, "files.json")
			require.NoError(t, os.WriteFile(resp, []byte(tt.json), 0o600))
			fail := ""
			if tt.ghFails {
				fail = "1"
			}
			out, err := runPDCAScript(t, guard.Run, map[string]string{"gh": fakeGH},
				"REPO=pnz1990/kardinal-promoter", "PR_NUMBER=7", "AUTHOR="+tt.author,
				"CHANGED_FILES="+tt.changed, "GH_TOKEN=fake", "FAKE_LOG="+log,
				"FAKE_FILES="+resp, "FAKE_FAIL="+fail)
			if tt.wantOK {
				require.NoError(t, err, out)
			} else {
				require.Error(t, err, "the guard must fail: %s", out)
			}
			if tt.wantFile != "" {
				assert.Contains(t, out, "::error::")
				assert.Contains(t, out, "  "+tt.wantFile+"\n", "the log must name the guarded file")
				assert.Contains(t, out, "::stop-commands::", "PR file names must be printed with workflow commands stopped")
			}
			for _, l := range strings.Split(out, "\n") {
				assert.False(t, strings.HasPrefix(l, "::warning::"), "a PR file name became a workflow command: %q", l)
			}
			calls, readErr := os.ReadFile(log)
			if !tt.wantGH {
				assert.True(t, os.IsNotExist(readErr), "gh must not be called: %s", calls)
				return
			}
			require.NoError(t, readErr)
			assert.Equal(t, "api --paginate repos/pnz1990/kardinal-promoter/pulls/7/files?per_page=100", strings.SplitN(string(calls), " --jq", 2)[0],
				"the guard reads only the PR's file list")
		})
	}
}

// TestDependabotCoversEveryManifest checks that Dependabot updates every
// dependency manifest in the repository, and that it bumps the Kubernetes
// modules together: they only build at matching versions, so one PR per
// module fails, and a patch bump of one alone leaves go.mod skewed.
func TestDependabotCoversEveryManifest(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "dependabot.yml"))
	require.NoError(t, err)
	var cfg struct {
		Updates []struct {
			Ecosystem string `json:"package-ecosystem"`
			Directory string `json:"directory"`
			Groups    map[string]struct {
				Patterns []string `json:"patterns"`
			} `json:"groups"`
		} `json:"updates"`
	}
	require.NoError(t, yaml.Unmarshal(data, &cfg))
	got := map[string]bool{}
	var k8sPatterns []string
	for _, u := range cfg.Updates {
		got[u.Ecosystem+" "+u.Directory] = true
		if u.Ecosystem == "gomod" {
			for _, g := range u.Groups {
				k8sPatterns = append(k8sPatterns, g.Patterns...)
			}
		}
	}
	manifests := map[string]string{
		"go.mod":            "gomod /",
		"web/package.json":  "npm /web",
		"Dockerfile":        "docker /",
		".github/workflows": "github-actions /",
	}
	for file, entry := range manifests {
		_, err := os.Stat(filepath.Join(repoRoot(t), file))
		require.NoError(t, err, file)
		assert.True(t, got[entry], "dependabot.yml has no %q entry for %s", entry, file)
	}
	assert.Contains(t, k8sPatterns, "k8s.io/*", "k8s.io modules must be updated in one group")
	assert.Contains(t, k8sPatterns, "sigs.k8s.io/*", "sigs.k8s.io modules must be updated with k8s.io")
}
