// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package daycases

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"

	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

// Step is one thing that happens in an episode: Do (when Pre holds; by
// default, when people can act), then Wait until it holds, for at most Max
// rounds.
type Step struct {
	Name string
	Pre  func() bool
	Do   func()
	Wait func() bool
	Max  int
}

// Episode is a part of the day, self-contained: it owns the Pipelines in
// Uses while it runs and leaves them with nothing in flight.
type Episode struct {
	Name  string
	Uses  []string
	Steps []Step

	i, waited int
	acted     bool
}

func act(name string, do func()) Step { return Step{Name: name, Do: do} }

// check is a step that only looks, once people can act: what it expects
// may need the cluster to have caught up with an outage.
func check(name string, do func()) Step { return Step{Name: name, Do: do} }

// during is a step that runs whatever happens around it: the outage's own.
func during(name string, do func()) Step {
	return Step{Name: name, Do: do, Pre: func() bool { return true }}
}

func until(name string, cond func() bool) Step { return Step{Name: name, Wait: cond} }

// rounds is a step that lets n rounds go by.
func rounds(name string, n int) Step {
	c := 0
	return Step{Name: name, Wait: func() bool { c++; return c > n }}
}

// Run runs the episodes: in their order, one at a time, or (interleave)
// in a seeded random order, up to three at once on Pipelines they do not
// share, each step taken at a random round. The invariants are checked
// after every step, every round (observe) and whenever no episode runs.
func (d *Day) Run(eps []*Episode, interleave bool) {
	t := d.t
	t.Helper()
	queue := append([]*Episode(nil), eps...)
	if interleave {
		d.rng.Shuffle(len(queue), func(i, j int) { queue[i], queue[j] = queue[j], queue[i] })
	}
	busy := map[string]bool{}
	var active []*Episode
	free := func(e *Episode) bool {
		for _, u := range e.Uses {
			if busy[u] {
				return false
			}
		}
		return true
	}
	for len(queue)+len(active) > 0 {
		if len(active) == 0 || (interleave && len(active) < 3 && d.rng.Intn(3) == 0) {
			for i, e := range queue {
				if !free(e) {
					continue
				}
				for _, u := range e.Uses {
					busy[u] = true
				}
				t.Logf("round %d: %s starts", d.Rounds(), e.Name)
				active = append(active, e)
				queue = append(queue[:i], queue[i+1:]...)
				break
			}
		}
		order := active
		if interleave {
			order = append([]*Episode(nil), active...)
			d.rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		}
		for _, e := range order {
			d.advance(e, interleave)
		}
		left := active[:0]
		for _, e := range active {
			if e.i < len(e.Steps) {
				left = append(left, e)
				continue
			}
			for _, u := range e.Uses {
				delete(busy, u)
			}
			t.Logf("round %d: %s done", d.Rounds(), e.Name)
		}
		active = left
		if len(active) == 0 {
			d.Converge()
			d.CheckInvariants()
			continue
		}
		d.Round()
	}
	d.CheckEvidence()
	d.Summary()
}

// advance takes e's next steps that can be taken now: one at most when
// interleaving (and not always), all that are ready otherwise.
func (d *Day) advance(e *Episode, interleave bool) {
	t := d.t
	t.Helper()
	if interleave && d.rng.Intn(4) == 0 {
		return
	}
	for e.i < len(e.Steps) {
		s := e.Steps[e.i]
		max := s.Max
		if max == 0 {
			max = 3 * scenario.MaxRounds
		}
		if !e.acted && s.Do != nil {
			pre := s.Pre
			if pre == nil {
				pre = func() bool { return !d.outage }
			}
			if !pre() {
				e.waited++
				require.LessOrEqual(t, e.waited, max, "%s: %s could not start", e.Name, s.Name)
				return
			}
			s.Do()
			d.CheckInvariants()
		}
		e.acted = true
		if s.Wait != nil && !s.Wait() {
			e.waited++
			require.LessOrEqual(t, e.waited, max, "%s: %s did not happen within %d rounds", e.Name, s.Name, max)
			return
		}
		e.i, e.acted, e.waited = e.i+1, false, 0
		if interleave && d.rng.Intn(2) == 0 {
			return
		}
	}
}

