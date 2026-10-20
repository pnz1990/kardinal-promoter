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
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
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

// probeFakesEnv lists the variables TestIsolate_NoCredentials started its
// child with a fake credential in.
const probeFakesEnv = "KUBETEST_PROBE_FAKES"

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

// TestIsolate_NoCredentials (#2321, QA-3 on #2358): a guarded test binary
// started with the developer's credentials in every place kubetest.Isolated
// lists (git's global, system and repository config, docker's and podman's
// registry logins, AWS, Google, Azure and GitHub credentials, a netrc, an ssh
// agent and ssh commands), each a fake, reaches none of them. Its
// environment is what Isolated says. A git push to a server that asks for a
// password calls no credential helper and sends no extra header; the
// registry keychain kardinal reads registries with answers anonymous for
// every login; the AWS default credential chain finds none, and calls no
// instance metadata.
func TestIsolate_NoCredentials(t *testing.T) {
	if os.Getenv(probeEnv) == "no-credentials" {
		noCredentials(t)
		return
	}
	marker := filepath.Join(t.TempDir(), "calls")
	bin := t.TempDir()
	helper := func(name, what, out string) string {
		p := filepath.Join(bin, name)
		require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\necho "+what+" >> \""+marker+"\"\n"+out), 0o755))
		return p
	}
	gitHelper := helper("git-helper", "git", "echo username=u\necho password=p\n")
	helper("docker-credential-kardinalprobe", "docker", `echo '{"ServerURL":"helper.example.invalid","Username":"u","Secret":"p"}'`+"\n")
	askpass := helper("askpass", "askpass", "echo p\n")
	sshCmd := helper("fake-ssh", "ssh", "exit 1\n")
	write := func(path, content string) string {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}
	podman := func(registry string) string {
		return `{"auths":{"` + registry + `":{"auth":"dTpw"}}}`
	}
	real := t.TempDir() // the developer's $HOME
	gitconfig := write(filepath.Join(real, ".gitconfig"), "[credential]\n\thelper = !"+gitHelper+"\n")
	docker := filepath.Join(real, ".docker")
	write(filepath.Join(docker, "config.json"), `{"credHelpers":{"helper.example.invalid":"kardinalprobe"},"auths":{"static.example.invalid":{"auth":"dTpw"}}}`)
	runtimeDir, configDir := t.TempDir(), t.TempDir()
	write(filepath.Join(runtimeDir, "containers", "auth.json"), podman("xdgrun.example.invalid"))
	write(filepath.Join(configDir, "containers", "auth.json"), podman("xdgcfg.example.invalid"))
	aws := t.TempDir()

	base := []string{probeEnv + "=no-credentials", probeMarkerEnv + "=" + marker, "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"REGISTRY_AUTH_FILE=" + write(filepath.Join(t.TempDir(), "auth.json"), podman("regauth.example.invalid")),
		"XDG_RUNTIME_DIR=" + runtimeDir, "XDG_CONFIG_HOME=" + configDir,
		"AWS_ACCESS_KEY_ID=AKIAFAKEFAKEFAKEFAKE", "AWS_SECRET_ACCESS_KEY=fake", "AWS_SESSION_TOKEN=fake", "AWS_PROFILE=fake",
		"AWS_SHARED_CREDENTIALS_FILE=" + write(filepath.Join(aws, "credentials"), "[fake]\naws_access_key_id = AKIAFAKEFAKEFAKEFAKE\naws_secret_access_key = fake\n"),
		"AWS_CONFIG_FILE=" + write(filepath.Join(aws, "config"), "[profile fake]\nregion = us-east-1\n"),
		"AWS_CONTAINER_CREDENTIALS_FULL_URI=http://127.0.0.1:1/creds",
		"GOOGLE_APPLICATION_CREDENTIALS=" + write(filepath.Join(t.TempDir(), "sa.json"), `{"type":"service_account"}`),
		"CLOUDSDK_CONFIG=" + t.TempDir(), "CLOUDSDK_AUTH_ACCESS_TOKEN_FILE=/nonexistent/token",
		"AZURE_CLIENT_ID=fake", "AZURE_CLIENT_SECRET=fake",
		"GH_TOKEN=fake", "GITHUB_TOKEN=fake", "NETRC=" + write(filepath.Join(t.TempDir(), "netrc"), "machine 127.0.0.1 login u password p\n"),
		"SSH_AUTH_SOCK=/nonexistent/agent.sock", "GIT_SSH_COMMAND=" + sshCmd, "GIT_SSH=" + sshCmd,
		"GIT_ASKPASS=" + askpass, "SSH_ASKPASS=" + askpass, "GNUPGHOME=" + t.TempDir()}
	for name, env := range map[string][]string{
		"in $HOME":     {"HOME=" + real},
		"by variables": {"HOME=" + t.TempDir(), "GIT_CONFIG_GLOBAL=" + gitconfig, "GIT_CONFIG_SYSTEM=" + gitconfig, "DOCKER_CONFIG=" + docker},
	} {
		env = append(append([]string{}, base...), env...)
		var fakes []string
		for _, kv := range env[2:] { // all but the probe's own
			if k, _, _ := strings.Cut(kv, "="); k != "PATH" && k != "HOME" {
				fakes = append(fakes, k)
			}
		}
		out := runChild(t, "TestIsolate_NoCredentials", append(env, probeFakesEnv+"="+strings.Join(fakes, ",")))
		assert.Contains(t, out, "--- PASS: TestIsolate_NoCredentials", "%s: the probe ran", name)
		calls, _ := os.ReadFile(marker)
		assert.Empty(t, string(calls), "%s: a credential helper was called", name)
	}
}

