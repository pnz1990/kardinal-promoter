// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"fmt"
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

// These tests run the preflight scripts of .github/workflows/release.yml:
// the on-main guard, the version and release channel, and the release notes
// taken from docs/changelog.md.

const releaseWorkflowPath = ".github/workflows/release.yml"

type releaseStep struct {
	ID   string            `json:"id"`
	Name string            `json:"name"`
	Uses string            `json:"uses"`
	Run  string            `json:"run"`
	With map[string]any    `json:"with"`
	Env  map[string]string `json:"env"`
}

type releaseJob struct {
	Needs       any               `json:"needs"`
	Permissions map[string]string `json:"permissions"`
	Outputs     map[string]string `json:"outputs"`
	Env         map[string]string `json:"env"`
	Steps       []releaseStep     `json:"steps"`
}

func (j releaseJob) needs() []string {
	switch n := j.Needs.(type) {
	case string:
		return []string{n}
	case []any:
		out := make([]string, 0, len(n))
		for _, v := range n {
			out = append(out, fmt.Sprint(v))
		}
		return out
	}
	return nil
}

func (j releaseJob) step(t *testing.T, id string) releaseStep {
	t.Helper()
	for _, s := range j.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("release.yml has no step with id %q", id)
	return releaseStep{}
}

type releaseWorkflow struct {
	Defaults struct {
		Run struct {
			Shell string `json:"shell"`
		} `json:"run"`
	} `json:"defaults"`
	Jobs map[string]releaseJob `json:"jobs"`
}

func readReleaseWorkflow(t *testing.T) releaseWorkflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), releaseWorkflowPath))
	require.NoError(t, err)
	var wf releaseWorkflow
	require.NoError(t, yaml.Unmarshal(data, &wf))
	require.Contains(t, wf.Jobs, "preflight")
	return wf
}

func releaseJobs(t *testing.T) map[string]releaseJob {
	t.Helper()
	return readReleaseWorkflow(t).Jobs
}

// preflightScript returns the run: script of the preflight step whose name
// starts with prefix.
func preflightScript(t *testing.T, prefix string) string {
	t.Helper()
	for _, s := range releaseJobs(t)["preflight"].Steps {
		if strings.HasPrefix(s.Name, prefix) {
			require.NotEmpty(t, s.Run)
			return s.Run
		}
	}
	t.Fatalf("release.yml preflight has no step named %q...", prefix)
	return ""
}

// isolatedGitEnv keeps the host's git config out of the tests.
var isolatedGitEnv = []string{
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_CONFIG_GLOBAL=" + os.DevNull,
	"GIT_AUTHOR_NAME=test",
	"GIT_AUTHOR_EMAIL=test@example.com",
	"GIT_COMMITTER_NAME=test",
	"GIT_COMMITTER_EMAIL=test@example.com",
}

// runReleaseStep runs a release.yml run: script the way Actions runs it with
// the workflow's "shell: bash" default (bash -e -o pipefail) in dir. It returns the output and the values the script wrote
// to $GITHUB_OUTPUT.
func runReleaseStep(t *testing.T, dir, script string, env ...string) (string, map[string]string, error) {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "step.sh")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o600))
	outFile := filepath.Join(tmp, "github_output")
	require.NoError(t, os.WriteFile(outFile, nil, 0o600))
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", path)
	cmd.Dir = dir
	cmd.Env = append(append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GITHUB_OUTPUT=" + outFile,
	}, isolatedGitEnv...), env...)
	out, err := cmd.CombinedOutput()
	data, rerr := os.ReadFile(outFile)
	require.NoError(t, rerr)
	return string(out), parseGitHubOutput(t, string(data)), err
}

