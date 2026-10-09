//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// TestBundle_LifecycleAndColumns follows one Bundle from creation to Verified
// as a user sees it: the controller picks it up (Available or Promoting), it
// stays Promoting with the Graph accepted while prod waits on its PR, and it
// turns Verified, with Ready=True and GraphReady=True, once prod is
// Verified. `kubectl get bundles` and `kubectl get promotionsteps` show the
// documented columns with the live values at each point.
//
// Covers BUNDLE-PHASE-01, BUNDLE-VERIFIED-01, BUNDLE-GRAPHCOND-01, BUNDLE-COLUMNS-01.
func TestBundle_LifecycleAndColumns(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	e.WaitBundle(t, a.ns, bundle, 30*time.Second, "picked up by the controller", func(b *v1alpha1.Bundle) (bool, string) {
		return b.Status.Phase == "Available" || b.Status.Phase == "Promoting", fmt.Sprintf("phase=%q", b.Status.Phase)
	})
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	e.WaitBundle(t, a.ns, bundle, time.Minute, "Promoting with the Graph accepted", func(b *v1alpha1.Bundle) (bool, string) {
		ok, seen := framework.CondIs(b.Status.Conditions, "GraphAccepted", metav1.ConditionTrue, "")
		return ok && b.Status.Phase == "Promoting", fmt.Sprintf("phase=%q %s", b.Status.Phase, seen)
	})
	framework.Consistently(t, 10*time.Second, "the Bundle stays Promoting while prod waits", func(ctx context.Context) (bool, string) {
		var b v1alpha1.Bundle
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: bundle}, &b); err != nil {
			return false, err.Error()
		}
		return b.Status.Phase == "Promoting", fmt.Sprintf("phase=%q", b.Status.Phase)
	})
	b := a.bundle(t, bundle)
	ok, seen := framework.CondIs(b.Status.Conditions, "Ready", metav1.ConditionFalse, "Promoting")
	assert.True(t, ok, "Ready while promoting: %s", seen)
	graphReady := findCond(b.Status.Conditions, "GraphReady")
	assert.NotEmpty(t, graphReady.Status, "GraphReady is shown while promoting")
	assert.NotEqual(t, metav1.ConditionTrue, graphReady.Status, "GraphReady is not True while prod is unverified")

	prod, _, err := e.Step(context.Background(), a.ns, pipelineName, bundle, "prod")
	require.NoError(t, err)
	checkColumns(t, e, a.ns, bundle, prod.Name, "Promoting", "WaitingForMerge")

	a.merge(t, a.openPR(t, bundle, "prod"))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	e.WaitBundle(t, a.ns, bundle, time.Minute, "Verified and Ready", func(b *v1alpha1.Bundle) (bool, string) {
		ok, seen := framework.CondIs(b.Status.Conditions, "Ready", metav1.ConditionTrue, "Verified")
		return ok && b.Status.Phase == "Verified", fmt.Sprintf("phase=%q %s", b.Status.Phase, seen)
	})
	e.WaitBundle(t, a.ns, bundle, 2*time.Minute, "GraphReady True", func(b *v1alpha1.Bundle) (bool, string) {
		return framework.CondIs(b.Status.Conditions, "GraphReady", metav1.ConditionTrue, "")
	})
	checkColumns(t, e, a.ns, bundle, prod.Name, "Verified", "Verified")
	a.running(t, "prod", imageV2, "the Verified Bundle is deployed")
}

// checkColumns checks the kubectl get columns of bundle and of its step.
func checkColumns(t *testing.T, e *framework.Env, ns, bundle, step, phase, state string) {
	t.Helper()
	bt := e.GetTable(t, "kardinal.io", "v1alpha1", "bundles", ns)
	assert.Equal(t, []string{"Name", "Type", "Pipeline", "Phase", "Age"}, bt.Columns, "bundle columns")
	assert.Equal(t, "image", bt.Cell(bundle, "Type"))
	assert.Equal(t, pipelineName, bt.Cell(bundle, "Pipeline"))
	assert.Equal(t, phase, bt.Cell(bundle, "Phase"))
	assert.NotEmpty(t, bt.Cell(bundle, "Age"))

	st := e.GetTable(t, "kardinal.io", "v1alpha1", "promotionsteps", ns)
	assert.Equal(t, []string{"Name", "Pipeline", "Env", "Bundle", "State", "Age"}, st.Columns, "promotionstep columns")
	assert.Equal(t, pipelineName, st.Cell(step, "Pipeline"))
	assert.Equal(t, "prod", st.Cell(step, "Env"))
	assert.Equal(t, bundle, st.Cell(step, "Bundle"))
	assert.Equal(t, state, st.Cell(step, "State"))
	assert.NotEmpty(t, st.Cell(step, "Age"))
}

