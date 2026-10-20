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

// Isolate points the ambient kubeconfig of this process, and of every
// process it starts, at nothing:
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
//   - git and docker given only what the tests need (#2321): a global
//     gitconfig in the scratch $HOME with a commit identity and nothing else
//     (GIT_CONFIG_GLOBAL, and no system config), so no test reaches the
//     developer's credential helpers, and an empty DOCKER_CONFIG, so no
//     registry login either. A test that needs either writes its own and
//     points the variable at it (t.Setenv).
//
// Go's module and build caches and its env file, under the real $HOME, are
// passed by path, as hack/e2e/local-live.sh does. The scratch $HOME is the
// one HomeEnv names, when a parent gave one. Calling it again is a no-op.
func Isolate() {
	_ = os.Setenv("KUBECONFIG", NoKubeconfig)
	_ = os.Unsetenv("KUBERNETES_SERVICE_HOST")
	_ = os.Unsetenv("KUBERNETES_SERVICE_PORT")
	_ = os.Unsetenv("USE_EXISTING_CLUSTER")
	clientcmd.RecommendedConfigDir = noHomeKube
	clientcmd.RecommendedHomeFile = filepath.Join(noHomeKube, clientcmd.RecommendedFileName)
	clientcmd.RecommendedSchemaFile = filepath.Join(noHomeKube, clientcmd.RecommendedSchemaName, clientcmd.RecommendedFileName)
	if home != "" {
		return
	}
	keepGoPaths()
	dir := os.Getenv(HomeEnv)
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
	_ = os.Setenv("HOME", dir)
	gitconfig := filepath.Join(dir, ".gitconfig")
	_ = os.WriteFile(gitconfig, []byte(gitIdentity), 0o600)
	_ = os.Setenv("GIT_CONFIG_GLOBAL", gitconfig)
	_ = os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	docker := filepath.Join(dir, ".docker")
	_ = os.MkdirAll(docker, 0o700)
	_ = os.Setenv("DOCKER_CONFIG", docker)
	_ = os.Unsetenv("GNUPGHOME")
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
