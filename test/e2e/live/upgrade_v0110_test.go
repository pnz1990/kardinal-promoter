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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// guideChartV0110 is the chart reference of the "Upgrading from v0.11.0"
// guide: the test replaces it with the checkout's chart.
const guideChartV0110 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.13.0"

// u0110Pipelines is how many Pipelines each estate namespace has.
const u0110Pipelines = 6

// u0110Estate is the estate the v0.11.0 release runs before the upgrade:
// five namespaces of six Pipelines each.
type u0110Estate struct {
	e *framework.Env
	// auto: test, uat, prod (auto, bake 1m on uat and prod); review: prod
	// pr-review; approve: prod behind a two-approver gate; fleet: prod a
	// fleet of three targets, one at a time, bake 1m; ops: a paused
	// Pipeline, a held one, a rejected, a failed and a superseded version.
	auto, review, approve, fleet, ops *scale.Fleet
	repos                             map[string]gitserver.Repo // namespace/pipeline → repo
}

func (s *u0110Estate) fleets() []*scale.Fleet {
	return []*scale.Fleet{s.auto, s.review, s.approve, s.fleet, s.ops}
}

func (s *u0110Estate) pipeline(t *testing.T, f *scale.Fleet, name string, dirs []string, envs []v1alpha1.EnvironmentSpec, edits ...func(*v1alpha1.Pipeline)) string {
	t.Helper()
	repo := f.Repo(t, f.NS+"-"+name, map[string][]string{name: dirs})
	p := f.PipelineSpec(name, repo, envs, edits...)
	f.Apply(t, p, repo)
	s.repos[f.NS+"/"+name] = repo
	return name
}

func u0110Name(prefix string, i int) string { return fmt.Sprintf("%s-%02d", prefix, i+1) }

// u0110Bake gives the named environments a one-minute bake, so their
// versions are in flight during the upgrade.
func u0110Bake(envs ...string) func(*v1alpha1.Pipeline) {
	return func(p *v1alpha1.Pipeline) {
		for i := range p.Spec.Environments {
			for _, e := range envs {
				if p.Spec.Environments[i].Name == e {
					p.Spec.Environments[i].Bake = &v1alpha1.BakeConfig{Minutes: 1}
				}
			}
		}
	}
}

func u0110Envs(prodApproval string) []v1alpha1.EnvironmentSpec {
	return []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "uat"}, {Name: "prod", Approval: prodApproval}}
}

// u0110Snapshot is what the estate holds at one moment.
type u0110Snapshot struct {
	bundles map[string]v1alpha1.Bundle        // namespace/name
	steps   map[string]v1alpha1.PromotionStep // namespace/name
	graphs  map[string]types.UID              // namespace/bundle → Graph uid
	prs     map[string]int                    // namespace/bundle/env → PR number (PRStatus)
}

func (s *u0110Estate) snapshot(t *testing.T) u0110Snapshot {
	t.Helper()
	ctx := context.Background()
	snap := u0110Snapshot{bundles: map[string]v1alpha1.Bundle{}, steps: map[string]v1alpha1.PromotionStep{},
		graphs: map[string]types.UID{}, prs: map[string]int{}}
	for _, f := range s.fleets() {
		var bl v1alpha1.BundleList
		require.NoError(t, s.e.Client.List(ctx, &bl, client.InNamespace(f.NS)))
		for _, b := range bl.Items {
			snap.bundles[f.NS+"/"+b.Name] = b
		}
		var sl v1alpha1.PromotionStepList
		require.NoError(t, s.e.Client.List(ctx, &sl, client.InNamespace(f.NS)))
		for _, st := range sl.Items {
			snap.steps[f.NS+"/"+st.Name] = st
		}
		graphs, err := s.e.Dynamic.Resource(framework.GraphGVR).Namespace(f.NS).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		for _, g := range graphs.Items {
			if g.GetDeletionTimestamp() == nil {
				snap.graphs[f.NS+"/"+g.GetLabels()["kardinal.io/bundle"]] = g.GetUID()
			}
		}
		var pl v1alpha1.PRStatusList
		require.NoError(t, s.e.Client.List(ctx, &pl, client.InNamespace(f.NS)))
		for _, p := range pl.Items {
			if p.Spec.PRNumber != 0 {
				snap.prs[f.NS+"/"+p.Labels["kardinal.io/bundle"]+"/"+p.Labels["kardinal.io/environment"]] = p.Spec.PRNumber
			}
		}
	}
	return snap
}

