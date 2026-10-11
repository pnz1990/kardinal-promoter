//go:build e2e

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

package live

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestCLI_CompleteNames runs the shell's Tab (kardinal __complete) against
// the cluster: the Pipeline, its environments, its Bundles with their
// versions, its version tags and its gates. Without a cluster Tab completes
// nothing and offers no file names.
// Covers CLI-COMPLETE-01.
func TestCLI_CompleteNames(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	e.CreateGate(t, framework.Gate(a.ns, "hold", "prod", openExpr, recheck))
	waitPipelineValid(t, e, a.ns, pipelineName)
	b := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, b, "test", "Verified", promoteTimeout)

	tab := func(args ...string) []string {
		t.Helper()
		// __complete takes the whole command line after it, -n included.
		r := c.Exec(framework.CLIOptions{}, append([]string{"__complete", "-n", a.ns}, args...)...)
		require.Equal(t, 0, r.Code, r.Output())
		out := r.Stdout
		lines := strings.Split(strings.TrimSpace(out), "\n")
		require.Equal(t, ":36", lines[len(lines)-1], "no file names, order kept: %s", out)
		var names []string
		for _, l := range lines[:len(lines)-1] {
			names = append(names, strings.SplitN(l, "\t", 2)[0])
		}
		return names
	}
	assert.Equal(t, []string{pipelineName}, tab("why", ""))
	assert.Equal(t, []string{"test", "prod"}, tab("why", pipelineName, ""), "promotion order")
	assert.Equal(t, []string{"prod"}, tab("why", pipelineName, "p"), "the prefix typed")
	assert.Equal(t, []string{"test", "prod"}, tab("history", pipelineName, "--env", ""))
	assert.Equal(t, []string{fixtures.V2}, tab("approve", pipelineName, "prod", "--version", ""))
	assert.Equal(t, []string{b}, tab("rollback", pipelineName, "prod", "--to", ""))
	assert.Contains(t, tab("reject", ""), b)
	framework.Eventually(t, gateTimeout, "the hold gate instance completes", func(context.Context) (bool, string) {
		got := tab("override", pipelineName, "prod", "--gate", "")
		return assert.ObjectsAreEqual([]string{"hold"}, got), strings.Join(got, " ")
	})

	r := c.Exec(framework.CLIOptions{Kubeconfig: "/dev/null"}, "__complete", "why", "")
	require.Equal(t, 0, r.Code, r.Output())
	assert.Equal(t, ":36\n", r.Stdout, "no cluster: nothing, and no file names")
}
