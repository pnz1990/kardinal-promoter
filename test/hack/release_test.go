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
	If   any               `json:"if"`
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
	Jobs        map[string]releaseJob `json:"jobs"`
	Concurrency struct {
		Group            string `json:"group"`
		CancelInProgress bool   `json:"cancel-in-progress"`
	} `json:"concurrency"`
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
			// v0.8.1's merge commit also carries v0.9.0, the next minor.
			assert.True(t, strings.Contains(out, "which is not on main") || strings.Contains(out, "contains v0.9.0"), out)
		})
	}
}

// TestReleaseMaintenanceBranch: a patch is released from its maintenance
// branch release/vX.Y when main has moved on, if that branch descends from
// vX.Y.0. A .0 off main, a patch on a branch that does not descend from its
// .0, or on another minor's branch, is refused; so is a dry run of a
// version outside its branch's line.
func TestReleaseMaintenanceBranch(t *testing.T) {
	script := preflightScript(t, "Check that the tagged commit is on main")

	origin := t.TempDir()
	runGit(t, origin, "init", "-q", "-b", "main")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "v0.11")
	runGit(t, origin, "tag", "v0.11.0")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "v0.12")
	runGit(t, origin, "tag", "v0.12.0")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "v0.13 work")
	runGit(t, origin, "tag", "v0.13.0")
	// An old line's patch tagged on main, after v0.13.0.
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "a v0.12 fix on main")
	runGit(t, origin, "tag", "v0.12.6")
	// release/v0.12 from v0.12.0, with a cherry-picked fix.
	runGit(t, origin, "checkout", "-q", "-b", "release/v0.12", "v0.12.0")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "fix")
	runGit(t, origin, "tag", "v0.12.1")
	runGit(t, origin, "tag", "v0.12.2-rc.1")
	runGit(t, origin, "tag", "v0.12.3-wrong-zero")
	// A branch named for v0.11 that starts after v0.11.0's line diverged
	// is fine; one named release/v0.10 without a v0.10.0 is not.
	runGit(t, origin, "checkout", "-q", "-b", "release/v0.10", "v0.11.0")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "no v0.10.0 here")
	runGit(t, origin, "tag", "v0.10.1")
	// A release/v0.11 cut before v0.11.0's successor exists is fine; one
	// cut from v0.11.0 but named release/v0.12 does not descend from
	// v0.12.0.
	runGit(t, origin, "checkout", "-q", "-b", "release/v0.12-wrong", "v0.11.0")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "cut from v0.11.0")
	runGit(t, origin, "tag", "v0.12.5")
	// A side branch, not a maintenance branch.
	runGit(t, origin, "checkout", "-q", "-b", "side", "v0.12.0")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "side")
	runGit(t, origin, "tag", "v0.12.4")
	runGit(t, origin, "checkout", "-q", "release/v0.12")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "a .0 on a maintenance branch")
	// A v0.12.0-less line merged into release/v0.12: the tagged commit is on
	// the branch, but does not descend from v0.12.0.
	runGit(t, origin, "merge", "-q", "--no-ff", "--no-edit", "release/v0.12-wrong")
	runGit(t, origin, "tag", "v0.12.0-again")
	// A major bump on main, and a later line's prerelease.
	runGit(t, origin, "checkout", "-q", "main")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "v1")
	runGit(t, origin, "tag", "v1.0.0")
	runGit(t, origin, "checkout", "-q", "-b", "release/v0.13", "v0.13.0")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "a v0.13 fix")
	runGit(t, origin, "tag", "v0.13.1")
	runGit(t, origin, "checkout", "-q", "-b", "v13-major", "release/v0.13")
	runGit(t, origin, "merge", "-q", "--no-ff", "--no-edit", "v1.0.0")
	runGit(t, origin, "tag", "v0.13.2")
	runGit(t, origin, "checkout", "-q", "-b", "v13-rc", "release/v0.13")
	runGit(t, origin, "commit", "-q", "--allow-empty", "-m", "v0.14 work")
	runGit(t, origin, "tag", "v0.14.0-rc.1")
	runGit(t, origin, "tag", "v0.13.3")
	runGit(t, origin, "checkout", "-q", "main")
	// main (with v0.13.0) merged into release/v0.12, then tagged as a patch.
	runGit(t, origin, "checkout", "-q", "-b", "release/v0.12-merged", "release/v0.12")
	runGit(t, origin, "merge", "-q", "--no-ff", "--no-edit", "v0.13.0")
	runGit(t, origin, "tag", "v0.12.7")
	runGit(t, origin, "checkout", "-q", "main")

	work := func(t *testing.T, ref string) (string, string) {
		t.Helper()
		w := t.TempDir()
		runGit(t, w, "init", "-q")
		runGit(t, w, "remote", "add", "origin", origin)
		runGit(t, w, "fetch", "-q", "--no-tags", "origin", "+"+ref+":"+ref)
		runGit(t, w, "checkout", "-q", "--detach", ref)
		return w, runGit(t, w, "rev-parse", "HEAD")
	}
	for _, tt := range []struct {
		tag, want string
		ok        bool
	}{
		{tag: "v0.12.1", ok: true, want: "is on the maintenance branch release/v0.12, which descends from v0.12.0"},
		{tag: "v0.12.2-rc.1", ok: true, want: "maintenance branch release/v0.12"},
		// Cut from v0.11.0, it carries the next minor: refused before its
		// missing v0.10.0 is looked for.
		{tag: "v0.10.1", want: "contains v0.11.0"},
		{tag: "v0.12.4", want: "nor on its maintenance branch release/v0.12"},
		{tag: "v0.12.5", want: "does not descend from v0.12.0"},
		{tag: "v0.12.6", want: "contains v0.13.0: a v0.12 patch must not carry a later line's work"},
		{tag: "v0.12.7", want: "contains v0.13.0"},
		{tag: "v0.13.1", ok: true, want: "maintenance branch release/v0.13"}, // v1.0.0 is later, but not in it
		{tag: "v0.13.2", want: "contains v1.0.0"},                            // a major bump merged in
		{tag: "v0.13.3", want: "contains v0.14.0-rc.1"},                      // a later line's prerelease
	} {
		t.Run(tt.tag, func(t *testing.T) {
			w, sha := work(t, "refs/tags/"+tt.tag)
			out, _, err := runReleaseStep(t, w, script, "GITHUB_SHA="+sha, "GITHUB_REF_NAME="+tt.tag)
			if tt.ok {
				require.NoError(t, err, out)
			} else {
				require.Error(t, err, out)
			}
			assert.Contains(t, out, tt.want)
		})
	}
	t.Run("a failed tag fetch does not skip the later-line check", func(t *testing.T) {
		w, sha := work(t, "refs/tags/v0.12.6")
		runGit(t, w, "fetch", "-q", "--no-tags", "origin", "+refs/tags/v0.13.0:refs/tags/v0.13.0")
		runGit(t, w, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone"))
		out, _, err := runReleaseStep(t, w, script, "GITHUB_SHA="+sha, "GITHUB_REF_NAME=v0.12.6")
		require.Error(t, err, out)
		assert.Contains(t, out, "contains v0.13.0")
	})
	t.Run("a .0 off main", func(t *testing.T) {
		w, sha := work(t, "refs/tags/v0.12.0-again")
		out, _, err := runReleaseStep(t, w, script, "GITHUB_SHA="+sha, "GITHUB_REF_NAME=v0.13.0")
		require.Error(t, err, out)
		assert.Contains(t, out, "A .0 release is tagged on main")
	})

	// Dry runs: from release/v0.12, of a v0.12.x version only.
	for _, tt := range []struct {
		ref, version, want string
		ok                 bool
	}{
		{ref: "release/v0.12", version: "v0.12.1-dryrun.1", ok: true, want: "descends from v0.12.0"},
		{ref: "release/v0.12", version: "v0.13.1-dryrun.1", want: "runs from release/v0.13"},
		{ref: "release/v0.12", version: "v0.12.0-dryrun.1", want: "A .0 release is tagged on main"},
		{ref: "release/v0.10", version: "v0.10.1-dryrun.1", want: "contains v0.11.0"},
		{ref: "main", version: "v0.13.0-dryrun.1", ok: true, want: "the tag check runs at the real release"},
	} {
		t.Run("dry run "+tt.ref+" "+tt.version, func(t *testing.T) {
			w, sha := work(t, "refs/heads/"+tt.ref)
			out, _, err := runReleaseStep(t, w, script, "DRY_RUN=true", "DISPATCH_VERSION="+tt.version,
				"GITHUB_SHA="+sha, "GITHUB_REF_NAME="+tt.ref)
			if tt.ok {
				require.NoError(t, err, out)
			} else {
				require.Error(t, err, out)
			}
			assert.Contains(t, out, tt.want)
		})
	}
}

