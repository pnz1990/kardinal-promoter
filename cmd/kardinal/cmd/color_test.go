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

package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestColorizer_Disabled(t *testing.T) {
	cr := newColorizer(&bytes.Buffer{}, false) // non-TTY writer, no force
	for _, s := range []string{"Pass", "Block", "Pending", "Waiting", "Verified", "Failed", "Superseded"} {
		assert.Equal(t, s, cr.colorState(s), "disabled: %s must be unchanged", s)
	}
}

// C09a-cli-13: every PromotionStep state (the CRD enum) and gate phase has a color.
func TestColorizer_Forced(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	cr := newColorizer(&bytes.Buffer{}, true)
	cases := map[string]string{
		"Pass":            ansiGreen,
		"Verified":        ansiGreen,
		"Block":           ansiRed,
		"Failed":          ansiRed,
		"AbortedByAlarm":  ansiRed,
		"Pending":         ansiYellow,
		"Waiting":         ansiYellow,
		"Promoting":       ansiYellow,
		"WaitingForMerge": ansiYellow,
		"HealthChecking":  ansiYellow,
		"RollingBack":     ansiYellow,
	}
	for state, color := range cases {
		assert.Equal(t, color+state+ansiReset, cr.colorState(state), state)
	}
	assert.Equal(t, "Superseded", cr.colorState("Superseded"), "unknown states have no color")
}

func TestColorizer_NoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cr := newColorizer(&bytes.Buffer{}, true) // force=true but NO_COLOR overrides
	assert.Equal(t, "Pass", cr.colorState("Pass"), "NO_COLOR: color must be disabled")
}
