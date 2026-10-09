// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"fmt"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// controllerSCMConfig is the controller's own SCM provider, from its flags.
type controllerSCMConfig struct {
	providerType, token, apiURL, webhookSecret string
	// githubApp, when set, authenticates the static provider as a GitHub
	// App installation (--github-app-*); token is then ignored. The dynamic
	// provider reads a token from its Secret.
	githubApp *scm.GitHubAppCredentials
	// onGitHubApp, when set, is called with the unguarded provider built
	// from githubApp (the controller checks the App at startup).
	onGitHubApp func(scm.SCMProvider)
	// dynamic builds a DynamicProvider, which a SecretWatcher reloads
	// (--scm-token-secret-name).
	dynamic bool
	// allowed is --scm-allowed-repositories; nil allows every repository.
	allowed *scm.RepositoryAllowlist
}

// buildControllerSCM returns the controller's SCM provider and the
// allowlist the reconcilers check Pipelines with. The allowlist matches
// repositories in the provider's canonical form (WithCanonicalRepo:
// Bitbucket Data Center's KEY/slug, whatever the URL), and the provider is
// wrapped in its Guard on the SCM's host, so every call the shared token
// makes is checked against --scm-allowed-repositories, whichever code path
// makes it (#1332). dyn is the DynamicProvider when cfg.dynamic, for the
// SecretWatcher.
func buildControllerSCM(cfg controllerSCMConfig) (provider scm.SCMProvider, dyn *scm.DynamicProvider, allowed *scm.RepositoryAllowlist, err error) {
	if cfg.dynamic {
		dyn, err = scm.NewDynamicProvider(cfg.providerType, cfg.token, cfg.apiURL, cfg.webhookSecret)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("unable to create dynamic SCM provider: %w", err)
		}
		provider = dyn
	} else {
		cred := scm.Credentials{Token: cfg.token, GitHubApp: cfg.githubApp}
		if provider, err = scm.NewProviderWithCredentials(cfg.providerType, cred, cfg.apiURL, cfg.webhookSecret); err != nil {
			return nil, nil, nil, fmt.Errorf("unable to create SCM provider: %w", err)
		}
		if cfg.githubApp != nil && cfg.onGitHubApp != nil {
			cfg.onGitHubApp(provider)
		}
	}
	allowed = cfg.allowed.WithCanonicalRepo(provider)
	if allowed != nil {
		host, err := scm.WebHost(cfg.providerType, cfg.apiURL)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("--scm-allowed-repositories needs the SCM host: %w", err)
		}
		provider = allowed.Guard(provider, host)
	}
	return provider, dyn, allowed, nil
}
