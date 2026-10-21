//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// The day's episodes. Each runs on the test's goroutine while the load goes
// on, checks what it set out to show and returns an error naming what did
// not happen: the soak then stops there (soak.Checker.Fail). What an
// episode leaves in flight is checked by a later pass (soakDay.later).

// freezeGrace is how long after a freeze turns active a step that had
// already passed its freeze check may still start: the ChangeWindow watch
// wakes the held steps within seconds.
const freezeGrace = 15 * time.Second

// warmUp ends the warm-up: every load Pipeline has reached its
// historyLimit, so the memory baselines start here.
func (s *soakDay) warmUp(context.Context) error {
	s.mu.Lock()
	s.warmAt = time.Now()
	s.mu.Unlock()
	s.note("warmAtSeconds", int(time.Since(s.start).Seconds()))
	return nil
}

// agentsFrozen freezes agents in every namespace (kardinal freeze --agents)
// for a twelfth of the day: the agent's version waits at test, frozen,
// while people's versions go on; no agent's promotion starts until the
// freeze is lifted, and then the held version promotes.
func (s *soakDay) agentsFrozen(ctx context.Context) error {
	a := s.a
	s.pause("agent", true)
	defer s.pause("agent", false)
	const freeze = "kardinal-freeze-agents"
	if _, err := s.cliRun(ctx, "", "", "freeze", "--agents", "--reason", "SOAK-1 agents paused for the drill", "--yes"); err != nil {
		return err
	}
	lifted := false
	defer func() {
		if !lifted {
			_, _ = s.cliRun(context.Background(), "", "", "unfreeze", freeze)
		}
	}()
	if err := s.waitFreeze(ctx, freeze, true); err != nil {
		return err
	}
	active := time.Now()
	held, err := s.agentShip(ctx)
	if err != nil {
		return err
	}
	if err := s.waitFrozen(ctx, a.ns, a.agentPipeline, held, "test", freeze); err != nil {
		return err
	}
	if err := soakWait(ctx, s.span(5.0/60, 2*time.Minute), "a person's or CI's version Verified in team a during the agent freeze",
		func(ctx context.Context) (bool, string, error) {
			n, err := s.verifiedSince(ctx, a.ns, active.Add(freezeGrace), func(p string) bool { return p != a.agentPipeline })
			return n > 0, fmt.Sprintf("%d steps Verified", n), err
		}); err != nil {
		return err
	}
	if !s.sleepUntil(ctx, active.Add(s.span(5.0/60, 2*time.Minute))) {
		return nil
	}
	ps, ok, err := s.e.Step(ctx, a.ns, a.agentPipeline, held, "test")
	switch {
	case err != nil:
		return err
	case !ok || ps.Status.FrozenBy == nil || ps.Status.FrozenBy.Name != freeze:
		return fmt.Errorf("the agent's version %s is no longer held at test before the freeze is lifted: %+v", held, ps)
	}
	if err := s.noPromotionStarted(ctx, a.ns, active.Add(freezeGrace), time.Now(), func(ev *v1alpha1.AuditEvent) bool {
		return ev.Spec.Actor != nil && ev.Spec.Actor.Kind == v1alpha1.ActorAgent
	}); err != nil {
		return fmt.Errorf("agents frozen: %w", err)
	}
	if _, err := s.cliRun(ctx, "", "", "unfreeze", freeze); err != nil {
		return err
	}
	lifted = true
	s.window("agents-frozen", active, time.Now())
	return soakWait(ctx, 5*time.Minute, "the agent's held version "+held+" Verified at test once the freeze is lifted",
		func(ctx context.Context) (bool, string, error) {
			ps, ok, err := s.e.Step(ctx, a.ns, a.agentPipeline, held, "test")
			if err != nil || !ok {
				return false, fmt.Sprint(ok), err
			}
			return ps.Status.State == "Verified", ps.Status.State + ": " + ps.Status.Message, nil
		})
}

// waitFreeze waits until ChangeWindow name is active (or not).
func (s *soakDay) waitFreeze(ctx context.Context, name string, active bool) error {
	return soakWait(ctx, time.Minute, fmt.Sprintf("freeze %s active=%v", name, active), func(ctx context.Context) (bool, string, error) {
		var cw v1alpha1.ChangeWindow
		if err := s.e.Client.Get(ctx, types.NamespacedName{Name: name}, &cw); err != nil {
			return false, "", err
		}
		return cw.Status.Active == active, fmt.Sprintf("active=%v reason=%q", cw.Status.Active, cw.Status.Reason), nil
	})
}