// TestReleaseLatestIsTheHighestRelease: a patch of an older minor, released
// after a newer minor, gets neither the latest image tags nor the latest
// GitHub release; the newest final release does.
func TestReleaseLatestIsTheHighestRelease(t *testing.T) {
	script := preflightScript(t, "Set version and release channel from tag")
	const image = "ghcr.io/pnz1990/kardinal-promoter/controller"
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "x")
	for _, tag := range []string{"v0.12.0", "v0.13.0", "v0.14.0-rc.1", "v0.13.1-dryrun.1"} {
		runGit(t, dir, "tag", tag)
	}
	for _, tt := range []struct {
		tag    string
		latest bool
	}{
		{"v0.12.1", false}, // v0.13.0 is out
		{"v0.13.1", true},  // a prerelease of v0.14 does not count
		{"v0.14.0", true},
		{"v0.13.0", true}, // re-running the newest release
	} {
		t.Run(tt.tag, func(t *testing.T) {
			out, got, err := runReleaseStep(t, dir, script, "GITHUB_REF_NAME="+tt.tag)
			require.NoError(t, err, out)
			assert.Equal(t, fmt.Sprint(tt.latest), got["make-latest"], out)
			assert.Equal(t, tt.latest, strings.Contains(got["image-tags"], image+":latest"), got["image-tags"])
			assert.Equal(t, "false", got["prerelease"])
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
			// Outside a repository: no released tags, so a final version is
			// the highest (TestReleaseLatestIsTheHighestRelease has tags).
			out, got, err := runReleaseStep(t, t.TempDir(), script, "GITHUB_REF_NAME="+tt.tag)
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

// TestReleaseRefusesUnreleasedFragments (#1894): a tag whose
// docs/changelog.d still has entries fails before its notes are read; the
// README there, or no directory, passes.
func TestReleaseRefusesUnreleasedFragments(t *testing.T) {
	script := preflightScript(t, "No unreleased changelog fragments")
	dir := t.TempDir()
	out, _, err := runReleaseStep(t, dir, script)
	require.NoError(t, err, "no docs/changelog.d: %s", out)
	frag := filepath.Join(dir, "docs", "changelog.d")
	require.NoError(t, os.MkdirAll(frag, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(frag, "README.md"), []byte("how to\n"), 0o600))
	out, _, err = runReleaseStep(t, dir, script)
	require.NoError(t, err, "only the README: %s", out)
	require.NoError(t, os.WriteFile(filepath.Join(frag, "12-x.md"), []byte("---\nsection: Added\n---\n- **X** — x\n"), 0o600))
	out, _, err = runReleaseStep(t, dir, script)
	require.Error(t, err)
	assert.Contains(t, out, "docs/changelog.d still has entries; run make changelog-release")
	assert.Contains(t, out, "12-x.md")
}

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
			_ = os.RemoveAll(filepath.Join(dir, "release-notes"))
			out, got, err := runReleaseStep(t, dir, script, "VERSION="+tt.version)
			if tt.wantErr {
				require.Error(t, err, out)
				assert.Contains(t, out, "docs/changelog.md has no \"## ["+tt.version+"]\" section")
				assert.Empty(t, got, "a missing section must write no notes")
				assert.NoFileExists(t, filepath.Join(dir, "release-notes", "notes.md"))
				return
			}
			require.NoError(t, err, out)
			assert.Equal(t, "release-notes/notes.md", got["notes"])
			assert.Equal(t, tt.want, readNotes(t, dir))
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

// readNotes is the notes file the notes step wrote in dir, without the final
// newline it adds.
func readNotes(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "release-notes", "notes.md"))
	require.NoError(t, err)
	return strings.TrimSuffix(string(data), "\n")
}

// TestReleaseNotesForEveryChangelogVersion runs the notes step on the real
// docs/changelog.md: every released version has a section the step can read.
func TestReleaseNotesForEveryChangelogVersion(t *testing.T) {
	t.Parallel()
	script := preflightScript(t, "Release notes from docs/changelog.md")
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "changelog.md"))
	require.NoError(t, err)
	// A copy, so the notes file is not written into the checkout.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docs", "changelog.md"), data, 0o600))
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
			out, _, err := runReleaseStep(t, dir, script, "VERSION="+version)
			require.NoError(t, err, out)
			notes := readNotes(t, dir)
			require.NotEmpty(t, notes)
			assert.NotRegexp(t, `(?m)^## `, withoutCodeBlocks(notes), "the notes must stop at the next version")
			assertAbsoluteLinks(t, notes)
			if sha, ok := offMain[version]; ok {
				assert.Contains(t, notes, sha)
			}
		})
	}
	for version := range offMain {
		assert.True(t, seen[version], "docs/changelog.md has no %s section", version)
	}
}

