// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm

import "strings"

// invisibleChars are the directional formatting characters (the bidi set
// the PR body's markdown removes, #2170), which reorder how the text around
// them displays, and the zero-width characters, which hide text inside a
// name: an account or author name with them can read as another.
var invisibleChars = strings.NewReplacer(
	// Directional formatting.
	"\u200e", "", "\u200f", "", "\u061c", "",
	"\u202a", "", "\u202b", "", "\u202c", "", "\u202d", "", "\u202e", "",
	"\u2066", "", "\u2067", "", "\u2068", "", "\u2069", "",
	// Zero width: space, non-joiner, joiner, word joiner, no-break space (BOM).
	"\u200b", "", "\u200c", "", "\u200d", "", "\u2060", "", "\ufeff", "",
)

// StripInvisible removes the directional formatting and zero-width
// characters from s (invisibleChars): SCM-supplied names kardinal records
// in status, AuditEvents and signed receipts display as what they are.
func StripInvisible(s string) string {
	return invisibleChars.Replace(s)
}