// Parts is how many parts the day runs in, each on a World of its own with
// all the tenants, so that a package stays well under the scenario duration
// bound. The whole day on one World runs in full mode (Whole).
const Parts = 5

// Episodes are part (1-based) of the day. 1: agents ship while people
// approve, and agent autonomy. 2: verification passes, fails and times out
// (the clock jumps), and the agent index outage, which refuses agents'
// Verdicts. 3: a pause, and a canary rollback on the fleet. 4: an
// agents-only freeze, and a hold. 5: stop-the-line with a capped exemption.
// PRs are reviewed and merged all day.
func (d *Day) Episodes(part int) []*Episode {
	switch part {
	case 1:
		return []*Episode{d.oneParty(), d.autonomy()}
	case 2:
		return []*Episode{d.verify(), d.outageEp()}
	case 3:
		return []*Episode{d.pause(), d.rollback()}
	case 4:
		return []*Episode{d.freezeAgents(), d.hold()}
	case 5:
		return []*Episode{d.stopTheLine()}
	}
	d.t.Fatalf("no part %d of the day", part)
	return nil
}

func key(ns, p string) string { return ns + "/" + p }

func (d *Day) stamp(id scenario.Identity, ns, pipeline, env string) *v1alpha1.Actor {
	d.t.Helper()
	s, err := d.StampFor(id, actor.Where{Namespace: ns, Pipeline: pipeline, Environment: env})
	require.NoError(d.t, err, "%s's stamp", id.Username)
	return s
}

func (d *Day) change(id scenario.Identity, ns, pipeline, env, reason string) *v1alpha1.PipelineChange {
	return &v1alpha1.PipelineChange{By: id.Username, Reason: reason, Actor: d.stamp(id, ns, pipeline, env)}
}

func (d *Day) reject(id scenario.Identity, ns string, b *v1alpha1.Bundle, why string) {
	d.t.Helper()
	_, err := lifecycle.Reject(d.CtxOf(id), d.Client, ns, b.Name, why, id.Username, d.stamp(id, ns, b.Spec.Pipeline, ""), d.Now())
	require.NoError(d.t, err, "%s rejects %s", id.Username, b.Name)
}

func (d *Day) freeze(id scenario.Identity, actors, ns string) *freezeRec {
	d.t.Helper()
	from := d.Now()
	cw, _, err := lifecycle.Freeze(d.CtxOf(id), d.Client, lifecycle.FreezeRequest{Actors: actors, Namespace: ns, Reason: "day: incident",
		CreatedBy: id.Username, Stamp: d.stamp(id, ns, "", ""), Now: d.Now()})
	require.NoError(d.t, err, "%s freezes %s in %q", id.Username, actors, ns)
	f := &freezeRec{name: cw.Name, ns: ns, actors: actors, from: from, exempt: map[string]bool{}}
	d.freezes = append(d.freezes, f)
	return f
}

func (d *Day) unfreeze(id scenario.Identity, f *freezeRec) {
	d.t.Helper()
	require.NoError(d.t, lifecycle.Unfreeze(d.CtxOf(id), d.Client, f.name, "", &v1alpha1.FreezeLift{By: id.Username,
		Actor: d.stamp(id, f.ns, "", "")}), "%s lifts %s", id.Username, f.name)
	f.to = d.Now()
}

// endPause records the end of the open pause of ns/pipeline.
func (d *Day) endPause(ns, pipeline string) {
	for _, p := range d.pauses {
		if p.ns == ns && p.pipeline == pipeline && p.to.IsZero() {
			p.to = d.Now()
		}
	}
}

