// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadlock starts hack/loadlock.sh with lock and args.
func loadlock(t *testing.T, lock string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(repoRoot(t), "hack", "loadlock.sh"), "-l", lock, "-o", "test",
		"-r", "0.05", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "LOADLOCK=", "LOADLOCK_TEST=1", "LOADLOCK_TEST_LOAD=99") // a busy host: one holder at a time
	return cmd
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

// TestLoadLock: hack/loadlock.sh runs a command holding the heavy-load
// lock, waits while another holder has it, and removes it afterwards only
// if it is still its own (the 07:19 incident: a lock another run held was
// removed by a command chain whose mkdir had failed).
func TestLoadLock(t *testing.T) {
	t.Run("runs the command holding the lock, then removes it", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		out, err := loadlock(t, lock, "bash", "-c", `cat "$0/owner"; exit 3`, lock).CombinedOutput()
		assert.Equal(t, 3, exitCode(err), "the command's status: %s", out)
		assert.Contains(t, string(out), "test pid:", "the view names the holder (pid:N, which older clients do not parse)")
		assert.NoDirExists(t, lock)
	})
	t.Run("waits while another holder has it", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		require.NoError(t, os.Mkdir(lock, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(lock, "owner"), []byte("other\n"), 0o644))
		ran := filepath.Join(t.TempDir(), "ran")
		cmd := loadlock(t, lock, "touch", ran)
		require.NoError(t, cmd.Start())
		time.Sleep(500 * time.Millisecond)
		assert.NoFileExists(t, ran, "the command waits for the lock")
		owner, _ := os.ReadFile(filepath.Join(lock, "owner"))
		assert.Equal(t, "other\n", string(owner), "the other holder's lock is left alone")
		require.NoError(t, os.RemoveAll(lock))
		require.NoError(t, cmd.Wait())
		assert.FileExists(t, ran)
		assert.NoDirExists(t, lock)
	})
	t.Run("a lock that is not its own any more is left", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		// The holder's record is its slot's owner file (LOCK/owner is the view).
		out, err := loadlock(t, lock, "bash", "-c", `echo other > "$0.d/1/owner"`, lock).CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.Contains(t, string(out), "not ours any more")
		owner, rerr := os.ReadFile(filepath.Join(lock+".d", "1", "owner"))
		require.NoError(t, rerr)
		assert.Equal(t, "other\n", string(owner))
	})
	t.Run("a signal stops the command and releases the lock", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		cmd := loadlock(t, lock, "sleep", "30")
		require.NoError(t, cmd.Start())
		require.Eventually(t, func() bool {
			owner, err := os.ReadFile(filepath.Join(lock, "owner"))
			return err == nil && strings.HasPrefix(string(owner), "test pid:")
		}, 10*time.Second, 20*time.Millisecond)
		start := time.Now()
		require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
		err := cmd.Wait()
		assert.Equal(t, 143, exitCode(err))
		assert.Less(t, time.Since(start), 10*time.Second, "the command is stopped, not waited for")
		assert.NoDirExists(t, lock)
	})
	t.Run("no lock, no run", func(t *testing.T) {
		cmd := exec.Command("bash", filepath.Join(repoRoot(t), "hack", "loadlock.sh"), "--", "true")
		cmd.Env = append(os.Environ(), "LOADLOCK=", "LOADLOCK_TEST=1", "LOADLOCK_TEST_LOAD=99") // a busy host: one holder at a time
		_, err := cmd.CombinedOutput()
		assert.Equal(t, 2, exitCode(err))
	})
}

// tickets is how many waiters queue for lock.
func tickets(lock string) int {
	entries, _ := os.ReadDir(lock + ".queue")
	n := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			n++
		}
	}
	return n
}

// served starts each waiter (name, then hack/loadlock.sh flags) while
// another holder, without a pid, holds lock, each queued before the next
// starts; it then frees the lock and returns the order they ran in.
func served(t *testing.T, lock string, waiters ...[]string) []string {
	t.Helper()
	require.NoError(t, os.Mkdir(lock, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(lock, "owner"), []byte("other\n"), 0o644))
	ran := filepath.Join(t.TempDir(), "ran")
	var cmds []*exec.Cmd
	for i, w := range waiters {
		args := append(append([]string{filepath.Join(repoRoot(t), "hack", "loadlock.sh"), "-l", lock, "-o", w[0], "-r", "0.05"},
			w[1:]...), "--", "bash", "-c", `echo "$0" >> "$1"; sleep 0.1`, w[0], ran)
		cmd := exec.Command("bash", args...)
		cmd.Env = append(os.Environ(), "LOADLOCK=", "LOADLOCK_TEST=1", "LOADLOCK_TEST_LOAD=99") // a busy host: one holder at a time
		require.NoError(t, cmd.Start())
		cmds = append(cmds, cmd)
		require.Eventually(t, func() bool { return tickets(lock) == i+1 }, 10*time.Second, 10*time.Millisecond, "%s queued", w[0])
	}
	require.NoError(t, os.RemoveAll(lock))
	for _, cmd := range cmds {
		require.NoError(t, cmd.Wait())
	}
	out, err := os.ReadFile(ran)
	require.NoError(t, err)
	assert.NoDirExists(t, lock)
	assert.Zero(t, tickets(lock), "every ticket is gone")
	return strings.Fields(string(out))
}

