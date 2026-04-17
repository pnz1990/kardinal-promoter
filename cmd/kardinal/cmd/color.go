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

// Package cmd provides ANSI color utilities for CLI output.
//
// Color rules:
//   - Colors are enabled only when writing to a TTY (os.Stdout), or when
//     --color flag is set explicitly.
//   - The NO_COLOR environment variable (https://no-color.org/) always takes
//     precedence and disables colors even if --color is set.
//   - Non-TTY output (pipes, redirects, CI) is always plain text.

package cmd

import (
	"io"
	"os"

	"github.com/mattn/go-isatty"
)

// ANSI escape codes for common colors and resets.
const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiBold   = "\033[1m"
)

// colorizer wraps a writer and provides state-aware coloring.
type colorizer struct {
	enabled bool
}

// newColorizer returns a colorizer for the given writer.
// It enables colors when:
//   - NO_COLOR is not set AND (force is true OR w is a TTY).
//
// NO_COLOR (https://no-color.org/) always takes precedence over --color.
func newColorizer(w io.Writer, force bool) colorizer {
	// NO_COLOR always wins regardless of force.
	if os.Getenv("NO_COLOR") != "" {
		return colorizer{enabled: false}
	}
	if force {
		return colorizer{enabled: true}
	}
	f, ok := w.(*os.File)
	if !ok {
		return colorizer{enabled: false}
	}
	return colorizer{enabled: isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())}
}

// colorState wraps a promotion state string with an appropriate ANSI color:
//   - Pass / Succeeded / Verified → green
//   - Block / Failed              → red
//   - Pending / Running           → yellow
//   - anything else (Superseded)  → no color
func (c colorizer) colorState(state string) string {
	if !c.enabled {
		return state
	}
	switch state {
	case "Pass", "PASS", "Succeeded", "Verified":
		return ansiGreen + state + ansiReset
	case "Block", "FAIL", "Failed":
		return ansiRed + state + ansiReset
	case "Pending", "PENDING", "Running":
		return ansiYellow + state + ansiReset
	default:
		return state
	}
}

// bold wraps text in bold ANSI if colors are enabled.
func (c colorizer) bold(s string) string {
	if !c.enabled {
		return s
	}
	return ansiBold + s + ansiReset
}
