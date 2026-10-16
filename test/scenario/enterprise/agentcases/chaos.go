// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentcases

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise"
)

// chaosSize is the restart chaos: Teams teams (even ones review every
// promotion in a PR, odd ones push) with PipelinesPerTeam Pipelines, an
// agent per team shipping a version of each of its Pipelines per wave,
// Waves waves, and Seeds runs that each crash the controller Crashes times
// at random side effects. A reviewer merges a PR ReviewDelay rounds after it
// opens, or sooner (the seeds take turns).
type chaosSize struct{ Teams, PipelinesPerTeam, Waves, Seeds, Crashes, ReviewDelay int }

// quickChaos keeps go test ./... under its budget; fullChaos is the release
// gate's (KARDINAL_SCENARIO_ENTERPRISE=full). KARDINAL_CHAOS_SEEDS sets the
// number of seeds of either.
var (
	quickChaos = chaosSize{Teams: 2, PipelinesPerTeam: 1, Waves: 1, Seeds: 2, Crashes: 4, ReviewDelay: 2}
	fullChaos  = chaosSize{Teams: 4, PipelinesPerTeam: 2, Waves: 3, Seeds: 24, Crashes: 8, ReviewDelay: 2}
)

func chaosSizeFromEnv(t *testing.T) chaosSize {
	sz := quickChaos
	if os.Getenv("KARDINAL_SCENARIO_ENTERPRISE") == "full" {
		sz = fullChaos
	}
	if v := os.Getenv("KARDINAL_CHAOS_SEEDS"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err, "KARDINAL_CHAOS_SEEDS")
		sz.Seeds = n
	}
	return sz
}

// chaosRun is what one run of the waves left: the record the invariants
// compare with the run without crashes.
type chaosRun struct {
	effects int64
	log     []string
	crashes []string
	rounds  int
	// prs counts the PRs ever opened per repository and head branch, and
	// perPromotion per namespace/bundle/environment.
	prs          map[string]int
	perPromotion map[string]int
	// audit counts the AuditEvents per namespace/bundle/environment/action/
	// outcome; pending are the outbox entries left unwritten.
	audit   map[string]int
	pending []string
	// steps counts the PromotionSteps per namespace/bundle/environment;
	// noChanges are the ones that found nothing to commit.
	steps     map[string]int
	noChanges map[string]bool
	// commits counts the commits of every repository's main branch, and
	// promoted per namespace/pipeline/environment the image tags its
	// environment directory was set to, in order.
	commits  map[string]int
	promoted map[string][]string
	// verified: every version of every wave ended Verified.
	unverified []string
}

// RunChaos is the controller restarted mid-wave: the waves of agent-shipped
// versions run once without a crash, then once per seed with the controller
// killed right after Crashes side effects picked at random (a write to the
// API server, a request that changes something on the SCM, a git push; the
// effect is done, nothing after it is) and started again with nothing in
// memory. Every run ends with every version Verified everywhere, and none
// opens a PR twice (per head branch, or per version and environment), loses
// or duplicates an AuditEvent (the same events, per version, environment,
// action and outcome, as the run without crashes, and none left in an
// outbox), or promotes twice (one PromotionStep per version and environment,
// the commits of the run without crashes, each environment set to each
// version's tag once, in wave order).
func RunChaos(t *testing.T) {
	sz := chaosSizeFromEnv(t)
	// One run without a crash per review delay (the seeds take turns), and
	// then the runs with crashes, in parallel: each is a World of its own.
	bases := map[int]chaosRun{}
	for _, delay := range []int{0, sz.ReviewDelay} {
		start := time.Now()
		b := runChaos(t, sz, delay, nil)
		require.Positive(t, b.effects)
		require.Empty(t, b.unverified, "without a crash, every version is Verified")
		outside := 0
		for _, e := range b.log {
			if !strings.HasPrefix(e, "write ") {
				outside++
			}
		}
		t.Logf("without a crash, PRs merged %d rounds after they open: %d controller side effects (%d SCM requests and git pushes) "+
			"over %d waves, %d rounds, %d PRs, %d AuditEvents, in %s", delay, b.effects, outside, sz.Waves, b.rounds, len(b.prs),
			sum(b.audit), time.Since(start).Round(time.Millisecond))
		bases[delay] = b
	}
	t.Run("crashes", func(t *testing.T) {
		// The crashes most likely to repeat or lose something, each right
		// after the first of its kind: a PR opened, a branch pushed, an
		// AuditEvent written, a PR's status recorded, with the PR merged at
		// once and with it still open when the controller is back.
		for _, delay := range []int{0, sz.ReviewDelay} {
			t.Run(fmt.Sprintf("targeted-delay-%d", delay), func(t *testing.T) {
				t.Parallel()
				when := []func(string) bool{
					func(e string) bool { return strings.HasPrefix(e, "POST /repos/") && strings.HasSuffix(e, "/pulls") },
					func(e string) bool { return strings.HasPrefix(e, "git push ") },
					func(e string) bool { return strings.HasPrefix(e, "write AuditEvent ") },
					func(e string) bool { return strings.HasPrefix(e, "write PRStatus ") },
				}
				run := runChaosWhen(t, sz, delay, nil, when)
				t.Logf("crashes: %v", run.crashes)
				require.Len(t, run.crashes, len(when), "every crash happened")
				checkChaos(t, bases[delay], run)
			})
		}
		for seed := 1; seed <= sz.Seeds; seed++ {
			t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
				t.Parallel()
				// A PR merged at once, or still open when the controller
				// comes back.
				delay := seed % 2 * sz.ReviewDelay
				base := bases[delay]
				points := crashPoints(uint64(seed), base.log, sz.Crashes)
				run := runChaos(t, sz, delay, points)
				t.Logf("review delay %d, crashes at %v: %v; %d rounds (%d without)", delay, points, run.crashes, run.rounds, base.rounds)
				require.Len(t, run.crashes, len(points), "every crash happened")
				checkChaos(t, base, run)
			})
		}
	})
}

