// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGoFuzz is a go that, fuzzing (-fuzz), prints $FAKE_OUT, writes
// $FAKE_NEW when set and exits $FAKE_RC; running the seed corpus (no -fuzz)
// logs it and exits $FAKE_SEED_RC.
const fakeGoFuzz = `#!/usr/bin/env bash
for a in "$@"; do
  if [ "$a" = -fuzz ]; then
    cat "$FAKE_OUT"
    [ -n "${FAKE_NEW:-}" ] && mkdir -p "$(dirname "$FAKE_NEW")" && echo 'go test fuzz v1' > "$FAKE_NEW"
    exit "$FAKE_RC"
  fi
done
echo "seeds $*" >> "$FAKE_LOG"
exit "${FAKE_SEED_RC:-0}"
`

// The output of the deadline race: no failing input, the target failed with
// "context deadline exceeded" when -fuzztime ended (CI run 38863914714).
const deadlineRace = `fuzz: elapsed: 18s, execs: 8240 (956/sec), new interesting: 35 (total: 195)
fuzz: elapsed: 21s, execs: 9509 (475/sec), new interesting: 42 (total: 202)
--- FAIL: FuzzYAMLUpdate (20.68s)
    context deadline exceeded
FAIL
exit status 1
FAIL	github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps	20.708s
`

// A real finding: a failing input written.
const crasher = `fuzz: minimizing 69-byte failing input file
--- FAIL: FuzzYAMLUpdate (79.55s)
    --- FAIL: FuzzYAMLUpdate (0.00s)
        fuzz_editors_internal_test.go:303:
            	Error:      	Should be true

    Failing input written to testdata/fuzz/FuzzYAMLUpdate/7d73243629f082c3
    To re-run:
    go test -run=FuzzYAMLUpdate/7d73243629f082c3
FAIL
`

// jsonLines is out as go test -json writes it: one output event per line.
func jsonLines(out string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(out, "\n") {
		if line == "" {
			continue
		}
		raw, _ := json.Marshal(map[string]string{"Action": "output", "Package": "p", "Test": "FuzzYAMLUpdate", "Output": line})
		b.Write(raw)
		b.WriteString("\n")
	}
	return b.String()
}

// TestFuzzScript (#2078) runs hack/ci/fuzz.sh against a fake go: Go's
// fuzzing deadline race passes as the seed corpus's result; a failing input
// written, another failure, or the race's message with a failing input
// written still fail.
func TestFuzzScript(t *testing.T) {
	const pkg, target = "./pkg/steps/steps", "FuzzYAMLUpdate"
	for _, tc := range []struct {
		name        string
		out         string
		rc, seedRC  string
		writesInput bool
		json        bool
		pass        bool
		seeds       bool
	}{
		{name: "the deadline race passes on its seed corpus", out: deadlineRace, rc: "1", pass: true, seeds: true},
		{name: "the deadline race, -json", out: jsonLines(deadlineRace), rc: "1", json: true, pass: true, seeds: true},
		{name: "a failing input, -json", out: jsonLines(crasher), rc: "1", json: true, writesInput: true},
		{name: "the deadline race with failing seeds fails", out: deadlineRace, rc: "1", seedRC: "1", pass: false, seeds: true},
		{name: "a failing input fails", out: crasher, rc: "1", writesInput: true},
		{name: "the race's message with a failing input written fails", out: deadlineRace, rc: "1", writesInput: true},
		{name: "another failure fails", out: "--- FAIL: FuzzYAMLUpdate (3.00s)\n    fuzzing process hung or terminated unexpectedly: exit status 2\nFAIL\n", rc: "1"},
		{name: "another target's failure fails", out: strings.ReplaceAll(deadlineRace, "FAIL: FuzzYAMLUpdate", "FAIL: FuzzOther") + "--- FAIL: FuzzYAMLUpdate (1s)\n", rc: "1"},
		{name: "a pass passes without the seed run", out: "PASS\nok\n", rc: "0", pass: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, bin := t.TempDir(), t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(fakeGoFuzz), 0o755))
			corpus := filepath.Join(dir, "pkg", "steps", "steps", "testdata", "fuzz", target)
			require.NoError(t, os.MkdirAll(corpus, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(corpus, "seed1"), []byte("go test fuzz v1\n"), 0o600))
			outFile, logFile := filepath.Join(t.TempDir(), "out"), filepath.Join(t.TempDir(), "calls")
			if tc.seedRC == "" {
				tc.seedRC = "0"
			}
			require.NoError(t, os.WriteFile(outFile, []byte(tc.out), 0o600))
			env := []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + t.TempDir(),
				"FAKE_OUT=" + outFile, "FAKE_LOG=" + logFile, "FAKE_RC=" + tc.rc, "FAKE_SEED_RC=" + tc.seedRC}
			if tc.writesInput {
				env = append(env, "FAKE_NEW="+filepath.Join(corpus, "7d73243629f082c3"))
			}
			args := []string{filepath.Join(repoRoot(t), "hack", "ci", "fuzz.sh"), pkg, target, "20s"}
			if tc.json {
				args = append(args, filepath.Join(t.TempDir(), "tests.json"))
			}
			cmd := exec.Command("bash", args...)
			cmd.Dir, cmd.Env = dir, env
			out, err := cmd.CombinedOutput()
			if tc.pass {
				assert.NoError(t, err, "%s", out)
			} else {
				assert.Error(t, err, "%s", out)
			}
			calls, _ := os.ReadFile(logFile)
			if tc.seeds {
				assert.Contains(t, string(calls), "seeds test "+pkg+" -run ^"+target+"$ -count=1", "the seed corpus runs: %s", out)
			} else {
				assert.Empty(t, string(calls), "no seed run: %s", out)
			}
		})
	}
}
