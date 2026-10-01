// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"fmt"
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

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/coverage"
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
// 1.29 rejects its CEL rule as over the cost budget), and that no workflow
// hard-codes a kind version (hack/tool-versions.env holds the one version).
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
			assert.GreaterOrEqual(t, minor, 30, "hack/install-kro.sh needs Kubernetes 1.30 or newer (kro's graphrevisions CRD uses selectableFields): %s", images[0][0])
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
	assert.Empty(t, kindVersions, "kind downloads must take KIND_VERSION from hack/tool-versions.env: %v", kindVersions)
}

// toolVersions parses hack/tool-versions.env (KEY=value lines, # comments).
func toolVersions(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "hack/tool-versions.env"))
	require.NoError(t, err)
	out := map[string]string{}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		require.True(t, ok, "hack/tool-versions.env: not KEY=value: %q", l)
		out[k] = v
	}
	return out
}

var (
	semver    = regexp.MustCompile(`^v\d+\.(\d+)\.\d+$`)
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// toolDownload matches a download of kind, kubectl or the argocd CLI.
	toolDownload = regexp.MustCompile(`kind\.sigs\.k8s\.io/dl/|dl\.k8s\.io/release/|argo-cd/releases/download/`)
)

// TestToolDownloadsArePinnedAndVerified covers #1294: every kind, kubectl
// and argocd download in a workflow takes its version from
// hack/tool-versions.env and is checked against the sha256 there, kubectl
// matches the kind node's minor, and nothing installs a floating version.
func TestToolDownloadsArePinnedAndVerified(t *testing.T) {
	tv := toolVersions(t)
	for _, tool := range []string{"KIND", "KUBECTL"} {
		assert.Regexp(t, semver, tv[tool+"_VERSION"], "%s_VERSION", tool)
		assert.Regexp(t, sha256Hex, tv[tool+"_SHA256"], "%s_SHA256", tool)
	}

	cfg, err := os.ReadFile(filepath.Join(repoRoot(t), "test/e2e/kind-config.yaml"))
	require.NoError(t, err)
	node := kindNodeImage.FindStringSubmatch(string(cfg))
	require.NotNil(t, node)
	if m := semver.FindStringSubmatch(tv["KUBECTL_VERSION"]); assert.NotNil(t, m) {
		assert.Equal(t, node[1], m[1], "KUBECTL_VERSION %s must have the kind node's minor (%s)", tv["KUBECTL_VERSION"], node[0])
	}

	downloads := 0
	for _, f := range workflowFiles(t) {
		for _, s := range workflowSteps(t, f) {
			assert.NotContains(t, s.Run, "stable.txt", "%s: step %q installs a floating kubectl", f, s.Name)
			if !toolDownload.MatchString(s.Run) {
				continue
			}
			downloads++
			assert.Contains(t, s.Run, "source hack/tool-versions.env", "%s: step %q", f, s.Name)
			for _, l := range strings.Split(s.Run, "\n") {
				if toolDownload.MatchString(l) {
					assert.Contains(t, l, "_VERSION}", "%s: step %q hard-codes a tool version: %s", f, s.Name, strings.TrimSpace(l))
				}
			}
			assert.Equal(t, strings.Count(s.Run, "curl "), strings.Count(s.Run, "sha256sum -c"),
				"%s: step %q must check every download with sha256sum -c", f, s.Name)
		}
	}
	assert.GreaterOrEqual(t, downloads, 2, "expected the kind and kubectl installs in e2e-live")

	for _, rel := range append(workflowFiles(t), "Makefile") {
		data, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
		require.NoError(t, err)
		assert.NotContains(t, string(data), "@latest", "%s installs a floating version", rel)
	}
}

var kindNodeKey = regexp.MustCompile(`^KIND_NODE_1_(\d+)$`)

// TestKindNodeMatrixIsPinned checks the KIND_NODE_1_<minor> images in
// hack/tool-versions.env that the live e2e matrix boots: at least three
// minors, each image digest-pinned with the minor its key names and new
// enough for kro, kind-config.yaml using one of them, and kubectl within its
// one minor of skew of every one.
func TestKindNodeMatrixIsPinned(t *testing.T) {
	tv := toolVersions(t)
	kubectl := semver.FindStringSubmatch(tv["KUBECTL_VERSION"])
	require.NotNil(t, kubectl, "KUBECTL_VERSION")
	kubectlMinor, err := strconv.Atoi(kubectl[1])
	require.NoError(t, err)

	images := map[string]bool{}
	for k, v := range tv {
		m := kindNodeKey.FindStringSubmatch(k)
		if m == nil {
			continue
		}
		images[v] = true
		img := kindNodeImage.FindStringSubmatch(v)
		if !assert.NotNil(t, img, "%s=%s is not a kindest/node image", k, v) {
			continue
		}
		assert.Equal(t, v, img[0], "%s=%s: only the image, nothing else", k, v)
		assert.Equal(t, m[1], img[1], "%s=%s: the image's minor must match the key", k, v)
		assert.NotEmpty(t, img[2], "%s=%s: pin the image by digest", k, v)
		minor, err := strconv.Atoi(img[1])
		require.NoError(t, err)
		assert.GreaterOrEqual(t, minor, 30, "%s: hack/install-kro.sh needs Kubernetes 1.30 or newer (kro's graphrevisions CRD uses selectableFields)", k)
		assert.LessOrEqual(t, abs(minor-kubectlMinor), 1, "%s: KUBECTL_VERSION %s is more than one minor away", k, tv["KUBECTL_VERSION"])
	}
	assert.GreaterOrEqual(t, len(images), 3, "the live e2e matrix runs on three Kubernetes minors")

	cfg, err := os.ReadFile(filepath.Join(repoRoot(t), "test/e2e/kind-config.yaml"))
	require.NoError(t, err)
	node := kindNodeImage.FindString(string(cfg))
	assert.True(t, images[node], "kind-config.yaml's node image %s must be one of the KIND_NODE_* images", node)
}

