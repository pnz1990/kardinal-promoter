// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