var (
	// markdownRefDef is a reference definition's target ([ref]: target).
	markdownRefDef = regexp.MustCompile(`(?m)^ {0,3}\[[^\]\n]+\]:[ \t]+<?([^\s>]+)`)
	hasScheme      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
)

// assertAbsoluteLinks checks that every link in notes (inline links, images
// and reference definitions, outside code) has a scheme, and that each link
// to the docs site resolves: a page of docs/ that is no redirect, and an
// anchor on it (docsLinkProblem). It returns how many docs links it saw.
func assertAbsoluteLinks(t *testing.T, notes string) int {
	t.Helper()
	root := repoRoot(t)
	redirects := mkdocsRedirects(t, filepath.Join(root, "mkdocs.yml"))
	var links []string
	for _, l := range markdownLinks(notes) {
		links = append(links, l.target)
	}
	for _, m := range markdownRefDef.FindAllStringSubmatch(withoutCodeBlocks(notes), -1) {
		links = append(links, m[1])
	}
	docsLinks := 0
	for _, link := range links {
		if !assert.Regexp(t, hasScheme, link, "a link in the release notes is relative") {
			continue
		}
		if problem, ok := docsLinkProblem(filepath.Join(root, "docs"), "changelog.md", link, redirects); ok {
			docsLinks++
			assert.Empty(t, problem, "release notes link %s", link)
		}
	}
	return docsLinks
}

