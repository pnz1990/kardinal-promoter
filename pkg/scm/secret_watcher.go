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
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// secretWatchInterval is how often the SecretWatcher polls for token changes.
	// Short enough to pick up rotations within a minute; long enough to avoid
	// hammering the API server.
	secretWatchInterval = 30 * time.Second
)

// SecretWatcher polls a Kubernetes Secret and calls DynamicProvider.Reload when
// the token value changes. It runs as a controller-runtime manager.Runnable so
// it starts after the cache is synced and stops with the manager context.
//
// Graph-purity: this component reads a core/v1 Secret (external I/O) and writes
// only to DynamicProvider's atomic pointer — no CRD status writes, no business
// logic. It is a pure infrastructure component, not a reconciler.
type SecretWatcher struct {
	// K8sClient is used to read the Secret. Use the manager client (cache-backed)
	// for efficiency; the informer keeps the Secret in memory.
	K8sClient client.Client

	// Provider is the DynamicProvider to reload on token change.
	Provider *DynamicProvider

	// SecretName is the name of the Secret containing the SCM token.
	SecretName string

	// SecretNamespace is the namespace of the Secret.
	SecretNamespace string

	// SecretKey is the data key within the Secret (e.g. "token").
	SecretKey string

	// Log is the zerolog logger.
	Log zerolog.Logger

	// lastToken tracks the most recently applied token so we only call Reload
	// when the token actually changes.
	lastToken string

	// seeded is set after the first successful read of the Secret. That read
	// logs "SCM credentials loaded", not "rotated": docs/scm-providers.md tells
	// users to wait for a "rotated" line before revoking the old token, so a
	// "rotated" line at every startup would let them revoke too early.
	seeded bool
}

// NewSecretWatcher constructs a SecretWatcher.
func NewSecretWatcher(
	k8sClient client.Client,
	provider *DynamicProvider,
	secretName, secretNamespace, secretKey string,
	log zerolog.Logger,
) *SecretWatcher {
	return &SecretWatcher{
		K8sClient:       k8sClient,
		Provider:        provider,
		SecretName:      secretName,
		SecretNamespace: secretNamespace,
		SecretKey:       secretKey,
		Log:             log,
	}
}

// Start implements manager.Runnable. It polls the Secret at secretWatchInterval
// until the context is cancelled.
func (w *SecretWatcher) Start(ctx context.Context) error {
	log := w.Log.With().
		Str("secret", w.SecretNamespace+"/"+w.SecretName).
		Str("key", w.SecretKey).
		Logger()

	log.Info().Msg("SCM credential watcher started")

	// Read the Secret at once so a token changed since the Pod started is
	// picked up before the first reconcile. This first read logs "loaded".
	w.checkAndReload(ctx, log)

	ticker := time.NewTicker(secretWatchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("SCM credential watcher stopped")
			return nil
		case <-ticker.C:
			w.checkAndReload(ctx, log)
		}
	}
}

// checkAndReload reads the Secret and reloads the provider if its
// credentials changed: the token in SecretKey, or GitHub App credentials
// (githubAppID, githubAppInstallationID, githubAppPrivateKey) when the Secret
// has githubAppPrivateKey. The first successful read seeds lastToken and logs
// "SCM credentials loaded"; only a later change logs "SCM credentials
// rotated". New GitHub App credentials are checked by minting an
// installation token at once, so a wrong App ID or key is logged at startup
// rather than at the first promotion.
func (w *SecretWatcher) checkAndReload(ctx context.Context, log zerolog.Logger) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{
		Name:      w.SecretName,
		Namespace: w.SecretNamespace,
	}
	if err := w.K8sClient.Get(ctx, key, secret); err != nil {
		log.Error().Err(err).Msg("SCM credential watcher: failed to read Secret (will retry)")
		return
	}

	app, isApp, appErr := GitHubAppCredentialsFromData(secret.Data)
	if appErr != nil {
		log.Error().Err(appErr).Msg("SCM credential watcher: the GitHub App credentials in the Secret are incomplete; the provider keeps its credentials")
		return
	}
	var cred Credentials
	if isApp {
		cred = Credentials{GitHubApp: &app}
	} else {
		tokenBytes, ok := secret.Data[w.SecretKey]
		if !ok {
			log.Warn().Str("key", w.SecretKey).Msg("SCM credential watcher: key not found in Secret")
			return
		}
		// Trim so a trailing newline neither breaks the header nor looks like
		// a rotation on every poll.
		cred = Credentials{Token: strings.TrimSpace(string(tokenBytes))}
	}
	fp := cred.fingerprint()
	if fp == w.lastToken {
		// Credentials unchanged — no-op.
		return
	}

	var err error
	if isApp {
		err = w.Provider.ReloadCredentials(cred)
	} else {
		err = w.Provider.Reload(cred.Token)
	}
	if err != nil {
		log.Error().Err(err).Msg("SCM credential watcher: provider reload failed")
		return
	}

	w.lastToken = fp
	what := "the token in the Secret"
	if isApp {
		what = fmt.Sprintf("GitHub App %d installation %d", app.AppID, app.InstallationID)
		w.checkApp(ctx, log)
	}
	if !w.seeded {
		w.seeded = true
		log.Info().Msg("SCM credentials loaded — provider uses " + what)
		return
	}
	log.Info().Msg("SCM credentials rotated — provider reloaded with " + what)
}

// appCheckTimeout bounds the installation token mint that checks new GitHub
// App credentials.
const appCheckTimeout = 15 * time.Second

// checkApp mints an installation token with the provider's GitHub App
// credentials and logs a warning when GitHub refuses them. The token is
// cached for the provider's first requests.
func (w *SecretWatcher) checkApp(ctx context.Context, log zerolog.Logger) {
	ctx, cancel := context.WithTimeout(ctx, appCheckTimeout)
	defer cancel()
	if err := CheckGitHubApp(ctx, w.Provider.Current()); err != nil {
		log.Warn().Err(err).Msg("SCM GITHUB APP WARNING — cannot mint an installation token; promotion steps will fail until the App ID, installation ID or private key is fixed")
		return
	}
	log.Info().Msg("SCM GitHub App installation token minted")
}

// CheckGitHubApp mints an installation token with p's GitHub App
// credentials. It returns nil for a provider that does not use a GitHub
// App.
func CheckGitHubApp(ctx context.Context, p SCMProvider) error {
	if d, ok := p.(*DynamicProvider); ok {
		p = d.Current()
	}
	g, ok := p.(*GitHubProvider)
	if !ok || g.tokens == nil {
		return nil
	}
	_, err := g.tokens.Token(ctx)
	return err
}
