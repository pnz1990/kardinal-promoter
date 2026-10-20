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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/kardinal-promoter/kardinal-promoter/test/kubetest"
)

// kubeconfigFile writes a kubeconfig whose cluster is server.
func kubeconfigFile(t *testing.T, path, server string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: "`+server+`"}}]
users: [{name: u, user: {token: t}}]
contexts: [{name: c, context: {cluster: c, user: u}}]
current-context: c
`), 0o600))
}

func loadDefault() (*rest.Config, error) {
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{}).ClientConfig()
}

// probeHomeEnv makes this test binary, started by TestIsolate_HomeKubeconfig
// with a $HOME holding a kubeconfig, run the probe in that test.
const probeHomeEnv = "KUBETEST_PROBE_STARTED_WITH_HOME"

// TestIsolate_HomeKubeconfig (QA-3 on #2216): a guarded test binary started
// with a ~/.kube/config finds none, even when a test clears KUBECONFIG:
// client-go's default rules, controller-runtime's GetConfig and a child
// process all see the scratch $HOME. clientcmd computed its home kubeconfig
// from the real $HOME at package init, before TestMain, so only Isolate
// resetting it (and $HOME) keeps the fallback away.
func TestIsolate_HomeKubeconfig(t *testing.T) {
	if started := os.Getenv(probeHomeEnv); started != "" {
		// The child: TestMain (kubetest.Main) isolated it.
		assert.NotEqual(t, started, os.Getenv("HOME"), "a scratch $HOME")
		assert.NoFileExists(t, filepath.Join(os.Getenv("HOME"), ".kube", "config"))
		t.Setenv("KUBECONFIG", "")
		_, err := loadDefault()
		assert.True(t, clientcmd.IsEmptyConfig(err), "KUBECONFIG cleared: still no config, not %s's: %v", started, err)
		_, err = config.GetConfig()
		assert.Error(t, err, "controller-runtime's GetConfig finds none either")
		out, err := exec.Command("sh", "-c", `cat "$HOME/.kube/config" 2>/dev/null || echo none`).Output()
		require.NoError(t, err)
		assert.Equal(t, "none\n", string(out), "a child process (kubectl, helm, kind) finds no ~/.kube/config")
		return
	}
	home := t.TempDir()
	kubeconfigFile(t, filepath.Join(home, ".kube", "config"), "https://home.invalid:443")
	cmd := exec.Command(os.Args[0], "-test.run=^TestIsolate_HomeKubeconfig$", "-test.count=1", "-test.v")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "KUBECONFIG=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+home, probeHomeEnv+"="+home)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Contains(t, string(out), "--- PASS: TestIsolate_HomeKubeconfig", "the probe ran")
}

// TestIsolate: after Isolate, client-go's default loading rules give no
// config and the in-cluster fallback fails, where before a KUBECONFIG and an
// in-cluster service address in the environment gave one. (The positive
// step uses a temporary kubeconfig: the home one is never read.)
func TestIsolate(t *testing.T) {
	kc := filepath.Join(t.TempDir(), "config")
	kubeconfigFile(t, kc, "https://cluster.invalid:443")
	t.Setenv("KUBECONFIG", kc)
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	t.Setenv("USE_EXISTING_CLUSTER", "true")
	load := loadDefault
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

	// KUBECONFIG cleared: client-go falls back to the home kubeconfig, which
	// Isolate pointed at nothing (QA-3 on #2216).
	t.Setenv("KUBECONFIG", "")
	_, err = load()
	assert.True(t, clientcmd.IsEmptyConfig(err), "no home kubeconfig: %v", err)
	assert.NoFileExists(t, clientcmd.RecommendedHomeFile)
	assert.NotEmpty(t, os.Getenv("GOMODCACHE"), "go, run by tests, keeps its module cache")
}