// TestReleaseNotesAbsoluteLinks runs the notes step on a section with every
// link form: the relative ones become docs-site URLs in mkdocs' directory
// form, the rest and the code stay as they are, and a link outside the docs
// fails the release with no notes (#1829).
func TestReleaseNotesAbsoluteLinks(t *testing.T) {
	script := preflightScript(t, "Release notes from docs/changelog.md")
	run := func(t *testing.T, section string) (string, error) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "docs"), 0o755))
		changelog := "# Changelog\n\n## [v9.9.9] — 2026-10-15\n\n" + section + "\n\n## [v9.9.8] — 2026-10-01\n\n- older\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "docs", "changelog.md"), []byte(changelog), 0o600))
		out, _, err := runReleaseStep(t, dir, script, "VERSION=v9.9.9")
		if err != nil {
			assert.NoFileExists(t, filepath.Join(dir, "release-notes", "notes.md"), "a failed step writes no notes")
			return out, err
		}
		return readNotes(t, dir), nil
	}

	tests := []struct{ name, in, want string }{
		{"page", "- see [evidence](pr-evidence.md).", "- see [evidence](" + siteURL + "pr-evidence/)."},
		{"page and anchor", "- [audit](guides/security.md#audit-logging)", "- [audit](" + siteURL + "guides/security/#audit-logging)"},
		{"in-page anchor", "- [Before you upgrade](#from-v0100)", "- [Before you upgrade](" + siteURL + "changelog/#from-v0100)"},
		{"file", "- [spec](reference/openapi.json)", "- [spec](" + siteURL + "reference/openapi.json)"},
		{"index pages", "[a](index.md) [b](agentic/index.md) [c](guides/README.md#x)", "[a](" + siteURL + ") [b](" + siteURL + "agentic/) [c](" + siteURL + "guides/#x)"},
		{"dot segments", "[a](./ui.md) [b](guides/../rollback.md)", "[a](" + siteURL + "ui/) [b](" + siteURL + "rollback/)"},
		{"image and title", `![x](img/a.png "A") [y](<sharding.md>)`, `![x](` + siteURL + `img/a.png "A") [y](<` + siteURL + `sharding/>)`},
		{"reference definition", "[x][ref]\n\n[ref]: operate.md#scale", "[x][ref]\n\n[ref]: " + siteURL + "operate/#scale"},
		{"two on a line", "[a](ui.md) and [b](#c)", "[a](" + siteURL + "ui/) and [b](" + siteURL + "changelog/#c)"},
		{"absolute links stay", "[a](https://github.com/x/y/pull/1) [b](mailto:a@b.c) [c](//cdn.example/x)", "[a](https://github.com/x/y/pull/1) [b](mailto:a@b.c) [c](//cdn.example/x)"},
		{"code stays", "- `[a](ui.md)` and [b](ui.md)\n\n```md\n[c](ui.md)\n```\n\n~~~\n[d](#e)\n~~~", "- `[a](ui.md)` and [b](" + siteURL + "ui/)\n\n```md\n[c](ui.md)\n```\n\n~~~\n[d](#e)\n~~~"},
		{"no links", "- plain (with parens) and [brackets]", "- plain (with parens) and [brackets]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := run(t, tt.in)
			require.NoError(t, err, got)
			assert.Equal(t, tt.want, got)
		})
	}
	for _, bad := range []string{"[a](/abs.md)", "[a](../outside.md)", "[a](guides/../../x.md#y)"} {
		t.Run("refuses "+bad, func(t *testing.T) {
			out, err := run(t, "- "+bad)
			require.Error(t, err, out)
			assert.Contains(t, out, "the release notes link outside the docs")
		})
	}
	t.Run("site is mkdocs.yml's site_url", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(repoRoot(t), "mkdocs.yml"))
		require.NoError(t, err)
		assert.Regexp(t, `(?m)^site_url: `+regexp.QuoteMeta(siteURL)+`\s*$`, string(data))
		assert.NotRegexp(t, `(?m)^use_directory_urls:\s*false`, string(data), "the step writes directory URLs")
		assert.Contains(t, script, `SITE = "`+siteURL+`"`)
	})
}

// TestReleaseNotesLinksFromTheFragments runs a dry run's notes step on the
// real docs/changelog.md and docs/changelog.d: every link the next release's
// body will carry is absolute and names a docs page or file.
func TestReleaseNotesLinksFromTheFragments(t *testing.T) {
	script := preflightScript(t, "Release notes from docs/changelog.md")
	root := repoRoot(t)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "docs", "changelog.d"), 0o755))
	data, err := os.ReadFile(filepath.Join(root, "docs", "changelog.md"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docs", "changelog.md"), data, 0o600))
	fragments, err := filepath.Glob(filepath.Join(root, "docs", "changelog.d", "*.md"))
	require.NoError(t, err)
	relative := 0
	for _, f := range fragments {
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "docs", "changelog.d", filepath.Base(f)), data, 0o600))
		if filepath.Base(f) == "README.md" {
			continue
		}
		for _, l := range markdownLinks(string(data)) {
			if !hasScheme.MatchString(l.target) && !strings.HasPrefix(l.target, "//") {
				relative++
			}
		}
	}
	out, _, err := runReleaseStep(t, dir, script, "VERSION=v0.0.0-dryrun.1", "DRY_RUN=true")
	require.NoError(t, err, out)
	notes := readNotes(t, dir)
	require.Contains(t, notes, "(dry run v0.0.0-dryrun.1: the unreleased entries)")
	docsLinks := assertAbsoluteLinks(t, notes)
	assert.GreaterOrEqual(t, docsLinks, relative, "each relative link of the fragments is a docs link in the notes")
	t.Logf("%d fragments, %d relative links, %d docs links in the notes", len(fragments), relative, docsLinks)
}

