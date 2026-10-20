// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localLiveHost is a fake host for hack/e2e/local-live.sh: stub up.sh,
// run.sh and tools.sh (LOCAL_LIVE_E2E_DIR), and kind and gh on PATH that
// record what they were asked, around a real lock and slots directory.
type localLiveHost struct {
	dir, lock, slots, state, fake string
}

const localLiveKind = `#!/usr/bin/env bash
f=$FAKE/clusters; touch "$f"
case "$1 $2" in
  "get clusters") cat "$f" ;;
  "delete cluster") grep -vx "$4" "$f" >"$f.tmp" || true; mv "$f.tmp" "$f"; echo "$4" >>"$FAKE/deleted" ;;
esac
`

const localLiveGh = `#!/usr/bin/env bash
echo "$*" >>"$FAKE/gh"
while [ $# -gt 0 ]; do [ "$1" = --body-file ] && cp "$2" "$FAKE/body"; shift; done
`

// up.sh records how it was run, and whether the lock and a slot were held.
const localLiveUp = `#!/usr/bin/env bash
set -eu
{
  echo "KUBECONFIG=$KUBECONFIG"
  echo "E2E_OUT=$E2E_OUT"
  echo "KIND_K8S=$KIND_K8S"
  echo "BUILD=$KARDINAL_E2E_BUILD"
  echo "REUSE=${KARDINAL_E2E_REUSE:-}"
  echo "SHARD=$SHARD"
  [ -d "$LOCK_UNDER_TEST" ] && echo "LOCKED=yes"
  ls "$SLOTS_UNDER_TEST" | sed 's/^/SLOT=/'
} >>"$FAKE/up"
if [ -z "${KARDINAL_E2E_REUSE:-}" ]; then
  echo "$KIND_CLUSTER" >>"$FAKE/clusters"
  mkdir -p "$E2E_OUT/secrets"
  echo token >"$E2E_OUT/secrets/forgejo-token"
  echo "export KARDINAL_E2E_SUITE=$1" >"$E2E_OUT/env"
fi
`

const localLiveRun = `#!/usr/bin/env bash
echo '{"suite":"'"$1"'","results":[{"test":"TestA","action":"pass"},{"test":"TestB","action":"pass"}]}' >"$E2E_OUT/summary.json"
echo "| TestA | pass |" >"$GITHUB_STEP_SUMMARY"
[ -z "${FAKE_SLEEP:-}" ] || sleep "$FAKE_SLEEP"
exit "${FAKE_RC:-0}"
`

func newLocalLiveHost(t *testing.T) *localLiveHost {
	t.Helper()
	dir := t.TempDir()
	h := &localLiveHost{dir: dir, lock: filepath.Join(dir, "LOADLOCK"), slots: filepath.Join(dir, "slots"),
		state: filepath.Join(dir, "e2e-state"), fake: filepath.Join(dir, "fake")}
	for _, d := range []string{h.fake, filepath.Join(dir, "bin"), filepath.Join(dir, "e2e"), h.slots} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	for name, body := range map[string]string{"bin/kind": localLiveKind, "bin/gh": localLiveGh,
		"e2e/up.sh": localLiveUp, "e2e/run.sh": localLiveRun, "e2e/tools.sh": "#!/usr/bin/env bash\n",
		// The core suite needs zsh on PATH; CI's runners have none.
		"bin/zsh": "#!/usr/bin/env bash\n"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755))
	}
	return h
}

func (h *localLiveHost) cmd(t *testing.T, env []string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), "hack", "e2e", "local-live.sh")}, args...)...)
	cmd.Env = append(os.Environ(),
		"PATH="+filepath.Join(h.dir, "bin")+":"+os.Getenv("PATH"),
		"KUBECONFIG=/dev/null", "KARDINAL_E2E_TOOLS=path", "KARDINAL_E2E_BUILD=", "KARDINAL_E2E_STATE=",
		"KIND_CLUSTER=", "KEEP=", "AGENT_BADGE=", "RETRY=0.05", "OWNER=test", "AGENT_ID=",
		"LOCAL_LIVE_E2E_DIR="+filepath.Join(h.dir, "e2e"), "FAKE="+h.fake,
		"LOADLOCK="+h.lock, "RELEASE_GATE_SLOTS="+h.slots,
		"LOCK_UNDER_TEST="+h.lock, "SLOTS_UNDER_TEST="+h.slots,
		// An idle host with room for clusters, unless a test says otherwise.
		"LOCAL_LIVE_TEST=1", "LOCAL_LIVE_TEST_LOAD=0", "LOCAL_LIVE_TEST_INOTIFY=8192")
	cmd.Env = append(cmd.Env, env...)
	return cmd
}

func (h *localLiveHost) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(h.fake, name))
	return string(b)
}