// parseGitHubOutput reads a $GITHUB_OUTPUT file: "name=value" lines and
// "name<<DELIM" ... "DELIM" blocks.
func parseGitHubOutput(t *testing.T, data string) map[string]string {
	t.Helper()
	out := map[string]string{}
	lines := strings.Split(data, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			continue
		}
		if name, delim, ok := strings.Cut(line, "<<"); ok && !strings.Contains(name, "=") {
			var val []string
			for i++; i < len(lines) && lines[i] != delim; i++ {
				val = append(val, lines[i])
			}
			require.Less(t, i, len(lines), "GITHUB_OUTPUT: %s has no closing %s", name, delim)
			out[name] = strings.Join(val, "\n")
			continue
		}
		name, val, ok := strings.Cut(line, "=")
		require.True(t, ok, "GITHUB_OUTPUT line %q", line)
		out[name] = val
	}
	return out
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}, isolatedGitEnv...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// TestReleaseRefusesTagsOffMain builds the shape of the v0.8.1 tag: a merge
// commit outside main. It then checks out each tag the way a tag push can
// arrive, with only the tag and no origin/main, and runs the guard.
func TestReleaseRefusesTagsOffMain(t *testing.T) {
	script := preflightScript(t, "Check that the tagged commit is on main")

	origin := t.TempDir()
	runGit(t, origin, "init", "-q", "-b", "main")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "A")
	runGit(t, origin, "tag", "v0.8.0")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "B")
	runGit(t, origin, "tag", "v0.9.0")
	runGit(t, origin, "checkout", "-q", "-b", "side", "v0.8.0")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "S")
	runGit(t, origin, "tag", "v0.9.1-rc.0")
	runGit(t, origin, "checkout", "-q", "--detach", "main")
	runGit(t, origin, "merge", "-q", "--no-ff", "--no-edit", "side")
	runGit(t, origin, "tag", "v0.8.1")
	runGit(t, origin, "checkout", "-q", "main")

	tests := []struct {
		tag     string
		onMain  bool
		comment string
	}{
		{tag: "v0.9.0", onMain: true, comment: "the tip of main"},
		{tag: "v0.8.0", onMain: true, comment: "an older commit on main"},
		{tag: "v0.8.1", onMain: false, comment: "a merge commit outside main, like v0.8.1"},
		{tag: "v0.9.1-rc.0", onMain: false, comment: "a commit on an unmerged branch"},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			work := t.TempDir()
			runGit(t, work, "init", "-q")
			runGit(t, work, "remote", "add", "origin", origin)
			runGit(t, work, "fetch", "-q", "--no-tags", "origin", "+refs/tags/"+tt.tag+":refs/tags/"+tt.tag)
			runGit(t, work, "checkout", "-q", "--detach", tt.tag)
			sha := runGit(t, work, "rev-parse", "HEAD")
			check := exec.Command("git", "rev-parse", "-q", "--verify", "refs/remotes/origin/main")
			check.Dir = work
			require.Error(t, check.Run(), "the work clone must start without origin/main")

			out, _, err := runReleaseStep(t, work, script, "GITHUB_SHA="+sha, "GITHUB_REF_NAME="+tt.tag)
			if tt.onMain {
				require.NoError(t, err, "%s: %s", tt.comment, out)
				assert.Contains(t, out, "is on main")
				return
			}
			require.Error(t, err, "%s must fail: %s", tt.comment, out)
			assert.Contains(t, out, "which is not on main")
		})
	}
}