// TestLoadLock_Fair: waiters are served in the order they queued, a release
// gate's ticket (-p release) before the normal ones queued ahead of it, and
// a lock or ticket of a process that is gone is removed (the lock was a
// mutex with pollers: the release gate lost the race to later pollers).
func TestLoadLock_Fair(t *testing.T) {
	t.Run("first come, first served", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		assert.Equal(t, []string{"w1", "w2", "w3"}, served(t, lock, []string{"w1"}, []string{"w2"}, []string{"w3"}))
	})
	t.Run("a release ticket jumps the queue", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		assert.Equal(t, []string{"gate", "w1", "w2"},
			served(t, lock, []string{"w1"}, []string{"w2", "-p", "normal"}, []string{"gate", "-p", "release"}))
	})
	t.Run("a dead holder and a dead waiter are reclaimed", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		gone := exec.Command("true")
		require.NoError(t, gone.Run())
		dead := gone.Process.Pid
		require.NoError(t, os.Mkdir(lock, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(lock, "owner"), []byte(fmt.Sprintf("killed run pid %d 2026-10-17T00:00:00Z\n", dead)), 0o644))
		require.NoError(t, os.Mkdir(lock+".queue", 0o755))
		ghost := filepath.Join(lock+".queue", fmt.Sprintf("0-%019d-%d", 1, dead))
		require.NoError(t, os.WriteFile(ghost, []byte("ghost\n"), 0o644))
		out, err := loadlock(t, lock, "bash", "-c", `cat "$0/owner"`, lock).CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.Contains(t, string(out), "removed "+lock+" of a holder that is gone")
		assert.Contains(t, string(out), "test pid:", "it took the lock")
		assert.NoFileExists(t, ghost)
	})
	t.Run("an unknown priority is refused", func(t *testing.T) {
		cmd := exec.Command("bash", filepath.Join(repoRoot(t), "hack", "loadlock.sh"), "-l", filepath.Join(t.TempDir(), "LOADLOCK"),
			"-p", "urgent", "--", "true")
		out, err := cmd.CombinedOutput()
		assert.Equal(t, 2, exitCode(err), "%s", out)
		assert.Contains(t, string(out), "usage:")
	})
}

