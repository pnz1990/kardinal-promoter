// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pipeIntoGrep matches "| grep <options>" and captures the options.
var pipeIntoGrep = regexp.MustCompile(`\|\s*grep((?:\s+-[A-Za-z-]+)*)`)

// quietGrepPipes returns the lines of a shell script that pipe into grep -q.
// grep -q exits on its first match; a writer that still has output then dies
// of SIGPIPE (exit 141), and under pipefail the whole check fails.
func quietGrepPipes(script string) []string {
	var out []string
	joined := strings.ReplaceAll(script, "\\\n", " ")
	for _, line := range strings.Split(joined, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, m := range pipeIntoGrep.FindAllStringSubmatch(line, -1) {
			for _, opt := range strings.Fields(m[1]) {
				if opt == "--quiet" || opt == "--silent" || (!strings.HasPrefix(opt, "--") && strings.Contains(opt, "q")) {
					out = append(out, strings.TrimSpace(line))
				}
			}
		}
	}
	return out
}

func TestQuietGrepPipesFindsOnlyGrepQ(t *testing.T) {
	script := "a | grep -q x\nb | grep -Eqi x\nc |\\\n  grep --quiet x\nd | grep -i x >/dev/null\n# e | grep -q x\ngrep -q x <<<\"$f\"\n"
	assert.Equal(t, []string{"a | grep -q x", "b | grep -Eqi x", "c |   grep --quiet x"}, quietGrepPipes(script))
}

// TestScriptsDoNotPipeIntoGrepQ checks the demo, hack and scripts/ shell
// scripts, which run under pipefail or are sourced by scripts that do.
func TestScriptsDoNotPipeIntoGrepQ(t *testing.T) {
	root := repoRoot(t)
	var files []string
	for _, pattern := range []string{"demo/scripts/*.sh", "hack/*.sh", "scripts/*.sh"} {
		m, err := filepath.Glob(filepath.Join(root, pattern))
		require.NoError(t, err)
		files = append(files, m...)
	}
	require.NotEmpty(t, files)
	for _, p := range files {
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		rel, err := filepath.Rel(root, p)
		require.NoError(t, err)
		for _, line := range quietGrepPipes(string(data)) {
			t.Errorf("%s: %q pipes into grep -q; use grep ... >/dev/null so the writer is not killed by SIGPIPE", rel, line)
		}
	}
}
