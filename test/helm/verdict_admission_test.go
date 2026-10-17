// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIdentityAdmission_Verdicts (docs/design/19-verdicts.md, section 4):
// a Verdict names its writer and the writer's own groups, its labels match
// its spec, it is owned only by its Bundle, a run-bound Verdict comes from
// that AgentRun's Pod and is named after it, and only its writer (or the
// garbage collector) deletes it.
func TestIdentityAdmission_Verdicts(t *testing.T) {
	t.Parallel()
	vap := identityPolicy(t, "verdicts")
	verdict := func(name, by string, groups []interface{}) map[string]interface{} {
		return map[string]interface{}{
			"metadata": map[string]interface{}{"name": name, "namespace": "payments", "labels": map[string]interface{}{
				"kardinal.io/bundle": "app-v1", "kardinal.io/environment": "uat", "kardinal.io/check": "explore"}},
			"spec": map[string]interface{}{"pipeline": "app", "bundle": "app-v1", "bundleUID": "uid-1",
				"environment": "uat", "check": "explore", "result": "pass", "by": by, "groups": groups},
		}
	}
	withRun := func(v map[string]interface{}, run string) map[string]interface{} {
		v["spec"].(map[string]interface{})["run"] = map[string]interface{}{"name": run, "index": 1, "attempt": 1}
		return v
	}
	withLabel := func(v map[string]interface{}, k, val string) map[string]interface{} {
		v["metadata"].(map[string]interface{})["labels"].(map[string]interface{})[k] = val
		return v
	}
	withOwner := func(v map[string]interface{}, kind, name, uid string) map[string]interface{} {
		v["metadata"].(map[string]interface{})["ownerReferences"] = []interface{}{
			map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": kind, "name": name, "uid": uid}}
		return v
	}
	pod := func(name string) map[string]interface{} {
		return map[string]interface{}{"userInfoExtra": map[string]interface{}{
			"authentication.kubernetes.io/pod-name": []interface{}{name}}}
	}
	agent := "system:serviceaccount:payments:qa"
	sa := []interface{}{"system:serviceaccounts", "system:serviceaccounts:payments", "system:authenticated"}
	run := "app-app-v1-uat-explore-r1-a1"
	runSA := "system:serviceaccount:payments:kr-" + run
	tests := []struct {
		name     string
		obj, old map[string]interface{}
		user     string
		groups   []interface{}
		request  []map[string]interface{}
		want     bool
	}{
		{name: "a person in their own name", obj: verdict("v", "alice", nil), user: "alice", want: true},
		{name: "in someone else's name", obj: verdict("v", "alice", nil), user: "mallory", want: false},
		{name: "own groups", obj: verdict("v", "alice", []interface{}{"qa-team"}), user: "alice",
			groups: []interface{}{"qa-team", "system:authenticated"}, want: true},
		{name: "a group not one's own", obj: verdict("v", "alice", []interface{}{"release-managers"}), user: "alice", want: false},
		{name: "labels that do not match the spec", obj: withLabel(verdict("v", "alice", nil), "kardinal.io/check", "smoke"),
			user: "alice", want: false},
		{name: "owned by its Bundle", obj: withOwner(verdict("v", "alice", nil), "Bundle", "app-v1", "uid-1"), user: "alice", want: true},
		{name: "owned by another Bundle", obj: withOwner(verdict("v", "alice", nil), "Bundle", "app-v0", "uid-0"), user: "alice", want: false},
		{name: "a run's Verdict from its Pod, as the run's ServiceAccount", obj: withRun(verdict(run, runSA, nil), run), user: runSA, groups: sa,
			request: []map[string]interface{}{pod(run + "-x7k2p")}, want: true},
		{name: "a run's Verdict without a Pod (a minted token)", obj: withRun(verdict(run, runSA, nil), run), user: runSA, groups: sa,
			want: false},
		{name: "a run's Verdict written by the agent's own ServiceAccount", obj: withRun(verdict(run, agent, nil), run), user: agent, groups: sa,
			request: []map[string]interface{}{pod(run + "-x7k2p")}, want: false},
		{name: "a run's Verdict from another run's Pod", obj: withRun(verdict(run, runSA, nil), run), user: runSA, groups: sa,
			request: []map[string]interface{}{pod("app-app-v1-uat-explore-r2-a1-x7k2p")}, want: false},
		{name: "run X's ServiceAccount claims run X-r2 (another run)",
			obj: withRun(verdict(run+"-r2", runSA, nil), run+"-r2"), user: runSA, groups: sa,
			request: []map[string]interface{}{pod(run + "-r2-x7k2p")}, want: false},
		{name: "a Pod of run X-y… is not run X's Pod (no prefix match)", obj: withRun(verdict(run, runSA, nil), run), user: runSA, groups: sa,
			request: []map[string]interface{}{pod(run + "-extra-x7k2p")}, want: false},
		{name: "a run's Verdict not named after the run", obj: withRun(verdict("extra-vote", runSA, nil), run), user: runSA, groups: sa,
			request: []map[string]interface{}{pod(run + "-x7k2p")}, want: false},
		{name: "a run's ServiceAccount cannot write an unbound Verdict (a second vote)", obj: verdict("extra", runSA, nil), user: runSA,
			groups: sa, request: []map[string]interface{}{pod(run + "-x7k2p")}, want: false},
		{name: "an agent's own ServiceAccount writes an unbound Verdict", obj: verdict("v", agent, nil), user: agent, groups: sa, want: true},
		{name: "the writer revokes", old: verdict("v", "alice", nil), user: "alice", want: true},
		{name: "someone else revokes", old: verdict("v", "alice", nil), user: "mallory", want: false},
		{name: "the garbage collector deletes with the Bundle", old: verdict("v", "alice", nil),
			user: "system:serviceaccount:kube-system:generic-garbage-collector", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			groups := tc.groups
			if groups == nil {
				groups = []interface{}{"system:authenticated"}
			}
			assert.Equal(t, tc.want, admitsGroups(t, vap, tc.obj, tc.old, tc.user, groups, tc.request...))
		})
	}
}