func (d *Day) events(ns, action string) []v1alpha1.AuditEvent {
	var events v1alpha1.AuditEventList
	require.NoError(d.t, d.Client.List(d.Ctx, &events, client.InNamespace(ns)))
	var out []v1alpha1.AuditEvent
	for _, e := range events.Items {
		if e.Spec.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func actorOf(e v1alpha1.AuditEvent) string {
	if e.Spec.Actor == nil {
		return ""
	}
	return e.Spec.Actor.Principal
}

// oneParty: an agent ships to prod; its sponsor's approval is recorded and
// not counted, and the version waits for a release manager's.
func (d *Day) oneParty() *Episode {
	ns, p := TeamA, scenario.PipelineName
	var b *v1alpha1.Bundle
	return &Episode{Name: "agents ship, people approve", Uses: []string{key(ns, p)}, Steps: []Step{
		act("fixer ships", func() { b = d.ship(fixer, ns, p, "1.1.0") }),
		until("the release gate waits", func() bool { return d.gate(ns, b.Name, releaseGate, "prod") != nil }),
		act("the sponsor approves", func() { require.NoError(d.t, d.approveAs(bob, ns, b.Name, "prod")) }),
		until("the gate records it", func() bool {
			found, _, _ := counted(*d.gate(ns, b.Name, releaseGate, "prod"), bob.Username)
			return found
		}),
		rounds("held", scenario.BlockRounds),
		check("the sponsor's approval does not count", func() {
			found, ok, why := counted(*d.gate(ns, b.Name, releaseGate, "prod"), bob.Username)
			assert.True(d.t, found && !ok, "bob's approval of his agent's version is recorded, not counted")
			assert.NotEmpty(d.t, why)
			assert.False(d.t, d.verifiedIn(ns, b.Name, "prod"), "the agent's version reached prod on its sponsor's approval")
		}),
		act("a release manager approves", func() { d.approve[b.Name] = true }),
		until("Verified", func() bool { return d.phase(ns, b.Name) == "Verified" }),
	}}
}

// autonomy: an agent of autonomy approve waits for a person (not its
// sponsor) in every environment; one of autonomy deny never promotes; a
// suspended agent and forged stamps are refused.
func (d *Day) autonomy() *Episode {
	ns, p := TeamA, Web
	var c, x *v1alpha1.Bundle
	return &Episode{Name: "agent autonomy", Uses: []string{key(ns, p)}, Steps: []Step{
		act("careful ships", func() { c = d.ship(careful, ns, p, "2.1.0") }),
		until("its agent gate waits in dev", func() bool { return d.agentGate(ns, c.Name, "dev") != nil }),
		act("the sponsor approves", func() { require.NoError(d.t, d.approveAs(bob, ns, c.Name, "dev")) }),
		until("the gate records it", func() bool {
			found, _, _ := counted(*d.agentGate(ns, c.Name, "dev"), bob.Username)
			return found
		}),
		rounds("held", scenario.BlockRounds),
		check("the sponsor's approval does not count", func() {
			found, ok, _ := counted(*d.agentGate(ns, c.Name, "dev"), bob.Username)
			assert.True(d.t, found && !ok, "bob's approval of careful's version is recorded, not counted")
			assert.Empty(d.t, d.Verified(ns, c.Name), "careful's version promoted on its sponsor's approval")
		}),
		act("a release manager approves", func() { d.approve[c.Name] = true }),
		until("Verified", func() bool { return d.phase(ns, c.Name) == "Verified" }),
		act("banned ships", func() { x = d.ship(banned, ns, p, "2.2.0") }),
		rounds("held", 2*scenario.BlockRounds),
		check("deny never promotes", func() {
			assert.Empty(d.t, d.Verified(ns, x.Name), "an agent of autonomy deny promoted")
			assert.NotNil(d.t, d.agentGate(ns, x.Name, "dev"), "the deny gate holds it")
		}),
		act("refused writes", func() {
			b := scenario.ImageBundle(ns, p, "2.9.0")
			cliErr, _, err := d.CreateBundle(parked, b.DeepCopy())
			require.NoError(d.t, err)
			assert.ErrorIs(d.t, cliErr, actor.ErrBlocked, "the CLI refuses the suspended agent")
			for _, tc := range []struct {
				id    scenario.Identity
				stamp v1alpha1.Actor
				why   string
			}{
				{parked, v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: parked.Username, Agent: "parked", Sponsor: scenario.Sponsor}, "suspended"},
				{careful, v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: careful.Username, Agent: "fixer", Sponsor: scenario.Sponsor}, "actor must be"},
				{careful, v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: careful.Username}, "actor must be"},
				{bot, v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: forged}, "actor must be"},
			} {
				f := b.DeepCopy()
				f.Annotations = map[string]string{"kardinal.io/created-by": tc.id.Username}
				f.Spec.Actor = &tc.stamp
				reasons, err := d.ApplyAs(d.CtxOf(tc.id), f)
				require.NoError(d.t, err)
				assert.Contains(d.t, strings.Join(reasons, "; "), tc.why, "%s stamping %+v", tc.id.Username, tc.stamp)
			}
			assert.Nil(d.t, d.bundle(ns, b.Name), "a refused version exists")
		}),
		act("a release manager rejects it", func() { d.reject(rm, ns, x, "agents may not ship web") }),
		until("Rejected", func() bool { return d.phase(ns, x.Name) == "Rejected" }),
	}}
}

