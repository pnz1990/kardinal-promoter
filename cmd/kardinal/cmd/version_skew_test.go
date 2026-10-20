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

package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// TestVersionSkewWarning (#1757): a warning only when the CLI and the
// controller are a minor (or major) version apart; never for a patch, a
// pre-release of the same minor, a dev build of either, or an unknown
// controller. It says which side to upgrade, and how.
//
// Covers CLI-VERSION-03.
func TestVersionSkewWarning(t *testing.T) {
	t.Parallel()
	const helm = "helm upgrade --install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter"
	cases := []struct {
		name, cli, controller, goos string
		want                        string
	}{
		{name: "equal", cli: "v0.13.0", controller: "v0.13.0"},
		{name: "patch apart", cli: "v0.13.2", controller: "v0.13.0"},
		{name: "same minor, a pre-release", cli: "v0.13.0-rc.1", controller: "v0.13.1"},
		{name: "controller without the v", cli: "v0.13.0", controller: "0.13.4"},
		{name: "CLI dev build", cli: "v0.1.0-dev", controller: "v0.12.0"},
		{name: "CLI from go install at a commit", cli: "v0.12.1-0.20261017120000-abcdef123456", controller: "v0.10.0"},
		{name: "CLI not a version", cli: "(devel)", controller: "v0.12.0"},
		{name: "controller dev build", cli: "v0.13.0", controller: "v0.1.0-dev"},
		{name: "controller unknown", cli: "v0.13.0", controller: "unknown"},
		{name: "controller not a version", cli: "v0.13.0", controller: "main"},
		{name: "CLI a minor newer", cli: "v0.13.0", controller: "v0.12.1",
			want: "warning: kardinal CLI v0.13.0 talks to controller v0.12.1 (in kardinal-system); some output may not match: " +
				"upgrade the controller: " + helm + " --version 0.13.0 --namespace kardinal-system --create-namespace"},
		{name: "CLI a minor older", cli: "v0.11.0", controller: "v0.12.1", goos: "darwin",
			want: "warning: kardinal CLI v0.11.0 talks to controller v0.12.1 (in kardinal-system); some output may not match: " +
				"upgrade the CLI: curl -Lo kardinal https://github.com/pnz1990/kardinal-promoter/releases/download/v0.12.1/kardinal-darwin-arm64"},
		{name: "CLI older, on windows", cli: "v0.11.0", controller: "v0.12.1", goos: "windows",
			want: "warning: kardinal CLI v0.11.0 talks to controller v0.12.1 (in kardinal-system); some output may not match: " +
				"upgrade the CLI: curl -Lo kardinal.exe https://github.com/pnz1990/kardinal-promoter/releases/download/v0.12.1/kardinal-windows-arm64.exe"},
		{name: "a major apart", cli: "v1.0.0", controller: "v0.14.0",
			want: "warning: kardinal CLI v1.0.0 talks to controller v0.14.0 (in kardinal-system); some output may not match: " +
				"upgrade the controller: " + helm + " --version 1.0.0 --namespace kardinal-system --create-namespace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			goos := tc.goos
			if goos == "" {
				goos = "linux"
			}
			assert.Equal(t, tc.want, versionSkewWarning(tc.cli, tc.controller, "kardinal-system", goos, "arm64"))
		})
	}
}

// TestVersionSkew_Commands (#1757): a command that talks to the cluster
// warns once on stderr, however many clients it builds; -o json and yaml,
// kardinal version and a dev build do not; --controller-namespace names the
// controller whose version is compared.
//
// Covers CLI-VERSION-03.
func TestVersionSkew_Commands(t *testing.T) {
	keepStyle(t)
	versionCM := func(ns, v string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "kardinal-version"}, Data: map[string]string{"version": v}}
	}
	c := fake.NewClientBuilder().WithScheme(rootScheme).
		WithObjects(versionCM("kardinal-system", "v0.12.0"), versionCM("ops", "v0.13.1")).Build()
	setForTest(t, &newClient, func() (sigs_client.Client, string, error) { return c, "default", nil })
	run := func(t *testing.T, args ...string) string {
		t.Helper()
		t.Cleanup(func() { globalOutput, globalNamespace = "", "" })
		root := NewRootCmd()
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetArgs(args)
		_ = root.Execute()
		return errb.String()
	}
	const warning = "warning: kardinal CLI v0.13.0 talks to controller v0.12.0 (in kardinal-system)"

	setForTest(t, &CLIVersion, "v0.13.0")
	assert.Equal(t, 1, strings.Count(run(t, "get", "pipelines"), warning), "a table command warns once")
	assert.NotContains(t, run(t, "get", "pipelines", "-o", "json"), "warning: kardinal CLI", "-o json")
	assert.NotContains(t, run(t, "get", "pipelines", "-o", "yaml"), "warning: kardinal CLI", "-o yaml")
	assert.NotContains(t, run(t, "version"), "warning: kardinal CLI", "version prints both versions itself")
	assert.NotContains(t, run(t, "status", "--controller-namespace", "ops"), "warning: kardinal CLI",
		"--controller-namespace: that controller is v0.13.1")

	// One warning per process, however many clients it builds (the MCP
	// server builds one per tool call).
	var errb bytes.Buffer
	root := NewRootCmd()
	root.SetErr(&errb)
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"get", "pipelines"})
	t.Cleanup(func() { globalOutput, globalNamespace = "", "" })
	require.NoError(t, root.Execute())
	for range 3 {
		_, _, err := buildClient()
		require.NoError(t, err)
	}
	assert.Equal(t, 1, strings.Count(errb.String(), warning))

	setForTest(t, &CLIVersion, "v0.1.0-dev")
	assert.NotContains(t, run(t, "get", "pipelines"), "warning: kardinal CLI", "a dev build")
}
