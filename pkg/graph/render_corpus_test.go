// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// renderCorpusFile pins, for graph.RenderVersion, the hash of each corpus
// Graph's spec (TestRenderCorpus).
const renderCorpusFile = "testdata/render-corpus.json"

// updateRenderCorpusEnv set to 1 records the corpus hashes, for a new
// graph.RenderVersion or new entries.
const updateRenderCorpusEnv = "KARDINAL_UPDATE_RENDER_CORPUS"

// renderCorpusPin is renderCorpusFile's content.
type renderCorpusPin struct {
	RenderVersion int               `json:"renderVersion"`
	Graphs        map[string]string `json:"graphs"`
}

// renderCorpus are builder inputs as installed Pipelines, Bundles and gates
// have them: every gate collection (plain, skip permission, approval with
// and without a policy, approval readers, verify with and without a policy,
// and the mixed collections of #2271), agents environments, pr-review, the
// compact shape, a fleet and org-namespace gates.
func renderCorpus() map[string]graph.BuildInput {
	gates := func(g ...kardinalv1alpha1.PolicyGate) []kardinalv1alpha1.PolicyGate { return g }
	approvalPolicy := func(g kardinalv1alpha1.PolicyGate, required int) kardinalv1alpha1.PolicyGate {
		g.Spec.Approval = &kardinalv1alpha1.GateApprovalPolicy{Required: required, AllowedGroups: []string{"release-managers"}, ExcludeAuthor: true}
		return g
	}
	plain := makePolicyGate("weekday", "platform-policies", "test", "!schedule.isWeekend")
	approval := approvalPolicy(makePolicyGate("two-approvers", "platform-policies", "prod", "true"), 2)
	readers := makePolicyGate("one-ok", "platform-policies", "test", "approvals.count >= 1")
	verify := verifyGateFor("prod", kardinalv1alpha1.GateVerifyPolicy{Check: "explore"})
	verifyApproval := approvalPolicy(verifyGateFor("prod", kardinalv1alpha1.GateVerifyPolicy{Check: "smoke"}), 1)
	verifyApproval.Name = "smoke-and-approve"

	agents := makeLinearPipeline("app", "test", "uat", "prod")
	agents.Spec.Environments[1].Agents = kardinalv1alpha1.AutonomyApprove
	agents.Spec.Environments[2].Agents = kardinalv1alpha1.AutonomyDeny
	prReview := makeLinearPipeline("app", "test", "prod")
	prReview.Spec.Environments[1].Approval = "pr-review"

	linear := func(envs ...string) *kardinalv1alpha1.Pipeline { return makeLinearPipeline("app", envs...) }
	bundle := makeBundle("app-v1", "app")
	return map[string]graph.BuildInput{
		"linear, no gates":   {Pipeline: linear("test", "uat", "prod"), Bundle: bundle},
		"plain gate":         {Pipeline: linear("test", "prod"), Bundle: bundle, PolicyGates: gates(plain)},
		"approval gates":     {Pipeline: linear("test", "prod"), Bundle: bundle, PolicyGates: gates(approval, readers, plain)},
		"approval readers":   {Pipeline: linear("test", "prod"), Bundle: bundle, PolicyGates: gates(readers, plain)},
		"verify gate":        {Pipeline: linear("uat", "prod"), Bundle: bundle, PolicyGates: gates(verify)},
		"verify with policy": {Pipeline: linear("uat", "prod"), Bundle: bundle, PolicyGates: gates(verify, verifyApproval)},
		// #2271: a verify gate without a policy next to an approval gate with
		// one.
		"verify beside an approval policy": {Pipeline: linear("uat", "prod"), Bundle: bundle, PolicyGates: gates(verify, approval)},
		"skip permission": {Pipeline: linear("test", "staging", "prod"), Bundle: skipBundle("staging"),
			PolicyGates: gates(orgGate("staging-soak", "staging"),
				skipPermission("allow-staging-skip-for-hotfix", "platform-policies", "staging", `bundle.version.startsWith("hotfix-")`, true))},
		"org-namespace gate": {Pipeline: linear("test", "prod"), Bundle: bundle, PolicyGates: gates(orgGate("prod-freeze", "prod")),
			PolicyNamespaces: []string{"platform-policies"}},
		"agents": {Pipeline: agents, Bundle: agentBundle()},
		// A gated pr-review environment mirrors its gates onto the PR.
		"pr-review": {Pipeline: prReview, Bundle: bundle, PolicyGates: gates(makePolicyGate("prod-weekday", "platform-policies", "prod", "!schedule.isWeekend"))},
		"compact": {Pipeline: compactPipeline(kardinalv1alpha1.EnvironmentSpec{Name: "test"}, kardinalv1alpha1.EnvironmentSpec{Name: "uat"},
			kardinalv1alpha1.EnvironmentSpec{Name: "prod"}), Bundle: bundle, PolicyGates: gates(approval, verify, plain)},
		"fleet": {Pipeline: fleetPipeline(2), Bundle: bundle, PolicyGates: gates(makePolicyGate("change-freeze", "platform-policies", "prod", "true"))},
	}
}

