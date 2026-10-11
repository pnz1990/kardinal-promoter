// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import "github.com/kardinal-promoter/kardinal-promoter/pkg/display"

// The CLI's default (table) output shows states as pkg/display labels, the
// words the UI uses, never the raw CamelCase API value (P2); -o json and -o
// yaml keep the API values.

// stepLabel is the label of a PromotionStep state ("" is Pending).
func stepLabel(state string) string {
	if state == "" {
		state = "Pending"
	}
	return display.Step(state).Label
}

// bundleLabel is the label of a Bundle phase ("" is Unknown).
func bundleLabel(phase string) string { return display.Bundle(phase).Label }

// subStepLabel is the label of a PromotionStep's sub-step state.
func subStepLabel(state string) string { return display.SubStep(state).Label }

// gateLabel is the label of a gate instance state (graph.GateState).
func gateLabel(state string) string { return display.Gate(state).Label }

// pipelineLabel is the label of a Pipeline phase, or Paused.
func pipelineLabel(phase string, paused bool) string { return display.Pipeline(phase, paused).Label }

// envCellLabel is the label of a pipeline-table environment cell: a step
// state, "Waiting" (the Bundle will still come here: not reached yet), or a
// fleet cell (fleetCell), which is already words.
func envCellLabel(cell string) string {
	switch cell {
	case "", "-":
		return "-"
	case "Waiting":
		return display.Step("NotStarted").Label
	}
	if st := display.Step(cell); st.Tone != display.ToneUnknown {
		return st.Label
	}
	return cell
}
