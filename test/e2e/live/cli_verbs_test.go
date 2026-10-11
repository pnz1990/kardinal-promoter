// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build e2e

package live

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// shipOutcome is kardinal ship --wait -o json.
type shipOutcome struct {
	Bundle, Pipeline, Version, Result, Message string
	ExitCode                                   int
	Environments                               []struct{ Name, State, PRURL string }
}

// ship runs kardinal ship <pipeline> --image ... --wait -o json and returns
// its exit code and outcome.
func ship(t *testing.T, c *framework.CLI, ns, tag string) (int, shipOutcome) {
	t.Helper()
	r := c.Run(ns, "ship", pipelineName, "--image", fixtures.Image+":"+tag, "--wait", "--timeout", "10m", "-o", "json")
	var out shipOutcome
	require.NoError(t, json.Unmarshal([]byte(r.Stdout), &out), "stdout is the JSON outcome only:\n%s", r.Output())
	assert.Equal(t, r.Code, out.ExitCode, "exitCode is the exit code")
	return r.Code, out
}

// TestCLI_ShipWait ships versions with --wait and checks the documented exit
// codes against a live promotion: 3 when an evaluated gate holds the Bundle,
// 4 when its PR waits for a person to merge it, 0 once it is live
// everywhere it goes.
//
// Covers CLI-VERBS-02.
func TestCLI_ShipWait(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)

	// A: test, then prod through a PR, with a gate on prod that is closed.
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	gate := framework.Gate(a.ns, "hold", "prod", openExpr, recheck)
	e.CreateGate(t, gate)
	waitPipelineValid(t, e, a.ns, pipelineName)

	code, out := ship(t, c, a.ns, fixtures.V2)
	require.Equal(t, 3, code, "held at prod's gate: %+v", out)
	assert.Equal(t, "Held", out.Result)
	assert.Equal(t, fixtures.V2, out.Version)
	assert.Equal(t, "Bundle "+out.Bundle+" stopped: prod is held by gate hold. Why: kardinal why "+pipelineName+" prod", out.Message)
	assertEnvAt(t, a, "test", fixtures.V2)

	// Without the gate, the next version waits for its prod PR.
	require.NoError(t, e.Client.Delete(context.Background(), gate))
	code, out = ship(t, c, a.ns, fixtures.V3)
	require.Equal(t, 4, code, "waiting for the prod PR: %+v", out)
	assert.Equal(t, "WaitingForMerge", out.Result)
	pr := openPR(t, e, a.repo, fixtures.V3)
	assert.True(t, strings.HasSuffix(out.Message, ": "+pr.URL), out.Message)
	var prod string
	for _, en := range out.Environments {
		if en.Name == "prod" {
			prod = en.State + " " + en.PRURL
		}
	}
	assert.Equal(t, "WaitingForMerge "+pr.URL, prod)

	// B: one auto environment: live, exit 0, and the text form.
	b := newArgoApp(t, e, "test")
	b.apply(t, b.pipeline(nil))
	waitPipelineValid(t, e, b.ns, pipelineName)
	r := c.Run(b.ns, "ship", pipelineName, "--image", fixtures.Image+":"+fixtures.V2, "--wait", "--timeout", "10m")
	require.Equal(t, 0, r.Code, r.Output())
	assert.Equal(t, fixtures.V2+" is live in test\n", r.Stdout)
	assert.Contains(t, r.Stderr, "following it")
	assert.Contains(t, r.Stderr, "test: Live")
	assertEnvAt(t, b, "test", fixtures.V2)

	// Without --wait: the name on stdout, the watch hint on stderr.
	r = c.Run(b.ns, "ship", pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	require.Equal(t, 0, r.Code, r.Output())
	assert.Regexp(t, `^Shipped `+fixtures.V3+` as podinfo-[a-z0-9]+\n$`, r.Stdout)
	assert.Equal(t, "Watch: kardinal status podinfo -w\n", r.Stderr)
}
