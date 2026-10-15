// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSampleMismatches(t *testing.T) {
	sample := `Pipeline: demo   Namespace: default
sha-9349a3f live in test, uat, prod

ENVIRONMENT  RUNNING                   INCOMING  STATE  SINCE  PR
test         sha-9349a3f (demo-zhxk2)  -         Live   35s    -
prod         sha-9349a3f (demo-zhxk2)  -         Live   1s     -
Approve sha-9349a3f (demo-zhxk2) for prod? [y/N] y
Recorded: kubernetes-admin approves demo-zhxk2 for prod (Approval demo-zhxk2-prod-…)
`
	output := `Pipeline: demo   Namespace: default
sha-9349a3f live in test, uat, prod

ENVIRONMENT  RUNNING                    INCOMING  STATE  SINCE  PR
test         sha-9349a3f (demo-q8m4t)   -         Live   2m5s   -
prod         sha-9349a3f (demo-q8m4t)   -         Live   48s    -
Recorded: kubernetes-admin approves demo-q8m4t for prod (Approval demo-q8m4t-prod-3f2a9c1b0d)
`
	assert.Empty(t, SampleMismatches(sample, output, nil, "demo"))

	// A different state, a missing line and lines out of order are reported.
	wrong := `Pipeline: demo   Namespace: default
ENVIRONMENT  RUNNING                    INCOMING  STATE  SINCE  PR
prod         sha-9349a3f (demo-q8m4t)   -         Live   48s    -
test         sha-9349a3f (demo-q8m4t)   -         Failed 2m5s   -
`
	assert.Equal(t, []string{
		"sha-9349a3f live in test, uat, prod",
		"test         sha-9349a3f (demo-zhxk2)  -         Live   35s    -",
		"Recorded: kubernetes-admin approves demo-zhxk2 for prod (Approval demo-zhxk2-prod-…)",
	}, SampleMismatches(sample, wrong, nil, "demo"))

	// Another pipeline's name is not a generated suffix of demo.
	assert.NotEmpty(t, SampleMismatches("Version main (demo-q8m4t) stopped", "Version main (shop-q8m4t) stopped", nil, "demo"))

	// Optional lines may be missing.
	ship := "Shipped main as demo-q8m4t; following it\n  test: Promoting\n  test: Live\n"
	assert.Empty(t, SampleMismatches(ship, "Shipped main as demo-abcde; following it\n  test: Live\n",
		regexp.MustCompile(`: (Promoting|Checking health)$`), "demo"))
}

func TestSampleMismatchesElapsed(t *testing.T) {
	sample := "      +2s  test: Promoting\n      +9s  test: Live\nSummary after 20s:\n  test   Live   took 9s\n"
	out := "     +1m5s  test: Live\nSummary after 1m10s:\n  test   Live   took 1m5s\n"
	assert.Empty(t, SampleMismatches(sample, out, regexp.MustCompile(`: Promoting$`)))
}
