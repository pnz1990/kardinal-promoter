// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// docker knows the fake clusters' nodes: ps lists them, inspect finds them.
// FAKE_DOCKER_DOWN makes docker info fail, FAKE_DOCKER_ERR every inspect.
// Its tools are the system's (command -p): a run's failing tools are not
// docker's.
const localLiveDocker = `#!/usr/bin/env bash
f=$FAKE/clusters; command -p touch "$f"
case "$1" in
  info) [ -z "${FAKE_DOCKER_DOWN:-}" ] ;;
  ps) command -p sed 's/$/-control-plane/' "$f" ;;
  inspect)
    if [ -n "${FAKE_DOCKER_ERR:-}" ]; then echo "permission denied while trying to connect to the Docker daemon socket" >&2; exit 1; fi
    command -p grep -qx "${2%-control-plane}" "$f" || { echo "Error: No such object: $2" >&2; exit 1; } ;;
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
  echo "AT=$(date +%s%N)"
  echo "HOME=$HOME"
  [ -e "$HOME/.kube/config" ] && echo "HOMEKUBE=yes"
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
{ echo "RUN_KUBECONFIG=$KUBECONFIG"; echo "RUN_HOME=$HOME"; [ -e "$HOME/.kube/config" ] && echo "RUN_HOMEKUBE=yes"; } >>"$FAKE/run"
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
	for name, body := range map[string]string{"bin/kind": localLiveKind, "bin/gh": localLiveGh, "bin/docker": localLiveDocker,
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
		"KIND_CLUSTER=", "KEEP=", "AGENT_BADGE=", "RETRY=0.05", "OWNER=test", "AGENT_ID=", "SLOT_MAX=", "DRAIN_DEADLINE=",
		"LOCAL_LIVE_E2E_DIR="+filepath.Join(h.dir, "e2e"), "FAKE="+h.fake,
		"LOADLOCK="+h.lock, "RELEASE_GATE_SLOTS="+h.slots,
		"LOCK_UNDER_TEST="+h.lock, "SLOTS_UNDER_TEST="+h.slots,
		// An idle host with room for clusters, unless a test says otherwise.
		"LOCAL_LIVE_TEST=1", "LOCAL_LIVE_TEST_LOAD=0", "LOCAL_LIVE_TEST_INOTIFY=8192", "LOCAL_LIVE_TEST_SPACING=0")
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
	t.Run("a copy outside a checkout runs an older checkout's suites itself, with the loadlock.sh next to it", func(t *testing.T) {
		h := newLocalLiveHost(t)
		shared := filepath.Join(t.TempDir(), "bin")
		require.NoError(t, os.MkdirAll(shared, 0o755))
		src, err := os.ReadFile(filepath.Join(repoRoot(t), "hack", "e2e", "local-live.sh"))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(shared, "local-live.sh"), src, 0o755))
		// The shared loadlock.sh: records that it ran, then runs CMD.
		require.NoError(t, os.WriteFile(filepath.Join(shared, "loadlock.sh"), []byte(`#!/usr/bin/env bash
