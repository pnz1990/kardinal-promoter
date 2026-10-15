// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agents

import (
	"encoding/json"
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
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise"
)

// agentSize is the agent load: Identities AgentIdentities registered, of
// which Agents act, across Teams with PipelinesPerTeam Pipelines, in Bursts
// rounds of versions; Probes requests checked against pkg/actor;
// IndexBytes is the index's bound (0: actor.MaxIndexBytes, 1 MiB).
type agentSize struct{ Teams, PipelinesPerTeam, Identities, Agents, Bursts, Probes, IndexBytes int }

// fullAgents is CI's scenario-enterprise job (KARDINAL_SCENARIO_ENTERPRISE=
// full): 2,000 AgentIdentities, half the index's 4,000-subject bound, and 50
// agents across 20 teams. quickAgents keeps go test ./... fast, with a
// 32 KiB bound to overflow.
var (
	fullAgents  = agentSize{Teams: 20, PipelinesPerTeam: 3, Identities: 2000, Agents: 50, Bursts: 3, Probes: 4000}
	quickAgents = agentSize{Teams: 3, PipelinesPerTeam: 2, Identities: 90, Agents: 6, Bursts: 2, Probes: 300, IndexBytes: 32 << 10}
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
	// The users the probes write as: every subject of every AgentIdentity
	// (agents), users that match two of them (duals: a User subject in
	// another agent's group, or a member of two agent groups), and people
	// and bots that are none.
	agents, duals, people, bots []scenario.Identity
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
	var users []string  // the User subjects so far
	var groups []string // the Group subjects so far
	candidates := map[string][]activeAgent{}
	created := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for i := 0; i < sz.Identities; i++ {
		name := fmt.Sprintf("agent-%04d", i)
		ns := enterprise.Team(1 + i%sz.Teams)
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
			scope.Pipelines = []string{enterprise.PipelineName(1)}
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
			reg.agents = append(reg.agents, id)
			if s.Kind == "Group" {
				groups = append(groups, s.Name)
			}
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
		ns := enterprise.Team(1 + k%sz.Teams)
		if k/sz.Teams < len(candidates[ns]) {
			a := candidates[ns][k/sz.Teams]
			a.pipeline = enterprise.PipelineName(1 + k/sz.Teams%sz.PipelinesPerTeam)
			reg.active = append(reg.active, a)
		}
	}
	for i := 0; i < 100 && len(users) > 0 && len(groups) > 1; i++ {
		g1, g2 := groups[rng.IntN(len(groups))], groups[rng.IntN(len(groups))]
		reg.duals = append(reg.duals,
			scenario.Identity{Username: users[rng.IntN(len(users))], Groups: []string{g1, "system:authenticated"}},
			scenario.Identity{Username: fmt.Sprintf("oidc:two-groups-%03d", i), Groups: []string{g1, g2, "system:authenticated"}})
	}
	for i := 0; i < 400; i++ {
		reg.people = append(reg.people, scenario.Identity{Username: fmt.Sprintf("person-%03d@corp.example.com", i),
			Groups: []string{fmt.Sprintf("team-%02d-dev", 1+i%sz.Teams), "system:authenticated"}})
		ns := enterprise.Team(1 + i%sz.Teams)
		reg.bots = append(reg.bots, scenario.Identity{Username: fmt.Sprintf("system:serviceaccount:%s:bot-%03d", ns, i),
			Groups: scenario.ServiceAccountGroups(ns)})
	}
	return reg
}

