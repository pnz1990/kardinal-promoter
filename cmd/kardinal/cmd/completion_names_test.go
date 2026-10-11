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
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
)

var updateCompletion = flag.Bool("update-completion", false, "rewrite testdata/completion.golden")

// complete runs kardinal __complete with args, as a shell's Tab does, and
// returns its candidates (with descriptions) and directive.
func complete(t *testing.T, args ...string) string {
	t.Helper()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"__complete"}, args...))
	require.NoError(t, root.Execute())
	return out.String()
}

// TestCompletion_Names_Golden (CLI UX 1): Tab completes Pipelines, their
// environments, Bundles (with their version and phase), version tags and
// gates, from the cluster, for arguments and flags alike (the live side is
// TestCLI_CompleteNames).
func TestCompletion_Names_Golden(t *testing.T) {
	created := time.Now().Add(-time.Hour)
	b1 := whyBundle("demo-a", "1.4.0")
	b1.Status.Phase = "Verified"
	b1.CreationTimestamp.Time = created
	b2 := whyBundle("demo-b", "1.5.0")
	b2.CreationTimestamp.Time = created.Add(time.Minute)
	other := whyBundle("web-x", "9.9.9")
	other.Spec.Pipeline = "web"
	gate := explainGateInstance("demo", "demo-b", "prod", "no-weekend", "!schedule.isWeekend", false, true, "")
	uatGate := explainGateInstance("demo", "demo-b", "uat", "soak", "upstream.test.soakMinutes > 1", false, true, "")
	template := policyGate("no-weekend", "default", "prod", "!schedule.isWeekend")
	c := policyClient(t, policyPipeline("demo", "test", "uat", "prod"), policyPipeline("web", "dev", "live"), b1, b2, other, gate, uatGate, template)
	prev := buildClient
	buildClient = func() (sigs_client.Client, string, error) { return c, "default", nil }
	t.Cleanup(func() { buildClient = prev })

	cases := [][]string{
		{"why", ""},
		{"why", "demo", ""},
		{"approve", "demo", ""},
		{"approve", "demo", "prod", "--version", ""},
		{"rollback", "demo", "prod", "--to", ""},
		{"override", "demo", "prod", "--gate", ""},
		{"override", "demo", "--gate", ""},
		{"history", "--env", ""},
		{"history", "web", "--env", ""},
		{"logs", "demo", "--bundle", ""},
		{"reject", ""},
		{"diff", "demo-a", ""},
		{"delete", "bundle", ""},
		{"get", "pipelines", ""},
		{"get", "steps", ""},
		{"metrics", "--pipeline", ""},
		{"why", "demo", "prod", ""},
	}
	var golden strings.Builder
	for _, args := range cases {
		fmt.Fprintf(&golden, "$ kardinal %s<TAB>\n%s\n", strings.Join(args, " "), complete(t, args...))
	}
	path := filepath.Join("testdata", "completion.golden")
	if *updateCompletion {
		require.NoError(t, os.WriteFile(path, []byte(golden.String()), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), golden.String())
}

// TestCompletion_NoCluster: without a cluster, Tab completes nothing and
// does not offer file names; it never fails.
func TestCompletion_NoCluster(t *testing.T) {
	prev := buildClient
	buildClient = func() (sigs_client.Client, string, error) { return nil, "", errors.New("no cluster") }
	t.Cleanup(func() { buildClient = prev })
	assert.Equal(t, ":4\n", complete(t, "why", ""), "nothing, and no file names (directive 4)")
}
