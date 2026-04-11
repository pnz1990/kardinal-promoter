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

package scm

import "fmt"

// NewProvider constructs an SCMProvider for the given provider type.
// Supported types: "github" (default), "gitlab".
// Returns an error for unknown provider types.
func NewProvider(providerType, token, apiURL, webhookSecret string) (SCMProvider, error) {
	switch providerType {
	case "github", "":
		return NewGitHubProvider(token, apiURL, webhookSecret), nil
	case "gitlab":
		return NewGitLabProvider(token, apiURL, webhookSecret), nil
	default:
		return nil, fmt.Errorf("unknown SCM provider type %q: supported types are \"github\" and \"gitlab\"", providerType)
	}
}