var stopCommands = regexp.MustCompile(`^::stop-commands::(.*)$`)

// workflowCommands is what the Actions runner would take as workflow commands
// in a step's log: the lines starting with "::" (leading space ignored),
// except between "::stop-commands::TOKEN" and "::TOKEN::", where it reads
// nothing but that resume line. It also returns the tokens it stopped with.
func workflowCommands(log string) (commands, tokens []string) {
	stopped := ""
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if stopped != "" {
			if line == "::"+stopped+"::" {
				stopped = ""
			}
			continue
		}
		if m := stopCommands.FindStringSubmatch(line); m != nil {
			stopped = m[1]
			tokens = append(tokens, m[1])
			continue
		}
		if strings.HasPrefix(line, "::") {
			commands = append(commands, line)
		}
	}
	return commands, tokens
}

// TestReleaseNotesLogRunsNoCommands runs a dry run's notes step on a fragment
// whose lines are workflow commands: the step prints the notes with commands
// stopped by a random token, so none of them runs, not even one that tries to
// resume commands itself.
func TestReleaseNotesLogRunsNoCommands(t *testing.T) {
	script := preflightScript(t, "Release notes from docs/changelog.md")
	hostile := []string{
		"::set-output name=notes::../../etc/passwd",
		"  ::add-mask::hide-the-logs",
		"::error::a fake failure",
		"::stop-commands::mine",
		"::mine::",
		"::warning::after a guessed resume",
	}
	run := func(t *testing.T) (string, []string, []string) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "docs", "changelog.d"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "docs", "changelog.md"), []byte(changelogFixture), 0o600))
		fragment := "---\nsection: Fixed\n---\n\n- **A fix** (#1) — text.\n\n" + strings.Join(hostile, "\n") + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "docs", "changelog.d", "1-hostile.md"), []byte(fragment), 0o600))
		out, got, err := runReleaseStep(t, dir, script, "VERSION=v0.0.0-dryrun.1", "DRY_RUN=true")
		require.NoError(t, err, out)
		assert.Equal(t, map[string]string{"notes": "release-notes/notes.md"}, got, "the fragment set no output")
		notes := readNotes(t, dir)
		for _, h := range hostile {
			require.Contains(t, notes, h, "the notes keep the fragment's text")
			require.Contains(t, out, h, "the log shows the notes")
		}
		commands, tokens := workflowCommands(out)
		return out, commands, tokens
	}

	out, commands, tokens := run(t)
	assert.Empty(t, commands, "the notes ran workflow commands:\n%s", out)
	require.Len(t, tokens, 1, "commands are stopped once, around the notes")
	assert.Regexp(t, `^[0-9a-f]{32}$`, tokens[0], "the token is 16 random bytes")
	assert.Contains(t, out, "::"+tokens[0]+"::", "commands resume after the notes")
	_, _, again := run(t)
	require.Len(t, again, 1)
	assert.NotEqual(t, tokens[0], again[0], "each run has its own token")
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
			"version":           "${{ steps.version.outputs.version }}",
			"chart-version":     "${{ steps.version.outputs.chart-version }}",
			"prerelease":        "${{ steps.version.outputs.prerelease }}",
			"make-latest":       "${{ steps.version.outputs.make-latest }}",
			"image-tags":        "${{ steps.version.outputs.image-tags }}",
			"render-image-tags": "${{ steps.version.outputs.render-image-tags }}",
		}, preflight.Outputs, "the notes go as an artifact, not a job output")
		assert.Equal(t, map[string]string{"VERSION": "${{ steps.version.outputs.version }}"},
			preflight.step(t, "notes").Env)
	})

	t.Run("releases do not run at once", func(t *testing.T) {
		wf := readReleaseWorkflow(t)
		assert.Equal(t, "${{ github.event_name == 'push' && 'release-publish' || format('release-dry-run-{0}', github.ref) }}", wf.Concurrency.Group,
			"every tag push shares one group, so two releases cannot race latest; dry runs queue per ref")
		assert.False(t, wf.Concurrency.CancelInProgress, "a release is never cancelled by the next one")
	})

	t.Run("preflight runs first, read-only", func(t *testing.T) {
		assert.Empty(t, preflight.needs())
		assert.Equal(t, map[string]string{"contents": "read"}, preflight.Permissions)
		var names []string
		for _, s := range preflight.Steps {
			names = append(names, s.Name)
		}
		require.Len(t, names, 7)
		assert.True(t, strings.HasPrefix(preflight.Steps[0].Uses, "actions/checkout@"))
		assert.Equal(t, "Check that the tagged commit is on main or its maintenance branch", names[1])
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
		assert.NotContains(t, gh.With, "body", "the body is the file the Release body step writes")
		assert.Equal(t, "${{ steps.body.outputs.path }}", gh.With["body_path"])
	})

	t.Run("the notes travel as the release-notes artifact", func(t *testing.T) {
		var up, down releaseStep
		for _, s := range preflight.Steps {
			if strings.HasPrefix(s.Uses, "actions/upload-artifact@") {
				up = s
			}
		}
		for _, s := range jobs["release"].Steps {
			if strings.HasPrefix(s.Uses, "actions/download-artifact@") && s.With["name"] == "release-notes" {
				down = s
			}
		}
		require.NotEmpty(t, up.Uses, "preflight uploads the notes")
		assert.Equal(t, "release-notes", up.With["name"])
		assert.Equal(t, "release-notes/notes.md", up.With["path"])
		assert.Equal(t, "error", up.With["if-no-files-found"])
		require.NotEmpty(t, down.Uses, "the release job downloads the notes")
		assert.Equal(t, "release-notes/", down.With["path"])
		for name, job := range jobs {
			for _, s := range job.Steps {
				assert.NotContains(t, s.Run+fmt.Sprint(s.With), "outputs.notes", "%s: step %q reads the notes as an output", name, s.Name)
			}
		}
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

// releaseBodyScript is the release job's "Release body" script.
func releaseBodyScript(t *testing.T) string {
	t.Helper()
	s := releaseJobs(t)["release"].step(t, "body")
	require.NotEmpty(t, s.Run)
	return s.Run
}

// TestReleaseBody runs the release job's body script: the finished body has
// the title, the notes as they are, the install section with the version
// and --force-conflicts on the CRD apply; missing or empty notes fail the
// release.
func TestReleaseBody(t *testing.T) {
	script := releaseBodyScript(t)
	env := []string{"VERSION=v0.11.0", "CHART_VERSION=0.11.0"}
	run := func(t *testing.T, notes *string) (string, string, map[string]string, error) {
		dir := t.TempDir()
		if notes != nil {
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "release-notes"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "release-notes", "notes.md"), []byte(*notes), 0o600))
		}
		out, got, err := runReleaseStep(t, dir, script, env...)
		body, _ := os.ReadFile(filepath.Join(dir, "release-body.md"))
		return out, string(body), got, err
	}

	t.Run("notes, then install", func(t *testing.T) {
		// Big, with code blocks, "##" lines and characters a template
		// could eat (|, &, \, @VERSION@-like text).
		notes := "### Highlights\n\n- a | b & c \\ d, and a literal @VERSION@ and @CHART_VERSION@\n\n```bash\n## not a heading\nkubectl apply -f x\n```\n" +
			strings.Repeat("- **item** — a long line of notes text\n", 3000)
		out, body, got, err := run(t, &notes)
		require.NoError(t, err, out)
		assert.Equal(t, "release-body.md", got["path"])
		require.Greater(t, len(body), 100_000, "a body over 100 KB comes through whole")
		assert.True(t, strings.HasPrefix(body, "## kardinal-promoter v0.11.0\n\n"+notes), "the title, then the notes as they are")
		_, install, ok := strings.Cut(body, "### Install")
		require.True(t, ok, "the body has an install section")
		assert.Contains(t, install, "--version 0.11.0")
		assert.Contains(t, install, "| kubectl apply --server-side --force-conflicts -f -")
		assert.Contains(t, install, "releases/download/v0.11.0/kardinal-linux-amd64")
		assert.NotContains(t, install, "@VERSION@", "the template's placeholders are filled")
		assert.NotContains(t, install, "@CHART_VERSION@")
		assert.NotContains(t, body, "@NOTES@")
		assert.NotContains(t, body, "${{")
	})
	t.Run("missing notes fail", func(t *testing.T) {
		out, _, _, err := run(t, nil)
		require.Error(t, err)
		assert.Contains(t, out, "release notes from preflight")
	})
	t.Run("empty notes fail", func(t *testing.T) {
		empty := "  \n\n"
		out, _, _, err := run(t, &empty)
		require.Error(t, err)
		assert.Contains(t, out, "missing or empty")
	})
}

