// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestControllerEmbedsTZData guards the time/tzdata import in main.go. The
// runtime image (alpine, no tzdata package) has no /usr/share/zoneinfo, so a
// ChangeWindow with spec.schedule.timezone set only works when the timezone
// database is compiled into the binary. A runtime LoadLocation check cannot
// prove this on a host that has tzdata installed, so the test reads the
// package's dependency list instead.
func TestControllerEmbedsTZData(t *testing.T) {
	goBin, err := exec.LookPath("go")
	require.NoError(t, err, "the go tool is needed to list the controller's dependencies")

	out, err := exec.Command(goBin, "list", "-deps", ".").CombinedOutput()
	require.NoError(t, err, "go list -deps: %s", out)

	deps := strings.Fields(string(out))
	assert.Contains(t, deps, "time/tzdata",
		"cmd/kardinal-controller must import _ \"time/tzdata\": the runtime image has no timezone database")
}