// TestBundle_FailedBundle checks how a Bundle fails and what retries it. A
// Bundle whose test step fails its health check is Failed (Failed=True
// StepFailed) and goes no further. A Pipeline change does not retry that
// Bundle: docs/changelog.md said "A Failed Bundle retries after its Pipeline
// changes", which holds only for a Bundle that failed validation before any
// Graph was built. Such a Bundle, one targeting an environment the Pipeline
// does not have yet, is retried when the environment is added and promotes.
//
// Covers BUNDLE-FAILED-01.
func TestBundle_FailedBundle(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod", "staging")
	p := a.pipeline(nil)
	staging := *envSpec(t, p, "staging")
	p.Spec.Environments = p.Spec.Environments[:2]
	envSpec(t, p, "test").Health.Timeout = "20s"
	a.apply(t, p)

	broken := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
	failed := e.WaitStepState(t, a.ns, pipelineName, broken, "test", "Failed", promoteTimeout)
	assert.Contains(t, failed.Status.Message, "timeout after 20s", "the step names the health timeout")
	b := e.WaitBundle(t, a.ns, broken, time.Minute, "Failed by its step", func(b *v1alpha1.Bundle) (bool, string) {
		ok, seen := framework.CondIs(b.Status.Conditions, "Failed", metav1.ConditionTrue, "StepFailed")
		return ok && b.Status.Phase == "Failed", fmt.Sprintf("phase=%q %s", b.Status.Phase, seen)
	})
	ready := findCond(b.Status.Conditions, "Ready")
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.True(t, strings.HasPrefix(ready.Message, "promotion failed: environment test"), "Ready message: %q", ready.Message)
	a.noStep(t, broken, "prod", 10*time.Second)
	a.fileHas(t, "prod", fixtures.V1, "prod is untouched")

	a.updatePipeline(t, pipelineName, func(p *v1alpha1.Pipeline) { envSpec(t, p, "test").Health.Timeout = "1m" })
	framework.Consistently(t, 30*time.Second, "a step-failed Bundle is not retried by a Pipeline change",
		func(ctx context.Context) (bool, string) {
			var cur v1alpha1.Bundle
			if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: broken}, &cur); err != nil {
				return false, err.Error()
			}
			ps, _, err := e.Step(ctx, a.ns, pipelineName, broken, "test")
			if err != nil {
				return false, err.Error()
			}
			_, prodStep, err := e.Step(ctx, a.ns, pipelineName, broken, "prod")
			if err != nil {
				return false, err.Error()
			}
			return cur.Status.Phase == "Failed" && ps != nil && ps.UID == failed.UID && ps.Status.State == "Failed" && !prodStep,
				fmt.Sprintf("phase=%q prod step=%v", cur.Status.Phase, prodStep)
		})

	retried := a.createBundle(t, v1alpha1.BundleSpec{Images: podinfoImages(fixtures.V2),
		Intent: &v1alpha1.BundleIntent{TargetEnvironment: "staging"}})
	e.WaitBundle(t, a.ns, retried, time.Minute, "Failed with InvalidSpec",
		failedWith("InvalidIntent", `unknown target environment "staging"`))
	a.updatePipeline(t, pipelineName, func(p *v1alpha1.Pipeline) {
		p.Spec.Environments = append(p.Spec.Environments, staging)
	})
	framework.Eventually(t, time.Minute, "a Retrying event on "+retried, func(ctx context.Context) (bool, string) {
		evs, err := e.Events(ctx, a.ns, "Bundle", retried)
		if err != nil {
			return false, err.Error()
		}
		for _, ev := range evs {
			if ev.Reason == "Retrying" {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d events", len(evs))
	})
	for _, env := range []string{"test", "prod", "staging"} {
		e.WaitStepState(t, a.ns, pipelineName, retried, env, "Verified", promoteTimeout)
	}
	e.WaitBundlePhase(t, a.ns, retried, "Verified", time.Minute)
	a.running(t, "staging", imageV2, "the retried Bundle reaches the added environment")
}

// TestBundle_SupersededBundleStops checks what supersession stops. Bundle A
// is Verified in test and held before prod by a ChangeWindow gate. The
// Pipeline is paused, and Bundle B supersedes A while B's test step is held
// Pending. Bundle C supersedes B: B's never-started step fails with the
// documented message, an Event (Superseded, Cancel) and no AuditEvent. After
// resume C goes through test, and once the window ends C reaches prod, while
// A and B get no new step even though the gate before prod has opened.
//
// Covers BUNDLE-SUPERSEDE-01.
func TestBundle_SupersededBundleStops(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	ctx := context.Background()
	now := time.Now()
	cw := &v1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: "blackout-" + a.ns},
		Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Reason: "e2e blackout",
			Start: metav1.NewTime(now.Add(-time.Hour)), End: metav1.NewTime(now.Add(time.Hour))},
	}
	require.NoError(t, e.Client.Create(ctx, cw))
	t.Cleanup(func() {
		if err := e.Client.Delete(context.Background(), cw); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete ChangeWindow %s: %v", cw.Name, err)
		}
	})
	require.NoError(t, e.Client.Create(ctx, &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "window", Namespace: a.ns, Labels: map[string]string{"kardinal.io/applies-to": "prod"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: fmt.Sprintf("changewindow.isAllowed(%q)", cw.Name),
			Message: "prod waits for the blackout to end", RecheckInterval: "10s"},
	}))
	a.apply(t, a.pipeline(nil))

	first := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, first, "test", "Verified", promoteTimeout)
	a.noStep(t, first, "prod", 15*time.Second)

	e.MustKardinal(t, a.ns, "pause", pipelineName)
	second := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitBundlePhase(t, a.ns, first, "Superseded", time.Minute)
	e.WaitStep(t, a.ns, pipelineName, second, "test", time.Minute, "held by the pause", func(ps *v1alpha1.PromotionStep) (bool, string) {
		// Pending is the empty state: a held step has not started.
		return ps.Status.State == "" && strings.Contains(ps.Status.Message, "is paused"),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})

	third := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)
	e.WaitBundlePhase(t, a.ns, second, "Superseded", time.Minute)
	cancelled := e.WaitStepState(t, a.ns, pipelineName, second, "test", "Failed", time.Minute)
	assert.Equal(t, fmt.Sprintf("bundle %s was superseded before this step started", second), cancelled.Status.Message)
	framework.Eventually(t, time.Minute, "a Superseded Cancel event on "+cancelled.Name, func(ctx context.Context) (bool, string) {
		evs, err := e.Events(ctx, a.ns, "PromotionStep", cancelled.Name)
		if err != nil {
			return false, err.Error()
		}
		var seen []string
		for _, ev := range evs {
			if ev.Reason == "Superseded" && ev.Action == "Cancel" {
				return true, ""
			}
			seen = append(seen, ev.Reason+"/"+ev.Action)
		}
		return false, strings.Join(seen, ", ")
	})
	assert.Empty(t, stepAudits(t, e, a.ns, second, "test"), "the never-started step writes no AuditEvent")

	e.MustKardinal(t, a.ns, "resume", pipelineName)
	e.WaitStepState(t, a.ns, pipelineName, third, "test", "Verified", promoteTimeout)
	a.noStep(t, third, "prod", 15*time.Second)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var cur v1alpha1.ChangeWindow
		if err := e.Client.Get(ctx, types.NamespacedName{Name: cw.Name}, &cur); err != nil {
			return err
		}
		cur.Spec.Start = metav1.NewTime(time.Now().Add(-2 * time.Hour))
		cur.Spec.End = metav1.NewTime(time.Now().Add(-time.Minute))
		return e.Client.Update(ctx, &cur)
	})
	require.NoError(t, err, "end the blackout")
	e.WaitStepState(t, a.ns, pipelineName, third, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, third, "Verified", time.Minute)
	a.running(t, "prod", imageV3, "the newest Bundle reaches prod")
	framework.Consistently(t, 20*time.Second, "the Superseded Bundles get no new step", func(ctx context.Context) (bool, string) {
		n1, n2 := a.stepCount(t, first), a.stepCount(t, second)
		return n1 == 1 && n2 == 1, fmt.Sprintf("%s has %d steps, %s has %d", first, n1, second, n2)
	})
}

// stepAudits returns the actions of the AuditEvents of bundle's step for env.
func stepAudits(t *testing.T, e *framework.Env, ns, bundle, env string) []string {
	t.Helper()
	all, err := e.AuditEvents(context.Background(), ns, bundle)
	require.NoError(t, err)
	var out []string
	for _, ev := range all {
		if ev.Labels["kardinal.io/environment"] == env {
			out = append(out, ev.Spec.Action)
		}
	}
	return out
}