// TestEnterprise_AgentLoad is the enterprise of the load test with agents:
// Identities AgentIdentities registered (2,000 in full: half the index's
// bound), Agents of them shipping versions in bursts across every team, a
// release manager approving prod, every write admitted by the API server's
// ValidatingAdmissionPolicy plugin with the chart's policies and the
// controller's AgentIndex as the actor policy's param. Probes requests
// (Bundles, Approvals, gate instances and their overrides) by every kind of
// principal (each AgentIdentity's subjects, users of two agents, people,
// bots, the controller, kro), with the CLI's stamp or a forged one, in and
// out of scope, get the verdict pkg/actor (Derive, Scope) gives: the policy
// is its transliteration, at scale, no refusal is an expression error, and
// its cost grows at most linearly with the index. A suspension refuses the agent's next
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
	w.MustApply(enterprise.OrgGates()...)
	for i := 1; i <= sz.Teams; i++ {
		enterprise.Tenant(t, w, enterprise.Team(i), sz.PipelinesPerTeam, i%2 == 0)
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
	probeAgainstGo(t, reg, &idx, sz)

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
			other := enterprise.Team(1 + (teamIndex(a.ns))%sz.Teams)
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
	// A version whose agent was suspended after it shipped is held by its
	// kardinal-agents-* gates (agent.* is the registry now,
	// docs/design/18-actor-policy.md section 2.2); every other newest
	// version is Verified.
	heldBySuspension := func(ns, b string) bool {
		a, ok := shipper[ns+"/"+b]
		return ok && suspended[a.name]
	}
	done := func() bool {
		for key, b := range newest {
			ns := strings.SplitN(key, "/", 2)[0]
			if !heldBySuspension(ns, b) && w.Bundle(ns, b).Status.Phase != "Verified" {
				return false
			}
		}
		return true
	}
	if !w.RunUntil(300, done) {
		var stuck []string
		for key, b := range newest {
			ns := strings.SplitN(key, "/", 2)[0]
			if ph := w.Bundle(ns, b).Status.Phase; ph != "Verified" && !heldBySuspension(ns, b) {
				stuck = append(stuck, key+"="+b+":"+ph)
			}
		}
		sort.Strings(stuck)
		t.Fatalf("after %d rounds, %d newest versions are not Verified: %v\n%s", w.Rounds(), len(stuck), stuck[:min(len(stuck), 10)],
			w.Describe(strings.SplitN(stuck[0], "/", 2)[0]))
	}
	held := 0
	for key, b := range newest {
		ns := strings.SplitN(key, "/", 2)[0]
		if heldBySuspension(ns, b) {
			held++
			assert.NotEqual(t, "Verified", w.Bundle(ns, b).Status.Phase,
				"%s: a suspended agent's in-flight version is held by its agents gate", key)
		}
	}
	t.Logf("%d newest versions held: their agent was suspended after they shipped", held)
	// The on-call rejects the held versions, so the load settles for the
	// quiet-period check (a held version keeps rechecking its gates).
	for key, b := range newest {
		ns := strings.SplitN(key, "/", 2)[0]
		if heldBySuspension(ns, b) {
			_, err := lifecycle.Reject(scenario.AsUser(w.Ctx, "oncall@example.com"), w.Client, ns, b,
				"its agent was suspended", "oncall@example.com", w.Now())
			require.NoError(t, err)
		}
	}
	w.RunUntil(50, func() bool {
		for key, b := range newest {
			ns := strings.SplitN(key, "/", 2)[0]
			if heldBySuspension(ns, b) && w.Bundle(ns, b).Status.Phase == "Promoting" {
				return false
			}
		}
		return true
	})
	t.Logf("%d agents, %d versions over %d bursts (%d agents suspended after the first): %d rounds, %s, reconciles %v",
		sz.Agents, len(shipper), sz.Bursts, len(suspended), w.Rounds(), time.Since(start).Round(time.Second), w.Reconciles())
	checkAgentStamps(t, w, sz, reg, shipper)
	enterprise.CheckIsolation(t, w, enterprise.Size{Teams: sz.Teams, PipelinesPerTeam: sz.PipelinesPerTeam})
	// A version lives through the bursts after it: the bound is per burst.
	enterprise.CheckBounded(t, w, enterprise.MaxPerObject*sz.Bursts)

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
	rm := scenario.Identity{Username: enterprise.ReleaseManager, Groups: []string{enterprise.ReleaseManagers, "system:authenticated"}}
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
			Environment: env, User: rm.Username, Groups: []string{enterprise.ReleaseManagers}, Decision: lifecycle.DecisionApprove, Actor: stamp})
		if err != nil && !strings.Contains(err.Error(), "never promotes again") && !strings.Contains(err.Error(), "already") {
			return fmt.Errorf("approve %s/%s: %w", g.Namespace, b, err)
		}
	}
	return nil
}