// TestLoadLock_Races (#2202 QA): the reclaim and liveness races of the
// first fair lock.
func TestLoadLock_Races(t *testing.T) {
	script := filepath.Join(repoRoot(t), "hack", "loadlock.sh")
	run := func(t *testing.T, lock, owner string, args ...string) *exec.Cmd {
		cmd := exec.Command("bash", append([]string{script, "-l", lock, "-o", owner, "-r", "0.05", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "LOADLOCK=", "LOADLOCK_TEST=1", "LOADLOCK_TEST_LOAD=99") // a busy host: one holder at a time
		return cmd
	}
	t.Run("25 locks at once, 8 waiters each on a dead holder: one holder at a time, none wedged", func(t *testing.T) {
		// Locks in parallel, so reapers overlap (#2226 QA: sequential rounds
		// never interleaved two, and #2202's script passed them).
		rounds := 2 // CI time; LOADLOCK_STRESS=5 for the review-size run
		if v := os.Getenv("LOADLOCK_STRESS"); v != "" {
			n, err := strconv.Atoi(v)
			require.NoError(t, err)
			rounds = n
		}
		gone := exec.Command("true")
		require.NoError(t, gone.Run())
		const locks, waiters = 25, 8
		for r := 0; r < rounds; r++ {
			type lockRun struct {
				dir  string
				cmds []*exec.Cmd
				outs []*strings.Builder
			}
			var runs []lockRun
			for l := 0; l < locks; l++ {
				dir := t.TempDir()
				lock := filepath.Join(dir, "LOADLOCK")
				require.NoError(t, os.Mkdir(lock, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(lock, "owner"),
					[]byte(fmt.Sprintf("gone pid %d 2026-10-19T00:00:00Z\n", gone.Process.Pid)), 0o644))
				lr := lockRun{dir: dir}
				for i := 0; i < waiters; i++ {
					// The critical section: mkdir fails when another holder is in it.
					cmd := run(t, lock, fmt.Sprintf("w%d", i), "sh", "-c",
						`mkdir "$0" 2>/dev/null || touch "$1"; sleep 0.05; rmdir "$0" 2>/dev/null; true`,
						filepath.Join(dir, "inside"), filepath.Join(dir, "overlap"))
					o := &strings.Builder{}
					cmd.Stdout, cmd.Stderr = o, o
					lr.cmds, lr.outs = append(lr.cmds, cmd), append(lr.outs, o)
				}
				runs = append(runs, lr)
			}
			for _, lr := range runs {
				for _, c := range lr.cmds {
					require.NoError(t, c.Start())
				}
			}
			done := make(chan struct{})
			go func() {
				for _, lr := range runs {
					for _, c := range lr.cmds {
						_ = c.Wait()
					}
				}
				close(done)
			}()
			kill := func() {
				for _, lr := range runs {
					for _, c := range lr.cmds {
						_ = c.Process.Kill()
					}
				}
			}
			// A round takes seconds; a lock with no owner for five looks a
			// second apart is wedged now, not at the timeout.
			ownerless := map[string]int{}
			deadline := time.After(60 * time.Second)
		wait:
			for {
				select {
				case <-done:
					break wait
				case <-deadline:
					kill()
					t.Fatalf("round %d: waiters never got their lock", r)
				case <-time.After(time.Second):
					for _, lr := range runs {
						lock := filepath.Join(lr.dir, "LOADLOCK")
						_, lerr := os.Stat(lock)
						_, oerr := os.Stat(filepath.Join(lock, "owner"))
						if lerr == nil && oerr != nil {
							if ownerless[lock]++; ownerless[lock] >= 5 {
								kill()
								t.Fatalf("round %d: %s is left with no owner", r, lock)
							}
						} else {
							delete(ownerless, lock)
						}
					}
				}
			}
			var overlaps, notOurs, left int
			for _, lr := range runs {
				if _, err := os.Stat(filepath.Join(lr.dir, "overlap")); err == nil {
					overlaps++
				}
				if _, err := os.Stat(filepath.Join(lr.dir, "LOADLOCK")); err == nil {
					left++
				}
				for _, o := range lr.outs {
					if strings.Contains(o.String(), "not ours any more") {
						notOurs++
					}
				}
			}
			assert.Zero(t, overlaps, "round %d: locks with two holders at once", r)
			assert.Zero(t, notOurs, "round %d: holders whose lock was taken from them", r)
			assert.Zero(t, left, "round %d: locks left behind", r)
		}
	})
	t.Run("a reused pid: another boot's or another start's record is dead", func(t *testing.T) {
		live := os.Getpid() // a pid that runs, as a reused one does
		for _, owner := range []string{
			fmt.Sprintf("before the reboot pid %d 2026-10-17T20:00:00Z boot=not-this-boot start=5", live),
			fmt.Sprintf("this boot pid %d 2026-10-19T00:00:00Z boot=%s start=1", live, strings.TrimSpace(readFile(t, "/proc/sys/kernel/random/boot_id"))),
			fmt.Sprintf("an older client pid %d 2020-01-01T00:00:00Z", live), // this test started long after
		} {
			lock := filepath.Join(t.TempDir(), "LOADLOCK")
			require.NoError(t, os.Mkdir(lock, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(lock, "owner"), []byte(owner+"\n"), 0o644))
			cmd := run(t, lock, "me", "true")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			assert.Contains(t, string(out), "of a holder that is gone", owner)
		}
	})
	t.Run("a waiter whose queue directory is removed re-creates it and runs", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		require.NoError(t, os.Mkdir(lock, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(lock, "owner"), []byte("other\n"), 0o644))
		ran := filepath.Join(t.TempDir(), "ran")
		cmd := run(t, lock, "waiter", "touch", ran)
		out := &syncBuf{}
		cmd.Stdout, cmd.Stderr = out, out
		require.NoError(t, cmd.Start())
		require.Eventually(t, func() bool { return tickets(lock) == 1 }, 10*time.Second, 10*time.Millisecond)
		// The queue cleaned by hand, as after a reboot (renamed away at once, as
		// rm -rf ends; the waiter may queue again before RemoveAll ends).
		require.NoError(t, os.Rename(lock+".queue", lock+".queue.gone"))
		require.NoError(t, os.RemoveAll(lock+".queue.gone"))
		require.Eventually(t, func() bool { return tickets(lock) == 1 }, 10*time.Second, 10*time.Millisecond, "queued again for real")
		require.NoError(t, os.RemoveAll(lock))
		require.NoError(t, cmd.Wait(), "%s", out.String())
		assert.FileExists(t, ran)
	})
	t.Run("an owner file that cannot be written gives the lock up and fails", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		require.NoError(t, os.Mkdir(lock+".queue", 0o755))
		require.NoError(t, os.Mkdir(lock+".d", 0o755))
		ran := filepath.Join(t.TempDir(), "ran")
		// umask 277: the slot directory mkdir makes is read-only to its owner.
		cmd := exec.Command("sh", "-c", `umask 277; exec bash "$@"`, "sh", script, "-l", lock, "-o", "me", "-r", "0.05", "--", "touch", ran)
		cmd.Env = append(os.Environ(), "LOADLOCK=", "LOADLOCK_TEST=1", "LOADLOCK_TEST_LOAD=99") // a busy host: one holder at a time
		out, err := cmd.CombinedOutput()
		assert.Equal(t, 1, exitCode(err), "%s", out)
		assert.Contains(t, string(out), "cannot write "+lock+".d/1/owner; gave the lock up")
		assert.NoDirExists(t, lock, "no ownerless lock is left")
		assert.NoDirExists(t, filepath.Join(lock+".d", "1"), "nor an ownerless slot")
		assert.NoFileExists(t, ran, "the command did not run")
	})
	t.Run("a holder whose start time is unknown is judged by boot id and pid", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		require.NoError(t, os.Mkdir(lock, 0o755))
		boot := strings.TrimSpace(readFile(t, "/proc/sys/kernel/random/boot_id"))
		require.NoError(t, os.WriteFile(filepath.Join(lock, "owner"),
			[]byte(fmt.Sprintf("live pid %d 2026-10-19T00:00:00Z boot=%s start=unknown\n", os.Getpid(), boot)), 0o644))
		cmd := run(t, lock, "me", "true")
		out := &syncBuf{}
		cmd.Stdout, cmd.Stderr = out, out
		require.NoError(t, cmd.Start())
		time.Sleep(time.Second)
		assert.DirExists(t, lock, "a live holder of this boot is not reclaimed: %s", out.String())
		require.NoError(t, os.RemoveAll(lock))
		require.NoError(t, cmd.Wait())
	})
	t.Run("an ownerless lock is reclaimed after a minute, not before", func(t *testing.T) {
		for _, tc := range []struct {
			age     time.Duration
			reclaim bool
		}{{50 * time.Second, false}, {61 * time.Second, true}} {
			lock := filepath.Join(t.TempDir(), "LOADLOCK")
			require.NoError(t, os.Mkdir(lock, 0o755))
			old := time.Now().Add(-tc.age)
			require.NoError(t, os.Chtimes(lock, old, old))
			cmd := run(t, lock, "me", "true")
			out := &syncBuf{}
			cmd.Stdout, cmd.Stderr = out, out
			require.NoError(t, cmd.Start())
			if tc.reclaim {
				require.NoError(t, cmd.Wait(), "%s", out.String())
				assert.Contains(t, out.String(), "which has had no owner for a minute")
				continue
			}
			time.Sleep(time.Second)
			assert.NotContains(t, out.String(), "no owner for a minute", "at %s", tc.age)
			require.NoError(t, os.RemoveAll(lock))
			require.NoError(t, cmd.Wait())
		}
	})
	t.Run("a ticket from before #2226 is warned about", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		require.NoError(t, os.Mkdir(lock, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(lock, "owner"), []byte("other\n"), 0o644))
		require.NoError(t, os.Mkdir(lock+".queue", 0o755))
		old := fmt.Sprintf("1-%019d-%d", time.Now().Add(time.Hour).UnixNano(), os.Getpid())
		require.NoError(t, os.WriteFile(filepath.Join(lock+".queue", old),
			[]byte(fmt.Sprintf("ENGINEER-9 old worktree pid %d %s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))), 0o644))
		cmd := run(t, lock, "me", "true")
		out := &syncBuf{}
		cmd.Stdout, cmd.Stderr = out, out
		require.NoError(t, cmd.Start())
		require.Eventually(t, func() bool { return strings.Contains(out.String(), "warning: ticket "+old) }, 10*time.Second, 20*time.Millisecond)
		assert.Contains(t, out.String(), "ENGINEER-9 old worktree")
		require.NoError(t, os.Remove(filepath.Join(lock+".queue", old)))
		require.NoError(t, os.RemoveAll(lock))
		require.NoError(t, cmd.Wait())
	})
	t.Run("a waiter whose ticket is removed queues it again", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		require.NoError(t, os.Mkdir(lock, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(lock, "owner"), []byte("other\n"), 0o644))
		ran := filepath.Join(t.TempDir(), "ran")
		cmd := run(t, lock, "waiter", "touch", ran)
		out := &syncBuf{}
		cmd.Stdout, cmd.Stderr = out, out
		require.NoError(t, cmd.Start())
		require.Eventually(t, func() bool { return tickets(lock) == 1 }, 10*time.Second, 10*time.Millisecond)
		entries, err := os.ReadDir(lock + ".queue")
		require.NoError(t, err)
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				require.NoError(t, os.Remove(filepath.Join(lock+".queue", e.Name())))
			}
		}
		require.Eventually(t, func() bool { return tickets(lock) == 1 }, 10*time.Second, 10*time.Millisecond, "queued again")
		require.NoError(t, os.RemoveAll(lock))
		require.NoError(t, cmd.Wait())
		assert.FileExists(t, ran)
		assert.Contains(t, out.String(), "queued it again in its place")
	})
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

