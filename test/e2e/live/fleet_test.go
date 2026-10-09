//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// fleetOverlay is a target directory that deploys a ConfigMap, and with
// workload a one-replica pause Deployment pinned to fixtures.Pause:PauseV1:
// the fleet tests measure kardinal's pacing, not 50 applications starting.
// Argo CD targets need the workload: when several targets push to one
// branch at once, Argo CD can sync a later commit than a target's own, and
// the argocd health check accepts it only when the Application runs the
// Bundle images (docs/health-adapters.md).
func fleetOverlay(ns, name string, workload bool) map[string][]byte {
	files := map[string][]byte{
		"kustomization.yaml": []byte(fmt.Sprintf("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"+
			"resources:\n  - configmap.yaml\nimages:\n  - name: %s\n    newTag: %s\n", fixtures.Image, fixtures.V1)),
		"configmap.yaml": []byte(fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fleet-%s\n  namespace: %s\n"+
			"data:\n  target: %s\n", name, ns, name)),
	}
	if workload {
		files["kustomization.yaml"] = []byte(fmt.Sprintf("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"+
			"resources:\n  - configmap.yaml\n  - deployment.yaml\nimages:\n  - name: %s\n    newTag: %q\n", fixtures.Pause, fixtures.PauseV1))
		files["deployment.yaml"] = []byte(fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: fleet-%[1]s
  namespace: %[2]s
spec:
  replicas: 1
  selector:
    matchLabels: {app: fleet-%[1]s}
  template:
    metadata:
      labels: {app: fleet-%[1]s}
    spec:
      containers:
        - name: pause
          image: %[3]s:%[4]s
          resources: {requests: {cpu: 1m, memory: 4Mi}}
`, name, ns, fixtures.Pause, fixtures.PauseV1))
	}
	return files
}

// fleetFiles is a repo with an overlay at fixtures.Path(env) for each env
// and one fleetOverlay per target at dir/<target>.
func fleetFiles(ns string, envs []string, dir string, targets []string, workload bool) map[string][]byte {
	files := map[string][]byte{}
	for _, env := range envs {
		files[fixtures.Path(env)+"/kustomization.yaml"] = []byte(fmt.Sprintf(
			"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nimages:\n  - name: %s\n    newTag: %s\n",
			fixtures.Image, fixtures.V1))
	}
	for _, t := range targets {
		for f, b := range fleetOverlay(ns, t, workload) {
			files[dir+"/"+t+"/"+f] = b
		}
	}
	return files
}

// fleetPromotion is test → prod (fleet) → post over repo, test and post with
// resource health on compactHealthDeployment.
func fleetPromotion(ns string, repo gitserver.Repo, fleet *v1alpha1.FleetSpec, approval string) *v1alpha1.Pipeline {
	env := func(name string) v1alpha1.EnvironmentSpec {
		return v1alpha1.EnvironmentSpec{Name: name, Path: fixtures.Path(name), Approval: "auto",
			Update: v1alpha1.UpdateConfig{Strategy: "kustomize"},
			Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "5m",
				Resource: &v1alpha1.ResourceRef{Name: compactHealthDeployment, Namespace: ns}}}
	}
	prod := env("prod")
	prod.Path = "fleet"
	prod.Approval = approval
	prod.Fleet = fleet
	return &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: pipelineName, Namespace: ns},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: repo.CloneURL, Branch: repo.Branch,
				SecretRef: &v1alpha1.SecretRef{Name: framework.GitSecretName}},
			Environments: []v1alpha1.EnvironmentSpec{env("test"), prod, env("post")},
		},
	}
}

// fleetSteps lists bundle's PromotionSteps of fleet environment fleet by
// target environment.
func fleetSteps(t *testing.T, e *framework.Env, ns, bundle, fleet string) map[string]v1alpha1.PromotionStep {
	t.Helper()
	steps, err := e.Steps(context.Background(), ns, pipelineName, bundle)
	require.NoError(t, err)
	out := map[string]v1alpha1.PromotionStep{}
	for _, s := range steps {
		if s.Labels[graph.LabelFleet] == fleet {
			out[s.Spec.Environment] = s
		}
	}
	return out
}

// maxOverlap is the most steps in flight at once: each step from its
// creation until it was Verified (an end at the same second as a start
// counts first, as the Graph admits the next target once one is Verified).
func maxOverlap(t *testing.T, steps map[string]v1alpha1.PromotionStep) int {
	t.Helper()
	type event struct {
		at    time.Time
		delta int
	}
	var events []event
	for _, s := range steps {
		end := verifiedAt(t, &s)
		require.False(t, end.IsZero(), "step %s has no completion time", s.Name)
		events = append(events, event{s.CreationTimestamp.Time, 1}, event{end.Truncate(time.Second), -1})
	}
	sort.Slice(events, func(i, j int) bool {
		if !events[i].at.Equal(events[j].at) {
			return events[i].at.Before(events[j].at)
		}
		return events[i].delta < events[j].delta
	})
	n, most := 0, 0
	for _, ev := range events {
		n += ev.delta
		most = max(most, n)
	}
	return most
}

// TestGraph_FleetFiftyApplicationsFiveAtATime promotes a fleet of 50 targets that
// a label selector picks from the Argo CD Applications (D1, #1457), with
// maxConcurrent 5. Applications without the label are not targets. The
// Pipeline resolves the selector into status.fleets; each target is an
// environment of its own ("prod-<application>") with its own PromotionStep,
// path (the Application's spec.source.path) and argocd health check on its
// Application. At most 5 targets are in flight at once and the pacing
// reaches 5; they start in name order, every target ends Verified with the
// new version in its directory, and post starts only once all 50 are.
//
// It does not run in parallel: 50 Applications and 50 pushes would slow
// Argo CD and the git server for the suite's other tests.
//
// Covers FLEET-01.
func TestGraph_FleetFiftyApplicationsFiveAtATime(t *testing.T) {
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	const n, maxConcurrent = 50, 5
	// Application names are short (a target environment is a DNS label)
	// and unique across the suite's namespaces.
	short := ns[strings.LastIndex(ns, "-")+1:]
	var names []string
	for i := range n {
		names = append(names, fmt.Sprintf("f%s-c%02d", short, i))
	}
	other := fmt.Sprintf("f%s-other", short)
	repo := e.Repo(t, ns, fleetFiles(ns, []string{"test", "post"}, "clusters", append(append([]string(nil), names...), other), true))
	createCompactHealth(t, e, ns)
	selector := map[string]string{"kardinal.io/e2e-fleet": short}
	for _, name := range names {
		e.ArgoApp(t, name, repo, "clusters/"+name, ns)
		labelArgoApp(t, e, name, selector)
	}
	e.ArgoApp(t, other, repo, "clusters/"+other, ns) // not labelled: not a target
	for _, name := range names {
		e.WaitArgoApp(t, name, syncTimeout)
	}

	p := fleetPromotion(ns, repo, &v1alpha1.FleetSpec{MaxConcurrent: maxConcurrent, Selector: &v1alpha1.FleetSelector{
		Kind: v1alpha1.FleetSelectorApplication, Namespace: framework.ArgoCDNamespace, MatchLabels: selector}}, "auto")
	a := &app{e: e, ns: ns, envs: []string{"test", "prod", "post"}, repo: repo}
	a.apply(t, p)
	framework.Eventually(t, 2*time.Minute, "status.fleets to list the 50 Applications", func(ctx context.Context) (bool, string) {
		var got v1alpha1.Pipeline
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: pipelineName}, &got); err != nil {
			return false, err.Error()
		}
		if len(got.Status.Fleets) != 1 {
			return false, fmt.Sprintf("status.fleets: %+v", got.Status.Fleets)
		}
		f := got.Status.Fleets[0]
		return f.Message == "" && len(f.Targets) == n, fmt.Sprintf("%d targets, message %q", len(f.Targets), f.Message)
	})

	bundle := e.CreateBundle(t, ns, pipelineName, "--image", fixtures.Pause+":"+fixtures.PauseV2)
	e.WaitBundlePhase(t, ns, bundle, "Verified", 25*time.Minute)

	steps := fleetSteps(t, e, ns, bundle, "prod")
	require.Len(t, steps, n, "one step per selected Application")
	assert.NotContains(t, steps, "prod-"+other, "an Application the selector does not match is not a target")
	assert.Equal(t, maxConcurrent, maxOverlap(t, steps), "at most, and up to, maxConcurrent targets in flight")
	var last time.Time
	var prev *v1alpha1.PromotionStep
	for _, name := range names {
		s := steps["prod-"+name]
		assert.Equal(t, "Verified", s.Status.State, s.Name)
		if prev != nil {
			assert.False(t, s.CreationTimestamp.Before(&prev.CreationTimestamp), "%s starts after %s", s.Name, prev.Name)
		}
		prev = &s
		last = maxTime(last, verifiedAt(t, &s))
		assert.Contains(t, e.ReadFile(t, repo, repo.Branch, "clusters/"+name+"/kustomization.yaml"), fixtures.PauseV2, name)
	}
	post, ok, err := e.Step(ctx, ns, pipelineName, bundle, "post")
	require.NoError(t, err)
	require.True(t, ok)
	startedAfter(t, post, last, "post starts after every target is Verified")
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// labelArgoApp adds labels to the Argo CD Application name.
func labelArgoApp(t *testing.T, e *framework.Env, name string, labels map[string]string) {
	t.Helper()
	var parts []string
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%q:%q", k, v))
	}
	patch := []byte(`{"metadata":{"labels":{` + strings.Join(parts, ",") + `}}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := e.Dynamic.Resource(framework.ApplicationGVR).Namespace(framework.ArgoCDNamespace).
		Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	require.NoError(t, err, "label Application %s", name)
}

// TestGraph_FleetMaxUnavailableStopsTheRollout: a fleet of 30 targets with
// maxConcurrent 3 and maxUnavailable 2, whose first two targets fail their
// health check at once (a 1s timeout on a Deployment that does not exist).
// Once both have Failed no further target starts although a place is free,
// the targets already in flight finish Verified, post never starts, and the
// Bundle is Failed.
//
// Covers FLEET-02.
func TestGraph_FleetMaxUnavailableStopsTheRollout(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	var targets []v1alpha1.FleetTarget
	var names []string
	for i := range 30 {
		name := fmt.Sprintf("t%02d", i)
		names = append(names, name)
		tg := v1alpha1.FleetTarget{Name: name}
		if i < 2 {
			// Its Deployment never exists: the health check times out.
			tg.Health = &v1alpha1.HealthConfig{Type: "resource", Timeout: "1s",
				Resource: &v1alpha1.ResourceRef{Name: "missing", Namespace: ns}}
		}
		targets = append(targets, tg)
	}
	two := 2
	repo := e.Repo(t, ns, fleetFiles(ns, []string{"test", "post"}, "fleet", names, false))
	createCompactHealth(t, e, ns)
	a := &app{e: e, ns: ns, envs: []string{"test", "prod", "post"}, repo: repo}
	a.apply(t, fleetPromotion(ns, repo, &v1alpha1.FleetSpec{MaxConcurrent: 3, MaxUnavailable: &two, Targets: targets}, "auto"))

	bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, ns, pipelineName, bundle, "prod-t00", "Failed", promoteTimeout)
	e.WaitStepState(t, ns, pipelineName, bundle, "prod-t01", "Failed", promoteTimeout)
	stoppedAt := time.Now()

	// The targets in flight when the second failure stopped the rollout
	// finish; no other starts.
	started := fleetSteps(t, e, ns, bundle, "prod")
	for env := range started {
		if env != "prod-t00" && env != "prod-t01" {
			e.WaitStepState(t, ns, pipelineName, bundle, env, "Verified", promoteTimeout)
		}
	}
	framework.Consistently(t, 45*time.Second, "no target starts after maxUnavailable failures", func(context.Context) (bool, string) {
		now := fleetSteps(t, e, ns, bundle, "prod")
		for env, s := range now {
			if _, ok := started[env]; !ok && s.CreationTimestamp.After(stoppedAt.Add(time.Second)) {
				return false, fmt.Sprintf("%s started at %s, after the rollout stopped", env, s.CreationTimestamp)
			}
		}
		return true, ""
	})
	final := fleetSteps(t, e, ns, bundle, "prod")
	assert.Less(t, len(final), len(names), "the rollout stopped before every target: %d of %d started", len(final), len(names))
	verified := 0
	for _, s := range final {
		if s.Status.State == "Verified" {
			verified++
		}
	}
	t.Logf("stopped with %d targets started, %d Verified, 2 Failed", len(final), verified)
	a.noStep(t, bundle, "post", time.Second)
	e.WaitBundlePhase(t, ns, bundle, "Failed", time.Minute)
}

// TestGraph_FleetTargetsChangedMidRollout: the targets of a fleet are edited
// while its Bundle promotes (pr-review, so each target waits on its PR). A
// target added mid-rollout gets a step and a PR in the same Bundle and is
// promoted; a target removed while its PR is open loses its step and the
// PR is closed; the targets already Verified are not promoted again, and
// post waits for the targets that remain, the new one included.
//
// Covers FLEET-04.
func TestGraph_FleetTargetsChangedMidRollout(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	repo := e.Repo(t, ns, fleetFiles(ns, []string{"test", "post"}, "fleet", []string{"a", "b", "r", "n"}, false))
	createCompactHealth(t, e, ns)
	a := &app{e: e, ns: ns, envs: []string{"test", "prod", "post"}, repo: repo}
	a.apply(t, fleetPromotion(ns, repo, &v1alpha1.FleetSpec{
		Targets: []v1alpha1.FleetTarget{{Name: "a"}, {Name: "b"}, {Name: "r"}}}, "pr-review"))

	bundle := e.CreateBundle(t, ns, pipelineName, "--image", imageV2)
	a.merge(t, a.openPR(t, bundle, "prod-a"))
	verifiedA := e.WaitStepState(t, ns, pipelineName, bundle, "prod-a", "Verified", promoteTimeout)
	e.WaitStepState(t, ns, pipelineName, bundle, "prod-b", "WaitingForMerge", promoteTimeout)
	e.WaitStepState(t, ns, pipelineName, bundle, "prod-r", "WaitingForMerge", promoteTimeout)
	prR := a.openPR(t, bundle, "prod-r")

	// r leaves the fleet and n joins it, in one edit.
	a.updatePipeline(t, pipelineName, func(p *v1alpha1.Pipeline) {
		for i := range p.Spec.Environments {
			if f := p.Spec.Environments[i].Fleet; f != nil {
				f.Targets = []v1alpha1.FleetTarget{{Name: "a"}, {Name: "b"}, {Name: "n"}}
			}
		}
	})
	framework.Eventually(t, 2*time.Minute, "the removed target's step to be deleted", func(ctx context.Context) (bool, string) {
		_, ok, err := e.Step(ctx, ns, pipelineName, bundle, "prod-r")
		return err == nil && !ok, fmt.Sprintf("exists=%v err=%v", ok, err)
	})
	e.WaitPRState(t, repo, prR.Number, "closed", 2*time.Minute)
	a.noStep(t, bundle, "post", 5*time.Second)

	a.merge(t, a.openPR(t, bundle, "prod-n"))
	a.merge(t, a.openPR(t, bundle, "prod-b"))
	e.WaitStepState(t, ns, pipelineName, bundle, "prod-n", "Verified", promoteTimeout)
	e.WaitStepState(t, ns, pipelineName, bundle, "post", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, ns, bundle, "Verified", 2*time.Minute)

	a2, ok, err := e.Step(ctx, ns, pipelineName, bundle, "prod-a")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, verifiedA.UID, a2.UID, "a Verified target is not promoted again")
	assert.Contains(t, e.ReadFile(t, repo, repo.Branch, "fleet/n/kustomization.yaml"), "newTag: "+fixtures.V2)
	assert.Contains(t, e.ReadFile(t, repo, repo.Branch, "fleet/r/kustomization.yaml"), "newTag: "+fixtures.V1,
		"the removed target's PR was closed unmerged")
}
