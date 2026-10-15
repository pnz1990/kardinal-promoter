// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package enterprise

import (
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

// agentSize is the agent load: Identities AgentIdentities registered, of
// which Agents act, across Teams with PipelinesPerTeam Pipelines, in Bursts
// rounds of versions; Probes requests checked against pkg/actor;
// IndexBytes is the index's bound (0: actor.MaxIndexBytes, 1 MiB).
type agentSize struct{ Teams, PipelinesPerTeam, Identities, Agents, Bursts, Probes, IndexBytes int }

// fullAgents is CI's scenario-enterprise job (KARDINAL_SCENARIO_ENTERPRISE=
// full): 2,000 AgentIdentities, half the index's 4,000-subject bound, and 50
// agents across 20 teams. quickAgents keeps go test ./... fast, with a
// 64 KiB bound to overflow.
var (
	fullAgents  = agentSize{Teams: 20, PipelinesPerTeam: 3, Identities: 2000, Agents: 50, Bursts: 3, Probes: 4000}
	quickAgents = agentSize{Teams: 4, PipelinesPerTeam: 2, Identities: 200, Agents: 8, Bursts: 2, Probes: 300, IndexBytes: 64 << 10}
)

func agentSizeFromEnv() agentSize {
	if os.Getenv("KARDINAL_SCENARIO_ENTERPRISE") == "full" {
		return fullAgents
	}
	return quickAgents
}

// registry is the generated agent registry: who acts, and who must be
// refused.
type registry struct {
	ids []client.Object
	// active are the agents that ship, one per team in turn: agent name ->
	// its ServiceAccount, team and Pipeline.
	active []activeAgent
	// sponsors of every AgentIdentity, by name.
	sponsor map[string]string
	// principals are users the probes write as: every subject of every
	// AgentIdentity, and people and bots that are none.
	principals []scenario.Identity
}

type activeAgent struct {
	name, ns, pipeline string
	id                 scenario.Identity
}

// agentRegistry generates n AgentIdentities, deterministically, the way an
// enterprise has them: most a ServiceAccount scoped to their team (some to
// one Pipeline, some to every team by prefix, some to a few environments),
// some an OIDC user or group, one in 30 suspended, one in 50 listing an older
// one's subject (not ready, its other subject blocked), one in 100 sponsored
// by an agent (not ready), one in 60 scoped nowhere. The first `agents`
// teams-scoped, ready ones are the active agents.
func agentRegistry(sz agentSize) registry {
	rng := rand.New(rand.NewPCG(1837, 1867))
	reg := registry{sponsor: map[string]string{}}
	var subjects []v1alpha1.AgentSubject
	var users []string // the User subjects so far
	candidates := map[string][]activeAgent{}
	created := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for i := 0; i < sz.Identities; i++ {
		name := fmt.Sprintf("agent-%04d", i)
		ns := team(1 + i%sz.Teams)
		sponsor := fmt.Sprintf("sponsor-%02d@corp.example.com", rng.IntN(30))
		subj := []v1alpha1.AgentSubject{{Kind: "ServiceAccount", Namespace: ns, Name: name}}
		switch rng.IntN(10) {
		case 0:
			subj = []v1alpha1.AgentSubject{{Kind: "User", Name: "oidc:" + name}}
		case 1:
			subj = append(subj, v1alpha1.AgentSubject{Kind: "Group", Name: "agents:" + name})
		}
		scope := v1alpha1.AgentScope{Namespaces: []string{ns}}
		switch rng.IntN(8) {
		case 0:
			scope.Pipelines = []string{pipelineName(1)}
		case 1:
			scope.Namespaces = []string{"team-0*"}
		case 2:
			scope.Environments = []string{"dev", "staging"}
		case 3:
			scope.Pipelines = []string{"app-0*"}
		}
		a := scenario.AgentIdentityOf(name, subj, nil, false)
		a.Spec.Scope = scope
		a.Spec.Sponsor.User = sponsor
		switch {
		case i%60 == 59:
			a.Spec.Scope.Namespaces = nil
		case i%30 == 29:
			a.Spec.Suspended = true
		case i%50 == 49 && len(subjects) > 0:
			a.Spec.Subjects = append(a.Spec.Subjects, subjects[rng.IntN(len(subjects))])
		case i%100 == 99 && len(users) > 0:
			a.Spec.Sponsor.User = users[rng.IntN(len(users))]
		}
		// Oldest first, one second apart: Evaluate's order.
		a.CreationTimestamp = metav1.NewTime(created.Add(time.Duration(i) * time.Second))
		subjects = append(subjects, a.Spec.Subjects[0])
		if a.Spec.Subjects[0].Kind == "User" {
			users = append(users, a.Spec.Subjects[0].Name)
		}
		reg.ids = append(reg.ids, a)
		reg.sponsor[name] = a.Spec.Sponsor.User
		for _, s := range a.Spec.Subjects {
			id := scenario.Identity{Username: actor.SubjectKey(s), Groups: []string{"system:authenticated"}}
			if s.Kind == "Group" {
				id = scenario.Identity{Username: "oidc:member-of-" + name, Groups: []string{s.Name, "system:authenticated"}}
			}
			reg.principals = append(reg.principals, id)
		}
		plain := a.Spec.Subjects[0].Kind == "ServiceAccount" && len(a.Spec.Subjects) == 1 && !a.Spec.Suspended &&
			len(scope.Pipelines) == 0 && len(scope.Environments) == 0 && len(a.Spec.Scope.Namespaces) == 1 &&
			a.Spec.Scope.Namespaces[0] == ns && !strings.HasPrefix(a.Spec.Sponsor.User, "oidc:") && i%50 != 49
		if plain {
			candidates[ns] = append(candidates[ns], activeAgent{name: name, ns: ns,
				id: scenario.Identity{Username: actor.SubjectKey(subj[0]), Groups: []string{"system:serviceaccounts", "system:authenticated"},
					Asserted: v1alpha1.AssertedActor{Model: "model-" + name, Session: "load"}}})
		}
	}
	// One active agent per team in turn, each on its own Pipeline.
	for k := 0; k < sz.Agents; k++ {
		ns := team(1 + k%sz.Teams)
		if k/sz.Teams < len(candidates[ns]) {
			a := candidates[ns][k/sz.Teams]
			a.pipeline = pipelineName(1 + k/sz.Teams%sz.PipelinesPerTeam)
			reg.active = append(reg.active, a)
		}
	}
	for i := 0; i < 40; i++ {
		reg.principals = append(reg.principals,
			scenario.Identity{Username: fmt.Sprintf("person-%02d@corp.example.com", i), Groups: []string{"system:authenticated"}},
			scenario.Identity{Username: fmt.Sprintf("system:serviceaccount:ci:bot-%02d", i), Groups: []string{"system:serviceaccounts", "system:authenticated"}})
	}
	return reg
}

// TestEnterprise_AgentLoad is the enterprise of the load test with agents:
// Identities AgentIdentities registered (2,000 in full: half the index's
// bound), Agents of them shipping versions in bursts across every team, a
// release manager approving prod, every write admitted by the API server's
// ValidatingAdmissionPolicy plugin with the chart's actor policy and the
// controller's AgentIndex as its param. Probes requests by every kind of
// principal (each AgentIdentity's subjects, people, bots), with the CLI's
// stamp or a forged one, in and out of scope, get the verdict pkg/actor
// (Derive, Scope) gives: the policy is its transliteration, at scale, and
// no refusal is an expression error. A suspension refuses the agent's next
// write; versions it shipped before still promote. Every version is stamped
// with its agent and sponsor, and so is every promotion AuditEvent and PR. An
// index pushed over its 1 MiB bound refuses every write the policy checks
// and none of the controller's, and recovers when the AgentIdentities go.
// Covers SCEN-AGENT-LOAD-01, SCEN-AGENT-DIFF-01.
func TestEnterprise_AgentLoad(t *testing.T) {
	sz := agentSizeFromEnv()
	reg := agentRegistry(sz)
	require.Len(t, reg.active, sz.Agents, "the registry has an active agent per team in turn")
	w := scenario.NewWorld(t, scenario.Options{PolicyNamespaces: []string{scenario.PolicyNS}, Watch: true, Tick: 30 * time.Second,
		Actors: true, MaxIndexBytes: sz.IndexBytes})
	require.NoError(t, w.Client.Create(w.Ctx, scenario.TenantObjects(scenario.PolicyNS)[0]))
	w.MustApply(orgGates()...)
	for i := 1; i <= sz.Teams; i++ {
		tenant(t, w, team(i), sz.PipelinesPerTeam, i%2 == 0)
	}

	// Register the agents and build the index, as the controller does.
	start := time.Now()
	w.MustApply(reg.ids...)
	before := w.Reconciles()["AgentIdentity"]
	require.True(t, w.RunUntil(20, func() bool { return w.Round() == 0 }), "the registry settles")
	var idx v1alpha1.AgentIndex
	require.NoError(t, w.Client.Get(w.Ctx, types.NamespacedName{Name: scenario.Release}, &idx))
	require.True(t, meta.IsStatusConditionTrue(idx.Status.Conditions, "Ready"), "the index is Ready: %v", idx.Status.Conditions)
	raw, err := yaml.Marshal(idx.Status)
	require.NoError(t, err)
	t.Logf("registry: %d AgentIdentities, %d users and %d groups in the index (%d KiB of %d), built and checked in %s with %d AgentIdentity reconciles",
		sz.Identities, len(idx.Status.Users), len(idx.Status.Groups), len(raw)>>10, sz.bound()>>10,
		time.Since(start).Round(time.Millisecond), w.Reconciles()["AgentIdentity"]-before)
	assert.Equal(t, sz.Identities, idx.Status.ObservedIdentities)

	// The policy is pkg/actor's transliteration: the same verdict on every
	// probe, over the whole index.
	probeAgainstGo(t, w, reg, &idx.Status, sz)

	// Bursts of versions by the active agents, while the release manager
	// approves prod and out-of-scope and suspended agents are refused.
	w.Actors = append(w.Actors, scenario.Reviewer, approveAs)
	newest := map[string]string{} // ns/pipeline -> Bundle
	shipper := map[string]activeAgent{}
	suspended := map[string]bool{}
	for burst := 1; burst <= sz.Bursts; burst++ {
		for _, a := range reg.active {
			b := scenario.ImageBundle(a.ns, a.pipeline, fmt.Sprintf("1.%d.%d", burst, len(newest)%7))
			b.Name = fmt.Sprintf("%s-%s-b%d", a.pipeline, a.name, burst)
			cliErr, refused, err := w.CreateBundle(a.id, b)
			require.NoError(t, err)
			if suspended[a.name] {
				assert.ErrorIs(t, cliErr, actor.ErrBlocked, "suspended agent %s", a.name)
				forged := b.DeepCopy()
				forged.Spec.Actor = &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: a.id.Username, Agent: a.name, Sponsor: reg.sponsor[a.name]}
				reasons, err := w.ApplyAs(w.CtxOf(a.id), forged)
				require.NoError(t, err)
				assert.Contains(t, strings.Join(reasons, "; "), "agent "+a.name+" may not act: suspended")
				continue
			}
			require.NoError(t, cliErr, "agent %s", a.name)
			require.Empty(t, refused, "agent %s", a.name)
			newest[a.ns+"/"+a.pipeline] = b.Name
			shipper[a.ns+"/"+b.Name] = a
			// The same agent in a team it is not scoped to.
			other := team(1 + (teamIndex(a.ns))%sz.Teams)
			o := scenario.ImageBundle(other, a.pipeline, "9.9.9")
			cliErr, _, err = w.CreateBundle(a.id, o)
			require.NoError(t, err)
			assert.ErrorIs(t, cliErr, actor.ErrOutOfScope, "agent %s in %s", a.name, other)
			o.Spec.Actor = &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: a.id.Username, Agent: a.name, Sponsor: reg.sponsor[a.name]}
			reasons, err := w.ApplyAs(w.CtxOf(a.id), o)
			require.NoError(t, err)
			assert.Contains(t, strings.Join(reasons, "; "), "agent "+a.name+" may not act here (namespace "+other)
		}
		for i := 0; i < 3; i++ {
			w.Round()
		}
		if burst == 1 {
			// Suspend one active agent in ten: from the next index build,
			// its writes are refused.
			for i, a := range reg.active {
				if i%10 == 3 {
					var ai v1alpha1.AgentIdentity
					require.NoError(t, w.Client.Get(w.Ctx, types.NamespacedName{Name: a.name}, &ai))
					ai.Spec.Suspended = true
					require.NoError(t, w.Client.Update(scenario.AsUser(w.Ctx, scenario.AdminUser, scenario.AdminGroups...), &ai))
					suspended[a.name] = true
				}
			}
			w.Round()
		}
	}
	done := func() bool {
		for key, b := range newest {
			ns := strings.SplitN(key, "/", 2)[0]
			if w.Bundle(ns, b).Status.Phase != "Verified" {
				return false
			}
		}
		return true
	}
	if !w.RunUntil(300, done) {
		var stuck []string
		for key, b := range newest {
			ns := strings.SplitN(key, "/", 2)[0]
			if ph := w.Bundle(ns, b).Status.Phase; ph != "Verified" {
				stuck = append(stuck, key+"="+b+":"+ph)
			}
		}
		sort.Strings(stuck)
		t.Fatalf("after %d rounds, %d newest versions are not Verified: %v\n%s", w.Rounds(), len(stuck), stuck[:min(len(stuck), 10)],
			w.Describe(strings.SplitN(stuck[0], "/", 2)[0]))
	}
	t.Logf("%d agents, %d versions over %d bursts (%d agents suspended after the first): %d rounds, %s, reconciles %v",
		sz.Agents, len(shipper), sz.Bursts, len(suspended), w.Rounds(), time.Since(start).Round(time.Second), w.Reconciles())
	checkAgentStamps(t, w, sz, reg, shipper)
	checkIsolation(t, w, size{Teams: sz.Teams, PipelinesPerTeam: sz.PipelinesPerTeam})
	// A version lives through the bursts after it: the bound is per burst.
	checkBounded(t, w, maxPerObject*sz.Bursts)

	overflowAtScale(t, w, reg, sz)
	n, last := w.Errors()
	assert.Empty(t, n, "reconcile errors: %v", last)
	assert.Empty(t, w.Kro.Errors())
	assert.Empty(t, w.SCM.Unknown())
}