// syncBuf is a buffer a command writes while the test reads it.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestLoadLock_Counting: under load 32 two hold the lock at once and a third
// waits; at load 32 or above one does. LOCK/owner lists every holder.
func TestLoadLock_Counting(t *testing.T) {
	script := filepath.Join(repoRoot(t), "hack", "loadlock.sh")
	start := func(t *testing.T, lock, owner, load string, args ...string) (*exec.Cmd, *syncBuf) {
		cmd := exec.Command("bash", append([]string{script, "-l", lock, "-o", owner, "-r", "0.05", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "LOADLOCK=", "LOADLOCK_TEST=1", "LOADLOCK_TEST_LOAD="+load)
		out := &syncBuf{}
		cmd.Stdout, cmd.Stderr = out, out
		require.NoError(t, cmd.Start())
		return cmd, out
	}
	t.Run("two holders on an idle host, a third waits", func(t *testing.T) {
		dir := t.TempDir()
		lock := filepath.Join(dir, "LOADLOCK")
		stop := filepath.Join(dir, "stop")
		t.Cleanup(func() { _ = os.WriteFile(stop, nil, 0o644) }) // holders never outlive the test
		hold := `while [ ! -e "$0" ]; do sleep 0.02; done`
		a, _ := start(t, lock, "a", "0", "sh", "-c", hold, stop)
		b, _ := start(t, lock, "b", "0", "sh", "-c", hold, stop)
		require.Eventually(t, func() bool {
			o, err := os.ReadFile(filepath.Join(lock, "owner"))
			return err == nil && strings.Count(strings.TrimSpace(string(o)), "\n") == 1
		}, 10*time.Second, 20*time.Millisecond, "LOCK/owner lists two holders")
		ran := filepath.Join(dir, "c-ran")
		c, _ := start(t, lock, "c", "0", "touch", ran)
		time.Sleep(500 * time.Millisecond)
		assert.NoFileExists(t, ran, "a third holder")
		require.NoError(t, os.WriteFile(stop, nil, 0o644))
		for _, cmd := range []*exec.Cmd{a, b, c} {
			require.NoError(t, cmd.Wait())
		}
		assert.FileExists(t, ran)
		assert.NoDirExists(t, lock, "no holder, no lock")
	})
	t.Run("one holder on a busy host", func(t *testing.T) {
		dir := t.TempDir()
		lock := filepath.Join(dir, "LOADLOCK")
		stop := filepath.Join(dir, "stop")
		t.Cleanup(func() { _ = os.WriteFile(stop, nil, 0o644) })
		a, _ := start(t, lock, "a", "32", "sh", "-c", `while [ ! -e "$0" ]; do sleep 0.02; done`, stop)
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(lock, "owner")); return err == nil }, 10*time.Second, 20*time.Millisecond)
		ran := filepath.Join(dir, "b-ran")
		b, _ := start(t, lock, "b", "32", "touch", ran)
		time.Sleep(500 * time.Millisecond)
		assert.NoFileExists(t, ran, "a second holder at load 32")
		require.NoError(t, os.WriteFile(stop, nil, 0o644))
		require.NoError(t, a.Wait())
		require.NoError(t, b.Wait())
		assert.FileExists(t, ran)
	})
	t.Run("50 locks at once on an idle host, 8 waiters each on dead holders: at most two, none wedged", func(t *testing.T) {
		gone := exec.Command("true")
		require.NoError(t, gone.Run())
		var cmds []*exec.Cmd
		var dirs []string
		for l := 0; l < 50; l++ {
			dir := t.TempDir()
			lock := filepath.Join(dir, "LOADLOCK")
			for _, n := range []string{"1", "2"} {
				require.NoError(t, os.MkdirAll(filepath.Join(lock+".d", n), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(lock+".d", n, "owner"),
					[]byte(fmt.Sprintf("gone pid %d 2026-10-19T00:00:00Z\n", gone.Process.Pid)), 0o644))
			}
			require.NoError(t, os.Mkdir(lock, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(lock, ".counting"), nil, 0o644))
			dirs = append(dirs, dir)
			for i := 0; i < 8; i++ {
				// Two places in the critical section; a third holder finds both taken.
				cmd := exec.Command("bash", script, "-l", lock, "-o", fmt.Sprintf("w%d", i), "-r", "0.05", "--", "sh", "-c",
					`if mkdir "$0/in1" 2>/dev/null; then p=in1; elif mkdir "$0/in2" 2>/dev/null; then p=in2; touch "$0/two"; else touch "$0/overflow"; exit 0; fi; sleep 0.05; rmdir "$0/$p"`, dir)
				cmd.Env = append(os.Environ(), "LOADLOCK=", "LOADLOCK_TEST=1", "LOADLOCK_TEST_LOAD=0")
				require.NoError(t, cmd.Start())
				cmds = append(cmds, cmd)
			}
		}
		done := make(chan struct{})
		go func() {
			for _, c := range cmds {
				_ = c.Wait()
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Minute):
			for _, c := range cmds {
				_ = c.Process.Kill()
			}
			t.Fatal("waiters never got their lock")
		}
		two := 0
		for _, dir := range dirs {
			assert.NoFileExists(t, filepath.Join(dir, "overflow"), "a third holder in %s", dir)
			assert.NoDirExists(t, filepath.Join(dir, "LOADLOCK"), "a lock left behind in %s", dir)
			if _, err := os.Stat(filepath.Join(dir, "two")); err == nil {
				two++
			}
		}
		assert.Positive(t, two, "two did hold a lock at once")
	})
}