// waitFrozen waits until bundle's env step is held by freeze.
func (s *soakDay) waitFrozen(ctx context.Context, ns, pipeline, bundle, env, freeze string) error {
	return soakWait(ctx, 2*time.Minute, fmt.Sprintf("%s/%s held at %s by %s", ns, bundle, env, freeze), func(ctx context.Context) (bool, string, error) {
		ps, ok, err := s.e.Step(ctx, ns, pipeline, bundle, env)
		if err != nil || !ok {
			return false, fmt.Sprint(ok), err
		}
		return ps.Status.FrozenBy != nil && ps.Status.FrozenBy.Name == freeze, ps.Status.State + ": " + ps.Status.Message, nil
	})
}

// verifiedSince counts ns's steps Verified after from whose Pipeline match
// accepts.
func (s *soakDay) verifiedSince(ctx context.Context, ns string, from time.Time, match func(pipeline string) bool) (int, error) {
	var list v1alpha1.PromotionStepList
	if err := s.e.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return 0, err
	}
	n := 0
	for i := range list.Items {
		ps := &list.Items[i]
		if at := stepVerifiedAt(ps); at.After(from) && match(ps.Spec.PipelineName) {
			n++
		}
	}
	return n, nil
}

// noPromotionStarted fails when ns has a PromotionStarted AuditEvent from
// from to to that match accepts.
func (s *soakDay) noPromotionStarted(ctx context.Context, ns string, from, to time.Time, match func(*v1alpha1.AuditEvent) bool) error {
	var list v1alpha1.AuditEventList
	if err := s.e.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return err
	}
	for i := range list.Items {
		ev := &list.Items[i]
		at := ev.Spec.Timestamp.Time
		if ev.Spec.Action == "PromotionStarted" && at.After(from) && at.Before(to) && match(ev) {
			return objectErr(ev, "%s/%s started promoting to %s at %s, while frozen", ns, ev.Spec.BundleName, ev.Spec.Environment,
				at.UTC().Format(time.RFC3339))
		}
	}
	return nil
}