func teamIndex(ns string) int {
	var i int
	_, _ = fmt.Sscanf(ns, "team-%d", &i)
	return i
}

// approveAs is the release manager approving every prod that waits, as
// kardinal approve does: as themself, with their actor stamp.
func approveAs(w *scenario.World) error {
	var gates v1alpha1.PolicyGateList
	if err := w.Client.List(w.Ctx, &gates); err != nil {
		return err
	}
	rm := scenario.Identity{Username: releaseManager, Groups: []string{releaseManagers, "system:authenticated"}}
	for _, g := range gates.Items {
		b, env := g.Labels["kardinal.io/bundle"], g.Labels["kardinal.io/environment"]
		if g.Spec.Approval == nil || b == "" || g.Status.Ready {
			continue
		}
		stamp, err := w.StampFor(rm, actor.Where{Namespace: g.Namespace, Environment: env})
		if err != nil {
			return err
		}
		_, _, err = lifecycle.RecordApproval(w.CtxOf(rm), w.Client, lifecycle.ApprovalRequest{Namespace: g.Namespace, Bundle: b,
			Environment: env, User: rm.Username, Groups: []string{releaseManagers}, Decision: lifecycle.DecisionApprove, Actor: stamp})
		if err != nil && !strings.Contains(err.Error(), "never promotes again") && !strings.Contains(err.Error(), "already") {
			return fmt.Errorf("approve %s/%s: %w", g.Namespace, b, err)
		}
	}
	return nil
}

