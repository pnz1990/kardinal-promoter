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

package steps

import (
	"errors"
	"fmt"
)

// registry maps step names to their Step implementations.
var registry = map[string]Step{}

// Register adds a step to the built-in registry. Called from each step's init().
func Register(s Step) {
	registry[s.Name()] = s
}

// Lookup returns the registered Step for the given name.
//
// An empty or unregistered name is a permanent error: the step sequence comes
// from DefaultSequenceForBundle, which names only built-in steps, so an
// unknown name is a bug that no retry can fix.
func Lookup(name string) (Step, error) {
	if name == "" {
		return nil, Permanent(errors.New("empty step name"))
	}
	if s, ok := registry[name]; ok {
		return s, nil
	}
	return nil, Permanent(fmt.Errorf("unknown step %q", name))
}
