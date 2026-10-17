// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package part1 runs part of the actor-aware policy scenarios (policycases).
package part1

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/policycases"
)

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }

// TestScenarioOneParty runs policycases.OneParty. Covers SCEN-POLICY-PARTY-01.
func TestScenarioOneParty(t *testing.T) { policycases.OneParty(t) }

// TestScenarioAgentApprovals runs policycases.AgentApprovals. Covers SCEN-POLICY-APPROVE-01.
func TestScenarioAgentApprovals(t *testing.T) { policycases.AgentApprovals(t) }

// TestScenarioFreeze runs policycases.Freeze. Covers SCEN-POLICY-FREEZE-01.
func TestScenarioFreeze(t *testing.T) { policycases.Freeze(t) }

// TestScenarioRequireStamp runs policycases.RequireStamp. Covers SCEN-POLICY-STAMP-01.
func TestScenarioRequireStamp(t *testing.T) { policycases.RequireStamp(t) }

// TestScenarioApprovalFields runs policycases.ApprovalFields. Covers SCEN-POLICY-FIELDS-01.
func TestScenarioApprovalFields(t *testing.T) { policycases.ApprovalFields(t) }
