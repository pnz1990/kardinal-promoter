//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// The races: operators, CI and the SCM acting on a Pipeline while its
// Bundles are in flight. Each test ends with the invariants.

// TestScale_RaceRapidFire creates the profile's RapidFire Bundles on one
// Pipeline one after another, as fast as the API takes them: the newest
// must end Verified everywhere, every other one Superseded (or Verified,
// had it finished first), and git must hold the newest.
// Covers SCALE-RACE-RAPIDFIRE-01.
func TestScale_RaceRapidFire(t *testing.T) {
	t.Parallel()
	r := scale.Begin(t)
	p := r.Fleet.Pipeline(t, "rapid", scale.Chain(6))
	var last scale.Created
	for i := 1; i <= r.P.RapidFire; i++ {
		last = r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, i))
	}
	r.Note("bundles", r.P.RapidFire)
	r.Finish()
	r.E.WaitBundlePhase(t, r.Fleet.NS, last.Name, "Verified", time.Minute)
}

// TestScale_RacePipelineEdit edits the Pipeline every few seconds while
// Bundles are in flight: an environment added at the end and removed again,
// one approval switched to pr-review and back, the health timeout changed.
// A reviewer merges any PR. After the edits stop, one more Bundle must go
// through the final Pipeline. Covers SCALE-RACE-PIPELINEEDIT-01.
func TestScale_RacePipelineEdit(t *testing.T) {
	t.Parallel()
	r := scale.Begin(t)
	envs := scale.Chain(8)
	extra := v1alpha1.EnvironmentSpec{Name: "s009"}
	all := append(append([]v1alpha1.EnvironmentSpec(nil), envs...), extra)
	repo := r.Fleet.Repo(t, r.Fleet.NS+"-edit", map[string][]string{"edit": scale.Names(all)})
	p := r.Fleet.PipelineSpec("edit", repo, envs)
	r.Fleet.Apply(t, p, repo)
	rev := scale.Review(t, r.E.Git, nil, repo)
	defer rev.Stop()

	ctx := context.Background()
	edits := 0
	for i := 1; i <= 4; i++ {
		r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, i))
		for j := 0; j < 4; j++ {
			edits++
			edit := edits
			editPipeline(t, r, p.Name, func(spec *v1alpha1.PipelineSpec) {
				switch edit % 4 {
				case 0:
					if len(spec.Environments) == 8 {
						spec.Environments = append(spec.Environments, r.Fleet.PipelineSpec("edit", repo, []v1alpha1.EnvironmentSpec{extra}).Spec.Environments[0])
					} else {
						spec.Environments = spec.Environments[:8]
					}
				case 1:
					a := &spec.Environments[4]
					if a.Approval == "auto" {
						a.Approval = "pr-review"
					} else {
						a.Approval = "auto"
					}
				case 2:
					spec.Environments[2].Health.Timeout = fmt.Sprintf("%dm", 4+edit%3)
				case 3:
					spec.HistoryLimit = 40 + edit%5
				}
			})
			time.Sleep(time.Duration(1500+rand.Intn(2000)) * time.Millisecond)
		}
	}
	// The final shape: eight environments, all auto.
	editPipeline(t, r, p.Name, func(spec *v1alpha1.PipelineSpec) {
		spec.Environments = spec.Environments[:8]
		spec.Environments[4].Approval = "auto"
	})
	r.Fleet.WaitSettled(t, r.P.Settle)
	final := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 99))
	r.Note("edits", edits)
	r.Finish()
	r.E.WaitBundlePhase(t, r.Fleet.NS, final.Name, "Verified", time.Minute)
	_ = ctx
}

// editPipeline applies edit to the Pipeline's spec with an optimistic-lock
// update, retrying on conflict.
func editPipeline(t *testing.T, r *scale.Run, name string, edit func(*v1alpha1.PipelineSpec)) {
	t.Helper()
	ctx := context.Background()
	for try := 0; try < 10; try++ {
		var p v1alpha1.Pipeline
		if err := r.E.Client.Get(ctx, types.NamespacedName{Namespace: r.Fleet.NS, Name: name}, &p); err != nil {
			t.Fatalf("get Pipeline %s: %v", name, err)
		}
		edit(&p.Spec)
		err := r.E.Client.Update(ctx, &p)
		if err == nil {
			return
		}
		if !strings.Contains(err.Error(), "the object has been modified") {
			t.Fatalf("update Pipeline %s: %v", name, err)
		}
	}
	t.Fatalf("update Pipeline %s: conflicts on every try", name)
}

