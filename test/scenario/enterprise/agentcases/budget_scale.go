// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentcases

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise"
)

// The scale of agents with budgets (SCEN-AGENT-BUDGET-03). Full is the
// release gate's (KARDINAL_SCENARIO_SCALE=full): 5,000 AgentIdentities and
// 50 agents in 25 tenants, under the 1 MiB index bound, which the object
// format fills at about 5,200 (202 bytes each, RunScale's log). 7,500 waits
// for #2372's compact entries, which fit them in the same bound: with it,
// full runs 7,500. The bound is never raised here: a size no real cluster
// can have would prove less than it seems. Medium is CI's
// scenario-enterprise job, quick go test ./....
var (
	fullBudgetScale   = agentSize{Teams: 25, PipelinesPerTeam: 2, Identities: 5000, Agents: 50, Bursts: 2}
	mediumBudgetScale = agentSize{Teams: 20, PipelinesPerTeam: 2, Identities: 2000, Agents: 40, Bursts: 2}
	quickBudgetScale  = agentSize{Teams: 3, PipelinesPerTeam: 2, Identities: 150, Agents: 6, Bursts: 2}
)

func budgetScaleFromEnv() (agentSize, string) {
	switch {
	case os.Getenv("KARDINAL_SCENARIO_SCALE") == "full":
		return fullBudgetScale, "full"
	case os.Getenv("KARDINAL_SCENARIO_ENTERPRISE") == "full":
		return mediumBudgetScale, "medium"
	}
	return quickBudgetScale, "quick"
}

// everyRule is each agent's budget at scale: every environment, room for
// both bursts, so it counts every promotion and holds none.
var everyRule = v1alpha1.AgentBudgetRule{Name: "all", Environments: []string{"*"}, MaxConcurrent: 2, VersionsPerDay: 20}

// maxUsageWritesPerPromotion is section 14's bound: a grant, its settling,
// and at most one more (a ledger made current, a window's prune).
const maxUsageWritesPerPromotion = 3.0

