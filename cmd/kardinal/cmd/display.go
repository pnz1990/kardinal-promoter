// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"strings"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
)

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
	case cellHeld:
		return display.Held("Pending").Label
	}
	if gates, ok := strings.CutPrefix(cell, cellHeldByGate); ok {
		return display.HeldByGate("Pending", gates).Label
	}
	if st := display.Step(cell); st.Tone != display.ToneUnknown {
		return st.Label
	}
	return cell
}

// Pipeline-table cells of an environment the current Bundle has not entered
// because something holds it there: a rollback hold (kardinal rollback
// --hold) or gates, whose names follow cellHeldByGate.
const (
	cellHeld       = "Held"
	cellHeldByGate = "HeldByGate:"
)

// heldCell is cell for an environment of a Pipeline row where the current
// Bundle waits (cell is "Waiting" or "Pending"): cellHeld when a rollback
// hold keeps the Bundle out, cellHeldByGate and the gate names when gates
// hold it, else cell.
func heldCell(cell string, held bool, gates []string) string {
	if cell != "Waiting" && cell != "Pending" {
		return cell
	}
	switch {
	case held:
		return cellHeld
	case len(gates) > 0:
		return cellHeldByGate + strings.Join(gates, ", ")
	}
	return cell
}

// stepRowLabel is the STATE label of a step row of kardinal status: a
// Pending step that a rollback hold or gates keep waiting says so.
func stepRowLabel(state string, held bool, gates []string) string {
	if state == "" {
		state = "Pending"
	}
	if state == "Pending" {
		switch {
		case held:
			return display.Held(state).Label
		case len(gates) > 0:
			return display.HeldByGate(state, strings.Join(gates, ", ")).Label
		}
	}
	return stepLabel(state)
}
