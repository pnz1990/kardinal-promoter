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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
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

// probeEnv makes this test binary, started by one of the probes below, run
// that probe's child part; its value names the probe.
const probeEnv = "KUBETEST_PROBE"

// probeMarkerEnv is the file the probes' fake credential helpers append a
// line to each time they are called.
const probeMarkerEnv = "KUBETEST_PROBE_MARKER"

// runChild runs this test binary's test name with only env (and nothing of
// this process's environment) and returns its output.
func runChild(t *testing.T, name string, env []string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1", "-test.v")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return string(out)
}

// TestIsolate_GivenHome (QA-3 on #2216): a child whose parent hands it a home
// (kubetest.HomeEnv) takes it as its scratch $HOME, so a child that exits
// before Run returns, as the controller's main with -help does, leaves no
// scratch home behind; without one it does.
func TestIsolate_GivenHome(t *testing.T) {
	if os.Getenv(probeEnv) == "given-home" {
		fmt.Println("HOME=" + os.Getenv("HOME"))
		os.Exit(0) // as main does: Run never removes a scratch home
	}
	for _, given := range []bool{true, false} {
		tmp, home := t.TempDir(), t.TempDir()
		env := []string{probeEnv + "=given-home", "TMPDIR=" + tmp, "PATH=" + os.Getenv("PATH"), "HOME=" + home}
		if given {
			env = append(env, kubetest.HomeEnv+"="+home)
		}
		out := runChild(t, "TestIsolate_GivenHome", env)
		leaked, err := filepath.Glob(filepath.Join(tmp, "kardinal-unit-home-*"))
		require.NoError(t, err)
		if given {
			assert.Empty(t, leaked, "a given home: no scratch home of the child's own")
			assert.Contains(t, out, "HOME="+home)
			assert.FileExists(t, filepath.Join(home, ".gitconfig"))
		} else {
			assert.Len(t, leaked, 1, "no given home: the child's own scratch home outlives it")
		}
	}
}

// TestIsolate_NoCredentials (#2321): a guarded test binary started with the
// developer's git credential helper and docker logins, in $HOME or named by
// GIT_CONFIG_GLOBAL, GIT_CONFIG_SYSTEM and DOCKER_CONFIG, reaches none of
// them. A git push to a server that asks for a password calls no helper
// (and the commit before it still has an identity), and the registry
// keychain kardinal reads registries with answers anonymous, for a registry
// with a credential helper and for one with a stored login.
func TestIsolate_NoCredentials(t *testing.T) {
	if os.Getenv(probeEnv) == "no-credentials" {
		noCredentials(t)
		return
	}
	marker := filepath.Join(t.TempDir(), "calls")
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git-helper"), []byte(
		"#!/bin/sh\necho git >> \""+marker+"\"\necho username=u\necho password=p\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker-credential-kardinalprobe"), []byte(
		"#!/bin/sh\necho docker >> \""+marker+"\"\necho '{\"ServerURL\":\"helper.example.invalid\",\"Username\":\"u\",\"Secret\":\"p\"}'\n"), 0o755))
	real := t.TempDir() // the developer's $HOME
	gitconfig := filepath.Join(real, ".gitconfig")
	require.NoError(t, os.WriteFile(gitconfig, []byte("[credential]\n\thelper = !"+filepath.Join(bin, "git-helper")+"\n"), 0o600))
	docker := filepath.Join(real, ".docker")
	require.NoError(t, os.MkdirAll(docker, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(docker, "config.json"), []byte(
		`{"credHelpers":{"helper.example.invalid":"kardinalprobe"},"auths":{"static.example.invalid":{"auth":"dTpw"}}}`), 0o600))

	base := []string{probeEnv + "=no-credentials", probeMarkerEnv + "=" + marker, "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	for name, env := range map[string][]string{
		"in $HOME":     {"HOME=" + real},
		"by variables": {"HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=" + gitconfig, "GIT_CONFIG_SYSTEM=" + gitconfig, "DOCKER_CONFIG=" + docker},
	} {
		out := runChild(t, "TestIsolate_NoCredentials", append(append([]string{}, base...), env...))
		assert.Contains(t, out, "--- PASS: TestIsolate_NoCredentials", "%s: the probe ran", name)
		calls, _ := os.ReadFile(marker)
		assert.Empty(t, string(calls), "%s: a credential helper was called", name)
	}
}

// noCredentials is TestIsolate_NoCredentials' child, in a guarded binary.
func noCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="probe"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	repo := t.TempDir()
	git := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		return cmd.CombinedOutput()
	}
	out, err := git("init", "-q")
	require.NoError(t, err, "%s", out)
	out, err = git("commit", "-q", "--allow-empty", "-m", "probe")
	require.NoError(t, err, "a commit identity: %s", out)
	out, err = git("push", srv.URL+"/r.git", "HEAD:refs/heads/main")
	require.Error(t, err, "the server refuses every request: %s", out)

	for _, registry := range []string{"helper.example.invalid", "static.example.invalid"} {
		reg, err := name.NewRegistry(registry)
		require.NoError(t, err)
		auth, err := authn.DefaultKeychain.Resolve(reg)
		require.NoError(t, err, registry)
		assert.Equal(t, authn.Anonymous, auth, "%s: no login", registry)
	}
	calls, _ := os.ReadFile(os.Getenv(probeMarkerEnv))
	assert.Empty(t, string(calls), "no credential helper was called")
}
