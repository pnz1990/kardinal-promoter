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

// init runs before TestMain (kube_guard_test.go): a child of
// TestRunPeakRSS holds its MiB for a moment and exits, running no test.
func init() {
	v := os.Getenv(rssChildEnv)
	if v == "" {
		return
	}
	mib, _ := strconv.Atoi(v)
	b := make([]byte, mib<<20)
	for i := 0; i < len(b); i += 4096 {
		b[i] = 1
	}
	time.Sleep(200 * time.Millisecond)
	runtime.KeepAlive(b)
	os.Exit(0)
}

// TestRunPeakRSS: a child is measured by its own memory, not its parent's.
// The parent holds 300 MiB. A child holding 40 MiB reads about 40 MiB more
// than one holding none (80 under -race, whose shadow memory doubles it;
// the test binary's own size cancels out), and far below the parent; its
// rusage Maxrss reads the parent's 300 or more (the trap the evidence pack
// scale tests fell into, #2320).
func TestRunPeakRSS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	parent := make([]byte, 300<<20)
	for i := 0; i < len(parent); i += 4096 {
		parent[i] = 1
	}
	child := func(mib int) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), rssChildEnv+"="+strconv.Itoa(mib))
		return cmd
	}
	base, err := RunPeakRSS(child(0))
	require.NoError(t, err)
	peak, err := RunPeakRSS(child(40))
	require.NoError(t, err)
	t.Logf("child of 0 MiB: %d MiB, of 40 MiB: %d MiB (race detector: %v)", base, peak, raceEnabled)
	want := 40.0
	if raceEnabled {
		want = 80 // the race detector's shadow memory doubles what the child touches
	}
	assert.InDelta(t, want, peak-base, 15, "the 40 MiB child reads about %v MiB above an empty one", want)

	cmd := child(40)
	require.NoError(t, cmd.Run())
	inherited := cmd.ProcessState.SysUsage().(*syscall.Rusage).Maxrss / 1024
	assert.GreaterOrEqual(t, inherited, int64(300), "rusage Maxrss carries the parent's high-water mark")
	assert.Less(t, peak, inherited-100, "RunPeakRSS reads the child, far below the inherited %d MiB", inherited)
	runtime.KeepAlive(parent)

	_, err = RunPeakRSS(exec.Command("false"))
	assert.Error(t, err, "a failed command is an error")
}
