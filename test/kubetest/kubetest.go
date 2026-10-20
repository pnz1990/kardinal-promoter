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
// tests can reach such code (hack/hygiene enforces it), points both at
// nothing before any test runs. A test that needs a cluster passes its own
// kubeconfig explicitly (t.Setenv, --kubeconfig, a fake client).
package kubetest

import (
	"os"
	"testing"
)

// NoKubeconfig is the KUBECONFIG every guarded test binary starts with: a
// path that does not exist, so client-go's default loading rules find no
// config at all, and never fall back to ~/.kube/config.
const NoKubeconfig = "/nonexistent/kardinal-unit-tests/kubeconfig"

// Isolate points the ambient kubeconfig of this process, and of every
// process it starts, at nothing: KUBECONFIG at NoKubeconfig, no in-cluster
// service address, so the in-cluster fallback fails too, and no
// USE_EXISTING_CLUSTER, with which envtest would take the ambient config and
// install CRDs into that cluster.
func Isolate() {
	_ = os.Setenv("KUBECONFIG", NoKubeconfig)
	_ = os.Unsetenv("KUBERNETES_SERVICE_HOST")
	_ = os.Unsetenv("KUBERNETES_SERVICE_PORT")
	_ = os.Unsetenv("USE_EXISTING_CLUSTER")
}

// Main is a package's TestMain: Isolate, then the tests.
func Main(m *testing.M) {
	Isolate()
	os.Exit(m.Run())
}
