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

// home is the scratch $HOME Isolate made, "" before.
var home string

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
//     config and install CRDs into that cluster.
//
// What the tests' tools find under the real $HOME is passed by path, as
// hack/e2e/local-live.sh does: Go's module and build caches and its env
// file, git's global config and the docker config. Calling it again is a
// no-op.
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
	keepHomeTools()
	dir, err := os.MkdirTemp("", "kardinal-unit-home-")
	if err != nil {
		dir = filepath.Dir(noHomeKube) // no scratch directory: a $HOME that does not exist
	} else {
		home = dir
	}
	_ = os.Setenv("HOME", dir)
}

// keepHomeTools sets, from the real $HOME and before Isolate replaces it,
// the paths that Go, git and docker would otherwise derive from $HOME.
func keepHomeTools() {
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
	gitConfigs := []string{filepath.Join(realHome, ".gitconfig")}
	if cfg != "" {
		gitConfigs = append(gitConfigs, filepath.Join(cfg, "git", "config"))
	}
	for _, f := range gitConfigs {
		if _, err := os.Stat(f); err == nil {
			setIfUnset("GIT_CONFIG_GLOBAL", f) // the commit identity tests' git repositories use
			break
		}
	}
	if d := filepath.Join(realHome, ".docker"); dirExists(d) {
		setIfUnset("DOCKER_CONFIG", d)
	}
	if d := filepath.Join(realHome, ".gnupg"); dirExists(d) {
		setIfUnset("GNUPGHOME", d) // git's signing key, when its config signs commits
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
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// Run is Isolate, the tests, and the scratch $HOME removed: a TestMain that
// does more than Main does calls os.Exit(kubetest.Run(m)).
func Run(m *testing.M) int {
	Isolate()
	code := m.Run()
	if home != "" {
		_ = os.RemoveAll(home)
	}
	return code
}

// Main is a package's TestMain: Isolate, then the tests.
func Main(m *testing.M) {
	os.Exit(Run(m))
}