// noCredentials is TestIsolate_NoCredentials' child, in a guarded binary.
func noCredentials(t *testing.T) {
	home := os.Getenv("HOME")
	for _, r := range kubetest.Isolated {
		if prefix, ok := strings.CutSuffix(r.Name, "*"); ok {
			for _, kv := range os.Environ() {
				k, _, _ := strings.Cut(kv, "=")
				if strings.HasPrefix(k, prefix) && !isolatedSets(k) {
					t.Errorf("%s is set: Isolated clears %s (%s)", k, r.Name, r.Why)
				}
			}
			continue
		}
		v, set := os.LookupEnv(r.Name)
		if r.Unset {
			assert.False(t, set, "%s is set: Isolated clears it (%s)", r.Name, r.Why)
			continue
		}
		want := r.Value
		if want == "~" {
			want = home
		} else if rest, ok := strings.CutPrefix(want, "~/"); ok {
			want = filepath.Join(home, rest)
		}
		assert.Equal(t, want, v, "%s (%s)", r.Name, r.Why)
	}

	// Every variable the parent planted a fake in is cleared, or holds what
	// Isolated sets, whether or not a rule names it: a rule dropped from the
	// table fails here too.
	for _, k := range strings.Split(os.Getenv(probeFakesEnv), ",") {
		if v, set := os.LookupEnv(k); set && !isolatedSets(k) {
			t.Errorf("%s=%s survived Isolate", k, v)
		}
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv("DOCKER_CONFIG"), "config.json"))
	require.NoError(t, err, "a docker config.json, so go-containerregistry never falls back to podman's logins")
	assert.JSONEq(t, "{}", string(b))

	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			sent = append(sent, a)
		}
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
	for _, args := range [][]string{{"init", "-q"},
		// The repository's own helper and header, as actions/checkout stores
		// the job token.
		{"config", "--local", "credential.helper", "!" + filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "git-helper")},
		{"config", "--local", "http.extraheader", "Authorization: Basic ZmFrZTpmYWtl"},
		{"commit", "-q", "--allow-empty", "-m", "probe"}} {
		out, err := git(args...)
		require.NoError(t, err, "git %v: %s", args, out)
	}
	out, err := git("push", srv.URL+"/r.git", "HEAD:refs/heads/main")
	require.Error(t, err, "the server refuses every request: %s", out)
	assert.Empty(t, sent, "git sent an Authorization header: the repository's extra header or a helper's login")

	for _, registry := range []string{"helper.example.invalid", "static.example.invalid", "regauth.example.invalid",
		"xdgrun.example.invalid", "xdgcfg.example.invalid"} {
		reg, err := name.NewRegistry(registry)
		require.NoError(t, err)
		auth, err := authn.DefaultKeychain.Resolve(reg)
		require.NoError(t, err, registry)
		assert.Equal(t, authn.Anonymous, auth, "%s: no login", registry)
	}

	// The AWS default chain, which kardinal's EKS health and CloudWatch
	// metrics load. Never against the real instance metadata service: refuse
	// to run unless it is disabled, and point the chain's endpoint at a
	// local server that counts the calls.
	require.Equal(t, "true", os.Getenv("AWS_EC2_METADATA_DISABLED"), "instance metadata must be off before the chain runs")
	imdsCalls := 0
	imds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		imdsCalls++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer imds.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithEC2IMDSEndpoint(imds.URL))
	require.NoError(t, err)
	creds, err := cfg.Credentials.Retrieve(ctx)
	assert.Error(t, err, "the AWS default chain found credentials: %s", creds.Source)
	assert.Zero(t, imdsCalls, "the AWS chain asked the instance metadata service")

	calls, _ := os.ReadFile(os.Getenv(probeMarkerEnv))
	assert.Empty(t, string(calls), "no credential helper or ssh command was called")
}

// isolatedSets reports whether Isolated sets k (a rule after the prefix
// that clears it).
func isolatedSets(k string) bool {
	for _, r := range kubetest.Isolated {
		if r.Name == k && !r.Unset {
			return true
		}
	}
	return false
}
