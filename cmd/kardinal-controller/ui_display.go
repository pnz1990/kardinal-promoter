// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"net/http"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
)

// handleDisplayStates serves the display-state table (P2): the labels,
// glyphs and tones the CLI uses, so the UI says the same words.
func (s *uiAPIServer) handleDisplayStates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, displayStates())
}

func displayStates() uiDisplayStates {
	t := display.Table()
	conv := func(m map[string]display.State) map[string]uiDisplayState {
		out := make(map[string]uiDisplayState, len(m))
		for k, st := range m {
			out[k] = uiState(st)
		}
		return out
	}
	return uiDisplayStates{
		Pipeline: conv(t[display.KindPipeline]), Bundle: conv(t[display.KindBundle]), Step: conv(t[display.KindStep]),
		SubStep: conv(t[display.KindSubStep]), Gate: conv(t[display.KindGate]),
		HeldByGate: uiState(display.HeldByGate("Pending", "{gate}")), Held: uiState(display.Held("Verified")),
		Frozen: uiState(display.Frozen("Pending", "{freeze}")),
	}
}

func uiState(st display.State) uiDisplayState {
	return uiDisplayState{Value: st.Value, Label: st.Label, Glyph: st.Glyph, ASCII: st.ASCII, Tone: string(st.Tone)}
}
