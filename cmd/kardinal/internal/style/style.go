// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package style renders the kardinal CLI's output for the stream it goes
// to. A person at a terminal gets each state's glyph and its colour, the
// status colours of the UI (DESIGN-SYSTEM.md: verified spruce, promoting
// thread red, waiting dusk, blocked ochre, failed garnet as the only filled
// chip, superseded and pending ink-3, paused and held ink). Anything else
// gets plain words:
//
//   - stdout not a terminal (a pipe, a file, CI): the state's label only,
//     ASCII rules, no colour, so scripts and tests read stable text;
//   - NO_COLOR (https://no-color.org/) or TERM=dumb on a terminal: the
//     state's ASCII glyph and its label, no colour;
//   - a terminal without a UTF-8 locale: ASCII glyphs, in colour.
//
// The labels, glyphs and tones come from pkg/display, the table the UI uses.
package style

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mattn/go-isatty"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
)

// Styler renders text for one output stream. The zero value is Plain.
type Styler struct {
	// color is ANSI colour.
	color bool
	// glyphs is a state glyph before each state's label: a person reads
	// the stream.
	glyphs bool
	// unicode is Unicode glyphs and rules; ASCII when false.
	unicode bool
	// light is a light terminal background (COLORFGBG).
	light bool
	// truecolor is 24-bit colour (COLORTERM); the 16 ANSI colours when false.
	truecolor bool
}

// Plain is the Styler of a stream that is not a terminal: words only.
func Plain() Styler { return Styler{} }

// Detect is the Styler for w, from whether w is a terminal and from the
// environment (NO_COLOR, TERM, COLORTERM, COLORFGBG and the locale).
func Detect(w io.Writer) Styler { return detect(IsTerminal(w), os.Getenv) }

// Terminal is the Styler of a terminal with the environment getenv reads:
// what Detect returns for a terminal.
func Terminal(getenv func(string) string) Styler { return detect(true, getenv) }

// detect is Detect for a stream that is a terminal when tty, reading the
// environment with getenv.
func detect(tty bool, getenv func(string) string) Styler {
	if !tty {
		return Plain()
	}
	plain := getenv("NO_COLOR") != "" || getenv("TERM") == "dumb"
	return Styler{
		color:     !plain,
		glyphs:    true,
		unicode:   !plain && utf8Locale(getenv),
		light:     lightBackground(getenv("COLORFGBG")),
		truecolor: getenv("COLORTERM") == "truecolor" || getenv("COLORTERM") == "24bit",
	}
}

// ForceColor is s with colour on, as --color asks, unless NO_COLOR is set:
// NO_COLOR always wins. It adds no glyphs, so a coloured pipe reads the same
// words as a plain one.
func (s Styler) ForceColor() Styler {
	if os.Getenv("NO_COLOR") != "" {
		return s
	}
	s.color = true
	s.truecolor = s.truecolor || os.Getenv("COLORTERM") == "truecolor" || os.Getenv("COLORTERM") == "24bit"
	s.light = s.light || lightBackground(os.Getenv("COLORFGBG"))
	return s
}

// Color reports whether s writes ANSI colour.
func (s Styler) Color() bool { return s.color }

// Unicode reports whether s writes Unicode glyphs and rules.
func (s Styler) Unicode() bool { return s.unicode }

// Terminal reports whether s writes for a person at a terminal (glyphs).
func (s Styler) Terminal() bool { return s.glyphs }

// IsTerminal reports whether w is a terminal.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && (isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()))
}

// utf8Locale reports whether the locale (LC_ALL, else LC_CTYPE, else LANG)
// is UTF-8.
func utf8Locale(getenv func(string) string) bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(k); v != "" {
			v = strings.ToLower(v)
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	return false
}

// lightBackground reports whether COLORFGBG ("fg;bg", as rxvt, Konsole and
// iTerm2 set it) names a light background colour (7, or 9 to 15 but 8).
func lightBackground(colorfgbg string) bool {
	parts := strings.Split(colorfgbg, ";")
	bg, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return false
	}
	return bg == 7 || (bg >= 9 && bg <= 15)
}