// TestLoadLock_MixedClients (#2242 QA): the counting lock among the clients
// agents still run: #2202's and #2226's hack/loadlock.sh (copies in
// testdata) and a bare mkdir LOCK. Older clients hold the lock alone, new
// ones two at once on an idle host.
func TestLoadLock_MixedClients(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "hack", "loadlock.sh")
	old2202 := filepath.Join(root, "test", "hack", "testdata", "loadlock-2202.sh")
	old2226 := filepath.Join(root, "test", "hack", "testdata", "loadlock-2226.sh")
	env := append(os.Environ(), "LOADLOCK=", "LOADLOCK_TEST=1", "LOADLOCK_TEST_LOAD=0")

	t.Run("an older client never takes over a view whose dead first holder another slot outlives", func(t *testing.T) {
		dir := t.TempDir()
		lock := filepath.Join(dir, "LOADLOCK")
		stop := filepath.Join(dir, "stop")
		// However the test ends, the holders end before their directory goes.
		t.Cleanup(func() { _ = os.WriteFile(stop, nil, 0o644) })
		hold := `while [ ! -e "$0" ]; do sleep 0.02; done`
		a := exec.Command("bash", script, "-l", lock, "-o", "a", "-r", "0.05", "--", "sh", "-c", hold, stop)
		a.Env = env
		a.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		require.NoError(t, a.Start())
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(lock+".d", "1", "owner")); return err == nil }, 30*time.Second, 20*time.Millisecond)
		b := exec.Command("bash", script, "-l", lock, "-o", "b", "-r", "0.05", "--", "sh", "-c", hold, stop)
		b.Env = env
		require.NoError(t, b.Start())
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(lock+".d", "2", "owner")); return err == nil }, 30*time.Second, 20*time.Millisecond)
		// a dies holding slot 1: its slot and the view's first line stay.
		require.NoError(t, syscall.Kill(-a.Process.Pid, syscall.SIGKILL))
		_ = a.Wait()
		view := readFile(t, filepath.Join(lock, "owner"))
		assert.NotRegexp(t, `(^|\s)pid \d+`, view, "the view has no pid N an older client would judge")
		for _, old := range []string{old2202, old2226} {
			ran := filepath.Join(dir, "ran-"+filepath.Base(old))
			o := exec.Command("bash", old, "-l", lock, "-o", "older", "-r", "0.05", "--", "touch", ran)
			o.Env = env
			out := &syncBuf{}
			o.Stdout, o.Stderr = out, out
			require.NoError(t, o.Start())
			time.Sleep(time.Second)
			assert.NoFileExists(t, ran, "%s ran while slot 2's holder runs: %s", filepath.Base(old), out.String())
			_ = o.Process.Kill()
			_ = o.Wait()
		}
		require.NoError(t, os.WriteFile(stop, nil, 0o644))
		require.NoError(t, b.Wait())
	})

	t.Run("20 locks, new, older and bare-mkdir clients at once: never an older client with anyone, never three", func(t *testing.T) {
		rounds := 1 // CI time; LOADLOCK_STRESS=10 for the review-size run
		if v := os.Getenv("LOADLOCK_STRESS"); v != "" {
			n, err := strconv.Atoi(v)
			require.NoError(t, err)
			rounds = n
		}
		// LOADLOCK_STRESS_LOCKS sizes a round for a busy host (the
		// review-size run holds the host's LOADLOCK).
		locks := 20
		if v := os.Getenv("LOADLOCK_STRESS_LOCKS"); v != "" {
			n, err := strconv.Atoi(v)
			require.NoError(t, err)
			locks = n
		}
		// crit KIND DIR [CLIENT]: the critical section. A new client may share it
		// with one other new client; an older one with nobody.
		crit := filepath.Join(t.TempDir(), "crit.sh")
		// One listing, counted in the shell: a fork that fails on a busy
		// host is a crit error, never a violation, and the violation names
		// the markers it counted.
		require.NoError(t, os.WriteFile(crit, []byte(`kind=$1 d=$2 me=$d/$kind.$$.${3:-new}
mkdir "$me" || { echo "crit: mkdir $me failed" >>"$d/crit-error"; exit 1; }
seen=$(cd "$d" && echo new.* old.*) || { echo "crit: listing failed" >>"$d/crit-error"; rmdir "$me"; exit 1; }
n_new=0 n_all=0
for m in $seen; do
  case $m in new.\*|old.\*) ;; new.*) n_new=$((n_new + 1)); n_all=$((n_all + 1)) ;; old.*) n_all=$((n_all + 1)) ;; esac
done
if [ "$kind" = new ]; then ok=$([ "$n_new" -le 2 ] && [ "$n_new" -eq "$n_all" ] && echo y); else ok=$([ "$n_all" -eq 1 ] && echo y); fi
[ "$ok" = y ] || echo "$kind.$$ counted: $seen; slots: $(cat "$d"/LOADLOCK.d/*/owner 2>&1 | tr '\n' '|') LOCK/owner: $(cat "$d/LOADLOCK/owner" 2>&1 | tr '\n' '|')" >>"$d/violation"
sleep 0.05
rmdir "$me"
`), 0o755))
		for r := 0; r < rounds; r++ {
			var cmds []*exec.Cmd
			var outs []*syncBuf
			var dirs []string
			for l := 0; l < locks; l++ {
				dir := t.TempDir()
				lock := filepath.Join(dir, "LOADLOCK")
				dirs = append(dirs, dir)
				add := func(args ...string) {
					c := exec.Command(args[0], args[1:]...)
					c.Env = env
					o := &syncBuf{}
					c.Stdout, c.Stderr = o, o
					cmds, outs = append(cmds, c), append(outs, o)
				}
				for i := 0; i < 4; i++ {
					add("bash", script, "-l", lock, "-o", fmt.Sprintf("new%d", i), "-r", "0.05", "--", "sh", crit, "new", dir)
				}
				add("bash", old2202, "-l", lock, "-o", "old2202", "-r", "0.05", "--", "sh", crit, "old", dir, "old2202")
				add("bash", old2226, "-l", lock, "-o", "old2226", "-r", "0.05", "--", "sh", crit, "old", dir, "old2226")
				for i := 0; i < 2; i++ {
					// The bare mkdir an agent's brief once described.
					add("sh", "-c", `until mkdir "$0" 2>/dev/null; do sleep 0.02; done
echo "manual pid $$ now" >"$0/owner"
sh "$1" old "$2" manual
if [ "$(cat "$0/owner" 2>/dev/null)" = "manual pid $$ now" ]; then rm -r "$0"; else echo "manual: not ours any more"; fi`, lock, crit, dir)
				}
			}
			for _, c := range cmds {
				require.NoError(t, c.Start())
			}
			done := make(chan struct{})
			go func() {
				for _, c := range cmds {
					_ = c.Wait()
				}
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Minute):
				for _, c := range cmds {
					_ = c.Process.Kill()
				}
				t.Fatalf("round %d: clients never got their lock", r)
			}
			violations, notOurs, left := 0, 0, 0
			for i, dir := range dirs {
				if b, err := os.ReadFile(filepath.Join(dir, "violation")); err == nil {
					violations++
					t.Logf("round %d %s: %s", r, dir, b)
					// What its clients said: reclaims, fork failures.
					for _, o := range outs[i*8 : i*8+8] {
						for _, l := range strings.Split(o.String(), "\n") {
							if !strings.Contains(l, "checking again") && strings.TrimSpace(l) != "" {
								t.Logf("  client: %s", l)
							}
						}
					}
				}
				if b, err := os.ReadFile(filepath.Join(dir, "crit-error")); err == nil {
					t.Logf("round %d %s: a crit error (not counted): %s", r, dir, b)
				}
				if _, err := os.Stat(filepath.Join(dir, "LOADLOCK")); err == nil {
					left++
				}
			}
			for _, o := range outs {
				if strings.Contains(o.String(), "not ours any more") {
					notOurs++
				}
			}
			assert.Zero(t, violations, "round %d: locks whose exclusivity broke", r)
			assert.Zero(t, notOurs, "round %d: holders whose lock was taken from them", r)
			assert.Zero(t, left, "round %d: locks left behind", r)
		}
	})
}

