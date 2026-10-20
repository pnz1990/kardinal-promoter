// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// rssSample is how often RunPeakRSS reads the child's high-water mark.
const rssSample = 5 * time.Millisecond

// RunPeakRSS runs cmd and returns its own peak resident set size, in MiB:
// the largest VmHWM of /proc/<pid>/status read while it runs.
//
// The rusage of a finished child (cmd.ProcessState.SysUsage().Maxrss) is
// not that number. Go starts children with clone(CLONE_VM|CLONE_VFORK), so
// the child's exec retires the parent's address space as the child's own,
// and Linux keeps its high-water mark in the child's maxrss: a test process
// of 470 MiB reads 470 MiB for every CLI it runs. VmHWM is the high-water
// mark of the process's current address space, the one exec gave it.
//
// A peak followed by any sample is seen: VmHWM only grows. Growth after
// the last sample, in the child's final 5ms, is not: a burst right before
// exit can read a few MiB low (5 to 10 MiB for a 100 MiB allocation just
// before os.Exit), which a streaming export does not do. A child gone
// before the first sample returns an error rather than 0.
func RunPeakRSS(cmd *exec.Cmd) (int64, error) {
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	status := fmt.Sprintf("/proc/%d/status", cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var peakKiB int64
	sample := func() {
		if kib, ok := vmHWM(status); ok && kib > peakKiB {
			peakKiB = kib
		}
	}
	tick := time.NewTicker(rssSample)
	defer tick.Stop()
	sample()
	for {
		select {
		case err := <-done:
			if err != nil {
				return 0, err
			}
			if peakKiB == 0 {
				return 0, fmt.Errorf("%s exited before its memory could be read", cmd.Path)
			}
			return peakKiB / 1024, nil
		case <-tick.C:
			sample()
		}
	}
}

// vmHWM reads the VmHWM line (KiB) of a /proc/<pid>/status file; false
// when the process is gone or a zombie (no address space, so no line).
func vmHWM(path string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "VmHWM:"); ok {
			kib, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return kib, err == nil
		}
	}
	return 0, false
}
