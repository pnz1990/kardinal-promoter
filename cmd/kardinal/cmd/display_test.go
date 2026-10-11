// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

var cellGap = regexp.MustCompile(`\s{2,}`)

// tableCells splits a row of a tabwriter table (padding 2 or 3) into its
// cells. A cell may hold several words (a display label such as "Waiting
// for PR merge"), so strings.Fields would split it.
func tableCells(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	return cellGap.Split(line, -1)
}

// TestEnvCellLabel (P2): a pipeline-table cell shows the display label; the
// pipeline table's own "Waiting" (the Bundle will still come here) is "Not
// reached"; a fleet cell is already words.
//
// Covers DISPLAY-STATES-01.
func TestEnvCellLabel(t *testing.T) {
	assert.Equal(t, "Waiting for PR merge", envCellLabel("WaitingForMerge"))
	assert.Equal(t, "Live", envCellLabel("Verified"))
	assert.Equal(t, "Not reached", envCellLabel("Waiting"))
	assert.Equal(t, "-", envCellLabel(""))
	assert.Equal(t, "12/50 live, 1 failed", envCellLabel("12/50 live, 1 failed"))
	assert.Equal(t, "Not started", stepLabel(""))
}

var camelCase = regexp.MustCompile(`\b[A-Z][a-z]+[A-Z][A-Za-z]*\b`)

// apiStates are the CamelCase API values of the step, bundle, sub-step and
// gate states (the single-word ones are words already).
var apiStates = []string{"WaitingForMerge", "HealthChecking", "AbortedByAlarm", "RollingBack", "InProgress", "NotStarted"}

// assertNoRawStates fails when out shows a raw CamelCase state (P2: the
// default output says display labels).
func assertNoRawStates(t *testing.T, out string) {
	t.Helper()
	for _, s := range apiStates {
		assert.NotContains(t, out, s, "raw API state %q in the default output:\n%s", s, out)
	}
}