// probe is one creation or update the differential sends.
type probe struct {
	u        user.Info
	gvr      string
	obj, old map[string]interface{}
	where    actor.Where
	stamps   []*v1alpha1.Actor
	desc     string
}

// probes generates n requests, deterministically: who writes is an agent's
// subject (35%), a user matching two agents (10%), a person (20%), a bot
// (15%), the controller (10%) or kro's Graph ServiceAccount (10%); what is
// a Bundle or an Approval created, a gate instance created (as kro does) or
// a new override on one (as kardinal override does); the stamp is the CLI's,
// none, a forged sponsor or a human's.
func probes(reg registry, idx *v1alpha1.AgentIndexStatus, sz agentSize, n int) ([]probe, error) {
	rng := rand.New(rand.NewPCG(1867, 1837))
	defaults := scenario.NewAdmission(scenario.RepoRoot())
	toMap := func(o client.Object, kind string) (map[string]interface{}, error) {
		m, err := k8sruntime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			return nil, err
		}
		m["apiVersion"], m["kind"] = "kardinal.io/v1alpha1", kind
		delete(m, "status")
		return m, defaults.Default(kind, m)
	}
	pick := func(l []scenario.Identity) scenario.Identity { return l[rng.IntN(len(l))] }
	out := make([]probe, 0, n)
	for i := 0; i < n; i++ {
		var u user.Info
		var id scenario.Identity
		ns := enterprise.Team(1 + rng.IntN(sz.Teams))
		switch c := rng.IntN(100); {
		case c < 35:
			id = pick(reg.agents)
		case c < 45:
			id = pick(reg.duals)
		case c < 65:
			id = pick(reg.people)
		case c < 80:
			id = pick(reg.bots)
		case c < 90:
			u = scenario.ControllerInfo()
		default:
			u = scenario.GraphInfo(ns)
		}
		if u == nil {
			u = &user.DefaultInfo{Name: id.Username, Groups: id.Groups}
			// Half the time a ServiceAccount writes in its own namespace,
			// where its agent is usually scoped.
			if own, ok := strings.CutPrefix(id.Username, "system:serviceaccount:"); ok && rng.IntN(2) == 0 {
				ns, _, _ = strings.Cut(own, ":")
			}
		}
		r, derr := actor.Derive(actor.User{Username: u.GetName(), Groups: u.GetGroups()}, idx)
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
			stamp = &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: u.GetName()}
		default: // what the CLI stamps; for a user of two agents, the first
			if derr == nil {
				s := r.Actor
				stamp = &s
			} else if e, ok := firstMatch(idx, u); ok {
				stamp = &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: u.GetName(), Agent: e.Agent, Sponsor: e.Sponsor}
			}
		}
		pl := enterprise.PipelineName(1 + rng.IntN(sz.PipelinesPerTeam))
		env := []string{"dev", "staging", "prod"}[rng.IntN(3)]
		pr := probe{u: u, where: actor.Where{Namespace: ns}}
		var err error
		switch k := rng.IntN(4); k {
		case 0:
			pr.gvr, pr.where.Environment, pr.stamps = "approvals", env, []*v1alpha1.Actor{stamp}
			name := fmt.Sprintf("probe-%d", i)
			pr.obj, err = toMap(&v1alpha1.Approval{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name,
				Labels: map[string]string{"kardinal.io/bundle": name, "kardinal.io/environment": env}},
				Spec: v1alpha1.ApprovalSpec{Bundle: name, BundleUID: "u", Environment: env, User: u.GetName(),
					Decision: lifecycle.DecisionApprove, Actor: stamp}}, "Approval")
		case 1:
			pr.gvr, pr.where.Pipeline, pr.stamps = "bundles", pl, []*v1alpha1.Actor{stamp}
			b := scenario.ImageBundle(ns, pl, fmt.Sprintf("0.0.%d", i))
			b.Spec.Actor = stamp
			pr.obj, err = toMap(b, "Bundle")
		default:
			// A gate instance: created with no override (kro), or updated
			// with a new one (kardinal override), stamped.
			pr.gvr, pr.where.Pipeline, pr.where.Environment = "policygates", pl, env
			g := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: fmt.Sprintf("gate-%d", i),
				Labels: map[string]string{"kardinal.io/bundle": pl + "-1-0-0", "kardinal.io/pipeline": pl, "kardinal.io/environment": env}},
				Spec: v1alpha1.PolicyGateSpec{Expression: "true"}}
			if k == 3 {
				if pr.old, err = toMap(g, "PolicyGate"); err != nil {
					return nil, err
				}
				g.Spec.Overrides = []v1alpha1.PolicyGateOverride{{Reason: "probe", ExpiresAt: metav1.NewTime(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
					CreatedBy: u.GetName(), Actor: stamp}}
				pr.stamps = []*v1alpha1.Actor{stamp}
			}
			pr.obj, err = toMap(g, "PolicyGate")
		}
		if err != nil {
			return nil, err
		}
		pr.desc = fmt.Sprintf("%s (groups %v) writes %s in %+v stamped %+v", u.GetName(), u.GetGroups(), pr.gvr, pr.where, stamp)
		out = append(out, pr)
	}
	return out, nil
}