// probeAgainstGo sends sz.Probes creations (Bundles and Approvals) by
// random principals at random places, each with the CLI's stamp, no stamp,
// or a forged one, through the admission plugin (writing nothing), and
// checks each verdict against pkg/actor's, and that every refusal is one of
// the policy's messages. It logs the admission latency.
func probeAgainstGo(t *testing.T, w *scenario.World, reg registry, idx *v1alpha1.AgentIndexStatus, sz agentSize) {
	t.Helper()
	rng := rand.New(rand.NewPCG(1867, 1837))
	var lat []time.Duration
	verdicts := map[string]int{}
	mismatches := 0
	for i := 0; i < sz.Probes; i++ {
		id := reg.principals[rng.IntN(len(reg.principals))]
		ns := team(1 + rng.IntN(sz.Teams))
		// Half the time a ServiceAccount writes in its own namespace, where
		// its agent is usually scoped.
		if own, ok := strings.CutPrefix(id.Username, "system:serviceaccount:"); ok && rng.IntN(2) == 0 {
			ns, _, _ = strings.Cut(own, ":")
		}
		where := actor.Where{Namespace: ns}
		r, derr := actor.Derive(actor.User{Username: id.Username, Groups: id.Groups}, idx)
		var stamp *v1alpha1.Actor
		switch rng.IntN(6) {
		case 0: // no stamp
		case 1: // forged sponsor
			if derr == nil {
				s := r.Actor
				s.Sponsor = "someone-else@corp.example.com"
				stamp = &s
			}
		case 2: // a person, says the stamp
			stamp = &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: id.Username}
		default: // what the CLI stamps
			if derr == nil {
				s := r.Actor
				stamp = &s
			}
		}
		var obj client.Object
		if rng.IntN(3) == 0 {
			where.Environment = []string{"dev", "staging", "prod"}[rng.IntN(3)]
			bundle := fmt.Sprintf("probe-%d", i)
			obj = &v1alpha1.Approval{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: bundle,
				Labels: map[string]string{"kardinal.io/bundle": bundle, "kardinal.io/environment": where.Environment}},
				Spec: v1alpha1.ApprovalSpec{Bundle: bundle, BundleUID: "u", Environment: where.Environment, User: id.Username,
					Decision: lifecycle.DecisionApprove, Actor: stamp}}
		} else {
			where.Pipeline = pipelineName(1 + rng.IntN(sz.PipelinesPerTeam))
			b := scenario.ImageBundle(ns, where.Pipeline, fmt.Sprintf("0.0.%d", i))
			b.Spec.Actor = stamp
			obj = b
		}
		want := derr == nil && actor.Scope(r.Entry, where) == nil && sameStamp(stamp, r, id.Username)
		t0 := time.Now()
		err := w.AdmitCreate(w.CtxOf(id), obj)
		lat = append(lat, time.Since(t0))
		got := err == nil
		verdicts[fmt.Sprintf("%s/admit=%v", r.Actor.Kind, want)]++
		if got != want {
			mismatches++
			if mismatches <= 10 {
				t.Errorf("probe %d: %s (groups %v) creating %T in %+v with stamp %+v: pkg/actor says admit=%v (derive err %v), the policy %v",
					i, id.Username, id.Groups, obj, where, stamp, want, derr, err)
			}
		}
		if err != nil {
			assert.Contains(t, err.Error(), "ValidatingAdmissionPolicy 'kardinal-promoter-actor'", "probe %d", i)
			assert.NotContains(t, err.Error(), "failed to evaluate", "probe %d: a refusal names its reason", i)
			assert.NotContains(t, err.Error(), "no such key", "probe %d: a refusal names its reason", i)
		}
	}
	assert.Zero(t, mismatches, "verdicts that differ from pkg/actor")
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p := func(q float64) time.Duration { return lat[int(q*float64(len(lat)-1))] }
	t.Logf("%d probes against an index of %d users and %d groups: %v; admission p50 %s, p99 %s, max %s", sz.Probes,
		len(idx.Users), len(idx.Groups), verdicts, p(0.5), p(0.99), lat[len(lat)-1])
	if !raceEnabled {
		// The plugin converts the whole AgentIndex param to JSON and back
		// for every request it judges (#1936): 13 ms at 2,000 agents.
		assert.Less(t, p(0.99), 50*time.Millisecond, "the actor policy's p99 admission latency")
	}
}

