// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package hack tests the hack/ cluster setup scripts without a cluster.
//
// Each test puts fake kubectl, helm, kind and docker binaries first on PATH.
// The fakes record their arguments and never contact anything, so the tests
// can check that the scripts only ever act on the kind context they own.
package hack

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakeKubectl = `#!/usr/bin/env bash
echo "kubectl $*" >> "$FAKE_LOG"
# Read piped input (kubectl apply -f -) so the writer never gets SIGPIPE.
for a in "$@"; do [ "$a" = - ] && cat >/dev/null; done
if [ "$1" = config ]; then
  case "$2" in
    current-context) echo "$FAKE_CURRENT_CONTEXT" ;;
    view)
      for a in "$@"; do
        case "$a" in
          jsonpath=*contexts*)
            if [ -n "$FAKE_KIND_CONTEXT" ] && [[ "$a" == *"\"$FAKE_KIND_CONTEXT\""* ]]; then
              echo "$FAKE_KIND_CONTEXT"
            fi
            ;;
          jsonpath=*clusters*) echo "$FAKE_SERVER" ;;
        esac
      done
      ;;
  esac
fi
exit 0
`

const fakeKind = `#!/usr/bin/env bash
echo "kind $*" >> "$FAKE_LOG"
if [ "$1" = get ] && [ "$2" = clusters ]; then
  echo "$FAKE_KIND_CLUSTERS"
fi
exit 0
`

const fakeRecorder = `#!/usr/bin/env bash
echo "$(basename "$0") $*" >> "$FAKE_LOG"
# Read piped input (kubectl apply -f -) so the writer never gets SIGPIPE.
for a in "$@"; do [ "$a" = - ] && cat >/dev/null; done
exit 0
`

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// runScript runs a hack/ script with fake tools and returns its combined
// output, the recorded tool invocations, and its error.
func runScript(t *testing.T, script string, env map[string]string, args ...string) (string, []string, error) {
	t.Helper()
	bin := t.TempDir()
	for name, body := range map[string]string{
		"kubectl": fakeKubectl, "kind": fakeKind, "helm": fakeRecorder, "docker": fakeRecorder,
		"terraform": fakeRecorder, "aws": fakeRecorder,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755))
	}
	logPath := filepath.Join(t.TempDir(), "calls.log")

	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), "hack", script)}, args...)...)
	cmd.Dir = repoRoot(t)
	cmd.Env = []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"KUBECONFIG=" + os.DevNull,
		"FAKE_LOG=" + logPath,
		"GITHUB_TOKEN=",
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()

	var calls []string
	if data, readErr := os.ReadFile(logPath); readErr == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if l != "" {
				calls = append(calls, l)
			}
		}
	}
	return string(out), calls, err
}

// clusterCalls drops the read-only kubeconfig lookups the guard makes.
func clusterCalls(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "kubectl config ") || strings.HasPrefix(c, "kind ") ||
			strings.HasPrefix(c, "docker ") {
			continue
		}
		out = append(out, c)
	}
	return out
}

func TestClusterSetupScriptsTargetOnlyTheirKindContext(t *testing.T) {
	for _, script := range []string{"setup-e2e-env.sh", "e2e-setup.sh"} {
		t.Run(script, func(t *testing.T) {
			out, calls, err := runScript(t, script, map[string]string{
				"FAKE_CURRENT_CONTEXT": "arn:aws:eks:us-east-2:111111111111:cluster/prod",
				"FAKE_KIND_CONTEXT":    "kind-kardinal-e2e",
				"FAKE_KIND_CLUSTERS":   "kardinal-e2e",
				"FAKE_SERVER":          "https://127.0.0.1:40123",
				"SKIP_BUILD":           "1",
				"SKIP_ARGOCD":          "1",
			})
			require.NoError(t, err, out)

			cc := clusterCalls(calls)
			require.NotEmpty(t, cc, "the script made no kubectl/helm calls")
			for _, c := range cc {
				switch {
				case strings.HasPrefix(c, "kubectl "):
					assert.Contains(t, c, "--context kind-kardinal-e2e", "kubectl call without the kind context")
				case strings.HasPrefix(c, "helm "):
					assert.Contains(t, c, "--kube-context kind-kardinal-e2e", "helm call without the kind context")
				}
			}
		})
	}
}

func TestClusterSetupScriptsRefuseNonKindTargets(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "kind context points at a remote API server",
			env: map[string]string{
				"FAKE_KIND_CONTEXT":  "kind-kardinal-e2e",
				"FAKE_KIND_CLUSTERS": "kardinal-e2e",
				"FAKE_SERVER":        "https://ABCDEF.gr7.us-east-2.eks.amazonaws.com",
			},
		},
		{
			name: "kind context missing from kubeconfig",
			env: map[string]string{
				"FAKE_KIND_CONTEXT":  "",
				"FAKE_KIND_CLUSTERS": "kardinal-e2e",
				"FAKE_SERVER":        "https://127.0.0.1:40123",
			},
		},
	}
	for _, script := range []string{"setup-e2e-env.sh", "e2e-setup.sh"} {
		for _, tt := range tests {
			t.Run(script+"/"+tt.name, func(t *testing.T) {
				env := map[string]string{
					"FAKE_CURRENT_CONTEXT": "arn:aws:eks:us-east-2:111111111111:cluster/prod",
					"SKIP_BUILD":           "1",
					"SKIP_ARGOCD":          "1",
				}
				for k, v := range tt.env {
					env[k] = v
				}
				out, calls, err := runScript(t, script, env)
				require.Error(t, err, "script must refuse: %s", out)
				assert.Contains(t, out, "ERROR: ")
				assert.Empty(t, clusterCalls(calls), "no kubectl/helm call may run before the guard")
			})
		}
	}
}

func TestKindContextGuardRejectsNonKindName(t *testing.T) {
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "kubectl"), []byte(fakeKubectl), 0o755))
	cmd := exec.Command("bash", "-c", `source hack/kind-context.sh && use_kind_context prod-cluster`)
	cmd.Dir = repoRoot(t)
	cmd.Env = []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_LOG=" + filepath.Join(t.TempDir(), "calls.log"),
		"KUBECONFIG=" + os.DevNull,
	}
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "not a kind-* context")
}

// TestDemoTeardownIgnoresRemovedEKSFlag: the demo's --eks mode and
// terraform/eks-e2e were removed (#1293). teardown.sh --eks used to run
// terraform destroy; now it warns, says how to destroy an old EKS cluster, and
// only deletes the kind clusters. An unknown flag is reported instead of
// ignored.
func TestDemoTeardownIgnoresRemovedEKSFlag(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantWarn []string
	}{
		{name: "--eks", args: []string{"--eks"},
			wantWarn: []string{"--eks was removed", "terraform/eks-e2e/"}},
		{name: "unknown flag", args: []string{"--bogus"},
			wantWarn: []string{"unknown flag: --bogus"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// runScript resolves the script under hack/.
			out, calls, err := runScript(t, filepath.Join("..", "demo", "scripts", "teardown.sh"),
				map[string]string{"FAKE_KIND_CLUSTERS": "kardinal-control"}, tc.args...)
			require.NoError(t, err, out)
			for _, w := range tc.wantWarn {
				assert.Contains(t, out, w)
			}
			assert.Contains(t, calls, "kind delete cluster --name kardinal-control")
			for _, c := range calls {
				assert.False(t, strings.HasPrefix(c, "terraform ") || strings.HasPrefix(c, "aws "),
					"teardown must not call terraform or aws: %s", c)
			}
		})
	}
}
