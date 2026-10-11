// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
)

// TestDisplayStatesEndpoint (P2): the UI API serves pkg/display's table, so
// the UI shows the CLI's words; only GET is allowed.
func TestDisplayStatesEndpoint(t *testing.T) {
	s := &uiAPIServer{}
	rec := httptest.NewRecorder()
	s.handleDisplayStates(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ui/display-states", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var got uiDisplayStates
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, uiDisplayState{Value: "WaitingForMerge", Label: "Waiting for PR merge", Glyph: "◷", ASCII: "~", Tone: "waiting"}, got.Step["WaitingForMerge"])
	assert.Equal(t, "Live", got.Step["Verified"].Label)
	assert.Equal(t, "Up to date", got.Pipeline["Ready"].Label)
	assert.Equal(t, "Blocking", got.Gate["Block"].Label)
	assert.Equal(t, "Done", got.SubStep["Completed"].Label)
	assert.Equal(t, "Held by gate {gate}", got.HeldByGate.Label)
	assert.Len(t, got.Bundle, len(display.Table()[display.KindBundle]))

	rec = httptest.NewRecorder()
	s.handleDisplayStates(rec, httptest.NewRequest(http.MethodPost, "/api/v1/ui/display-states", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
