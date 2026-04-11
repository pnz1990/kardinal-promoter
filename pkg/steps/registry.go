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

// registry maps step names to their Step implementations.
var registry = map[string]Step{}

// Register adds a step to the built-in registry. Called from each step's init().
func Register(s Step) {
	registry[s.Name()] = s
}

// Lookup returns the Step for the given name.
// For registered built-in steps the registered implementation is returned.
// For any other name a CustomWebhookStep is returned — unknown step names are
// treated as custom HTTP webhook steps whose URL is provided at runtime via
// PromotionStep.Spec.Inputs["webhook.url"].
func Lookup(name string) (Step, error) {
	if s, ok := registry[name]; ok {
		return s, nil
	}
	return NewCustomWebhookStep(name), nil
}

// IsBuiltin reports whether the given step name is a registered built-in step.
func IsBuiltin(name string) bool {
	_, ok := registry[name]
	return ok
}

// Registered returns the names of all registered built-in steps.
func Registered() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