// TestBundle_SupersededBeforePushPRReview supersedes a Bundle whose pr-review
// step is held by a pause, so it never pushed its kardinal/<bundle>/<env>
// branch. A pr-review step that ends before it opens its PR deletes that
// branch, and a branch that is not there is not an error: the step fails at
// once with "superseded before this step started" (B90: Forgejo answers 500
// "object does not exist" to the delete of a missing branch, so the step
// retried the delete with backoff and then said to delete the branch by
// hand). After resume the newer Bundle opens its PR.
//
// Covers BUNDLE-SUPERSEDE-06.
func TestBundle_SupersededBeforePushPRReview(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))
	e.MustKardinal(t, a.ns, "pause", pipelineName)

	older := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStep(t, a.ns, pipelineName, older, "test", time.Minute, "held by the pause", func(ps *v1alpha1.PromotionStep) (bool, string) {
		return ps.Status.State == "" && strings.Contains(ps.Status.Message, "is paused"),
			fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	newer := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)
	e.WaitBundlePhase(t, a.ns, older, "Superseded", time.Minute)
	cancelled := e.WaitStepState(t, a.ns, pipelineName, older, "test", "Failed", 30*time.Second)
	assert.Equal(t, fmt.Sprintf("bundle %s was superseded before this step started", older), cancelled.Status.Message)
	assert.Zero(t, cancelled.Status.RetryCount, "status.retryCount of a step whose branch delete found no branch")

	e.MustKardinal(t, a.ns, "resume", pipelineName)
	e.WaitStepState(t, a.ns, pipelineName, newer, "test", "WaitingForMerge", promoteTimeout)
	a.openPR(t, newer, "test")
}

// TestBundle_SupersededBackToBack creates three image Bundles of an auto
// Pipeline back to back, as a burst of CI builds does. The two older ones turn
// Superseded with Ready reason Superseded, every step they got fails as
// superseded, and neither gets a prod step or a new step later. Only the
// newest promotes: it is Verified in test and prod, and both environments pin
// and run its image, not the tag or the digest of the older two.
//
// Covers BUNDLE-SUPERSEDE-03.
func TestBundle_SupersededBackToBack(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))
	older := []string{
		e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2),
		e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest),
	}
	newest := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)

	for _, name := range older {
		e.WaitBundle(t, a.ns, name, time.Minute, "Superseded", func(b *v1alpha1.Bundle) (bool, string) {
			ok, seen := framework.CondIs(b.Status.Conditions, "Ready", metav1.ConditionFalse, "Superseded")
			return ok && b.Status.Phase == "Superseded", fmt.Sprintf("phase=%q %s", b.Status.Phase, seen)
		})
	}
	for _, env := range []string{"test", "prod"} {
		e.WaitStepState(t, a.ns, pipelineName, newest, env, "Verified", promoteTimeout)
	}
	e.WaitBundlePhase(t, a.ns, newest, "Verified", time.Minute)
	for _, env := range []string{"test", "prod"} {
		k := e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path(env)+"/kustomization.yaml")
		assert.Contains(t, k, "newTag: "+fixtures.V3, "%s pins the newest Bundle's tag", env)
		assert.NotContains(t, k, "digest:", "%s does not pin the second Bundle's digest", env)
		e.WaitDeploymentImage(t, a.ns, fixtures.Workload(env), imageV3, syncTimeout)
	}

	counts := make([]int, len(older))
	framework.Eventually(t, time.Minute, "every step of the Superseded Bundles failed as superseded", func(ctx context.Context) (bool, string) {
		for i, name := range older {
			steps, err := e.Steps(ctx, a.ns, pipelineName, name)
			if err != nil {
				return false, err.Error()
			}
			for _, ps := range steps {
				cancelled := ps.Status.Message == fmt.Sprintf("bundle %s was superseded — promotion cancelled", name) ||
					ps.Status.Message == fmt.Sprintf("bundle %s was superseded before this step started", name)
				if ps.Spec.Environment != "test" || ps.Status.State != "Failed" || !cancelled {
					return false, fmt.Sprintf("%s step of %s: state=%q message=%q", ps.Spec.Environment, name,
						ps.Status.State, ps.Status.Message)
				}
			}
			counts[i] = len(steps)
		}
		return true, ""
	})
	t.Logf("the Superseded Bundles %v had %v steps", older, counts)
	framework.Consistently(t, 20*time.Second, "the Superseded Bundles stay Superseded and get no new step", func(ctx context.Context) (bool, string) {
		for i, name := range older {
			var b v1alpha1.Bundle
			if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: name}, &b); err != nil {
				return false, err.Error()
			}
			steps, err := e.Steps(ctx, a.ns, pipelineName, name)
			if err != nil {
				return false, err.Error()
			}
			if b.Status.Phase != "Superseded" || len(steps) != counts[i] {
				return false, fmt.Sprintf("%s: phase=%q, %d steps (had %d)", name, b.Status.Phase, len(steps), counts[i])
			}
		}
		return true, ""
	})
}

// TestBundle_SupersessionPerType checks that supersession is per Bundle type,
// with test behind a PR so that in-flight Bundles wait there. A config Bundle
// waits on its PR; a newer image Bundle opens its own PR, and the config
// Bundle keeps promoting with its PR open. A newer config Bundle then
// supersedes the first config Bundle, whose PR is closed, while the image
// Bundle keeps promoting. Both PRs still open merge: both Bundles end
// Verified, and test runs the new image with the config change.
//
// Covers BUNDLE-SUPERSEDE-04.
func TestBundle_SupersessionPerType(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))
	cfg, sha := a.configRepo(t, "test")
	configBundle := func() string {
		t.Helper()
		return e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha, "--config-repo", cfg.CloneURL)
	}
	waitPR := func(bundle string) gitserver.PR {
		t.Helper()
		e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "WaitingForMerge", promoteTimeout)
		return a.openPR(t, bundle, "test")
	}
	// keepPromoting checks that each Bundle stays Promoting, its test step
	// waiting on its PR, which stays open.
	keepPromoting := func(what string, prs map[string]gitserver.PR) {
		t.Helper()
		framework.Consistently(t, 15*time.Second, what, func(ctx context.Context) (bool, string) {
			all, err := e.Git.PullRequests(ctx, a.repo)
			if err != nil {
				return false, err.Error()
			}
			state := map[int]string{}
			for _, pr := range all {
				state[pr.Number] = pr.State
			}
			for name, pr := range prs {
				var b v1alpha1.Bundle
				if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: name}, &b); err != nil {
					return false, err.Error()
				}
				ps, ok, err := e.Step(ctx, a.ns, pipelineName, name, "test")
				if err != nil {
					return false, err.Error()
				}
				if !ok || b.Status.Phase != "Promoting" || ps.Status.State != "WaitingForMerge" || state[pr.Number] != "open" {
					return false, fmt.Sprintf("%s: phase=%q step found=%v, PR #%d %s", name, b.Status.Phase, ok,
						pr.Number, state[pr.Number])
				}
			}
			return true, ""
		})
	}

	config1 := configBundle()
	config1PR := waitPR(config1)
	image := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	imagePR := waitPR(image)
	keepPromoting("a newer image Bundle does not supersede the config Bundle",
		map[string]gitserver.PR{config1: config1PR, image: imagePR})

	config2 := configBundle()
	e.WaitBundlePhase(t, a.ns, config1, "Superseded", time.Minute)
	e.WaitPRState(t, a.repo, config1PR.Number, "closed", time.Minute)
	config2PR := waitPR(config2)
	keepPromoting("a newer config Bundle does not supersede the image Bundle",
		map[string]gitserver.PR{image: imagePR, config2: config2PR})

	a.merge(t, imagePR)
	a.merge(t, config2PR)
	for _, name := range []string{image, config2} {
		e.WaitStepState(t, a.ns, pipelineName, name, "test", "Verified", promoteTimeout)
		e.WaitBundlePhase(t, a.ns, name, "Verified", time.Minute)
	}
	a.fileHas(t, "test", fixtures.V2, "the image Bundle is promoted")
	assert.True(t, a.configDeployed(t, "test"), "the second config Bundle is promoted")
	a.waitConfigRunning(t, "test")
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("test"), imageV2, syncTimeout)
}