// forgedVerdicts: a person bound to the verifier role records a pass on a
// version waiting at team a's verify gate: admitted and recorded, never
// counted (verify.allowedAgents names the verifier agent only). A registered
// agent, once suspended, may not write a Verdict at all.
func (s *soakDay) forgedVerdicts(ctx context.Context) error {
	a := s.a
	var bundle, out string
	var tried []string
	err := soakWait(ctx, 5*time.Minute, "mallory's pass recorded on a version waiting at prod's verify gate", func(ctx context.Context) (bool, string, error) {
		g, pipeline, version, err := s.waitingVerifyGate(ctx)
		if err != nil || g == nil {
			return false, "no version of team a waits for verdicts", err
		}
		b := g.Labels["kardinal.io/bundle"]
		o, err := s.cliRun(ctx, s.malloryKubeconfig, a.ns, "verify", pipeline, "uat", "--check", "explore", "--pass",
			"--summary", "forged: looks fine to me", "--version", version)
		if err != nil {
			tried = append(tried, b+": "+err.Error())
			return false, strings.Join(tried, "; "), nil
		}
		bundle, out = b, o
		return true, "", nil
	})
	if err != nil {
		return err
	}
	// Admitted and kept, and kardinal verify says the gate does not count
	// it; the gate copies and counts no verdict of hers.
	if !strings.Contains(out, "not counted by gate explore-qa: ") {
		return fmt.Errorf("kardinal verify does not say mallory's verdict on %s is not counted:\n%s", bundle, out)
	}
	var vs v1alpha1.VerdictList
	if err := s.e.Client.List(ctx, &vs, client.InNamespace(a.ns), client.MatchingLabels{"kardinal.io/bundle": bundle}); err != nil {
		return err
	}
	if !slices.ContainsFunc(vs.Items, func(v v1alpha1.Verdict) bool { return v.Spec.By == soakMallory }) {
		return fmt.Errorf("mallory's Verdict on %s is not kept", bundle)
	}
	err = soakWait(ctx, time.Minute, "mallory's verdict on "+bundle+" never counted", func(ctx context.Context) (bool, string, error) {
		gs, err := s.e.GateInstances(ctx, a.ns, bundle, "prod", "explore-qa")
		if err != nil || len(gs) != 1 {
			return false, fmt.Sprintf("%d instances", len(gs)), err
		}
		for _, v := range gs[0].Status.Verdicts {
			if strings.Contains(v.Verifier, soakMallory) && v.Counted {
				return false, "", objectErr(&gs[0], "gate %s counts mallory's verdict %s: %+v", gs[0].Name, v.Name, v)
			}
		}
		// Judged at least once since her verdict.
		return gs[0].Status.LastEvaluatedAt != nil && gs[0].Status.LastEvaluatedAt.After(time.Now().Add(-30*time.Second)),
			framework.DescribeGate(&gs[0]), nil
	})
	if err != nil {
		return err
	}
	s.note("forgedVerdictStanding", strings.TrimSpace(out))

	// The rogue agent: registered, then suspended; its Verdict is refused.
	var id v1alpha1.AgentIdentity
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := s.e.Client.Get(ctx, types.NamespacedName{Name: s.rogue}, &id); err != nil {
			return err
		}
		id.Spec.Suspended = true
		return s.e.Client.Update(ctx, &id)
	}); err != nil {
		return fmt.Errorf("suspend agent %s: %w", s.rogue, err)
	}
	var b v1alpha1.Bundle
	if err := s.e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: bundle}, &b); err != nil {
		return err
	}
	var refused error
	err = soakWait(ctx, time.Minute, "the suspended agent "+s.rogue+"'s Verdict refused", func(ctx context.Context) (bool, string, error) {
		v := &v1alpha1.Verdict{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "rogue-", Namespace: a.ns},
			Spec: v1alpha1.VerdictSpec{Pipeline: b.Spec.Pipeline, Bundle: b.Name, BundleUID: string(b.UID), Environment: "uat",
				Check: "explore", Result: "pass", Summary: "trust me", By: soakRogueUser,
				Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorAgent, Principal: soakRogueUser, Agent: s.rogue, Sponsor: soakRogueSponsor}},
		}
		refused = s.asRogue.Create(ctx, v)
		if refused == nil {
			return false, "", objectErr(v, "the suspended agent %s wrote Verdict %s", s.rogue, v.Name)
		}
		return strings.Contains(refused.Error(), "suspended"), refused.Error(), nil
	})
	if err != nil {
		return err
	}
	s.note("forgedVerdictRefusal", refused.Error())
	return nil
}

// waitingVerifyGate is a prod verify gate instance of team a still waiting
// for verdicts whose version runs in uat, with its Pipeline and version
// (the image tag: not the evidence Pipeline, whose versions are digests).
func (s *soakDay) waitingVerifyGate(ctx context.Context) (*v1alpha1.PolicyGate, string, string, error) {
	a := s.a
	var gates v1alpha1.PolicyGateList
	if err := s.e.Client.List(ctx, &gates, client.InNamespace(a.ns), client.MatchingLabels{
		"kardinal.io/gate-template": "explore-qa", "kardinal.io/environment": "prod"}); err != nil {
		return nil, "", "", err
	}
	for i := range gates.Items {
		g := &gates.Items[i]
		if g.Status.Ready || !strings.Contains(g.Status.Reason, "waiting for verdicts") {
			continue
		}
		var b v1alpha1.Bundle
		if err := s.e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: g.Labels["kardinal.io/bundle"]}, &b); err != nil {
			continue
		}
		if b.Spec.Pipeline == a.evidence || len(b.Spec.Images) == 0 || b.Spec.Images[0].Tag == "" || scale.TerminalPhase(b.Status.Phase) {
			continue
		}
		return g, b.Spec.Pipeline, b.Spec.Images[0].Tag, nil
	}
	return nil, "", "", nil
}

