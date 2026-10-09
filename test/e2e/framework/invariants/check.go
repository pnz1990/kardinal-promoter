// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package invariants checks what must hold after any promotion load, however
// it was produced: every environment's git content is the image of the last
// Bundle verified there, no environment has two open PRs, no kardinal branch
// is left behind, every Bundle reached a terminal phase, no Graph is stuck,
// AuditEvents agree with the phases, the controller logged no data race,
// panic or unexpected error, and its metrics (reconcile errors, work queue
// depth, promotion latency, memory and goroutines) stayed sound. Each run
// writes a JSON report and a markdown summary.
//
// The scale suite (test/e2e/live/scale_*_test.go) runs Check at the end of
// every test; any live test can.
package invariants

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// Target is a Pipeline and the repo it writes.
type Target struct {
	Pipeline *v1alpha1.Pipeline
	Repo     gitserver.Repo
}

// Options says what Check checks.
type Options struct {
	// Namespace holds the Targets' Pipelines, Bundles and Graphs.
	Namespace string
	Targets   []Target
	// Image is the image repository the Bundles promote; env content is the
	// tag the env's kustomization.yaml pins it to.
	Image string
	// SeedTag is every environment's tag before any promotion.
	SeedTag string
	// Start is when the test began: the metrics window starts here.
	Start time.Time
	// Logs is the test's Collector; nil skips the log checks.
	Logs *Collector
	// Metrics turns on the Prometheus checks (the scale suite has
	// Prometheus; other suites may not).
	Metrics bool
	// RaceBuild is set when the controller is built with -race: its memory
	// growth threshold is wider (rssGrowthRace).
	RaceBuild bool
	// SharedController is set when other tests load the controller at the
	// same time: the work queue and goroutine checks then only report, as
	// neither drains nor stays flat for one test.
	SharedController bool
	// MaxReconcileErrorRatio is the highest share of reconciles that may end
	// in an error over the run (default 0.05). Chaos tests set it higher.
	MaxReconcileErrorRatio float64
	// Allow lists more benign error-log patterns, for faults the test
	// injects (a git outage makes clones fail).
	Allow []*regexp.Regexp
	// Extra are the test's own numbers, copied into the report.
	Extra map[string]interface{}
	// SLO, when set, fails the run when promotion latency exceeds it.
	SLO *SLO
	// AllowEmpty lets the namespace have no Bundle (a test deleted them).
	AllowEmpty bool
	// Outcome is the phases the test expects its Bundles to end in
	// (checkOutcome): by default the newest Bundle of each Pipeline Verified
	// and the others Verified or Superseded. A Failed Bundle passes only
	// with OutcomeAny, which needs OutcomeWhy.
	Outcome    Outcome
	OutcomeWhy string
	// Skip names checks not to run, each with the reason, for a test whose
	// own assertions replace them (the report lists them as skipped).
	Skip map[string]string
}

