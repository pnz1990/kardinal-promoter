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

// TestScriptsDoNotPipeIntoGrepQ checks the demo and hack scripts, which run
// under pipefail or are sourced by scripts that do.
func TestScriptsDoNotPipeIntoGrepQ(t *testing.T) {
	root := repoRoot(t)
	var files []string
	for _, pattern := range []string{"demo/scripts/*.sh", "hack/*.sh"} {
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

// longOutputCLI prints the line each scenario 9 check looks for, then more
// output than a pipe buffer holds, as a real CLI can.
const longOutputCLI = `#!/usr/bin/env bash
case "$*" in
  version) echo "CLI: v0.7.0" ;;
  "get pipelines") echo "PIPELINE  kardinal-test-app" ;;
  "explain "*) echo "prod  gate  no-weekend-deploys" ;;
  "history "*) echo "Bundle  AGE" ;;
  "get steps "*) echo "STEP  PromotionStep" ;;
  "completion bash") ;;
  *--dry-run*) echo "dry run: would create a Bundle" ;;
  *) echo "unexpected call: $*" >&2; exit 1 ;;
esac
seq 1 100000
`

// TestDemoValidateSurvivesLongCLIOutput runs validate.sh scenario 9 against a
// CLI whose output continues after the matching line. A check that pipes into
// grep -q fails here the way it flaked in the Demo job.
func TestDemoValidateSurvivesLongCLIOutput(t *testing.T) {
	kardinal := filepath.Join(t.TempDir(), "kardinal")
	require.NoError(t, os.WriteFile(kardinal, []byte(longOutputCLI), 0o755))
	out, err := runWithFakes(t, filepath.Join(repoRoot(t), "demo", "scripts", "validate.sh"),
		map[string]string{"kubectl": "#!/usr/bin/env bash\nexit 0\n", "curl": "#!/usr/bin/env bash\nexit 7\n"},
		[]string{"KARDINAL=" + kardinal},
		"--fast", "--scenario", "9")
	require.NoError(t, err, out)
	assert.Contains(t, out, "7 passed")
	assert.Contains(t, out, "kardinal create bundle --dry-run works")
}