// TestLoadLock_Exclusive (#2244 QA, item 5): loadlock.sh -x holds the
// counting lock alone, on an idle host too: two -x runs (two scale suites)
// never overlap, and no other run holds it beside one.
func TestLoadLock_Exclusive(t *testing.T) {
	script := filepath.Join(repoRoot(t), "hack", "loadlock.sh")
	dir := t.TempDir()
	lock := filepath.Join(dir, "LOADLOCK")
	crit := filepath.Join(t.TempDir(), "crit.sh")
	// One listing, counted in the shell (a failed fork is no violation).
	require.NoError(t, os.WriteFile(crit, []byte(`kind=$1 d=$2 me=$d/$kind.$$
mkdir "$me" || exit 1
seen=$(cd "$d" && echo x.* n.*) || { rmdir "$me"; exit 1; }
n_x=0 n_all=0
for m in $seen; do
  case $m in x.\*|n.\*) ;; x.*) n_x=$((n_x + 1)); n_all=$((n_all + 1)) ;; n.*) n_all=$((n_all + 1)) ;; esac
done
if [ "$kind" = x ]; then [ "$n_all" -eq 1 ] || echo "x.$$ counted: $seen" >>"$d/violation"; else [ "$n_x" -eq 0 ] && [ "$n_all" -le 2 ] || echo "n.$$ counted: $seen" >>"$d/violation"; fi
sleep 0.05
rmdir "$me"
`), 0o755))
	var cmds []*exec.Cmd
	for i := 0; i < 9; i++ {
		args := []string{script, "-l", lock, "-o", fmt.Sprintf("w%d", i), "-r", "0.05"}
		kind := "n"
		if i%3 == 0 {
			args, kind = append(args, "-x"), "x"
		}
		cmd := exec.Command("bash", append(args, "--", "sh", crit, kind, dir)...)
		cmd.Env = append(os.Environ(), "LOADLOCK=", "LOADLOCK_TEST=1", "LOADLOCK_TEST_LOAD=0")
		require.NoError(t, cmd.Start())
		cmds = append(cmds, cmd)
	}
	for _, c := range cmds {
		require.NoError(t, c.Wait())
	}
	assert.NoFileExists(t, filepath.Join(dir, "violation"), "an exclusive holder shared the lock")
	assert.NoDirExists(t, lock)
}

