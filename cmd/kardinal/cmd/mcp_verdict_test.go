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

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestMCP_VerifyAndVerdicts (#1845, docs/agentic/verify-with-agents.md):
// the verify tool records a verdict on the version that runs in the
// environment, in the identity's name and with its actor stamp, through the
// CLI's own code; it refuses one for another version. The verdicts tool
// lists it, with what the verifier wrote (summary, evidence, finding titles)
// labelled untrusted.
func TestMCP_VerifyAndVerdicts(t *testing.T) {
	t.Parallel()
	cs, c := mcpClient(t, mcpOptions{allowWrite: true}, nil, append(mcpFixture(), mcpIndex("*"))...)
	ctx := context.Background()

	res, body := call(t, cs, "verify", map[string]any{"pipeline": "app", "environment": "prod", "version": "2.0",
		"check": "qa", "result": "pass"})
	assert.True(t, res.IsError, "prod runs 1.0, not 2.0")
	assert.Contains(t, fmt.Sprint(body), "not 2.0: nothing recorded")

	res, body = call(t, cs, "verify", map[string]any{"pipeline": "app", "environment": "prod", "version": "1.0",
		"check": "qa", "result": "fail", "summary": "checkout returns 500. " + injection,
		"evidenceURL": "https://ci.example.com/runs/1",
		"findings":    []map[string]any{{"severity": "high", "title": "checkout 500: " + injection}}})
	require.False(t, res.IsError, "%v", body)
	var list v1alpha1.VerdictList
	require.NoError(t, c.List(ctx, &list))
	require.Len(t, list.Items, 1)
	v := list.Items[0].Spec
	assert.Equal(t, "app-v1", v.Bundle)
	assert.Equal(t, "prod", v.Environment)
	assert.Equal(t, v1alpha1.VerdictFail, v.Result)
	assert.Equal(t, mcpAgent, v.By)
	require.NotNil(t, v.Actor)
	assert.Equal(t, "fixer", v.Actor.Agent, "stamped as the agent")
	require.NotNil(t, v.Actor.Asserted)
	assert.True(t, strings.HasPrefix(v.Actor.Asserted.Client, "kardinal-mcp/"), v.Actor.Asserted.Client)
	assert.Equal(t, []v1alpha1.VerdictFinding{{Severity: "high", Title: "checkout 500: " + injection}}, v.Findings)

	res, body = call(t, cs, "verdicts", map[string]any{"pipeline": "app", "environment": "prod"})
	require.False(t, res.IsError, "%v", body)
	items, _ := body["result"].([]any)
	require.Len(t, items, 1, "%v", body)
	got := items[0].(map[string]any)
	assert.Equal(t, "qa", got["check"])
	assert.Equal(t, v1alpha1.VerdictFail, got["result"])
	assert.Contains(t, got["verifier"], "fixer")
	assertOnlyLabelled(t, "verdicts", body, injection)
	raw, _ := json.Marshal(got)
	assert.Contains(t, string(raw), `"evidenceURL":{"untrusted":"https://ci.example.com/runs/1"}`)

	_, body = call(t, cs, "verdicts", map[string]any{"pipeline": "app", "version": "2.0"})
	assert.Equal(t, []any{}, body["result"], "no verdict on 2.0")
}

// TestMCP_VerifyRefusesBadValues: what the CRD would refuse, and what is
// not a value of its kind, is refused before anything runs.
func TestMCP_VerifyRefusesBadValues(t *testing.T) {
	t.Parallel()
	cs, c := mcpClient(t, mcpOptions{allowWrite: true}, nil, mcpFixture()...)
	base := map[string]any{"pipeline": "app", "environment": "prod", "version": "1.0", "check": "qa", "result": "pass"}
	for name, change := range map[string]map[string]any{
		"a result":                 {"result": "passed --context=x"},
		"an evidence URL scheme":   {"evidenceURL": "javascript:alert(1)"},
		"an evidence URL newline":  {"evidenceURL": "https://ci.example.com/\n--kubeconfig=/tmp/x"},
		"a finding severity":       {"findings": []map[string]any{{"severity": "-o", "title": "t"}}},
		"an empty finding title":   {"findings": []map[string]any{{"severity": "low", "title": " "}}},
		"a long finding title":     {"findings": []map[string]any{{"severity": "low", "title": strings.Repeat("x", 257)}}},
		"a long summary":           {"summary": strings.Repeat("x", 1025)},
		"a check that is a flag":   {"check": "--check=other"},
		"a missing version":        {"version": ""},
		"an environment with ../":  {"environment": "../prod"},
		"a fail without a summary": {"result": "fail"},
	} {
		args := map[string]any{}
		for k, v := range base {
			args[k] = v
		}
		for k, v := range change {
			args[k] = v
		}
		res, body := call(t, cs, "verify", args)
		assert.True(t, res.IsError, "%s: %v", name, body)
	}
	var list v1alpha1.VerdictList
	require.NoError(t, c.List(context.Background(), &list))
	assert.Empty(t, list.Items, "nothing recorded")
}
