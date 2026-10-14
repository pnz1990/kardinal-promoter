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
// status colours of the UI, read from its design tokens (web/tokens). Any
// other stream gets plain words: the state's label, no glyph, no colour:
//
//   - stdout not a terminal (a pipe, a file, CI), or a terminal with NO_COLOR
//     (https://no-color.org/) or TERM=dumb: plain words;
//   - a terminal without a UTF-8 locale: ASCII glyphs, in colour.
//
// Plain words keep UTF-8 when the locale is UTF-8 (LC_ALL, LC_CTYPE or
// LANG), so the cluster's text (a dash, an arrow) passes through; without a
// UTF-8 locale, or with TERM=dumb, everything is folded to ASCII (Fold).
// The labels and tones come from pkg/display, the table the UI uses.
package style

import (
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/mattn/go-isatty"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
	"github.com/kardinal-promoter/kardinal-promoter/web/tokens"
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
	// utf8 is a UTF-8 locale, without TERM=dumb: the stream may carry
	// non-ASCII text (the cluster's); when false, text is folded to ASCII.
	utf8 bool
}

// Plain is the Styler of a stream that is not a terminal: words only, in
// ASCII.
func Plain() Styler { return Styler{} }

// Detect is the Styler for w, from whether w is a terminal and from the
// environment (NO_COLOR, TERM, COLORTERM, COLORFGBG and the locale).
func Detect(w io.Writer) Styler {
	s := detect(IsTerminal(w), os.Getenv)
	if s.color && !enableColor(w) {
		// A Windows console without virtual-terminal processing.
		s.color = false
	}
	return s
}

// Terminal is the Styler of a terminal with the environment getenv reads:
// what Detect returns for a terminal.
func Terminal(getenv func(string) string) Styler { return detect(true, getenv) }

// detect is Detect for a stream that is a terminal when tty, reading the
// environment with getenv. NO_COLOR and TERM=dumb get plain words, as a pipe
// does. The exact token colours need a known background (COLORFGBG) as well
// as COLORTERM=truecolor; otherwise the 16 ANSI colours, which the
// terminal's theme adapts to its background.
func detect(tty bool, getenv func(string) string) Styler {
	utf8 := utf8Locale(getenv) && getenv("TERM") != "dumb"
	if !tty || getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		return Styler{utf8: utf8}
	}
	light, known := background(getenv("COLORFGBG"))
	return Styler{
		color:     true,
		glyphs:    true,
		unicode:   utf8,
		utf8:      utf8,
		light:     light,
		truecolor: known && (getenv("COLORTERM") == "truecolor" || getenv("COLORTERM") == "24bit"),
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
	if light, known := background(os.Getenv("COLORFGBG")); known {
		s.light = light
		s.truecolor = os.Getenv("COLORTERM") == "truecolor" || os.Getenv("COLORTERM") == "24bit"
	}
	return s
}

// ASCIIOnly reports whether the stream takes ASCII only (no UTF-8 locale,
// or TERM=dumb): its text, the cluster's too, is folded (Fold).
func (s Styler) ASCIIOnly() bool { return !s.utf8 }

// IsTerminal reports whether w is a terminal (also through a Folder).
func IsTerminal(w io.Writer) bool {
	if fw, ok := w.(*Folder); ok {
		w = fw.w
	}
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

// background reads COLORFGBG ("fg;bg", as rxvt, Konsole and iTerm2 set it):
// light is a light background colour (7, or 9 to 15), known whether it
// names one at all.
func background(colorfgbg string) (light, known bool) {
	parts := strings.Split(colorfgbg, ";")
	bg, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || colorfgbg == "" {
		return false, false
	}
	return bg == 7 || (bg >= 9 && bg <= 15), true
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

// tokenOf is the design token of each tone's colour (web/tokens/tokens.json,
// DESIGN-SYSTEM.md "Status"); failed is a filled chip, its text on-fill.
// Paused and held are ink, the terminal's own foreground, in bold: a
// person's decision, not a system signal, so they get no hue.
var tokenOf = map[display.Tone]string{
	display.ToneVerified:   "color.status.verified.fg",
	display.TonePromoting:  "color.status.promoting.fg",
	display.ToneWaiting:    "color.status.waiting.fg",
	display.ToneBlocked:    "color.status.blocked.fg",
	display.ToneFailed:     "color.status.failed.fill",
	display.ToneDegraded:   "color.status.failed.fg",
	display.ToneSuperseded: "color.status.superseded.fg",
	display.TonePending:    "color.status.superseded.fg",
}

// sgr is the SGR parameters of tone for s; "" for no colour.
func (s Styler) sgr(tone display.Tone) string {
	switch tone {
	case display.TonePaused, display.ToneHeld:
		return "1"
	}
	path, ok := tokenOf[tone]
	if !ok {
		return ""
	}
	c, ok := tokens.Color(path)
	if !ok {
		return ""
	}
	filled := tone == display.ToneFailed
	if !s.truecolor {
		hue := ansiHue(c.Light)
		switch {
		case hue < 0:
			return "2" // a grey: dim
		case filled:
			return fmt.Sprintf("1;97;%d", 40+hue)
		}
		return fmt.Sprintf("%d", 30+hue)
	}
	fg := c.Dark
	if s.light {
		fg = c.Light
	}
	if !filled {
		return "38;2;" + rgb(fg)
	}
	on, _ := tokens.Color("color.status.failed.on-fill")
	text := on.Dark
	if s.light {
		text = on.Light
	}
	return "1;38;2;" + rgb(text) + ";48;2;" + rgb(fg)
}

// ansiHue is the ANSI colour (1 red, 2 green, 3 yellow, 4 blue, 5 magenta,
// 6 cyan) of a #RRGGBB colour's hue, -1 for a grey (low saturation) or not a
// colour, so the 16-colour form keeps each token's hue. The bands follow how
// terminal themes draw the six colours: green reaches into blue-green
// (spruce), magenta into purple-red (garnet).
func ansiHue(hex string) int {
	if len(hex) != 7 || hex[0] != '#' {
		return -1
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return -1
	}
	r, g, b := float64(v>>16&0xff)/255, float64(v>>8&0xff)/255, float64(v&0xff)/255
	mx, mn := max(r, g, b), min(r, g, b)
	if mx == 0 || (mx-mn)/mx < 0.3 {
		return -1
	}
	var h float64
	switch mx {
	case r:
		h = 60 * math.Mod((g-b)/(mx-mn), 6)
	case g:
		h = 60 * ((b-r)/(mx-mn) + 2)
	default:
		h = 60 * ((r-g)/(mx-mn) + 4)
	}
	if h < 0 {
		h += 360
	}
	switch {
	case h < 30 || h >= 340:
		return 1
	case h < 75:
		return 3
	case h < 165:
		return 2
	case h < 195:
		return 6
	case h < 270:
		return 4
	}
	return 5
}

// rgb is "r;g;b" of a #RRGGBB colour.
func rgb(hex string) string {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil {
		return "0;0;0"
	}
	return fmt.Sprintf("%d;%d;%d", v>>16&0xff, v>>8&0xff, v&0xff)
}
