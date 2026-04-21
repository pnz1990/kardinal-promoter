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

// CheckAndReloadForTest exposes checkAndReload for unit testing.
// Do not call in production code.
func (w *SecretWatcher) CheckAndReloadForTest(ctx context.Context) {
	log := w.Log.With().
		Str("secret", w.SecretNamespace+"/"+w.SecretName).
		Str("key", w.SecretKey).
		Logger()
	w.checkAndReload(ctx, log)
}

// Start implements manager.Runnable. It polls the Secret at secretWatchInterval
// until the context is cancelled.
func (w *SecretWatcher) Start(ctx context.Context) error {
	log := w.Log.With().
		Str("secret", w.SecretNamespace+"/"+w.SecretName).
		Str("key", w.SecretKey).
		Logger()

	log.Info().Msg("SCM credential watcher started")

	// Do an immediate check on startup so a rotated secret is picked up
	// before the first reconcile loop runs.
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

// checkAndReload reads the Secret and calls Provider.Reload if the token changed.
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

	tokenBytes, ok := secret.Data[w.SecretKey]
	if !ok {
		log.Warn().Str("key", w.SecretKey).Msg("SCM credential watcher: key not found in Secret")
		return
	}

	token := string(tokenBytes)
	if token == w.lastToken {
		// Token unchanged — no-op.
		return
	}

	if err := w.Provider.Reload(token); err != nil {
		log.Error().Err(err).Msg("SCM credential watcher: provider reload failed")
		return
	}

	w.lastToken = token
	log.Info().Msg("SCM credentials rotated — provider reloaded with new token")
}
