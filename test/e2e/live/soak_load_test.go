//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// The day's load (section 2 of #2403's design): CI, people, an agent, the
// teams' registry images, a fleet rollout and podinfo releases create
// Bundles; bots answer what they wait for: the fake ticketing API's
// approvers, team a's approver and team b's reviewers. Load generators run
// on the soak's context and stop at its end or at the first violation; the
// bots run until the estate has settled. None of them touches a testing.T
// but to log: a failure they see is a violation (soak.Checker.Fail) or a
// count in the report.

// startLoad starts the warm-up burst, the load generators, the bots and
// the memory sampler.
func (s *soakDay) startLoad(soakCtx context.Context) {
	loadCtx, cancel := context.WithCancel(soakCtx)
	s.mu.Lock()
	s.loadCancel, s.load = cancel, loadCtx
	s.mu.Unlock()
	botsCtx, bcancel := context.WithCancel(context.Background())
	s.botsCancel = bcancel
	reviewer := scale.Review(s.t, s.e.Git, nil, s.reviewedRepos()...)
	s.botsWG.Add(1)
	go func() {
		defer s.botsWG.Done()
		<-botsCtx.Done()
		reviewer.Stop()
		s.note("prsMerged", reviewer.Merged())
	}()
	run := func(wg *sync.WaitGroup, ctx context.Context, fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(ctx)
		}()
	}
	run(&s.loadWG, loadCtx, s.warmBurst)
	run(&s.loadWG, loadCtx, s.ciLoad)
	run(&s.loadWG, loadCtx, s.peopleLoad)
	run(&s.loadWG, loadCtx, s.agentLoad)
	run(&s.loadWG, loadCtx, s.evidenceLoad)
	run(&s.loadWG, loadCtx, s.fleetLoad)
	run(&s.loadWG, loadCtx, s.podinfoLoad)
	run(&s.botsWG, botsCtx, s.ticketBot)
	run(&s.botsWG, botsCtx, s.approverBot)
	run(&s.botsWG, botsCtx, s.heapSampler)
}

// reviewedRepos are team b's repos: every one reviews prod.
func (s *soakDay) reviewedRepos() []gitserver.Repo {
	var out []gitserver.Repo
	for _, p := range s.b.pipelines {
		out = append(out, s.b.repos[p])
	}
	return out
}

// every runs fn every interval (jittered by a fifth) until ctx ends.
func (s *soakDay) every(ctx context.Context, interval time.Duration, fn func(ctx context.Context)) {
	for {
		d := s.rng.Duration(interval*4/5, interval*6/5)
		select {
		case <-ctx.Done():
			return
		case <-time.After(d):
		}
		fn(ctx)
	}
}

// tag is the next image tag of pipeline: scale.Tag with a counter per
// Pipeline, shared by every generator.
func (s *soakDay) tag(pipeline string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "tag/" + pipeline
	n, _ := s.notes[key].(int)
	n++
	s.notes[key] = n
	return scale.Tag(pipeline, n)
}

// create runs fn (one Bundle create), once more after two seconds when it
// fails; a second failure is a load error (the load invariant).
func (s *soakDay) create(ctx context.Context, what string, fn func(ctx context.Context) error) {
	err := fn(ctx)
	if err != nil && ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		err = fn(ctx)
	}
	switch {
	case err == nil:
		s.count("bundlesCreated", 1)
	case ctx.Err() == nil:
		s.mu.Lock()
		s.loadErrors = append(s.loadErrors, fmt.Sprintf("%s: %v", what, err))
		s.mu.Unlock()
	}
}

// ciBundle creates a Bundle of tn's pipeline at the scale image, as CI does.
func (s *soakDay) ciBundle(ctx context.Context, tn *soakTenant, pipeline string) {
	tag := s.tag(pipeline)
	s.create(ctx, "CI Bundle of "+tn.ns+"/"+pipeline, func(ctx context.Context) error {
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		_, err := tn.fleet.CreateBundle(cctx, pipeline, tag)
		return err
	})
}

