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

// Package kubetest keeps unit tests away from real clusters. The CLI and the
// controller build their clients from the ambient kubeconfig ($KUBECONFIG,
// else ~/.kube/config) or, with none, from the pod's in-cluster config; a
// test that reaches that code by mistake would talk to whatever cluster the
// developer's machine points at. Main, the TestMain of every package whose
// tests can reach such code (hack/hygiene enforces it), points all of it at
// nothing before any test runs. A test that needs a cluster passes its own
// kubeconfig explicitly (t.Setenv, --kubeconfig, a fake client).
package kubetest

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

// NoKubeconfig is the KUBECONFIG every guarded test binary starts with: a
// path that does not exist, so client-go's default loading rules find no
// config at all, and never fall back to ~/.kube/config.
const NoKubeconfig = "/nonexistent/kardinal-unit-tests/kubeconfig"

// noHomeKube is where client-go's home kubeconfig points after Isolate: a
// directory that does not exist.
const noHomeKube = "/nonexistent/kardinal-unit-tests/home/.kube"

// HomeEnv names a directory a parent test hands a child process it starts
// (a test binary run again, the controller's main with -help) for the
// child's Isolate to use as its scratch $HOME: one the parent removes, so a
// child that exits before Run returns leaves nothing behind (QA-3 on #2216).
const HomeEnv = "KUBETEST_HOME"

// gitIdentity is the only git configuration a guarded test gets: the commit
// identity its repositories use. No credential helpers, signing or aliases
// from the developer's config, and no system config.
const gitIdentity = "[user]\n\tname = kardinal unit tests\n\temail = unit-tests@kardinal-promoter.invalid\n"

// home is the scratch $HOME Isolate uses, "" before; ownHome is whether
// Isolate made it (and Run removes it).
var (
	home    string
	ownHome bool
)

// EnvRule is one environment variable Isolate sets or clears.
type EnvRule struct {
	// Name is the variable; a Name ending in "*" is every variable with
	// that prefix.
	Name string
	// Value is what Isolate sets it to, "~" for the scratch $HOME and "~/"
	// a path under it. Unset clears it instead.
	Value string
	Unset bool
	// Why is what the rule keeps the tests from.
	Why string
}

// Isolated is every variable Isolate sets or clears, in the order it does
// (CONTRIBUTING.md cites it). Nothing a guarded test, or a process it starts,
// reads from them reaches a cluster or a credential of the machine it runs
// on. A test that needs one sets its own (t.Setenv) after the guard.
var Isolated = []EnvRule{
	{Name: "KUBECONFIG", Value: NoKubeconfig, Why: "the ambient kubeconfig"},
	{Name: "KUBERNETES_SERVICE_HOST", Unset: true, Why: "the in-cluster config"},
	{Name: "KUBERNETES_SERVICE_PORT", Unset: true, Why: "the in-cluster config"},
	{Name: "USE_EXISTING_CLUSTER", Unset: true, Why: "envtest taking the ambient config and installing CRDs into that cluster"},
	{Name: "HOME", Value: "~", Why: "everything tools read under the real home: ~/.kube, ~/.aws, ~/.config/gcloud, ~/.netrc, ~/.ssh"},
	// git: a commit identity, nothing else.
	{Name: "GIT_CONFIG_GLOBAL", Value: "~/.gitconfig", Why: "the developer's gitconfig: credential helpers, signing (the scratch one holds a commit identity only)"},
	{Name: "GIT_CONFIG_NOSYSTEM", Value: "1", Why: "the system gitconfig"},
	{Name: "GIT_CONFIG_SYSTEM", Unset: true, Why: "a system gitconfig named by path"},
	{Name: "GIT_CONFIG_COUNT", Value: "2", Why: "a repository's own credential.helper and http.extraheader (actions/checkout stores the job token there): command-line config, read last, resets both"},
	{Name: "GIT_CONFIG_KEY_0", Value: "credential.helper", Why: "see GIT_CONFIG_COUNT"},
	{Name: "GIT_CONFIG_VALUE_0", Value: "", Why: "an empty value resets the list of helpers"},
	{Name: "GIT_CONFIG_KEY_1", Value: "http.extraheader", Why: "see GIT_CONFIG_COUNT"},
	{Name: "GIT_CONFIG_VALUE_1", Value: "", Why: "an empty value resets the extra headers"},
	{Name: "GNUPGHOME", Unset: true, Why: "git's signing keys"},
	{Name: "GIT_ASKPASS", Unset: true, Why: "a credential prompt program"},
	{Name: "SSH_ASKPASS", Unset: true, Why: "a credential prompt program"},
	{Name: "GIT_SSH_COMMAND", Unset: true, Why: "an ssh command with the developer's keys"},
	{Name: "GIT_SSH", Unset: true, Why: "an ssh command with the developer's keys"},
	{Name: "SSH_AUTH_SOCK", Unset: true, Why: "the ssh agent's keys (go-git falls back to it when no key is given)"},
	// Registries: docker's config, and podman's, which go-containerregistry
	// falls back to when there is no docker config.json.
	{Name: "DOCKER_CONFIG", Value: "~/.docker", Why: "docker's registry logins and credential helpers (its config.json is {})"},
	{Name: "REGISTRY_AUTH_FILE", Unset: true, Why: "podman's and skopeo's registry logins"},
	{Name: "XDG_RUNTIME_DIR", Value: "~/.xdg-runtime", Why: "containers/auth.json registry logins (podman, skopeo, buildah)"},
	{Name: "XDG_CONFIG_HOME", Value: "~/.config", Why: "containers/auth.json registry logins, and other tools' config"},
	// Cloud and forge credentials.
	{Name: "AWS_*", Unset: true, Why: "AWS credentials, profiles, config and credentials files, container credentials"},
	{Name: "AWS_EC2_METADATA_DISABLED", Value: "true", Why: "the instance metadata service's role credentials (on an EC2 host)"},
	{Name: "GOOGLE_APPLICATION_CREDENTIALS", Unset: true, Why: "a Google service account key"},
	{Name: "CLOUDSDK_*", Unset: true, Why: "gcloud's config and access tokens"},
	{Name: "AZURE_*", Unset: true, Why: "Azure client secrets and certificates"},
	{Name: "GH_TOKEN", Unset: true, Why: "the gh CLI's and GitHub clients' token"},
	{Name: "GITHUB_TOKEN", Unset: true, Why: "a GitHub token"},
	{Name: "NETRC", Unset: true, Why: "a .netrc of logins (the go command reads it)"},
}