// TestScale_RaceGateFlap opens and closes a PolicyGate on the middle of a
// chain every two seconds (a Bundle label the gate reads) while Bundles
// promote, then leaves it open: every Bundle must settle and the
// environments behind the gate must hold the last Bundle verified there.
// Covers SCALE-RACE-GATEFLAP-01.
func TestScale_RaceGateFlap(t *testing.T) {
	t.Parallel()
	r := scale.Begin(t)
	p := r.Fleet.Pipeline(t, "flap", scale.Chain(5))
	r.E.CreateGate(t, framework.Gate(r.Fleet.NS, "flapper", "s003", `"e2e-open" in bundle.labels`, "10s"))
	var bundles []string
	for i := 1; i <= 3; i++ {
		bundles = append(bundles, r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, i)).Name)
	}
	flips := 0
	deadline := time.Now().Add(time.Minute)
	for open := true; time.Now().Before(deadline); open = !open {
		for _, b := range bundles {
			setBundleLabel(t, r, b, "e2e-open", open)
		}
		flips++
		time.Sleep(2 * time.Second)
	}
	for _, b := range bundles {
		setBundleLabel(t, r, b, "e2e-open", true)
	}
	r.Note("flips", flips)
	r.Finish()
}

// setBundleLabel sets or removes label key on a Bundle; a Bundle already
// deleted (history limit) is skipped.
func setBundleLabel(t *testing.T, r *scale.Run, bundle, key string, on bool) {
	t.Helper()
	var v interface{}
	if on {
		v = "true"
	}
	patch, _ := json.Marshal(map[string]interface{}{"metadata": map[string]interface{}{"labels": map[string]interface{}{key: v}}})
	b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Namespace: r.Fleet.NS, Name: bundle}}
	if err := r.E.Client.Patch(context.Background(), b, client.RawPatch(types.MergePatchType, patch)); err != nil &&
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("label Bundle %s: %v", bundle, err)
	}
}

// TestScale_RaceChangeWindowFlip gates prod (pr-review) on a blackout
// ChangeWindow. With the window off, the first Bundle's prod PR opens; the
// window is switched on while that step waits for merge, and the PR is
// merged during the blackout. A second Bundle created during the blackout
// must get no prod step until the window is switched off again; then it
// promotes. Covers SCALE-RACE-CHANGEWINDOW-01.
func TestScale_RaceChangeWindowFlip(t *testing.T) {
	t.Parallel()
	r := scale.Begin(t)
	window := r.Fleet.NS + "-freeze"
	now := time.Now().UTC().Truncate(time.Second)
	r.E.CreateChangeWindow(t, blackout(window, now.Add(time.Hour), now.Add(2*time.Hour)))
	envs := scale.Chain(3)
	envs[2].Name, envs[2].Approval = "prod", "pr-review"
	p := r.Fleet.Pipeline(t, "cw", envs)
	repo := r.Fleet.Targets()[0].Repo
	r.E.CreateGate(t, framework.Gate(r.Fleet.NS, "no-freeze", "prod", fmt.Sprintf(`!changewindow.isBlocked(%q)`, window), "10s"))

	b1 := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 1))
	r.E.WaitStepState(t, r.Fleet.NS, p.Name, b1.Name, "prod", "WaitingForMerge", 5*time.Minute)
	pr := r.E.WaitPR(t, repo, time.Minute, "b1's prod PR", func(pr gitserver.PR) bool { return pr.State == "open" })

	// Blackout on, while prod waits for merge.
	editWindow(t, r.E, window, func(s *v1alpha1.ChangeWindowSpec) { s.Start = metav1.NewTime(time.Now().UTC().Add(-time.Minute)) })
	r.E.WaitChangeWindow(t, window, time.Minute, "active", func(cw *v1alpha1.ChangeWindow) bool { return cw.Status.Active })
	if err := r.E.Git.MergePR(context.Background(), repo, pr.Number); err != nil {
		t.Fatalf("merge PR #%d: %v", pr.Number, err)
	}
	b2 := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 2))
	r.E.WaitStepState(t, r.Fleet.NS, p.Name, b2.Name, "s002", "Verified", 5*time.Minute)
	r.E.NoStep(t, r.Fleet.NS, p.Name, b2.Name, "prod", 25*time.Second)

	// Blackout off: b2 promotes to prod through its PR.
	editWindow(t, r.E, window, func(s *v1alpha1.ChangeWindowSpec) {
		s.End = metav1.NewTime(time.Now().UTC().Truncate(time.Second).Add(-time.Second))
	})
	rev := scale.Review(t, r.E.Git, nil, repo)
	r.Fleet.WaitSettled(t, r.P.Settle)
	rev.Stop()
	r.Note("b1", b1.Name)
	r.Finish()
}

