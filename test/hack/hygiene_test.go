// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// unpinnedUses returns the uses: references that are neither a local action
// (./...) nor pinned to a full commit SHA. A tag or branch can be moved to
// other code after review; a SHA cannot.
func unpinnedUses(uses []string) []string {
	var out []string
	for _, u := range uses {
		if strings.HasPrefix(u, "./") || pinnedAction.MatchString(u) {
			continue
		}
		out = append(out, u)
	}
	return out
}

func TestUnpinnedUses(t *testing.T) {
	tests := []struct {
		name string
		uses string
		want bool
	}{
		{"full SHA", "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", false},
		{"local action", "./.github/actions/setup-go", false},
		{"tag", "actions/checkout@v4", true},
		{"branch", "actions/checkout@main", true},
		{"short SHA", "actions/checkout@3d3c42e", true},
		{"no ref", "actions/checkout", true},
		{"reusable workflow by tag", "org/repo/.github/workflows/x.yml@v1", true},
		{"docker by tag", "docker://alpine:3", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, len(unpinnedUses([]string{tt.uses})) == 1)
		})
	}
}

// TestWorkflowActionsArePinnedBySHA checks every step and every reusable
// workflow call in .github/workflows and .github/actions (#1354). #1346
// pinned them; this keeps them pinned.
func TestWorkflowActionsArePinnedBySHA(t *testing.T) {
	for _, rel := range workflowFiles(t) {
		var uses []string
		for _, s := range workflowSteps(t, rel) {
			if s.Uses != "" {
				uses = append(uses, s.Uses)
			}
		}
		data, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
		require.NoError(t, err)
		var wf struct {
			Jobs map[string]struct {
				Uses string `json:"uses"`
			} `json:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(data, &wf), rel)
		for _, j := range wf.Jobs {
			if j.Uses != "" {
				uses = append(uses, j.Uses)
			}
		}
		assert.Empty(t, unpinnedUses(uses), "%s: pin these to a full commit SHA (with the tag in a comment)", rel)
	}
}

// TestLicenseHeaderCheck runs hack/check-license-headers.sh in a scratch git
// repository. It must fail on a missing header and on a file it cannot read,
// and pass when every file has one or no file matches. CI's old inline check
// ended in `2>/dev/null || true`, so an error in it passed (#1354).
func TestLicenseHeaderCheck(t *testing.T) {
	script := filepath.Join(repoRoot(t), "hack", "check-license-headers.sh")
	const header = "// Copyright 2026 The kardinal-promoter Authors.\n// Licensed under the Apache License, Version 2.0\n\npackage x\n"

	tests := []struct {
		name     string
		files    map[string]string
		remove   string // a tracked file deleted from the work tree
		pathspec []string
		wantCode int
		wantOut  string
	}{
		{
			name:     "every file has the header",
			files:    map[string]string{"a.go": header, "sub/b.go": header},
			pathspec: []string{"*.go"},
			wantOut:  "2 files have the Apache 2.0 header",
		},
		{
			name:     "a file without the header",
			files:    map[string]string{"a.go": header, "sub/b.go": "package x\n"},
			pathspec: []string{"*.go"},
			wantCode: 1,
			wantOut:  "sub/b.go",
		},
		{
			name:     "excluded path is not checked",
			files:    map[string]string{"a.go": header, "vendor/v.go": "package v\n"},
			pathspec: []string{"*.go", ":!:vendor/**"},
			wantOut:  "1 files have the Apache 2.0 header",
		},
		{
			name:     "no file matches",
			files:    map[string]string{"a.go": header},
			pathspec: []string{"*.tsx"},
			wantOut:  "no files match",
		},
		{
			name:     "a file that cannot be read fails the check",
			files:    map[string]string{"a.go": header, "gone.go": header},
			remove:   "gone.go",
			pathspec: []string{"*.go"},
			wantCode: 2,
			wantOut:  "cannot read gone.go",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tt.files {
				p := filepath.Join(dir, name)
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
			}
			git := func(args ...string) {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, string(out))
			}
			git("init", "-q")
			git("add", ".")
			if tt.remove != "" {
				require.NoError(t, os.Remove(filepath.Join(dir, tt.remove)))
			}

			cmd := exec.Command("bash", append([]string{script}, tt.pathspec...)...)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantCode, code, string(out))
			assert.Contains(t, string(out), tt.wantOut)
		})
	}
}

// TestCIRunsTheLicenseHeaderCheck checks that both header checks in ci.yml
// use the script, and that no CI step hides a check's failure behind
// `|| true` on a captured result.
func TestCIRunsTheLicenseHeaderCheck(t *testing.T) {
	var n int
	for _, s := range workflowSteps(t, ".github/workflows/ci.yml") {
		if !strings.Contains(s.Name, "Apache 2.0 headers") {
			continue
		}
		n++
		assert.Contains(t, s.Run, "hack/check-license-headers.sh", "step %q", s.Name)
		assert.NotContains(t, s.Run, "|| true", "step %q", s.Name)
	}
	assert.Equal(t, 2, n, "ci.yml has a Go and a web header check")
}

// TestDocsLintPinsPyYAML checks that docs-lint installs a pinned PyYAML, so a
// new release cannot change the YAML check under an unchanged commit.
func TestDocsLintPinsPyYAML(t *testing.T) {
	var found bool
	for _, s := range workflowSteps(t, ".github/workflows/ci.yml") {
		for _, line := range strings.Split(s.Run, "\n") {
			if strings.Contains(line, "pip install") && strings.Contains(strings.ToLower(line), "pyyaml") {
				found = true
				assert.Regexp(t, `(?i)pyyaml==\d+\.\d+\.\d+`, line, "step %q", s.Name)
			}
		}
	}
	assert.True(t, found, "no pip install pyyaml step in ci.yml")
}

// TestDemoSetupUsesE2EComponentVersions checks that demo/scripts/setup.sh takes
// the Argo CD version from hack/e2e/versions.env, the version the live suites
// test (#1354), and hard-codes no component version.
func TestDemoSetupUsesE2EComponentVersions(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "demo", "scripts", "setup.sh"))
	require.NoError(t, err)
	src := string(data)

	assert.True(t, strings.Contains(src, `source "${REPO_ROOT}/hack/e2e/versions.env"`),
		"setup.sh does not source hack/e2e/versions.env")
	assert.Contains(t, src, `ARGOCD_VERSION="${ARGOCD_VERSION:-${ARGOCD_RELEASE}}"`)
	assert.NotRegexp(t, regexp.MustCompile(`_VERSION:-v\d`), src, "a hard-coded component version default")

	env, err := os.ReadFile(filepath.Join(repoRoot(t), "hack", "e2e", "versions.env"))
	require.NoError(t, err)
	assert.Contains(t, string(env), "\nARGOCD_RELEASE=", "hack/e2e/versions.env sets ARGOCD_RELEASE")
}