// TestLoadLock_ForkFailureIsNoDeath (#2242 stress at load 300): judging a
// holder's record forks nothing, so a fork that fails on a busy host (here:
// a cat that fails on /proc, as a $(cat ...) whose fork failed does) never
// makes a live holder look gone and its slot reclaimed.
func TestLoadLock_ForkFailureIsNoDeath(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "LOADLOCK")
	bin := t.TempDir()
	realCat, err := exec.LookPath("cat")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "cat"), []byte(`#!/bin/sh
case "$*" in */proc/*) echo "bash: fork: Resource temporarily unavailable" >&2; exit 1 ;; esac
exec `+realCat+` "$@"
`), 0o755))
	held := filepath.Join(t.TempDir(), "held")
	holder := loadlock(t, lock, "sh", "-c", `touch "$0"; sleep 3`, held)
	require.NoError(t, holder.Start())
	require.Eventually(t, func() bool { _, err := os.Stat(held); return err == nil }, 10*time.Second, 20*time.Millisecond)
	waiter := loadlock(t, lock, "true")
	waiter.Env = append(waiter.Env, "PATH="+bin+":"+os.Getenv("PATH"))
	out := &syncBuf{}
	waiter.Stdout, waiter.Stderr = out, out
	require.NoError(t, waiter.Start())
	time.Sleep(time.Second)
	assert.DirExists(t, filepath.Join(lock+".d", "1"), "the live holder keeps its slot")
	assert.NotContains(t, out.String(), "removed lock slot")
	assert.NotContains(t, out.String(), "of a waiter that is gone", "nor its own ticket (the old $(cat ...) judged this waiter gone)")
	require.NoError(t, holder.Wait())
	done := make(chan error, 1)
	go func() { done <- waiter.Wait() }()
	select {
	case err := <-done:
		require.NoError(t, err, "%s", out.String())
	case <-time.After(20 * time.Second):
		_ = waiter.Process.Kill()
		<-done
		t.Fatalf("the waiter never got the lock: %s", out.String())
	}
}
