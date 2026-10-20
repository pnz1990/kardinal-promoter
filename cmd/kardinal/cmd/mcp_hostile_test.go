// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apiserver/pkg/authentication/user"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

// mcpWorld is a scenario World (every kardinal reconciler, the chart's
// admission policies) with a Pipeline app in team-a that ran version 1.1.0
// to prod, a Pipeline app in team-b, and kardinal mcp --allow-write
// connected as agent fixer (scoped to team-a) through a kubeconfig of its
// own (World.ClientAs). Without admission, the World stores every write
// as the CLI made it (scenario.Options.NoAdmission).
func mcpWorld(t *testing.T, admission bool) (*scenario.World, *mcp.ClientSession, scenario.Identity) {
	t.Helper()
	c := scenario.Combo{Update: "kustomize", Health: "deployment", Gate: "cel", Topology: "linear", Delivery: "direct",
		SCM: scenario.SCMGitHub, Outcome: "success", Actor: "human"}
	w := scenario.NewWorld(t, scenario.Options{SCM: c.SCM, PolicyNamespaces: []string{scenario.PolicyNS}, Actors: true,
		NoAdmission: !admission})
	p := scenario.BuildPlan(c, "team-a", w.Now(), false)
	require.NoError(t, p.Setup(w))
	w.MustApply(scenario.AgentFixture("team-a")...)
	w.MustApply(p.Pipeline)
	other := p.Pipeline.DeepCopy()
	other.Namespace, other.ResourceVersion, other.UID = "team-b", "", ""
	w.MustApply(other)
	w.Round()
	b := scenario.ImageBundle("team-a", scenario.PipelineName, "1.1.0")
	scenario.ShipAs(t, w, "human", b)
	require.True(t, w.RunUntil(scenario.MaxRounds, func() bool { return w.BundlePhase("team-a")[b.Name] == "Verified" }),
		"%s", w.Describe("team-a"))
	agent := scenario.ActorIdentities["agent"]
	// The agent's RBAC: its own namespace, and reading the org's gates.
	kube := w.ClientAsRBAC(&user.DefaultInfo{Name: agent.Username, Groups: agent.Groups}, []string{"team-a"}, []string{scenario.PolicyNS})
	// As clusterClient: --namespace (the tools' namespace argument) wins
	// over the kubeconfig's namespace. The client and the caller come with
	// the server's options: the test sets no package variable (#2230).
	client := func() (sigs_client.Client, string, error) {
		if globalNamespace != "" {
			return kube, globalNamespace, nil
		}
		return kube, "team-a", nil
	}
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := newMCPServer(mcpOptions{allowWrite: true, maxBytes: defaultMCPMaxBytes, client: client,
		identity: fixedIdentity(Identity{Username: agent.Username, Groups: agent.Groups})}).Connect(ctx, st, nil)
	require.NoError(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "hostile-agent", Version: "1"}, nil).Connect(ctx, ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Wait() })
	return w, cs, agent
}

// mcpValid is a valid value of each tool argument, by name.
var mcpValid = map[string]any{
	"pipeline": "app", "environment": "prod", "version": "1.1.0", "namespace": "team-a", "a": "1.1.0", "b": "1.1.0",
	"to": "1.1.0", "images": []any{"ghcr.io/acme/app:9.9.9"}, "check": scenario.VerifyCheck, "result": "pass",
	"reason": "scenario", "summary": "scenario", "comment": "scenario", "decision": "approve",
	"evidenceURL": "https://ci.example.com/runs/1", "author": "ci", "commit": "abcdef1", "ciRunURL": "https://ci.example.com/runs/1",
	"type": "image", "time": "Tuesday 10am", "bundle": "app-1-1-0",
}

// mcpNaming are the arguments that name an object or a place; the others
// are free text.
var mcpNaming = []string{"pipeline", "environment", "version", "namespace", "a", "b", "to", "images", "check", "bundle", "decision", "result", "type"}

