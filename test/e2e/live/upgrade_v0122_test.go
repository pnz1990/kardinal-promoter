//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// guideChartV0122 is the chart reference of the "Upgrading from v0.12.2"
// guide: the test replaces it with the checkout's chart.
const guideChartV0122 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.3"

// TestUpgradeV0122_Estate is TestUpgradeV0120_Estate from kardinal-promoter
// v0.12.2 as released (its chart, digest-pinned image, CLI and kro;
// components/kardinal-v0122.sh): it builds the same estate under v0.12.2,
// with the automatic rollback in flight (u012AutoRollback), follows
// "Upgrading from v0.12.2" in docs/upgrade.md while versions are in flight,
// and makes the same checks.
//
// It adds a version v0.12.2 never creates a verify gate for (u0122Stuck):
// the code v0.12.3 changes (#2271).
//
// Covers UPG-V0122-01, UPG-V0122-02, UPG-V0122-03, UPG-V0122-04, UPG-V0122-05, UPG-V0122-06, UPG-V0122-07.
func TestUpgradeV0122_Estate(t *testing.T) {
	stuck := &u0122Stuck{}
	u012Upgrade(t, u012From{pins: "V0122", cli: (*framework.Env).KardinalV0122, guideChart: guideChartV0122,
		extra: u012Extras(u012AutoRollback, stuck.before), kroExpected: stuck.expected})
}

// u012Extras runs several of a suite's own cases as one u012From.extra:
// each before the estate's versions start, in order, and their checks after
// the upgrade in the same order.
func u012Extras(cases ...func(t *testing.T, e *framework.Env, from u012From) func()) func(t *testing.T, e *framework.Env, from u012From) func() {
	return func(t *testing.T, e *framework.Env, from u012From) func() {
		t.Helper()
		var after []func()
		for _, c := range cases {
			after = append(after, c(t, e, from))
		}
		return func() {
			for _, a := range after {
				a()
			}
		}
	}
}

// u0122NullApproval is what the API server answers kro for a gate instance
// rendered with spec.approval: null (#2271): the PolicyGate CRD refuses it.
// The Graph's condition message carries it as is; kro's log line carries it
// inside a JSON string, its quotes escaped (u0122NullApprovalLogged).
const u0122NullApproval = `spec.approval in body must be of type object: "null"`

// u0122NullApprovalLogged is u0122NullApproval as kro's log lines carry it.
const u0122NullApprovalLogged = `spec.approval in body must be of type object: \"null\"`

// u0122Stuck is upgrade-v0122's own case: a version stuck under #2271
// across the upgrade. Under v0.12.2, prod of a Pipeline has an approval gate
// with spec.approval (people) and a verify gate without one (explore-qa).
// v0.12.2 renders the verify gate's instance with spec.approval: null, which
// the PolicyGate CRD refuses, so kro never creates it: the version waits at
// prod with its approval given and no explore-qa instance, and kro logs the
// refusal. After the upgrade, with no step by hand:
//   - the new controller renders the version's Graph again, once, in place
//     (the same uid, the current kardinal.io/render-version label, and the
//     generation holds after it);
//   - kro creates the explore-qa instance, which holds prod until carol's
//     Verdict, recorded with this release's CLI;
//   - the version then promotes to prod and is Verified: the same Bundle,
//     the same uat step, and the people instance and alice's approval given
//     under v0.12.2.
//
// kro's refusals from the upgrade on are expected until the explore-qa
// instance exists (expected); after it, one is an error the estate's kro
// log check reports.
type u0122Stuck struct {
	mu      sync.Mutex
	ns      string
	created time.Time // when the test first saw the explore-qa instance
}

// expected says whether kro's error line text, logged at at, is a refusal
// of the stuck version's explore-qa instance from before it existed.
func (s *u0122Stuck) expected(text string, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ns != "" && !s.created.IsZero() && at.Before(s.created) &&
		strings.Contains(text, s.ns) && strings.Contains(text, u0122NullApprovalLogged)
}

// u0122Graph is the Graph of bundle in ns: there is exactly one.
func u0122Graph(ctx context.Context, e *framework.Env, ns, bundle string) (*unstructured.Unstructured, error) {
	list, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "kardinal.io/bundle=" + bundle,
	})
	if err != nil {
		return nil, fmt.Errorf("list the Graphs of %s/%s: %w", ns, bundle, err)
	}
	if len(list.Items) != 1 {
		return nil, fmt.Errorf("%d Graphs of %s/%s", len(list.Items), ns, bundle)
	}
	return &list.Items[0], nil
}