// Isolate points the ambient kubeconfig of this process, and of every
// process it starts, at nothing, and keeps the machine's credentials from
// them (Isolated):
//   - KUBECONFIG at NoKubeconfig;
//   - $HOME at an empty scratch directory, and client-go's home kubeconfig
//     (clientcmd.RecommendedHomeFile, computed from the real $HOME when
//     clientcmd initialized, before any TestMain) at a path that does not
//     exist, so a test that clears KUBECONFIG, a child kubectl, helm or kind,
//     and controller-runtime's GetConfig find no ~/.kube/config either (QA-3
//     on #2216);
//   - no in-cluster service address, so the in-cluster fallback fails too;
//   - no USE_EXISTING_CLUSTER, with which envtest would take the ambient
//     config and install CRDs into that cluster;
//   - git, registries, the clouds and GitHub given nothing of the
//     developer's (#2321): a scratch gitconfig with a commit identity and
//     no system config, repository credential helpers reset, an empty
//     docker config, no podman logins, no AWS, Google, Azure or GitHub
//     credentials, no instance metadata and no ssh agent.
//
// Go's module and build caches and its env file, under the real $HOME, are
// passed by path, as hack/e2e/local-live.sh does. The scratch $HOME is the
// one HomeEnv names, when a parent gave one. Calling it again applies
// Isolated again, in the same scratch $HOME.
func Isolate() {
	clientcmd.RecommendedConfigDir = noHomeKube
	clientcmd.RecommendedHomeFile = filepath.Join(noHomeKube, clientcmd.RecommendedFileName)
	clientcmd.RecommendedSchemaFile = filepath.Join(noHomeKube, clientcmd.RecommendedSchemaName, clientcmd.RecommendedFileName)
	dir := home
	if dir == "" {
		keepGoPaths()
		dir = os.Getenv(HomeEnv)
		if !dirExists(dir) {
			var err error
			if dir, err = os.MkdirTemp("", "kardinal-unit-home-"); err != nil {
				dir = filepath.Dir(noHomeKube) // no scratch directory: a $HOME that does not exist
			} else {
				ownHome = true
			}
		}
		if dirExists(dir) {
			home = dir
		}
		_ = os.WriteFile(filepath.Join(dir, ".gitconfig"), []byte(gitIdentity), 0o600)
		for _, d := range []string{".docker", ".xdg-runtime", ".config"} {
			_ = os.MkdirAll(filepath.Join(dir, d), 0o700)
		}
		_ = os.WriteFile(filepath.Join(dir, ".docker", "config.json"), []byte("{}\n"), 0o600)
	}
	for _, r := range Isolated {
		if prefix, ok := strings.CutSuffix(r.Name, "*"); ok {
			for _, kv := range os.Environ() {
				if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, prefix) {
					_ = os.Unsetenv(k)
				}
			}
			continue
		}
		if r.Unset {
			_ = os.Unsetenv(r.Name)
			continue
		}
		v := r.Value
		if v == "~" {
			v = dir
		} else if rest, ok := strings.CutPrefix(v, "~/"); ok {
			v = filepath.Join(dir, rest)
		}
		_ = os.Setenv(r.Name, v)
	}
}

// keepGoPaths sets, from the real $HOME and before Isolate replaces it,
// the paths that Go would otherwise derive from $HOME.
func keepGoPaths() {
	realHome, err := os.UserHomeDir()
	if err != nil {
		return
	}
	cfg, _ := os.UserConfigDir()
	cache, _ := os.UserCacheDir()
	goenv := os.Getenv("GOENV")
	if goenv == "" && cfg != "" {
		goenv = filepath.Join(cfg, "go", "env")
	}
	file := goEnvFile(goenv)
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		if v := file[k]; v != "" {
			return v
		}
		return def
	}
	gopath := get("GOPATH", filepath.Join(realHome, "go"))
	setIfUnset("GOENV", goenv)
	setIfUnset("GOPATH", gopath)
	setIfUnset("GOMODCACHE", get("GOMODCACHE", filepath.Join(filepath.SplitList(gopath)[0], "pkg", "mod")))
	if cache != "" {
		setIfUnset("GOCACHE", get("GOCACHE", filepath.Join(cache, "go-build")))
	}
}

// goEnvFile is the go env file's KEY=VALUE lines (go env -w).
func goEnvFile(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func setIfUnset(k, v string) {
	if _, set := os.LookupEnv(k); !set && v != "" {
		_ = os.Setenv(k, v)
	}
}

func dirExists(p string) bool {
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// Run is Isolate, the tests, and the scratch $HOME removed: a TestMain that
// does more than Main does calls os.Exit(kubetest.Run(m)).
func Run(m *testing.M) int {
	Isolate()
	code := m.Run()
	if ownHome {
		_ = os.RemoveAll(home)
	}
	return code
}

// Main is a package's TestMain: Isolate, then the tests.
func Main(m *testing.M) {
	os.Exit(Run(m))
}
