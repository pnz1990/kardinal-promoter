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

package style

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Table aligns tab-separated cells into columns, as
// text/tabwriter.NewWriter(w, 0, 0, padding, ' ', 0) does, but measures a
// cell without its ANSI colour codes, so a coloured cell lines up with
// plain ones. Write text with "\t" after each cell and "\n" after each
// line, then Flush. A column is a run of consecutive lines that have a cell
// in it, and the last cell of a line (after its last tab) is not aligned,
// as with tabwriter.
type Table struct {
	w       io.Writer
	padding int
	buf     bytes.Buffer
}

// NewTable is a Table writing to w with padding spaces between columns.
func NewTable(w io.Writer, padding int) *Table { return &Table{w: w, padding: padding} }

// Write buffers p until Flush.
func (t *Table) Write(p []byte) (int, error) { return t.buf.Write(p) }

// ansiRE matches an SGR escape sequence.
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// Width is the number of columns text takes on a terminal: its runes,
// without ANSI colour codes.
func Width(text string) int { return utf8.RuneCountInString(ansiRE.ReplaceAllString(text, "")) }

// Strip is text without its ANSI colour codes.
func Strip(text string) string { return ansiRE.ReplaceAllString(text, "") }

// Flush writes the buffered lines, aligned, and empties the buffer.
func (t *Table) Flush() error {
	text := t.buf.String()
	t.buf.Reset()
	if _, ok := t.w.(*Folder); ok {
		// Fold before measuring, so a folded cell ("->") lines up.
		text = Fold(text)
	}
	if text == "" {
		return nil
	}
	trailing := strings.HasSuffix(text, "\n")
	raw := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	lines := make([][]string, len(raw))
	for i, l := range raw {
		lines[i] = strings.Split(l, "\t")
	}
	f := formatter{lines: lines, padding: t.padding}
	f.format(0, len(lines))
	out := f.out.String()
	if !trailing {
		out = strings.TrimSuffix(out, "\n")
	}
	if _, err := io.WriteString(t.w, out); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	return nil
}

// formatter is text/tabwriter's column algorithm over measured cells.
type formatter struct {
	lines   [][]string
	padding int
	widths  []int
	out     strings.Builder
}

// format writes lines[line0:line1], aligning column len(f.widths) and the
// ones after it: each run of consecutive lines with a tab-terminated cell
// in that column is one column block, as wide as its widest cell plus the
// padding.
func (f *formatter) format(line0, line1 int) {
	column := len(f.widths)
	for this := line0; this < line1; this++ {
		if column >= len(f.lines[this])-1 {
			continue
		}
		// This line has a cell in the column: write the lines above it,
		// then the block of lines that have one.
		f.write(line0, this)
		line0 = this
		width := 0
		for ; this < line1; this++ {
			line := f.lines[this]
			if column >= len(line)-1 {
				break
			}
			if w := Width(line[column]) + f.padding; w > width {
				width = w
			}
		}
		f.widths = append(f.widths, width)
		f.format(line0, this)
		f.widths = f.widths[:len(f.widths)-1]
		line0 = this
	}
	f.write(line0, line1)
}

// write writes lines[line0:line1] with the current column widths.
func (f *formatter) write(line0, line1 int) {
	for _, line := range f.lines[line0:line1] {
		for j, cell := range line {
			f.out.WriteString(cell)
			if j < len(f.widths) && j < len(line)-1 {
				f.out.WriteString(strings.Repeat(" ", max(f.widths[j]-Width(cell), 0)))
			}
		}
		f.out.WriteString("\n")
	}
}