// Check runs every check, writes the report (Report.Write) and fails the
// test (t.Errorf) for each failed check.
func Check(t *testing.T, e *framework.Env, o Options) *Report {
	t.Helper()
	if o.MaxReconcileErrorRatio == 0 {
		o.MaxReconcileErrorRatio = 0.05
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	r := &Report{Test: t.Name(), Namespace: o.Namespace, Start: o.Start, End: time.Now(), Extra: o.Extra, skip: o.Skip}
	r.Duration = r.End.Sub(r.Start).Round(time.Second).String()

	st, err := load(ctx, e, o)
	if err != nil {
		r.add(Result{Name: "state", Violations: []string{err.Error()}})
	} else {
		// An empty run checks nothing: a test that meant to leave nothing
		// behind says so (AllowEmpty).
		var empty []string
		if len(o.Targets) == 0 {
			empty = append(empty, "no Pipeline to check")
		}
		if len(st.bundles) == 0 && !o.AllowEmpty {
			empty = append(empty, "no Bundle in "+o.Namespace)
		}
		r.add(Result{Name: "not-empty", Violations: empty})
		r.Bundles = PhaseCount(st.bundles)
		r.Steps = stepCount(st.steps)
		r.Latency = latency(st)
		if o.SLO != nil {
			r.add(checkSLO(st, o))
		}
		r.add(checkTerminal(st))
		r.add(checkOutcome(st, o))
		r.add(checkPhases(st))
		r.add(checkEnvContent(ctx, e, o, st))
		prs, branches := checkPRsAndBranches(ctx, e, o, st)
		r.add(prs)
		r.add(branches)
		g, info := checkGraphs(ctx, e, o, st)
		r.add(g)
		r.Graphs = info
		r.add(checkAudit(ctx, e, o, st))
	}
	if o.Logs != nil {
		r.Logs = o.Logs.Summary()
		r.add(checkLogs(r.Logs, o))
	}
	r.add(checkRestarts(ctx, e, o))
	r.add(checkKroRestarts(ctx, e, o))
	if o.Metrics {
		m, res := checkMetrics(ctx, e, o)
		r.Metrics = m
		for _, x := range res {
			r.add(x)
		}
	}
	r.Pass = true
	for _, c := range r.Checks {
		if !c.Pass {
			r.Pass = false
			t.Errorf("invariant %s failed:\n  %s", c.Name, strings.Join(limit(c.Violations, 20), "\n  "))
		}
	}
	r.Write(t)
	return r
}

// state is what the checks read once.
type state struct {
	bundles []v1alpha1.Bundle
	steps   []v1alpha1.PromotionStep
	byName  map[string]*v1alpha1.Bundle
	// stepsOf[bundle][env]
	stepsOf map[string]map[string]*v1alpha1.PromotionStep
}

func load(ctx context.Context, e *framework.Env, o Options) (*state, error) {
	st := &state{byName: map[string]*v1alpha1.Bundle{}, stepsOf: map[string]map[string]*v1alpha1.PromotionStep{}}
	var bl v1alpha1.BundleList
	if err := e.Client.List(ctx, &bl, client.InNamespace(o.Namespace)); err != nil {
		return nil, fmt.Errorf("list Bundles: %w", err)
	}
	var sl v1alpha1.PromotionStepList
	if err := e.Client.List(ctx, &sl, client.InNamespace(o.Namespace)); err != nil {
		return nil, fmt.Errorf("list PromotionSteps: %w", err)
	}
	st.bundles, st.steps = bl.Items, sl.Items
	for i := range st.bundles {
		st.byName[st.bundles[i].Name] = &st.bundles[i]
	}
	st.steps = append(st.steps, retiredSteps(st.bundles, st.steps)...)
	for i := range st.steps {
		s := &st.steps[i]
		b := s.Spec.BundleName
		if st.stepsOf[b] == nil {
			st.stepsOf[b] = map[string]*v1alpha1.PromotionStep{}
		}
		st.stepsOf[b][s.Spec.Environment] = s
	}
	return st, nil
}

// retiredSteps are the PromotionSteps of retired Graphs (#1492), rebuilt
// from their Bundles' status.retiredSteps: retirement deletes a finished
// Bundle's Graph and its steps, and the checks cover them all the same.
// A step that still exists is not added twice.
func retiredSteps(bundles []v1alpha1.Bundle, live []v1alpha1.PromotionStep) []v1alpha1.PromotionStep {
	seen := map[string]bool{}
	for i := range live {
		seen[live[i].Name] = true
	}
	var out []v1alpha1.PromotionStep
	for i := range bundles {
		b := &bundles[i]
		for _, r := range b.Status.RetiredSteps {
			if seen[r.Name] {
				continue
			}
			// The controller's own rebuild, so a retired step reads as the
			// CLI and the UI read it (its Verified time included).
			out = append(out, lifecycle.StepFromRetired(b, r))
		}
	}
	return out
}

func terminal(phase string) bool {
	return phase == "Verified" || phase == "Failed" || phase == "Superseded"
}

func terminalStep(state string) bool {
	switch state {
	case "Verified", "Failed", "AbortedByAlarm", "RollingBack", "Superseded":
		return true
	}
	return false
}

// PhaseCount counts Bundles by phase ("" counts as New).
func PhaseCount(bundles []v1alpha1.Bundle) map[string]int {
	out := map[string]int{}
	for _, b := range bundles {
		p := b.Status.Phase
		if p == "" {
			p = "New"
		}
		out[p]++
	}
	return out
}

func stepCount(steps []v1alpha1.PromotionStep) map[string]int {
	out := map[string]int{}
	for _, s := range steps {
		p := s.Status.State
		if p == "" {
			p = "Pending"
		}
		out[p]++
	}
	return out
}

// checkTerminal: every Bundle is Verified, Failed or Superseded.
func checkTerminal(st *state) Result {
	res := Result{Name: "bundles-terminal"}
	for _, b := range st.bundles {
		if !terminal(b.Status.Phase) {
			var where []string
			for env, s := range st.stepsOf[b.Name] {
				if !terminalStep(s.Status.State) {
					where = append(where, fmt.Sprintf("%s=%s (%s)", env, s.Status.State, trim(s.Status.Message, 120)))
				}
			}
			sort.Strings(where)
			res.Violations = append(res.Violations, fmt.Sprintf("Bundle %s (%s) is %q; open steps: %s",
				b.Name, b.Spec.Pipeline, b.Status.Phase, strings.Join(limit(where, 3), "; ")))
		}
	}
	res.Note = fmt.Sprintf("%d Bundles", len(st.bundles))
	return res
}

// Outcome is what a test expects its Bundles to end as.
type Outcome string

const (
	// OutcomeNewestVerified (the default): the newest Bundle of each
	// Pipeline (kardinal.io/created-at order) is Verified, every other one
	// Verified or Superseded. No Bundle Failed.
	OutcomeNewestVerified Outcome = ""
	// OutcomeAllVerified: every Bundle is Verified (one Bundle per Pipeline,
	// or Bundles promoted one after the other).
	OutcomeAllVerified Outcome = "all-verified"
	// OutcomeAny: any terminal phase, Failed included, for a test whose
	// faults can fail a Bundle on purpose; OutcomeWhy says why.
	OutcomeAny Outcome = "any"
)

// checkOutcome holds the Bundles to the test's Outcome. bundles-terminal
// counts Failed as terminal, and phase-consistency only checks the steps of
// each phase, so without it a test that expects success passed with Failed
// Bundles.
func checkOutcome(st *state, o Options) Result {
	res := Result{Name: "expected-outcome"}
	switch o.Outcome {
	case OutcomeAny:
		if o.OutcomeWhy == "" {
			res.Violations = append(res.Violations, "Outcome any needs OutcomeWhy: say why this test accepts Failed Bundles")
		}
		res.Note = "any terminal phase: " + o.OutcomeWhy
		return res
	case OutcomeAllVerified:
		for i := range st.bundles {
			b := &st.bundles[i]
			if b.Status.Phase != "Verified" {
				res.Violations = append(res.Violations, fmt.Sprintf("Bundle %s (%s) is %q, want Verified (%s)",
					b.Name, b.Spec.Pipeline, b.Status.Phase, bundleWhy(b)))
			}
		}
		res.Note = "every Bundle Verified"
		return res
	case OutcomeNewestVerified:
	default:
		res.Violations = append(res.Violations, fmt.Sprintf("unknown Outcome %q", o.Outcome))
		return res
	}
	newest := map[string]*v1alpha1.Bundle{}
	for i := range st.bundles {
		b := &st.bundles[i]
		if n := newest[b.Spec.Pipeline]; n == nil || lifecycle.CompareCreation(b, n) > 0 {
			newest[b.Spec.Pipeline] = b
		}
	}
	for i := range st.bundles {
		b := &st.bundles[i]
		switch {
		case newest[b.Spec.Pipeline] == b && b.Status.Phase != "Verified":
			res.Violations = append(res.Violations, fmt.Sprintf("Pipeline %s: its newest Bundle %s is %q, want Verified (%s)",
				b.Spec.Pipeline, b.Name, b.Status.Phase, bundleWhy(b)))
		case newest[b.Spec.Pipeline] != b && b.Status.Phase != "Verified" && b.Status.Phase != "Superseded":
			res.Violations = append(res.Violations, fmt.Sprintf("Bundle %s (%s) is %q, want Verified or Superseded (%s)",
				b.Name, b.Spec.Pipeline, b.Status.Phase, bundleWhy(b)))
		}
	}
	sort.Strings(res.Violations)
	res.Note = fmt.Sprintf("newest Bundle Verified on each of %d Pipelines, the others Verified or Superseded", len(newest))
	return res
}

// bundleWhy is the message of b's Ready condition, cut short.
func bundleWhy(b *v1alpha1.Bundle) string {
	for _, c := range b.Status.Conditions {
		if c.Type == "Ready" {
			return trim(c.Message, 160)
		}
	}
	return "no Ready condition"
}

// checkPhases: a Verified Bundle has every one of its steps Verified; a step
// of a Superseded or Failed Bundle is not left running.
func checkPhases(st *state) Result {
	res := Result{Name: "phase-consistency"}
	for _, b := range st.bundles {
		steps := st.stepsOf[b.Name]
		switch b.Status.Phase {
		case "Verified":
			for env, s := range steps {
				if s.Status.State != "Verified" {
					res.Violations = append(res.Violations, fmt.Sprintf("Bundle %s is Verified but its %s step is %q", b.Name, env, s.Status.State))
				}
			}
		case "Superseded", "Failed":
			for env, s := range steps {
				if s.Status.State == "Promoting" || s.Status.State == "HealthChecking" {
					res.Violations = append(res.Violations, fmt.Sprintf("Bundle %s is %s but its %s step is still %q",
						b.Name, b.Status.Phase, env, s.Status.State))
				}
			}
		}
	}
	return res
}

// verifiedAt is when step s became Verified, or zero.
func verifiedAt(s *v1alpha1.PromotionStep) time.Time {
	t, _ := lifecycle.VerifiedTime(s)
	return t
}

// envPath is the environment's directory in the repo.
func envPath(env v1alpha1.EnvironmentSpec) string {
	if env.Path != "" {
		return env.Path
	}
	return "environments/" + env.Name
}

// tagIn returns the tag kustomization pins image to.
func tagIn(kustomization []byte, image string) (string, error) {
	var k struct {
		Images []struct {
			Name    string `json:"name"`
			NewName string `json:"newName"`
			NewTag  string `json:"newTag"`
			Digest  string `json:"digest"`
		} `json:"images"`
	}
	if err := yaml.Unmarshal(kustomization, &k); err != nil {
		return "", err
	}
	for _, i := range k.Images {
		if i.Name == image || i.NewName == image {
			if i.Digest != "" {
				return i.Digest, nil
			}
			return i.NewTag, nil
		}
	}
	return "", fmt.Errorf("no images entry for %s", image)
}

func bundleTag(b *v1alpha1.Bundle, image string) string {
	for _, i := range b.Spec.Images {
		if i.Repository == image {
			if i.Digest != "" {
				return i.Digest
			}
			return i.Tag
		}
	}
	return ""
}

// checkEnvContent: each environment's kustomization pins the image to the
// tag of the Bundle most recently Verified there (SeedTag when none was).
// A tag of a Bundle whose step in that environment Failed is noted, not a
// violation: a failed promotion leaves its commit (docs/rollback.md).
func checkEnvContent(ctx context.Context, e *framework.Env, o Options, st *state) Result {
	res := Result{Name: "env-content"}
	type job struct {
		t   Target
		env v1alpha1.EnvironmentSpec
	}
	var jobs []job
	for _, t := range o.Targets {
		for _, env := range t.Pipeline.Spec.Environments {
			jobs = append(jobs, job{t, env})
		}
	}
	var mu sync.Mutex
	failedTag := map[string]bool{}
	for _, s := range st.steps {
		if s.Status.State == "Failed" {
			if b := st.byName[s.Spec.BundleName]; b != nil {
				failedTag[s.Spec.PipelineName+"/"+s.Spec.Environment+"/"+bundleTag(b, o.Image)] = true
			}
		}
	}
	checked, noted := 0, 0
	_ = parallel(8, len(jobs), func(i int) error {
		j := jobs[i]
		p := j.t.Pipeline.Name
		want, from := o.SeedTag, "the seed (no Bundle Verified)"
		var best time.Time
		for _, s := range st.steps {
			if s.Spec.PipelineName != p || s.Spec.Environment != j.env.Name || s.Status.State != "Verified" {
				continue
			}
			b := st.byName[s.Spec.BundleName]
			if b == nil {
				continue
			}
			if at := verifiedAt(&s); at.After(best) {
				best, want, from = at, bundleTag(b, o.Image), "Bundle "+b.Name+" (Verified "+at.UTC().Format(time.RFC3339)+")"
			}
		}
		raw, err := e.Git.ReadFile(ctx, j.t.Repo, j.t.Repo.Branch, envPath(j.env)+"/kustomization.yaml")
		mu.Lock()
		defer mu.Unlock()
		checked++
		if err != nil {
			res.Violations = append(res.Violations, fmt.Sprintf("%s/%s: read %s: %v", p, j.env.Name, envPath(j.env), err))
			return nil
		}
		got, err := tagIn(raw, o.Image)
		switch {
		case err != nil:
			res.Violations = append(res.Violations, fmt.Sprintf("%s/%s: %v", p, j.env.Name, err))
		case got == want:
		case failedTag[p+"/"+j.env.Name+"/"+got]:
			noted++
		default:
			res.Violations = append(res.Violations, fmt.Sprintf("%s/%s: git pins %s, want %s of %s", p, j.env.Name, got, want, from))
		}
		return nil
	})
	res.Note = fmt.Sprintf("%d environments checked; %d hold a Failed promotion's commit", checked, noted)
	return res
}

// prBranch splits a kardinal PR head branch kardinal/<bundle>/<env>.
var prBranch = regexp.MustCompile(`^kardinal/([^/]+)/([^/]+)$`)

// checkPRsAndBranches: per repo, at most one open PR per (pipeline,
// environment), none for a terminal or deleted Bundle, and every kardinal/
// branch has an open PR or a merged one.
func checkPRsAndBranches(ctx context.Context, e *framework.Env, o Options, st *state) (Result, Result) {
	prs := Result{Name: "no-duplicate-or-stale-prs"}
	br := Result{Name: "no-orphan-branches"}
	repos := map[string]gitserver.Repo{}
	for _, t := range o.Targets {
		repos[t.Repo.Owner+"/"+t.Repo.Name+"@"+t.Repo.Branch] = t.Repo
	}
	keys := make([]string, 0, len(repos))
	for k := range repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lister, canList := e.Git.(gitserver.BranchLister)
	var mu sync.Mutex
	open, total, branches := 0, 0, 0
	_ = parallel(8, len(keys), func(i int) error {
		repo := repos[keys[i]]
		list, err := e.Git.PullRequests(ctx, repo)
		if err != nil {
			mu.Lock()
			prs.Violations = append(prs.Violations, fmt.Sprintf("%s: list PRs: %v", repo.Name, err))
			mu.Unlock()
			return nil
		}
		perEnv := map[string][]int{}
		state := map[string]string{} // head branch -> open or merged (if any PR is)
		var v []string
		for _, pr := range list {
			if pr.State == "open" || pr.State == "merged" {
				state[pr.Head] = pr.State
			}
			if pr.State != "open" {
				continue
			}
			m := prBranch.FindStringSubmatch(pr.Head)
			if m == nil {
				continue
			}
			b := st.byName[m[1]]
			pipeline := "?"
			if b != nil {
				pipeline = b.Spec.Pipeline
			}
			key := pipeline + "/" + m[2]
			perEnv[key] = append(perEnv[key], pr.Number)
			switch {
			case b == nil:
				v = append(v, fmt.Sprintf("%s: PR #%d (%s) is open but Bundle %s is gone", repo.Name, pr.Number, pr.Head, m[1]))
			case terminal(b.Status.Phase):
				v = append(v, fmt.Sprintf("%s: PR #%d (%s) is open but Bundle %s is %s", repo.Name, pr.Number, pr.Head, m[1], b.Status.Phase))
			}
		}
		for key, nums := range perEnv {
			if len(nums) > 1 {
				sort.Ints(nums)
				v = append(v, fmt.Sprintf("%s: %d open PRs for %s: %v", repo.Name, len(nums), key, nums))
			}
		}
		var orphans []string
		nb := 0
		if canList {
			names, err := lister.Branches(ctx, repo)
			if err != nil {
				orphans = append(orphans, fmt.Sprintf("%s: list branches: %v", repo.Name, err))
			}
			for _, name := range names {
				m := prBranch.FindStringSubmatch(name)
				if m == nil {
					continue
				}
				nb++
				if state[name] != "" {
					continue
				}
				if b := st.byName[m[1]]; b != nil && !terminal(b.Status.Phase) {
					continue // the step may still open its PR
				}
				orphans = append(orphans, fmt.Sprintf("%s: branch %s has no open or merged PR", repo.Name, name))
			}
		}
		mu.Lock()
		defer mu.Unlock()
		total += len(list)
		for _, n := range perEnv {
			open += len(n)
		}
		branches += nb
		prs.Violations = append(prs.Violations, v...)
		br.Violations = append(br.Violations, orphans...)
		return nil
	})
	prs.Note = fmt.Sprintf("%d repos, %d PRs, %d open", len(keys), total, open)
	br.Note = fmt.Sprintf("%d kardinal branches", branches)
	if !canList {
		br.Note = e.Git.Kind() + " cannot list branches; not checked"
	}
	return prs, br
}

// GraphInfo is the size of the namespace's Graphs.
type GraphInfo struct {
	Count     int `json:"count"`
	MaxNodes  int `json:"maxNodes"`
	MaxBytes  int `json:"maxBytes"`
	Revisions int `json:"graphRevisions"`
}

// checkGraphs: every Graph belongs to an existing Bundle, none has been
// deleting for over three minutes, none has a condition reporting an error,
// and none is bigger than etcd's default 1.5 MiB request limit allows for.
func checkGraphs(ctx context.Context, e *framework.Env, o Options, st *state) (Result, GraphInfo) {
	res := Result{Name: "graphs-not-stuck"}
	var info GraphInfo
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(framework.GraphGVR.GroupVersion().WithKind("GraphList"))
	if err := e.Client.List(ctx, list, client.InNamespace(o.Namespace)); err != nil {
		res.Violations = append(res.Violations, "list Graphs: "+err.Error())
		return res, info
	}
	info.Count = len(list.Items)
	for _, g := range list.Items {
		raw, _ := json.Marshal(g.Object)
		if len(raw) > info.MaxBytes {
			info.MaxBytes = len(raw)
		}
		nodes, _, _ := unstructured.NestedSlice(g.Object, "spec", "nodes")
		if len(nodes) > info.MaxNodes {
			info.MaxNodes = len(nodes)
		}
		if len(raw) > 1<<20 {
			res.Violations = append(res.Violations, fmt.Sprintf("Graph %s is %d bytes, close to etcd's 1.5 MiB limit", g.GetName(), len(raw)))
		}
		bundle := g.GetLabels()["kardinal.io/bundle"]
		if del := g.GetDeletionTimestamp(); del != nil {
			if time.Since(del.Time) > 3*time.Minute {
				res.Violations = append(res.Violations, fmt.Sprintf("Graph %s has been deleting since %s (finalizers %v)",
					g.GetName(), del.UTC().Format(time.RFC3339), g.GetFinalizers()))
			}
			continue
		}
		if _, ok := st.byName[bundle]; !ok {
			res.Violations = append(res.Violations, fmt.Sprintf("Graph %s outlived its Bundle %s", g.GetName(), bundle))
		}
		conds, _, _ := unstructured.NestedSlice(g.Object, "status", "conditions")
		for _, c := range conds {
			m, _ := c.(map[string]interface{})
			reason, _ := m["reason"].(string)
			status, _ := m["status"].(string)
			msg, _ := m["message"].(string)
			if status == "False" && graphErrorReason(reason) {
				b := st.byName[bundle]
				phase := ""
				if b != nil {
					phase = b.Status.Phase
				}
				res.Violations = append(res.Violations, fmt.Sprintf("Graph %s (Bundle %s %s): %s=%s %s: %s",
					g.GetName(), bundle, phase, m["type"], status, reason, trim(msg, 200)))
			}
		}
	}
	revs := &unstructured.UnstructuredList{}
	revs.SetAPIVersion("internal.kro.run/v1alpha1")
	revs.SetKind("GraphRevisionList")
	if err := e.Client.List(ctx, revs, client.InNamespace(o.Namespace)); err == nil {
		info.Revisions = len(revs.Items)
	}
	res.Note = fmt.Sprintf("%d Graphs, at most %d nodes and %d bytes, %d GraphRevisions", info.Count, info.MaxNodes, info.MaxBytes, info.Revisions)
	return res, info
}

// graphErrorReason reports whether a kro Graph condition reason says the
// Graph cannot make progress (as opposed to waiting on a node).
func graphErrorReason(reason string) bool {
	r := strings.ToLower(reason)
	for _, s := range []string{"error", "invalid", "failed", "conflict", "forbidden"} {
		if strings.Contains(r, s) {
			return true
		}
	}
	return false
}

// checkAudit: each Verified step has its PromotionSucceeded AuditEvent and
// each Failed step a PromotionFailed one; no step that is not Verified has a
// PromotionSucceeded event; no (Bundle, environment) has two events of one
// action.
func checkAudit(ctx context.Context, e *framework.Env, o Options, st *state) Result {
	res := Result{Name: "audit-consistent"}
	var list v1alpha1.AuditEventList
	if err := e.Client.List(ctx, &list, client.InNamespace(o.Namespace)); err != nil {
		res.Violations = append(res.Violations, "list AuditEvents: "+err.Error())
		return res
	}
	count := map[string]int{}
	for _, a := range list.Items {
		count[a.Spec.BundleName+"/"+a.Spec.Environment+"/"+a.Spec.Action]++
	}
	for k, n := range count {
		// A gate instance records a GateEvaluated event each time its
		// result changes; a step records each Promotion and Rollback action once.
		if n > 1 && !strings.HasSuffix(k, "/GateEvaluated") {
			res.Violations = append(res.Violations, fmt.Sprintf("%d AuditEvents %s", n, k))
		}
	}
	for _, s := range st.steps {
		k := s.Spec.BundleName + "/" + s.Spec.Environment + "/"
		switch s.Status.State {
		case "Verified":
			if count[k+"PromotionSucceeded"] == 0 {
				res.Violations = append(res.Violations, fmt.Sprintf("step %s is Verified with no PromotionSucceeded AuditEvent", s.Name))
			}
		case "Failed":
			// A step of a superseded Bundle ends Failed with PromotionSuperseded;
			// one its Bundle was superseded before it started records nothing.
			if strings.Contains(s.Status.Message, "superseded before this step started") && count[k+"PromotionStarted"] == 0 {
				continue
			}
			if count[k+"PromotionFailed"] == 0 && count[k+"RollbackStarted"] == 0 && count[k+"PromotionSuperseded"] == 0 {
				res.Violations = append(res.Violations, fmt.Sprintf("step %s Failed with no PromotionFailed or PromotionSuperseded AuditEvent", s.Name))
			}
		default:
			if count[k+"PromotionSucceeded"] > 0 {
				res.Violations = append(res.Violations, fmt.Sprintf("step %s is %q but has a PromotionSucceeded AuditEvent", s.Name, s.Status.State))
			}
		}
	}
	res.Note = fmt.Sprintf("%d AuditEvents", len(list.Items))
	return res
}

// checkLogs: no data race, no panic (controller or kro), no unexpected
// error-level line.
func checkLogs(s LogSummary, o Options) Result {
	res := Result{Name: "controller-logs"}
	controllers := 0
	for _, p := range s.Pods {
		if !strings.HasPrefix(p, "kro-") {
			controllers++
		}
	}
	if controllers == 0 {
		res.Violations = append(res.Violations, "no controller container's log was read: nothing was checked")
	}
	for _, e := range s.StreamErrors {
		res.Violations = append(res.Violations, "log stream: "+e)
	}
	for _, b := range s.Races {
		res.Violations = append(res.Violations, fmt.Sprintf("DATA RACE in %s:\n    %s", b.Pod, strings.Join(limit(b.Lines, 30), "\n    ")))
	}
	for _, b := range s.Panics {
		res.Violations = append(res.Violations, fmt.Sprintf("panic in %s:\n    %s", b.Pod, strings.Join(limit(b.Lines, 30), "\n    ")))
	}
	for _, b := range s.KroPanics {
		res.Violations = append(res.Violations, fmt.Sprintf("kro panic in %s:\n    %s", b.Pod, strings.Join(limit(b.Lines, 30), "\n    ")))
	}
	for _, g := range s.Unexpected {
		all := g.Example.Logger + " " + g.Example.Message + " " + g.Example.Error
		allowed := false
		for _, re := range o.Allow {
			allowed = allowed || re.MatchString(all)
		}
		if !allowed {
			res.Violations = append(res.Violations, fmt.Sprintf("%dx error log %q (e.g. %s: %s)", g.Count, g.Key, g.Example.Pod, trim(g.Example.Error, 200)))
		}
	}
	res.Note = fmt.Sprintf("%d lines from %d containers, %d errors (%d benign), %d warnings", s.Lines, len(s.Pods), s.Errors, s.Benign, s.Warnings)
	return res
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func limit(s []string, n int) []string {
	if len(s) > n {
		return append(append([]string(nil), s[:n]...), fmt.Sprintf("... and %d more", len(s)-n))
	}
	return s
}

func parallel(workers, n int, fn func(i int) error) error {
	var wg sync.WaitGroup
	next := make(chan int)
	errs := make(chan error, n)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				if err := fn(i); err != nil {
					errs <- err
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
	close(errs)
	return <-errs
}

// artifacts is where reports go: <KARDINAL_E2E_ARTIFACTS>/scale.
func artifacts() string {
	dir := os.Getenv(framework.EnvArtifacts)
	if dir == "" {
		dir = filepath.Join("test", "e2e", "results")
	}
	return filepath.Join(dir, "scale")
}

// Dir is the directory a test's report and raw logs go to.
func Dir(t *testing.T) string {
	return filepath.Join(artifacts(), strings.ReplaceAll(t.Name(), "/", "_"))
}