// stopTheLine stops the line in team a (kardinal freeze --everyone -n) for
// a twelfth of the day: steps wait before they start or before their next
// git step, none starts while it holds, and every held step goes on once
// it is lifted.
func (s *soakDay) stopTheLine(ctx context.Context) error {
	a := s.a
	freeze := "kardinal-freeze-everyone-" + a.ns
	if _, err := s.cliRun(ctx, "", a.ns, "freeze", "--everyone", "--reason", "SOAK-2 stop the line", "--yes"); err != nil {
		return err
	}
	lifted := false
	defer func() {
		if !lifted {
			_, _ = s.cliRun(context.Background(), "", a.ns, "unfreeze", "--everyone")
		}
	}()
	if err := s.waitFreeze(ctx, freeze, true); err != nil {
		return err
	}
	active := time.Now()
	s.ciBundle(ctx, a, a.load[1])
	if err := soakWait(ctx, 2*time.Minute, "a step of team a held by "+freeze, func(ctx context.Context) (bool, string, error) {
		n, err := s.frozenBy(ctx, a.ns, freeze)
		return n > 0, fmt.Sprintf("%d held", n), err
	}); err != nil {
		return err
	}
	if !s.sleepUntil(ctx, active.Add(s.span(5.0/60, 2*time.Minute))) {
		return nil
	}
	if err := s.noPromotionStarted(ctx, a.ns, active.Add(freezeGrace), time.Now(),
		func(*v1alpha1.AuditEvent) bool { return true }); err != nil {
		return fmt.Errorf("the line stopped: %w", err)
	}
	held, err := s.frozenBy(ctx, a.ns, freeze)
	if err != nil {
		return err
	}
	s.note("stopTheLineHeld", held)
	if _, err := s.cliRun(ctx, "", a.ns, "unfreeze", "--everyone"); err != nil {
		return err
	}
	lifted = true
	s.window("stop-the-line", active, time.Now())
	return soakWait(ctx, 5*time.Minute, "every step of team a released once the line restarts", func(ctx context.Context) (bool, string, error) {
		n, err := s.frozenBy(ctx, a.ns, freeze)
		return n == 0, fmt.Sprintf("%d still held", n), err
	})
}

// frozenBy counts ns's steps held by freeze.
func (s *soakDay) frozenBy(ctx context.Context, ns, freeze string) (int, error) {
	var list v1alpha1.PromotionStepList
	if err := s.e.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return 0, err
	}
	n := 0
	for _, ps := range list.Items {
		if ps.Status.FrozenBy != nil && ps.Status.FrozenBy.Name == freeze {
			n++
		}
	}
	return n, nil
}

// soakFlight is what was in flight at an episode: steps not finished (by
// UID), change requests pending and receipts not Stored.
type soakFlight struct {
	steps         []soakFlightStep
	crs, receipts []types.NamespacedName
}

// soakFlightStep is a step in flight, with its UID: a step of that name made
// again later is another promotion.
type soakFlightStep struct {
	key              types.NamespacedName
	uid              types.UID
	bundle, pipeline string
}

func (f soakFlight) String() string {
	return fmt.Sprintf("%d steps, %d change requests, %d receipts", len(f.steps), len(f.crs), len(f.receipts))
}

// inFlight snapshots what is in flight in the teams' namespaces.
func (s *soakDay) inFlight(ctx context.Context) (soakFlight, error) {
	var f soakFlight
	for _, tn := range s.tenants() {
		var steps v1alpha1.PromotionStepList
		if err := s.e.Client.List(ctx, &steps, client.InNamespace(tn.ns)); err != nil {
			return f, err
		}
		for _, ps := range steps.Items {
			if !finishedState(ps.Status.State) {
				f.steps = append(f.steps, soakFlightStep{key: types.NamespacedName{Namespace: ps.Namespace, Name: ps.Name}, uid: ps.UID,
					bundle: ps.Spec.BundleName, pipeline: ps.Spec.PipelineName})
			}
		}
		var crs v1alpha1.ChangeRequestList
		if err := s.e.Client.List(ctx, &crs, client.InNamespace(tn.ns)); err != nil {
			return f, err
		}
		for _, cr := range crs.Items {
			// A ticket open and waiting: an instance whose upstream is not
			// Verified yet has no state and nothing in flight.
			if cr.Labels["kardinal.io/bundle"] != "" && cr.Status.State == "pending" {
				f.crs = append(f.crs, types.NamespacedName{Namespace: cr.Namespace, Name: cr.Name})
			}
		}
		var rcs v1alpha1.PromotionReceiptList
		if err := s.e.Client.List(ctx, &rcs, client.InNamespace(tn.ns)); err != nil {
			return f, err
		}
		for _, rc := range rcs.Items {
			if rc.Status.Phase != v1alpha1.ReceiptStored {
				f.receipts = append(f.receipts, types.NamespacedName{Namespace: rc.Namespace, Name: rc.Name})
			}
		}
	}
	return f, nil
}