// u0122Condition is the message of the Graph condition typ, and whether it
// is True.
func u0122Condition(g *unstructured.Unstructured, typ string) (status bool, message string) {
	conds, _, _ := unstructured.NestedSlice(g.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if ok && m["type"] == typ {
			msg, _ := m["message"].(string)
			return m["status"] == "True", msg
		}
	}
	return false, ""
}

func (s *u0122Stuck) before(t *testing.T, e *framework.Env, from u012From) func() {
	t.Helper()
	ctx := context.Background()
	fromVersion, _, _ := u012Pins(t, from)
	seed, err := scale.Seed()
	require.NoError(t, err)
	f := scale.NewFleet(t, e, scale.NewRNG(seed, "stuck"))
	s.mu.Lock()
	s.ns = f.NS
	s.mu.Unlock()

	people := framework.Gate(f.NS, "people", "prod", "true", recheck)
	people.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, AllowedGroups: []string{"release-managers"}}
	e.CreateGate(t, people)
	check := framework.Gate(f.NS, "explore-qa", "prod", "true", recheck)
	check.Spec.Verify = &v1alpha1.GateVerifyPolicy{Check: "explore", Required: 1}
	e.CreateGate(t, check)
	const p = "stuck"
	repo := f.Repo(t, f.NS+"-"+p, map[string][]string{p: {"uat", "prod"}})
	f.Apply(t, f.PipelineSpec(p, repo, u0120Envs("uat", "prod")), repo)
	alice := userKubeconfig(t, e, f.NS, "alice@example.com", "release-managers")
	carol := userKubeconfig(t, e, f.NS, "carol@example.com")
	_, err = e.Kube.RbacV1().RoleBindings(f.NS).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "verifier-carol", Namespace: f.NS},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: verifierRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: "carol@example.com"}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	waitBoundNamed(t, e, f.NS, "verifier-carol")

	// 1. Under the release: the version waits at prod with its approval
	// given, and no explore-qa instance.
	out, err := from.cli(e, t, f.NS, "create", "bundle", p, "--image", scale.ImageRepo+":"+scale.Tag(p, 1))
	require.NoError(t, err, out)
	m := v081BundleCreated.FindStringSubmatch(out)
	require.Len(t, m, 2, "kardinal create bundle names the Bundle it created:\n%s", out)
	b := m[1]
	uat := e.WaitStepState(t, f.NS, p, b, "uat", "Verified", promoteTimeout)
	e.WaitGateReady(t, f.NS, b, "prod", "people", false, "waiting for approvals: 0 of 1", gateTimeout)
	out, err = from.cli(e, t, f.NS, "--kubeconfig", alice, "approve", b, "--env", "prod")
	require.NoError(t, err, out)
	approved := e.WaitGateReady(t, f.NS, b, "prod", "people", true, "", gateTimeout)
	framework.Consistently(t, 30*time.Second, "v"+fromVersion+" creates no explore-qa gate and no prod step for "+b, func(ctx context.Context) (bool, string) {
		if g, ok, err := e.GateInstance(ctx, f.NS, b, "prod", "explore-qa"); err != nil || ok {
			return false, fmt.Sprintf("explore-qa instance: ok=%v err=%v %v", ok, err, g)
		}
		if st, ok, err := e.Step(ctx, f.NS, p, b, "prod"); err != nil || ok {
			return false, fmt.Sprintf("prod step: ok=%v err=%v %v", ok, err, st)
		}
		return true, ""
	})
	stuckGraph, err := u0122Graph(ctx, e, f.NS, b)
	require.NoError(t, err)
	_, labelled := stuckGraph.GetLabels()[graph.LabelRenderVersion]
	require.False(t, labelled, "v%s's Graph of %s has no %s label: %v", fromVersion, b, graph.LabelRenderVersion, stuckGraph.GetLabels())
	converged, msg := u0122Condition(stuckGraph, "ResourcesConverged")
	require.False(t, converged, "v%s's Graph of %s does not converge", fromVersion, b)
	require.Contains(t, msg, u0122NullApproval, "kro could not create the explore-qa instance v%s rendered (#2271)", fromVersion)
	stuckB := getBundle(t, e, f.NS, b)
	require.Equal(t, "Promoting", stuckB.Status.Phase, "%s is in flight at the upgrade", b)
	t.Logf("%s/%s stuck under v%s: %s", f.NS, b, fromVersion, msg)

	return func() {
		// 3. The new controller renders the Graph again, once, in place.
		want := graph.RenderLabel
		var rendered *unstructured.Unstructured
		framework.Eventually(t, 2*time.Minute, "the Graph of "+b+" rendered by this release", func(ctx context.Context) (bool, string) {
			g, err := u0122Graph(ctx, e, f.NS, b)
			if err != nil {
				return false, err.Error()
			}
			rendered = g
			return g.GetLabels()[graph.LabelRenderVersion] == want, fmt.Sprintf("labels %v", g.GetLabels())
		})
		assert.Equal(t, stuckGraph.GetUID(), rendered.GetUID(), "the Graph of %s is the same Graph, rendered in place", b)
		g := e.WaitGate(t, f.NS, b, "prod", "explore-qa", 2*time.Minute, "created", func(*v1alpha1.PolicyGate) bool { return true })
		s.mu.Lock()
		s.created = time.Now()
		s.mu.Unlock()
		t.Logf("explore-qa instance %s created after the upgrade", g.Name)
		e.WaitGateReady(t, f.NS, b, "prod", "explore-qa", false, "", gateTimeout)
		framework.Eventually(t, time.Minute, "kro reports no refused instance for the Graph of "+b, func(ctx context.Context) (bool, string) {
			g, err := u0122Graph(ctx, e, f.NS, b)
			if err != nil {
				return false, err.Error()
			}
			_, msg := u0122Condition(g, "ResourcesConverged")
			return !strings.Contains(msg, u0122NullApproval), msg
		})
		generation := rendered.GetGeneration()
		framework.Consistently(t, 20*time.Second, "explore-qa holds prod, and the Graph of "+b+" is rendered once", func(ctx context.Context) (bool, string) {
			if st, ok, err := e.Step(ctx, f.NS, p, b, "prod"); err != nil || ok {
				return false, fmt.Sprintf("prod step: ok=%v err=%v %v", ok, err, st)
			}
			g, err := u0122Graph(ctx, e, f.NS, b)
			if err != nil {
				return false, err.Error()
			}
			return g.GetGeneration() == generation && g.GetLabels()[graph.LabelRenderVersion] == want,
				fmt.Sprintf("generation %d (was %d), labels %v", g.GetGeneration(), generation, g.GetLabels())
		})

		// 4. carol's Verdict opens it, and the version promotes.
		c := e.CLI(t)
		u012RunsIn(t, e, f.NS, p, "uat", b)
		r := c.Exec(framework.CLIOptions{Kubeconfig: carol}, c.Args(f.NS, "verify", p, "uat", "--check", "explore",
			"--pass", "--summary", "checked after the upgrade")...)
		require.Equal(t, 0, r.Code, r.Output())
		e.WaitGateReady(t, f.NS, b, "prod", "explore-qa", true, "verified: 1 of 1", gateTimeout)
		e.WaitStepState(t, f.NS, p, b, "prod", "Verified", promoteTimeout)
		e.WaitBundlePhase(t, f.NS, b, "Verified", promoteTimeout)
		assert.Equal(t, stuckB.UID, getBundle(t, e, f.NS, b).UID, "%s is the same Bundle", b)
		now, ok, err := e.Step(ctx, f.NS, p, b, "uat")
		require.NoError(t, err)
		if assert.True(t, ok, "%s keeps its uat step", b) {
			assert.Equal(t, uat.UID, now.UID, "%s's uat step is the same step: the version did not promote again", b)
		}
		gate, ok, err := e.GateInstance(ctx, f.NS, b, "prod", "people")
		require.NoError(t, err)
		if assert.True(t, ok, "%s keeps its people instance", b) {
			assert.Equal(t, approved.UID, gate.UID, "the people instance is the one v%s's Graph made", fromVersion)
			assert.True(t, gate.Status.Ready, "alice's approval from v%s still counts: %s", fromVersion, framework.DescribeGate(gate))
		}
	}
}
