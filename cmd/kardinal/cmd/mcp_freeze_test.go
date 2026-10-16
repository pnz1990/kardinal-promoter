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
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestMCP_Freeze (#1845, #1843): the freeze tool makes the agent freeze or
// the stop-the-line, in every namespace or one, through the CLI's own code;
// the freezes tool lists it with its reason labelled untrusted; there is no
// tool that lifts a freeze or exempts a pipeline from it.
func TestMCP_Freeze(t *testing.T) {
	cs, c := mcpClient(t, mcpOptions{allowWrite: true}, nil, mcpFixture()...)
	res, body := call(t, cs, "freeze", map[string]any{"holds": "agents", "reason": "INC-1: " + injection, "for": "2h"})
	require.False(t, res.IsError, "%v", body)
	res, body = call(t, cs, "freeze", map[string]any{"holds": "everyone", "namespace": "default", "reason": "payments incident"})
	require.False(t, res.IsError, "%v", body)

	var windows v1alpha1.ChangeWindowList
	require.NoError(t, c.List(context.Background(), &windows))
	require.Len(t, windows.Items, 2)
	holds := map[string]string{}
	for _, w := range windows.Items {
		require.NotNil(t, w.Spec.Freeze, w.Name)
		holds[w.Spec.Freeze.Actors] = strings.Join(w.Spec.Freeze.Namespaces, ",")
		assert.Equal(t, mcpAgent, w.Spec.Freeze.CreatedBy, "the freeze names who made it")
		if w.Spec.Freeze.Actors == v1alpha1.FreezeAgents {
			assert.False(t, w.Spec.End.IsZero(), "for: 2h ends it")
		}
	}
	assert.Equal(t, map[string]string{v1alpha1.FreezeAgents: "", v1alpha1.FreezeEveryone: "default"}, holds)

	// A freeze being lifted (kardinal unfreeze marked it, #2034) is not listed.
	lifting := &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-freeze-lifting"},
		Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.Now(),
			Freeze: &v1alpha1.ChangeWindowFreeze{Actors: v1alpha1.FreezeEveryone, CreatedBy: "carol",
				Lifted: &v1alpha1.FreezeLift{By: "carol"}}}}
	require.NoError(t, c.Create(context.Background(), lifting))

	res, body = call(t, cs, "freezes", map[string]any{})
	require.False(t, res.IsError, "%v", body)
	items, _ := body["result"].([]any)
	require.Len(t, items, 2, "%v", body)
	assert.NotContains(t, fmt.Sprint(items), "kardinal-freeze-lifting")
	assertOnlyLabelled(t, "freezes", body, injection)

	tools, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		assert.NotContains(t, []string{"unfreeze", "override"}, tool.Name, "a person lifts a freeze or exempts a pipeline")
	}
}

// TestMCP_FreezeRefusesBadValues: what is not a freeze is refused before
// anything is written.
func TestMCP_FreezeRefusesBadValues(t *testing.T) {
	cs, c := mcpClient(t, mcpOptions{allowWrite: true}, nil, mcpFixture()...)
	for name, args := range map[string]map[string]any{
		"holds that is a flag":       {"holds": "--everyone", "reason": "r"},
		"holds of another kind":      {"holds": "humans", "reason": "r"},
		"no reason":                  {"holds": "agents", "reason": "  "},
		"a long reason":              {"holds": "agents", "reason": strings.Repeat("x", 1025)},
		"a bad duration":             {"holds": "agents", "reason": "r", "for": "--for=1h"},
		"a negative duration":        {"holds": "agents", "reason": "r", "for": "-1h"},
		"a namespace with ../":       {"holds": "agents", "reason": "r", "namespace": "../kube-system"},
		"a namespace that is a flag": {"holds": "agents", "reason": "r", "namespace": "--cluster"},
	} {
		res, body := call(t, cs, "freeze", args)
		assert.True(t, res.IsError, "%s: %v", name, body)
	}
	var windows v1alpha1.ChangeWindowList
	require.NoError(t, c.List(context.Background(), &windows))
	assert.Empty(t, windows.Items)
}

// TestMCP_SimulateAsAgent (#1843): simulate with asAgent evaluates the gates
// for a version that agent created, its registry entry read for agent.*:
// prod denies agents, test lets this one in; an unknown agent is an error.
func TestMCP_SimulateAsAgent(t *testing.T) {
	objs := mcpFixture()
	for _, o := range objs {
		if p, ok := o.(*v1alpha1.Pipeline); ok {
			p.Spec.Environments[1].Agents = v1alpha1.AutonomyDeny
		}
	}
	id := &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: "fixer", Generation: 1},
		Spec: v1alpha1.AgentIdentitySpec{Subjects: []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: "agents", Name: "fixer"}},
			Sponsor: v1alpha1.AgentSponsor{User: "alice@example.com"}, Autonomy: v1alpha1.AutonomyAuto},
		Status: v1alpha1.AgentIdentityStatus{Ready: true, ObservedGeneration: 1}}
	idx := mcpIndex("*")
	entry := idx.Status.Users[mcpAgent]
	entry.Autonomy = v1alpha1.AutonomyAuto
	idx.Status.Users[mcpAgent] = entry
	cs, _ := mcpClient(t, mcpOptions{}, nil, append(objs, id, idx)...)

	res, body := call(t, cs, "simulate", map[string]any{"pipeline": "app", "environment": "prod", "time": "Tuesday 10am", "asAgent": "fixer"})
	require.False(t, res.IsError, "%v", body)
	r := body["result"].(map[string]any)
	assert.Equal(t, false, r["allowed"])
	raw, _ := json.Marshal(r["gates"])
	assert.Contains(t, string(raw), "kardinal-agents-deny")

	res, body = call(t, cs, "simulate", map[string]any{"pipeline": "app", "environment": "test", "time": "Tuesday 10am", "asAgent": "fixer"})
	require.False(t, res.IsError, "%v", body)
	assert.Equal(t, true, body["result"].(map[string]any)["allowed"], "%v", body)

	res, body = call(t, cs, "simulate", map[string]any{"pipeline": "app", "environment": "prod", "time": "Tuesday 10am"})
	require.False(t, res.IsError, "%v", body)
	assert.Equal(t, true, body["result"].(map[string]any)["allowed"], "without asAgent: no agent gate; %v", body)

	res, body = call(t, cs, "simulate", map[string]any{"pipeline": "app", "environment": "prod", "asAgent": "nobody"})
	assert.True(t, res.IsError)
	assert.Contains(t, fmt.Sprint(body), "nobody")
}