# a counting lock: [-x] holds it alone (exclusive=1)
echo shared >>"$FAKE/loadlock"
echo "$*" >>"$FAKE/loadlock-args"
echo "KUBECONFIG=$KUBECONFIG" >>"$FAKE/loadlock-env"
while [ "$1" != -- ]; do shift; done
shift
exec "$@"
`), 0o755))
		// An older checkout: its local-live.sh has no LOCAL_LIVE_VERSION.
		old := t.TempDir()
		for _, f := range []string{"hack/e2e/matrix.txt", "hack/tool-versions.env"} {
			b, err := os.ReadFile(filepath.Join(repoRoot(t), f))
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(old, f)), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(old, f), b, 0o644))
		}
		require.NoError(t, os.WriteFile(filepath.Join(old, "hack", "e2e", "local-live.sh"), []byte("#!/usr/bin/env bash\necho old >>\"$FAKE/old\"\n"), 0o755))
		for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "older"}} {
			git := exec.Command("git", args...)
			git.Dir = old
			out, err := git.CombinedOutput()
			require.NoError(t, err, "git %v: %s", args, out)
		}
		// The caller's KUBECONFIG is a production-like file (#2244 QA): a
		// tool run before the run's own kubeconfig is set must not see it.
		prod := filepath.Join(t.TempDir(), "prod-kubeconfig")
		require.NoError(t, os.WriteFile(prod, []byte("production\n"), 0o600))
		cmd := h.cmd(t, []string{"KUBECONFIG=" + prod}, "scale")
		cmd.Args[1] = filepath.Join(shared, "local-live.sh")
		cmd.Dir = old
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.Equal(t, "KUBECONFIG=/dev/null\n", h.read("loadlock-env"), "the lock, run before the cluster exists, sees no kubeconfig")
		assert.NotContains(t, h.read("up"), prod)
		assert.NotContains(t, h.read("run"), prod)
		assert.Empty(t, h.read("old"), "the older checkout's own script is not run")
		assert.Equal(t, "shared\n", h.read("loadlock"), "the heavy suite ran under the shared loadlock.sh")
		assert.Contains(t, h.read("loadlock-args"), " -x ", "a heavy suite holds a counting lock alone")
		assert.Contains(t, h.read("up"), "KIND_K8S=1.37", "the checkout's matrix: scale's minor")

		cmd = h.cmd(t, nil, "-n", "core")
		cmd.Args[1] = filepath.Join(shared, "local-live.sh")
		cmd.Dir = t.TempDir() // not a checkout
		out, err = cmd.CombinedOutput()
		assert.Equal(t, 2, exitCode(err), "%s", out)
		assert.Contains(t, string(out), "run it from a kardinal-promoter checkout")
	})
	t.Run("a copy outside a checkout hands off to a checkout's local-live.sh at least as new", func(t *testing.T) {
		h := newLocalLiveHost(t)
		shared := filepath.Join(t.TempDir(), "bin")
		require.NoError(t, os.MkdirAll(shared, 0o755))
		src, err := os.ReadFile(filepath.Join(repoRoot(t), "hack", "e2e", "local-live.sh"))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(shared, "local-live.sh"), src, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(shared, "loadlock.sh"), []byte("#!/usr/bin/env bash\necho shared >>\"$FAKE/loadlock\"\nexit 9\n"), 0o755))
		cmd := h.cmd(t, nil, "scale")
		cmd.Args[1] = filepath.Join(shared, "local-live.sh")
		cmd.Dir = repoRoot(t) // this checkout: the same version
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.Empty(t, h.read("loadlock"), "the checkout's own script ran, with its own loadlock.sh")
		assert.Contains(t, h.read("up"), "LOCKED=yes")
	})
	t.Run("a copy outside a checkout never hands off to a copy that does not keep the run off the user's kubeconfig", func(t *testing.T) {
		src, err := os.ReadFile(filepath.Join(repoRoot(t), "hack", "e2e", "local-live.sh"))
		require.NoError(t, err)
		require.Contains(t, string(src), "\nLOCAL_LIVE_VERSION=3\n")
		require.Contains(t, string(src), "\nLOCAL_LIVE_ISOLATION=kubeconfig,home\n")
		for _, tc := range []struct{ name, own string }{
			// a89b89b6f: version 2, no scratch HOME, no top KUBECONFIG=/dev/null.
			{"version 2, as a89b89b6f", "LOCAL_LIVE_VERSION=2\n"},
			{"as new, without the isolation line", "LOCAL_LIVE_VERSION=3\n"},
			{"newer, without the isolation line", "LOCAL_LIVE_VERSION=9\n"},
			{"newer, with another isolation", "LOCAL_LIVE_VERSION=9\nLOCAL_LIVE_ISOLATION=kubeconfig\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := newLocalLiveHost(t)
				shared := filepath.Join(t.TempDir(), "bin")
				require.NoError(t, os.MkdirAll(shared, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(shared, "local-live.sh"), src, 0o755))
				co := t.TempDir()
				for _, f := range []string{"hack/e2e/matrix.txt", "hack/tool-versions.env", "hack/loadlock.sh"} {
					b, err := os.ReadFile(filepath.Join(repoRoot(t), f))
					require.NoError(t, err)
					require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(co, f)), 0o755))
					require.NoError(t, os.WriteFile(filepath.Join(co, f), b, 0o755))
				}
				require.NoError(t, os.WriteFile(filepath.Join(co, "hack", "e2e", "local-live.sh"),
					[]byte("#!/usr/bin/env bash\n"+tc.own+"echo own >>\"$FAKE/own\"\n"), 0o755))
				for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "co"}} {
					git := exec.Command("git", args...)
					git.Dir = co
					out, err := git.CombinedOutput()
					require.NoError(t, err, "git %v: %s", args, out)
				}
				cmd := h.cmd(t, nil, "gitea")
				cmd.Args[1] = filepath.Join(shared, "local-live.sh")
				cmd.Dir = co
				out, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", out)
				assert.Empty(t, h.read("own"), "the checkout's copy is not run")
				assert.Contains(t, h.read("run"), "RUN_HOME="+filepath.Join(h.state, "kardinal-e2e-ll-test-gitea-37", "runs"), "the shared copy ran it, with a scratch HOME")
				if strings.Contains(tc.own, "=9") || strings.Contains(tc.own, "=3") {
					assert.Contains(t, string(out), "does not keep the run off the user's kubeconfig")
				}
			})
		}
	})
	t.Run("a waiting heavy suite drains the host: no new run starts, and it starts with one other slot in use", func(t *testing.T) {
		h := newLocalLiveHost(t)
		// Two other runs hold slots (live records: this test's own pid).
		hold := func(n string) {
			require.NoError(t, os.MkdirAll(filepath.Join(h.slots, n), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(h.slots, n, "owner"), []byte(fmt.Sprintf("other run pid %d now\n", os.Getpid())), 0o644))
		}
		hold("3")
		hold("4")
		heavy := h.cmd(t, nil, "scale")
		hout := &liveOut{}
		heavy.Stdout, heavy.Stderr = hout, hout
		require.NoError(t, heavy.Start())
		require.Eventually(t, func() bool { return strings.Contains(hout.String(), "a heavy suite starts with at most 1") }, 10*time.Second, 20*time.Millisecond)
		assert.FileExists(t, filepath.Join(h.slots, ".drain"))
		light := h.cmd(t, nil, "gitea")
		lout := &liveOut{}
		light.Stdout, light.Stderr = lout, lout
		require.NoError(t, light.Start())
		require.Eventually(t, func() bool { return strings.Contains(lout.String(), "a heavy suite is draining the host") }, 10*time.Second, 20*time.Millisecond)
		require.NoError(t, os.RemoveAll(filepath.Join(h.slots, "4"))) // one other run ends
		require.NoError(t, heavy.Wait(), "%s", hout.String())
		require.NoError(t, light.Wait(), "%s", lout.String())
		up := h.read("up")
		require.Equal(t, 2, strings.Count(up, "KIND_K8S="), "%s", up)
		runs := strings.Split(up, "KUBECONFIG=")
		require.Len(t, runs, 3)
		assert.Contains(t, runs[1], "-scale-", "the heavy suite started first")
		assert.Contains(t, runs[2], "-gitea-")
		assert.NoFileExists(t, filepath.Join(h.slots, ".drain"), "the marker goes with the heavy run")
	})
	t.Run("a heavy suite waits for the load to be under 16", func(t *testing.T) {
		h := newLocalLiveHost(t)
		cmd := h.cmd(t, []string{"LOCAL_LIVE_TEST_LOAD=20"}, "scale")
		out := &liveOut{}
		cmd.Stdout, cmd.Stderr = out, out
		require.NoError(t, cmd.Start())
		require.Eventually(t, func() bool { return strings.Contains(out.String(), "the one-minute load is 20, not under 16") }, 10*time.Second, 20*time.Millisecond)
		assert.Empty(t, h.read("up"))
		require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
		_ = cmd.Wait()
		assert.NoFileExists(t, filepath.Join(h.slots, ".drain"))
	})
	t.Run("two runs at once start their clusters at least the spacing apart", func(t *testing.T) {
		h := newLocalLiveHost(t)
		var cmds []*exec.Cmd
		for _, suite := range []string{"gitea", "flux"} {
			c := h.cmd(t, []string{"LOCAL_LIVE_TEST_SPACING=2", "LOCAL_LIVE_TEST_LOAD_RETRY=0.2"}, suite)
			require.NoError(t, c.Start())
			cmds = append(cmds, c)
		}
		for _, c := range cmds {
			require.NoError(t, c.Wait())
		}
		var at []int64
		for _, l := range strings.Split(h.read("up"), "\n") {
			if v, ok := strings.CutPrefix(l, "AT="); ok {
				n, err := strconv.ParseInt(v, 10, 64)
				require.NoError(t, err)
				at = append(at, n)
			}
		}
		require.Len(t, at, 2)
		gap := time.Duration(at[1] - at[0])
		if gap < 0 {
			gap = -gap
		}
		assert.GreaterOrEqual(t, gap, 1900*time.Millisecond, "starts 2s apart (up.sh runs a moment after its start is recorded)")
	})
	t.Run("a slot whose owner is gone is reclaimed; a kept cluster's only when the cluster is gone", func(t *testing.T) {
		h := newLocalLiveHost(t)
		gone := exec.Command("true")
		require.NoError(t, gone.Run())
		slot := func(n, owner, cluster string) {
			require.NoError(t, os.MkdirAll(filepath.Join(h.slots, n), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(h.slots, n, "owner"), []byte(owner+"\n"), 0o644))
			if cluster != "" {
				require.NoError(t, os.WriteFile(filepath.Join(h.slots, n, "cluster"), []byte(cluster+"\n"), 0o644))
			}
		}
		slot("1", fmt.Sprintf("dead run pid %d now", gone.Process.Pid), "")
		slot("2", fmt.Sprintf("reused pid pid %d now boot=not-this-boot start=1", os.Getpid()), "")
		slot("3", "KEEP kept-gone, kept by someone", "kardinal-e2e-kept-gone")
		slot("4", "KEEP kept-here, kept by someone", "kardinal-e2e-kept-here")
		require.NoError(t, os.WriteFile(filepath.Join(h.fake, "clusters"), []byte("kardinal-e2e-kept-here\n"), 0o644))
		out, err := h.cmd(t, nil, "gitea").CombinedOutput()
		require.NoError(t, err, "%s", out)
		for _, n := range []string{"1", "2", "3"} {
			assert.Contains(t, string(out), "reclaimed cluster slot "+filepath.Join(h.slots, n))
		}
		assert.DirExists(t, filepath.Join(h.slots, "4"), "a kept cluster that is there keeps its slot")
	})
	t.Run("children see the run's kind kubeconfig, never ~/.kube/config", func(t *testing.T) {
		h := newLocalLiveHost(t)
		home := t.TempDir() // the user's: a kubeconfig of another cluster
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".kube"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(home, ".kube", "config"), []byte("production\n"), 0o600))
		// The caller's KUBECONFIG unset, and set to the production file.
		for _, kc := range []string{"", filepath.Join(home, ".kube", "config")} {
			require.NoError(t, os.RemoveAll(h.fake))
			require.NoError(t, os.MkdirAll(h.fake, 0o755))
			out, err := h.cmd(t, []string{"HOME=" + home, "KUBECONFIG=" + kc}, "gitea").CombinedOutput()
			require.NoError(t, err, "%s", out)
			kubeconfig := filepath.Join(h.state, "kardinal-e2e-ll-test-gitea-37", "kubeconfig")
			up, run := h.read("up"), h.read("run")
			assert.Contains(t, up, "KUBECONFIG="+kubeconfig+"\n", "up.sh (caller KUBECONFIG=%q)", kc)
			assert.Contains(t, run, "RUN_KUBECONFIG="+kubeconfig+"\n", "run.sh and its test binary")
			assert.NotContains(t, up, "HOME="+home+"\n", "a scratch HOME")
			assert.NotContains(t, up, "HOMEKUBE=yes", "no ~/.kube/config under the scratch HOME")
			assert.NotContains(t, run, "RUN_HOMEKUBE=yes")
			assert.NotContains(t, run, "RUN_HOME="+home+"\n")
		}
	})
	t.Run("a heavy suite gives up draining after the deadline, naming what blocked it", func(t *testing.T) {
		h := newLocalLiveHost(t)
		for n, owner := range map[string]string{"3": fmt.Sprintf("other run pid %d now", os.Getpid()), "4": "KEEP kept-here, kept by someone"} {
			require.NoError(t, os.MkdirAll(filepath.Join(h.slots, n), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(h.slots, n, "owner"), []byte(owner+"\n"), 0o644))
		}
		require.NoError(t, os.WriteFile(filepath.Join(h.slots, "4", "cluster"), []byte("kardinal-e2e-kept-here\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(h.fake, "clusters"), []byte("kardinal-e2e-kept-here\n"), 0o644))
		start := time.Now()
		// Its own timeout (#2244 QA): a run that never gives up fails this
		// test in seconds, not at the package's timeout.
		cmd := h.cmd(t, []string{"DRAIN_DEADLINE=1"}, "scale")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		var timedOut atomic.Bool
		timer := time.AfterFunc(30*time.Second, func() { timedOut.Store(true); _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
		out, err := cmd.CombinedOutput()
		timer.Stop()
		require.False(t, timedOut.Load(), "the heavy run never gave up draining: %s", out)
		assert.Equal(t, 1, exitCode(err), "%s", out)
		assert.Less(t, time.Since(start), 15*time.Second)
		assert.Contains(t, string(out), "gave up draining the host after 1s")
		assert.Contains(t, string(out), "kept cluster kardinal-e2e-kept-here holds slot 4; delete it or run with -reuse kardinal-e2e-kept-here")
		assert.Contains(t, string(out), "slot 3 is held by other run pid")
		assert.Contains(t, string(out), "the one-minute load is")
		assert.NoFileExists(t, filepath.Join(h.slots, ".drain"), "the marker is given back")
		assert.NoDirExists(t, h.lock, "and the heavy-load lock")
		assert.Empty(t, h.read("up"))
	})
	t.Run("a kept cluster's slot is reclaimed only on a definite no such object", func(t *testing.T) {
		for _, tc := range []struct {
			name, env, warn string
		}{
			{"docker does not answer", "FAKE_DOCKER_DOWN=1", "docker does not answer"},
			{"docker inspect fails otherwise", "FAKE_DOCKER_ERR=1", "cannot tell whether cluster kardinal-e2e-kept-gone"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := newLocalLiveHost(t)
				require.NoError(t, os.MkdirAll(filepath.Join(h.slots, "1"), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(h.slots, "1", "owner"), []byte("KEEP kept-gone, kept by someone\n"), 0o644))
				require.NoError(t, os.WriteFile(filepath.Join(h.slots, "1", "cluster"), []byte("kardinal-e2e-kept-gone\n"), 0o644))
				out, err := h.cmd(t, []string{tc.env}, "gitea").CombinedOutput()
				require.NoError(t, err, "%s", out)
				assert.Contains(t, string(out), tc.warn)
				assert.DirExists(t, filepath.Join(h.slots, "1"), "the slot stays")
			})
		}
	})
	t.Run("a SIGKILLed KEEP run's slot stays while its cluster runs", func(t *testing.T) {
		h := newLocalLiveHost(t)
		gone := exec.Command("true")
		require.NoError(t, gone.Run())
		require.NoError(t, os.MkdirAll(filepath.Join(h.slots, "1"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(h.slots, "1", "owner"), []byte(fmt.Sprintf("killed run pid %d now\n", gone.Process.Pid)), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(h.slots, "1", "cluster"), []byte("kardinal-e2e-still-here\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(h.fake, "clusters"), []byte("kardinal-e2e-still-here\n"), 0o644))
		out, err := h.cmd(t, nil, "gitea").CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.Contains(t, string(out), "its cluster kardinal-e2e-still-here still runs; the slot stays")
		assert.DirExists(t, filepath.Join(h.slots, "1"))
	})
	t.Run("slot directories above SLOT_MAX count: no run starts while SLOT_MAX clusters run", func(t *testing.T) {
		h := newLocalLiveHost(t)
		for _, n := range []string{"3", "4"} { // two runs with a larger SLOT_MAX
			require.NoError(t, os.MkdirAll(filepath.Join(h.slots, n), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(h.slots, n, "owner"), []byte(fmt.Sprintf("other run pid %d now\n", os.Getpid())), 0o644))
		}
		cmd := h.cmd(t, nil, "gitea")
		out := &liveOut{}
		cmd.Stdout, cmd.Stderr = out, out
		require.NoError(t, cmd.Start())
		require.Eventually(t, func() bool { return strings.Contains(out.String(), "2 cluster slots are in use") }, 10*time.Second, 20*time.Millisecond)
		assert.Contains(t, out.String(), "and the host has 2", "SLOT_MAX's default")
		assert.Empty(t, h.read("up"))
		require.NoError(t, os.RemoveAll(filepath.Join(h.slots, "4")))
		require.NoError(t, cmd.Wait(), "%s", out.String())
		assert.Contains(t, h.read("up"), "SLOT=1\n", "slot 1, beside the other run's 3")
	})
	t.Run("a run whose cat, grep, cut, awk, sed and find fail takes no unsafe answer", func(t *testing.T) {
		// #2242 QA's rule, for the slots: the tools fail while the run takes
		// its slots and waits for room (LOCAL_LIVE_TEST_FAIL_PATH), as forks
		// do on a busy host. Before it, a failed $(cat ...) made a live run's
		// slot look gone, and a drain marker look absent.
		failing := t.TempDir()
		for _, tool := range []string{"cat", "grep", "cut", "awk", "sed", "find"} {
			require.NoError(t, os.WriteFile(filepath.Join(failing, tool),
				[]byte("#!/bin/sh\necho \"bash: fork: Resource temporarily unavailable\" >&2\nexit 1\n"), 0o755))
		}
		// And forks that fail now and then: a cat that reads a slot's owner
		// but fails on /proc and on a slot's cluster file (the script before
		// this fix then judged the live run gone and removed its slot).
		flaky := t.TempDir()
		realCat, err := exec.LookPath("cat")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(flaky, "cat"), []byte(`#!/bin/sh