// finishedState is a step state no promotion leaves (Fleet.WaitSettled's,
// and Superseded: a step whose Bundle a newer one replaced).
func finishedState(state string) bool {
	switch state {
	case "Verified", "Failed", "AbortedByAlarm", "RollingBack", "Superseded":
		return true
	}
	return false
}

// finished is the check that what f held in flight resumed and finished
// well, not just stopped: each step (by UID) ended Verified, or Superseded
// by a newer version, never Failed or rolling back, whether the step is
// still there or retired into its Bundle's status.retiredSteps; each change
// request was decided, or its Bundle was Superseded or Verified; each
// receipt is Stored. The podinfo release's steps are left out (the
// automatic-rollback episode fails one on purpose), and so are probes. A
// step whose Bundle was pruned (historyLimit) cannot be judged and is
// counted in the report.
func (s *soakDay) finished(what string, f soakFlight) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		bundle := func(ns, name string) (*v1alpha1.Bundle, error) {
			var b v1alpha1.Bundle
			if err := s.e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); apierrors.IsNotFound(err) {
				return nil, nil
			} else if err != nil {
				return nil, err
			}
			return &b, nil
		}
		pruned := 0
		for _, st := range f.steps {
			if st.pipeline == pipelineName || s.isProbe(st.bundle) {
				continue
			}
			b, err := bundle(st.key.Namespace, st.bundle)
			if err != nil {
				return err
			}
			if b == nil {
				pruned++
				continue
			}
			state, msg := "", ""
			var ps v1alpha1.PromotionStep
			switch err := s.e.Client.Get(ctx, st.key, &ps); {
			case err == nil && ps.UID == st.uid:
				state, msg = ps.Status.State, ps.Status.Message
			case err != nil && !apierrors.IsNotFound(err):
				return err
			default: // retired into the Bundle (or made again: not the step in flight)
				for _, r := range b.Status.RetiredSteps {
					if r.Name == st.key.Name {
						state, msg = r.State, r.Message
					}
				}
			}
			switch {
			case state == "Verified" || state == "Superseded":
			case state == "" && (b.Status.Phase == "Verified" || b.Status.Phase == "Superseded"):
				// The step is gone, retired with no record, and its Bundle
				// did not fail.
			case finishedState(state):
				return objectErr(b, "step %s of %s, in flight at %s, ended %s (%s), Bundle %s", st.key, st.bundle, what, state, msg, b.Status.Phase)
			default:
				return objectErr(b, "step %s of %s, in flight at %s, is still %q (%s), Bundle %s", st.key, st.bundle, what, state, msg, b.Status.Phase)
			}
		}
		s.note("inFlightPruned."+what, pruned)
		for _, k := range f.crs {
			var cr v1alpha1.ChangeRequest
			if err := s.e.Client.Get(ctx, k, &cr); apierrors.IsNotFound(err) {
				continue
			} else if err != nil {
				return err
			}
			if cr.Status.State != "pending" {
				continue
			}
			b, err := bundle(cr.Namespace, cr.Labels["kardinal.io/bundle"])
			if err != nil {
				return err
			}
			if b != nil && b.Status.Phase != "Superseded" && b.Status.Phase != "Verified" {
				return objectErr(&cr, "change request %s, pending at %s, is still pending; its Bundle is %s", k, what, b.Status.Phase)
			}
		}
		for _, k := range f.receipts {
			var rc v1alpha1.PromotionReceipt
			if err := s.e.Client.Get(ctx, k, &rc); apierrors.IsNotFound(err) {
				continue
			} else if err != nil {
				return err
			}
			if rc.Status.Phase != v1alpha1.ReceiptStored {
				return objectErr(&rc, "receipt %s, in flight at %s, is %s", k, what, rc.Status.Phase)
			}
		}
		return nil
	}
}

