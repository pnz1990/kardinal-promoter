// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	builtinsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// TestForeignPushAfterCrash (#2095): the controller dies right after it
// pushed kardinal's PR branch, before it recorded the push; someone else
// then pushes a commit onto the branch. The restarted controller re-runs
// the step, and git-push pushes nothing over that commit: it stays the
// branch's head, and the step records it (status.outputs.prForeignHead)
// and says so.
//
// Covers SCEN-FOREIGN-PUSH-01.
func TestForeignPushAfterCrash(t *testing.T) {
	c := Combo{Update: "kustomize", Health: "deployment", Gate: "cel", Topology: "linear", Delivery: "pr-review",
		SCM: SCMGitHub, Outcome: "success", Actor: "human"}
	const ns = "team-a"
	w := NewWorld(t, Options{SCM: c.SCM, PolicyNamespaces: []string{PolicyNS}, Actors: true})
	p := BuildPlan(c, ns, w.Now(), false)
	require.NoError(t, p.Setup(w))
	w.MustApply(AgentFixture(ns)...)
	reasons, err := w.Apply(p.Pipeline)
	require.NoError(t, err)
	require.Empty(t, reasons)
	for range 2 {
		w.Round()
	}

	w.CrashWhen(func(e string) bool { return strings.HasPrefix(e, "git push "+builtinsteps.PRBranchPrefix) })
	v1 := ImageBundle(ns, PipelineName, "1.1.0")
	p.Ship(t, w, v1)
	require.True(t, w.RunUntil(MaxRounds, func() bool { return len(w.Crashes()) == 1 }), "the controller dies after the push")
	t.Logf("crash: %q", w.Crashes()[0])
	pushed := builtinsteps.PRBranch(ns, v1.Name, p.Envs[0])

	// Someone else pushes onto kardinal's branch while it is down.
	require.NoError(t, w.Git.Commit(p.Pipeline.Spec.Git.URL, pushed, "hotfix by hand", map[string]string{"HOTFIX": "x\n"}, nil))
	foreign, err := w.Git.Head(p.Pipeline.Spec.Git.URL, pushed)
	require.NoError(t, err)

	env := p.Envs[0]
	require.True(t, w.RunUntil(MaxRounds, func() bool {
		s, ok := w.Steps(ns, v1.Name)[env]
		return ok && s.Status.Outputs[parentsteps.OutputPRForeignHead] != ""
	}), "the step reports the foreign commit\n%s", w.Describe(ns))
	s := w.Steps(ns, v1.Name)[env]
	assert.Equal(t, foreign, s.Status.Outputs[parentsteps.OutputPRForeignHead])
	assert.Contains(t, s.Status.Message, "kardinal does not push over it")
	head, err := w.Git.Head(p.Pipeline.Spec.Git.URL, pushed)
	require.NoError(t, err)
	assert.Equal(t, foreign, head, "the foreign commit is still the branch's head")
}

// TestCrashAfterPushRecovers (#2095, coordinator's case): the controller
// dies right after it pushed kardinal's PR branch, before the status write
// that follows the push. git-push recorded the commit (pushedSHAs) in a
// status write of its own before the push (#2065), so the restarted step
// finds the branch's head among its pushes, pushes again with a lease on
// it, and opens its PR: nothing is refused, and the version is Verified.
//
// Covers SCEN-FOREIGN-PUSH-02.
func TestCrashAfterPushRecovers(t *testing.T) {
	c := Combo{Update: "kustomize", Health: "deployment", Gate: "cel", Topology: "linear", Delivery: "pr-review",
		SCM: SCMGitHub, Outcome: "success", Actor: "human"}
	const ns = "team-a"
	w := NewWorld(t, Options{SCM: c.SCM, PolicyNamespaces: []string{PolicyNS}, Actors: true})
	p := BuildPlan(c, ns, w.Now(), false)
	require.NoError(t, p.Setup(w))
	w.MustApply(AgentFixture(ns)...)
	reasons, err := w.Apply(p.Pipeline)
	require.NoError(t, err)
	require.Empty(t, reasons)
	for range 2 {
		w.Round()
	}
	w.Actors = append(w.Actors, Reviewer)

	w.CrashWhen(func(e string) bool { return strings.HasPrefix(e, "git push "+builtinsteps.PRBranchPrefix) })
	v1 := ImageBundle(ns, PipelineName, "1.1.0")
	p.Ship(t, w, v1)
	require.True(t, w.RunUntil(MaxRounds, func() bool { return len(w.Crashes()) == 1 }), "the controller dies after the push")
	branch := builtinsteps.PRBranch(ns, v1.Name, p.Envs[0])
	crashedAt, err := w.Git.Head(p.Pipeline.Spec.Git.URL, branch)
	require.NoError(t, err)
	s := w.Steps(ns, v1.Name)[p.Envs[0]]
	assert.Contains(t, builtinsteps.PushedSHAs(s.Status.Outputs), crashedAt,
		"the push was recorded before it was made, so the crash after it loses nothing")

	require.True(t, w.RunUntil(MaxRounds, func() bool { return w.BundlePhase(ns)[v1.Name] == "Verified" }),
		"the restarted controller goes on\n%s", w.Describe(ns))
	for env, st := range w.Steps(ns, v1.Name) {
		assert.Empty(t, st.Status.Outputs[parentsteps.OutputPRForeignHead], "%s: nothing taken for someone else's commit", env)
	}
}
