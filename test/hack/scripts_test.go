// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package hack tests the repository's scripts and workflows without a cluster.
//
// Script tests put fake kubectl, helm, kind and docker binaries first on PATH.
// The fakes record their arguments and never contact anything, so the tests
// can check that the scripts only ever act on the kind context they own.
package hack

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
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

// TestKindContextGuard runs use_kind_context (hack/kind-context.sh, which
// hack/e2e/lib.sh sources) against a fake kubeconfig. It accepts only a kind-*
// context whose API server is on the local host, and then points KUBECTL and
// HELM at that context.
func TestKindContextGuard(t *testing.T) {
	tests := []struct {
		name    string
		ctx     string
		env     []string
		wantErr string
	}{
		{name: "local kind context", ctx: "kind-kardinal-e2e",
			env: []string{"FAKE_KIND_CONTEXT=kind-kardinal-e2e", "FAKE_SERVER=https://127.0.0.1:40123"}},
		{name: "not a kind-* name", ctx: "prod-cluster",
			env:     []string{"FAKE_KIND_CONTEXT=prod-cluster", "FAKE_SERVER=https://127.0.0.1:40123"},
			wantErr: "not a kind-* context"},
		{name: "kind context points at a remote API server", ctx: "kind-kardinal-e2e",
			env:     []string{"FAKE_KIND_CONTEXT=kind-kardinal-e2e", "FAKE_SERVER=https://ABCDEF.gr7.us-east-2.eks.amazonaws.com"},
			wantErr: "is not local"},
		{name: "kind context missing from kubeconfig", ctx: "kind-kardinal-e2e",
			env:     []string{"FAKE_KIND_CONTEXT=", "FAKE_SERVER=https://127.0.0.1:40123"},
			wantErr: "not found in kubeconfig"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "kubectl"), []byte(fakeKubectl), 0o755))
			cmd := exec.Command("bash", "-c",
				`source hack/kind-context.sh && use_kind_context "$1" && echo "KUBECTL=${KUBECTL[*]}" && echo "HELM=${HELM[*]}"`,
				"guard", tt.ctx)
			cmd.Dir = repoRoot(t)
			cmd.Env = append([]string{
				"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"FAKE_LOG=" + filepath.Join(t.TempDir(), "calls.log"),
				"FAKE_CURRENT_CONTEXT=arn:aws:eks:us-east-2:111111111111:cluster/prod",
				"KUBECONFIG=" + os.DevNull,
			}, tt.env...)
			out, err := cmd.CombinedOutput()
			if tt.wantErr != "" {
				require.Error(t, err, string(out))
				assert.Contains(t, string(out), "ERROR: ")
				assert.Contains(t, string(out), tt.wantErr)
				assert.NotContains(t, string(out), "KUBECTL=")
				return
			}
			require.NoError(t, err, string(out))
			assert.Contains(t, string(out), "KUBECTL=kubectl --context kind-kardinal-e2e\n")
			assert.Contains(t, string(out), "HELM=helm --kube-context kind-kardinal-e2e\n")
		})
	}
}

// TestPodinfoPrePullIsTheFixtureImage: components/podinfo.sh pulls
// PODINFO_IMAGE onto the node so the fixtures' first release and the probe
// Pods start without pulling. It must be that image (fixtures.V1), and the
// chart and multi-cluster suites, the spoke included, must run it.
func TestPodinfoPrePullIsTheFixtureImage(t *testing.T) {
	cmd := exec.Command("bash", "-c", `source hack/e2e/versions.env && echo "$PODINFO_IMAGE"`)
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, strings.TrimSpace(string(out)),
		"PODINFO_IMAGE in hack/e2e/versions.env is the fixtures' first release")

	up, err := os.ReadFile(filepath.Join(repoRoot(t), "hack/e2e/up.sh"))
	require.NoError(t, err)
	for _, suite := range []string{"chart", "multi-cluster"} {
		assert.Regexp(t, `(?m)^\s*`+regexp.QuoteMeta(suite)+`\) COMPONENTS=\([^)]*\bpodinfo\.sh\b`, string(up),
			"suite %s pulls podinfo onto its node", suite)
	}
	spoke, err := os.ReadFile(filepath.Join(repoRoot(t), "hack/e2e/components/spoke.sh"))
	require.NoError(t, err)
	assert.Contains(t, string(spoke), `KIND_CLUSTER=$SPOKE bash "$E2E_DIR/components/podinfo.sh"`,
		"the spoke, where the multi-cluster fixtures run, gets podinfo too")
}