// upgrade upgrades the release mid-soak with promotions, change requests,
// verdicts and receipts in flight. Until v0.13.0 is tagged it is a helm
// upgrade of this build that turns perNamespace: required on, with each
// team's registry prefix in namespaceRepositories (a rolling restart of
// both replicas); see #2403 for the v0.13 to v0.14 upgrade that replaces it.
func (s *soakDay) upgrade(ctx context.Context) error {
	t, e := s.t, s.e
	flight, err := s.inFlight(ctx)
	if err != nil {
		return err
	}
	from, closeWindow := s.openWindow("upgrade")
	defer closeWindow()
	sets := []string{"--set receipts.sinks.oci.perNamespace=required", "--set receipts.sinks.oci.namespaceAnnotation=false"}
	for _, tn := range s.tenants() {
		sets = append(sets, fmt.Sprintf("--set 'receipts.sinks.oci.namespaceRepositories.%s={%s}'", tn.ns, s.reg.Repository(tn.prefix)))
	}
	run := e.Shell(t, `helm upgrade kardinal-promoter "$KARDINAL_E2E_CHART" -n kardinal-system --reuse-values `+
		strings.Join(sets, " ")+` --wait --timeout 5m`)
	if run.Code != 0 {
		return fmt.Errorf("helm upgrade: exit %d: %s", run.Code, run.Output)
	}
	s.rel.WaitRolledOut(t, 5*time.Minute)
	e.WaitControllerLeads(t)
	d := s.rel.Deployment(t)
	args := strings.Join(d.Spec.Template.Spec.Containers[0].Args, " ")
	if !strings.Contains(args, "--receipt-oci-per-namespace=required") {
		return fmt.Errorf("the upgraded controller runs without --receipt-oci-per-namespace=required: %s", args)
	}
	s.note("upgradeSeconds", int(time.Since(from).Seconds()))
	s.note("upgradeInFlight", flight.String())
	s.later("upgrade", time.Now().Add(10*time.Minute), s.finished("the upgrade", flight))
	return nil
}

// crossTenantImage: after the upgrade, team a ships team b's image: its
// receipt is signed and kept but gets no referrer in team b's repository
// (Stored False, RepositoryNotAllowed, naming team a's namespace).
func (s *soakDay) crossTenantImage(ctx context.Context) error {
	a, b := s.a, s.b
	s.pause("evidence", true)
	defer s.pause("evidence", false)
	name, err := s.imageBundle(ctx, a.ns, a.evidence, b.image, "", b.digests[0], "ci@team-a.example.com")
	if err != nil {
		return err
	}
	s.probe(name)
	return soakWait(ctx, 6*time.Minute, "the receipt of team a's version of team b's image "+name+" in test", func(ctx context.Context) (bool, string, error) {
		var list v1alpha1.PromotionReceiptList
		if err := s.e.Client.List(ctx, &list, client.InNamespace(a.ns),
			client.MatchingLabels{"kardinal.io/bundle": name, "kardinal.io/environment": "test"}); err != nil {
			return false, "", err
		}
		if len(list.Items) != 1 || list.Items[0].Status.Phase != v1alpha1.ReceiptStored {
			return false, fmt.Sprintf("%d receipts", len(list.Items)), nil
		}
		rc := &list.Items[0]
		for _, st := range rc.Status.Stored {
			if st.Sink == v1alpha1.ReceiptSinkOCI {
				return false, "", objectErr(rc, "team a's receipt %s of team b's image was pushed to %s", rc.Name, st.Ref)
			}
		}
		for _, c := range rc.Status.Conditions {
			if c.Type == "Stored" {
				if c.Reason != "RepositoryNotAllowed" || !strings.Contains(c.Message, a.ns) {
					return false, "", objectErr(rc, "receipt %s: Stored %s %s: %s, want RepositoryNotAllowed naming %s", rc.Name, c.Status, c.Reason, c.Message, a.ns)
				}
				return true, "", nil
			}
		}
		return false, fmt.Sprintf("conditions %v", rc.Status.Conditions), nil
	})
}

