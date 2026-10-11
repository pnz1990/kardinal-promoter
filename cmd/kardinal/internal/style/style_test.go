// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package style

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"text/tabwriter"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

// TestDetect (C4): colour and Unicode only on a terminal; NO_COLOR and
// TERM=dumb give ASCII glyphs without colour; a pipe gets words only.
//
// Covers CLI-OUTPUT-01.
func TestDetect(t *testing.T) {
	live := display.Step("Verified")
	cases := []struct {
		name string
		tty  bool
		env  func(string) string
		want string
	}{
		{"pipe", false, env("LANG", "en_US.UTF-8", "COLORTERM", "truecolor"), "Live"},
		{"terminal, 16 colours", true, env("LANG", "en_US.UTF-8"), "\x1b[32m● Live\x1b[0m"},
		{"terminal, truecolor, dark", true, env("LANG", "en_US.UTF-8", "COLORTERM", "truecolor"), "\x1b[38;2;108;199;155m● Live\x1b[0m"},
		{"terminal, truecolor, light", true, env("LC_ALL", "C.UTF-8", "COLORTERM", "24bit", "COLORFGBG", "0;15"), "\x1b[38;2;43;110;82m● Live\x1b[0m"},
		{"NO_COLOR", true, env("LANG", "en_US.UTF-8", "NO_COLOR", "1"), "* Live"},
		{"TERM=dumb", true, env("LANG", "en_US.UTF-8", "TERM", "dumb"), "* Live"},
		{"no UTF-8 locale", true, env("LANG", "C"), "\x1b[32m* Live\x1b[0m"},
		{"no locale", true, env(), "\x1b[32m* Live\x1b[0m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, detect(tc.tty, tc.env).State(live))
		})
	}
}

// TestPalette (C4): each tone has the DESIGN-SYSTEM.md status colour; failed
// is the only filled one.
//
// Covers CLI-OUTPUT-01.
func TestPalette(t *testing.T) {
	dark := Styler{color: true, truecolor: true}
	light := Styler{color: true, truecolor: true, light: true}
	basic := Styler{color: true}
	cases := []struct {
		tone               display.Tone
		dark, light, basic string
	}{
		{display.ToneVerified, "38;2;108;199;155", "38;2;43;110;82", "32"},
		{display.TonePromoting, "38;2;255;122;126", "38;2;192;21;27", "31"},
		{display.ToneWaiting, "38;2;143;178;236", "38;2;53;96;158", "34"},
		{display.ToneBlocked, "38;2;237;177;90", "38;2;134;84;0", "33"},
		{display.ToneFailed, "1;38;2;15;21;19;48;2;255;123;163", "1;38;2;250;251;250;48;2;163;21;74", "1;97;41"},
		{display.ToneDegraded, "38;2;255;123;163", "38;2;163;21;74", "1;31"},
		{display.ToneSuperseded, "38;2;140;153;148", "38;2;95;107;102", "2"},
		{display.TonePending, "38;2;140;153;148", "38;2;95;107;102", "2"},
		{display.TonePaused, "1", "1", "1"},
		{display.ToneHeld, "1", "1", "1"},
	}
	for _, tc := range cases {
		t.Run(string(tc.tone), func(t *testing.T) {
			assert.Equal(t, "\x1b["+tc.dark+"mx\x1b[0m", dark.Paint(tc.tone, "x"))
			assert.Equal(t, "\x1b["+tc.light+"mx\x1b[0m", light.Paint(tc.tone, "x"))
			assert.Equal(t, "\x1b["+tc.basic+"mx\x1b[0m", basic.Paint(tc.tone, "x"))
		})
	}
	assert.Equal(t, "x", dark.Paint(display.ToneUnknown, "x"), "an unknown state gets no colour")
	assert.Equal(t, "x", Plain().Paint(display.ToneFailed, "x"))
}

// TestForceColor (C4): --color colours a pipe without adding glyphs;
// NO_COLOR wins.
//
// Covers CLI-OUTPUT-01.
func TestForceColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("COLORTERM", "")
	assert.Equal(t, "\x1b[33mBlocking\x1b[0m", Plain().ForceColor().State(display.Gate("Block")))
	t.Setenv("NO_COLOR", "1")
	assert.Equal(t, "Blocking", Plain().ForceColor().State(display.Gate("Block")))
}

// TestPlainIsASCII (C4): with words only, every state of the display table,
// the rules and the symbols are ASCII.
//
// Covers CLI-OUTPUT-01.
func TestPlainIsASCII(t *testing.T) {
	for _, s := range []Styler{Plain(), detect(true, env("NO_COLOR", "1", "LANG", "en_US.UTF-8")), detect(true, env("TERM", "dumb"))} {
		var b strings.Builder
		for _, states := range display.Table() {
			for _, st := range states {
				b.WriteString(s.State(st))
			}
		}
		b.WriteString(s.State(display.Held("Pending")) + s.State(display.HeldByGate("Pending", "g")))
		b.WriteString(s.Rule(10) + s.Sym("→", "->") + s.Error("ERROR:") + s.Warning("WARNING:") + s.Dim("x"))
		for _, r := range b.String() {
			require.Less(t, r, rune(0x80), "%q in %q", r, b.String())
		}
		assert.NotContains(t, b.String(), "\x1b")
	}
}

// TestTable (C4): the table aligns like text/tabwriter on plain text, and
// on visible widths with colour.
//
// Covers CLI-OUTPUT-01.
func TestTable(t *testing.T) {
	inputs := []string{
		"A\tBB\tC\nlonger\tx\ty\n",
		"NAME\tSTATE\tAGE\na\tLive\t1m\nbbbbbb\tWaiting for PR merge\t2h\n\nnote line\nX\tY\n",
		"header without tabs\nk\tv\tw\nk2\tv2\n",
		"",
		"no newline\tat end",
		"ENV\tSTATE\n▶ prod\t● Live\ntest\t-\n",
	}
	for _, pad := range []int{2, 3} {
		for _, in := range inputs {
			var want bytes.Buffer
			tw := tabwriter.NewWriter(&want, 0, 0, pad, ' ', 0)
			_, _ = fmt.Fprint(tw, in)
			require.NoError(t, tw.Flush())

			var got bytes.Buffer
			tb := NewTable(&got, pad)
			_, _ = fmt.Fprint(tb, in)
			require.NoError(t, tb.Flush())
			assert.Equal(t, want.String(), got.String(), "padding %d: %q", pad, in)
		}
	}

	s := Styler{color: true, glyphs: true, unicode: true}
	var got bytes.Buffer
	tb := NewTable(&got, 2)
	_, _ = fmt.Fprintf(tb, "ENV\tSTATE\tAGE\n")
	_, _ = fmt.Fprintf(tb, "prod\t%s\t1m\n", s.State(display.Step("WaitingForMerge")))
	_, _ = fmt.Fprintf(tb, "test\t%s\t2m\n", s.State(display.Step("Verified")))
	require.NoError(t, tb.Flush())
	lines := strings.Split(Strip(got.String()), "\n")
	assert.Equal(t, []string{
		"ENV   STATE                   AGE",
		"prod  ◷ Waiting for PR merge  1m",
		"test  ● Live                  2m",
		"",
	}, lines)
}