// TestE2EMatrixRunsEverySuite checks hack/e2e/matrix.txt, the jobs
// hack/e2e/all.sh runs locally and e2e-live.yml in CI: every suite in
// hack/e2e/up.sh has a job and no other suite does, every job boots a
// KIND_NODE_* minor of hack/tool-versions.env, each suite runs on a minor
// once, as a whole or with a complete set of shards, and the core suite runs
// on every one of those minors. e2e-live.yml must take its matrix from
// all.sh -matrix.
func TestE2EMatrixRunsEverySuite(t *testing.T) {
	root := repoRoot(t)
	runs, err := coverage.SuiteRuns(root)
	require.NoError(t, err)
	tv := toolVersions(t)
	data, err := os.ReadFile(filepath.Join(root, "hack/e2e/matrix.txt"))
	require.NoError(t, err)

	suites := map[string]bool{}
	// shards["core 1.37"] is the set of core's shards on 1.37 ("-" for none).
	shards := map[string]map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if !assert.Len(t, f, 3, "matrix.txt %q: want suite, Kubernetes minor and shard", line) {
			continue
		}
		suite, minor, shard := f[0], f[1], f[2]
		_, known := runs[suite]
		assert.True(t, known, "matrix.txt %q: hack/e2e/up.sh has no suite %s", line, suite)
		assert.NotEmpty(t, tv["KIND_NODE_"+strings.ReplaceAll(minor, ".", "_")],
			"matrix.txt %q: hack/tool-versions.env has no KIND_NODE_ image for %s", line, minor)
		if shard != "-" {
			assert.Regexp(t, `^[1-9][0-9]*/[1-9][0-9]*$`, shard, "matrix.txt %q", line)
		}
		suites[suite] = true
		key := suite + " " + minor
		if shards[key] == nil {
			shards[key] = map[string]bool{}
		}
		assert.False(t, shards[key][shard], "matrix.txt %q: duplicate job", line)
		shards[key][shard] = true
	}
	for s := range runs {
		assert.True(t, suites[s], "hack/e2e/up.sh suite %s has no job in hack/e2e/matrix.txt", s)
	}
	for k := range tv {
		if m := kindNodeKey.FindStringSubmatch(k); m != nil {
			assert.NotEmpty(t, shards["core 1."+m[1]], "the core suite has no job on Kubernetes 1.%s (%s)", m[1], k)
		}
	}
	for key, got := range shards {
		n := 1
		for sh := range got {
			if _, of, ok := strings.Cut(sh, "/"); ok {
				n, _ = strconv.Atoi(of)
			}
		}
		want := map[string]bool{"-": true}
		if n > 1 || !got["-"] {
			want = map[string]bool{}
			for i := 1; i <= n; i++ {
				want[fmt.Sprintf("%d/%d", i, n)] = true
			}
		}
		assert.Equal(t, want, got, "%s: the whole suite, or every shard 1/n to n/n, once", key)
	}

	const rel = ".github/workflows/e2e-live.yml"
	data, err = os.ReadFile(filepath.Join(root, rel))
	require.NoError(t, err)
	var wf struct {
		Jobs struct {
			Image struct {
				Outputs map[string]string `json:"outputs"`
				Steps   []workflowStep    `json:"steps"`
			} `json:"image"`
			Suite struct {
				Strategy struct {
					Matrix any `json:"matrix"`
				} `json:"strategy"`
			} `json:"suite"`
		} `json:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &wf), rel)
	assert.Equal(t, "${{ fromJSON(needs.image.outputs.matrix) }}", wf.Jobs.Suite.Strategy.Matrix,
		"%s: the suite job's matrix must be the image job's matrix output", rel)
	assert.Equal(t, "${{ steps.matrix.outputs.matrix }}", wf.Jobs.Image.Outputs["matrix"], rel)
	var run string
	for _, st := range wf.Jobs.Image.Steps {
		if st.ID == "matrix" {
			run = st.Run
		}
	}
	assert.Regexp(t, `(?m)^m=\$\(bash hack/e2e/all\.sh -matrix\)$`, run,
		"%s: the image job's matrix step must print the matrix with hack/e2e/all.sh -matrix", rel)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
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

// runWorkflowScript runs a workflow run: script the way Actions does (bash -e
// -o pipefail) with fake tools first on PATH. sleep only advances $SECONDS,
// so wait loops run to their deadlines instantly.
func runWorkflowScript(t *testing.T, script string, fakes map[string]string, env ...string) (string, error) {
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
			out, err := runWorkflowScript(t, guard.Run, map[string]string{"gh": fakeGH},
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
