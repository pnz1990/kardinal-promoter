// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// scmTokenCheckTimeout bounds the startup token check. The validators also set
// a 10s HTTP client timeout.
const scmTokenCheckTimeout = 15 * time.Second

// checkSCMTokenAtStartup validates the configured SCM token once, at startup,
// and logs what it finds. It never fails startup: a warning is logged and the
// controller keeps running, so a bad token surfaces in minutes instead of at
// the first open-pr step. It returns the warnings it logged.
//
// It runs whenever a token is set, including chart installs, where the token
// Secret is also watched for rotation: GITHUB_TOKEN holds the Secret's value at
// start. It is skipped when the token is empty (the watcher loads it later) and
// for providers without a validator (bitbucket, azuredevops). A network or HTTP
// error is logged at debug level.
//
// The token is trimmed the same way the SCM providers trim it, and it is never
// logged.
func checkSCMTokenAtStartup(ctx context.Context, logger zerolog.Logger, providerType, token, apiURL string) []scm.TokenScopeWarning {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}

	var validate func(context.Context, string, string) ([]scm.TokenScopeWarning, error)
	switch providerType {
	case "", "github":
		validate = scm.ValidateGitHubTokenScopes
	case "gitlab":
		validate = scm.ValidateGitLabTokenScopes
	case "forgejo", "gitea":
		validate = scm.ValidateForgejoTokenScopes
	default:
		logger.Info().
			Str("provider", providerType).
			Msg("SCM token check at startup is not available for this provider; token problems show on the first promotion step")
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, scmTokenCheckTimeout)
	defer cancel()
	warnings, err := validate(ctx, token, apiURL)
	if err != nil {
		logger.Debug().Err(err).
			Str("provider", providerType).
			Msg("SCM token scope check skipped (network error — non-fatal)")
		return nil
	}
	for _, w := range warnings {
		logger.Warn().
			Str("provider", providerType).
			Str("missing_scope", w.MissingScope).
			Str("consequence", w.Consequence).
			Msg("SCM TOKEN SCOPE WARNING — promotion steps may fail when this scope is required")
	}
	return warnings
}
