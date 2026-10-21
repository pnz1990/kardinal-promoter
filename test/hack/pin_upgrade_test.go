// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinUpgradeBudget is how long TestPinUpgradeReproducesV0123Pins waits for
// the registry and GitHub: past it the network is too slow to tell, and the
// test skips.
const pinUpgradeBudget = 90 * time.Second

// TestPinUpgradeUsage: hack/release/pin-upgrade.sh takes one version, X.Y.Z,
// and refuses anything else with exit status 2 before it reads the network.
func TestPinUpgradeUsage(t *testing.T) {
	script := filepath.Join(repoRoot(t), "hack", "release", "pin-upgrade.sh")
	for _, args := range [][]string{nil, {"v0.12.3"}, {"0.12"}, {"0.12.3", "0.13.0"}} {
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		require.True(t, errors.As(err, &exit), "pin-upgrade.sh %q: %v\n%s", args, err, out)
		assert.Equal(t, 2, exit.ExitCode(), "pin-upgrade.sh %q: usage", args)
		assert.Contains(t, string(out), "usage: ", "pin-upgrade.sh %q", args)
	}
}

// TestPinUpgradeReproducesV0123Pins runs hack/release/pin-upgrade.sh for
// v0.12.3, as published on ghcr.io and GitHub, and checks that it prints the
// V0123 pins of hack/e2e/versions.env byte for byte: each release's pins come
// from the script (CONTRIBUTING.md, "Pin the upgrade suite"). It reads the
// network and downloads the release's CLI (86 MB), so it skips with -short,
// offline, when a service refuses or cannot be reached (the script's exit
// status 3), and when the services take longer than pinUpgradeBudget. Tests
// run with no GitHub login (kubetest.Isolate), so GitHub's limit of 60
// unauthenticated API requests an hour applies: on a host that spent it,
// the script's first request is refused and the test skips, saying so.
func TestPinUpgradeReproducesV0123Pins(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: the script downloads a release's CLI (86 MB)")
	}
	root := repoRoot(t)
	env, err := os.ReadFile(filepath.Join(root, "hack", "e2e", "versions.env"))
	require.NoError(t, err)
	pins := map[string]string{}
	for _, l := range strings.Split(string(env), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.HasPrefix(k, "V0123_") {
			pins[strings.TrimPrefix(k, "V0123_")] = v
		}
	}
	require.Equal(t, "0.12.3", pins["CHART_VERSION"], "versions.env V0123_CHART_VERSION")
	for _, host := range []string{"ghcr.io:443", "github.com:443", "api.github.com:443"} {
		c, err := net.DialTimeout("tcp", host, 5*time.Second)
		if err != nil {
			t.Skipf("offline: %v", err)
		}
		_ = c.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), pinUpgradeBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "hack", "release", "pin-upgrade.sh"), pins["CHART_VERSION"])
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Skipf("ghcr.io and GitHub did not answer within %s: %s", pinUpgradeBudget, stderr.String())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 3 {
		t.Skipf("a service could not be reached or refused the request: %s", stderr.String())
	}
	require.NoError(t, err, stderr.String())
	want := fmt.Sprintf("V0123_CHART_DIGEST=%s\nV0123_CONTROLLER_DIGEST=%s\nV0123_CLI_SHA256=%s\n",
		pins["CHART_DIGEST"], pins["CONTROLLER_DIGEST"], pins["CLI_SHA256"])
	assert.Equal(t, want, stdout.String(), "pin-upgrade.sh 0.12.3 prints the pins versions.env holds")
}