// agentGate is bundle's agent-autonomy gate in env, or nil.
func (d *Day) agentGate(ns, bundle, env string) *v1alpha1.PolicyGate {
	for _, g := range d.GateInstances(ns, bundle) {
		if strings.HasPrefix(g.Labels["kardinal.io/gate-name"], graph.AgentGatePrefix) && g.Labels["kardinal.io/environment"] == env {
			return &g
		}
	}
	return nil
}

// verify: QAAgent's run fails a version, then never finishes on the next
// (the gate times out), and passes the third.
func (d *Day) verify() *Episode {
	ns, p := TeamA, scenario.PipelineName
	gate := d.plans[ns].Gate.Name
	var v, u, z *v1alpha1.Bundle
	waits := func(b **v1alpha1.Bundle) func() bool {
		return func() bool {
			return d.gate(ns, (*b).Name, gate, "prod") != nil && d.verifiedIn(ns, (*b).Name, "staging")
		}
	}
	blocked := func(b **v1alpha1.Bundle, why string) func() {
		return func() {
			assert.False(d.t, d.verifiedIn(ns, (*b).Name, "prod"), "a version reached prod: %s", why)
			g := d.gate(ns, (*b).Name, gate, "prod")
			assert.False(d.t, g.Status.Ready, "the verify gate opened: %s", why)
			d.t.Logf("%s: verify gate: %s", why, g.Status.Reason)
		}
	}
	return &Episode{Name: "verify gate", Uses: []string{key(ns, p)}, Steps: []Step{
		act("dev ships; the agent finds a bug", func() {
			v = scenario.ImageBundle(ns, p, "1.2.0")
			d.verdicts[v.Name] = scenario.AgentResult{Result: v1alpha1.VerdictFail, Summary: "checkout broken"}
			v = d.ship(dev, ns, p, "1.2.0")
			d.approve[v.Name] = true
		}),
		until("verified in staging", waits(&v)),
		rounds("held", scenario.BlockRounds),
		check("a failing verdict holds prod", blocked(&v, "fail")),
		act("rejected", func() { d.reject(rm, ns, v, "QA found a bug") }),
		until("Rejected", func() bool { return d.phase(ns, v.Name) == "Rejected" }),
		act("a bot ships; the agent never finishes", func() {
			u = scenario.ImageBundle(ns, p, "1.3.0")
			d.verdicts[u.Name] = scenario.AgentResult{Wait: true}
			u = d.ship(bot, ns, p, "1.3.0")
			d.approve[u.Name] = true
		}),
		until("verified in staging", waits(&u)),
		check("the verification times out", func() { d.Advance(verifyTimeout + time.Minute) }),
		rounds("held", scenario.BlockRounds),
		check("a timed-out verification holds prod (onTimeout: fail)", blocked(&u, "timeout")),
		act("rejected", func() { d.reject(rm, ns, u, "not verified in time") }),
		until("Rejected", func() bool { return d.phase(ns, u.Name) == "Rejected" }),
		act("fixer ships; the agent passes it", func() {
			z = d.ship(fixer, ns, p, "1.4.0")
			d.approve[z.Name] = true
		}),
		until("Verified", func() bool { return d.phase(ns, z.Name) == "Verified" }),
	}}
}

