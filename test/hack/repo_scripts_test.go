// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runWithFakes runs `bash <script> args...` with the given fake executables
// first on PATH.
func runWithFakes(t *testing.T, script string, fakes map[string]string, env []string, args ...string) (string, error) {
	t.Helper()
	bin := t.TempDir()
	for name, body := range fakes {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755))
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = repoRoot(t)
	cmd.Env = append([]string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"KUBECONFIG=" + os.DevNull,
	}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const fakeGo = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FAKE_LOG"
exit 0
`

func TestDemoValidatePassesRunFilterOnce(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "go.log")
	out, err := runWithFakes(t, filepath.Join(repoRoot(t), "scripts", "demo-validate.sh"),
		map[string]string{"go": fakeGo}, []string{"FAKE_LOG=" + logPath}, "-run", "Flux")
	require.NoError(t, err, out)

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	var testCall string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "test ") {
			testCall = l
		}
	}
	require.NotEmpty(t, testCall, "go test was not called")
	assert.Contains(t, testCall, "-run Flux")
	assert.NotContains(t, testCall, "-run -run")
}

func TestDemoValidateFailsWhenAnAdapterHasTooFewTests(t *testing.T) {
	// A copy of the script in a fake repo whose pkg/health has one test per adapter.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "pkg", "health"), 0o755))
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "demo-validate.sh"))
	require.NoError(t, err)
	script := filepath.Join(root, "scripts", "demo-validate.sh")
	require.NoError(t, os.WriteFile(script, src, 0o755))
	var tests strings.Builder
	for _, p := range []string{"TestDeploymentAdapter_", "TestArgoCDAdapter_", "TestFluxAdapter_",
		"TestArgoRolloutsAdapter_", "TestFlaggerAdapter_"} {
		tests.WriteString("func " + p + "One(t *testing.T) {}\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "health", "health_test.go"),
		[]byte(tests.String()), 0o644))

	out, err := runWithFakes(t, script, map[string]string{"go": fakeGo},
		[]string{"FAKE_LOG=" + filepath.Join(t.TempDir(), "go.log")})
	require.Error(t, err, out)
	assert.Contains(t, out, "minimum 3 required")
}

func TestWorkflowBashSyntaxValidator(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		body     string
		wantFail bool
	}{
		{
			name:     "workflow run block with a missing fi fails",
			file:     "wf.yml",
			body:     "on: push\njobs:\n  j:\n    runs-on: x\n    steps:\n      - run: |\n          if true; then\n            echo hi\n",
			wantFail: true,
		},
		{
			name:     "composite action run block with a missing fi fails",
			file:     "action.yml",
			body:     "runs:\n  using: composite\n  steps:\n    - shell: bash\n      run: |\n        if true; then\n          echo hi\n",
			wantFail: true,
		},
		{
			name: "python step is not checked as bash",
			file: "py.yml",
			body: "on: push\njobs:\n  j:\n    runs-on: x\n    steps:\n      - shell: python\n        run: |\n          if True:\n            print('hi')\n",
		},
		{
			name: "valid workflow passes",
			file: "ok.yml",
			body: "on: push\njobs:\n  j:\n    runs-on: x\n    steps:\n      - run: |\n          if true; then echo hi; fi\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), tt.file)
			require.NoError(t, os.WriteFile(f, []byte(tt.body), 0o644))
			out, err := runWithFakes(t, filepath.Join(repoRoot(t), "scripts", "validate-workflow-bash-syntax.sh"),
				nil, nil, f)
			if tt.wantFail {
				require.Error(t, err, out)
				assert.Contains(t, out, "syntax error")
			} else {
				require.NoError(t, err, out)
			}
		})
	}

	t.Run("repository workflows and actions pass", func(t *testing.T) {
		out, err := runWithFakes(t, filepath.Join(repoRoot(t), "scripts", "validate-workflow-bash-syntax.sh"), nil, nil)
		require.NoError(t, err, out)
		assert.Contains(t, out, "All run: blocks are syntactically valid")
	})
}

// fakeSimulate is a kardinal CLI whose policy simulate output is chosen by
// the --time argument. The gate rows use the real CLI's format: tabwriter
// pads with spaces, and a blocking gate is BLOCK (cmd/kardinal/cmd/policy.go).
const fakeSimulate = `#!/usr/bin/env bash
case "$*" in
  *Saturday*) printf 'RESULT: BLOCKED\nBlocked by: no-weekend-deploys\n\nno-weekend-deploys:   BLOCK   (weekend)\nno-bot-deploys:       PASS    (ok)\n' ;;
  *Tuesday*) printf '%b' "$FAKE_TUESDAY" ;;
esac
`

func TestDemoValidateScenarioSelectionAndMatching(t *testing.T) {
	tests := []struct {
		name     string
		tuesday  string
		wantFail bool
	}{
		{
			name:    "weekend gate passes on Tuesday while another gate blocks",
			tuesday: `RESULT: BLOCKED\nBlocked by: require-uat-soak\n\nno-weekend-deploys:   PASS    (weekday)\nrequire-uat-soak:     BLOCK   (soak)\n`,
		},
		{
			name:     "weekend gate blocks on Tuesday; another gate's PASS must not count",
			tuesday:  `RESULT: BLOCKED\nBlocked by: no-weekend-deploys\n\nno-weekend-deploys:   BLOCK   (weekend)\nno-bot-deploys:       PASS    (ok)\n`,
			wantFail: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			kardinal := filepath.Join(bin, "kardinal")
			require.NoError(t, os.WriteFile(kardinal, []byte(fakeSimulate), 0o755))
			out, err := runWithFakes(t, filepath.Join(repoRoot(t), "demo", "scripts", "validate.sh"),
				map[string]string{"kubectl": "#!/usr/bin/env bash\nexit 0\n", "curl": "#!/usr/bin/env bash\nexit 7\n"},
				[]string{"KARDINAL=" + kardinal, "FAKE_TUESDAY=" + tt.tuesday},
				"--fast", "--scenario", "5")
			assert.Contains(t, out, "Scenario 5")
			assert.NotContains(t, out, "Scenario 1:")
			if tt.wantFail {
				require.Error(t, err, out)
				assert.Contains(t, out, "1 failed")
			} else {
				require.NoError(t, err, out)
				assert.Contains(t, out, "2 passed")
			}
		})
	}
}

// TestGitignoreDoesNotHideSources checks that .gitignore matches no tracked
// file and no new file under cmd/. The root binary patterns once matched the
// cmd/kardinal and cmd/kardinal-controller source directories.
func TestGitignoreDoesNotHideSources(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := repoRoot(t)
	ls := exec.Command("git", "ls-files", "-ci", "--exclude-standard")
	ls.Dir = root
	out, err := ls.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Empty(t, strings.TrimSpace(string(out)), "tracked files matched by .gitignore")

	for _, p := range []string{"cmd/kardinal/cmd/new.go", "cmd/kardinal-controller/new.go", "cmd/kardinal-agent/new.go"} {
		ci := exec.Command("git", "check-ignore", "-v", "--no-index", p)
		ci.Dir = root
		out, err := ci.CombinedOutput()
		// git check-ignore exits 1 when the path is not ignored.
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr, "%s is ignored: %s", p, out)
		assert.Equal(t, 1, exitErr.ExitCode(), string(out))
	}
}
