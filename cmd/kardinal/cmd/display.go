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

// bundleLabel is the label of a Bundle phase ("" is Unknown).
func bundleLabel(phase string) string { return display.Bundle(phase).Label }

// pipelineLabel is the label of a Pipeline phase, or Paused.
func pipelineLabel(phase string, paused bool) string { return display.Pipeline(phase, paused).Label }

// envCellLabel is the label of a pipeline-table environment cell: a step
// state, "Waiting" (the Bundle will still come here: not reached yet), or a
// fleet cell (fleetCell), which is already words.
func envCellLabel(cell string) string { return envCellState(cell).Label }

// envCellState is the display state of a pipeline-table environment cell
// (envCellLabel); a dash and a fleet cell have no glyph and no tone.
func envCellState(cell string) display.State {
	switch cell {
	case "", "-":
		return display.State{Label: "-"}
	case "Waiting":
		return display.Step("NotStarted")
	case cellHeld:
		return display.Held("Pending")
	}
	if gates, ok := strings.CutPrefix(cell, cellHeldByGate); ok {
		return display.HeldByGate("Pending", gates)
	}
	if window, ok := strings.CutPrefix(cell, cellFrozen); ok {
		return display.Frozen("Pending", window)
	}
	if st := display.Step(cell); st.Tone != display.ToneUnknown {
		return st
	}
	return display.State{Label: cell}
}

// Pipeline-table cells of an environment the current Bundle has not entered
// because something holds it there: a rollback hold (kardinal rollback
// --hold) or gates, whose names follow cellHeldByGate.
const (
	cellHeld       = "Held"
	cellHeldByGate = "HeldByGate:"
	// cellFrozen, followed by the freeze's name: a freeze holds the step.
	cellFrozen = "Frozen:"
)

// heldCell is cell for an environment of a Pipeline row where the current
// Bundle waits (cell is "Waiting" or "Pending"): cellHeld when a rollback
// hold keeps the Bundle out, cellHeldByGate and the gate names when gates
// hold it, else cell.
func heldCell(cell string, held bool, gates []string, frozenBy string) string {
	if frozenBy != "" && (cell == "Pending" || cell == "Promoting") {
		return cellFrozen + frozenBy
	}
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
