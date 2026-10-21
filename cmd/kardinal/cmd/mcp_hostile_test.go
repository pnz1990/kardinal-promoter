// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"runtime/pprof"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/user"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

// mcpWorld is a scenario World (every kardinal reconciler, the chart's
// admission policies) with a Pipeline app in team-a that ran version 1.1.0
// to prod and whose version mcpWaiting waits at prod's approval gate, a
// Pipeline app in team-b, and kardinal mcp --allow-write
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
	// A version that waits at prod for an approval the agent's does not
	// give (anyone may approve, an agent counts only with allowAgents), so
	// approve records an Approval, the gate records it as not counted, and
	// the version still waits for the next call (#2277). The gate lists no
	// allowedUsers: the Graph copies only allowed approvers' Approvals into
	// it, so an outside one would never be recorded at all
	// (TestGate_ApprovalQuorum, GATE-APPROVAL-02, covers that refusal).
	w.MustApply(&v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: p.PolicyNS, Name: "approval-gate",
		Labels: map[string]string{"kardinal.io/scope": "team", "kardinal.io/applies-to": p.GateEnv}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "true", Message: "prod waits for a person's approval",
			Approval: &v1alpha1.GateApprovalPolicy{Required: 1}}})
	waiting := scenario.ImageBundle("team-a", scenario.PipelineName, mcpWaiting)
	scenario.ShipAs(t, w, "human", waiting)
	require.True(t, w.RunUntil(scenario.MaxRounds, func() bool {
		var pl v1alpha1.Pipeline
		if w.Store().Get(context.Background(), sigs_client.ObjectKey{Namespace: "team-a", Name: scenario.PipelineName}, &pl) != nil {
			return false
		}
		for _, e := range pl.Status.Environments {
			if e.Name == p.GateEnv {
				return e.IncomingBundle == waiting.Name
			}
		}
		return false
	}), "%s", w.Describe("team-a"))
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

// mcpWaiting is the version that waits at prod's approval gate in mcpWorld.
const mcpWaiting = "1.2.0"

