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
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// mcpAgent is the identity the MCP tests run as (mcpClient's stubIdentity).
const mcpAgent = "system:serviceaccount:agents:fixer"

// mcpIndex is a ready AgentIndex that registers mcpAgent as agent fixer,
// sponsored by alice, allowed on pipelines.
func mcpIndex(pipelines ...string) *v1alpha1.AgentIndex {
	idx := &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}}
	idx.Status.Users = map[string]v1alpha1.AgentIndexEntry{mcpAgent: {Agent: "fixer", Sponsor: "alice@example.com",
		Namespaces: []string{"*"}, Pipelines: pipelines, Environments: []string{"*"}, Autonomy: "approve"}}
	meta.SetStatusCondition(&idx.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Built"})
	return idx
}

// mcpCompactIndex is mcpIndex as a later release writes it (#2071): the
// entry in the compact format, Ready False (CompactFormat), CompactReady
// True, and in the legacy Groups the guard that release keeps for its own
// older readers, which every authenticated requester matches as a blocked
// agent.
func mcpCompactIndex(pipelines ...string) *v1alpha1.AgentIndex {
	idx := &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}}
	idx.Status.CompactUsers = map[string]string{mcpAgent: "fixer\x7falice@example.com\x7fapprove\x7f*\x7f" +
		strings.Join(pipelines, "\x1f") + "\x7f*\x7f"}
	idx.Status.Groups = map[string]v1alpha1.AgentIndexEntry{"system:authenticated": {Agent: "agent-index-in-a-newer-format",
		Sponsor: "upgrade-kardinal-to-read-it", Blocked: "kardinal's agent index is in the compact format of a newer release"}}
	meta.SetStatusCondition(&idx.Status.Conditions, metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "CompactFormat"})
	meta.SetStatusCondition(&idx.Status.Conditions, metav1.Condition{Type: "CompactReady", Status: metav1.ConditionTrue, Reason: "Built"})
	return idx
}

// TestMCP_StampsAgainstACompactIndex (#2071): against the compact index of
// a later release, this CLI stamps the agent as against its own index (the
// later release's guard for older readers is not read), and refuses it out
// of the scope the compact entry names, before writing anything.
// Covers ACTOR-INDEX-04.
func TestMCP_StampsAgainstACompactIndex(t *testing.T) {
	t.Parallel()
	opts := func() mcpOptions {
		return mcpOptions{allowWrite: true,
			identity: fixedIdentity(Identity{Username: mcpAgent, Groups: []string{"system:authenticated", "system:serviceaccounts"}})}
	}
	cs, c := mcpClient(t, opts(), nil, append(mcpFixture(), mcpCompactIndex("*"))...)
	res, body := call(t, cs, "ship", map[string]any{"pipeline": "app", "images": []string{"ghcr.io/acme/app:3.0"}})
	require.False(t, res.IsError, "%v", body)
	created := mcpNewBundles(t, c)
	require.Len(t, created, 1)
	got := created[0].Spec.Actor
	require.NotNil(t, got)
	assert.Equal(t, v1alpha1.Actor{Kind: "agent", Principal: mcpAgent, Agent: "fixer", Sponsor: "alice@example.com"},
		v1alpha1.Actor{Kind: got.Kind, Principal: got.Principal, Agent: got.Agent, Sponsor: got.Sponsor})

	cs, c = mcpClient(t, opts(), nil, append(mcpFixture(), mcpCompactIndex("other"))...)
	res, body = call(t, cs, "ship", map[string]any{"pipeline": "app", "images": []string{"ghcr.io/acme/app:3.0"}})
	assert.True(t, res.IsError)
	assert.Contains(t, fmt.Sprint(body), "agent fixer may not act")
	assert.Empty(t, mcpNewBundles(t, c), "nothing written")
}

// newBundles are the Bundles of c that the fixture did not create.
func mcpNewBundles(t *testing.T, c sigs_client.Client) []v1alpha1.Bundle {
	t.Helper()
	var list v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &list))
	var out []v1alpha1.Bundle
	for _, b := range list.Items {
		if b.Name != "app-v1" && b.Name != "app-v2" {
			out = append(out, b)
		}
	}
	return out
}

