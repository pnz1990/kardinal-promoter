// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build e2e

package live

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// durationIn parses the Go duration re's first group finds in s, failing
// the test when it does not match.
func durationIn(t *testing.T, re, s string) time.Duration {
	t.Helper()
	m := regexp.MustCompile(re).FindStringSubmatch(s)
	require.NotNil(t, m, "%s in:\n%s", re, s)
	d, err := time.ParseDuration(m[1])
	require.NoError(t, err, m[0])
	return d
}

// TestCLI_WalkthroughGates is the v0.11 new-user walkthrough's prod: an
// approval gate and a soak gate. ship --wait stops at the approval and its
// summary gives each environment its own time (never the 2562047h of an
// unset since); status's Blocking gates shows the approval as "approval 0 of
// 1" and the soak's progress; why leads with the approval and says when the
// soak opens. Once approved, why says the soak opens by itself, offers the
// override only as Or, and the soak then opens with no one doing anything.
//
// Covers CLI-WALK-01.
func TestCLI_WalkthroughGates(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test", "uat", "prod")
	approval := framework.Gate(a.ns, "prod-approval", "prod", "true", recheck)
	approval.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
	e.CreateGate(t, approval)
	soak := framework.Gate(a.ns, "uat-soak", "prod", "bundle.upstreamSoakMinutes >= 2", recheck)
	soak.Spec.Message = "A version runs in uat for 2 minutes before prod"
	e.CreateGate(t, soak)
	a.apply(t, a.pipeline(nil))
	waitPipelineValid(t, e, a.ns, pipelineName)

	r := c.Run(a.ns, "ship", pipelineName, "--image", fixtures.Image+":"+fixtures.V2, "--wait", "--timeout", "10m")
	require.Equal(t, 3, r.Code, "held at prod's approval: %s", r.Output())
	assert.NotContains(t, r.Stderr, "2562047h", "a since that is not known is never max Duration")
	total := durationIn(t, `(?m)^Summary after (\S+):$`, r.Stderr)
	for _, env := range []string{"test", "uat"} {
		took := durationIn(t, `(?m)^  `+env+` +Live +took (\S+)$`, r.Stderr)
		assert.LessOrEqual(t, took, total, "%s took its own time, within the follow", env)
	}
	held := durationIn(t, `(?m)^  prod +Held by gate \S.* +for (\S+)$`, r.Stderr)
	assert.LessOrEqual(t, held, total+5*time.Second, "prod waited at most the follow")
	bundle := regexp.MustCompile(`\((podinfo-[a-z0-9]+)\)`).FindStringSubmatch(r.Stdout)
	require.NotNil(t, bundle, r.Stdout)

	// status: the approval's tally, not its expression; the soak's progress.
	r = c.Run(a.ns, "status", pipelineName)
	require.Equal(t, 0, r.Code, r.Output())
	_, gates, ok := strings.Cut(r.Stdout, "\nBlocking gates\n")
	require.True(t, ok, r.Stdout)
	assert.Regexp(t, `(?m)^prod-approval +prod +approval 0 of 1 `, gates)
	assert.Regexp(t, `(?m)^uat-soak +prod +bundle\.upstreamSoakMinutes >= 2 +A version runs in uat for 2 minutes before prod; `+
		`soak \d+\.\d of 2 min in uat, opens (in about \S+|at its next check) `, gates)

	// why: the approval first, then the soak and when it opens.
	r = c.Run(a.ns, "why", pipelineName, "prod")
	require.Equal(t, 0, r.Code, r.Output())
	assert.Contains(t, r.Stdout, "  Gate     prod-approval · 0 of 1 approvals\n  Next     kardinal approve "+pipelineName+" prod\n")
	assert.Regexp(t, `(?m)^  Also +gate uat-soak: soak \d+\.\d of 2 min in uat, opens by itself (in about \S+|at its next check)$`, r.Stdout)

	r = c.Run(a.ns, "approve", pipelineName, "prod")
	require.Equal(t, 0, r.Code, r.Output())
	e.WaitGateReady(t, a.ns, bundle[1], "prod", "prod-approval", true, "", gateTimeout)

	// The soak alone: wait for it; the override is only the alternative.
	r = c.Run(a.ns, "why", pipelineName, "prod")
	require.Equal(t, 0, r.Code, r.Output())
	if strings.Contains(r.Stdout, "is held by gate uat-soak") {
		assert.Regexp(t, `(?m)^  Now +soak \d+\.\d of 2 min in uat$`, r.Stdout)
		assert.Regexp(t, `(?m)^  Next +wait: it opens by itself (in about \S+|at its next check) \(kardinal status `+pipelineName+` -w follows it\)$`, r.Stdout)
		assert.Regexp(t, `(?m)^  Or +kardinal override `+pipelineName+` prod --gate uat-soak --reason <why>$`, r.Stdout)
		assert.NotRegexp(t, `(?m)^  Next +kardinal override`, r.Stdout)
	} else {
		// The soak opened before why asked: it is never an override first.
		assert.NotContains(t, r.Stdout, "Next     kardinal override", r.Stdout)
	}
	r = c.Run(a.ns, "explain", pipelineName, "prod")
	require.Equal(t, 0, r.Code, r.Output())
	assert.NotContains(t, r.Stderr, "Override:", "a gate that opens by itself is waited for first")

	// It opens by itself: prod promotes with no one doing anything.
	e.WaitStepState(t, a.ns, pipelineName, bundle[1], "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}
