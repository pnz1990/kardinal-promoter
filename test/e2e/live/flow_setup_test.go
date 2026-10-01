//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// This file holds the setup the flow suites (graph, bundle, pipeline, step,
// audit, examples) share on top of setup_test.go.

// Images the flow suites promote.
var (
	imageV1 = fixtures.Image + ":" + fixtures.V1
	imageV2 = fixtures.Image + ":" + fixtures.V2
	imageV3 = fixtures.Image + ":" + fixtures.V3
)

// newArgoAppFiles is newArgoApp over the repo files(ns) returns instead of
// fixtures.KustomizeRepo. The Deployments must be fixtures.Workload(env) in
// ns, running imageV1 first.
func newArgoAppFiles(t *testing.T, e *framework.Env, files func(ns string) map[string][]byte, envs ...string) *app {
	t.Helper()
	ns := e.Namespace(t)
	a := &app{e: e, ns: ns, envs: envs, repo: e.Repo(t, ns, files(ns))}
	for _, env := range envs {
		e.ArgoApp(t, a.argoApp(env), a.repo, fixtures.Path(env), ns)
	}
	for _, env := range envs {
		e.WaitArgoApp(t, a.argoApp(env), syncTimeout)
		e.WaitDeploymentImage(t, ns, fixtures.Workload(env), imageV1, syncTimeout)
	}
	return a
}

// envSpec returns p's environment name, failing the test when there is none.
func envSpec(t *testing.T, p *v1alpha1.Pipeline, name string) *v1alpha1.EnvironmentSpec {
	t.Helper()
	for i := range p.Spec.Environments {
		if p.Spec.Environments[i].Name == name {
			return &p.Spec.Environments[i]
		}
	}
	t.Fatalf("pipeline %s has no environment %s", p.Name, name)
	return nil
}

// createBundle creates a Bundle of the app's Pipeline from spec the way
// `kardinal create bundle` does (GenerateName, kardinal.io/created-at), for
// fields the CLI has no flag for, such as intent. Type defaults to image.
func (a *app) createBundle(t *testing.T, spec v1alpha1.BundleSpec) string {
	t.Helper()
	if spec.Pipeline == "" {
		spec.Pipeline = pipelineName
	}
	if spec.Type == "" {
		spec.Type = "image"
	}
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: spec.Pipeline + "-",
			Namespace:    a.ns,
			Annotations:  map[string]string{"kardinal.io/created-at": time.Now().UTC().Format(time.RFC3339Nano)},
		},
		Spec: spec,
	}
	if err := a.e.Client.Create(context.Background(), b); err != nil {
		t.Fatalf("create Bundle: %v", err)
	}
	t.Logf("Bundle %s created", b.Name)
	return b.Name
}

// podinfoImages is the images list of a Bundle that promotes podinfo at tag.
func podinfoImages(tag string) []v1alpha1.ImageRef {
	return []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: tag}}
}

// noStep checks the Bundle gets no PromotionStep for env during d.
func (a *app) noStep(t *testing.T, bundle, env string, d time.Duration) {
	t.Helper()
	framework.Consistently(t, d, fmt.Sprintf("no %s step for %s", env, bundle), func(ctx context.Context) (bool, string) {
		ps, ok, err := a.e.Step(ctx, a.ns, pipelineName, bundle, env)
		if err != nil {
			return false, err.Error()
		}
		if ok {
			return false, fmt.Sprintf("step %s exists: state=%q message=%q", ps.Name, ps.Status.State, ps.Status.Message)
		}
		return true, ""
	})
}

// verifiedAt is when ps turned Verified: entering Verified completes every
// status.steps entry, so it is the latest completion time.
func verifiedAt(t *testing.T, ps *v1alpha1.PromotionStep) time.Time {
	t.Helper()
	var last time.Time
	for _, s := range ps.Status.Steps {
		if s.CompletedAt != nil && s.CompletedAt.After(last) {
			last = s.CompletedAt.Time
		}
	}
	if last.IsZero() {
		t.Fatalf("step %s has no completed status.steps entry", ps.Name)
	}
	return last
}

// startedAfter checks that step later was created no earlier than time at, at
// the one-second precision of creationTimestamp.
func startedAfter(t *testing.T, later *v1alpha1.PromotionStep, at time.Time, what string) {
	t.Helper()
	created := later.CreationTimestamp.Time
	if created.Before(at.Truncate(time.Second)) {
		t.Errorf("%s: step %s was created at %s, before %s", what, later.Name,
			created.Format(time.RFC3339), at.Format(time.RFC3339))
	}
}

