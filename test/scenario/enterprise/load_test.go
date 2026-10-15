// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package enterprise

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

// approveAll is the release manager approving every version that waits
// for the org approval gate, except in the namespaces of hold.
func approveAll(w *scenario.World, hold map[string]bool) error {
	var gates v1alpha1.PolicyGateList
	if err := w.Client.List(w.Ctx, &gates); err != nil {
		return err
	}
	for _, g := range gates.Items {
		b, env := g.Labels["kardinal.io/bundle"], g.Labels["kardinal.io/environment"]
		if g.Spec.Approval == nil || b == "" || g.Status.Ready || hold[g.Namespace] {
			continue
		}
		_, _, err := lifecycle.RecordApproval(w.Ctx, w.Client, lifecycle.ApprovalRequest{Namespace: g.Namespace, Bundle: b,
			Environment: env, User: ReleaseManager, Groups: []string{ReleaseManagers}, Decision: lifecycle.DecisionApprove})
		if err != nil && !strings.Contains(err.Error(), "never promotes again") && !strings.Contains(err.Error(), "already") {
			return fmt.Errorf("approve %s/%s: %w", g.Namespace, b, err)
		}
	}
	return nil
}

// TestEnterprise_MultiTenantLoad is an enterprise on one controller:
// Teams namespaces with PipelinesPerTeam Pipelines each (200 in full),
// the platform team's org gates (a CEL schedule gate and a release-manager
// approval gate) on every prod, each team's own soak gate, half the teams
// on pr-review and half pushing directly, and VersionsPerPipeline versions
// created at once for every Pipeline (1,000 Bundles in full). The
// newest version of every Pipeline ends Verified in every environment and
// the others Superseded or Verified; no object, Graph reference, PR or
// approval crosses a team's namespace; an Approval by another team's
// release manager, in another namespace, for a Bundle of the same name and
// UID, is neither copied into nor opens a team's gate; no object is
// reconciled more than MaxPerObject times, and once the writes stop
// nothing is reconciled at all. Each of these fails when the property
// breaks (see the PR: an Approvals ref without its namespace, an injected
// requeue loop). Covers SCEN-TENANT-01, SCEN-TENANT-02.
func TestEnterprise_MultiTenantLoad(t *testing.T) {
	sz := SizeFromEnv()
	w := scenario.NewWorld(t, scenario.Options{PolicyNamespaces: []string{scenario.PolicyNS}, Watch: true, Tick: 30 * time.Second})
	require.NoError(t, w.Client.Create(w.Ctx, scenario.TenantObjects(scenario.PolicyNS)[0]))
	w.MustApply(OrgGates()...)
	for i := 1; i <= sz.Teams; i++ {
		Tenant(t, w, Team(i), sz.PipelinesPerTeam, i%2 == 0)
	}
	held := map[string]bool{Team(1): true}
	w.Actors = append(w.Actors, scenario.Reviewer, func(w *scenario.World) error { return approveAll(w, held) })
	w.Round()

	start := time.Now()
	newest := map[string]string{}
	for i := 1; i <= sz.Teams; i++ {
		for j := 1; j <= sz.PipelinesPerTeam; j++ {
			for v := 1; v <= sz.VersionsPerPipeline; v++ {
				b := scenario.ImageBundle(Team(i), PipelineName(j), fmt.Sprintf("1.%d.0", v))
				w.MustApply(b)
				newest[Team(i)+"/"+PipelineName(j)] = b.Name
			}
		}
	}

	// team-01's approval waits: an Approval in team-02 naming team-01's
	// Bundle, with its UID, must not open team-01's gate.
	victim := newest[Team(1)+"/"+PipelineName(1)]
	vb := w.Bundle(Team(1), victim)
	forged := &v1alpha1.Approval{ObjectMeta: metav1.ObjectMeta{Namespace: Team(2), Name: "forged",
		Labels: map[string]string{"kardinal.io/bundle": victim, "kardinal.io/environment": "prod", "kardinal.io/pipeline": PipelineName(1)}},
		Spec: v1alpha1.ApprovalSpec{Bundle: victim, BundleUID: string(vb.UID), Environment: "prod", User: forger,
			Groups: []string{ReleaseManagers}, Decision: lifecycle.DecisionApprove}}
	w.MustApply(forged)
	waiting := func() bool {
		_, found := gateReady(w, Team(1), victim, "prod-change-approval")
		return found && len(w.Verified(Team(1), victim)) == 2
	}
	require.True(t, w.RunUntil(200, waiting), "team-01's newest version reaches its approval gate\n%s", w.Describe(Team(1)))
	for i := 0; i < 4; i++ {
		w.Round()
	}
	// The forged Approval is still there (the approver actor, another user,
	// never replaces it), and team-01's gate neither copied it nor opened.
	var still v1alpha1.Approval
	require.NoError(t, w.Client.Get(w.Ctx, client.ObjectKeyFromObject(forged), &still), "the forged Approval exists")
	ready, _ := gateReady(w, Team(1), victim, "prod-change-approval")
	assert.False(t, ready, "an Approval in another namespace opened team-01's gate")
	for _, g := range w.GateInstances(Team(1), victim) {
		for _, a := range g.Spec.Approvals {
			assert.NotEqual(t, forger, a.User, "gate %s copied the Approval of another namespace", g.Name)
		}
	}
	delete(held, Team(1))

	done := func() bool {
		for key, b := range newest {
			ns := strings.SplitN(key, "/", 2)[0]
			if w.Bundle(ns, b).Status.Phase != "Verified" {
				return false
			}
		}
		return true
	}
	if !w.RunUntil(400, done) {
		var stuck []string
		for key, b := range newest {
			ns := strings.SplitN(key, "/", 2)[0]
			if ph := w.Bundle(ns, b).Status.Phase; ph != "Verified" {
				stuck = append(stuck, key+"="+ph)
			}
		}
		sort.Strings(stuck)
		t.Fatalf("after %d rounds, %d newest versions are not Verified: %v\n%s", w.Rounds(), len(stuck), stuck[:min(len(stuck), 10)],
			w.Describe(strings.SplitN(stuck[0], "/", 2)[0]))
	}
	t.Logf("%d Pipelines, %d Bundles: %d rounds, %s, reconciles %v", sz.Teams*sz.PipelinesPerTeam, len(newest)*sz.VersionsPerPipeline,
		w.Rounds(), time.Since(start).Round(time.Second), w.Reconciles())

	checkPhases(t, w, sz, newest)
	CheckIsolation(t, w, sz)
	CheckBounded(t, w, MaxPerObject)
	n, last := w.Errors()
	assert.Empty(t, n, "reconcile errors: %v", last)
	assert.Empty(t, w.Kro.Errors())
	assert.Empty(t, w.SCM.Unknown())
}

