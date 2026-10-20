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
	"net/http"
	"net/http/httptest"
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
	keepStyle(t)
	created := time.Now().Add(-time.Hour)
	b1 := whyBundle("demo-a", "1.4.0")
	b1.Status.Phase = "Verified"
	b1.CreationTimestamp.Time = created
	b2 := whyBundle("demo-b", "1.5.0")
	b2.CreationTimestamp.Time = created.Add(time.Minute)
	rejected := whyBundle("demo-r", "1.4.9")
	rejected.Status.Phase = "Rejected"
	rejected.CreationTimestamp.Time = created.Add(-time.Minute)
	other := whyBundle("web-x", "9.9.9")
	other.Spec.Pipeline = "web"
	gate := explainGateInstance("demo", "demo-b", "prod", "no-weekend", "!schedule.isWeekend", false, true, "")
	uatGate := explainGateInstance("demo", "demo-b", "uat", "soak", "upstream.test.soakMinutes > 1", false, true, "")
	template := policyGate("no-weekend", "default", "prod", "!schedule.isWeekend")
	c := policyClient(t, policyPipeline("demo", "test", "uat", "prod"), policyPipeline("web", "dev", "live"), b1, b2, rejected, other, gate, uatGate, template)
	prev := completionClient
	completionClient = func() (sigs_client.Client, string, error) { return c, "default", nil }
	t.Cleanup(func() { completionClient = prev })

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
		{"why", "de"},
		{"why", "demo", "u"},
		{"approve", "demo", "prod", "--version", "1.4"},
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

// TestCompletion_HungServer (QA #1762): an API server that accepts the
// connection and never answers (discovery included) does not freeze the
// shell: Tab gives up after the completion timeout with no names.
func TestCompletion_HungServer(t *testing.T) {
	keepStyle(t)
	release := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	path := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- {name: hung, cluster: {server: %q, insecure-skip-tls-verify: true}}
contexts:
- {name: hung, context: {cluster: hung, user: u}}
users:
- {name: u, user: {token: t}}
current-context: hung
`, srv.URL)), 0o600))
	oldK := globalKubeconfig
	globalKubeconfig = path
	t.Cleanup(func() { globalKubeconfig = oldK })
	// The bound, shortened so that the test does not wait three seconds.
	setForTest(t, &completionTimeout, 300*time.Millisecond)
	start := time.Now()
	out := complete(t, "--kubeconfig", path, "why", "")
	assert.Less(t, time.Since(start), completionTimeout+time.Second, "Tab gives up in time")
	assert.Equal(t, ":36\n", out, "no names; no file names, order kept")
}

// TestCompletion_NoCluster: without a cluster, Tab completes nothing and
// does not offer file names; it never fails.
func TestCompletion_NoCluster(t *testing.T) {
	keepStyle(t)
	prev := completionClient
	completionClient = func() (sigs_client.Client, string, error) { return nil, "", errors.New("no cluster") }
	t.Cleanup(func() { completionClient = prev })
	assert.Equal(t, ":36\n", complete(t, "why", ""), "nothing, no file names, order kept (directives 4|32)")
}

// TestArgKindOf: an argument's kind comes from its token in Use; for
// alternatives such as <name|version> the first one names it, so renaming an
// argument to accept a version keeps its completion.
func TestArgKindOf(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cmd, token string
		want       argKind
	}{
		{"why", "<pipeline>", argPipeline},
		{"why", "[env]", argEnv},
		{"bundle", "<name>", argBundle},
		{"bundle", "<name|version>", argBundle},
		{"reject", "<bundle|version>", argBundle},
		{"diff", "<bundle-a|version>", argBundle},
		{"pipelines", "[name]", argPipeline},
		{"pipelines", "[name|other]", argPipeline},
		{"logs", "<version>", argNone},
	} {
		assert.Equal(t, tc.want, argKindOf(tc.cmd, tc.token), "%s %s", tc.cmd, tc.token)
	}
}