// phases counts the Bundle phases and the step states of snap.
func (snap u0110Snapshot) phases() (bundles, steps map[string]int) {
	bundles, steps = map[string]int{}, map[string]int{}
	for _, b := range snap.bundles {
		bundles[b.Status.Phase]++
	}
	for _, s := range snap.steps {
		state := s.Status.State
		if state == "" {
			state = "Pending"
		}
		steps[state]++
	}
	return bundles, steps
}

// TestUpgradeV0110_Estate builds an estate under kardinal-promoter v0.11.0 as
// released (its chart, CLI and kro) and follows "Upgrading from v0.11.0" in
// docs/upgrade.md, running the guide's own commands.
//
// The estate: 30 Pipelines in 5 namespaces (test, uat, prod each; health on
// a Deployment). At the upgrade it has versions Verified, Promoting and
// HealthChecking (a one-minute bake), WaitingForMerge with their prod PR
// open, waiting at a two-approver gate with one approval given (alice, with
// the v0.11.0 CLI) or none, promoting a fleet of three targets one at a
// time, held at prod by kardinal hold, waiting on a paused Pipeline,
// Rejected, Failed and Superseded.
//
// After the upgrade:
//   - kardinal doctor passes and every Pipeline is Ready;
//   - each in-flight version keeps its Graph (same uid), PromotionSteps and
//     PRStatus with the same PR number, and finishes: the reviewer merges the
//     open PRs, bob's approval completes alice's (2 of 2), the fleet's
//     targets finish;
//   - the pause and the hold are kept: nothing moves until kardinal resume
//     and kardinal unhold, and then the versions finish;
//   - Rejected, Failed and Superseded versions stay so;
//   - no Bundle has two Graphs, no (Bundle, environment) two steps, and no
//     PR head branch two PRs; each namespace passes the invariants
//     (terminal phases, git content, open PRs, Graphs, AuditEvents);
//   - every stored object of every kardinal kind reads with this release's
//     types, and the admission policies admit a label on the ones the
//     estate wrote;
//   - kardinal get pipelines -A, the UI API (validated against the OpenAPI
//     document) and a new version promoting end to end work;
//   - the actors (docs/actors.md): the release's AgentIndex is built and the
//     actor policies are bound; versions v0.11.0 stored have no actor; a
//     version kardinal create bundle makes now is stamped with its creator.
//
// Covers UPG-V0110-01, UPG-V0110-02, UPG-V0110-03, UPG-V0110-04, UPG-V0110-05.
func TestUpgradeV0110_Estate(t *testing.T) {
	e := framework.New(t)
	ctx := context.Background()
	t.Logf("Kubernetes 1.%d", serverMinor(t, e))
	seed, err := scale.Seed()
	require.NoError(t, err)
	s := &u0110Estate{e: e, repos: map[string]gitserver.Repo{}}
	newFleet := func(name string) *scale.Fleet { return scale.NewFleet(t, e, scale.NewRNG(seed, name)) }
	s.auto, s.review, s.approve, s.fleet, s.ops = newFleet("auto"), newFleet("review"), newFleet("approve"), newFleet("fleet"), newFleet("ops")
	dirs := []string{"test", "uat", "prod"}
	bundle := func(f *scale.Fleet, p string, i int) string { return f.MustCreateBundle(t, p, scale.Tag(p, i)).Name }

	// ── The estate, under v0.11.0 ───────────────────────────────────────
	// approve: one two-approver gate in front of every prod.
	gate := framework.Gate(s.approve.NS, "two-approvers", "prod", "true", recheck)
	gate.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 2, AllowedGroups: []string{"release-managers"}}
	e.CreateGate(t, gate)
	alice := userKubeconfig(t, e, s.approve.NS, "alice@example.com", "release-managers")
	bob := userKubeconfig(t, e, s.approve.NS, "bob@example.com", "release-managers")

	var auto, review, approve, fleets []string
	for i := 0; i < u0110Pipelines; i++ {
		auto = append(auto, s.pipeline(t, s.auto, u0110Name("auto", i), dirs, u0110Envs(""), u0110Bake("uat", "prod")))
		review = append(review, s.pipeline(t, s.review, u0110Name("review", i), dirs, u0110Envs("pr-review")))
		approve = append(approve, s.pipeline(t, s.approve, u0110Name("approve", i), dirs, u0110Envs("")))
		fleets = append(fleets, s.pipeline(t, s.fleet, u0110Name("fleet", i), []string{"test", "prod/eu", "prod/us", "prod/ap"},
			[]v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod", Bake: &v1alpha1.BakeConfig{Minutes: 2},
				Fleet: &v1alpha1.FleetSpec{MaxConcurrent: 1, Targets: []v1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}, {Name: "ap"}}}}}))
	}
	paused := s.pipeline(t, s.ops, "paused", dirs, u0110Envs(""))
	held := s.pipeline(t, s.ops, "held", dirs, u0110Envs(""))
	rejected := s.pipeline(t, s.ops, "rejected", dirs, u0110Envs("pr-review"))
	failed := s.pipeline(t, s.ops, "failed", dirs, u0110Envs(""), func(p *v1alpha1.Pipeline) {
		// No such Deployment: the test environment's health check fails.
		p.Spec.Environments[0].Health.Resource.Name = "no-such-deployment"
		p.Spec.Environments[0].Health.Timeout = "30s"
	})
	superseded := s.pipeline(t, s.ops, "superseded", dirs, u0110Envs("pr-review"))
	steady := s.pipeline(t, s.ops, "steady", dirs, u0110Envs(""))
	t.Logf("estate: %d Pipelines in %d namespaces", 4*u0110Pipelines+6, len(s.fleets()))

	// Versions that finish under v0.11.0.
	settled := map[string]string{} // namespace/bundle → pipeline
	for _, p := range auto[:3] {
		settled[s.auto.NS+"/"+bundle(s.auto, p, 1)] = p
	}
	settled[s.ops.NS+"/"+bundle(s.ops, held, 1)] = held
	settled[s.ops.NS+"/"+bundle(s.ops, steady, 1)] = steady
	for k := range settled {
		ns, name, _ := strings.Cut(k, "/")
		e.WaitBundlePhase(t, ns, name, "Verified", promoteTimeout)
	}

	// The held Pipeline: prod held on its Verified version; the next one
	// waits at prod.
	out, err := e.KardinalV0110(t, s.ops.NS, "hold", held, "prod", "--reason", "upgrade window")
	require.NoError(t, err, out)
	heldNext := bundle(s.ops, held, 2)
	e.WaitStepState(t, s.ops.NS, held, heldNext, "uat", "Verified", promoteTimeout)

	// The paused Pipeline: its version waits before test.
	out, err = e.KardinalV0110(t, s.ops.NS, "pause", paused)
	require.NoError(t, err, out)
	pausedB := bundle(s.ops, paused, 1)

	// Rejected, Failed, Superseded.
	rejectedB := bundle(s.ops, rejected, 1)
	e.WaitStepState(t, s.ops.NS, rejected, rejectedB, "prod", "WaitingForMerge", promoteTimeout)
	out, err = e.KardinalV0110(t, s.ops.NS, "reject", rejectedB, "--reason", "CVE in the base image")
	require.NoError(t, err, out)
	e.WaitBundlePhase(t, s.ops.NS, rejectedB, "Rejected", time.Minute)
	failedB := bundle(s.ops, failed, 1)
	e.WaitBundlePhase(t, s.ops.NS, failedB, "Failed", promoteTimeout)
	oldB := bundle(s.ops, superseded, 1)
	e.WaitStepState(t, s.ops.NS, superseded, oldB, "prod", "WaitingForMerge", promoteTimeout)
	newB := bundle(s.ops, superseded, 2)
	e.WaitBundlePhase(t, s.ops.NS, oldB, "Superseded", promoteTimeout)

	// Versions in flight across the upgrade.
	inFlight := map[string]string{} // namespace/bundle → pipeline
	add := func(f *scale.Fleet, p string, i int) string {
		b := bundle(f, p, i)
		inFlight[f.NS+"/"+b] = p
		return b
	}
	for _, p := range review {
		add(s.review, p, 1)
	}
	approvalB := map[string]string{}
	for _, p := range approve {
		approvalB[p] = add(s.approve, p, 1)
	}
	inFlight[s.ops.NS+"/"+newB] = superseded
	inFlight[s.ops.NS+"/"+heldNext] = held
	inFlight[s.ops.NS+"/"+pausedB] = paused
	for _, p := range review {
		var b string
		for k, v := range inFlight {
			if v == p {
				_, b, _ = strings.Cut(k, "/")
			}
		}
		e.WaitStepState(t, s.review.NS, p, b, "prod", "WaitingForMerge", promoteTimeout)
	}
	e.WaitStepState(t, s.ops.NS, superseded, newB, "prod", "WaitingForMerge", promoteTimeout)
	for i, p := range approve {
		e.WaitGateReady(t, s.approve.NS, approvalB[p], "prod", "two-approvers", false, "waiting for approvals: 0 of 2", gateTimeout)
		if i%2 == 0 {
			// alice approves half of them with the v0.11.0 CLI.
			out, err := e.KardinalV0110(t, s.approve.NS, "--kubeconfig", alice, "approve", approvalB[p], "--env", "prod")
			require.NoError(t, err, out)
			e.WaitGateReady(t, s.approve.NS, approvalB[p], "prod", "two-approvers", false, "waiting for approvals: 1 of 2 (alice@example.com)", gateTimeout)
		}
	}
	// The fleet and the auto namespace last, so their bakes run through the
	// upgrade: three targets one at a time with a two-minute bake each.
	fleetB := map[string]string{}
	for _, p := range fleets {
		fleetB[p] = add(s.fleet, p, 1)
	}
	var autoSuperseded string
	for i, p := range auto {
		b := add(s.auto, p, 2)
		if i == 0 {
			autoSuperseded = b
			add(s.auto, p, 3) // supersedes the one just created
		}
	}
	framework.Eventually(t, 3*time.Minute, "a step HealthChecking (baking) in the auto namespace", func(ctx context.Context) (bool, string) {
		var sl v1alpha1.PromotionStepList
		if err := e.Client.List(ctx, &sl, client.InNamespace(s.auto.NS)); err != nil {
			return false, err.Error()
		}
		for _, st := range sl.Items {
			if st.Status.State == "HealthChecking" {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d steps, none HealthChecking", len(sl.Items))
	})

	// The upgrade starts only once every fleet version is part-way through
	// its targets: one started, not every one Verified.
	for _, p := range fleets {
		b := fleetB[p]
		framework.Eventually(t, promoteTimeout, "fleet version "+b+" part-way through its targets", func(ctx context.Context) (bool, string) {
			started, verified := 0, 0
			for _, target := range []string{"prod-eu", "prod-us", "prod-ap"} {
				st, ok, err := e.Step(ctx, s.fleet.NS, p, b, target)
				if err != nil {
					return false, err.Error()
				}
				if ok {
					started++
					if st.Status.State == "Verified" {
						verified++
					}
				}
			}
			return started > 0 && verified < 3, fmt.Sprintf("%d targets started, %d Verified", started, verified)
		})
	}

	before := s.snapshot(t)
	// Every version meant to be in flight is, at the snapshot, and every
	// fleet has a prod target left.
	for k := range inFlight {
		if strings.HasSuffix(k, "/"+autoSuperseded) {
			continue // its successor may have superseded it already
		}
		b, ok := before.bundles[k]
		if assert.True(t, ok, "%s exists at the upgrade", k) {
			assert.NotContains(t, []string{"Verified", "Failed", "Superseded", "Rejected"}, b.Status.Phase,
				"%s is in flight at the upgrade", k)
		}
	}
	for _, p := range fleets {
		verified := 0
		for _, st := range before.steps {
			if st.Spec.BundleName == fleetB[p] && strings.HasPrefix(st.Spec.Environment, "prod-") && st.Status.State == "Verified" {
				verified++
			}
		}
		assert.Less(t, verified, 3, "fleet version %s has a prod target not Verified at the upgrade", fleetB[p])
	}
	bp, sp := before.phases()
	t.Logf("before the upgrade: Bundles %v, steps %v, %d Graphs, %d PRs", bp, sp, len(before.graphs), len(before.prs))
	for _, phase := range []string{"Verified", "Promoting", "Rejected", "Failed", "Superseded"} {
		assert.NotZero(t, bp[phase], "the estate has a %s version at the upgrade: %v", phase, bp)
	}
	for _, state := range []string{"Verified", "WaitingForMerge", "HealthChecking"} {
		assert.NotZero(t, sp[state], "the estate has a %s step at the upgrade: %v", state, sp)
	}

	// v0.12 bound the reader ClusterRole in every Graph namespace for the
	// health ref nodes, and recorded it on the applier RoleBinding (#1283).
	readerRole := framework.ChartFullname(upgradeRelease) + "-graph-reader"
	for _, f := range s.fleets() {
		_, err := e.Kube.RbacV1().RoleBindings(f.NS).Get(context.Background(), readerRole+"-"+f.NS, metav1.GetOptions{})
		require.NoError(t, err, "v0.12 has a reader RoleBinding in %s", f.NS)
	}

	// ── 2. Apply the new CRDs ───────────────────────────────────────────
	e.MustShell(t, framework.Fill(t, framework.GuideBlockFrom(t, "0.11.0", "helm show crds"), guideChartV0110, `"$KARDINAL_E2E_CHART"`))

	// ── 3. Upgrade the chart ────────────────────────────────────────────
	rel := e.ExistingRelease(t, upgradeRelease, framework.ControllerNamespace)
	upgradeCmd := strings.TrimRight(framework.Fill(t, framework.GuideBlockFrom(t, "0.11.0", "helm upgrade"), guideChartV0110, `"$KARDINAL_E2E_CHART"`), "\n") + ` \
  --set image.repository="${KARDINAL_E2E_IMAGE%:*}" --set image.tag="${KARDINAL_E2E_IMAGE##*:}" --set image.pullPolicy=Never
`
	// The version the old leader recorded: the new one records its own once it
	// leads, a few seconds after its Pod is Ready (#2383).
	leaderBefore := rel.LeaderVersion(t)
	up := e.Shell(t, upgradeCmd)
	require.Zero(t, up.Code, up.Output)
	rel.WaitRolledOut(t, 3*time.Minute)
	// The guide waits until kardinal version shows the new controller.
	rel.WaitLeaderVersionChanged(t, leaderBefore, 2*time.Minute)

	// ── 4. Check the result ─────────────────────────────────────────────
	check := e.MustShell(t, framework.GuideBlockFrom(t, "0.11.0", "kardinal doctor"))
	assert.Regexp(t, `(?m)^\d+ check\(s\) passed$`, check, "kardinal doctor passes every check")
	for _, f := range s.fleets() {
		ns := f.NS
		framework.Eventually(t, 2*time.Minute, "every Pipeline of "+ns+" Ready", func(ctx context.Context) (bool, string) {
			var pl v1alpha1.PipelineList
			if err := e.Client.List(ctx, &pl, client.InNamespace(ns)); err != nil {
				return false, err.Error()
			}
			var not []string
			for _, p := range pl.Items {
				if c := meta.FindStatusCondition(p.Status.Conditions, "Ready"); c == nil || c.Status != metav1.ConditionTrue {
					not = append(not, fmt.Sprintf("%s: %v", p.Name, c))
				}
			}
			return len(not) == 0, strings.Join(not, "; ")
		})
	}

	// #1283: the reader ClusterRole is gone; the controller deletes the
	// reader RoleBindings v0.12 recorded, and their record, and removes the
	// health ref nodes and reader labels from every Graph.
	_, err = e.Kube.RbacV1().ClusterRoles().Get(context.Background(), readerRole, metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "the upgrade deletes ClusterRole %s: %v", readerRole, err)
	for _, f := range s.fleets() {
		ns := f.NS
		framework.Eventually(t, 2*time.Minute, "the reader RoleBinding of "+ns+" deleted, and its record", func(ctx context.Context) (bool, string) {
			if _, err := e.Kube.RbacV1().RoleBindings(ns).Get(ctx, readerRole+"-"+ns, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				return false, fmt.Sprintf("reader RoleBinding: %v", err)
			}
			applier, err := e.Kube.RbacV1().RoleBindings(ns).Get(ctx, framework.ChartFullname(upgradeRelease)+"-graph-applier", metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			rec, ok := applier.Annotations["kardinal.io/reader-namespaces"]
			return !ok, "record: " + rec
		})
		framework.Eventually(t, 2*time.Minute, "the Graphs of "+ns+" without health ref nodes", func(ctx context.Context) (bool, string) {
			list, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, err.Error()
			}
			var left []string
			for _, g := range list.Items {
				nodes, _, _ := unstructured.NestedSlice(g.Object, "spec", "nodes")
				for _, n := range nodes {
					if id, _ := n.(map[string]interface{})["id"].(string); strings.HasPrefix(id, "health") && len(id) > 6 && id[6] >= 'A' && id[6] <= 'Z' {
						left = append(left, g.GetName()+"/"+id)
					}
				}
				if _, ok := g.GetLabels()["kardinal.io/reader-labels"]; ok {
					left = append(left, g.GetName()+" reader label")
				}
			}
			return len(left) == 0, strings.Join(left, ", ")
		})
	}

	// Right after the upgrade, every in-flight version keeps its Graph and
	// its steps, and every open PR is the same PR.
	after := s.snapshot(t)
	for k := range inFlight {
		if uid, ok := before.graphs[k]; ok {
			assert.Equal(t, uid, after.graphs[k], "%s keeps its Graph (same uid)", k)
		}
	}
	for k, st := range before.steps {
		ns, _, _ := strings.Cut(k, "/")
		if _, ok := inFlight[ns+"/"+st.Spec.BundleName]; !ok {
			continue
		}
		now, ok := after.steps[k]
		if assert.True(t, ok, "step %s survives the upgrade", k) {
			assert.Equal(t, st.UID, now.UID, "step %s is the same object", k)
		}
	}
	for k, n := range before.prs {
		ns, rest, _ := strings.Cut(k, "/")
		b, _, _ := strings.Cut(rest, "/")
		if _, ok := inFlight[ns+"/"+b]; ok {
			assert.Equal(t, n, after.prs[k], "%s keeps PR #%d", k, n)
		}
	}

	// The pause and the hold are kept: nothing moves.
	var pp, hp v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: s.ops.NS, Name: paused}, &pp))
	assert.True(t, pp.Spec.Paused, "the paused Pipeline stays paused")
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: s.ops.NS, Name: held}, &hp))
	require.Len(t, hp.Spec.Holds, 1, "the hold is kept")
	assert.Equal(t, "prod", hp.Spec.Holds[0].Environment)
	framework.Consistently(t, 30*time.Second, "the paused version has no step, the held one no prod step", func(ctx context.Context) (bool, string) {
		if st, ok, err := e.Step(ctx, s.ops.NS, paused, pausedB, "test"); err != nil || (ok && st.Status.State != "" && st.Status.State != "Pending") {
			return false, fmt.Sprintf("paused: %v %v", st, err)
		}
		// A held-back version gets no step in the held environment at all.
		if st, ok, err := e.Step(ctx, s.ops.NS, held, heldNext, "prod"); err != nil || ok {
			return false, fmt.Sprintf("held prod: %v %v", st, err)
		}
		return true, ""
	})

	// ── Everything in flight finishes ───────────────────────────────────
	var reviewRepos []gitserver.Repo
	for _, p := range review {
		reviewRepos = append(reviewRepos, s.repos[s.review.NS+"/"+p])
	}
	reviewRepos = append(reviewRepos, s.repos[s.ops.NS+"/"+superseded])
	scale.Review(t, e.Git, nil, reviewRepos...)

	// bob, with this release's CLI, approves every gate: alice's approvals
	// from before the upgrade make 2 of 2; the others wait for alice.
	c := e.CLI(t)
	for i, p := range approve {
		r := c.Exec(framework.CLIOptions{Kubeconfig: bob}, c.Args(s.approve.NS, "approve", approvalB[p], "--env", "prod")...)
		require.Equal(t, 0, r.Code, r.Output())
		if i%2 == 0 {
			e.WaitGateReady(t, s.approve.NS, approvalB[p], "prod", "two-approvers", true, "(2 of 2)", gateTimeout)
		} else {
			e.WaitGateReady(t, s.approve.NS, approvalB[p], "prod", "two-approvers", false, "waiting for approvals: 1 of 2 (bob@example.com)", gateTimeout)
			r = c.Exec(framework.CLIOptions{Kubeconfig: alice}, c.Args(s.approve.NS, "approve", approvalB[p], "--env", "prod")...)
			require.Equal(t, 0, r.Code, r.Output())
		}
	}
	out, err = e.Kardinal(t, s.ops.NS, "resume", paused)
	require.NoError(t, err, out)
	out, err = e.Kardinal(t, s.ops.NS, "unhold", held, "prod")
	require.NoError(t, err, out)

	for k := range inFlight {
		ns, b, _ := strings.Cut(k, "/")
		want := "Verified"
		if b == autoSuperseded {
			want = "Superseded"
		}
		e.WaitBundlePhase(t, ns, b, want, 2*promoteTimeout)
	}
	for k := range settled {
		ns, b, _ := strings.Cut(k, "/")
		phase := e.WaitBundlePhase(t, ns, b, "Verified", time.Minute).Status.Phase
		assert.Equal(t, "Verified", phase, "%s stays Verified", k)
	}
	e.WaitBundlePhase(t, s.ops.NS, rejectedB, "Rejected", time.Minute)
	e.WaitBundlePhase(t, s.ops.NS, failedB, "Failed", time.Minute)
	e.WaitBundlePhase(t, s.ops.NS, oldB, "Superseded", time.Minute)

	// ── Nothing orphaned or duplicated ──────────────────────────────────
	end := s.snapshot(t)
	stepsPer := map[string][]string{}
	for k, st := range end.steps {
		ns, _, _ := strings.Cut(k, "/")
		key := ns + "/" + st.Spec.BundleName + "/" + st.Spec.Environment
		stepsPer[key] = append(stepsPer[key], st.Name)
	}
	for key, names := range stepsPer {
		assert.Len(t, names, 1, "one PromotionStep for %s: %v", key, names)
	}
	for _, f := range s.fleets() {
		graphs, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(f.NS).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		per := map[string]int{}
		for _, g := range graphs.Items {
			per[g.GetLabels()["kardinal.io/bundle"]]++
		}
		for b, n := range per {
			assert.Equal(t, 1, n, "Bundle %s/%s has one Graph", f.NS, b)
		}
	}
	for k, repo := range s.repos {
		prs, err := e.Git.PullRequests(ctx, repo)
		require.NoError(t, err)
		heads := map[string][]int{}
		for _, pr := range prs {
			heads[pr.Head] = append(heads[pr.Head], pr.Number)
		}
		for h, nums := range heads {
			assert.Len(t, nums, 1, "%s: one PR for head %s, got %v", k, h, nums)
		}
	}
	reasons := map[*scale.Fleet]string{
		s.ops: "the ops namespace keeps a Rejected and a Failed version on purpose",
	}
	for _, f := range s.fleets() {
		o := invariants.Options{Namespace: f.NS, Targets: f.Targets(), Image: scale.ImageRepo, SeedTag: scale.SeedTag,
			Start: time.Now().Add(-time.Hour), AllowEmpty: false}
		if why, ok := reasons[f]; ok {
			o.Outcome, o.OutcomeWhy = invariants.OutcomeAny, why
		}
		if f == s.fleet {
			o.Skip = map[string]string{"env-content": "the fleet targets' directories are apps/<pipeline>/prod/<target>, which the check does not read; the steps are Verified"}
		}
		invariants.Check(t, e, o)
	}

	// ── Every stored object reads, and takes a write ────────────────────
	u0110AllKinds(t, e, s)

	// ── The CLI, the UI API and a new version ───────────────────────────
	r := c.Run("", "get", "pipelines", "-A")
	require.Equal(t, 0, r.Code, r.Output())
	for _, f := range s.fleets() {
		assert.Contains(t, r.Stdout, f.NS, "kardinal get pipelines -A lists %s", f.NS)
	}
	// The one error is the version the estate failed on purpose.
	for _, line := range strings.Split(r.Output(), "\n") {
		if strings.HasPrefix(line, "ERROR:") {
			assert.Contains(t, line, "/"+failed+": environment test: health alarm", "the only error is the failed Pipeline's")
		}
	}
	spec := framework.LoadOpenAPI(t)
	ui := mainUI(t, e)
	res := ui.Get(t, uiAPI+"/pipelines")
	require.Equal(t, 200, res.Status, res.String())
	assert.Empty(t, spec.Validate(spec.ResponseSchema(t, "GET", "/api/v1/ui/pipelines", 200), []byte(res.Body)))
	var listed []uiPipeline
	res.JSON(t, &listed)
	assert.GreaterOrEqual(t, len(listed), 4*u0110Pipelines+6, "the UI lists every Pipeline")
	res = ui.Get(t, uiAPI+"/gates")
	require.Equal(t, 200, res.Status, res.String())
	assert.Empty(t, spec.Validate(spec.ResponseSchema(t, "GET", "/api/v1/ui/gates", 200), []byte(res.Body)))
	res = ui.Get(t, uiAPI+"/pipelines/"+approve[0]+"/bundles?namespace="+s.approve.NS)
	require.Equal(t, 200, res.Status, res.String())
	assert.Empty(t, spec.Validate(spec.ResponseSchema(t, "GET", "/api/v1/ui/pipelines/{pipeline}/bundles", 200), []byte(res.Body)))

	fresh := bundle(s.ops, steady, 2)
	e.WaitBundlePhase(t, s.ops.NS, fresh, "Verified", promoteTimeout)

	// ── The actors ──────────────────────────────────────────────────────
	for _, name := range []string{"kardinal-promoter-actor", "kardinal-promoter-audit-actor", "kardinal-promoter-agent-identities"} {
		_, err := e.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, err, "binding %s", name)
	}
	framework.Eventually(t, time.Minute, "the release's AgentIndex is Ready", func(ctx context.Context) (bool, string) {
		var idx v1alpha1.AgentIndex
		if err := e.Client.Get(ctx, types.NamespacedName{Name: framework.ControllerDeployment}, &idx); err != nil {
			return false, err.Error()
		}
		return meta.IsStatusConditionTrue(idx.Status.Conditions, "Ready"), fmt.Sprint(idx.Status.Conditions)
	})
	var stored v1alpha1.BundleList
	require.NoError(t, e.Client.List(ctx, &stored, client.InNamespace(s.approve.NS)))
	for _, b := range stored.Items {
		assert.Nil(t, b.Spec.Actor, "%s, stored by v0.11.0, has no actor", b.Name)
	}
	me := whoAmI(t, e)
	stamped := e.CreateBundle(t, s.ops.NS, steady, "--image", imageV3)
	var sb v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: s.ops.NS, Name: stamped}, &sb))
	assert.Equal(t, &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: me}, sb.Spec.Actor, "a new version is stamped")
}