// firstMatch is the first index entry u matches, by username then groups:
// the agent a user of two agents would claim to be.
func firstMatch(idx *v1alpha1.AgentIndexStatus, u user.Info) (v1alpha1.AgentIndexEntry, bool) {
	if e, ok := idx.Users[u.GetName()]; ok {
		return e, true
	}
	for _, g := range u.GetGroups() {
		if e, ok := idx.Groups[g]; ok {
			return e, true
		}
	}
	return v1alpha1.AgentIndexEntry{}, false
}

// actorVerdict is the actor policy's decision as docs/actors.md states it,
// through pkg/actor: the controller is not judged, nor is a write that adds
// no stamp (a gate instance without a new override); otherwise the
// requester's actor (Derive) must be able to act there (Scope) and every
// stamp says exactly that actor (sameStamp).
func actorVerdict(pr probe, idx *v1alpha1.AgentIndexStatus) bool {
	if pr.u.GetName() == scenario.ControllerUser || len(pr.stamps) == 0 {
		return true
	}
	r, err := actor.Derive(actor.User{Username: pr.u.GetName(), Groups: pr.u.GetGroups()}, idx)
	if err != nil || actor.Scope(r.Entry, pr.where) != nil {
		return false
	}
	for _, s := range pr.stamps {
		if !sameStamp(s, r, pr.u.GetName()) {
			return false
		}
	}
	return true
}