// TestScale_RacePauseStorm pauses and resumes five Pipelines every one to
// three seconds for a minute while their Bundles promote, then resumes them
// for good. Covers SCALE-RACE-PAUSESTORM-01.
func TestScale_RacePauseStorm(t *testing.T) {
	t.Parallel()
	r := scale.Begin(t)
	names := r.Fleet.Pipelines(t, "pause", 5, scale.Chain(4))
	for i := 1; i <= 3; i++ {
		for _, n := range names {
			r.Fleet.MustCreateBundle(t, n, scale.Tag(n, i))
		}
	}
	toggles := 0
	deadline := time.Now().Add(time.Minute)
	for paused := true; time.Now().Before(deadline); paused = !paused {
		for _, n := range names {
			r.E.SetPipelinePaused(t, r.Fleet.NS, n, paused)
			toggles++
		}
		time.Sleep(time.Duration(1000+rand.Intn(2000)) * time.Millisecond)
	}
	for _, n := range names {
		r.E.SetPipelinePaused(t, r.Fleet.NS, n, false)
	}
	r.Note("toggles", toggles)
	r.Finish()
}

// TestScale_RaceRollbackDuringPromotion verifies a first Bundle everywhere,
// starts a second, and while the second is mid-chain rolls the middle
// environment back (kardinal rollback). Covers SCALE-RACE-ROLLBACK-01.
func TestScale_RaceRollbackDuringPromotion(t *testing.T) {
	t.Parallel()
	r := scale.Begin(t)
	p := r.Fleet.Pipeline(t, "rb", scale.Chain(6))
	b1 := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 1))
	r.E.WaitBundlePhase(t, r.Fleet.NS, b1.Name, "Verified", 5*time.Minute)
	b2 := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 2))
	r.E.WaitStepState(t, r.Fleet.NS, p.Name, b2.Name, "s003", "Verified", 5*time.Minute)
	out := r.E.MustKardinal(t, r.Fleet.NS, "rollback", p.Name, "--env", "s003")
	r.Note("rollback", strings.TrimSpace(out))
	r.Finish()
}

// TestScale_RaceExternalPR acts on kardinal's PRs from outside: a PR closed
// and reopened at once, and the PR of a Bundle being superseded merged by a
// person right as the newer Bundle arrives. Covers SCALE-RACE-EXTERNALPR-01.
func TestScale_RaceExternalPR(t *testing.T) {
	t.Parallel()
	r := scale.Begin(t)
	envs := scale.Chain(2)
	envs[1].Name, envs[1].Approval = "prod", "pr-review"
	p := r.Fleet.Pipeline(t, "ext", envs)
	repo := r.Fleet.Targets()[0].Repo
	ctx := context.Background()

	b1 := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 1))
	r.E.WaitStepState(t, r.Fleet.NS, p.Name, b1.Name, "prod", "WaitingForMerge", 5*time.Minute)
	pr := r.E.WaitPR(t, repo, time.Minute, "b1's PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	if err := r.E.Git.ClosePR(ctx, repo, pr.Number); err != nil {
		t.Fatal(err)
	}
	if err := r.E.Git.ReopenPR(ctx, repo, pr.Number); err != nil {
		t.Fatal(err)
	}
	if err := r.E.Git.MergePR(ctx, repo, pr.Number); err != nil {
		t.Fatal(err)
	}
	r.E.WaitBundlePhase(t, r.Fleet.NS, b1.Name, "Verified", 5*time.Minute)

	// b2 waits for merge; b3 supersedes it while a person merges b2's PR.
	b2 := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 2))
	r.E.WaitStepState(t, r.Fleet.NS, p.Name, b2.Name, "prod", "WaitingForMerge", 5*time.Minute)
	pr2 := r.E.WaitPR(t, repo, time.Minute, "b2's PR", func(pr gitserver.PR) bool {
		return pr.State == "open" && strings.Contains(pr.Head, b2.Name)
	})
	r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 3))
	mergeErr := r.E.Git.MergePR(ctx, repo, pr2.Number)
	r.Note("supersededPRMerge", fmt.Sprintf("PR #%d: %v", pr2.Number, mergeErr))
	rev := scale.Review(t, r.E.Git, nil, repo)
	r.Fleet.WaitSettled(t, r.P.Settle)
	rev.Stop()
	r.Finish()
}

// TestScale_RaceForcePush force-pushes the Pipeline's branch back to its
// first commit while a Bundle promotes (history rewritten under kardinal's
// clones), then a new Bundle must promote everywhere on the rewritten
// branch. Covers SCALE-RACE-FORCEPUSH-01.
func TestScale_RaceForcePush(t *testing.T) {
	t.Parallel()
	r := scale.Begin(t)
	p := r.Fleet.Pipeline(t, "force", scale.Chain(6))
	repo := r.Fleet.Targets()[0].Repo
	seed, err := scale.Head(r.E, repo)
	if err != nil {
		t.Fatal(err)
	}
	b1 := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 1))
	r.E.WaitBundlePhase(t, r.Fleet.NS, b1.Name, "Verified", 5*time.Minute)
	b2 := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 2))
	r.E.WaitStepState(t, r.Fleet.NS, p.Name, b2.Name, "s002", "Verified", 5*time.Minute)
	scale.ForcePush(t, r.E, repo, seed)
	r.Fleet.WaitSettled(t, r.P.Settle)
	b3 := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 3))
	r.Finish()
	r.E.WaitBundlePhase(t, r.Fleet.NS, b3.Name, "Verified", time.Minute)
}

