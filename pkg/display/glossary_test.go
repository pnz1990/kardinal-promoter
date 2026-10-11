// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package display_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
)

// glossarySections maps each kind to the heading of its table in
// docs/reference/glossary.md ("States and how they are shown").
var glossarySections = map[display.Kind]string{
	display.KindPipeline: "### Pipeline",
	display.KindBundle:   "### Version",
	display.KindStep:     "### Promotion step",
	display.KindSubStep:  "### Sub-step",
	display.KindGate:     "### Gate",
}

// TestGlossaryShowsEveryLabel holds the glossary to the display table: every
// state of every kind has a "| `<value>` | <label> |" row in its kind's
// section, and each section lists no state the table does not have, so the
// docs say what the CLI and the UI show.
func TestGlossaryShowsEveryLabel(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "glossary.md"))
	require.NoError(t, err)
	doc := string(b)
	table := display.Table()
	require.Len(t, glossarySections, len(table), "a kind without a glossary section")
	for kind, heading := range glossarySections {
		i := strings.Index(doc, "\n"+heading+"\n")
		require.GreaterOrEqual(t, i, 0, "glossary has no %q section", heading)
		section := doc[i+len(heading)+2:]
		if j := strings.Index(section, "\n#"); j >= 0 {
			section = section[:j]
		}
		rows := 0
		for _, line := range strings.Split(section, "\n") {
			if strings.HasPrefix(line, "| `") {
				rows++
			}
		}
		for value, st := range table[kind] {
			assert.Contains(t, section, "| `"+value+"` | "+st.Label+" |", "%s %s", kind, value)
		}
		assert.Equal(t, len(table[kind]), rows, "%s: the glossary lists states the table does not have", heading)
	}
}