// freezeAgents: an agents-only freeze of every namespace holds an agent's
// version and lets a person's through, until it is lifted.
func (d *Day) freezeAgents() *Episode {
	var f *freezeRec
	var a, h *v1alpha1.Bundle
	return &Episode{Name: "agents-only freeze", Uses: []string{key(TeamC, Web), key(TeamB, scenario.PipelineName)}, Steps: []Step{
		act("the incident commander freezes agents", func() { f = d.freeze(ic, v1alpha1.FreezeAgents, "") }),
		act("fixer and a person ship", func() {
			a = d.ship(fixer, TeamC, Web, "2.1.0")
			h = d.ship(dev, TeamB, scenario.PipelineName, "1.5.0")
		}),
		until("the person's version is Verified", func() bool { return d.phase(TeamB, h.Name) == "Verified" }),
		rounds("held", scenario.BlockRounds),
		check("the agent's version is held, and says why", func() {
			assert.Empty(d.t, d.Verified(TeamC, a.Name), "an agent's version promoted through an agents-only freeze")
			s, ok := d.Steps(TeamC, a.Name)["dev"]
			if assert.True(d.t, ok, "the agent's version has a dev step") {
				assert.Contains(d.t, s.Status.Message, f.name, "the held step names the freeze")
			}
		}),
		act("lifted", func() { d.unfreeze(ic, f) }),
		until("the agent's version is Verified", func() bool { return d.phase(TeamC, a.Name) == "Verified" }),
	}}
}

// stopTheLine: everyone is frozen in team-c; a release manager exempts one
// Pipeline for 48 hours, which the controller caps at 24, and only it goes
// on.
func (d *Day) stopTheLine() *Episode {
	ns := TeamC
	var f *freezeRec
	var web, app *v1alpha1.Bundle
	return &Episode{Name: "stop-the-line", Uses: []string{key(ns, Web), key(ns, scenario.PipelineName)}, Steps: []Step{
		act("the incident commander stops the line", func() { f = d.freeze(ic, v1alpha1.FreezeEveryone, ns) }),
		act("a bot and a person ship", func() {
			web = d.ship(bot, ns, Web, "2.2.0")
			app = d.ship(dev, ns, scenario.PipelineName, "1.1.0")
		}),
		rounds("held", scenario.BlockRounds),
		check("nothing promotes", func() {
			assert.Empty(d.t, d.Verified(ns, web.Name))
			assert.Empty(d.t, d.Verified(ns, app.Name))
		}),
		act("a release manager exempts web for 48h", func() {
			_, err := lifecycle.ExemptFromFreezes(d.CtxOf(rm), d.Client, lifecycle.ExemptRequest{Namespace: ns, Pipeline: Web,
				Reason: "hotfix", ExpiresIn: 48 * time.Hour, CreatedBy: rm.Username, Actor: d.stamp(rm, ns, Web, ""), Now: d.Now()})
			require.NoError(d.t, err)
			f.exempt[key(ns, Web)] = true
		}),
		until("web is Verified", func() bool { return d.phase(ns, web.Name) == "Verified" }),
		check("only web went on, capped, and each step says who let it", func() {
			assert.Empty(d.t, d.Verified(ns, app.Name), "the exemption let another Pipeline through")
			n := 0
			for _, e := range d.events(ns, "FreezeExempted") {
				if e.Spec.BundleName == web.Name {
					n++
					assert.Contains(d.t, e.Spec.Message, rm.Username)
				}
			}
			assert.Positive(d.t, n, "a FreezeExempted AuditEvent per step")
			var cw v1alpha1.ChangeWindow
			require.NoError(d.t, d.Client.Get(d.Ctx, client.ObjectKey{Name: f.name}, &cw))
			require.NotEmpty(d.t, cw.Status.ExemptionRecords, "the exemption's record")
			for _, r := range cw.Status.ExemptionRecords {
				assert.LessOrEqual(d.t, r.Until.Sub(r.FirstSeen.Time), 24*time.Hour, "an exemption of 48h counts for at most 24h")
			}
		}),
		act("lifted", func() { d.unfreeze(ic, f) }),
		until("app is Verified", func() bool { return d.phase(ns, app.Name) == "Verified" }),
	}}
}

