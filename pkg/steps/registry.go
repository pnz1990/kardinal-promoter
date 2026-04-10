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

import "fmt"

// registry maps step names to their Step implementations.
var registry = map[string]Step{}

// Register adds a step to the built-in registry. Called from each step's init().
func Register(s Step) {
	registry[s.Name()] = s
}

// Lookup returns the Step for the given name, or an error if not found.
func Lookup(name string) (Step, error) {
	s, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown step %q: not registered", name)
	}
	return s, nil
}

// Registered returns the names of all registered built-in steps.
func Registered() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
