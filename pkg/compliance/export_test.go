// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package compliance

import (
	"fmt"
	"strings"
)

// Markdown renders the shipped rows of frameworks as the tables of
// docs/compliance.md (TestCompliancePage writes and checks them): one section per framework, one table per control. A
// planned row is left out, not marked as coming, and so is a control with
// no shipped row (spec 22, decision C7).
func Markdown(frameworks []Framework) string {
	var b strings.Builder
	for _, f := range frameworks {
		var controls []Control
		for _, c := range f.Controls {
			if len(shipped(c.Evidence)) > 0 {
				controls = append(controls, c)
			}
		}
		if len(controls) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n", f.Name)
		if f.Caveat != "" {
			fmt.Fprintf(&b, "%s\n\n", f.Caveat)
		}
		for _, c := range controls {
			heading := c.ID + ": " + c.Title
			if c.ID == c.Title {
				heading = c.Title
			}
			fmt.Fprintf(&b, "#### %s\n\n*Requirement:* %s\n\n", heading, c.Requirement)
			b.WriteString("| Element | kardinal record |\n|---|---|\n")
			for _, e := range shipped(c.Evidence) {
				fmt.Fprintf(&b, "| %s | %s |\n", cell(e.Element), cell(e.Record))
			}
			fmt.Fprintf(&b, "\n**Your part:** %s\n\n", c.Customer)
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func shipped(evidence []Evidence) []Evidence {
	var out []Evidence
	for _, e := range evidence {
		if e.Status == Shipped {
			out = append(out, e)
		}
	}
	return out
}

// cell escapes a table cell's pipes.
func cell(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}