// autoRollback: team c's podinfo release gets a version whose rollout
// fails; test (onHealthFailure: rollback) rolls back by itself to the
// release before, and only test does.
func (s *soakDay) autoRollback(ctx context.Context) error {
	c := s.c
	s.pause("podinfo", true)
	defer s.pause("podinfo", false)
	// The release before must be Verified at test: it is the rollback's
	// target.
	if err := soakWait(ctx, 10*time.Minute, "no podinfo release of team c in flight at test", func(ctx context.Context) (bool, string, error) {
		var list v1alpha1.PromotionStepList
		if err := s.e.Client.List(ctx, &list, client.InNamespace(c.ns)); err != nil {
			return false, "", err
		}
		for _, ps := range list.Items {
			if ps.Spec.PipelineName == pipelineName && ps.Spec.Environment == "test" && !finishedState(ps.Status.State) {
				return false, ps.Name + " " + ps.Status.State, nil
			}
		}
		return true, "", nil
	}); err != nil {
		return err
	}
	broken, err := s.imageBundle(ctx, c.ns, pipelineName, fixtures.Image, fixtures.BrokenTag, "", "ci@team-c.example.com")
	if err != nil {
		return err
	}
	s.probe(broken)
	from := time.Now()
	step := func(bundle, env string) func(ctx context.Context) (bool, string, error) {
		return func(ctx context.Context) (bool, string, error) {
			ps, ok, err := s.e.Step(ctx, c.ns, pipelineName, bundle, env)
			if err != nil || !ok {
				return false, fmt.Sprint(ok), err
			}
			return false, ps.Status.State + ": " + ps.Status.Message, nil
		}
	}
	state := func(bundle, env, want string) func(ctx context.Context) (bool, string, error) {
		return func(ctx context.Context) (bool, string, error) {
			_, seen, err := step(bundle, env)(ctx)
			return strings.HasPrefix(seen, want+":"), seen, err
		}
	}
	if err := soakWait(ctx, 8*time.Minute, "the broken release "+broken+" RollingBack at test", state(broken, "test", "RollingBack")); err != nil {
		return err
	}
	rb := broken + "-rollback-alarm"
	if err := soakWait(ctx, 8*time.Minute, "the rollback "+rb+" Verified at test", state(rb, "test", "Verified")); err != nil {
		return err
	}
	if err := soakWait(ctx, 2*time.Minute, "the broken release Superseded", func(ctx context.Context) (bool, string, error) {
		var b v1alpha1.Bundle
		if err := s.e.Client.Get(ctx, types.NamespacedName{Namespace: c.ns, Name: broken}, &b); err != nil {
			return false, "", err
		}
		return b.Status.Phase == "Superseded", b.Status.Phase, nil
	}); err != nil {
		return err
	}
	evs, err := s.e.AuditEvents(ctx, c.ns, broken)
	if err != nil {
		return err
	}
	started := false
	for _, ev := range evs {
		started = started || (ev.Spec.Action == "RollbackStarted" && ev.Spec.Environment == "test")
	}
	if !started {
		return fmt.Errorf("the broken release %s has no RollbackStarted AuditEvent for test", broken)
	}
	if ps, ok, err := s.e.Step(ctx, c.ns, pipelineName, broken, "uat"); err == nil && ok {
		return objectErr(ps, "the broken release %s reached uat", broken)
	}
	s.window("auto-rollback", from, time.Now())
	s.note("autoRollbackSeconds", int(time.Since(from).Seconds()))
	return nil
}

// leaderKilled deletes the leader Pod with no grace period while steps, a
// change request and (when the moment has one) receipts are in flight: the
// other replica takes the lease, and everything in flight finishes, each
// receipt signed once (the receipts invariant).
func (s *soakDay) leaderKilled(ctx context.Context) error {
	var flight soakFlight
	err := soakWait(ctx, 3*time.Minute, "steps and a change request in flight", func(ctx context.Context) (bool, string, error) {
		var err error
		flight, err = s.inFlight(ctx)
		return len(flight.steps) > 0 && len(flight.crs) > 0, flight.String(), err
	})
	if err != nil {
		return err
	}
	// A moment with a receipt in flight too, when one comes within a minute.
	_ = soakWait(ctx, time.Minute, "a receipt in flight", func(ctx context.Context) (bool, string, error) {
		f, err := s.inFlight(ctx)
		if err == nil && len(f.steps) > 0 && len(f.crs) > 0 {
			flight = f
		}
		return len(flight.receipts) > 0, flight.String(), err
	})
	old, err := scale.Leader(ctx, s.e)
	if err != nil {
		return err
	}
	from, closeWindow := s.openWindow("leader-killed")
	defer closeWindow()
	if err := scale.KillLeader(ctx, s.e); err != nil {
		return err
	}
	var leader string
	if err := soakWait(ctx, 2*time.Minute, "a new leader after killing "+old, func(ctx context.Context) (bool, string, error) {
		l, err := scale.Leader(ctx, s.e)
		if err != nil || l == old {
			return false, "lease held by " + l, err
		}
		leader = l
		return true, "", nil
	}); err != nil {
		return err
	}
	s.note("leaderHandoffSeconds", int(time.Since(from).Seconds()))
	s.e.WaitControllerLeads(s.t)
	s.note("leaderKilledInFlight", flight.String())
	s.note("leaderKilled", old+" -> "+leader)
	s.later("leader-killed", time.Now().Add(10*time.Minute), s.finished("the leader kill", flight))
	return nil
}

