// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package display_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

func repoRoot(t *testing.T) string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(f), "..", "..")
}

// enum returns the values of the +kubebuilder:validation:Enum marker just
// above the line of file that contains field.
func enum(t *testing.T, file, field string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), file))
	require.NoError(t, err)
	lines := strings.Split(string(b), "\n")
	re := regexp.MustCompile(`\+kubebuilder:validation:Enum=([A-Za-z;]+)`)
	for i, l := range lines {
		if !strings.Contains(l, field) {
			continue
		}
		for j := i - 1; j >= 0 && j > i-6; j-- {
			if m := re.FindStringSubmatch(lines[j]); m != nil {
				return strings.Split(m[1], ";")
			}
		}
	}
	t.Fatalf("no Enum marker above %q in %s", field, file)
	return nil
}

var camel = regexp.MustCompile(`[a-z][A-Z]`)

// TestEveryAPIValueHasALabel (P2): every value the API can hold has an
// entry, so no surface falls back to the raw value; a new enum value fails
// here until it gets a label.
func TestEveryAPIValueHasALabel(t *testing.T) {
	cases := map[display.Kind][]string{
		display.KindBundle:   enum(t, "api/v1alpha1/bundle_types.go", "Phase string `json:\"phase,omitempty\"`"),
		display.KindStep:     append(enum(t, "api/v1alpha1/promotionstep_types.go", "State string `json:\"state,omitempty\"`"), "NotStarted"),
		display.KindSubStep:  enum(t, "api/v1alpha1/promotionstep_types.go", "State StepExecutionState `json:\"state\"`"),
		display.KindPipeline: {"Ready", "Promoting", "Degraded", "Unknown", "Paused"},
		display.KindGate: {graph.GateStatePass, graph.GateStateBlock, graph.GateStateSuperseded,
			graph.GateStateRejected, graph.GateStatePending, graph.GateStateWaiting},
	}
	table := display.Table()
	for kind, values := range cases {
		require.NotEmpty(t, values, kind)
		for _, v := range values {
			st, ok := table[kind][v]
			if !assert.True(t, ok, "%s %q has no display state", kind, v) {
				continue
			}
			assert.Equal(t, v, st.Value)
			assert.NotRegexp(t, camel, st.Label, "%s %q: the label is not CamelCase", kind, v)
			assert.NotEqual(t, display.ToneUnknown, st.Tone)
			assert.NotEmpty(t, st.Glyph)
			for _, r := range st.ASCII {
				assert.Less(t, r, rune(unicode.MaxASCII), "%s %q: ASCII glyph %q", kind, v, st.ASCII)
			}
		}
	}
}

// TestLabels: the labels the UX audit (2.4) and the design system name.
func TestLabels(t *testing.T) {
	for _, tc := range []struct {
		got  display.State
		text string
		tone display.Tone
	}{
		{display.Step("WaitingForMerge"), "◷ Waiting for PR merge", display.ToneWaiting},
		{display.Step("HealthChecking"), "◌ Checking health", display.TonePromoting},
		{display.Step("Verified"), "● Live", display.ToneVerified},
		{display.Step("AbortedByAlarm"), "✕ Stopped by alarm", display.ToneFailed},
		{display.Step("Superseded"), "↷ Replaced by newer version", display.ToneSuperseded},
		{display.HeldByGate("Pending", "no-weekend-deploys"), "⏸ Held by gate no-weekend-deploys", display.ToneBlocked},
		{display.Held("Verified"), "⊙ Held by rollback", display.ToneHeld},
		{display.Bundle("Verified"), "● Verified", display.ToneVerified},
		{display.Pipeline("Ready", false), "● Up to date", display.ToneVerified},
		{display.Pipeline("Promoting", true), "‖ Paused", display.TonePaused},
		{display.Gate("Block"), "▮ Blocking", display.ToneBlocked},
	} {
		assert.Equal(t, tc.text, tc.got.Text(false))
		assert.Equal(t, tc.tone, tc.got.Tone, tc.text)
	}
	assert.Equal(t, "~ Waiting for PR merge", display.Step("WaitingForMerge").Text(true))
}

// TestUnknownValue: a value the table does not know reads as words, not
// CamelCase, and is ToneUnknown; the API value is kept.
func TestUnknownValue(t *testing.T) {
	st := display.Step("SomethingNewHere")
	assert.Equal(t, "Something new here", st.Label)
	assert.Equal(t, "SomethingNewHere", st.Value)
	assert.Equal(t, display.ToneUnknown, st.Tone)
	assert.Equal(t, "Unknown", display.Bundle("").Label)
	assert.Equal(t, "Waiting for PR merge", display.Words("Waiting for PR merge"))
	assert.Equal(t, "PR open", display.Words("PROpen"), "an acronym stays together")
}
