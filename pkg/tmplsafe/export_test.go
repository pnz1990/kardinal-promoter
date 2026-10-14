// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package tmplsafe

// Test-only helpers: production code does not call these (make hygiene).

// Const returns a Func with no arguments that returns s, sized len(s): for
// text computed before the render, such as a section of a default body.
func Const(s string) Func {
	return Func{Fn: func() string { return s }, Size: func([]interface{}) (int, error) { return len(s), nil }}
}