// u0110AllKinds reads every stored object of every kardinal kind in the
// estate's namespaces with this release's types (a new required field or a
// changed type fails the decode), and checks the admission policies admit a
// label on the objects users write: Pipelines, Bundles and gate templates.
func u0110AllKinds(t *testing.T, e *framework.Env, s *u0110Estate) {
	t.Helper()
	ctx := context.Background()
	lists := []client.ObjectList{
		&v1alpha1.PipelineList{}, &v1alpha1.BundleList{}, &v1alpha1.PolicyGateList{}, &v1alpha1.PromotionStepList{},
		&v1alpha1.PRStatusList{}, &v1alpha1.ApprovalList{}, &v1alpha1.AuditEventList{}, &v1alpha1.SubscriptionList{},
		&v1alpha1.MetricCheckList{}, &v1alpha1.NotificationHookList{}, &v1alpha1.HookRunList{}, &v1alpha1.RenderRunList{},
		&v1alpha1.ImageVerificationList{}, &v1alpha1.RollbackPolicyList{}, &v1alpha1.ScmProviderList{},
	}
	label := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"e2e.kardinal.io/after-upgrade":"true"}}}`))
	for _, f := range s.fleets() {
		for _, l := range lists {
			require.NoError(t, e.Client.List(ctx, l, client.InNamespace(f.NS)), "list %T in %s with this release's types", l, f.NS)
		}
		var pl v1alpha1.PipelineList
		require.NoError(t, e.Client.List(ctx, &pl, client.InNamespace(f.NS)))
		for i := range pl.Items {
			assert.NoError(t, e.Client.Patch(ctx, &pl.Items[i], label), "label Pipeline %s/%s", f.NS, pl.Items[i].Name)
		}
		var bl v1alpha1.BundleList
		require.NoError(t, e.Client.List(ctx, &bl, client.InNamespace(f.NS)))
		for i := range bl.Items {
			assert.NoError(t, e.Client.Patch(ctx, &bl.Items[i], label), "label Bundle %s/%s", f.NS, bl.Items[i].Name)
		}
		var gl v1alpha1.PolicyGateList
		require.NoError(t, e.Client.List(ctx, &gl, client.InNamespace(f.NS)))
		for i := range gl.Items {
			if gl.Items[i].Labels["kardinal.io/bundle"] == "" { // a template, not an instance
				assert.NoError(t, e.Client.Patch(ctx, &gl.Items[i], label), "label PolicyGate %s/%s", f.NS, gl.Items[i].Name)
			}
		}
	}
	var cw v1alpha1.ChangeWindowList
	require.NoError(t, e.Client.List(ctx, &cw))
	var cs v1alpha1.ClusterScmProviderList
	require.NoError(t, e.Client.List(ctx, &cs))
}
