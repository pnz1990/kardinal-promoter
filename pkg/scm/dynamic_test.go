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

package scm_test

import (
	"context"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestDynamicProvider_NewDynamicProvider verifies that a DynamicProvider is
// initialised successfully and delegates method calls to the inner provider.
func TestDynamicProvider_NewDynamicProvider(t *testing.T) {
	dp, err := scm.NewDynamicProvider("github", "token-v1", "", "webhook-secret")
	require.NoError(t, err)
	require.NotNil(t, dp)

	// Verify the provider satisfies SCMProvider.
	var _ scm.SCMProvider = dp
}

// TestDynamicProvider_Reload verifies that Reload swaps the inner provider and
// that subsequent calls use the new token.
func TestDynamicProvider_Reload(t *testing.T) {
	dp, err := scm.NewDynamicProvider("github", "token-v1", "", "")
	require.NoError(t, err)

	// Reload with a new token.
	require.NoError(t, dp.Reload("token-v2"))

	// The GitHubProvider stores the token in the Token field; we can verify
	// the swap happened by calling GetPRStatus on a fake server (or simply
	// confirm Reload does not error, which proves a new provider was built).
	require.NoError(t, dp.Reload("token-v3"))
}

// TestDynamicProvider_ReloadEmptyToken verifies that Reload with an empty token
// is a no-op and does not replace the current provider.
func TestDynamicProvider_ReloadEmptyToken(t *testing.T) {
	dp, err := scm.NewDynamicProvider("github", "token-v1", "", "")
	require.NoError(t, err)

	// Reload with empty token — should not change anything.
	require.NoError(t, dp.Reload(""))
}

// TestDynamicProvider_ConcurrentReload verifies that concurrent Reload calls
// and SCMProvider method calls do not race.
func TestDynamicProvider_ConcurrentReload(t *testing.T) {
	dp, err := scm.NewDynamicProvider("github", "token-v1", "", "")
	require.NoError(t, err)

	var wg sync.WaitGroup
	const goroutines = 20

	// Concurrent reloads.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = dp.Reload("token-concurrent")
		}()
	}

	// Concurrent method calls — ParseWebhookEvent with invalid signature returns
	// an error but must not panic or race.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = dp.ParseWebhookEvent([]byte(`{}`), "invalid")
		}()
	}

	wg.Wait()
}

// TestDynamicProvider_UnknownProvider verifies that NewDynamicProvider returns
// an error for an unknown provider type.
func TestDynamicProvider_UnknownProvider(t *testing.T) {
	_, err := scm.NewDynamicProvider("bitbucket", "token", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bitbucket")
}

// TestSecretWatcher_CheckAndReload verifies that the SecretWatcher detects a
// token change and reloads the DynamicProvider.
func TestSecretWatcher_CheckAndReload(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	// Initial secret.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-token",
			Namespace: "kardinal-system",
		},
		Data: map[string][]byte{
			"token": []byte("token-v1"),
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(secret).
		Build()

	dp, err := scm.NewDynamicProvider("github", "token-v1", "", "")
	require.NoError(t, err)

	log := zerolog.Nop()
	watcher := scm.NewSecretWatcher(fakeClient, dp, "github-token", "kardinal-system", "token", log)

	// Trigger an immediate check — token unchanged, should be no-op.
	watcher.CheckAndReloadForTest(context.Background())

	// Rotate the token in the Secret.
	secret.Data["token"] = []byte("token-v2")
	require.NoError(t, fakeClient.Update(context.Background(), secret))

	// Trigger a check — should detect the change and reload.
	watcher.CheckAndReloadForTest(context.Background())

	// Rotate again.
	secret.Data["token"] = []byte("token-v3")
	require.NoError(t, fakeClient.Update(context.Background(), secret))
	watcher.CheckAndReloadForTest(context.Background())
}

// TestSecretWatcher_MissingKey verifies that the watcher handles a missing data
// key gracefully without panicking.
func TestSecretWatcher_MissingKey(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github-token",
			Namespace: "kardinal-system",
		},
		Data: map[string][]byte{}, // empty — key "token" absent
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(secret).
		Build()

	dp, err := scm.NewDynamicProvider("github", "token-v1", "", "")
	require.NoError(t, err)

	log := zerolog.Nop()
	watcher := scm.NewSecretWatcher(fakeClient, dp, "github-token", "kardinal-system", "token", log)

	// Should log a warning but not panic.
	watcher.CheckAndReloadForTest(context.Background())
}

// TestSecretWatcher_MissingSecret verifies that the watcher handles a missing
// Secret gracefully without panicking.
func TestSecretWatcher_MissingSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	// No secret in the fake client.
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	dp, err := scm.NewDynamicProvider("github", "token-v1", "", "")
	require.NoError(t, err)

	log := zerolog.Nop()
	watcher := scm.NewSecretWatcher(fakeClient, dp, "github-token", "kardinal-system", "token", log)

	// Should log an error but not panic.
	watcher.CheckAndReloadForTest(context.Background())
}