// imageBundle creates a Bundle of pipeline in ns of repo at tag or digest,
// as CI does (kardinal.io/created-at stamped), and returns its name.
func (s *soakDay) imageBundle(ctx context.Context, ns, pipeline, repo, tag, digest, author string) (string, error) {
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, GenerateName: pipeline + "-"},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipeline,
			Images:     []v1alpha1.ImageRef{{Repository: repo, Tag: tag, Digest: digest}},
			Provenance: &v1alpha1.BundleProvenance{Author: author, CommitSHA: fmt.Sprintf("%040x", time.Now().UnixNano())}},
	}
	lifecycle.StampCreatedAt(b, time.Now())
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := s.e.Client.Create(cctx, b); err != nil {
		return "", fmt.Errorf("create Bundle of %s/%s: %w", ns, pipeline, err)
	}
	return b.Name, nil
}

// evidenceBundle creates a Bundle of tn's evidence Pipeline at the n-th
// copy of the team's registry image, by digest.
func (s *soakDay) evidenceBundle(ctx context.Context, tn *soakTenant, n int) {
	d := tn.digests[n%len(tn.digests)]
	s.create(ctx, "evidence Bundle of "+tn.ns, func(ctx context.Context) error {
		_, err := s.imageBundle(ctx, tn.ns, tn.evidence, tn.image, "", d, "ci@team-"+tn.name+".example.com")
		return err
	})
}

// cliRun runs the kardinal CLI with kubeconfig (the kind-only default when
// empty) in namespace ns, from any goroutine: it never touches a
// testing.T. It returns the output and an error naming the exit code.
func (s *soakDay) cliRun(ctx context.Context, kubeconfig, ns string, args ...string) (string, error) {
	c := s.e.CLI(s.t)
	if kubeconfig == "" {
		kubeconfig = c.Kubeconfig
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, c.Path(), c.Args(ns, args...)...)
	env := []string{"KUBECONFIG=" + kubeconfig}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "KUBECONFIG=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("kardinal %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// agentShip creates a version of the agent's Pipeline as team a's shipper
// agent, with its own token (the CLI stamps it agent), and returns its name.
func (s *soakDay) agentShip(ctx context.Context) (string, error) {
	a := s.a
	out, err := s.cliRun(ctx, a.agent.Kubeconfig, a.ns, "create", "bundle", a.agentPipeline,
		"--image", scale.ImageRepo+":"+s.tag(a.agentPipeline))
	if err != nil {
		return "", err
	}
	return createdBundle(out, a.agentPipeline)
}

// createdBundle is the Bundle of pipeline `kardinal create bundle` printed
// (bundleCreatedName, without a testing.T).
func createdBundle(out, pipeline string) (string, error) {
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, pipeline+"-") {
			return strings.Trim(f, ".:,()"), nil
		}
	}
	return "", fmt.Errorf("no Bundle of %s in the CLI's output: %q", pipeline, out)
}

// warmBurst creates soakHistory Bundles of every load Pipeline over the
// warm-up's first four fifths, so each Pipeline reaches its historyLimit
// before the memory baseline (the kept Bundles stop growing there).
func (s *soakDay) warmBurst(ctx context.Context) {
	type job struct {
		tn       *soakTenant
		pipeline string
	}
	var jobs []job
	for _, tn := range s.tenants() {
		for _, p := range tn.load {
			jobs = append(jobs, job{tn, p})
		}
		if tn.evidence != "" {
			jobs = append(jobs, job{tn, tn.evidence})
		}
	}
	span := s.span(4.0/60, time.Minute)
	gap := span / time.Duration(soakHistory*len(jobs)+soakHistory)
	for i := 0; i < soakHistory; i++ {
		for _, j := range jobs {
			if j.pipeline == j.tn.evidence {
				s.evidenceBundle(ctx, j.tn, i)
			} else {
				s.ciBundle(ctx, j.tn, j.pipeline)
			}
			if !s.sleepUntil(ctx, time.Now().Add(gap)) {
				return
			}
		}
		if _, err := s.agentShip(ctx); err != nil && ctx.Err() == nil {
			s.loadError("the agent's warm-up version", err)
		} else if err == nil {
			s.count("bundlesCreated", 1)
		}
	}
	s.note("warmBurstSeconds", int(time.Since(s.start).Seconds()))
}