// TestReleaseUpgradeGuidePinned runs the preflight step that refuses a final
// release while docs/upgrade.md still describes the upgrade to it with the
// --version <version> placeholder or the "not released yet" note, and checks
// that a prerelease skips it.
func TestReleaseUpgradeGuidePinned(t *testing.T) {
	var step releaseStep
	for _, s := range releaseJobs(t)["preflight"].Steps {
		if strings.HasPrefix(s.Name, "The upgrade guide names a released version") {
			step = s
		}
	}
	require.NotEmpty(t, step.Run, "release.yml preflight has the upgrade-guide step")
	assert.Equal(t, "steps.version.outputs.prerelease == 'false'", strings.TrimSpace(fmt.Sprint(step.If)))
	run := func(t *testing.T, guide string) (string, error) {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "docs"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "docs", "upgrade.md"), []byte(guide), 0o600))
		out, _, err := runReleaseStep(t, dir, step.Run, "GITHUB_REF_NAME=v0.12.0")
		return out, err
	}
	for name, guide := range map[string]string{
		"placeholder": "## Upgrading from v0.11.0\n\n```bash\nhelm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version <version>\n```\n",
		"note":        "## Upgrading from v0.11.0\n\n!!! note \"v0.12.0 is not released yet\"\n",
	} {
		t.Run(name+" fails", func(t *testing.T) {
			out, err := run(t, guide)
			require.Error(t, err, out)
			assert.Contains(t, out, "Replace them with 0.12.0 before tagging")
		})
	}
	t.Run("a pinned guide passes", func(t *testing.T) {
		out, err := run(t, "## Upgrading from v0.11.0\n\n```bash\nhelm upgrade kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.0\n```\n")
		require.NoError(t, err, out)
	})
}

