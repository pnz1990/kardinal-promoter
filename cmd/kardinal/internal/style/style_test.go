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
	"github.com/kardinal-promoter/kardinal-promoter/web/tokens"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

// TestDetect (C4): colour and glyphs only on a terminal; NO_COLOR and
// TERM=dumb give plain words, like a pipe; exact colours only with
// COLORTERM=truecolor and a known background.
//
// Covers CLI-STYLE-01.
func TestDetect(t *testing.T) {
	live := display.Step("Verified")
	verified, _ := tokens.Color("color.status.verified.fg")
	cases := []struct {
		name string
		tty  bool
		env  func(string) string
		want string
	}{
		{"pipe", false, env("LANG", "en_US.UTF-8", "COLORTERM", "truecolor"), "Live"},
		{"terminal, 16 colours", true, env("LANG", "en_US.UTF-8"), "\x1b[32m● Live\x1b[0m"},
		{"truecolor, background unknown: 16 colours", true, env("LANG", "en_US.UTF-8", "COLORTERM", "truecolor"), "\x1b[32m● Live\x1b[0m"},
		{"truecolor, dark", true, env("LANG", "en_US.UTF-8", "COLORTERM", "truecolor", "COLORFGBG", "15;0"),
			"\x1b[38;2;" + rgb(verified.Dark) + "m● Live\x1b[0m"},
		{"truecolor, light", true, env("LC_ALL", "C.UTF-8", "COLORTERM", "24bit", "COLORFGBG", "0;15"),
			"\x1b[38;2;" + rgb(verified.Light) + "m● Live\x1b[0m"},
		{"NO_COLOR", true, env("LANG", "en_US.UTF-8", "NO_COLOR", "1"), "Live"},
		{"TERM=dumb", true, env("LANG", "en_US.UTF-8", "TERM", "dumb"), "Live"},
		{"no UTF-8 locale", true, env("LANG", "C"), "\x1b[32m* Live\x1b[0m"},
		{"no locale", true, env(), "\x1b[32m* Live\x1b[0m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, detect(tc.tty, tc.env).State(live))
		})
	}
}

// TestPalette (C4): each tone's colour is its design token
// (web/tokens/tokens.json), read, not copied; failed is the only filled one.
// The 16-colour form is the ANSI hue nearest the token: garnet is magenta,
// the thread red of Promoting is red.
//
// Covers CLI-STYLE-01.
func TestPalette(t *testing.T) {
	dark := Styler{color: true, truecolor: true}
	light := Styler{color: true, truecolor: true, light: true}
	basic := Styler{color: true}
	tok := func(path string) tokens.Pair {
		c, ok := tokens.Color(path)
		require.True(t, ok, path)
		return c
	}
	onFill := tok("color.status.failed.on-fill")
	cases := []struct {
		tone        display.Tone
		token       string
		basic       string
		filled, dim bool
	}{
		{display.ToneVerified, "color.status.verified.fg", "32", false, false},
		{display.TonePromoting, "color.status.promoting.fg", "31", false, false},
		{display.ToneWaiting, "color.status.waiting.fg", "34", false, false},
		{display.ToneBlocked, "color.status.blocked.fg", "33", false, false},
		// garnet: magenta with the final token (#8A1E66, hue 320)
		{display.ToneFailed, "color.status.failed.fill", fmt.Sprintf("1;97;%d", 40+ansiHue(tok("color.status.failed.fill").Light)), true, false},
		{display.ToneDegraded, "color.status.failed.fg", fmt.Sprintf("%d", 30+ansiHue(tok("color.status.failed.fg").Light)), false, false},
		{display.ToneSuperseded, "color.status.superseded.fg", "2", false, true},
		{display.TonePending, "color.status.superseded.fg", "2", false, true},
	}
	for _, tc := range cases {
		t.Run(string(tc.tone), func(t *testing.T) {
			c := tok(tc.token)
			wantDark, wantLight := "38;2;"+rgb(c.Dark), "38;2;"+rgb(c.Light)
			if tc.filled {
				wantDark = "1;38;2;" + rgb(onFill.Dark) + ";48;2;" + rgb(c.Dark)
				wantLight = "1;38;2;" + rgb(onFill.Light) + ";48;2;" + rgb(c.Light)
			}
			assert.Equal(t, "\x1b["+wantDark+"mx\x1b[0m", dark.Paint(tc.tone, "x"))
			assert.Equal(t, "\x1b["+wantLight+"mx\x1b[0m", light.Paint(tc.tone, "x"))
			assert.Equal(t, "\x1b["+tc.basic+"mx\x1b[0m", basic.Paint(tc.tone, "x"))
		})
	}
	for _, tone := range []display.Tone{display.TonePaused, display.ToneHeld} {
		assert.Equal(t, "\x1b[1mx\x1b[0m", dark.Paint(tone, "x"), "%s is ink, bold", tone)
	}
	assert.Equal(t, "x", dark.Paint(display.ToneUnknown, "x"), "an unknown state gets no colour")
	assert.Equal(t, "x", Plain().Paint(display.ToneFailed, "x"))
}