// crashPoints picks n distinct effect numbers in [1, len(log)], ascending:
// half of them among the SCM requests and git pushes of log (the effects a
// crash right after is likeliest to repeat: a PR opened or a commit pushed,
// not yet recorded), the others among every effect.
func crashPoints(seed uint64, log []string, n int) []int64 {
	rng := rand.New(rand.NewPCG(seed, 2035))
	var outside []int64
	for i, e := range log {
		if !strings.HasPrefix(e, "write ") {
			outside = append(outside, int64(i+1))
		}
	}
	picked := map[int64]bool{}
	for len(picked) < n/2 && len(picked) < len(outside) {
		picked[outside[rng.IntN(len(outside))]] = true
	}
	for len(picked) < n && len(picked) < len(log) {
		picked[1+rng.Int64N(int64(len(log)))] = true
	}
	out := make([]int64, 0, len(picked))
	for p := range picked {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// slowReviewer approves and merges an open PR delay rounds after it first
// saw it: a restarted controller then finds its PR still open, or merged
// by now, as each crash happens.
func slowReviewer(delay int) func(w *scenario.World) error {
	seen := map[string]int{}
	return func(w *scenario.World) error {
		for _, pr := range w.SCM.PRs() {
			if pr.State != "open" {
				continue
			}
			key := fmt.Sprintf("%s#%d", pr.Repo, pr.Number)
			if _, ok := seen[key]; !ok {
				seen[key] = w.Rounds()
			}
			if w.Rounds()-seen[key] < delay {
				continue
			}
			if err := w.SCM.Approve(pr.Repo, pr.Number, "reviewer"); err != nil {
				return err
			}
			if err := w.SCM.MergeAs(pr.Repo, pr.Number, "reviewer"); err != nil {
				return err
			}
		}
		return nil
	}
}

// chaosAgent is the agent that ships team ns's versions.
func chaosAgent(ns string) (*v1alpha1.AgentIdentity, scenario.Identity) {
	name := "shipper-" + ns
	subj := v1alpha1.AgentSubject{Kind: "ServiceAccount", Namespace: ns, Name: name}
	a := scenario.AgentIdentityOf(name, []v1alpha1.AgentSubject{subj}, []string{ns}, false)
	a.Spec.Sponsor.User = "sponsor@corp.example.com"
	return a, scenario.Identity{Username: actor.SubjectKey(subj), Groups: []string{"system:serviceaccounts", "system:authenticated"},
		Asserted: v1alpha1.AssertedActor{Model: "model-" + name, Session: "chaos"}}
}

func runChaos(t *testing.T, sz chaosSize, reviewDelay int, points []int64) chaosRun {
	t.Helper()
	return runChaosWhen(t, sz, reviewDelay, points, nil)
}

// runChaosWhen is runChaos crashing after the effects when picks
// (World.CrashWhen) instead, when not nil.
func runChaosWhen(t *testing.T, sz chaosSize, reviewDelay int, points []int64, when []func(string) bool) chaosRun {
	t.Helper()
	w := scenario.NewWorld(t, scenario.Options{PolicyNamespaces: []string{scenario.PolicyNS}, Watch: true, Tick: 30 * time.Second,
		Actors: true})
	require.NoError(t, w.Client.Create(w.Ctx, scenario.TenantObjects(scenario.PolicyNS)[0]))
	w.MustApply(enterprise.OrgGates()...)
	agents := map[string]scenario.Identity{}
	for i := 1; i <= sz.Teams; i++ {
		ns := enterprise.Team(i)
		enterprise.Tenant(t, w, ns, sz.PipelinesPerTeam, i%2 == 0)
		a, id := chaosAgent(ns)
		w.MustApply(a)
		agents[ns] = id
	}
	require.True(t, w.RunUntil(20, func() bool { return w.Round() == 0 }), "the tenants settle")
	w.Actors = append(w.Actors, slowReviewer(reviewDelay), approveAs)

	if when != nil {
		w.CrashWhen(when...)
	} else {
		w.CrashAfter(points...)
	}
	var run chaosRun
	for wave := 1; wave <= sz.Waves; wave++ {
		var this []string
		for i := 1; i <= sz.Teams; i++ {
			ns := enterprise.Team(i)
			for j := 1; j <= sz.PipelinesPerTeam; j++ {
				b := scenario.ImageBundle(ns, enterprise.PipelineName(j), fmt.Sprintf("1.%d.0", wave))
				cliErr, refused, err := w.CreateBundle(agents[ns], b)
				require.NoError(t, err)
				require.NoError(t, cliErr)
				require.Empty(t, refused)
				this = append(this, ns+"/"+b.Name)
			}
		}
		verified := func() bool {
			for _, key := range this {
				ns, name, _ := strings.Cut(key, "/")
				if w.Bundle(ns, name).Status.Phase != "Verified" {
					return false
				}
			}
			return true
		}
		if !w.RunUntil(200, verified) {
			for _, key := range this {
				ns, name, _ := strings.Cut(key, "/")
				if ph := w.Bundle(ns, name).Status.Phase; ph != "Verified" {
					run.unverified = append(run.unverified, key+"="+ph)
				}
			}
			t.Errorf("wave %d: not Verified after 200 rounds: %v (crashes %v)\n%s", wave, run.unverified, w.Crashes(),
				w.Describe(strings.SplitN(run.unverified[0], "/", 2)[0]))
			break
		}
	}
	// The writes stop: the outboxes drain.
	w.RunUntil(20, func() bool { return w.Round() == 0 })
	run.effects, run.log, run.crashes, run.rounds = w.Effects(), w.EffectLog(), w.Crashes(), w.Rounds()
	run.prs, run.perPromotion = map[string]int{}, map[string]int{}
	for _, pr := range w.SCM.PRs() {
		run.prs[pr.Repo+" "+pr.Head]++
		// kardinal/<hash>/<bundle>/<environment>
		if parts := strings.Split(pr.Head, "/"); len(parts) == 4 {
			run.perPromotion[pr.Repo+" "+parts[2]+"/"+parts[3]]++
		}
	}
	run.audit = map[string]int{}
	var events v1alpha1.AuditEventList
	require.NoError(t, w.Client.List(w.Ctx, &events))
	for _, e := range events.Items {
		if e.Spec.Action == "APIWrite" {
			continue // the API write log samples writes, by design
		}
		if e.Spec.Action == "GateEvaluated" && e.Spec.Outcome == "Failure" {
			// A gate records its first evaluation and every flip. A gate
			// first evaluated after a restart can already pass (its soak
			// is over, its approval recorded): its Failure record is not
			// lost, it never was. Its Success is compared.
			continue
		}
		run.audit[fmt.Sprintf("%s/%s/%s %s %s", e.Namespace, e.Spec.BundleName, e.Spec.Environment, e.Spec.Action, e.Spec.Outcome)]++
	}
	run.pending = pendingAuditEvents(t, w)
	run.steps, run.noChanges = map[string]int{}, map[string]bool{}
	var steps v1alpha1.PromotionStepList
	require.NoError(t, w.Client.List(w.Ctx, &steps))
	for _, s := range steps.Items {
		run.steps[s.Namespace+"/"+s.Spec.BundleName+"/"+s.Spec.Environment]++
		if s.Status.Outputs["noChanges"] == "true" {
			run.noChanges[s.Namespace+"/"+s.Spec.BundleName+"/"+s.Spec.Environment] = true
		}
	}
	run.commits, run.promoted = map[string]int{}, map[string][]string{}
	for i := 1; i <= sz.Teams; i++ {
		for j := 1; j <= sz.PipelinesPerTeam; j++ {
			ns, p := enterprise.Team(i), enterprise.PipelineName(j)
			n, tags := gitHistory(t, w, enterprise.RepoURL(ns, p))
			run.commits[ns+"/"+p] = n
			for env, ts := range tags {
				run.promoted[ns+"/"+p+"/"+env] = ts
			}
		}
	}
	n, last := w.Errors()
	if len(points) == 0 && when == nil {
		assert.Empty(t, n, "reconcile errors without a crash: %v", last)
	}
	assert.Empty(t, w.Kro.Errors())
	assert.Empty(t, w.SCM.Unknown())
	return run
}

// pendingAuditEvents are the outbox entries still unwritten in any status.
func pendingAuditEvents(t *testing.T, w *scenario.World) []string {
	t.Helper()
	var out []string
	add := func(kind string, o client.Object, p []v1alpha1.PendingAuditEvent) {
		for _, e := range p {
			out = append(out, kind+" "+o.GetNamespace()+"/"+o.GetName()+": "+e.Name)
		}
	}
	var bundles v1alpha1.BundleList
	require.NoError(t, w.Client.List(w.Ctx, &bundles))
	for i := range bundles.Items {
		add("Bundle", &bundles.Items[i], bundles.Items[i].Status.PendingAuditEvents)
	}
	var steps v1alpha1.PromotionStepList
	require.NoError(t, w.Client.List(w.Ctx, &steps))
	for i := range steps.Items {
		add("PromotionStep", &steps.Items[i], steps.Items[i].Status.PendingAuditEvents)
	}
	var pipelines v1alpha1.PipelineList
	require.NoError(t, w.Client.List(w.Ctx, &pipelines))
	for i := range pipelines.Items {
		add("Pipeline", &pipelines.Items[i], pipelines.Items[i].Status.PendingAuditEvents)
	}
	var gates v1alpha1.PolicyGateList
	require.NoError(t, w.Client.List(w.Ctx, &gates))
	for i := range gates.Items {
		add("PolicyGate", &gates.Items[i], gates.Items[i].Status.PendingAuditEvents)
	}
	sort.Strings(out)
	return out
}

// gitHistory counts the commits of url's main branch and lists, per
// environment, the image tags its kustomization was set to, oldest first.
func gitHistory(t *testing.T, w *scenario.World, url string) (int, map[string][]string) {
	t.Helper()
	dir, ok := w.Git.Path(url)
	require.True(t, ok, url)
	repo, err := gogit.PlainOpen(dir)
	require.NoError(t, err)
	head, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	require.NoError(t, err)
	iter, err := repo.Log(&gogit.LogOptions{From: head.Hash(), Order: gogit.LogOrderCommitterTime})
	require.NoError(t, err)
	var commits []*object.Commit
	require.NoError(t, iter.ForEach(func(c *object.Commit) error {
		commits = append(commits, c)
		return nil
	}))
	tags := map[string][]string{}
	last := map[string]string{}
	for i := len(commits) - 1; i >= 0; i-- {
		tree, err := commits[i].Tree()
		require.NoError(t, err)
		for _, env := range []string{"dev", "staging", "prod"} {
			f, err := tree.File("environments/" + env + "/kustomization.yaml")
			if err != nil {
				continue
			}
			s, err := f.Contents()
			require.NoError(t, err)
			tag := ""
			for _, line := range strings.Split(s, "\n") {
				if v, ok := strings.CutPrefix(strings.TrimSpace(line), "newTag: "); ok {
					tag = v
				}
			}
			if tag != last[env] {
				tags[env] = append(tags[env], tag)
				last[env] = tag
			}
		}
	}
	return len(commits), tags
}

func checkChaos(t *testing.T, base, run chaosRun) {
	t.Helper()
	assert.Empty(t, run.unverified, "every version ends Verified everywhere")
	for k, n := range run.prs {
		assert.Equal(t, 1, n, "PR head %s opened %d times", k, n)
	}
	for k, n := range run.perPromotion {
		assert.Equal(t, 1, n, "%s: %d PRs for one promotion", k, n)
	}
	assert.Equal(t, base.perPromotion, run.perPromotion, "the PRs of the run without crashes")
	want := maps.Clone(base.audit)
	for k := range base.audit {
		// #2065: a PR merged before the crashed controller recorded it is
		// never recorded; the restarted step finds the change on the base
		// branch (noChanges). Remove this when #2065 is fixed.
		if promotion, ok := strings.CutSuffix(k, " PRMerged Success"); ok && run.audit[k] == 0 && run.noChanges[promotion] {
			t.Logf("known #2065: %s has no PRMerged record", promotion)
			delete(want, k)
		}
	}
	assert.Equal(t, want, run.audit, "the AuditEvents of the run without crashes: none lost, none twice")
	assert.Empty(t, run.pending, "no AuditEvent left in an outbox")
	for k, n := range run.steps {
		assert.Equal(t, 1, n, "%s: %d PromotionSteps", k, n)
	}
	assert.Equal(t, base.steps, run.steps)
	assert.Equal(t, base.commits, run.commits, "the commits of the run without crashes: no promotion twice")
	assert.Equal(t, base.promoted, run.promoted, "each environment set to each version once, in order")
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
