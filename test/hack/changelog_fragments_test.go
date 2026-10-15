// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/hack/changelog/fragments"
)

// TestChangelogFragments: every docs/changelog.d fragment is well formed,
// the changelog keeps the marker the docs build fills, and no entry is
// written under [Unreleased] itself: unreleased entries are fragments, so
// two pull requests never edit the same lines of docs/changelog.md.
func TestChangelogFragments(t *testing.T) {
	root := repoRoot(t)
	_, err := fragments.Read(root)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, fragments.Changelog))
	require.NoError(t, err)
	assert.Contains(t, string(data), "## [Unreleased]\n\n"+fragments.Marker+"\n\n## [",
		"[Unreleased] holds only the marker; write the entry as a docs/changelog.d fragment (CONTRIBUTING.md, Changelog)")
}

// TestChangelogHookRendersAsTheTool: the docs build's hook
// (hack/docs-changelog/changelog_fragments.py) lists the fragments exactly
// as hack/changelog writes them at a release, for this repo's fragments and
// for an ordering fixture.
func TestChangelogHookRendersAsTheTool(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("needs python3")
	}
	root := repoRoot(t)
	fixture := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(fixture, fragments.Dir), 0o755))
	for name, body := range map[string]string{
		"1000-big.md": "---\nsection: Added\n---\n- **Big** — b\n",
		"99-b.md":     "---\nsection: Added\n---\n- **B** — b\n",
		"99-a.md":     "---\nsection: Development\n---\n- **A** — a\n  continued\n",
		"7.md":        "---\nsection: Upgrade\n---\n- **Read first** — u\n",
		"README.md":   "not a fragment\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(fixture, fragments.Dir, name), []byte(body), 0o600))
	}
	for _, dir := range []string{root, fixture} {
		fs, err := fragments.Read(dir)
		require.NoError(t, err)
		cmd := exec.Command(python, "-I", "-c",
			"import sys; sys.path.insert(0, sys.argv[1]); import changelog_fragments as c; "+
				"sys.stdout.write(c.render(c.read_fragments(sys.argv[2])))",
			filepath.Join(root, "hack", "docs-changelog"), filepath.Join(dir, fragments.Dir))
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.Equal(t, fragments.Render(fs), string(out), dir)
	}
}