// State is the text of st: its label, after its glyph (when it has one) on
// a terminal, in its tone's colour when s colours.
func (s Styler) State(st display.State) string {
	text := st.Label
	if s.glyphs && st.Glyph != "" {
		text = st.Text(!s.unicode)
	}
	return s.Paint(st.Tone, text)
}

// Paint is text in tone's colour when s colours, else text.
func (s Styler) Paint(tone display.Tone, text string) string {
	if !s.color || text == "" {
		return text
	}
	sgr := s.sgr(tone)
	if sgr == "" {
		return text
	}
	return "\x1b[" + sgr + "m" + text + "\x1b[0m"
}

// Error is text in the failed tone's foreground (not filled): the colour of
// an ERROR: notice.
func (s Styler) Error(text string) string { return s.Paint(display.ToneDegraded, text) }

// Warning is text in the blocked tone: the colour of a WARNING: notice.
func (s Styler) Warning(text string) string { return s.Paint(display.ToneBlocked, text) }

// Dim is text in ink-3: secondary detail such as a hint.
func (s Styler) Dim(text string) string { return s.Paint(display.TonePending, text) }

// Rule is a horizontal rule n columns wide.
func (s Styler) Rule(n int) string {
	if s.unicode {
		return strings.Repeat("─", n)
	}
	return strings.Repeat("-", n)
}

// Sym is unicode on a Unicode terminal, else ascii.
func (s Styler) Sym(unicode, ascii string) string {
	if s.unicode {
		return unicode
	}
	return ascii
}

// palette is one tone's colours: DESIGN-SYSTEM.md's status -fg for a light
// and a dark background, and the nearest of the 16 ANSI colours, which a
// terminal's theme adapts to its own background. A filled tone (failed)
// also has its chip background.
type palette struct {
	light, dark     string // foreground hex
	lightBg, darkBg string // chip background hex; "" when not filled
	basic           string // SGR parameters of the 16-colour form
}

// palettes maps each tone to its colours. Paused and held are ink, the
// terminal's own foreground, in bold: they are a person's decision, not a
// system signal, so they get no hue.
var palettes = map[display.Tone]palette{
	display.ToneVerified:   {light: "#2B6E52", dark: "#6CC79B", basic: "32"},
	display.TonePromoting:  {light: "#C0151B", dark: "#FF7A7E", basic: "31"},
	display.ToneWaiting:    {light: "#35609E", dark: "#8FB2EC", basic: "34"},
	display.ToneBlocked:    {light: "#865400", dark: "#EDB15A", basic: "33"},
	display.ToneFailed:     {light: "#FAFBFA", dark: "#0F1513", lightBg: "#A3154A", darkBg: "#FF7BA3", basic: "1;97;41"},
	display.ToneDegraded:   {light: "#A3154A", dark: "#FF7BA3", basic: "1;31"},
	display.ToneSuperseded: {light: "#5F6B66", dark: "#8C9994", basic: "2"},
	display.TonePending:    {light: "#5F6B66", dark: "#8C9994", basic: "2"},
	display.TonePaused:     {basic: "1"},
	display.ToneHeld:       {basic: "1"},
}

// sgr is the SGR parameters of tone for s; "" for no colour.
func (s Styler) sgr(tone display.Tone) string {
	p, ok := palettes[tone]
	if !ok {
		return ""
	}
	if !s.truecolor || p.light == "" {
		return p.basic
	}
	fg, bg := p.dark, p.darkBg
	if s.light {
		fg, bg = p.light, p.lightBg
	}
	out := "38;2;" + rgb(fg)
	if bg != "" {
		out = "1;" + out + ";48;2;" + rgb(bg)
	}
	return out
}

// rgb is "r;g;b" of a #RRGGBB colour.
func rgb(hex string) string {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil {
		return "0;0;0"
	}
	return fmt.Sprintf("%d;%d;%d", v>>16&0xff, v>>8&0xff, v&0xff)
}