// pause: a paused Pipeline starts nothing; the group-registered agent's
// resume (if admitted) is recorded without its stamp; it then ships itself.
func (d *Day) pause() *Episode {
	ns, p := TeamB, scenario.PipelineName
	var x, y *v1alpha1.Bundle
	resumed := false
	return &Episode{Name: "pause and resume", Uses: []string{key(ns, p)}, Steps: []Step{
		act("paused", func() {
			require.NoError(d.t, lifecycle.Pause(d.CtxOf(dev), d.Client, ns, p, d.change(dev, ns, p, "", "day: investigating")))
			d.pauses = append(d.pauses, &pauseRec{ns: ns, pipeline: p, from: d.Now()})
		}),
		act("a person ships", func() { x = d.ship(dev, ns, p, "1.6.0") }),
		rounds("held", scenario.BlockRounds),
		check("nothing promotes", func() { assert.Empty(d.t, d.Verified(ns, x.Name), "a paused Pipeline promoted") }),
		act("triage resumes it", func() {
			err := lifecycle.Resume(d.CtxOf(triage), d.Client, ns, p, d.change(triage, ns, p, "", "day: looks fine"))
			if err == nil {
				resumed = true
				d.endPause(ns, p)
			}
			d.t.Logf("triage's resume: %v", err)
		}),
		act("resumed", func() {
			if !resumed {
				require.NoError(d.t, lifecycle.Resume(d.CtxOf(dev), d.Client, ns, p, d.change(dev, ns, p, "", "day: fixed")))
				d.endPause(ns, p)
			}
		}),
		until("Verified", func() bool { return d.phase(ns, x.Name) == "Verified" }),
		until("audited", func() bool {
			return len(d.events(ns, "PipelinePaused")) > 0 && len(d.events(ns, "PipelineResumed")) > 0
		}),
		check("the pause names who", func() {
			found := false
			for _, e := range d.events(ns, "PipelinePaused") {
				found = found || actorOf(e) == dev.Username
			}
			assert.True(d.t, found, "a PipelinePaused AuditEvent names %s", dev.Username)
		}),
		act("triage ships", func() { y = d.ship(triage, ns, p, "1.7.0") }),
		until("Verified", func() bool { return d.phase(ns, y.Name) == "Verified" }),
	}}
}

// hold: prod held on what runs there keeps the next version out until the
// hold is released (by triage if admitted, without its stamp, else by the
// release manager).
func (d *Day) hold() *Episode {
	ns, p := TeamB, Web
	var x, y *v1alpha1.Bundle
	var h *holdRec
	return &Episode{Name: "hold and release", Uses: []string{key(ns, p)}, Steps: []Step{
		act("a person ships", func() { x = d.ship(dev, ns, p, "2.1.0") }),
		until("Verified", func() bool { return d.phase(ns, x.Name) == "Verified" }),
		act("held", func() {
			_, _, err := lifecycle.HoldCurrent(d.CtxOf(rm), d.Client, lifecycle.HoldCurrentRequest{Namespace: ns, Pipeline: p,
				Environment: "prod", Reason: "day: soak", Actor: rm.Username, Stamp: d.stamp(rm, ns, p, "prod"), Now: d.Now()})
			require.NoError(d.t, err)
			h = &holdRec{ns: ns, pipeline: p, env: "prod", bundle: x.Name, from: d.Now()}
			d.holds = append(d.holds, h)
		}),
		act("a bot ships", func() { y = d.ship(bot, ns, p, "2.2.0") }),
		until("verified in staging", func() bool { return d.verifiedIn(ns, y.Name, "staging") }),
		rounds("held", scenario.BlockRounds),
		check("prod is held", func() {
			assert.False(d.t, d.verifiedIn(ns, y.Name, "prod"), "a version promoted into a held environment")
			found := false
			for _, e := range d.events(ns, "HoldCreated") {
				found = found || actorOf(e) == rm.Username
			}
			assert.True(d.t, found, "a HoldCreated AuditEvent names %s", rm.Username)
		}),
		act("released", func() {
			_, err := lifecycle.ReleaseHold(d.CtxOf(triage), d.Client, ns, p, "prod", d.change(triage, ns, p, "prod", "day: soaked"))
			d.t.Logf("triage's release: %v", err)
			if err != nil {
				_, err = lifecycle.ReleaseHold(d.CtxOf(rm), d.Client, ns, p, "prod", d.change(rm, ns, p, "prod", "day: soaked"))
				require.NoError(d.t, err)
			}
			h.to = d.Now()
		}),
		until("Verified", func() bool { return d.phase(ns, y.Name) == "Verified" }),
	}}
}

