// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/hack/changelog/fragments"
)

func TestRun(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, fragments.Dir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, fragments.Dir, "5-x.md"), []byte("---\nsection: Fixed\n---\n- **X** (#5) — x\n"), 0o600))
	changelog := "# Changelog\n\n## [Unreleased]\n\n" + fragments.Marker + "\n\n## [v0.1.0] — 2026-01-01\n\n### Added\n\n- **Y** — y\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, fragments.Changelog), []byte(changelog), 0o600))

	var out bytes.Buffer
	require.NoError(t, run(root, []string{"lint"}, &out))
	assert.Equal(t, "1 fragments in docs/changelog.d\n", out.String())
	out.Reset()
	require.NoError(t, run(root, []string{"unreleased"}, &out))
	assert.Equal(t, "### Fixed\n\n- **X** (#5) — x\n", out.String())
	out.Reset()
	require.NoError(t, run(root, []string{"page"}, &out))
	assert.Contains(t, out.String(), "## [Unreleased]\n\n### Fixed\n\n- **X** (#5) — x\n\n## [v0.1.0]")

	out.Reset()
	require.NoError(t, run(root, []string{"release", "v0.2.0", "2026-02-02"}, &out))
	assert.Contains(t, out.String(), "wrote 1 fragments into docs/changelog.md as v0.2.0")
	data, err := os.ReadFile(filepath.Join(root, fragments.Changelog))
	require.NoError(t, err)
	assert.Contains(t, string(data), "## [Unreleased]\n\n"+fragments.Marker+"\n\n## [v0.2.0] — 2026-02-02\n\n### Fixed\n\n- **X** (#5) — x\n\n## [v0.1.0]")
	assert.NoFileExists(t, filepath.Join(root, fragments.Dir, "5-x.md"), "the fragment is deleted")
	assert.Error(t, run(root, []string{"release", "v0.3.0"}, &out), "nothing left to release")

	for _, args := range [][]string{nil, {"nope"}, {"release"}, {"check"}} {
		assert.Error(t, run(root, args, &out), "%v", args)
	}
}