// incomingAt is the Bundle that waits for env of ns's Pipeline app, or "".
func incomingAt(t *testing.T, w *scenario.World, ns, env string) string {
	t.Helper()
	var pl v1alpha1.Pipeline
	require.NoError(t, w.Store().Get(context.Background(), sigs_client.ObjectKey{Namespace: ns, Name: scenario.PipelineName}, &pl))
	for _, e := range pl.Status.Environments {
		if e.Name == env {
			return e.IncomingBundle
		}
	}
	return ""
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

// hostileCallDeadline is when TestMCP_HostileInputs gives up on one call. A
// call takes under a second, even under -race on two loaded CPUs (#2455: at
// most 0.86s of 1,035 calls), so the bound only names a call that hangs
// before go test's -timeout ends the binary without saying which: the test
// binary's deadline less hostileReportMargin (the time to log the goroutines
// and fail), never less than hostileMinWait from now, and hostileNoDeadline
// from now without -timeout. It is not a fixed 20s: in #2445's docs-tests job
// the whole runner stalled for about 20s (every package still running took
// 19.5s to 22.9s longer than in its rerun), and the call in flight failed
// for having waited (#2455).
func hostileCallDeadline(now, deadline time.Time, ok bool) time.Time {
	if !ok {
		return now.Add(hostileNoDeadline)
	}
	if d := deadline.Add(-hostileReportMargin); d.After(now.Add(hostileMinWait)) {
		return d
	}
	return now.Add(hostileMinWait)
}

const (
	hostileReportMargin = 10 * time.Second
	hostileMinWait      = 20 * time.Second
	hostileNoDeadline   = 5 * time.Minute
)

// goroutineDump is every goroutine's stack, identical stacks once each
// (pprof's debug=1), for a call that hung: where it is stuck.
func goroutineDump() string {
	var b strings.Builder
	if err := pprof.Lookup("goroutine").WriteTo(&b, 1); err != nil {
		return "(no goroutine dump: " + err.Error() + ")"
	}
	return b.String()
}

// TestHostileCallDeadline (#2455): a hostile call waits until shortly before
// the test binary's deadline, never less than hostileMinWait, and a bounded
// time without one; the dump of a hung call shows the goroutines' frames.
func TestHostileCallDeadline(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 6, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name     string
		deadline time.Time
		ok       bool
		want     time.Time
	}{
		{"no -timeout", time.Time{}, false, now.Add(5 * time.Minute)},
		{"the binary's deadline far off", now.Add(100 * time.Second), true, now.Add(90 * time.Second)},
		{"just past the minimum", now.Add(31 * time.Second), true, now.Add(21 * time.Second)},
		{"near the deadline: the minimum", now.Add(25 * time.Second), true, now.Add(20 * time.Second)},
		{"past the deadline: the minimum", now.Add(-time.Second), true, now.Add(20 * time.Second)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, hostileCallDeadline(now, tt.deadline, tt.ok))
		})
	}
	dump := goroutineDump()
	assert.True(t, strings.HasPrefix(dump, "goroutine profile: total "), "pprof's debug=1 goroutine profile: %.80s", dump)
	assert.Contains(t, dump, "TestHostileCallDeadline", "the dump holds the frames of the goroutines it was taken from")
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
// stored is the CLI's own: a version, an Approval and a Verdict created and
// a version rejected are each checked there at least once. (Stored text is bounded
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
	// The agent's approval is recorded as the agent's, and its gate (which
	// counts an agent only with allowAgents) records it as not counted: the
	// version still waits, so every approve below meets the same waiting
	// version (no ship has run yet, and nothing reconciles during the calls).
	res, body := call(t, cs, "approve", map[string]any{"pipeline": "app", "environment": "prod", "version": mcpWaiting,
		"namespace": "team-a"})
	require.False(t, res.IsError, "approve: %v", body)
	waiting := incomingAt(t, w, "team-a", "prod")
	var gate *v1alpha1.PolicyGate
	require.True(t, w.RunUntil(scenario.MaxRounds, func() bool {
		gate = nil
		var gates v1alpha1.PolicyGateList
		if w.Store().List(context.Background(), &gates, sigs_client.InNamespace("team-a"),
			sigs_client.MatchingLabels{"kardinal.io/bundle": waiting, "kardinal.io/environment": "prod"}) != nil {
			return false
		}
		for i := range gates.Items {
			if g := &gates.Items[i]; g.Spec.Approval != nil && len(g.Status.Approvals) > 0 {
				gate = g
			}
		}
		return gate != nil
	}), "%s", w.Describe("team-a"))
	require.Len(t, gate.Status.Approvals, 1, "the gate's approvals")
	got := gate.Status.Approvals[0]
	assert.Equal(t, agent.Username, got.User)
	assert.False(t, got.Counted, "the agent's approval counts for a gate that counts an agent only with allowAgents (%s)", got.Reason)
	assert.NotEmpty(t, got.Reason, "why it does not count")
	assert.False(t, gate.Status.Ready, "the gate opened: %s", gate.Status.Reason)
	assert.Equal(t, waiting, incomingAt(t, w, "team-a", "prod"), "the version still waits at prod")
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
				case "approve":
					args["version"] = mcpWaiting
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
				deadline, ok := t.Deadline()
				ctx, cancel := context.WithDeadline(context.Background(), hostileCallDeadline(time.Now(), deadline, ok))
				res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
				hung := ctx.Err() != nil
				cancel()
				calls++
				what := fmt.Sprintf("%s %s=%s(%.40q)", name, arg, h.Kind, h.Value)
				if err != nil && hung {
					t.Logf("%s did not answer by its deadline; every goroutine:\n%s", what, goroutineDump())
				}
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
		for _, k := range []string{"Bundle", "Bundle rejected", "Approval", "Verdict"} {
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
// created names agent as its actor's principal (a Bundle also as its
// creator, an Approval as its user and a Verdict as who gave it), and that
// a Bundle it rejected names agent as who rejected it.
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
			// spec.user is whom a gate's approval policy matches.
			assert.Equal(t, agent, o.Spec.User, "%s: %s approves in another's name", what, k)
			a = o.Spec.Actor
		case "Verdict":
			var o v1alpha1.Verdict
			if w.Store().Get(context.Background(), key, &o) != nil {
				continue
			}
			assert.Equal(t, agent, o.Spec.By, "%s: %s gives another's verdict", what, k)
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