// loadError records a create that failed.
func (s *soakDay) loadError(what string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadErrors = append(s.loadErrors, fmt.Sprintf("%s: %v", what, err))
}

// ciLoad creates Bundles of the teams' load Pipelines at KARDINAL_E2E_SOAK_RATE
// a minute (default 4) once the warm-up burst is over.
func (s *soakDay) ciLoad(ctx context.Context) {
	if !s.sleepUntil(ctx, s.start.Add(s.span(5.0/60, time.Minute))) {
		return
	}
	rate := 4.0
	if v := os.Getenv(envSoakRate); v != "" {
		if _, err := fmt.Sscanf(v, "%g", &rate); err != nil || rate <= 0 {
			s.loadError(envSoakRate, fmt.Errorf("%q: want Bundles a minute, a positive number", v))
			return
		}
	}
	var all []struct {
		tn *soakTenant
		p  string
	}
	for _, tn := range s.tenants() {
		for _, p := range tn.load {
			all = append(all, struct {
				tn *soakTenant
				p  string
			}{tn, p})
		}
	}
	s.every(ctx, time.Duration(float64(time.Minute)/rate), func(ctx context.Context) {
		j := all[s.rng.Intn(len(all))]
		if s.isPaused("pipeline/" + j.p) {
			return
		}
		// Each create on a goroutine of its own, so a slow API server shows
		// as latency, not as a lower rate (scale.Fleet.Sustained). This
		// goroutine holds the WaitGroup meanwhile, so Add is safe.
		s.loadWG.Add(1)
		go func() {
			defer s.loadWG.Done()
			s.ciBundle(ctx, j.tn, j.p)
		}()
	})
}

// peopleLoad: a person of team b ships a version with the CLI every two
// minutes (stamped human).
func (s *soakDay) peopleLoad(ctx context.Context) {
	s.every(ctx, 2*time.Minute, func(ctx context.Context) {
		p := s.b.load[s.rng.Intn(len(s.b.load))]
		if s.isPaused("pipeline/" + p) {
			return
		}
		s.create(ctx, "a person's version of "+s.b.ns+"/"+p, func(ctx context.Context) error {
			_, err := s.cliRun(ctx, "", s.b.ns, "create", "bundle", p, "--image", scale.ImageRepo+":"+s.tag(p))
			return err
		})
	})
}

// agentLoad: team a's shipper agent ships a version a minute, unless an
// episode paused it.
func (s *soakDay) agentLoad(ctx context.Context) {
	if !s.sleepUntil(ctx, s.start.Add(s.span(5.0/60, time.Minute))) {
		return
	}
	s.every(ctx, time.Minute, func(ctx context.Context) {
		if s.isPaused("agent") {
			return
		}
		s.create(ctx, "the agent's version", func(ctx context.Context) error {
			_, err := s.agentShip(ctx)
			return err
		})
	})
}

// evidenceLoad: a version of a team's registry image every four minutes,
// teams a and b in turn.
func (s *soakDay) evidenceLoad(ctx context.Context) {
	if !s.sleepUntil(ctx, s.start.Add(s.span(5.0/60, time.Minute))) {
		return
	}
	n := 0
	s.every(ctx, 2*time.Minute, func(ctx context.Context) {
		if s.isPaused("evidence") {
			return
		}
		n++
		tn := s.a
		if n%2 == 0 {
			tn = s.b
		}
		s.evidenceBundle(ctx, tn, soakHistory+n/2)
	})
}