// TestReleaseChannelFromVersion checks that only a final version (no "-")
// pushes controller:latest and becomes the latest GitHub release.
func TestReleaseChannelFromVersion(t *testing.T) {
	script := preflightScript(t, "Set version and release channel from tag")
	const image = "ghcr.io/pnz1990/kardinal-promoter/controller"

	tests := []struct {
		tag        string
		wantErr    bool
		prerelease string
		makeLatest string
		chart      string
		tags       []string
	}{
		{tag: "v0.9.0", prerelease: "false", makeLatest: "true", chart: "0.9.0",
			tags: []string{image + ":v0.9.0", image + ":latest"}},
		{tag: "v1.10.2", prerelease: "false", makeLatest: "true", chart: "1.10.2",
			tags: []string{image + ":v1.10.2", image + ":latest"}},
		{tag: "v0.9.0-rc.1", prerelease: "true", makeLatest: "false", chart: "0.9.0-rc.1",
			tags: []string{image + ":v0.9.0-rc.1"}},
		{tag: "v0.10.0-alpha.0", prerelease: "true", makeLatest: "false", chart: "0.10.0-alpha.0",
			tags: []string{image + ":v0.10.0-alpha.0"}},
		{tag: "v1.0.0-0.3.7", prerelease: "true", makeLatest: "false", chart: "1.0.0-0.3.7",
			tags: []string{image + ":v1.0.0-0.3.7"}},
		{tag: "v1.0.0-x-y.01a", prerelease: "true", makeLatest: "false", chart: "1.0.0-x-y.01a",
			tags: []string{image + ":v1.0.0-x-y.01a"}},
		{tag: "v0.9", wantErr: true},
		{tag: "0.9.0", wantErr: true},
		{tag: "v0.9.0+build.1", wantErr: true},
		{tag: "v0.9.0-rc.1;id", wantErr: true},
		{tag: "v0.9.0.1", wantErr: true},
		// helm package rejects these, but only after the image is pushed.
		{tag: "v0.9.0-rc..1", wantErr: true},
		{tag: "v0.9.0-rc.01", wantErr: true},
		{tag: "v0.9.0-.", wantErr: true},
		{tag: "v0.9.0-", wantErr: true},
		{tag: "v01.2.3", wantErr: true},
		{tag: "v1.02.3", wantErr: true},
		{tag: "v1.2.03", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			out, got, err := runReleaseStep(t, repoRoot(t), script, "GITHUB_REF_NAME="+tt.tag)
			if tt.wantErr {
				require.Error(t, err, out)
				assert.Contains(t, out, "is not vMAJOR.MINOR.PATCH")
				assert.Empty(t, got, "a rejected tag must write no outputs")
				return
			}
			require.NoError(t, err, out)
			assert.Equal(t, tt.tag, got["version"])
			assert.Equal(t, tt.chart, got["chart-version"])
			assert.Equal(t, tt.prerelease, got["prerelease"])
			assert.Equal(t, tt.makeLatest, got["make-latest"])
			assert.Equal(t, tt.tags, strings.Split(got["image-tags"], "\n"))
			if tt.prerelease == "true" {
				assert.NotContains(t, got["image-tags"], "latest", "a prerelease must not move latest")
			}
		})
	}
}

// changelogFixture has the heading styles the notes step must accept, in the
// order docs/changelog.md uses: newest first, "---" between sections. The
// v0.9.1-rc.1 section has code blocks with "---" and "## " lines, like an
// upgrade note with YAML and shell comments. In the literal, three single
// quotes stand for a code fence, because a Go raw string cannot hold a
// backquote.
var changelogFixture = strings.ReplaceAll(`# Changelog

## [Unreleased]

- not released yet

---

## [v0.9.1-rc.1] — 2026-11-15

### Upgrade

'''yaml
apiVersion: v1
kind: ConfigMap
---
apiVersion: v1
kind: Secret
'''

- Then run:

  '''bash
  ## x
  kubectl apply -f crds.yaml
  '''

~~~
## [v0.7.9] — not a heading
---
~~~

- after the code blocks

---

## [v0.9.0] — 2026-11-01

- final notes

---

## [v0.9.0-rc.1] — 2026-10-01

### Added

- rc feature

---

## v0.8.2

- bare heading notes
## [v0.8.1] — 2026-04-17

- v0.8.1 notes

---

## [v0.8.0] — 2026-04-17

---

[Unreleased]: https://github.com/pnz1990/kardinal-promoter/compare/v0.9.0...HEAD
`, "'''", "```")