// rollback: a bot's version fails its canary on the fleet (Argo Rollouts
// aborts it) and the fleet rolls back to the version before.
func (d *Day) rollback() *Episode {
	ns := TeamC
	pl := d.plans[ns]
	var base, bad *v1alpha1.Bundle
	return &Episode{Name: "canary rollback", Uses: []string{key(ns, scenario.PipelineName)}, Steps: []Step{
		act("a person ships", func() { base = d.ship(dev, ns, scenario.PipelineName, "1.3.0") }),
		until("Verified", func() bool { return d.phase(ns, base.Name) == "Verified" }),
		act("a bot ships a bad one", func() {
			for _, e := range pl.GateEnvs {
				d.GitOps.Fail(ns, e, "1.4.0")
			}
			bad = d.ship(bot, ns, scenario.PipelineName, "1.4.0")
		}),
		until("rolled back", func() bool {
			run := pl.Running(d.World)
			for _, e := range pl.GateEnvs {
				if run[e] != "1.3.0" {
					return false
				}
			}
			ph := d.phase(ns, bad.Name)
			return !d.InFlight(ns, bad.Name) && (ph == "Failed" || ph == "Superseded") && d.rollbackVerified(ns)
		}),
		check("the abort failed it", func() {
			for _, e := range pl.GateEnvs {
				if s, ok := d.Steps(ns, bad.Name)[e]; ok && s.Status.State == "Failed" {
					assert.Contains(d.t, s.Status.Message, "aborted the update", "%s", e)
				}
			}
		}),
	}}
}

func (d *Day) rollbackVerified(ns string) bool {
	for name, phase := range d.BundlePhase(ns) {
		b := d.bundle(ns, name)
		if phase == "Verified" && b.Spec.Provenance != nil && b.Spec.Provenance.RollbackOf != "" {
			return true
		}
	}
	return false
}

// outageEp: the agent index is deleted in the middle of the day with a
// version in flight. Versions, agents' and people's, are refused and so are
// forged stamps; nothing forged is recorded; once the index is back the
// version in flight finishes.
func (d *Day) outageEp() *Episode {
	ns, p := TeamB, Web
	var in *v1alpha1.Bundle
	paused, held, forgedFreeze := false, false, false
	people := func() bool { return true }
	return &Episode{Name: "index outage", Uses: []string{key(ns, p)}, Steps: []Step{
		act("a person ships", func() { in = d.ship(dev, ns, p, "2.5.0") }),
		until("in flight", func() bool { return d.verifiedIn(ns, in.Name, "dev") }),
		{Name: "the index is deleted", Pre: people, Do: func() {
			d.outage, d.outageSince = true, true
			d.PauseIndex(true)
			require.NoError(d.t, d.Client.Delete(d.Ctx, &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: scenario.Release}}))
		}},
		rounds("out", 1),
		during("versions and forged stamps are refused", func() {
			for _, id := range []scenario.Identity{fixer, dev, triage} {
				b := scenario.ImageBundle(ns, p, "2.6.0")
				cliErr, refused, err := d.CreateBundle(id, b)
				require.NoError(d.t, err)
				require.NoError(d.t, cliErr)
				assert.NotEmpty(d.t, refused, "%s's version was admitted with no index", id.Username)
			}
			forgedStamp := &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: forged}
			err := lifecycle.Pause(d.CtxOf(dev), d.Client, ns, p, &v1alpha1.PipelineChange{By: dev.Username, Reason: "forged", Actor: forgedStamp})
			if err == nil {
				paused = true
				d.pauses = append(d.pauses, &pauseRec{ns: ns, pipeline: p, from: d.Now()})
				d.probes = append(d.probes, "a pause stamped "+forged)
			}
			// A person posing as an agent: the policy cannot tell with no
			// index; the controller must not record it.
			agentStamp := &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: dev.Username, Agent: "fixer", Sponsor: scenario.Sponsor}
			_, _, err = lifecycle.HoldCurrent(d.CtxOf(dev), d.Client, lifecycle.HoldCurrentRequest{Namespace: ns, Pipeline: p,
				Environment: "dev", Reason: "forged", Actor: dev.Username, Stamp: agentStamp, Now: d.Now()})
			if err == nil {
				d.probes = append(d.probes, "a hold by a person stamped as agent fixer")
				d.holds = append(d.holds, &holdRec{ns: ns, pipeline: p, env: "dev", bundle: in.Name, from: d.Now()})
				held = true
			} else {
				d.t.Logf("a hold stamped as another's agent with no index: %v", err)
			}
			_, _, err = lifecycle.Freeze(d.CtxOf(ic), d.Client, lifecycle.FreezeRequest{Actors: v1alpha1.FreezeAgents, Namespace: TeamA,
				Reason: "forged", CreatedBy: ic.Username, Stamp: forgedStamp, Now: d.Now()})
			if err == nil {
				d.probes = append(d.probes, "a freeze stamped "+forged)
				forgedFreeze = true
			}
			d.t.Logf("a freeze stamped %s with no index: %v", forged, err)
		}),
		rounds("out", scenario.BlockRounds),
		{Name: "the index is back", Pre: people, Do: func() { d.PauseIndex(false) }, Wait: d.IndexReady, Max: scenario.BlockRounds},
		during("people act again", func() { d.outage = false }),
		act("resumed", func() {
			if paused {
				require.NoError(d.t, lifecycle.Resume(d.CtxOf(dev), d.Client, ns, p, d.change(dev, ns, p, "", "day: after the outage")))
				d.endPause(ns, p)
			}
			if held {
				require.NoError(d.t, d.releaseAll(ns, p, "dev"))
			}
			if forgedFreeze {
				var cws v1alpha1.ChangeWindowList
				require.NoError(d.t, d.Client.List(d.Ctx, &cws))
				for _, cw := range cws.Items {
					if f := cw.Spec.Freeze; f != nil && f.Lifted == nil && f.Actor != nil && f.Actor.Principal == forged {
						require.NoError(d.t, lifecycle.Unfreeze(d.CtxOf(ic), d.Client, cw.Name, "", &v1alpha1.FreezeLift{By: ic.Username}))
					}
				}
			}
		}),
		until("the version in flight is Verified", func() bool { return d.phase(ns, in.Name) == "Verified" }),
		check("nothing refused exists", func() {
			assert.Nil(d.t, d.bundle(ns, scenario.ImageBundle(ns, p, "2.6.0").Name), "a version refused in the outage exists")
		}),
	}}
}