// fleetLoad: a rollout of team c's 31-environment fleet every sixth of the
// day (at least every three minutes).
func (s *soakDay) fleetLoad(ctx context.Context) {
	s.ciBundle(ctx, s.c, s.c.fleet30)
	s.every(ctx, s.span(1.0/6, 3*time.Minute), func(ctx context.Context) {
		s.ciBundle(ctx, s.c, s.c.fleet30)
	})
}

// podinfoTags are the podinfo releases the kind node has, in turn.
var podinfoTags = []string{fixtures.V2, fixtures.V3, fixtures.V1}

// podinfoLoad: a podinfo release of team c every twelfth of the day (at
// least every four minutes), unless the rollback episode paused it.
func (s *soakDay) podinfoLoad(ctx context.Context) {
	n := 0
	s.every(ctx, s.span(1.0/12, 4*time.Minute), func(ctx context.Context) {
		if s.isPaused("podinfo") {
			return
		}
		tag := podinfoTags[n%len(podinfoTags)]
		n++
		s.create(ctx, "a podinfo release", func(ctx context.Context) error {
			_, err := s.imageBundle(ctx, s.c.ns, pipelineName, fixtures.Image, tag, "", "ci@team-c.example.com")
			return err
		})
	})
}

// rejecting reports whether the ticket bot may still reject: while the load
// runs and before the last sixth of the day, so every rejected version is
// superseded (a fix-forward version follows each rejection).
func (s *soakDay) rejecting(load context.Context) bool {
	return load.Err() == nil && time.Now().Before(s.at(5.0/6))
}

// ticketBot answers the fake ticketing API's changes as a change board
// would, every ten seconds: a change open for 15 seconds is approved, or
// one in ten rejected; a rejected change's Pipeline gets a fix-forward
// version a minute later, as its team would ship one.
func (s *soakDay) ticketBot(ctx context.Context) {
	seen := map[string]time.Time{}
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := time.Now()
		snow, err := s.e.FakeSnowTickets(ctx)
		if err == nil {
			for _, tk := range snow {
				if s.decided(tk.Number) {
					continue
				}
				if _, ok := seen[tk.Number]; !ok {
					seen[tk.Number] = now
				}
				if now.Sub(seen[tk.Number]) < 15*time.Second {
					continue
				}
				change := map[string]interface{}{"state": "-2", "approval": "approved",
					"approvers": []map[string]string{{"name": "Jane Smith", "state": "approved"}}}
				decision := "approved"
				if s.rejecting(s.loadCtx()) && s.rng.Intn(10) == 0 {
					change, decision = map[string]interface{}{"approval": "rejected"}, "rejected"
				}
				if err := s.e.UpdateFakeSnowTicket(ctx, tk.Number, change); err == nil {
					s.decide(ctx, tk.Number, decision, s.a, tk.Fields["short_description"])
				}
			}
		}
		jira, err := s.e.FakeJiraIssues(ctx)
		if err == nil {
			for _, is := range jira {
				if s.decided(is.Key) || is.Category == "done" {
					continue
				}
				if _, ok := seen[is.Key]; !ok {
					seen[is.Key] = now
				}
				if now.Sub(seen[is.Key]) < 15*time.Second {
					continue
				}
				change := map[string]interface{}{"status": "Approved", "category": "done", "resolution": ""}
				decision := "approved"
				if s.rejecting(s.loadCtx()) && s.rng.Intn(10) == 0 {
					change, decision = map[string]interface{}{"status": "Done", "category": "done", "resolution": "Won't Do"}, "rejected"
				}
				if err := s.e.UpdateFakeJiraIssue(ctx, is.Key, change); err == nil {
					s.decide(ctx, is.Key, decision, s.b, fmt.Sprint(is.Fields["summary"]))
				}
			}
		}
	}
}