// probeAgainstGo sends sz.Probes requests (probes) through the API
// server's admission plugin serving the actor policy alone, with the index
// as its param, and checks each verdict against actorVerdict, and that
// every refusal is one of the policy's messages. Then it sends them again
// with a tenth of the index: the admission cost may grow at most linearly
// with the index (#1936 makes it linear today).
func probeAgainstGo(t *testing.T, reg registry, idx *v1alpha1.AgentIndex, sz agentSize) {
	t.Helper()
	c, err := scenario.RenderChart()
	require.NoError(t, err)
	api, stop, err := scenario.NewAPIServer(scenario.PolicyObjects(nil, c, []string{scenario.Release + "-actor"}, nil), nil)
	require.NoError(t, err)
	t.Cleanup(stop)
	for i := 1; i <= sz.Teams; i++ {
		require.NoError(t, api.EnsureNamespace(enterprise.Team(i)))
	}
	require.NoError(t, api.EnsureNamespace("ci"))
	prs, err := probes(reg, &idx.Status, sz, sz.Probes)
	require.NoError(t, err)
	send := func(pr probe) (time.Duration, error) {
		op := admission.Create
		if pr.old != nil {
			op = admission.Update
		}
		t0 := time.Now()
		err := api.Admit(op, scenario.GVR(pr.gvr), pr.obj, pr.old, pr.u)
		return time.Since(t0), err
	}
	require.NoError(t, api.SetAgentIndex(idx))
	verdicts := map[string]int{}
	mismatches := 0
	var full []time.Duration
	for i, pr := range prs {
		d, err := send(pr)
		full = append(full, d)
		want := actorVerdict(pr, &idx.Status)
		kind := "controller"
		if pr.u.GetName() != scenario.ControllerUser {
			r, _ := actor.Derive(actor.User{Username: pr.u.GetName(), Groups: pr.u.GetGroups()}, &idx.Status)
			kind = r.Actor.Kind
			if kind == "" {
				kind = "conflict-or-blocked"
			}
		}
		verdicts[fmt.Sprintf("%s/%s/admit=%v", kind, pr.gvr, want)]++
		if (err == nil) != want {
			mismatches++
			if mismatches <= 10 {
				t.Errorf("probe %d: %s: pkg/actor says admit=%v, the policy %v", i, pr.desc, want, err)
			}
		}
		if err != nil {
			assert.Contains(t, err.Error(), "ValidatingAdmissionPolicy '"+scenario.Release+"-actor'", "probe %d", i)
			assert.NotContains(t, err.Error(), "resulted in error", "probe %d: a refusal names its reason", i)
		}
	}
	assert.Zero(t, mismatches, "verdicts that differ from pkg/actor")
	keys := make([]string, 0, len(verdicts))
	for k := range verdicts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("  %-48s %d", k, verdicts[k])
	}

	// The same requests with a tenth of the index.
	tenth := idx.DeepCopy()
	tenth.Status.Users, tenth.Status.Groups = subset(idx.Status.Users, 10), subset(idx.Status.Groups, 10)
	require.NoError(t, api.SetAgentIndex(tenth))
	var small []time.Duration
	for _, pr := range prs[:min(len(prs), 1000)] {
		d, _ := send(pr)
		small = append(small, d)
	}
	fullBytes, smallBytes := jsonSize(t, idx.Status), jsonSize(t, tenth.Status)
	p50f, p50s := median(full[:len(small)]), median(small)
	sort.Slice(full, func(i, j int) bool { return full[i] < full[j] })
	t.Logf("admission over the index (%d KiB): p50 %s, p99 %s, max %s; over a tenth of it (%d KiB): p50 %s",
		fullBytes>>10, median(full), full[len(full)*99/100], full[len(full)-1], smallBytes>>10, p50s)
	// Measured in the same run, so a slow runner slows both: the ratio of
	// the medians stays under twice the ratio of the sizes.
	assert.Less(t, float64(p50f)/float64(p50s), 2*float64(fullBytes)/float64(smallBytes),
		"admission cost grows faster than linearly with the index (p50 %s at %d KiB, %s at %d KiB)", p50f, fullBytes>>10, p50s, smallBytes>>10)
}

// subset keeps every k-th entry of m, by key order.
func subset(m map[string]v1alpha1.AgentIndexEntry, k int) map[string]v1alpha1.AgentIndexEntry {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := map[string]v1alpha1.AgentIndexEntry{}
	for i, key := range keys {
		if i%k == 0 {
			out[key] = m[key]
		}
	}
	return out
}

func median(d []time.Duration) time.Duration {
	c := append([]time.Duration(nil), d...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return c[len(c)/2]
}

func jsonSize(t *testing.T, v interface{}) int {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return len(b)
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
		ns := enterprise.Team(i)
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
		a := scenario.AgentIdentityOf(fmt.Sprintf("bulk-%04d", i), subj, []string{enterprise.Team(1)}, true)
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

func TestMain(m *testing.M) { scenario.MainWithRenderJobs(m) }