// TestBundle_SameSecondOrder checks that within one second the
// kardinal.io/created-at stamp, not the name or the order the API server
// received them, decides which Bundle is newer: "-a" is created first and
// sorts first by name, but carries the later stamp, so it supersedes "-b"
// and is the one promoted.
//
// Covers BUNDLE-ORDER-01.
func TestBundle_SameSecondOrder(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))

	var newer, older *v1alpha1.Bundle
	for attempt := 1; ; attempt++ {
		at := time.Now()
		newer = a.createStamped(t, fmt.Sprintf("order%d-a", attempt), at.Add(500*time.Millisecond), fixtures.V2)
		older = a.createStamped(t, fmt.Sprintf("order%d-b", attempt), at, fixtures.V3)
		if newer.CreationTimestamp.Equal(&older.CreationTimestamp) {
			break
		}
		if attempt == 5 {
			t.Fatalf("5 Bundle pairs were created in different seconds")
		}
		t.Logf("attempt %d: created at %s and %s, in different seconds; retrying", attempt,
			newer.CreationTimestamp.Format(time.RFC3339), older.CreationTimestamp.Format(time.RFC3339))
	}

	e.WaitBundlePhase(t, a.ns, older.Name, "Superseded", time.Minute)
	e.WaitStepState(t, a.ns, pipelineName, newer.Name, "test", "WaitingForMerge", promoteTimeout)
	a.merge(t, a.openPR(t, newer.Name, "test"))
	e.WaitBundlePhase(t, a.ns, newer.Name, "Verified", promoteTimeout)
	a.running(t, "test", imageV2, "the Bundle with the later stamp is promoted")
}

// createStamped creates the image Bundle name of the app's Pipeline with
// kardinal.io/created-at set to at.
func (a *app) createStamped(t *testing.T, name string, at time.Time, tag string) *v1alpha1.Bundle {
	t.Helper()
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.ns,
			Annotations: map[string]string{"kardinal.io/created-at": at.UTC().Format(time.RFC3339Nano)}},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipelineName, Images: podinfoImages(tag)},
	}
	require.NoError(t, a.e.Client.Create(context.Background(), b), "create Bundle %s", name)
	return b
}

// TestBundle_InvalidSpecDoesNotRetry checks two Bundles that cannot be
// promoted. `kardinal create bundle --type mixed` without --config-commit is
// refused; the same mixed Bundle created with the API client, and a config
// Bundle of a Pipeline whose environment uses update.strategy argocd (a Graph
// that cannot be built), are Failed with InvalidSpec GraphBuildFailed. They
// stay Failed: no Graph, no PromotionStep and no commit, and the condition is
// not rewritten.
//
// Covers BUNDLE-INVALID-01.
func TestBundle_InvalidSpecDoesNotRetry(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: []string{"test"},
		repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}}))}
	a.apply(t, a.pipeline(nil))
	argo := a.pipeline(nil)
	argo.Name = "argo"
	envSpec(t, argo, "test").Update = v1alpha1.UpdateConfig{Strategy: "argocd",
		ArgoCD: &v1alpha1.ArgoCDUpdateConfig{Application: a.argoApp("test")}}
	a.apply(t, argo)
	ctx := context.Background()
	before, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 10)
	require.NoError(t, err)
	require.NotEmpty(t, before)

	out, err := e.Kardinal(t, ns, "create", "bundle", pipelineName, "--type", "mixed", "--image", imageV2)
	require.Error(t, err, "the CLI refuses a mixed Bundle without a config commit")
	assert.Contains(t, out, "requires --config-commit")

	mixed := a.createBundle(t, v1alpha1.BundleSpec{Type: "mixed", Images: podinfoImages(fixtures.V2),
		ConfigRef: &v1alpha1.ConfigRef{GitRepo: a.repo.CloneURL}})
	config := a.createBundle(t, v1alpha1.BundleSpec{Pipeline: "argo", Type: "config",
		ConfigRef: &v1alpha1.ConfigRef{GitRepo: a.repo.CloneURL, CommitSHA: before[0].SHA}})
	cases := map[string]string{
		mixed:  `type "mixed" requires configRef.commitSHA`,
		config: `environment "test" uses update.strategy argocd`,
	}
	failedAt := map[string]metav1.Time{}
	for name, msg := range cases {
		b := e.WaitBundle(t, ns, name, time.Minute, "Failed with InvalidSpec", failedWith("GraphBuildFailed", msg))
		failedAt[name] = findCond(b.Status.Conditions, "InvalidSpec").LastTransitionTime
	}

	framework.Consistently(t, 20*time.Second, "the invalid Bundles stay Failed", func(ctx context.Context) (bool, string) {
		for name := range cases {
			var b v1alpha1.Bundle
			if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
				return false, err.Error()
			}
			c := findCond(b.Status.Conditions, "InvalidSpec")
			if b.Status.Phase != "Failed" || b.Status.GraphRef != "" || !c.LastTransitionTime.Equal(ptrTime(failedAt[name])) {
				return false, fmt.Sprintf("%s: phase=%q graphRef=%q InvalidSpec since %s", name, b.Status.Phase,
					b.Status.GraphRef, c.LastTransitionTime)
			}
		}
		var steps v1alpha1.PromotionStepList
		if err := e.Client.List(ctx, &steps, client.InNamespace(ns)); err != nil {
			return false, err.Error()
		}
		return len(steps.Items) == 0, fmt.Sprintf("%d PromotionSteps", len(steps.Items))
	})
	after, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 10)
	require.NoError(t, err)
	assert.Equal(t, before[0].SHA, after[0].SHA, "nothing is committed")
}