// TestANSIHue (C4): the 16-colour form keeps a colour's hue.
//
// Covers CLI-STYLE-01.
func TestANSIHue(t *testing.T) {
	for hex, want := range map[string]int{"#C0151B": 1, "#2B6E52": 2, "#865400": 3, "#35609E": 4, "#8A1E66": 5, "#E78DD4": 5,
		"#00B7C3": 6, "#5F6B66": -1, "#18211E": -1, "#FFFFFF": -1, "bad": -1} {
		assert.Equal(t, want, ansiHue(hex), hex)
	}
}

// TestForceColor (C4): --color colours a pipe without adding glyphs;
// NO_COLOR wins.
//
// Covers CLI-STYLE-01.
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
// Covers CLI-STYLE-01.
func TestPlainIsASCII(t *testing.T) {
	for _, s := range []Styler{Plain(), detect(true, env("NO_COLOR", "1", "LANG", "en_US.UTF-8")), detect(true, env("TERM", "dumb")),
		detect(true, env("LANG", "C"))} {
		var b strings.Builder
		for _, states := range display.Table() {
			for _, st := range states {
				b.WriteString(s.State(st))
			}
		}
		b.WriteString(s.State(display.Held("Pending")) + s.State(display.HeldByGate("Pending", "g")))
		b.WriteString(s.Rule(10) + s.Sym("→", "->") + s.Error("ERROR:") + s.Warning("WARNING:") + s.Dim("x"))
		out := style16(b.String())
		for _, r := range out {
			require.Less(t, r, rune(0x80), "%q in %q", r, b.String())
		}
		if !s.color {
			assert.NotContains(t, out, "\x1b")
		}
	}
}

// style16 is text without its colour codes, which are ASCII too.
func style16(text string) string { return Strip(text) }

// TestFold (C4): plain words are ASCII, whatever the cluster's messages say;
// a character split across writes folds whole, and a table aligns on the
// folded text.
//
// Covers CLI-STYLE-01.
func TestFold(t *testing.T) {
	assert.Equal(t, "paused - resume -> now... \"q\" ?", Fold("paused — resume → now… “q” 世"))
	var b bytes.Buffer
	f := NewFolder(&b)
	msg := []byte("a—b→c")
	for i := range msg {
		_, err := f.Write(msg[i : i+1])
		require.NoError(t, err)
	}
	assert.Equal(t, "a-b->c", b.String())

	b.Reset()
	tb := NewTable(NewFolder(&b), 2)
	_, _ = fmt.Fprint(tb, "A\tB\nx→y\t1\nz\t2\n")
	require.NoError(t, tb.Flush())
	assert.Equal(t, "A     B\nx->y  1\nz     2\n", b.String())
	assert.True(t, IsTerminal(NewFolder(&b)) == false)
}

// TestTable (C4): the table aligns like text/tabwriter on plain text, and
// on visible widths with colour.
//
// Covers CLI-STYLE-01.
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