// consent: team b's first Pipeline adds the namespace that has not
// consented to its policyNamespaces: it is PolicyNamespacesShared False
// NotShared, and its next version gets the org's gate but none of that
// namespace's. Once the namespace consents the condition clears and the
// version after gets its gate too.
func (s *soakDay) consent(ctx context.Context) error {
	b := s.b
	p := b.load[0]
	s.pause("pipeline/"+p, true)
	defer s.pause("pipeline/"+p, false)
	if err := s.updatePipeline(ctx, b.ns, p, func(pl *v1alpha1.Pipeline) {
		pl.Spec.PolicyNamespaces = []string{s.org, s.private}
	}); err != nil {
		return err
	}
	cond := func(want bool) func(ctx context.Context) (bool, string, error) {
		return func(ctx context.Context) (bool, string, error) {
			var pl v1alpha1.Pipeline
			if err := s.e.Client.Get(ctx, types.NamespacedName{Namespace: b.ns, Name: p}, &pl); err != nil {
				return false, "", err
			}
			ok, seen := framework.CondIs(pl.Status.Conditions, "PolicyNamespacesShared", metav1.ConditionFalse, "NotShared")
			return ok == want, seen, nil
		}
	}
	if err := soakWait(ctx, 2*time.Minute, p+" PolicyNamespacesShared False NotShared", cond(true)); err != nil {
		return err
	}
	// The instance of template from namespace ns for bundle, by its labels
	// (its name is hashed when it is long).
	gateOf := func(bundle, ns, template string) func(ctx context.Context) (bool, string, error) {
		return func(ctx context.Context) (bool, string, error) {
			var list v1alpha1.PolicyGateList
			if err := s.e.Client.List(ctx, &list, client.InNamespace(b.ns), client.MatchingLabels{"kardinal.io/bundle": bundle,
				"kardinal.io/gate-template": template, graph.LabelGateTemplateNamespace: ns}); err != nil {
				return false, "", err
			}
			return len(list.Items) > 0, fmt.Sprintf("%d instances", len(list.Items)), nil
		}
	}
	v1, err := b.fleet.CreateBundle(ctx, p, s.tag(p))
	if err != nil {
		return err
	}
	if err := soakWait(ctx, 6*time.Minute, "the org's gate on "+v1.Name, gateOf(v1.Name, s.org, "org-policy")); err != nil {
		return err
	}
	if ok, _, err := gateOf(v1.Name, s.private, "private-policy")(ctx); err != nil || ok {
		return fmt.Errorf("%s got a gate of %s, which has not consented (err %v)", v1.Name, s.private, err)
	}
	s.mu.Lock()
	s.consentAt = time.Now()
	s.mu.Unlock()
	labelNamespace(s.t, s.e, s.private, v1alpha1.LabelPolicyShareable, "true")
	// The consent is re-read every five minutes, and at the Pipeline's next
	// reconcile, which an annotation brings forward.
	if err := s.updatePipeline(ctx, b.ns, p, func(pl *v1alpha1.Pipeline) {
		if pl.Annotations == nil {
			pl.Annotations = map[string]string{}
		}
		pl.Annotations["soak.kardinal.io/consented"] = time.Now().UTC().Format(time.RFC3339)
	}); err != nil {
		return err
	}
	if err := soakWait(ctx, 7*time.Minute, p+" no longer PolicyNamespacesShared False", cond(false)); err != nil {
		return err
	}
	v2, err := b.fleet.CreateBundle(ctx, p, s.tag(p))
	if err != nil {
		return err
	}
	return soakWait(ctx, 6*time.Minute, "the consenting namespace's gate on "+v2.Name, gateOf(v2.Name, s.private, "private-policy"))
}

// updatePipeline applies change to Pipeline ns/name, retrying on conflict.
func (s *soakDay) updatePipeline(ctx context.Context, ns, name string, change func(*v1alpha1.Pipeline)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var pl v1alpha1.Pipeline
		if err := s.e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &pl); err != nil {
			return err
		}
		change(&pl)
		return s.e.Client.Update(ctx, &pl)
	})
}