// TestLocalLive: hack/e2e/local-live.sh runs a suite as CI does (the host
// build by default), under the host's lock and a cluster slot, with its
// cluster's state in the shared state directory, prints the summary, posts
// it with -pr, and deletes the cluster and releases both however it ends.
func TestLocalLive(t *testing.T) {
	t.Run("a passing run: summary, comment, cleanup", func(t *testing.T) {
		h := newLocalLiveHost(t)
		out, err := h.cmd(t, nil, "-pr", "42", "-badge", "[BADGE]", "core").CombinedOutput()
		require.NoError(t, err, "%s", out)
		cluster := "kardinal-e2e-ll-test-core-35"
		up := h.read("up")
		e2eOut := filepath.Join(h.state, cluster)
		assert.Contains(t, up, "KUBECONFIG="+filepath.Join(e2eOut, "kubeconfig"), "the cluster's own kubeconfig, never the user's")
		assert.Contains(t, up, "E2E_OUT="+e2eOut, "the cluster's state is in the shared state directory")
		assert.Contains(t, up, "KIND_K8S=1.35", "the suite's first minor in matrix.txt")
		assert.Contains(t, up, "BUILD=host")
		assert.NotContains(t, up, "LOCKED=yes", "an ordinary suite does not take the heavy-load lock")
		assert.Contains(t, up, "SLOT=1", "only a cluster slot")
		assert.Contains(t, string(out), "local live: core on Kubernetes 1.35: PASS")
		assert.Contains(t, string(out), "2 pass")
		assert.Equal(t, cluster+"\n", h.read("deleted"))
		assert.Empty(t, strings.TrimSpace(h.read("clusters")))
		assert.NoDirExists(t, h.lock)
		assert.NoDirExists(t, filepath.Join(h.slots, "1"))
		assert.NoDirExists(t, filepath.Join(e2eOut, "secrets"), "a deleted cluster's credentials go with it")
		assert.Contains(t, h.read("gh"), "pr comment 42 --repo pnz1990/kardinal-promoter --body-file")
		body := h.read("body")
		assert.True(t, strings.HasPrefix(body, "[BADGE]\n"), "%s", body)
		assert.Contains(t, body, "| TestA | pass |", "run.sh's report")
		assert.Contains(t, body, "kind v", "the tool pins")
		runs, _ := filepath.Glob(filepath.Join(e2eOut, "runs", "*", "run.log"))
		assert.Len(t, runs, 1, "the run's log is kept")
	})
	t.Run("a failing run exits with the suite's status", func(t *testing.T) {
		h := newLocalLiveHost(t)
		out, err := h.cmd(t, []string{"FAKE_RC=3"}, "-shard", "2/3", "core", "1.37").CombinedOutput()
		assert.Equal(t, 3, exitCode(err), "%s", out)
		assert.Contains(t, h.read("up"), "SHARD=2/3")
		assert.Contains(t, string(out), "local live: core shard 2/3 on Kubernetes 1.37: FAIL")
		assert.Equal(t, "kardinal-e2e-ll-test-core-37\n", h.read("deleted"))
		assert.NoDirExists(t, h.lock)
		assert.Empty(t, h.read("gh"), "no -pr, no comment")
	})
	t.Run("KEEP, then -reuse rebuilds only the controller and cleans up", func(t *testing.T) {
		h := newLocalLiveHost(t)
		out, err := h.cmd(t, []string{"KEEP=1"}, "gitea").CombinedOutput()
		require.NoError(t, err, "%s", out)
		cluster := "kardinal-e2e-ll-test-gitea-37"
		assert.Empty(t, h.read("deleted"), "KEEP keeps the cluster")
		assert.FileExists(t, filepath.Join(h.state, cluster, "secrets", "forgejo-token"))
		assert.DirExists(t, filepath.Join(h.slots, "1"), "and its slot")
		assert.NoDirExists(t, h.lock)

		out, err = h.cmd(t, nil, "-reuse", cluster, "gitea").CombinedOutput()
		require.NoError(t, err, "%s", out)
		up := strings.Split(h.read("up"), "KUBECONFIG=")
		require.Len(t, up, 3)
		assert.Contains(t, up[2], "REUSE=1")
		assert.Contains(t, up[2], "KIND_K8S=1.37", "the minor the cluster was created on")
		assert.Contains(t, up[2], "SLOT=1\n")
		assert.NotContains(t, up[2], "SLOT=2", "the kept cluster's slot is taken over")
		assert.Equal(t, cluster+"\n", h.read("deleted"))
		assert.NoDirExists(t, filepath.Join(h.slots, "1"))

		out, err = h.cmd(t, nil, "-reuse", cluster, "gitea").CombinedOutput()
		assert.Equal(t, 2, exitCode(err), "a deleted cluster can't be reused: %s", out)
	})
	t.Run("a signal stops the suite, deletes the cluster, releases the lock and slot", func(t *testing.T) {
		h := newLocalLiveHost(t)
		cmd := h.cmd(t, []string{"FAKE_SLEEP=30"}, "core")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		require.NoError(t, cmd.Start())
		require.Eventually(t, func() bool { return strings.Contains(h.read("clusters"), "kardinal-e2e-ll-test-core-35") },
			20*time.Second, 50*time.Millisecond)
		start := time.Now()
		require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
		err := cmd.Wait()
		assert.NotEqual(t, 0, exitCode(err))
		assert.Less(t, time.Since(start), 15*time.Second, "the suite is stopped, not waited for")
		assert.Equal(t, "kardinal-e2e-ll-test-core-35\n", h.read("deleted"))
		assert.NoDirExists(t, h.lock)
		assert.NoDirExists(t, filepath.Join(h.slots, "1"))
	})
	t.Run("the owner names the cluster, so two agents' runs do not collide", func(t *testing.T) {
		h := newLocalLiveHost(t)
		for _, tc := range []struct {
			env  []string
			args []string
			want string
		}{
			{[]string{"AGENT_ID=ENGINEER-4"}, nil, "kardinal-e2e-ll-engineer-4-core-35"},
			{[]string{"AGENT_ID=ENGINEER-6"}, nil, "kardinal-e2e-ll-engineer-6-core-35"},
			{nil, []string{"-o", "QA-2 #2153 core"}, "kardinal-e2e-ll-qa-2-core-35"},
		} {
			out, err := h.cmd(t, tc.env, append(append([]string{"-n"}, tc.args...), "core")...).CombinedOutput()
			require.NoError(t, err, "%s", out)
			assert.Contains(t, string(out), "cluster  "+tc.want+"\n", "%v %v", tc.env, tc.args)
		}
	})
	t.Run("a heavy suite takes the heavy-load lock and a slot", func(t *testing.T) {
		h := newLocalLiveHost(t)
		out, err := h.cmd(t, nil, "scale").CombinedOutput()
		require.NoError(t, err, "%s", out)
		up := h.read("up")
		assert.Contains(t, up, "LOCKED=yes", "the scale suite runs under hack/loadlock.sh")
		assert.Contains(t, up, "SLOT=1")
		assert.NoDirExists(t, h.lock)
	})
	t.Run("a heavy suite refuses to run on a shared host without the lock", func(t *testing.T) {
		h := newLocalLiveHost(t)
		out, err := h.cmd(t, []string{"LOADLOCK="}, "scale").CombinedOutput()
		assert.Equal(t, 2, exitCode(err), "%s", out)
		assert.Contains(t, string(out), "LOADLOCK is not set")
		assert.Empty(t, h.read("up"))
	})
	t.Run("an ordinary suite runs without the lock set", func(t *testing.T) {
		h := newLocalLiveHost(t)
		out, err := h.cmd(t, []string{"LOADLOCK="}, "gitea").CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.NotContains(t, h.read("up"), "LOCKED=yes")
	})
	for _, tc := range []struct{ name, env, why string }{
		{"a busy host", "LOCAL_LIVE_TEST_LOAD=60", "the one-minute load is 60, not under 48"},
		{"too few inotify instances", "LOCAL_LIVE_TEST_INOTIFY=64", "fs.inotify.max_user_instances is 64"},
	} {
		t.Run("waits to create its cluster on "+tc.name, func(t *testing.T) {
			h := newLocalLiveHost(t)
			cmd := h.cmd(t, []string{tc.env}, "core")
			out := &liveOut{}
			cmd.Stdout, cmd.Stderr = out, out
			require.NoError(t, cmd.Start())
			require.Eventually(t, func() bool { return strings.Contains(out.String(), "waiting to create") }, 10*time.Second, 20*time.Millisecond)
			assert.Contains(t, out.String(), tc.why)
			assert.Empty(t, h.read("up"), "no cluster yet")
			require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
			_ = cmd.Wait()
			assert.NoDirExists(t, filepath.Join(h.slots, "1"), "its slot is given back")
		})
	}
	t.Run("an existing cluster is not taken over", func(t *testing.T) {
		h := newLocalLiveHost(t)
		require.NoError(t, os.WriteFile(filepath.Join(h.fake, "clusters"), []byte("kardinal-e2e-ll-test-core-35\n"), 0o644))
		out, err := h.cmd(t, nil, "core").CombinedOutput()
		assert.Equal(t, 2, exitCode(err), "%s", out)
		assert.Contains(t, string(out), "-reuse kardinal-e2e-ll-test-core-35")
		assert.Empty(t, h.read("deleted"), "another run's cluster is left alone")
		assert.NoDirExists(t, h.lock)
		assert.NoDirExists(t, filepath.Join(h.slots, "1"))
	})
}

// liveOut is a run's output, written by the run while the test reads it.
type liveOut struct {
	mu sync.Mutex
	b  strings.Builder
}

func (o *liveOut) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *liveOut) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}
