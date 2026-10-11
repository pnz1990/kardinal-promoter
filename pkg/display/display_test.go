// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package display_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
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
//
// Covers DISPLAY-STATES-01.
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

// tsPath is the UI's copy of the table, generated from this one.
const tsPath = "web/src/displayStates.gen.ts"

// TestTypeScriptTable (P2): web/src/displayStates.gen.ts is generated from
// the Go table, so the UI cannot drift from the CLI. Regenerate with
// KARDINAL_UPDATE_DISPLAY=1 go test ./pkg/display -run TestTypeScriptTable.
//
// Covers DISPLAY-STATES-01.
func TestTypeScriptTable(t *testing.T) {
	want := typeScript()
	path := filepath.Join(repoRoot(t), tsPath)
	if os.Getenv("KARDINAL_UPDATE_DISPLAY") == "1" {
		require.NoError(t, os.WriteFile(path, []byte(want), 0o644))
		return
	}
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, want, string(got),
		"%s is stale: run KARDINAL_UPDATE_DISPLAY=1 go test ./pkg/display -run TestTypeScriptTable", tsPath)
}

func typeScript() string {
	var b strings.Builder
	b.WriteString(`// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Code generated from pkg/display (TestTypeScriptTable). DO NOT EDIT.
// Regenerate: KARDINAL_UPDATE_DISPLAY=1 go test ./pkg/display -run TestTypeScriptTable
//
// The display-state table (P2): the label, glyph and tone of every API state,
// the same words as the CLI. The API values are unchanged; this is display only.

/** The design-system status bucket of a state: picks colour and shape. */
export type Tone =
  | 'verified' | 'promoting' | 'waiting' | 'blocked' | 'failed' | 'superseded'
  | 'paused' | 'held' | 'pending' | 'degraded' | 'unknown'

/** The object a state belongs to. */
export type DisplayKind = 'pipeline' | 'bundle' | 'step' | 'substep' | 'gate'

/** How one API value is shown. */
export interface DisplayState {
  /** The API value, unchanged (e.g. 'WaitingForMerge'). */
  value: string
  /** What a person reads (e.g. 'Waiting for PR merge'). */
  label: string
  /** A one-character Unicode symbol. */
  glyph: string
  /** The glyph without Unicode. */
  ascii: string
  tone: Tone
}

export const DISPLAY_STATES: Record<DisplayKind, Record<string, DisplayState>> = {
`)
	table := display.Table()
	for _, kind := range []display.Kind{display.KindPipeline, display.KindBundle, display.KindStep, display.KindSubStep, display.KindGate} {
		fmt.Fprintf(&b, "  %s: {\n", kind)
		values := make([]string, 0, len(table[kind]))
		for v := range table[kind] {
			values = append(values, v)
		}
		sort.Strings(values)
		for _, v := range values {
			fmt.Fprintf(&b, "    %s: %s,\n", v, tsState(table[kind][v]))
		}
		b.WriteString("  },\n")
	}
	b.WriteString("}\n\n")
	hg := display.HeldByGate("Pending", "{gate}")
	fmt.Fprintf(&b, `/** Splits a CamelCase value into words, like Go's display.Words ('WaitingForMerge' → 'Waiting for merge'). */
export function words(value: string): string {
  if (/[ \-_]/.test(value)) return value
  let out = ''
  for (let i = 0; i < value.length; i++) {
    const c = value[i]
    const upper = c >= 'A' && c <= 'Z'
    const prevLower = i > 0 && value[i - 1] >= 'a' && value[i - 1] <= 'z'
    const nextLower = i + 1 < value.length && value[i + 1] >= 'a' && value[i + 1] <= 'z'
    if (i > 0 && upper && (prevLower || nextLower)) {
      out += ' ' + c.toLowerCase()
    } else {
      out += c
    }
  }
  return out
}

/** How value of kind is shown; an unknown value reads as words, tone 'unknown' (Go: display.Of). */
export function displayState(kind: DisplayKind, value: string): DisplayState {
  const known = DISPLAY_STATES[kind][value]
  if (known) return known
  return { value, label: words(value) || 'Unknown', glyph: '?', ascii: '?', tone: 'unknown' }
}

/** An environment whose step a gate holds (Go: display.HeldByGate). */
export function heldByGate(stepState: string, gate: string): DisplayState {
  return { value: stepState, label: %s.replace("{gate}", gate), glyph: %s, ascii: %s, tone: %s }
}

/** An environment held by a rollback (Go: display.Held). */
export function held(stepState: string): DisplayState {
  const h: DisplayState = %s
  return { ...h, value: stepState }
}
`, tsString(hg.Label), tsString(hg.Glyph), tsString(hg.ASCII), tsString(string(hg.Tone)), tsState(display.Held("")))
	return b.String()
}

// tsString quotes s for TypeScript: a JSON string, without HTML escaping.
func tsString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func tsState(st display.State) string {
	return fmt.Sprintf("{ value: %s, label: %s, glyph: %s, ascii: %s, tone: %s }",
		tsString(st.Value), tsString(st.Label), tsString(st.Glyph), tsString(st.ASCII), tsString(string(st.Tone)))
}
