// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
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
	t.Parallel()
	assert.Equal(t, "Waiting for PR merge", envCellLabel("WaitingForMerge"))
	assert.Equal(t, "Live", envCellLabel("Verified"))
	assert.Equal(t, "Not reached", envCellLabel("Waiting"))
	assert.Equal(t, "-", envCellLabel(""))
	assert.Equal(t, "12/50 live, 1 failed", envCellLabel("12/50 live, 1 failed"))
	assert.Equal(t, "Queued", stepLabel(""))
}

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

// TestHeldEnvironments (P2 QA): an environment the current Bundle waits for
// because of a rollback hold or gates says "Held by rollback" or "Held by
// gate <name>", not "Not reached", in get pipelines and in kardinal status.
//
// Covers DISPLAY-STATES-01.
func TestHeldEnvironments(t *testing.T) {
	t.Parallel()
	old, recent := time.Now().Add(-2*time.Hour), time.Now().Add(-10*time.Minute)
	assert.Equal(t, "Held by rollback", envCellLabel(heldCell("Waiting", true, nil, "")))
	assert.Equal(t, "Held by gate a, b", envCellLabel(heldCell("Pending", false, []string{"a", "b"}, "")))
	assert.Equal(t, "Live", envCellLabel(heldCell("Verified", true, []string{"a"}, "")), "only a waiting cell is held")
	// #1974: a freeze holds a waiting or a promoting step, before the rest.
	assert.Equal(t, "Frozen by release-freeze", envCellLabel(heldCell("Pending", true, []string{"a"}, "release-freeze")))
	assert.Equal(t, "Frozen by release-freeze", envCellLabel(heldCell("Promoting", false, nil, "release-freeze")))
	assert.Equal(t, "Live", envCellLabel(heldCell("Verified", false, nil, "release-freeze")), "a step that moved on is not frozen")
	assert.Equal(t, "Not reached", envCellLabel(heldCell("Waiting", false, nil, "")))

	t.Run("get pipelines: rollback hold", func(t *testing.T) {
		p := policyPipeline("demo", "test", "prod")
		p.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "b1", Reason: "incident"}}
		rows := pipelineEnvStates([]v1alpha1.Pipeline{*p},
			[]v1alpha1.Bundle{*explainBundle("b1", "Verified", old), *explainBundle("b2", "Promoting", recent)},
			[]v1alpha1.PromotionStep{
				*explainStep("demo", "b1", "test", "Verified", "", old),
				*explainStep("demo", "b1", "prod", "Verified", "", old),
				*explainStep("demo", "b2", "test", "Verified", "", recent),
			}, nil)
		assert.Equal(t, "Held by rollback", envCellLabel(rows["default/demo"].envs["prod"]))
		assert.Equal(t, "Live", envCellLabel(rows["default/demo"].envs["test"]))
	})

	t.Run("status: a Pending step a gate holds", func(t *testing.T) {
		step := explainStep("demo", "b2", "prod", "Pending", "", recent)
		gate := explainGateInstance("demo", "b2", "prod", "no-weekend-deploys", "!schedule.isWeekend",
			false, true, "!schedule.isWeekend = false")
		step.Spec.RequiredGates = []string{gate.Name}
		out := runStatusPipeline(t, policyPipeline("demo", "uat", "prod"),
			explainBundle("b2", "Promoting", recent),
			explainStep("demo", "b2", "uat", "Verified", "", recent), step, gate)
		assert.Equal(t, "Held by gate no-weekend-deploys", statusRows(t, out)[1]["STATE"], out)
		assertNoRawStates(t, out)
	})

	t.Run("status: a Pending step a rollback hold keeps out", func(t *testing.T) {
		p := policyPipeline("demo", "uat", "prod")
		p.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "b1", Reason: "incident"}}
		objs := []sigs_client.Object{p,
			explainBundle("b1", "Verified", old), explainBundle("b2", "Promoting", recent),
			explainStep("demo", "b1", "prod", "Verified", "", old),
			explainStep("demo", "b2", "uat", "Verified", "", recent),
			explainStep("demo", "b2", "prod", "Pending", "", recent)}
		out := runStatusPipeline(t, objs...)
		assert.Equal(t, "Held by rollback", statusRows(t, out)[1]["STATE"], out)
	})
}