// RunBudgetScale is the enterprise at the scale of agents with budgets on
// and every active agent budgeted (everyRule), through Bursts bursts of a
// version per agent: each burst converges; every promotion of an agent's
// version is granted in its ledger, at most maxUsageWritesPerPromotion
// AgentIdentity writes per promotion on average; every ledger stays within
// its bounds; and no usage write rebuilds the agent index (W0's filter of
// its AgentIdentity watch, agentidentity.IdentityChanged, which the World
// applies to every AgentIdentity write it watches).
func RunBudgetScale(t *testing.T) {
	sz, mode := budgetScaleFromEnv()
	reg := agentRegistry(sz)
	require.GreaterOrEqual(t, len(reg.active), sz.Agents*9/10, "the registry has an active agent per team in turn")
	w := scenario.NewWorld(t, scenario.Options{PolicyNamespaces: []string{scenario.PolicyNS}, Watch: true, Tick: 30 * time.Second,
		Actors: true, Budgets: true, MaxIndexBytes: sz.IndexBytes})
	require.NoError(t, w.Client.Create(w.Ctx, scenario.TenantObjects(scenario.PolicyNS)[0]))
	w.MustApply(enterprise.OrgGates()...)
	for i := 1; i <= sz.Teams; i++ {
		enterprise.Tenant(t, w, enterprise.Team(i), sz.PipelinesPerTeam, i%2 == 0)
	}
	start := time.Now()
	w.MustApply(reg.ids...)
	require.True(t, w.RunUntil(20, func() bool { return w.Round() == 0 }), "the registry settles")
	var idx v1alpha1.AgentIndex
	require.NoError(t, w.Client.Get(w.Ctx, types.NamespacedName{Name: scenario.Release}, &idx))
	require.True(t, meta.IsStatusConditionTrue(idx.Status.Conditions, "Ready"), "the index is Ready: %v", idx.Status.Conditions)
	for _, a := range reg.active {
		setBudgets(t, w, a.name, everyRule)
	}
	require.True(t, w.RunUntil(20, func() bool { return w.Round() == 0 }), "the budgets are evaluated")
	for _, a := range reg.active {
		require.True(t, ledgerCurrent(w, a.name), "agent %s's ledger", a.name)
	}
	t.Logf("%s: %d AgentIdentities, %d of them budgeted agents, in %d tenants: registry and ledgers in %s",
		mode, sz.Identities, len(reg.active), sz.Teams, time.Since(start).Round(time.Millisecond))

	grants := func() int {
		n := 0
		for _, a := range reg.active {
			if u := w.Ledger(a.name); u != nil {
				n += len(u.Grants)
			}
		}
		return n
	}
	indexBefore, writesBefore, grantsBefore := w.Reconciles()["AgentIndex"], w.WritesByKind()["AgentIdentity"], grants()
	w.Actors = append(w.Actors, scenario.Reviewer, approveAs)
	shipped := 0
	for burst := 1; burst <= sz.Bursts; burst++ {
		var versions []string
		rounds, began := w.Rounds(), time.Now()
		for _, a := range reg.active {
			b := scenario.ImageBundle(a.ns, a.pipeline, fmt.Sprintf("4.%d.0", burst))
			b.Name = fmt.Sprintf("%s-%s-g%d", a.pipeline, a.name, burst)
			cliErr, refused, err := w.CreateBundle(a.id, b)
			require.NoError(t, err)
			require.NoError(t, cliErr, "agent %s", a.name)
			require.Empty(t, refused, "agent %s", a.name)
			versions = append(versions, a.ns+"/"+b.Name)
		}
		require.True(t, w.RunUntil(othersRounds+30, func() bool {
			for _, key := range versions {
				ns, name, _ := strings.Cut(key, "/")
				if w.Bundle(ns, name).Status.Phase != "Verified" {
					return false
				}
			}
			return true
		}), "burst %d converges with budgets on", burst)
		shipped += len(versions)
		t.Logf("burst %d: %d budgeted agents' versions Verified everywhere in %d rounds, %s",
			burst, len(versions), w.Rounds()-rounds, time.Since(began).Round(time.Millisecond))
	}

	promotions := grants() - grantsBefore
	usageWrites := w.WritesByKind()["AgentIdentity"] - writesBefore
	assert.Equal(t, shipped*3, promotions, "every promotion of an agent's version (dev, staging, prod) is granted once")
	perPromotion := float64(usageWrites) / float64(max(promotions, 1))
	t.Logf("%s: %d promotions, %d AgentIdentity writes (%.2f per promotion), %d agent index reconciles in the bursts",
		mode, promotions, usageWrites, perPromotion, w.Reconciles()["AgentIndex"]-indexBefore)
	assert.LessOrEqual(t, perPromotion, maxUsageWritesPerPromotion, "usage writes per promotion (docs/design/24-agent-budgets.md section 14)")
	assert.Zero(t, w.Reconciles()["AgentIndex"]-indexBefore, "no agent index reconcile follows a usage write")

	// Every ledger within its bounds: 2,000 grants, and the AgentIdentity
	// under 600 KB (the soak's invariant; the controller's own cap is 1 MiB).
	for _, a := range reg.active {
		var ai v1alpha1.AgentIdentity
		require.NoError(t, w.Client.Get(w.Ctx, types.NamespacedName{Name: a.name}, &ai))
		u := ai.Status.Usage[scenario.Release]
		assert.LessOrEqual(t, len(u.Grants), 2000, "agent %s's ledger", a.name)
		assert.Nil(t, meta.FindStatusCondition(u.Conditions, "LedgerFull"), "agent %s's ledger is not full", a.name)
		raw, err := json.Marshal(&ai)
		require.NoError(t, err)
		assert.Less(t, len(raw), 600<<10, "agent %s's AgentIdentity", a.name)
	}
	n, last := w.Errors()
	assert.Empty(t, n, "reconcile errors: %v", last)
	assert.Empty(t, w.Kro.Errors())
	assert.Empty(t, w.SCM.Unknown())
}
