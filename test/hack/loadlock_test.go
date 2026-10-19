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
	cmd.Env = append(os.Environ(), "LOADLOCK=")
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
		assert.Contains(t, string(out), "test pid ", "the owner names the holder")
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
		out, err := loadlock(t, lock, "bash", "-c", `echo other > "$0/owner"`, lock).CombinedOutput()
		require.NoError(t, err, "%s", out)
		assert.Contains(t, string(out), "not ours any more")
		owner, rerr := os.ReadFile(filepath.Join(lock, "owner"))
		require.NoError(t, rerr)
		assert.Equal(t, "other\n", string(owner))
	})
	t.Run("a signal stops the command and releases the lock", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		cmd := loadlock(t, lock, "sleep", "30")
		require.NoError(t, cmd.Start())
		require.Eventually(t, func() bool {
			owner, err := os.ReadFile(filepath.Join(lock, "owner"))
			return err == nil && strings.HasPrefix(string(owner), "test pid ")
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
		cmd.Env = append(os.Environ(), "LOADLOCK=")
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
		cmd.Env = append(os.Environ(), "LOADLOCK=")
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
		assert.Contains(t, string(out), "test pid ", "it took the lock")
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
		cmd.Env = append(os.Environ(), "LOADLOCK=")
		return cmd
	}
	t.Run("eight waiters on a dead holder's lock: one holder at a time, none wedged", func(t *testing.T) {
		rounds := 20
		if v := os.Getenv("LOADLOCK_STRESS"); v != "" {
			n, err := strconv.Atoi(v)
			require.NoError(t, err)
			rounds = n
		}
		gone := exec.Command("true")
		require.NoError(t, gone.Run())
		for r := 0; r < rounds; r++ {
			dir := t.TempDir()
			lock := filepath.Join(dir, "LOADLOCK")
			inside, overlap := filepath.Join(dir, "inside"), filepath.Join(dir, "overlap")
			require.NoError(t, os.Mkdir(lock, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(lock, "owner"),
				[]byte(fmt.Sprintf("gone pid %d 2026-10-19T00:00:00Z\n", gone.Process.Pid)), 0o644))
			var cmds []*exec.Cmd
			outs := make([]*strings.Builder, 8)
			for i := 0; i < 8; i++ {
				// The critical section: mkdir fails when another holder is in it.
				cmd := run(t, lock, fmt.Sprintf("w%d", i), "sh", "-c",
					`mkdir "$0" 2>/dev/null || touch "$1"; sleep 0.01; rmdir "$0" 2>/dev/null; true`, inside, overlap)
				outs[i] = &strings.Builder{}
				cmd.Stdout, cmd.Stderr = outs[i], outs[i]
				require.NoError(t, cmd.Start())
				cmds = append(cmds, cmd)
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
			case <-time.After(60 * time.Second):
				for _, c := range cmds {
					_ = c.Process.Kill()
				}
				t.Fatalf("round %d: a waiter never got the lock", r)
			}
			require.NoFileExists(t, overlap, "round %d: two holders at once", r)
			for i, o := range outs {
				require.NotContains(t, o.String(), "not ours any more", "round %d: w%d's lock was taken from it", r, i)
			}
			require.NoDirExists(t, lock, "round %d: the lock is released", r)
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
	t.Run("a waiter whose ticket is removed queues it again", func(t *testing.T) {
		lock := filepath.Join(t.TempDir(), "LOADLOCK")
		require.NoError(t, os.Mkdir(lock, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(lock, "owner"), []byte("other\n"), 0o644))
		ran := filepath.Join(t.TempDir(), "ran")
		cmd := run(t, lock, "waiter", "touch", ran)
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
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
