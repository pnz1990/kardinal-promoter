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

package kubetest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kardinal-promoter/kardinal-promoter/test/kubetest"
)

// TestIsolate: after Isolate, client-go's default loading rules give no
// config and the in-cluster fallback fails, where before a KUBECONFIG and an
// in-cluster service address in the environment gave one. (The positive
// step uses a temporary kubeconfig: the home one is never read.)
func TestIsolate(t *testing.T) {
	kc := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(kc, []byte(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: "https://cluster.invalid:443"}}]
users: [{name: u, user: {token: t}}]
contexts: [{name: c, context: {cluster: c, user: u}}]
current-context: c
`), 0o600))
	t.Setenv("KUBECONFIG", kc)
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	t.Setenv("USE_EXISTING_CLUSTER", "true")
	load := func() (*rest.Config, error) {
		return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{}).ClientConfig()
	}
	cfg, err := load()
	require.NoError(t, err)
	assert.Equal(t, "https://cluster.invalid:443", cfg.Host, "before: the ambient KUBECONFIG")

	kubetest.Isolate()
	assert.Equal(t, kubetest.NoKubeconfig, os.Getenv("KUBECONFIG"))
	_, err = load()
	assert.True(t, clientcmd.IsEmptyConfig(err), "no config at all: %v", err)
	_, err = rest.InClusterConfig()
	assert.Error(t, err, "no in-cluster address either")
	_, set := os.LookupEnv("USE_EXISTING_CLUSTER")
	assert.False(t, set, "envtest starts its own API server")
}
