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
	assert.Equal(t, "Pass", cr.colorState("Pass"), "disabled: Pass must be unchanged")
	assert.Equal(t, "Block", cr.colorState("Block"), "disabled: Block must be unchanged")
	assert.Equal(t, "Pending", cr.colorState("Pending"), "disabled: Pending must be unchanged")
	assert.Equal(t, "Superseded", cr.colorState("Superseded"), "disabled: Superseded must be unchanged")
	assert.Equal(t, "foo", cr.bold("foo"), "disabled: bold must be unchanged")
}

func TestColorizer_Forced(t *testing.T) {
	cr := newColorizer(&bytes.Buffer{}, true) // force=true
	assert.Equal(t, "\033[32mPass\033[0m", cr.colorState("Pass"), "forced: Pass must be green")
	assert.Equal(t, "\033[31mBlock\033[0m", cr.colorState("Block"), "forced: Block must be red")
	assert.Equal(t, "\033[33mPending\033[0m", cr.colorState("Pending"), "forced: Pending must be yellow")
	assert.Equal(t, "\033[33mRunning\033[0m", cr.colorState("Running"), "forced: Running must be yellow")
	assert.Equal(t, "\033[32mSucceeded\033[0m", cr.colorState("Succeeded"), "forced: Succeeded must be green")
	assert.Equal(t, "\033[31mFailed\033[0m", cr.colorState("Failed"), "forced: Failed must be red")
	assert.Equal(t, "Superseded", cr.colorState("Superseded"), "forced: Superseded has no color")
	assert.Equal(t, "\033[1mfoo\033[0m", cr.bold("foo"), "forced: bold must wrap in bold codes")
}

func TestColorizer_NoColorEnv(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cr := newColorizer(&bytes.Buffer{}, true) // force=true but NO_COLOR overrides
	assert.Equal(t, "Pass", cr.colorState("Pass"), "NO_COLOR: color must be disabled")
}
