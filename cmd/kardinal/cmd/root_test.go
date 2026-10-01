// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func executeRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	oldOutput := globalOutput
	t.Cleanup(func() { globalOutput = oldOutput })
	root := NewRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

// E2E-15: an unknown subcommand of a group command is an error, not help
// with exit 0.
func TestGroupCommands_UnknownSubcommandFails(t *testing.T) {
	cases := []struct {
		args    []string
		wantErr string
	}{
		{[]string{"get", "gates"}, `unknown command "gates" for "kardinal get"`},
		{[]string{"get", "stepz"}, "Did you mean this?\n\tsteps"},
		{[]string{"policy", "bogus"}, `unknown command "bogus" for "kardinal policy"`},
		{[]string{"create", "x"}, `unknown command "x" for "kardinal create"`},
		{[]string{"delete", "x"}, `unknown command "x" for "kardinal delete"`},
		{[]string{"audit", "x"}, `unknown command "x" for "kardinal audit"`},
		{[]string{"nosuch"}, `unknown command "nosuch" for "kardinal"`},
	}
	for _, tc := range cases {
		t.Run(tc.args[0]+" "+tc.args[len(tc.args)-1], func(t *testing.T) {
			_, err := executeRoot(t, tc.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// cobra's MarkFlagRequired refuses a missing flag but not an empty value, so
// the commands check the value too, before they connect to a cluster.
func TestRequiredFlags_EmptyValueRefused(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	cases := []struct {
		args    []string
		wantErr string
	}{
		{[]string{"override", "p", "--gate", "g", "--reason", ""}, "--reason is required for override (audit record)"},
		{[]string{"override", "p", "--gate", "", "--reason", "r"}, "--gate is required"},
		{[]string{"promote", "p", "--env", ""}, "--env is required"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, err := executeRoot(t, tc.args...)
			require.Error(t, err)
			assert.Equal(t, tc.wantErr, err.Error())
		})
	}
}

func TestGroupCommands_NoArgsShowsHelp(t *testing.T) {
	out, err := executeRoot(t, "get")
	require.NoError(t, err)
	assert.Contains(t, out, "Available Commands:")
	assert.Contains(t, out, "bundles")
}

// C09a-cli-18 / C09b-cli-24: -o is validated, and json/yaml fail on commands
// that only print tables instead of being ignored.
func TestOutputFlag_Validated(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"unknown format", []string{"version", "-o", "xml"}, `invalid -o "xml": must be table, json or yaml`},
		{"json on a table command", []string{"completion", "zsh", "-o", "json"},
			`-o json is not supported by "kardinal completion"`},
		{"yaml on history", []string{"history", "demo", "-o", "yaml"},
			`-o yaml is not supported by "kardinal history"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := executeRoot(t, tc.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	out, err := executeRoot(t, "completion", "zsh", "-o", "table")
	require.NoError(t, err)
	assert.Contains(t, out, "#compdef kardinal")
}

func TestOutputFlag_StructuredCommands(t *testing.T) {
	root := NewRootCmd()
	for _, path := range [][]string{
		{"get", "bundles"}, {"get", "pipelines"}, {"get", "steps"}, {"get", "subscriptions"},
		{"get", "auditevents"},
	} {
		c, _, err := root.Find(path)
		require.NoError(t, err)
		globalOutput = "json"
		assert.NoError(t, checkOutputFlag(c), c.CommandPath())
	}
	globalOutput = ""
}

// C09b-cli-22: refresh only annotates the Pipeline, and dashboard only opens a
// URL; the help says so.
func TestRefreshAndDashboard(t *testing.T) {
	c := policyClient(t, policyPipeline("demo", "test"))
	var buf bytes.Buffer
	require.NoError(t, refreshFn(&buf, c, "default", "demo"))
	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "demo", Namespace: "default"}, &p))
	assert.NotEmpty(t, p.Annotations["kardinal.io/refresh"])
	require.Error(t, refreshFn(&buf, c, "default", "missing"))

	buf.Reset()
	require.NoError(t, dashboardFn(&buf, "", true))
	assert.Equal(t, "kardinal UI: http://localhost:8082/ui/\n", buf.String())

	refreshHelp, dashboardHelp := newRefreshCmd().Long, newDashboardCmd().Long
	assert.Contains(t, refreshHelp, "does not re-evaluate gates")
	assert.NotContains(t, refreshHelp, "re-check health adapters")
	assert.Contains(t, dashboardHelp, "it does not\nport-forward")
	assert.NotContains(t, dashboardHelp, "Uses port-forwarding")
}
