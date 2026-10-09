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
	token = strings.TrimSpace(token)
	switch providerType {
	case "github", "":
		return NewGitHubProvider(token, apiURL, webhookSecret), nil
	case "gitlab":
		return NewGitLabProvider(token, apiURL, webhookSecret), nil
	case "forgejo", "gitea":
		return NewForgejoProvider(token, apiURL, webhookSecret), nil
	case "bitbucket":
		return NewBitbucketProvider(token, apiURL, webhookSecret), nil
	case "azuredevops":
		return NewAzureDevOpsProvider(token, apiURL, webhookSecret), nil
	default:
		return nil, fmt.Errorf("unknown SCM provider type %q: supported types are \"github\", \"gitlab\", \"forgejo\", \"gitea\", \"bitbucket\", \"azuredevops\"", providerType)
	}
}

// Credentials are what a provider authenticates with: a token, or for
// GitHub, GitHub App credentials.
type Credentials struct {
	// Token is the PAT or access token. Ignored when GitHubApp is set.
	Token string
	// GitHubApp, when set, authenticates as a GitHub App installation.
	GitHubApp *GitHubAppCredentials
}

// fingerprint identifies the credentials without revealing them.
func (c Credentials) fingerprint() string {
	if c.GitHubApp != nil {
		return c.GitHubApp.Fingerprint()
	}
	return "token:" + strings.TrimSpace(c.Token)
}

// NewProviderWithCredentials is NewProvider for cred. GitHub App credentials
// need providerType github (or ""); the provider mints installation tokens
// at apiURL, so --scm-api-url points it at GitHub Enterprise Server too.
func NewProviderWithCredentials(providerType string, cred Credentials, apiURL, webhookSecret string) (SCMProvider, error) {
	if cred.GitHubApp == nil {
		return NewProvider(providerType, cred.Token, apiURL, webhookSecret)
	}
	if providerType != "github" && providerType != "" {
		return nil, fmt.Errorf("GitHub App credentials need SCM provider github, not %q", providerType)
	}
	src, err := NewGitHubAppTokenSource(*cred.GitHubApp, apiURL)
	if err != nil {
		return nil, err
	}
	return NewGitHubAppProvider(src, apiURL, webhookSecret), nil
}
