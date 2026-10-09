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

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/rs/zerolog"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// envInt64 reads an integer environment variable for a flag default; an
// unset or invalid value is 0.
func envInt64(name string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(name)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// staticSCMCredentials are the credentials of a controller that does not
// watch a Secret: GitHub App credentials when --github-app-id is set, the
// token otherwise.
func staticSCMCredentials(token string, appID, installationID int64, keyFile string) (scm.Credentials, error) {
	if appID == 0 && installationID == 0 && keyFile == "" {
		return scm.Credentials{Token: token}, nil
	}
	if appID <= 0 || installationID <= 0 || keyFile == "" {
		return scm.Credentials{}, errors.New("--github-app-id, --github-app-installation-id and --github-app-private-key-file must be set together")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return scm.Credentials{}, fmt.Errorf("read --github-app-private-key-file: %w", err)
	}
	return scm.Credentials{GitHubApp: &scm.GitHubAppCredentials{AppID: appID, InstallationID: installationID, PrivateKey: key}}, nil
}

// githubAPIURL is the GitHub API that GitHub App installation tokens for git
// are minted at: --scm-api-url when the provider is GitHub (so GitHub
// Enterprise Server works), api.github.com otherwise.
func githubAPIURL(providerType, apiURL string) string {
	if providerType == "" || providerType == "github" {
		return apiURL
	}
	return ""
}

// checkGitHubAppAtStartup mints one installation token with the provider's
// GitHub App credentials and logs whether it worked, so a wrong App ID or key
// shows at startup. It never fails startup.
func checkGitHubAppAtStartup(ctx context.Context, logger zerolog.Logger, p scm.SCMProvider) {
	ctx, cancel := context.WithTimeout(ctx, scmTokenCheckTimeout)
	defer cancel()
	if err := scm.CheckGitHubApp(ctx, p); err != nil {
		logger.Warn().Err(err).Msg("SCM GITHUB APP WARNING — cannot mint an installation token; promotion steps will fail until the App ID, installation ID or private key is fixed")
		return
	}
	logger.Info().Msg("SCM GitHub App installation token minted")
}