func ptrTime(t metav1.Time) *metav1.Time { return &t }

// Config Bundles in this suite set podinfo's UI message through the
// Deployment, a change only the config repository has.
const (
	configVar   = "PODINFO_UI_MESSAGE"
	configValue = "from-the-config-repo"
)

// configDeployment is env's Deployment with the config change.
func configDeployment(env string) string {
	return withConfigChange(fixtures.Deployment(fixtures.Workload(env), imageV1))
}

// withConfigChange adds the config change to a fixtures.Deployment manifest:
// podinfo's container gets configVar=configValue.
func withConfigChange(deployment string) string {
	return strings.Replace(deployment, "          ports:\n",
		"          env:\n            - name: "+configVar+"\n              value: "+configValue+"\n          ports:\n", 1)
}

// configRepo creates the app's config repository, holding
// environments/<env>/deployment.yaml with the config change for each env,
// and returns it with its head commit.
func (a *app) configRepo(t *testing.T, envs ...string) (gitserver.Repo, string) {
	t.Helper()
	files := map[string][]byte{}
	for _, env := range envs {
		files[fixtures.Path(env)+"/deployment.yaml"] = []byte(configDeployment(env))
	}
	repo := a.e.Repo(t, a.ns+"-config", files)
	commits, err := gitserver.Commits(context.Background(), a.e.Git, repo, repo.Branch, 1)
	require.NoError(t, err)
	require.NotEmpty(t, commits)
	return repo, commits[0].SHA
}

// configDeployed reports whether env's deployment.yaml in the GitOps repo has
// the config change.
func (a *app) configDeployed(t *testing.T, env string) bool {
	t.Helper()
	return strings.Contains(a.e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path(env)+"/deployment.yaml"), configValue)
}

// waitConfigRunning waits until env's Deployment in the cluster has the
// config change.
func (a *app) waitConfigRunning(t *testing.T, env string) {
	t.Helper()
	framework.Eventually(t, syncTimeout, env+" runs the config change", func(ctx context.Context) (bool, string) {
		var d appsv1.Deployment
		if err := a.e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: fixtures.Workload(env)}, &d); err != nil {
			return false, err.Error()
		}
		envVars := d.Spec.Template.Spec.Containers[0].Env
		return slices.Contains(envVars, corev1.EnvVar{Name: configVar, Value: configValue}), fmt.Sprintf("env %v", envVars)
	})
}

// stepEntry returns the status.steps entry name of ps.
func stepEntry(ps *v1alpha1.PromotionStep, name string) (v1alpha1.StepStatus, bool) {
	for _, s := range ps.Status.Steps {
		if s.Name == name {
			return s, true
		}
	}
	return v1alpha1.StepStatus{}, false
}

// TestBundle_ConfigBundle promotes a config-only Bundle built with
// `kardinal create bundle --type config --config-commit --config-repo`: each
// environment's config-merge copies the config repository's directory at
// that commit into the GitOps repo, the Deployment gets the change, and the
// image stays where it was.
//
// Covers BUNDLE-CONFIG-01.
func TestBundle_ConfigBundle(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))
	cfg, sha := a.configRepo(t, "test", "prod")
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha, "--config-repo", cfg.CloneURL)

	for _, env := range []string{"test", "prod"} {
		ps := e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
		merge, ok := stepEntry(ps, "config-merge")
		require.True(t, ok, "%s runs config-merge: %v", env, ps.Status.Steps)
		assert.Equal(t, v1alpha1.StepExecutionCompleted, merge.State, "%s config-merge completes", env)
		assert.Equal(t, "1", ps.Status.Outputs["mergedFiles"], "%s merges the one changed file", env)
		assert.True(t, a.configDeployed(t, env), "%s deployment.yaml has the config change", env)
		a.waitConfigRunning(t, env)
		a.fileHas(t, env, fixtures.V1, "a config Bundle leaves the image pin alone")
		a.running(t, env, imageV1, "a config Bundle leaves the image alone")
	}
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
}

// TestBundle_MixedBundle promotes a mixed Bundle (an image and a config
// commit) through test and prod. Each environment runs config-merge and then
// kustomize-set-image in one step sequence and one commit: the Deployment gets
// the config change and runs the new image, which wins over the old image in
// the config commit's deployment.yaml. Before the fix a mixed Bundle ran the
// image steps only and its config change was dropped.
//
// Covers BUNDLE-MIXED-01.
func TestBundle_MixedBundle(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))
	cfg, sha := a.configRepo(t, "test", "prod")
	ctx := context.Background()
	before, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 20)
	require.NoError(t, err)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--type", "mixed", "--image", imageV2,
		"--config-commit", sha, "--config-repo", cfg.CloneURL)

	for _, env := range []string{"test", "prod"} {
		ps := e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
		var names []string
		for _, s := range ps.Status.Steps {
			names = append(names, s.Name)
			assert.Equal(t, v1alpha1.StepExecutionCompleted, s.State, "%s %s completes", env, s.Name)
		}
		assert.Equal(t, []string{"git-clone", "config-merge", "kustomize-set-image", "git-commit", "git-push", "health-check"},
			names, "%s merges the config, then sets the image", env)
		assert.Equal(t, "1", ps.Status.Outputs["mergedFiles"], "%s merges the one changed file", env)
		assert.True(t, a.configDeployed(t, env), "%s deployment.yaml has the config change", env)
		a.fileHas(t, env, fixtures.V2, "the image is promoted with the config")
		a.waitConfigRunning(t, env)
		a.running(t, env, imageV2, "the Bundle's image wins over the config commit's")
	}
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	after, err := gitserver.Commits(ctx, e.Git, a.repo, a.repo.Branch, 20)
	require.NoError(t, err)
	assert.Len(t, after, len(before)+2, "one commit per environment carries both changes")
}

// TestBundle_MultiImage promotes a Bundle with two images into a Deployment
// with two containers: kustomization.yaml pins both, and both containers run
// the new versions.
//
// Covers BUNDLE-MULTI-01.
func TestBundle_MultiImage(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoAppFiles(t, e, func(ns string) map[string][]byte {
		return fixtures.SidecarRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}})
	}, "test")
	a.apply(t, a.pipeline(nil))
	pause := fixtures.Pause + ":" + fixtures.PauseV2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2, "--image", pause)

	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	k := e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/kustomization.yaml")
	for _, pin := range []struct{ image, tag string }{{fixtures.Image, fixtures.V2}, {fixtures.Pause, fixtures.PauseV2}} {
		re := regexp.MustCompile(`name: ` + regexp.QuoteMeta(pin.image) + `\n\s+newTag: "?` + regexp.QuoteMeta(pin.tag) + `"?\n`)
		assert.Regexp(t, re, k, "kustomization.yaml pins %s:%s", pin.image, pin.tag)
	}
	framework.Eventually(t, syncTimeout, "both containers run the new images", func(ctx context.Context) (bool, string) {
		d, err := e.Kube.AppsV1().Deployments(a.ns).Get(ctx, fixtures.Workload("test"), metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		var images []string
		for _, c := range d.Spec.Template.Spec.Containers {
			images = append(images, c.Image)
		}
		done := d.Status.UpdatedReplicas == 1 && d.Status.AvailableReplicas == 1 && d.Status.Replicas == 1
		return slices.Equal(images, []string{imageV2, pause}) && done, fmt.Sprintf("images %v %+v", images, d.Status)
	})
}