// toolArgs returns each tool's string and string-list arguments, and the
// required ones, from the server's own input schemas.
func toolArgs(t *testing.T, cs *mcp.ClientSession) map[string]struct {
	args     map[string]string // name -> "string" or "array"
	required []string
} {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	out := map[string]struct {
		args     map[string]string
		required []string
	}{}
	for _, tool := range res.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)
		var s struct {
			Properties map[string]struct {
				Type  any `json:"type"`
				Items *struct {
					Type any `json:"type"`
				} `json:"items"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		require.NoError(t, json.Unmarshal(raw, &s), tool.Name)
		e := out[tool.Name]
		e.args, e.required = map[string]string{}, s.Required
		for name, p := range s.Properties {
			switch {
			case fmt.Sprint(p.Type) == "string":
				e.args[name] = "string"
			case fmt.Sprint(p.Type) == "array" && p.Items != nil && fmt.Sprint(p.Items.Type) == "string":
				e.args[name] = "array"
			}
		}
		out[tool.Name] = e
	}
	return out
}

// TestMCP_HostileInputs: every tool, every string argument, every value of
// scenario.HostileInputs (flags, traversal, shell, newlines, look-alike and
// control characters, smuggled fields and selectors, templates, a prompt
// injection, 64 KiB and 1 MiB values, empty), against the scenario World
// as an agent scoped to team-a. A tool never fails the transport; a hostile
// value in an argument that names something is refused, writing nothing;
// whatever any call writes is in team-a and is stamped with the agent;
// nothing cluster-scoped changes; and what the agent reads back carries the
// injection only labelled untrusted. Covers MCP-HOSTILE-01.
//
// The chart's admission policies refuse a write whose stamp is not its
// requester's, so there a wrong stamp is never stored to be checked. The
// write tools run again in a World without admission, where every stamp
// stored is the CLI's own: a version and a Verdict created and a version
// rejected are each checked there at least once. (Stored text is bounded
// by the policies, so that is checked with admission only.)
func TestMCP_HostileInputs(t *testing.T) {
	t.Parallel()
	for _, admission := range []bool{true, false} {
		name := "admission"
		if !admission {
			name = "no admission"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			hostileInputs(t, admission)
		})
	}
}

// hostileInputs is TestMCP_HostileInputs in a World with or without the
// chart's admission policies; without, only the write tools run.
func hostileInputs(t *testing.T, admission bool) {
	w, cs, agent := mcpWorld(t, admission)
	tools := toolArgs(t, cs)
	require.Len(t, tools, len(mcpReadTools)+len(mcpWriteTools))
	baseArgs := func(name string) map[string]any {
		args := map[string]any{}
		for _, r := range tools[name].required {
			args[r] = mcpValid[r]
		}
		for _, n := range []string{"pipeline", "environment", "namespace"} {
			if _, ok := tools[name].args[n]; ok {
				args[n] = mcpValid[n]
			}
		}
		return args
	}
	// The valid calls work, so a refusal below is the hostile value's.
	for _, name := range mcpReadTools {
		res, body := call(t, cs, name, baseArgs(name))
		assert.False(t, res.IsError, "%s with valid arguments: %v", name, body)
	}
	calls, refused, fresh, wrote := 0, 0, 0, 0
	stamped := map[string]int{}
	snap := w.Snapshot()
	accepted := map[string]int{}
	hostile := scenario.HostileInputs()
	for _, name := range slices.Sorted(maps.Keys(tools)) {
		if !admission && !slices.Contains(mcpWriteTools, name) {
			continue
		}
		spec := tools[name]
		for argN, arg := range slices.Sorted(maps.Keys(spec.args)) {
			for _, h := range hostile {
				if !hostileCalled(h, argN) {
					continue
				}
				args := baseArgs(name)
				switch name {
				case "ship":
					args["images"] = mcpValid["images"]
				case "reject":
					// A version of its own to reject, so each call is the first.
					fresh++
					b := scenario.ImageBundle("team-a", scenario.PipelineName, fmt.Sprintf("0.0.%d", fresh))
					w.MustApply(b)
					snap = w.Snapshot()
					args["version"] = b.Spec.Images[0].Tag
				}
				var v any = h.Value
				if spec.args[arg] == "array" {
					v = []any{h.Value}
				}
				args[arg] = v
				before, writes := snap, w.Writes()
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
				cancel()
				calls++
				what := fmt.Sprintf("%s %s=%s(%.40q)", name, arg, h.Kind, h.Value)
				require.NoError(t, err, "%s: the transport failed", what)
				require.NotEmpty(t, res.Content, what)
				var changed []string
				if w.Writes() != writes {
					snap = w.Snapshot()
					changed = scenario.Changed(before, snap)
				}
				if res.IsError {
					refused++
				} else {
					accepted[name+" "+arg+" "+h.Kind]++
				}
				// A place the agent may not write is refused only as a
				// namespace; as another name it is just a name.
				if slices.Contains(mcpNaming, arg) && h.Kind != "empty" && (h.Kind != "scope" || arg == "namespace") {
					assert.True(t, res.IsError, "%s: a hostile name is refused", what)
					assert.Empty(t, changed, "%s: a refused call writes nothing", what)
				}
				for _, k := range changed {
					parts := strings.SplitN(k, "/", 3)
					assert.Equal(t, "team-a", parts[1], "%s wrote %s: outside the agent's namespace", what, k)
				}
				if len(changed) > 0 {
					wrote++
				}
				for k, n := range checkStamped(t, w, what, before, changed, agent.Username) {
					stamped[k] += n
				}
				if admission {
					// The chart's policies bound stored text; the CLI does not.
					assert.Empty(t, w.LongStrings(changed, 4096), "%s: stored text past 4 KiB", what)
				}
			}
		}
	}
	t.Logf("%d calls, %d refused, %d wrote, stamps checked %v; accepted: %v", calls, refused, wrote, stamped, accepted)
	assert.NotZero(t, wrote, "no call wrote, so no stamp was checked")
	if !admission {
		// No version waits at an approval gate here, so approve writes no
		// Approval in either World.
		for _, k := range []string{"Bundle", "Bundle rejected", "Verdict"} {
			assert.NotZero(t, stamped[k], "no %s stamp checked without admission", k)
		}
	}

	// What the agent reads back: an injection it (or anyone) wrote is
	// labelled untrusted wherever it appears.
	for _, name := range mcpReadTools {
		_, body := call(t, cs, name, baseArgs(name))
		assertOnlyLabelled(t, name, body, scenario.HostileInjection)
	}
}

// hostileCalled reports whether a tool's argument number argN (in name
// order) is called with h. Under -race an oversized value goes to each
// tool's first argument only: decoding it is the MCP SDK's slowest path,
// about 170ms a call there, and it was this package's slowest test (#2194).
// Without -race (go test -count=3 in CI, and locally) every argument gets
// every value.
func hostileCalled(h scenario.Hostile, argN int) bool {
	return !raceEnabled || h.Kind != "oversized" || argN == 0
}

// checkStamped checks that every Bundle, Approval and Verdict changed
// created names agent as its actor's principal (and a Bundle as its
// creator), and that a Bundle it rejected names agent as who rejected it.
// It returns how many of each it checked ("Bundle rejected" for a
// rejection).
func checkStamped(t *testing.T, w *scenario.World, what string, before map[string]string, changed []string, agent string) map[string]int {
	t.Helper()
	checked := map[string]int{}
	for _, k := range changed {
		_, existed := before[k]
		parts := strings.SplitN(k, "/", 3)
		key := sigs_client.ObjectKey{Namespace: parts[1], Name: parts[2]}
		var a *v1alpha1.Actor
		switch parts[0] {
		case "Bundle":
			var b v1alpha1.Bundle
			if w.Store().Get(context.Background(), key, &b) != nil {
				continue
			}
			if existed {
				if b.Spec.Rejected != nil {
					checked["Bundle rejected"]++
					assert.Equal(t, agent, b.Spec.Rejected.By, "%s: %s rejected in another's name", what, k)
				}
				continue
			}
			if b.Spec.Actor != nil && b.Spec.Actor.Kind == v1alpha1.ActorController {
				continue // the controller's own version
			}
			assert.Equal(t, agent, b.Annotations[lifecycle.AnnotationCreatedBy], "%s: %s created in another's name", what, k)
			a = b.Spec.Actor
		case "Approval":
			var o v1alpha1.Approval
			if w.Store().Get(context.Background(), key, &o) != nil {
				continue
			}
			a = o.Spec.Actor
		case "Verdict":
			var o v1alpha1.Verdict
			if w.Store().Get(context.Background(), key, &o) != nil {
				continue
			}
			a = o.Spec.Actor
		default:
			continue
		}
		checked[parts[0]]++
		if assert.NotNil(t, a, "%s: %s has no actor stamp", what, k) {
			assert.Equal(t, agent, a.Principal, "%s: %s names another actor", what, k)
		}
	}
	return checked
}
