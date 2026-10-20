// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rssChildEnv makes this test binary a child that holds that many MiB for
// a moment and exits.
const rssChildEnv = "KARDINAL_SCALE_RSS_CHILD_MIB"

func TestMain(m *testing.M) {
	if v := os.Getenv(rssChildEnv); v != "" {
		mib, _ := strconv.Atoi(v)
		b := make([]byte, mib<<20)
		for i := 0; i < len(b); i += 4096 {
			b[i] = 1
		}
		time.Sleep(200 * time.Millisecond)
		runtime.KeepAlive(b)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestRunPeakRSS: a child is measured by its own memory, not its parent's.
// The parent holds 300 MiB; a child holding 40 MiB reads about 40 MiB from
// RunPeakRSS, while its rusage Maxrss reads the parent's 300 or more (the
// trap the evidence pack scale tests fell into, #2238).
func TestRunPeakRSS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	parent := make([]byte, 300<<20)
	for i := 0; i < len(parent); i += 4096 {
		parent[i] = 1
	}
	child := func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), rssChildEnv+"=40")
		return cmd
	}
	peak, err := RunPeakRSS(child())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, peak, int64(40), "the child's 40 MiB")
	assert.Less(t, peak, int64(150), "not the parent's 300 MiB")

	cmd := child()
	require.NoError(t, cmd.Run())
	inherited := cmd.ProcessState.SysUsage().(*syscall.Rusage).Maxrss / 1024
	assert.GreaterOrEqual(t, inherited, int64(300), "rusage Maxrss carries the parent's high-water mark")
	runtime.KeepAlive(parent)

	_, err = RunPeakRSS(exec.Command("false"))
	assert.Error(t, err, "a failed command is an error")
}