// TestMCP_WritesStampTheActor (#1845, docs/actors.md): a write through
// kardinal mcp is stamped by the CLI's own code with the actor the
// kubeconfig authenticates, here agent fixer for alice, and records what
// the client says about itself as asserted: kardinal mcp and the agent
// that connected, or KARDINAL_ACTOR_CLIENT when it is set.
func TestMCP_WritesStampTheActor(t *testing.T) {
	for _, tc := range []struct {
		name, env, wantClient string
	}{
		{"the client that connected", "", "kardinal-mcp/" + CLIVersion + " (test-agent/1)"},
		{"KARDINAL_ACTOR_CLIENT wins", "ci-runner/7", "ci-runner/7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KARDINAL_ACTOR_CLIENT", tc.env)
			t.Setenv("KARDINAL_ACTOR_MODEL", "claude-opus-5")
			cs, c := mcpClient(t, mcpOptions{allowWrite: true}, nil, append(mcpFixture(), mcpIndex("*"))...)
			res, body := call(t, cs, "ship", map[string]any{"pipeline": "app", "images": []string{"ghcr.io/acme/app:3.0"}})
			require.False(t, res.IsError, "%v", body)
			created := mcpNewBundles(t, c)
			require.Len(t, created, 1)
			b := created[0]
			assert.Equal(t, mcpAgent, b.Annotations[lifecycle.AnnotationCreatedBy])
			assert.Equal(t, &v1alpha1.Actor{Kind: "agent", Principal: mcpAgent, Agent: "fixer", Sponsor: "alice@example.com",
				Asserted: &v1alpha1.AssertedActor{Model: "claude-opus-5", Client: tc.wantClient}}, b.Spec.Actor)
			assert.Equal(t, tc.env, assertedOf().Client, "the label is put back after the call")
		})
	}
}

// TestMCP_AgentOutOfScope: an agent whose AgentIdentity does not cover the
// pipeline is refused before anything is written, with the reason.
func TestMCP_AgentOutOfScope(t *testing.T) {
	t.Parallel()
	cs, c := mcpClient(t, mcpOptions{allowWrite: true}, nil, append(mcpFixture(), mcpIndex("other"))...)
	res, body := call(t, cs, "ship", map[string]any{"pipeline": "app", "images": []string{"ghcr.io/acme/app:3.0"}})
	assert.True(t, res.IsError)
	assert.Contains(t, fmt.Sprint(body), "agent fixer may not act")
	assert.Empty(t, mcpNewBundles(t, c), "nothing written")
}

// TestMCPClientLabel: the client's name and version are what it says, so
// the label keeps printable ASCII only and fits the stamp's 128 bytes.
func TestMCPClientLabel(t *testing.T) {
	t.Parallel()
	session := func(name, version string) *mcp.CallToolRequest {
		st, ct := mcp.NewInMemoryTransports()
		ss, err := mcp.NewServer(&mcp.Implementation{Name: "s", Version: "1"}, nil).Connect(t.Context(), st, nil)
		require.NoError(t, err)
		cs, err := mcp.NewClient(&mcp.Implementation{Name: name, Version: version}, nil).Connect(t.Context(), ct, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = cs.Close(); _ = ss.Wait() })
		return &mcp.CallToolRequest{Session: ss}
	}
	base := "kardinal-mcp/" + CLIVersion
	for _, tc := range []struct {
		name    string
		req     *mcp.CallToolRequest
		want    string
		maxSize int
	}{
		{"no session", nil, base, 0},
		{"a client", session("claude-code", "2.1.0"), base + " (claude-code/2.1.0)", 0},
		{"control characters and non-ASCII are dropped", session("evil\n--context=x\u202e", "1\r\n"), base + " (evil--context=x/1)", 0},
		{"a long name is cut", session(strings.Repeat("a", 500), "1"), "", mcpMaxAsserted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mcpClientLabel(tc.req)
			if tc.want != "" {
				assert.Equal(t, tc.want, got)
			}
			assert.LessOrEqual(t, len(got), mcpMaxAsserted)
			if tc.maxSize > 0 {
				assert.Len(t, got, tc.maxSize)
			}
			for _, r := range got {
				assert.True(t, r >= 0x20 && r <= 0x7e, "%q", got)
			}
		})
	}
}