// sameStamp is the actor policy's rule as docs/actors.md states it: an
// agent stamps, and a stamp says exactly what the index says, with no via.
func sameStamp(s *v1alpha1.Actor, r actor.Resolved, username string) bool {
	if s == nil {
		return r.Entry == nil
	}
	return s.Kind == r.Actor.Kind && s.Principal == username && s.Via == "" && s.Agent == r.Actor.Agent && s.Sponsor == r.Actor.Sponsor
}

// checkAgentStamps: every version is stamped with the agent that shipped
// it and its sponsor, every promotion AuditEvent with its version's stamp,
// every PR on a reviewing team says which agent, for whom.
func checkAgentStamps(t *testing.T, w *scenario.World, sz agentSize, reg registry, shipper map[string]activeAgent) {
	t.Helper()
	promotion := map[string]bool{psrec.AuditActionPromotionStarted: true, psrec.AuditActionPromotionSucceeded: true,
		psrec.AuditActionPromotionSuperseded: true}
	audited := 0
	for i := 1; i <= sz.Teams; i++ {
		ns := team(i)
		var bl v1alpha1.BundleList
		require.NoError(t, w.Client.List(w.Ctx, &bl, client.InNamespace(ns)))
		stamps := map[string]v1alpha1.Actor{}
		for _, b := range bl.Items {
			a, ok := shipper[ns+"/"+b.Name]
			require.True(t, ok, "Bundle %s/%s was not shipped by an agent", ns, b.Name)
			require.NotNil(t, b.Spec.Actor, "Bundle %s/%s", ns, b.Name)
			assert.Equal(t, v1alpha1.ActorAgent, b.Spec.Actor.Kind)
			assert.Equal(t, a.name, b.Spec.Actor.Agent, "Bundle %s/%s", ns, b.Name)
			assert.Equal(t, reg.sponsor[a.name], b.Spec.Actor.Sponsor, "Bundle %s/%s", ns, b.Name)
			assert.Equal(t, a.id.Username, b.Spec.Actor.Principal, "Bundle %s/%s", ns, b.Name)
			stamps[b.Name] = *b.Spec.Actor
			for env, s := range w.Steps(ns, b.Name) {
				if s.Status.PRURL == "" {
					continue
				}
				for _, pr := range w.SCM.PRs() {
					if s.Status.PRURL == fmt.Sprintf("https://github.com/%s/pull/%d", pr.Repo, pr.Number) {
						assert.Contains(t, pr.Body, "Created by: agent "+a.name+" for "+reg.sponsor[a.name], "%s/%s %s PR", ns, b.Name, env)
					}
				}
			}
		}
		var events v1alpha1.AuditEventList
		require.NoError(t, w.Client.List(w.Ctx, &events, client.InNamespace(ns)))
		for _, e := range events.Items {
			want, ok := stamps[e.Spec.BundleName]
			if !promotion[e.Spec.Action] || !ok {
				continue
			}
			audited++
			if assert.NotNil(t, e.Spec.Actor, "AuditEvent %s/%s", ns, e.Name) {
				assert.Equal(t, want, *e.Spec.Actor, "AuditEvent %s/%s", ns, e.Name)
			}
		}
	}
	assert.Positive(t, audited)
}