// updatePipeline applies change to the app's live Pipeline, retrying on
// conflicts with the controller's status writes.
func (a *app) updatePipeline(t *testing.T, name string, change func(*v1alpha1.Pipeline)) {
	t.Helper()
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var p v1alpha1.Pipeline
		if err := a.e.Client.Get(context.Background(), types.NamespacedName{Namespace: a.ns, Name: name}, &p); err != nil {
			return err
		}
		change(&p)
		return a.e.Client.Update(context.Background(), &p)
	})
	if err != nil {
		t.Fatalf("update Pipeline %s: %v", name, err)
	}
}

// prHead is the branch kardinal pushes a pr-review promotion of bundle to env
// to (docs/pr-evidence.md).
func prHead(bundle, env string) string { return "kardinal/" + bundle + "/" + env }

// openPR waits for the open promotion PR of bundle to env.
func (a *app) openPR(t *testing.T, bundle, env string) gitserver.PR {
	t.Helper()
	return a.e.WaitPR(t, a.repo, time.Minute, fmt.Sprintf("the open %s PR of %s", env, bundle), func(pr gitserver.PR) bool {
		return pr.State == "open" && pr.Head == prHead(bundle, env)
	})
}

// merge merges pr. Right after a push to the PR branch Forgejo answers 405
// "Please try again later" while it rechecks mergeability; a reviewer would
// click again, so merge retries that answer.
func (a *app) merge(t *testing.T, pr gitserver.PR) {
	t.Helper()
	framework.Eventually(t, time.Minute, fmt.Sprintf("merge PR #%d", pr.Number), func(ctx context.Context) (bool, string) {
		err := a.e.Git.MergePR(ctx, a.repo, pr.Number)
		if err != nil && strings.Contains(err.Error(), "try again later") {
			return false, err.Error()
		}
		if err != nil {
			t.Fatalf("merge PR #%d: %v", pr.Number, err)
		}
		return true, ""
	})
}

// fileHas checks that env's kustomization.yaml on the default branch pins
// podinfo to tag.
func (a *app) fileHas(t *testing.T, env, tag, why string) {
	t.Helper()
	got := a.e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path(env)+"/kustomization.yaml")
	if !strings.Contains(got, "newTag: "+tag) {
		t.Errorf("%s: %s kustomization.yaml does not pin %s:\n%s", why, env, tag, got)
	}
}

// running checks that env's Deployment runs image now.
func (a *app) running(t *testing.T, env, image, why string) {
	t.Helper()
	if got := a.e.DeploymentImage(t, a.ns, fixtures.Workload(env)); got != image {
		t.Errorf("%s: %s runs %s, want %s", why, env, got, image)
	}
}

// stepCount returns how many PromotionSteps bundle has.
func (a *app) stepCount(t *testing.T, bundle string) int {
	t.Helper()
	steps, err := a.e.Steps(context.Background(), a.ns, pipelineName, bundle)
	if err != nil {
		t.Fatalf("list steps of %s: %v", bundle, err)
	}
	return len(steps)
}

// bundle reads the Bundle name.
func (a *app) bundle(t *testing.T, name string) *v1alpha1.Bundle {
	t.Helper()
	var b v1alpha1.Bundle
	if err := a.e.Client.Get(context.Background(), types.NamespacedName{Namespace: a.ns, Name: name}, &b); err != nil {
		t.Fatalf("get Bundle %s: %v", name, err)
	}
	return &b
}

// failedWith returns a check for WaitBundle: the Bundle is Failed with
// condition InvalidSpec True, reason, and a message containing substr.
func failedWith(reason, substr string) func(*v1alpha1.Bundle) (bool, string) {
	return func(b *v1alpha1.Bundle) (bool, string) {
		ok, seen := framework.CondIs(b.Status.Conditions, "InvalidSpec", metav1.ConditionTrue, reason)
		if c := findCond(b.Status.Conditions, "InvalidSpec"); ok && !strings.Contains(c.Message, substr) {
			ok = false
		}
		return ok && b.Status.Phase == "Failed", fmt.Sprintf("phase=%q %s", b.Status.Phase, seen)
	}
}

// findCond returns the condition of type typ, or an empty one.
func findCond(conds []metav1.Condition, typ string) metav1.Condition {
	for _, c := range conds {
		if c.Type == typ {
			return c
		}
	}
	return metav1.Condition{}
}