case "$*" in /proc/*|*/cluster) echo "bash: fork: Resource temporarily unavailable" >&2; exit 1 ;; esac
exec `+realCat+` "$@"
`), 0o755))
		boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		require.NoError(t, err)
		stat, err := os.ReadFile("/proc/self/stat")
		require.NoError(t, err)
		f := strings.Fields(string(stat)[strings.LastIndexByte(string(stat), ')')+1:])
		require.Greater(t, len(f), 19)
		live := fmt.Sprintf("other run pid %d now boot=%s start=%s", os.Getpid(), strings.TrimSpace(string(boot)), f[19])
		// slot is a live run's slot 1, its cluster running; drain a live
		// heavy run's marker. Each returns what to remove to unblock.
		slot := func(h *localLiveHost) string {
			d := filepath.Join(h.slots, "1")
			require.NoError(t, os.MkdirAll(d, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(d, "owner"), []byte(live+"\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(d, "cluster"), []byte("kardinal-e2e-other\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(h.fake, "clusters"), []byte("kardinal-e2e-other\n"), 0o644))
			return d
		}
		drain := func(h *localLiveHost) string {
			m := filepath.Join(h.slots, ".drain")
			require.NoError(t, os.WriteFile(m, []byte("E local-live scale 1.37 draining the host for a heavy suite, "+strings.TrimPrefix(live, "other run ")+"\n"), 0o644))
			return m
		}
		for _, tc := range []struct {
			name, why, fail string
			env             []string
			setup           func(h *localLiveHost) string
		}{
			{"a live run's slot (SLOT_MAX=1)", "cluster slots are in use", failing, []string{"SLOT_MAX=1"}, slot},
			{"a live run's slot, forks failing now and then", "cluster slots are in use", flaky, []string{"SLOT_MAX=1"}, slot},
			{"a live heavy run's drain marker", "a heavy suite is draining the host", failing, nil, drain},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := newLocalLiveHost(t)
				blocker := tc.setup(h)
				cmd := h.cmd(t, append(tc.env, "LOCAL_LIVE_TEST_FAIL_PATH="+tc.fail), "gitea")
				out := &liveOut{}
				cmd.Stdout, cmd.Stderr = out, out
				require.NoError(t, cmd.Start())
				if !assert.Eventually(t, func() bool { return strings.Contains(out.String(), tc.why) }, 10*time.Second, 20*time.Millisecond) {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatalf("it did not wait (%q): %s", tc.why, out.String())
				}
				time.Sleep(300 * time.Millisecond)
				assert.Empty(t, h.read("up"), "no cluster while it is blocked")
				assert.NotContains(t, out.String(), "reclaimed")
				assert.NotContains(t, out.String(), "removed the drain marker")
				require.NoError(t, os.RemoveAll(blocker))
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				select {
				case err := <-done:
					require.NoError(t, err, "%s", out.String())
				case <-time.After(30 * time.Second):
					_ = cmd.Process.Kill()
					<-done
					t.Fatalf("never started once unblocked: %s", out.String())
				}
				assert.Contains(t, h.read("up"), "KIND_K8S=1.37", "it runs once unblocked")
			})
		}
		t.Run("a load it cannot parse", func(t *testing.T) {
			h := newLocalLiveHost(t)
			cmd := h.cmd(t, []string{"LOCAL_LIVE_TEST_FAIL_PATH=" + failing, "LOCAL_LIVE_TEST_LOAD=busy"}, "gitea")
			out := &liveOut{}
			cmd.Stdout, cmd.Stderr = out, out
			require.NoError(t, cmd.Start())
			if !assert.Eventually(t, func() bool { return strings.Contains(out.String(), "the one-minute load is busy, not under 48") }, 10*time.Second, 20*time.Millisecond) {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("it did not wait: %s", out.String())
			}
			assert.Empty(t, h.read("up"))
			require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
			_ = cmd.Wait()
		})
		// #2244 QA: each protection below has a test that fails without it.
		// start runs gitea with env, and waits (5s at most) for want.
		start := func(t *testing.T, h *localLiveHost, want string, env ...string) (*exec.Cmd, *liveOut) {
			t.Helper()
			cmd := h.cmd(t, env, "gitea")
			out := &liveOut{}
			cmd.Stdout, cmd.Stderr = out, out
			require.NoError(t, cmd.Start())
			if !assert.Eventually(t, func() bool { return strings.Contains(out.String(), want) }, 5*time.Second, 20*time.Millisecond) {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("no %q: %s", want, out.String())
			}
			return cmd, out
		}
		t.Run("a flock that fails: no slot is taken without it", func(t *testing.T) {
			noFlock := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(noFlock, "flock"),
				[]byte("#!/bin/sh\necho \"bash: fork: Resource temporarily unavailable\" >&2\nexit 1\n"), 0o755))
			h := newLocalLiveHost(t)
			cmd, out := start(t, h, "cannot take the .reclaim flock", "LOCAL_LIVE_TEST_FAIL_PATH="+noFlock)
			time.Sleep(300 * time.Millisecond)
			entries, _ := os.ReadDir(h.slots)
			for _, e := range entries {
				assert.False(t, e.IsDir() && e.Name() != ".queue", "slot %s taken without the flock: %s", e.Name(), out.String())
			}
			assert.Empty(t, h.read("up"))
			require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
			_ = cmd.Wait()
		})
		if os.Geteuid() == 0 {
			return // root reads a mode-000 file
		}
		t.Run("a drain marker that cannot be read is a drain", func(t *testing.T) {
			h := newLocalLiveHost(t)
			marker := filepath.Join(h.slots, ".drain")
			require.NoError(t, os.WriteFile(marker, []byte(live+"\n"), 0o000))
			cmd, out := start(t, h, "a heavy suite is draining the host: (a marker that cannot be read)")
			assert.Empty(t, h.read("up"))
			assert.FileExists(t, marker, "and it is not removed")
			require.NoError(t, os.Remove(marker))
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				require.NoError(t, err, "%s", out.String())
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Fatalf("never started once the marker went: %s", out.String())
			}
			assert.Contains(t, h.read("up"), "KIND_K8S=1.37")
		})
		t.Run("a dead run's slot whose cluster file cannot be read stays", func(t *testing.T) {
			h := newLocalLiveHost(t)
			gone := exec.Command("true")
			require.NoError(t, gone.Run())
			d := filepath.Join(h.slots, "1")
			require.NoError(t, os.MkdirAll(d, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(d, "owner"), []byte(fmt.Sprintf("a SIGKILLed KEEP run pid %d now\n", gone.Process.Pid)), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(d, "cluster"), []byte("kardinal-e2e-kept\n"), 0o000))
			cmd, out := start(t, h, "cannot read cluster slot "+d+"'s cluster; the slot stays", "SLOT_MAX=1")
			assert.DirExists(t, d)
			assert.NotContains(t, out.String(), "reclaimed")
			assert.Empty(t, h.read("up"))
			require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
			_ = cmd.Wait()
		})
	})
	t.Run("a pid whose stat cannot be opened is alive while its /proc directory is there", func(t *testing.T) {
		// LOCAL_LIVE_TEST_PROC: a /proc where pid 424242 is there with no
		// stat to open (a full file table), and pid 424243 is not.
		h := newLocalLiveHost(t)
		proc := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(proc, "424242"), 0o755))
		for n, pid := range map[string]int{"1": 424242, "2": 424243} {
			d := filepath.Join(h.slots, n)
			require.NoError(t, os.MkdirAll(d, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(d, "owner"), []byte(fmt.Sprintf("a run pid %d now start=5\n", pid)), 0o644))
		}
		out, err := h.cmd(t, []string{"LOCAL_LIVE_TEST_PROC=" + proc, "SLOT_MAX=3"}, "gitea").CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.DirExists(t, filepath.Join(h.slots, "1"), "the pid whose /proc directory is there keeps its slot")
		assert.NotContains(t, string(out), "reclaimed cluster slot "+filepath.Join(h.slots, "1"))
		assert.Contains(t, string(out), "reclaimed cluster slot "+filepath.Join(h.slots, "2"), "the pid that is gone does not")
	})
	t.Run("a slot owner without a pid is never reclaimed", func(t *testing.T) {
		h := newLocalLiveHost(t)
		for _, n := range []string{"1", "2", "3", "4"} {
			require.NoError(t, os.MkdirAll(filepath.Join(h.slots, n), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(h.slots, n, "owner"), []byte("a person, by hand\n"), 0o644))
		}
		cmd := h.cmd(t, nil, "gitea")
		out := &liveOut{}
		cmd.Stdout, cmd.Stderr = out, out
		require.NoError(t, cmd.Start())
		require.Eventually(t, func() bool { return strings.Contains(out.String(), "4 cluster slots are in use") }, 10*time.Second, 20*time.Millisecond)
		require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
		_ = cmd.Wait()
		for _, n := range []string{"1", "2", "3", "4"} {
			assert.DirExists(t, filepath.Join(h.slots, n))
		}
	})
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