// bound is the index's bound.
func (sz agentSize) bound() int {
	if sz.IndexBytes > 0 {
		return sz.IndexBytes
	}
	return actor.MaxIndexBytes
}

// overflowAtScale pushes the index over its bound (1 MiB in full) with suspended
// AgentIdentities of 16 long subjects each: it turns not Ready (IndexFull)
// and is written empty, every write the actor policy checks is refused,
// people's included, while the controller's are not; deleting them (they
// are suspended, so they may be deleted) restores it.
func overflowAtScale(t *testing.T, w *scenario.World, reg registry, sz agentSize) {
	t.Helper()
	var idx v1alpha1.AgentIndex
	require.NoError(t, w.Client.Get(w.Ctx, types.NamespacedName{Name: scenario.Release}, &idx))
	raw, err := yaml.Marshal(idx.Status)
	require.NoError(t, err)
	per := 16 * 260
	extra := (sz.bound()-len(raw))/per + 2
	admin := scenario.AsUser(w.Ctx, scenario.AdminUser, scenario.AdminGroups...)
	var names []string
	for i := 0; i < extra; i++ {
		var subj []v1alpha1.AgentSubject
		for j := 0; j < 16; j++ {
			subj = append(subj, v1alpha1.AgentSubject{Kind: "User", Name: fmt.Sprintf("oidc:%s-%04d-%02d", strings.Repeat("o", 230), i, j)})
		}
		a := scenario.AgentIdentityOf(fmt.Sprintf("bulk-%04d", i), subj, []string{team(1)}, true)
		w.MustApply(a)
		names = append(names, a.Name)
	}
	start := time.Now()
	require.True(t, w.RunUntil(10, func() bool { return !w.IndexReady() }), "the index stays Ready past its bound")
	require.NoError(t, w.Client.Get(w.Ctx, types.NamespacedName{Name: scenario.Release}, &idx))
	c := meta.FindStatusCondition(idx.Status.Conditions, "Ready")
	require.NotNil(t, c)
	assert.Equal(t, actor.ReasonIndexFull, c.Reason)
	assert.Empty(t, idx.Status.Users)
	t.Logf("overflow: %d AgentIdentities of 16 subjects took the index past %d KiB; not Ready after %s",
		extra, sz.bound()>>10, time.Since(start).Round(time.Millisecond))

	a := reg.active[0]
	b := scenario.ImageBundle(a.ns, a.pipeline, "8.0.0")
	_, refused, err := w.CreateBundle(a.id, b)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(refused, "; "), "kardinal's agent index is not ready yet", "an agent's version while the index is full")
	person := scenario.Identity{Username: "person-00@corp.example.com", Groups: []string{"system:authenticated"}}
	_, refused, err = w.CreateBundle(person, scenario.ImageBundle(a.ns, a.pipeline, "8.0.1"))
	require.NoError(t, err)
	assert.Contains(t, strings.Join(refused, "; "), "kardinal's agent index is not ready yet", "a person's version while the index is full")
	controller := scenario.ImageBundle(a.ns, a.pipeline, "8.0.2")
	controller.Spec.Actor = actor.Controller(scenario.ControllerUser, v1alpha1.ViaAPI)
	assert.NoError(t, w.AdmitCreate(w.Ctx, controller), "the controller's version while the index is full")

	for _, n := range names {
		require.NoError(t, w.Client.Delete(admin, &v1alpha1.AgentIdentity{ObjectMeta: metav1.ObjectMeta{Name: n}}))
	}
	require.True(t, w.RunUntil(10, w.IndexReady), "the index is Ready once the AgentIdentities are gone")
	_, refused, err = w.CreateBundle(a.id, b.DeepCopy())
	require.NoError(t, err)
	assert.Empty(t, refused, "the agent ships again")
}
