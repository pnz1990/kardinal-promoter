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

import (
	"fmt"
	"strings"
)

// NewProvider constructs an SCMProvider for the given provider type.
// Supported types: "github" (default), "gitlab", "forgejo", "gitea", "bitbucket", "azuredevops".
// Returns an error for unknown provider types.
//
// Surrounding whitespace is trimmed from the token: a Secret written with
// --from-file or echo ends in a newline, which net/http refuses to send in a
// header (C06-scm-health-27).
func NewProvider(providerType, token, apiURL, webhookSecret string) (SCMProvider, error) {
	return newProvider(providerType, token, apiURL, webhookSecret, NewCircuitRegistry())
}

// newProvider is NewProvider with the circuit registry the provider uses, so
// a DynamicProvider can keep its circuits across token reloads (#1274).
func newProvider(providerType, token, apiURL, webhookSecret string, circuits *CircuitRegistry) (SCMProvider, error) {
	token = strings.TrimSpace(token)
	switch providerType {
	case "github", "":
		p := NewGitHubProvider(token, apiURL, webhookSecret)
		p.circuits = circuits
		return p, nil
	case "gitlab":
		p := NewGitLabProvider(token, apiURL, webhookSecret)
		p.circuits = circuits
		return p, nil
	case "forgejo", "gitea":
		p := NewForgejoProvider(token, apiURL, webhookSecret)
		p.circuits = circuits
		return p, nil
	case "bitbucket":
		p := NewBitbucketProvider(token, apiURL, webhookSecret)
		p.circuits = circuits
		return p, nil
	case "azuredevops":
		p := NewAzureDevOpsProvider(token, apiURL, webhookSecret)
		p.circuits = circuits
		return p, nil
	default:
		return nil, fmt.Errorf("unknown SCM provider type %q: supported types are \"github\", \"gitlab\", \"forgejo\", \"gitea\", \"bitbucket\", \"azuredevops\"", providerType)
	}
}