// TestReleaseDryRun: a workflow_dispatch of release.yml is a dry run. The
// release job, which publishes, runs only on a tag push. The dry-run job has
// a read-only token, and only the steps of an allowlist: the release job's
// build steps, unchanged but for pushing nothing, and an upload of what it
// built. No checkout keeps the token. Its version must be a prerelease that
// says dryrun (and a tag must not), and its notes are the unreleased entries.
func TestReleaseDryRun(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "release.yml"))
	require.NoError(t, err)
	text := string(data)
	assert.Contains(t, text, "workflow_dispatch:")
	assert.Contains(t, text, "DRY_RUN: ${{ github.event_name == 'workflow_dispatch' }}")

	var wf struct {
		Jobs map[string]struct {
			If          string            `json:"if"`
			Permissions map[string]string `json:"permissions"`
			Steps       []releaseStep     `json:"steps"`
		} `json:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &wf))
	assert.Equal(t, "github.event_name == 'push'", wf.Jobs["release"].If, "only a tag publishes")
	dry := wf.Jobs["dry-run"]
	assert.Equal(t, "github.event_name == 'workflow_dispatch'", dry.If)
	assert.Equal(t, map[string]string{"contents": "read"}, dry.Permissions, "a dry run's token can write nothing")

	for job, j := range wf.Jobs {
		for _, s := range j.Steps {
			if strings.HasPrefix(s.Uses, "actions/checkout@") {
				assert.Equal(t, false, s.With["persist-credentials"], "%s: no checkout keeps the token", job)
			}
		}
	}

	byName := map[string]releaseStep{}
	for _, s := range wf.Jobs["release"].Steps {
		byName[s.Name] = s
	}
	allowedUses := []string{"actions/checkout@", "actions/setup-go@", "docker/setup-buildx-action@", "docker/build-push-action@", "actions/download-artifact@", "actions/upload-artifact@"}
	publish := regexp.MustCompile(`\b(docker|helm|git|oras|crane) push\b|gh release|git tag|cosign|gh api -X (POST|PUT|PATCH|DELETE)`)
	built := 0
	for _, s := range dry.Steps {
		if s.Uses != "" {
			ok := false
			for _, u := range allowedUses {
				ok = ok || strings.HasPrefix(s.Uses, u)
			}
			assert.True(t, ok, "dry run step %q uses %s, which is not on the dry run's allowlist", s.Name, s.Uses)
		}
		assert.NotRegexp(t, publish, s.Run, "dry run step %q publishes", s.Name)
		// A build step is the release job's step, as the tag runs it.
		name := s.Name
		if strings.HasPrefix(s.Uses, "docker/build-push-action@") {
			assert.Equal(t, false, s.With["push"], "%s pushes nothing", s.Name)
			name = strings.Replace(s.Name, "Build ", "Build and push ", 1)
		}
		if rel, ok := byName[name]; ok && name != "" {
			built++
			assert.Equal(t, rel.Run, s.Run, "%s: the dry run runs the release job's script", s.Name)
			for k, v := range rel.With {
				if k != "push" {
					assert.Equal(t, v, s.With[k], "%s: with.%s as in the release job", s.Name, k)
				}
			}
		} else if s.Name != "" && s.Name != "Upload what the dry run built" {
			t.Errorf("dry run step %q is neither a release job step nor the upload", s.Name)
		}
	}
	assert.Equal(t, 8, built, "the binaries, both images, the chart and the release body, as the release job builds them")

	// Nothing a dispatch runs can reach a credential (QA on #1935). Only
	// preflight, test and the dry run run on a dispatch; every other job
	// runs on a tag push only. No job calls a reusable workflow or passes it
	// secrets. And no ${{ }} of a dispatch job, or of the workflow env, names
	// secrets or the job token in any form (secrets.X, secrets['X'],
	// toJSON(secrets), github.token, github['token']).
	var raw struct {
		Env  map[string]any            `json:"env"`
		Jobs map[string]map[string]any `json:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &raw))
	expr := regexp.MustCompile(`\$\{\{(.*?)\}\}`)
	credential := regexp.MustCompile(`secrets|github\s*(\.\s*token|\[)`)
	exposes := func(v any) []string {
		out, err := yaml.Marshal(v)
		require.NoError(t, err)
		var bad []string
		for _, m := range expr.FindAllStringSubmatch(string(out), -1) {
			if credential.MatchString(m[1]) {
				bad = append(bad, m[0])
			}
		}
		return bad
	}
	assert.Empty(t, exposes(raw.Env), "the workflow env")
	onDispatch := map[string]bool{"preflight": true, "test": true, "dry-run": true}
	for name, j := range raw.Jobs {
		assert.NotContains(t, j, "uses", "job %s calls a reusable workflow", name)
		assert.NotContains(t, j, "secrets", "job %s passes secrets", name)
		cond, _ := j["if"].(string)
		switch {
		case name == "dry-run":
			assert.Equal(t, "github.event_name == 'workflow_dispatch'", cond)
		case onDispatch[name]:
			assert.Empty(t, cond, "job %s runs on a tag and on a dispatch", name)
		default:
			assert.Equal(t, "github.event_name == 'push'", cond, "job %s is not on the dispatch allowlist: it must run on a tag push only", name)
		}
		if onDispatch[name] {
			assert.Empty(t, exposes(j), "job %s runs on a dispatch: no secret and no job token", name)
		}
	}

	for _, s := range releaseJobs(t)["preflight"].Steps {
		switch s.Name {
		case "No unreleased changelog fragments":
			assert.Equal(t, "env.DRY_RUN != 'true'", fmt.Sprint(s.If), "%s: a dry run builds a branch with fragments", s.Name)
		case "Check that the tagged commit is on main or its maintenance branch":
			// It runs on a dry run too: from release/vX.Y it checks the
			// branch's ancestry (TestReleaseMaintenanceBranch).
			assert.Nil(t, s.If, s.Name)
		}
	}

	script := preflightScript(t, "Set version and release channel from tag")
	out, _, err := runReleaseStep(t, repoRoot(t), script, "GITHUB_REF_NAME=v0.12.0-dryrun.1")
	require.Error(t, err, "a pushed tag that says dryrun")
	assert.Contains(t, out, "a dry run is a dispatch of this workflow, never a tag")
	for _, tt := range []struct {
		version string
		wantErr bool
	}{{"v0.12.0-dryrun.1", false}, {"v0.12.0-rc.1", true}, {"v0.12.0", true}, {"", true}} {
		t.Run("version "+tt.version, func(t *testing.T) {
			out, got, err := runReleaseStep(t, repoRoot(t), script, "DRY_RUN=true", "DISPATCH_VERSION="+tt.version, "GITHUB_REF_NAME=feat/x")
			if tt.wantErr {
				require.Error(t, err, out)
				assert.Contains(t, out, "a dry run's version must be a prerelease with dryrun in it")
				return
			}
			require.NoError(t, err, out)
			assert.Equal(t, "true", got["prerelease"])
			assert.Equal(t, "false", got["make-latest"], "a dry run is never latest")
		})
	}

	// The notes of a made-up version: the unreleased entries.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "docs", "changelog.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docs", "changelog.md"), []byte("# Changelog\n\n## [Unreleased]\n\n"+
		"<!-- changelog.d: the unreleased entries are the files in docs/changelog.d (CONTRIBUTING.md, Changelog) -->\n\n"+
		"### Fixed\n\n- **Old style** — x\n\n## [v0.11.0] — 2026-10-12\n\n- old\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docs", "changelog.d", "12-x.md"),
		[]byte("---\nsection: Added\n---\n\n- **Fragment entry** (#12) — y\n"), 0o600))
	out, _, err = runReleaseStep(t, dir, preflightScript(t, "Release notes from docs/changelog.md"), "VERSION=v0.12.0-dryrun.1", "DRY_RUN=true")
	require.NoError(t, err, out)
	notes, err := os.ReadFile(filepath.Join(dir, "release-notes", "notes.md"))
	require.NoError(t, err)
	assert.Contains(t, string(notes), "(dry run v0.12.0-dryrun.1: the unreleased entries)")
	assert.Contains(t, string(notes), "- **Old style** — x")
	assert.Contains(t, string(notes), "- **Fragment entry** (#12) — y")
	assert.NotContains(t, string(notes), "section: Added")
	assert.NotContains(t, string(notes), "- old", "not the last release's notes")

	// A real tag with no section still fails.
	_ = os.RemoveAll(filepath.Join(dir, "release-notes"))
	out, _, err = runReleaseStep(t, dir, preflightScript(t, "Release notes from docs/changelog.md"), "VERSION=v0.12.0")
	require.Error(t, err, out)
}