// TestBundle_DigestPinned promotes a Bundle created with --image
// repo@sha256:...: kustomization.yaml pins the digest instead of a tag, the
// Deployment runs the image by digest, and the pod serves that version.
//
// Covers BUNDLE-DIGEST-01.
func TestBundle_DigestPinned(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	byDigest := fixtures.Image + "@" + fixtures.V2Digest
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", byDigest)

	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	k := e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/kustomization.yaml")
	assert.Contains(t, k, "digest: "+fixtures.V2Digest, "kustomization.yaml pins the digest")
	assert.NotContains(t, k, "newTag:", "the tag pin is replaced")
	e.WaitDeploymentImage(t, a.ns, fixtures.Workload("test"), byDigest, syncTimeout)
	framework.Eventually(t, time.Minute, "the pod serves "+fixtures.V2, func(ctx context.Context) (bool, string) {
		pods, err := e.Kube.CoreV1().Pods(a.ns).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=" + fixtures.Workload("test")})
		if err != nil {
			return false, err.Error()
		}
		for _, p := range pods.Items {
			if p.Status.Phase != corev1.PodRunning || p.DeletionTimestamp != nil || p.Spec.Containers[0].Image != byDigest {
				continue
			}
			code, body, err := e.PodHTTP(ctx, a.ns, p.Name, 9898, "GET", "/version")
			if err != nil {
				return false, err.Error()
			}
			return code == 200 && strings.Contains(body, `"`+fixtures.V2+`"`), fmt.Sprintf("GET /version: %d %s", code, body)
		}
		return false, "no Running pod on the digest"
	})
}

// TestBundle_HistoryLimit checks history garbage collection. With
// historyLimit 2, each new Bundle deletes the oldest finished Bundles beyond
// two, Failed or Verified alike, and keeps the in-flight one. A Pipeline
// without historyLimit keeps 50: the 52nd Bundle deletes only the oldest of
// 51 finished ones.
//
// Covers BUNDLE-HISTORY-01.
func TestBundle_HistoryLimit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	p := a.pipeline(nil)
	p.Spec.HistoryLimit = 2
	a.apply(t, p)
	missing := &v1alpha1.BundleIntent{TargetEnvironment: "missing"}
	fail := func(pipeline string) string {
		t.Helper()
		name := a.createBundle(t, v1alpha1.BundleSpec{Pipeline: pipeline, Images: podinfoImages(fixtures.V2), Intent: missing})
		e.WaitBundlePhase(t, a.ns, name, "Failed", time.Minute)
		return name
	}

	f1, f2 := fail(pipelineName), fail(pipelineName)
	v := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitBundlePhase(t, a.ns, v, "Verified", promoteTimeout)
	f3 := a.createBundle(t, v1alpha1.BundleSpec{Images: podinfoImages(fixtures.V2), Intent: missing})
	a.waitGone(t, f1)
	e.WaitBundlePhase(t, a.ns, f3, "Failed", time.Minute)
	n := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)
	a.waitGone(t, f2)
	e.WaitBundlePhase(t, a.ns, n, "Verified", promoteTimeout)
	want := []string{v, f3, n}
	slices.Sort(want)
	assert.Equal(t, want, a.bundleNames(t, pipelineName), "two finished Bundles and the newest remain")

	dflt := a.pipeline(nil)
	dflt.Name = "dflt"
	a.apply(t, dflt)
	var made []string
	for range 51 {
		made = append(made, a.createBundle(t, v1alpha1.BundleSpec{Pipeline: dflt.Name, Images: podinfoImages(fixtures.V2),
			Intent: missing}))
	}
	framework.Eventually(t, 3*time.Minute, "51 finished Bundles of "+dflt.Name, func(ctx context.Context) (bool, string) {
		var list v1alpha1.BundleList
		if err := e.Client.List(ctx, &list, client.InNamespace(a.ns)); err != nil {
			return false, err.Error()
		}
		done := 0
		for _, b := range list.Items {
			if b.Spec.Pipeline == dflt.Name && (b.Status.Phase == "Failed" || b.Status.Phase == "Superseded") {
				done++
			}
		}
		return done == 51, fmt.Sprintf("%d finished", done)
	})
	assert.Len(t, a.bundleNames(t, dflt.Name), 51, "50 is the default limit: none is deleted yet")
	made = append(made, fail(dflt.Name))
	a.waitGone(t, made[0])
	want = slices.Clone(made[1:])
	slices.Sort(want)
	framework.Consistently(t, 10*time.Second, "only the oldest is deleted", func(ctx context.Context) (bool, string) {
		got := a.bundleNames(t, dflt.Name)
		return slices.Equal(got, want), fmt.Sprintf("%d Bundles", len(got))
	})
}

// waitGone waits until Bundle name is deleted.
func (a *app) waitGone(t *testing.T, name string) {
	t.Helper()
	framework.Eventually(t, time.Minute, "Bundle "+name+" to be deleted", func(ctx context.Context) (bool, string) {
		var b v1alpha1.Bundle
		err := a.e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: name}, &b)
		if apierrors.IsNotFound(err) {
			return true, ""
		}
		if err != nil {
			return false, err.Error()
		}
		return false, fmt.Sprintf("phase=%q", b.Status.Phase)
	})
}

// bundleNames lists the names of pipeline's Bundles, sorted.
func (a *app) bundleNames(t *testing.T, pipeline string) []string {
	t.Helper()
	var list v1alpha1.BundleList
	require.NoError(t, a.e.Client.List(context.Background(), &list, client.InNamespace(a.ns)))
	var out []string
	for _, b := range list.Items {
		if b.Spec.Pipeline == pipeline {
			out = append(out, b.Name)
		}
	}
	slices.Sort(out)
	return out
}