// loadCtx is the load generators' context, done once the load stopped.
func (s *soakDay) loadCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load
}

// decided reports whether the ticket bot answered ticket.
func (s *soakDay) decided(ticket string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.tickets[ticket]
	return ok
}

// decide records the bot's answer; a rejection gets its Pipeline (named
// in the ticket's summary, "Promote <pipeline> ...") a fix-forward version.
func (s *soakDay) decide(ctx context.Context, ticket, decision string, tn *soakTenant, summary string) {
	s.mu.Lock()
	s.tickets[ticket] = decision
	if decision == "rejected" {
		s.rejected++
	}
	s.mu.Unlock()
	s.count("tickets."+decision, 1)
	if decision != "rejected" {
		return
	}
	f := strings.Fields(summary)
	if len(f) < 2 || !tn.owns(f[1]) {
		s.loadError("fix-forward after rejecting "+ticket, fmt.Errorf("no Pipeline of %s in its summary %q", tn.ns, summary))
		return
	}
	p := f[1]
	if s.isPaused("pipeline/" + p) {
		return // the episode that paused it creates its versions itself
	}
	// The bot's goroutine holds botsWG meanwhile, so Add is safe; the
	// version is created on the load's context, never after it stopped.
	s.botsWG.Add(1)
	go func() {
		defer s.botsWG.Done()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
		ctx := s.loadCtx()
		if ctx.Err() != nil {
			return
		}
		switch {
		case p == tn.evidence:
			s.evidenceBundle(ctx, tn, s.rng.Intn(len(tn.digests)))
		case tn == s.a && p == tn.agentPipeline:
			s.create(ctx, "the agent's fix-forward", func(ctx context.Context) error {
				_, err := s.agentShip(ctx)
				return err
			})
		default:
			s.ciBundle(ctx, tn, p)
		}
	}()
}

// approverBot is team a's approver: every ten seconds, each prod cab gate
// whose change request is approved gets the approver's approval naming the
// ticket (lifecycle.RecordApproval, what kardinal approve runs, as the
// approver).
func (s *soakDay) approverBot(ctx context.Context) {
	a := s.a
	as := s.asApprover
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		var gates v1alpha1.PolicyGateList
		if err := s.e.Client.List(ctx, &gates, client.InNamespace(a.ns), client.MatchingLabels{
			"kardinal.io/gate-template": "cab", "kardinal.io/environment": "prod"}); err != nil {
			continue
		}
		for _, g := range gates.Items {
			b := g.Labels["kardinal.io/bundle"]
			if g.Status.Ready || b == "" || s.wasApproved(b) {
				continue
			}
			var cr v1alpha1.ChangeRequest
			if err := s.e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: "prod-change-prod--" + b}, &cr); err != nil ||
				cr.Status.State != "approved" || cr.Status.Ticket == nil {
				continue
			}
			_, _, err := lifecycle.RecordApproval(ctx, as, lifecycle.ApprovalRequest{Namespace: a.ns, Bundle: b, Environment: "prod",
				User: soakApprover, Decision: lifecycle.DecisionApprove, Fields: map[string]string{"ticketID": cr.Status.Ticket.Key}})
			if err == nil {
				s.mu.Lock()
				s.approved[b] = true
				s.mu.Unlock()
				s.count("approvals", 1)
			}
		}
	}
}

func (s *soakDay) wasApproved(bundle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approved[bundle]
}

// stepVerifiedAt is when step ps turned Verified (zero if it did not).
func stepVerifiedAt(ps *v1alpha1.PromotionStep) time.Time {
	if ps.Status.State != "Verified" {
		return time.Time{}
	}
	if c := meta.FindStatusCondition(ps.Status.Conditions, "Verified"); c != nil {
		return c.LastTransitionTime.Time
	}
	if ps.Status.StateTransitionTime != nil {
		return ps.Status.StateTransitionTime.Time
	}
	return time.Time{}
}
