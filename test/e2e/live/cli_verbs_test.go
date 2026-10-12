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
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
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
	// A gate on a Bundle label needs a person (a gate that opens by itself,
	// like a soak, is waited for).
	gate := framework.Gate(a.ns, "hold", "prod", openExpr, recheck)
	e.CreateGate(t, gate)
	waitPipelineValid(t, e, a.ns, pipelineName)

	code, out := ship(t, c, a.ns, fixtures.V2)
	require.Equal(t, 3, code, "held at prod's gate: %+v", out)
	assert.Equal(t, "Held", out.Result)
	assert.Equal(t, fixtures.V2, out.Version)
	assert.Equal(t, "Version "+fixtures.V2+" ("+out.Bundle+") stopped: prod is held by gate hold, which does not open by itself (kardinal why "+pipelineName+" prod)", out.Message)
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
	assert.Regexp(t, `(?m)^ +\+\S+  test: Live$`, r.Stderr, "each state after the time since the start")
	assert.Regexp(t, `(?m)^Summary after \S+:\n  test +Live +took \S+$`, r.Stderr, "a summary at the end")
	assertEnvAt(t, b, "test", fixtures.V2)

	// --quiet: only the last line, nothing on stderr.
	r = c.Run(b.ns, "ship", pipelineName, "--image", fixtures.Image+":"+fixtures.V3, "--wait", "--quiet", "--timeout", "10m")
	require.Equal(t, 0, r.Code, r.Output())
	assert.Equal(t, fixtures.V3+" is live in test\n", r.Stdout)
	assert.Empty(t, r.Stderr, "--quiet")
	assertEnvAt(t, b, "test", fixtures.V3)

	// hold: refused to someone without hold rights (an approver), then made,
	// made again (a no-op), and released.
	approver := userKubeconfig(t, e, b.ns, "carol@example.com", "release-managers")
	r = c.Exec(framework.CLIOptions{Kubeconfig: approver}, c.Args(b.ns, "hold", pipelineName, "test", "--reason", "freeze")...)
	assert.Equal(t, 1, r.Code, r.Output())
	// Refused by RBAC where it first lacks a right (reading the steps, or
	// writing the hold), with the role that grants it named.
	assert.Regexp(t, `^you \(carol@example\.com\) cannot (list promotionsteps|update pipelines) in namespace `+b.ns+`\.\nAsk your cluster admin to bind you the kardinal-promoter-(viewer|promoter) role`, r.Stderr)
	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: b.ns, Name: pipelineName}, &p))
	assert.Empty(t, p.Spec.Holds, "a refused hold changes nothing")
	r = c.Run(b.ns, "hold", pipelineName, "test", "--reason", "freeze")
	require.Equal(t, 0, r.Code, r.Output())
	assert.Regexp(t, `^Held test of podinfo on podinfo-[a-z0-9]+: no other version promotes there until kardinal unhold podinfo test\n$`, r.Stdout)
	r = c.Run(b.ns, "hold", pipelineName, "test", "--reason", "freeze")
	require.Equal(t, 0, r.Code, "the same hold again: %s", r.Output())
	assert.Contains(t, r.Stdout, "already, for that reason: nothing changed")
	r = c.Run(b.ns, "unhold", pipelineName, "test")
	require.Equal(t, 0, r.Code, r.Output())

	// Without --wait: the name on stdout, the watch hint on stderr.
	r = c.Run(b.ns, "ship", pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	require.Equal(t, 0, r.Code, r.Output())
	assert.Regexp(t, `^Shipped `+fixtures.V2+` as podinfo-[a-z0-9]+\n$`, r.Stdout)
	assert.Equal(t, "Watch: kardinal status podinfo -w\n", r.Stderr)
}