// TestKindClusterKeepsDefaultKubeconfig runs kind_cluster (hack/e2e/lib.sh),
// which up.sh and components/spoke.sh create their clusters with, against a
// fake kind. With KUBECONFIG unset it uses $E2E_OUT/kubeconfig, says so and
// exports it, and kind always gets an explicit --kubeconfig, so kind never
// writes the default ~/.kube/config. No other script in hack/e2e calls kind
// create cluster without --kubeconfig.
func TestKindClusterKeepsDefaultKubeconfig(t *testing.T) {
	out := t.TempDir()
	def := filepath.Join(out, "kubeconfig")
	tests := []struct {
		name, kubeconfig, clusters string
		wantCall, wantLog          string
		noCall                     []string
	}{
		{name: "unset, new cluster", wantLog: "KUBECONFIG is not set: using " + def,
			wantCall: "kind create cluster --name kardinal-e2e-x --config " + filepath.Join(repoRoot(t), "test/e2e/kind-config.yaml") +
				" --kubeconfig " + def + " --wait 120s"},
		{name: "unset, existing cluster", clusters: "kardinal-e2e-x", wantLog: "KUBECONFIG is not set: using " + def,
			wantCall: "kind export kubeconfig --name kardinal-e2e-x --kubeconfig " + def, noCall: []string{"kind create"}},
		{name: "set to a list, new cluster", kubeconfig: "/e2e/a:/e2e/b",
			wantCall: "--kubeconfig /e2e/a --wait 120s"},
		{name: "set, existing cluster", kubeconfig: "/e2e/a", clusters: "kardinal-e2e-x",
			noCall: []string{"kind create", "kind export"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "kind"), []byte(fakeKind), 0o755))
			logPath := filepath.Join(t.TempDir(), "calls.log")
			cmd := exec.Command("bash", "-c", `source hack/e2e/lib.sh && kind_cluster kardinal-e2e-x && echo "KUBECONFIG=$KUBECONFIG"`)
			cmd.Dir = repoRoot(t)
			cmd.Env = []string{
				"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"HOME=" + t.TempDir(),
				"FAKE_LOG=" + logPath,
				"FAKE_KIND_CLUSTERS=" + tt.clusters,
				"KIND_CLUSTER=kardinal-e2e-x",
				"E2E_OUT=" + out,
			}
			if tt.kubeconfig != "" {
				cmd.Env = append(cmd.Env, "KUBECONFIG="+tt.kubeconfig)
			}
			got, err := cmd.CombinedOutput()
			require.NoError(t, err, string(got))
			want := tt.kubeconfig
			if want == "" {
				want = def
			}
			assert.Contains(t, string(got), "KUBECONFIG="+want+"\n", "kind_cluster exports the kubeconfig it used")
			if tt.wantLog != "" {
				assert.Contains(t, string(got), tt.wantLog)
			} else {
				assert.NotContains(t, string(got), "KUBECONFIG is not set")
			}
			data, err := os.ReadFile(logPath)
			require.NoError(t, err)
			calls := string(data)
			if tt.wantCall != "" {
				assert.Contains(t, calls, tt.wantCall)
			}
			for _, c := range tt.noCall {
				assert.NotContains(t, calls, c)
			}
		})
	}

	create := regexp.MustCompile(`kind create cluster\b.*`)
	require.NoError(t, filepath.WalkDir(filepath.Join(repoRoot(t), "hack", "e2e"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sh") {
			return err
		}
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if m := create.FindString(line); m != "" {
				assert.Contains(t, m, "--kubeconfig", "%s:%d: kind create cluster without --kubeconfig writes the default kubeconfig",
					strings.TrimPrefix(p, repoRoot(t)+"/"), i+1)
			}
		}
		return nil
	}))
}

// TestE2EScriptsUseKindContext: every kubectl and helm call in hack/e2e goes
// through the KUBECTL and HELM arrays of hack/kind-context.sh, so a live suite
// never acts on the current kube context. A bare call is reported with its
// file and line.
func TestE2EScriptsUseKindContext(t *testing.T) {
	bare := regexp.MustCompile(`(^|[\s;|&(` + "`" + `])(kubectl|helm)\s`)
	var scripts []string
	require.NoError(t, filepath.WalkDir(filepath.Join(repoRoot(t), "hack", "e2e"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".sh") {
			scripts = append(scripts, p)
		}
		return err
	}))
	require.NotEmpty(t, scripts)
	for _, p := range scripts {
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		for i, line := range strings.Split(string(data), "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "#") {
				continue
			}
			assert.False(t, bare.MatchString(code), "%s:%d calls kubectl or helm without the kind context; use \"${KUBECTL[@]}\" or \"${HELM[@]}\": %s",
				strings.TrimPrefix(p, repoRoot(t)+"/"), i+1, code)
		}
	}
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