// releaseAll releases a hold admitted in the outage (as the cluster admin).
func (d *Day) releaseAll(ns, p, env string) error {
	_, err := lifecycle.ReleaseHold(scenario.AsUser(d.Ctx, scenario.AdminUser, scenario.AdminGroups...), d.Client, ns, p, env, nil)
	for _, h := range d.holds {
		if h.ns == ns && h.pipeline == p && h.env == env && h.to.IsZero() {
			h.to = d.Now()
		}
	}
	return err
}

// Timeline runs part (1-based) of the day's episodes in their order.
func Timeline(t *testing.T, part int) {
	d := NewDay(t, 0)
	d.Run(d.Episodes(part), false)
}

// Seeded runs part of the day with a seeded random interleaving of its
// episodes. A failure names the seed: KARDINAL_SCENARIO_DAY_SEED=<seed>
// replays it.
func Seeded(t *testing.T, part int, seed int64) {
	t.Logf("part %d, seed %d (replay with KARDINAL_SCENARIO_DAY_SEED=%d)", part, seed, seed)
	d := NewDay(t, seed)
	d.Run(d.Episodes(part), true)
}

// Whole reports whether the whole day also runs as one, its three parts
// interleaved on one World (KARDINAL_SCENARIO_ENTERPRISE=full, or
// KARDINAL_SCENARIO_DAY_WHOLE=1): longer than a package's bound in go test
// ./..., so CI's scenario-enterprise job runs it.
func Whole() bool {
	return os.Getenv("KARDINAL_SCENARIO_ENTERPRISE") == "full" || os.Getenv("KARDINAL_SCENARIO_DAY_WHOLE") == "1"
}

// SeededWhole is Seeded for the whole day.
func SeededWhole(t *testing.T, seed int64) {
	t.Logf("the whole day, seed %d (replay with KARDINAL_SCENARIO_DAY_WHOLE=1 KARDINAL_SCENARIO_DAY_SEED=%d)", seed, seed)
	d := NewDay(t, seed)
	var eps []*Episode
	for part := 1; part <= Parts; part++ {
		eps = append(eps, d.Episodes(part)...)
	}
	d.Run(eps, true)
}
