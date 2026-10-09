// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
)

// envMemoryLimit is the container's memory limit in bytes, which the chart
// sets from the downward API (resourceFieldRef limits.memory, divisor 1).
const envMemoryLimit = "KARDINAL_MEMORY_LIMIT"

// memoryLimitShare is the part of the container limit the Go runtime's soft
// memory limit is set to: the garbage collector works harder as the heap
// nears it, so the controller collects before the kernel OOMKills it, with
// room left for memory the runtime does not count (stacks, cgo, the page
// cache of git work trees).
const memoryLimitShare = 0.9

// goMemoryLimit returns the soft memory limit to set from the environment
// (getenv), and why. It returns 0 when GOMEMLIMIT is set (the runtime
// already applied it) or there is no usable container limit.
func goMemoryLimit(getenv func(string) string) (int64, string, error) {
	if v := getenv("GOMEMLIMIT"); v != "" {
		return 0, "GOMEMLIMIT=" + v + " is set", nil
	}
	raw := strings.TrimSpace(getenv(envMemoryLimit))
	if raw == "" {
		return 0, envMemoryLimit + " is not set", nil
	}
	limit, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || limit <= 0 {
		return 0, "", fmt.Errorf("%s=%q is not a positive number of bytes", envMemoryLimit, raw)
	}
	return int64(float64(limit) * memoryLimitShare), fmt.Sprintf("90%% of the container's %d-byte memory limit", limit), nil
}

// applyGoMemoryLimit sets the Go runtime's soft memory limit from the
// container limit (goMemoryLimit) and returns what it set, 0 for nothing.
func applyGoMemoryLimit(getenv func(string) string) (int64, string, error) {
	limit, why, err := goMemoryLimit(getenv)
	if err != nil || limit == 0 {
		return 0, why, err
	}
	debug.SetMemoryLimit(limit)
	return limit, why, nil
}