func gateReady(w *scenario.World, ns, bundle, gate string) (ready, found bool) {
	ready = true
	for _, g := range w.GateInstances(ns, bundle) {
		if g.Labels["kardinal.io/gate-name"] == gate {
			found = true
			ready = ready && g.Status.Ready
		}
	}
	return ready, found
}

// checkPhases: the newest version of each Pipeline is Verified and runs
// everywhere; the older ones are Superseded, or Verified when they
// finished first.
func checkPhases(t *testing.T, w *scenario.World, sz Size, newest map[string]string) {
	t.Helper()
	for i := 1; i <= sz.Teams; i++ {
		ns := Team(i)
		for name, phase := range w.BundlePhase(ns) {
			b := w.Bundle(ns, name)
			if newest[ns+"/"+b.Spec.Pipeline] == name {
				continue
			}
			assert.Contains(t, []string{"Superseded", "Verified"}, phase, "%s/%s", ns, name)
		}
		for j := 1; j <= sz.PipelinesPerTeam; j++ {
			for _, env := range []string{"dev", "staging", "prod"} {
				dir := "environments/" + env
				c, _, err := w.Git.ReadFile(repoURL(ns, PipelineName(j)), "main", dir+"/kustomization.yaml")
				require.NoError(t, err)
				assert.Contains(t, c, fmt.Sprintf("newTag: 1.%d.0", sz.VersionsPerPipeline), "%s/%s %s", ns, PipelineName(j), env)
			}
		}
	}
	for _, pr := range w.SCM.PRs() {
		assert.NotEqual(t, "open", pr.State, "PR %s#%d left open", pr.Repo, pr.Number)
	}
}