// TestBundle_ConcurrencyCap checks maxConcurrentPromotions 1: while an image
// Bundle waits on its PR, a config Bundle (a different type, so not a
// supersession) stays Available with Ready reason WaitingForSlot and no
// Graph. It starts once the first is Verified and is promoted in turn.
//
// Covers BUNDLE-SLOT-01.
func TestBundle_ConcurrencyCap(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	p := a.pipeline(map[string]string{"test": "pr-review"})
	p.Spec.MaxConcurrentPromotions = 1
	a.apply(t, p)
	cfg, sha := a.configRepo(t, "test")

	first := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, first, "test", "WaitingForMerge", promoteTimeout)
	second := e.CreateBundle(t, a.ns, pipelineName, "--type", "config", "--config-commit", sha, "--config-repo", cfg.CloneURL)
	waiting := func(b *v1alpha1.Bundle) (bool, string) {
		ok, seen := framework.CondIs(b.Status.Conditions, "Ready", metav1.ConditionFalse, "WaitingForSlot")
		ok = ok && findCond(b.Status.Conditions, "Ready").Message ==
			"maxConcurrentPromotions (1) reached; waiting for a promoting bundle to finish"
		return ok && b.Status.Phase == "Available" && b.Status.GraphRef == "", fmt.Sprintf("phase=%q graphRef=%q %s",
			b.Status.Phase, b.Status.GraphRef, seen)
	}
	e.WaitBundle(t, a.ns, second, time.Minute, "waiting for a slot", waiting)
	framework.Consistently(t, 15*time.Second, "the second Bundle waits", func(ctx context.Context) (bool, string) {
		var b v1alpha1.Bundle
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: second}, &b); err != nil {
			return false, err.Error()
		}
		return waiting(&b)
	})
	assert.Zero(t, a.stepCount(t, second), "a waiting Bundle has no step")

	a.merge(t, a.openPR(t, first, "test"))
	firstStep := e.WaitStepState(t, a.ns, pipelineName, first, "test", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, first, "Verified", time.Minute)
	secondStep := e.WaitStepState(t, a.ns, pipelineName, second, "test", "WaitingForMerge", 2*time.Minute)
	startedAfter(t, secondStep, verifiedAt(t, firstStep), "the second Bundle starts when the slot frees")
	a.merge(t, a.openPR(t, second, "test"))
	e.WaitBundlePhase(t, a.ns, second, "Verified", promoteTimeout)
	assert.True(t, a.configDeployed(t, "test"), "the second Bundle is promoted")
	a.waitConfigRunning(t, "test")
}

// TestBundle_Metrics checks status.metrics of a Verified Bundle: test bakes
// for a minute and its readiness is turned off during the bake, which resets
// the timer once; prod is held by a gate an operator overrides. The metrics
// are absent while the Bundle promotes and then show the bake resets, the one
// override and the minutes from the Bundle's creation to prod. test checks
// its Deployment (health.type resource): a replica lost after the rollout is
// a health alarm there, while Argo CD reports it as Progressing, which only
// stops the window.
//
// Covers BUNDLE-METRICS-01.
func TestBundle_Metrics(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	ctx := context.Background()
	p := a.pipeline(nil)
	testEnv := envSpec(t, p, "test")
	testEnv.Bake = &v1alpha1.BakeConfig{Minutes: 1}
	testEnv.Health = v1alpha1.HealthConfig{Type: "resource", Timeout: "3m",
		Resource: &v1alpha1.ResourceRef{Name: fixtures.Workload("test"), Namespace: a.ns}}
	require.NoError(t, e.Client.Create(ctx, &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "hold", Namespace: a.ns, Labels: map[string]string{"kardinal.io/applies-to": "prod"}},
		Spec:       v1alpha1.PolicyGateSpec{Expression: "false", Message: "held for an operator", RecheckInterval: "10s"},
	}))
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	notDone := func(ps *v1alpha1.PromotionStep) {
		if ps.Status.State == "Verified" || ps.Status.State == "Failed" {
			t.Fatalf("test step is %s before a bake reset: %s", ps.Status.State, ps.Status.Message)
		}
	}
	e.WaitStep(t, a.ns, pipelineName, bundle, "test", promoteTimeout, "the bake to start", func(ps *v1alpha1.PromotionStep) (bool, string) {
		notDone(ps)
		return ps.Status.BakeStartedAt != nil, fmt.Sprintf("state=%q message=%q", ps.Status.State, ps.Status.Message)
	})
	pod := e.RunningPod(t, a.ns, "app.kubernetes.io/name="+fixtures.Workload("test"))
	setReady := func(action string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		code, body, err := e.PodHTTP(ctx, a.ns, pod, 9898, "POST", "/readyz/"+action)
		require.NoError(t, err, "POST /readyz/%s", action)
		require.Less(t, code, 300, "POST /readyz/%s: %d %s", action, code, body)
	}
	setReady("disable")
	reset := e.WaitStep(t, a.ns, pipelineName, bundle, "test", 2*time.Minute, "a bake reset", func(ps *v1alpha1.PromotionStep) (bool, string) {
		notDone(ps)
		return ps.Status.BakeResets >= 1, fmt.Sprintf("resets=%d message=%q", ps.Status.BakeResets, ps.Status.Message)
	})
	assert.Contains(t, reset.Status.Message, "timer reset")
	setReady("enable")

	test := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assert.Contains(t, test.Status.Message, "bake complete: 1m contiguous healthy via ")
	assert.Contains(t, test.Status.Message, fmt.Sprintf("(resets=%d)", test.Status.BakeResets))
	a.noStep(t, bundle, "prod", 15*time.Second)
	b := a.bundle(t, bundle)
	assert.Equal(t, "Promoting", b.Status.Phase)
	assert.Nil(t, b.Status.Metrics, "no metrics before the Bundle is Verified")

	framework.Eventually(t, time.Minute, "the hold gate instance of "+bundle, func(ctx context.Context) (bool, string) {
		var gates v1alpha1.PolicyGateList
		if err := e.Client.List(ctx, &gates, client.InNamespace(a.ns), client.MatchingLabels{
			"kardinal.io/bundle": bundle, "kardinal.io/gate-template": "hold"}); err != nil {
			return false, err.Error()
		}
		return len(gates.Items) == 1, fmt.Sprintf("%d instances", len(gates.Items))
	})
	e.MustKardinal(t, a.ns, "override", pipelineName, "--stage", "prod", "--gate", "hold", "--reason", "e2e operator override")
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	b = e.WaitBundle(t, a.ns, bundle, time.Minute, "Verified with metrics", func(b *v1alpha1.Bundle) (bool, string) {
		return b.Status.Phase == "Verified" && b.Status.Metrics != nil, fmt.Sprintf("phase=%q metrics=%+v", b.Status.Phase, b.Status.Metrics)
	})
	m := b.Status.Metrics
	assert.Equal(t, test.Status.BakeResets, m.BakeResets, "bakeResets sums the steps' resets")
	assert.Equal(t, 1, m.OperatorInterventions, "one override")
	assert.GreaterOrEqual(t, m.CommitToProductionMinutes, int64(1), "the bake alone takes a minute")
}