// renderHash is the hash of the canonical JSON of g's spec: its nodes, their
// templates, includeWhen and readyWhen, and its ServiceAccount. Metadata is
// left out: the render-version label changes with every bump, and the
// Graph's name and owner are the Bundle's.
func renderHash(t *testing.T, g *graph.Graph) string {
	t.Helper()
	b, err := json.Marshal(g.Spec)
	require.NoError(t, err)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestRenderCorpus (#2296) pins how the builder renders the corpus, per
// graph.RenderVersion. The Bundle reconciler re-renders a Promoting Bundle's
// Graph whose render version differs (#2272): that is how a version stuck
// under a rendering bug recovers on upgrade, and it only happens if every
// change that renders an existing Graph differently bumps RenderVersion. A
// corpus Graph that renders differently at the same RenderVersion fails
// here. KARDINAL_UPDATE_RENDER_CORPUS=1 records the hashes for a new
// RenderVersion or for added entries, and refuses to change an existing
// entry's hash without a bump.
func TestRenderCorpus(t *testing.T) {
	got := map[string]string{}
	for name, in := range renderCorpus() {
		res, err := graph.NewBuilder().Build(in)
		require.NoError(t, err, name)
		assertKroValid(t, res.Graph)
		h := renderHash(t, res.Graph)
		again, err := graph.NewBuilder().Build(in)
		require.NoError(t, err, name)
		require.Equal(t, h, renderHash(t, again.Graph), "%s: the builder renders the same input the same way", name)
		got[name] = h
	}

	var pin renderCorpusPin
	if b, err := os.ReadFile(renderCorpusFile); err == nil {
		require.NoError(t, json.Unmarshal(b, &pin), renderCorpusFile)
	} else if !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	var changed, added, removed []string
	for name, h := range got {
		switch old, ok := pin.Graphs[name]; {
		case !ok:
			added = append(added, name)
		case old != h:
			changed = append(changed, name)
		}
	}
	for name := range pin.Graphs {
		if _, ok := got[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(changed)
	sort.Strings(added)
	sort.Strings(removed)
	sameVersion := pin.RenderVersion == graph.RenderVersion
	update := "run " + updateRenderCorpusEnv + "=1 go test ./pkg/graph -run '^TestRenderCorpus$'"
	bump := fmt.Sprintf("Do these differences reach Graphs that exist before this change, the Graphs of Bundles "+
		"already promoting? If so, bump graph.RenderVersion (pkg/graph/gates.go, now %d) and say so in the changelog "+
		"fragment, so those Bundles re-render once on upgrade (#2272), then %s. If no existing Graph renders "+
		"differently (only an input that could not exist before), change these entries in %s by hand and say why in the PR",
		graph.RenderVersion, update, renderCorpusFile)

	if os.Getenv(updateRenderCorpusEnv) == "1" {
		require.False(t, sameVersion && len(changed) > 0,
			"refusing to record new hashes for %s at the same RenderVersion %d. %s", strings.Join(changed, ", "), graph.RenderVersion, bump)
		b, err := json.MarshalIndent(renderCorpusPin{RenderVersion: graph.RenderVersion, Graphs: got}, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(renderCorpusFile), 0o755))
		require.NoError(t, os.WriteFile(renderCorpusFile, append(b, '\n'), 0o644))
		return
	}
	require.True(t, sameVersion, "graph.RenderVersion is %d, and %s records the corpus at %d: %s",
		graph.RenderVersion, renderCorpusFile, pin.RenderVersion, update)
	require.Empty(t, changed, "the builder renders these corpus Graphs differently at RenderVersion %d: %s. %s",
		graph.RenderVersion, strings.Join(changed, ", "), bump)
	require.Empty(t, added, "corpus entries with no recorded hash: %s; %s", strings.Join(added, ", "), update)
	require.Empty(t, removed, "recorded entries no longer in the corpus: %s; %s", strings.Join(removed, ", "), update)
}