// TestScale_RaceNamespaceDelete deletes a namespace whose Bundles are in
// flight, with PRs open: the namespace must be gone within three minutes,
// and kardinal must leave no open PR and no branch behind in its repo.
// Covers SCALE-RACE-NSDELETE-01.
func TestScale_RaceNamespaceDelete(t *testing.T) {
	t.Parallel()
	r := scale.Begin(t)
	envs := scale.Chain(4)
	envs[3].Approval = "pr-review"
	names := r.Fleet.Pipelines(t, "doomed", 3, envs)
	for _, n := range names {
		r.Fleet.MustCreateBundle(t, n, scale.Tag(n, 1))
	}
	targets := r.Fleet.Targets()
	framework.Eventually(t, 5*time.Minute, "a PR open on every repo", func(ctx context.Context) (bool, string) {
		open := 0
		for _, tg := range targets {
			prs, err := r.E.Git.PullRequests(ctx, tg.Repo)
			if err != nil {
				return false, err.Error()
			}
			for _, pr := range prs {
				if pr.State == "open" {
					open++
					break
				}
			}
		}
		return open == len(targets), fmt.Sprintf("%d of %d repos have an open PR", open, len(targets))
	})
	for _, n := range names {
		r.Fleet.MustCreateBundle(t, n, scale.Tag(n, 2))
	}
	start := time.Now()
	scale.DeleteNamespace(t, r.E, r.Fleet.NS, 3*time.Minute)
	r.Note("namespaceGoneSeconds", int(time.Since(start).Seconds()))
	scale.NoKardinalLeftovers(t, r.E, targets, 3*time.Minute)
	r.Note("pipelines", len(names))
	r.Finish(scale.Skip("the namespace and its Pipelines are deleted", "env-content"))
}

// TestScale_RaceWebhooks sends the controller SCM webhooks a git server can
// send but should not change anything: a forged "merged" for a PR that is
// open, a merge event delivered ten times, events out of order (opened after
// merged), and events for PRs that do not exist. Only the real merge may
// advance the step. Covers SCALE-RACE-WEBHOOK-01.
func TestScale_RaceWebhooks(t *testing.T) {
	t.Parallel()
	r := scale.Begin(t)
	envs := scale.Chain(2)
	envs[1].Name, envs[1].Approval = "prod", "pr-review"
	p := r.Fleet.Pipeline(t, "hooks", envs)
	repo := r.Fleet.Targets()[0].Repo
	b := r.Fleet.MustCreateBundle(t, p.Name, scale.Tag(p.Name, 1))
	r.E.WaitStepState(t, r.Fleet.NS, p.Name, b.Name, "prod", "WaitingForMerge", 5*time.Minute)
	pr := r.E.WaitPR(t, repo, time.Minute, "prod PR", func(pr gitserver.PR) bool { return pr.State == "open" })

	full := repo.Owner + "/" + repo.Name
	send := func(action string, number int, merged bool) {
		body := scale.ForgejoPREvent(full, number, action, merged)
		if code := r.E.PostSCMWebhook(t, scale.ForgejoHeaders(body), body); code != 204 {
			t.Errorf("webhook %s #%d merged=%v: HTTP %d, want 204", action, number, merged, code)
		}
	}
	for i := 0; i < 5; i++ {
		send("closed", pr.Number, true) // forged: the PR is open
		send("closed", pr.Number+1000, true)
		send("opened", pr.Number, false)
	}
	framework.Consistently(t, 15*time.Second, "prod waits for the real merge", func(ctx context.Context) (bool, string) {
		ps, _, err := r.E.Step(ctx, r.Fleet.NS, p.Name, b.Name, "prod")
		if err != nil || ps == nil {
			return false, fmt.Sprint("step lookup: ", err)
		}
		return ps.Status.State == "WaitingForMerge", ps.Status.State
	})
	if err := r.E.Git.MergePR(context.Background(), repo, pr.Number); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		send("closed", pr.Number, true)
	}
	send("opened", pr.Number, false)
	send("reopened", pr.Number, false)
	r.E.WaitBundlePhase(t, r.Fleet.NS, b.Name, "Verified", 5*time.Minute)
	r.Finish()
}