const rc1UpgradeNotes = "### Upgrade\n\n" +
	"```yaml\napiVersion: v1\nkind: ConfigMap\n---\napiVersion: v1\nkind: Secret\n```\n\n" +
	"- Then run:\n\n  ```bash\n  ## x\n  kubectl apply -f crds.yaml\n  ```\n\n" +
	"~~~\n## [v0.7.9] — not a heading\n---\n~~~\n\n" +
	"- after the code blocks"

func TestReleaseNotesFromChangelog(t *testing.T) {
	script := preflightScript(t, "Release notes from docs/changelog.md")
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docs", "changelog.md"), []byte(changelogFixture), 0o600))

	tests := []struct {
		version string
		want    string
		wantErr bool
	}{
		{version: "v0.9.0", want: "- final notes"},
		{version: "v0.9.1-rc.1", want: rc1UpgradeNotes},
		{version: "v0.9.0-rc.1", want: "### Added\n\n- rc feature"},
		{version: "v0.8.2", want: "- bare heading notes"},
		{version: "v0.8.1", want: "- v0.8.1 notes"},
		{version: "v0.8.0", wantErr: true},
		{version: "v0.7.0", wantErr: true},
		{version: "v0.7.9", wantErr: true}, // only inside a code block
		{version: "v0.9", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			out, got, err := runReleaseStep(t, dir, script, "VERSION="+tt.version)
			if tt.wantErr {
				require.Error(t, err, out)
				assert.Contains(t, out, "docs/changelog.md has no \"## ["+tt.version+"]\" section")
				assert.Empty(t, got, "a missing section must write no notes")
				return
			}
			require.NoError(t, err, out)
			assert.Equal(t, tt.want, got["notes"])
		})
	}
}

var changelogVersionHeading = regexp.MustCompile(`(?m)^## \[(v[0-9][^\]]*)\]`)

// withoutCodeBlocks drops the ``` and ~~~ blocks of a markdown text, the way
// the notes step ignores them when it looks for section boundaries.
func withoutCodeBlocks(text string) string {
	var out []string
	fence := ""
	for _, line := range strings.Split(text, "\n") {
		mark := strings.TrimSpace(line)
		if len(mark) > 3 {
			mark = mark[:3]
		}
		switch {
		case fence == "" && (mark == "```" || mark == "~~~"):
			fence = mark
		case fence != "":
			if mark == fence {
				fence = ""
			}
		default:
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// TestReleaseNotesForEveryChangelogVersion runs the notes step on the real
// docs/changelog.md: every released version has a section the step can read.
func TestReleaseNotesForEveryChangelogVersion(t *testing.T) {
	script := preflightScript(t, "Release notes from docs/changelog.md")
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "changelog.md"))
	require.NoError(t, err)
	matches := changelogVersionHeading.FindAllStringSubmatch(withoutCodeBlocks(string(data)), -1)
	require.NotEmpty(t, matches)

	// The two tags that point to merge commits outside main say so.
	offMain := map[string]string{"v0.8.1": "bd2bcf3", "v0.6.0": "369be4c"}
	seen := map[string]bool{}
	for _, m := range matches {
		version := m[1]
		t.Run(version, func(t *testing.T) {
			require.False(t, seen[version], "docs/changelog.md has two %s sections", version)
			seen[version] = true
			out, got, err := runReleaseStep(t, repoRoot(t), script, "VERSION="+version)
			require.NoError(t, err, out)
			notes := got["notes"]
			require.NotEmpty(t, notes)
			assert.NotRegexp(t, `(?m)^## `, withoutCodeBlocks(notes), "the notes must stop at the next version")
			if sha, ok := offMain[version]; ok {
				assert.Contains(t, notes, sha)
			}
		})
	}
	for version := range offMain {
		assert.True(t, seen[version], "docs/changelog.md has no %s section", version)
	}
}

var pinnedAction = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)