// TestBundle_ArtifactFieldsValidated: the API server refuses a Bundle whose
// artifact fields a per-promotion query could carry somewhere unintended: a
// tag outside the OCI tag grammar, a digest that is not an OCI digest, a
// configRef.commitSHA that is not 4 to 64 hex characters, and a
// provenance.commitSHA that is neither hex nor a digest (a CI placeholder
// such as "unknown"). The refusal names the field, nothing is stored, and a
// Bundle with valid values is accepted.
//
// Covers BUNDLE-PATTERNS-01.
func TestBundle_ArtifactFieldsValidated(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	digest := "sha256:" + strings.Repeat("ab", 32)
	bundle := func(name string, mutate func(*v1alpha1.BundleSpec)) *v1alpha1.Bundle {
		b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipelineName,
				Images: []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}}}
		mutate(&b.Spec)
		return b
	}
	for _, tc := range []struct {
		name, field string
		mutate      func(*v1alpha1.BundleSpec)
	}{
		{"tag-with-slash", "spec.images[0].tag", func(s *v1alpha1.BundleSpec) { s.Images[0].Tag = "v1/../x" }},
		{"tag-with-space", "spec.images[0].tag", func(s *v1alpha1.BundleSpec) { s.Images[0].Tag = "v1 x" }},
		{"tag-starting-with-dot", "spec.images[0].tag", func(s *v1alpha1.BundleSpec) { s.Images[0].Tag = ".hidden" }},
		{"short-digest", "spec.images[0].digest", func(s *v1alpha1.BundleSpec) { s.Images[0].Digest = "sha256:abc" }},
		{"config-commit-unknown", "spec.configRef.commitSHA", func(s *v1alpha1.BundleSpec) {
			s.Type = "config"
			s.Images = nil
			s.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://example.com/repo.git", CommitSHA: "unknown"}
		}},
		{"provenance-commit-unknown", "spec.provenance.commitSHA", func(s *v1alpha1.BundleSpec) {
			s.Provenance = &v1alpha1.BundleProvenance{CommitSHA: "unknown"}
		}},
	} {
		err := e.Client.Create(ctx, bundle(tc.name, tc.mutate))
		require.Error(t, err, tc.name)
		assert.True(t, apierrors.IsInvalid(err), "%s: %v", tc.name, err)
		assert.Contains(t, err.Error(), tc.field, tc.name)
		var got v1alpha1.Bundle
		assert.True(t, apierrors.IsNotFound(e.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: tc.name}, &got)), "%s is not stored", tc.name)
	}
	ok := bundle("valid", func(s *v1alpha1.BundleSpec) {
		s.Images[0].Tag = "1.2.3-rc_1"
		s.Images[0].Digest = digest
		s.Provenance = &v1alpha1.BundleProvenance{CommitSHA: digest}
	})
	require.NoError(t, e.Client.Create(ctx, ok), "valid tag, digest and a digest as provenance.commitSHA")
}

// TestBundle_ArtifactImmutable: the API server refuses an update that
// changes a Bundle's images, chart, configRef, provenance, type, pipeline or
// intent (QA #1521: gates and image verification were checked against the
// artifact, so an edit would promote something nobody checked; an intent
// edit applied only at a later re-translation). Metadata stays editable, and
// the Bundle still promotes after such an edit.
//
// Covers BUNDLE-IMMUTABLE-01.
func TestBundle_ArtifactImmutable(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)

	update := func(name string, edit func(b *v1alpha1.Bundle)) error {
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var cur v1alpha1.Bundle
			if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: name}, &cur); err != nil {
				return err
			}
			edit(&cur)
			return e.Client.Update(ctx, &cur)
		})
	}
	refused := func(name, field string, edit func(b *v1alpha1.Bundle)) {
		t.Helper()
		err := update(name, edit)
		require.Error(t, err, field)
		assert.True(t, apierrors.IsInvalid(err), "%s: %v", field, err)
		assert.Contains(t, err.Error(), field+" is immutable", field)
	}
	refused(bundle, "spec.images", func(b *v1alpha1.Bundle) { b.Spec.Images[0].Tag = fixtures.V3 })
	refused(bundle, "spec.images", func(b *v1alpha1.Bundle) {
		b.Spec.Images = append(b.Spec.Images, v1alpha1.ImageRef{Repository: fixtures.Image, Tag: fixtures.V3})
	})
	refused(bundle, "spec.provenance", func(b *v1alpha1.Bundle) {
		b.Spec.Provenance = &v1alpha1.BundleProvenance{Author: "someone-else", CommitSHA: "abcdef0"}
	})
	refused(bundle, "spec.type", func(b *v1alpha1.Bundle) { b.Spec.Type = "mixed" })
	refused(bundle, "spec.pipeline", func(b *v1alpha1.Bundle) { b.Spec.Pipeline = "other" })
	refused(bundle, "spec.intent", func(b *v1alpha1.Bundle) {
		b.Spec.Intent = &v1alpha1.BundleIntent{TargetEnvironment: "test"}
	})

	// A config Bundle's configRef and a chart Bundle's chart, for a
	// Pipeline that does not exist, so nothing is promoted.
	cfgBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "podinfo-config-immutable", Namespace: a.ns},
		Spec: v1alpha1.BundleSpec{Type: "config", Pipeline: "no-such-pipeline",
			ConfigRef: &v1alpha1.ConfigRef{GitRepo: "https://example.com/config.git", CommitSHA: strings.Repeat("a", 40)}},
	}
	require.NoError(t, e.Client.Create(ctx, cfgBundle))
	config := cfgBundle.Name
	refused(config, "spec.configRef", func(b *v1alpha1.Bundle) { b.Spec.ConfigRef.CommitSHA = strings.Repeat("0", 40) })
	refused(config, "spec.configRef", func(b *v1alpha1.Bundle) { b.Spec.ConfigRef.GitRepo = "https://example.com/other.git" })
	chart := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "podinfo-chart-immutable", Namespace: a.ns},
		Spec: v1alpha1.BundleSpec{Type: "chart", Pipeline: "no-such-pipeline",
			Chart: &v1alpha1.ChartRef{Name: "podinfo", Version: "6.14.1"}},
	}
	require.NoError(t, e.Client.Create(ctx, chart))
	refused(chart.Name, "spec.chart", func(b *v1alpha1.Bundle) { b.Spec.Chart.Version = "6.15.0" })
	refused(chart.Name, "spec.chart", func(b *v1alpha1.Bundle) { b.Spec.Chart.Digest = "sha256:" + strings.Repeat("a", 64) })

	require.NoError(t, update(bundle, func(b *v1alpha1.Bundle) {
		if b.Annotations == nil {
			b.Annotations = map[string]string{}
		}
		b.Annotations["e2e.kardinal.io/note"] = "metadata stays editable"
	}), "metadata stays editable")

	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	a.running(t, "test", imageV2, "the Bundle's original image")
}