// TestReleaseWorkflowWiring checks that the release job uses what preflight
// decided and that nothing in release.yml still builds notes from git log or
// pushes latest on its own.
func TestReleaseWorkflowWiring(t *testing.T) {
	wf := readReleaseWorkflow(t)
	jobs := wf.Jobs
	preflight := jobs["preflight"]

	t.Run("run scripts use bash -e -o pipefail", func(t *testing.T) {
		assert.Equal(t, "bash", wf.Defaults.Run.Shell,
			"runReleaseStep runs the scripts as the workflow does only with defaults.run.shell: bash")
	})

	t.Run("preflight outputs come from its steps", func(t *testing.T) {
		assert.Equal(t, map[string]string{
			"version":       "${{ steps.version.outputs.version }}",
			"chart-version": "${{ steps.version.outputs.chart-version }}",
			"prerelease":    "${{ steps.version.outputs.prerelease }}",
			"make-latest":   "${{ steps.version.outputs.make-latest }}",
			"image-tags":    "${{ steps.version.outputs.image-tags }}",
			"notes":         "${{ steps.notes.outputs.notes }}",
		}, preflight.Outputs)
		assert.Equal(t, map[string]string{"VERSION": "${{ steps.version.outputs.version }}"},
			preflight.step(t, "notes").Env)
	})

	t.Run("preflight runs first, read-only", func(t *testing.T) {
		assert.Empty(t, preflight.needs())
		assert.Equal(t, map[string]string{"contents": "read"}, preflight.Permissions)
		var names []string
		for _, s := range preflight.Steps {
			names = append(names, s.Name)
		}
		require.Len(t, names, 4)
		assert.True(t, strings.HasPrefix(preflight.Steps[0].Uses, "actions/checkout@"))
		assert.Equal(t, "Check that the tagged commit is on main", names[1])
		for name, job := range jobs {
			if name != "preflight" {
				assert.Contains(t, job.needs(), "preflight", "job %s must wait for preflight", name)
			}
		}
		assert.Contains(t, jobs["release"].needs(), "test")
	})

	t.Run("release uses the preflight outputs", func(t *testing.T) {
		release := jobs["release"]
		assert.Equal(t, "${{ needs.preflight.outputs.version }}", release.Env["VERSION"])
		assert.Equal(t, "${{ needs.preflight.outputs.chart-version }}", release.Env["CHART_VERSION"])
		assert.Equal(t, "${{ needs.preflight.outputs.image-tags }}", release.step(t, "build-controller").With["tags"])

		var gh releaseStep
		for _, s := range release.Steps {
			if strings.HasPrefix(s.Uses, "softprops/action-gh-release@") {
				gh = s
			}
		}
		require.NotEmpty(t, gh.Uses, "release job has no GitHub release step")
		assert.Equal(t, "${{ needs.preflight.outputs.prerelease }}", gh.With["prerelease"])
		assert.Equal(t, "${{ needs.preflight.outputs.make-latest }}", gh.With["make_latest"])
		assert.NotContains(t, gh.With, "body_path", "body_path would replace the changelog notes")
		assert.Contains(t, gh.With["body"], "${{ needs.preflight.outputs.notes }}")
	})

	t.Run("no other source of notes or latest", func(t *testing.T) {
		for name, job := range jobs {
			for _, s := range job.Steps {
				text := s.Run + fmt.Sprint(s.With)
				for _, banned := range []string{"git log", "git describe", "env.CHANGELOG"} {
					assert.False(t, strings.Contains(text, banned), "%s: step %q uses %q", name, s.Name, banned)
				}
				if name == "preflight" && s.ID == "version" {
					continue
				}
				assert.False(t, strings.Contains(text, ":latest"), "%s: step %q names a latest tag", name, s.Name)
			}
		}
	})

	t.Run("every action is pinned by SHA", func(t *testing.T) {
		for name, job := range jobs {
			for _, s := range job.Steps {
				if s.Uses != "" {
					assert.Regexp(t, pinnedAction, s.Uses, "%s: step %q", name, s.Name)
				}
			}
		}
	})
}
