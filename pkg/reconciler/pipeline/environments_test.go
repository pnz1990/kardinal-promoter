// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

var envT0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

// envBundle is a Bundle of pipeline "app" shipping image tag tag.
func envBundle(name, tag, phase string, created time.Time) kardinalv1alpha1.Bundle {
	return kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: metav1.NewTime(created)},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app",
			Images: []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/x/app", Tag: tag}}},
		Status: kardinalv1alpha1.BundleStatus{Phase: phase},
	}
}

// projStep is the step of bundle in env; a Verified one records verified as
// its Verified condition time.
func projStep(bundle, env, state string, created, verified time.Time) kardinalv1alpha1.PromotionStep {
	s := kardinalv1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "app-" + bundle + "-" + env, Namespace: "default",
			CreationTimestamp: metav1.NewTime(created),
			Labels:            map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/bundle": bundle, "kardinal.io/environment": env}},
		Spec:   kardinalv1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: bundle, Environment: env},
		Status: kardinalv1alpha1.PromotionStepStatus{State: state},
	}
	if state == "Verified" {
		s.Status.Conditions = []metav1.Condition{{Type: "Verified", Status: metav1.ConditionTrue,
			Reason: "Verified", LastTransitionTime: metav1.NewTime(verified)}}
	}
	if state == "WaitingForMerge" {
		s.Status.PRURL = "https://github.com/me/gitops/pull/42"
	}
	return s
}

// envGate is a not-ready gate instance of bundle in env.
func envGate(bundle, env, gate string) kardinalv1alpha1.PolicyGate {
	return kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: bundle + "-" + env + "-" + gate, Namespace: "default",
			Labels: map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/bundle": bundle,
				"kardinal.io/environment": env, "kardinal.io/gate-name": gate, "kardinal.io/gate-template": gate}},
	}
}

func appPipeline() *kardinalv1alpha1.Pipeline {
	return newPipeline("app", []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "uat"}, {Name: "prod"}})
}

var validReady = metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Valid"}

// TestProjectEnvironments (P1): per environment, what runs there, where the
// newest version stands, since when, and what holds it; and the one-line
// summary built from it.
//
// Covers PIPE-ENVSTATUS-02.
func TestProjectEnvironments(t *testing.T) {
	t1, t2 := envT0.Add(time.Minute), envT0.Add(time.Hour)
	now := envT0.Add(2 * time.Hour)
	v1 := []kardinalv1alpha1.PromotionStep{
		projStep("b1", "test", "Verified", envT0, t1), projStep("b1", "uat", "Verified", envT0, t1),
		projStep("b1", "prod", "Verified", envT0, t1),
	}
	type want struct {
		env, bundle, version, incoming, state string
		blockedBy                             []string
		held                                  bool
	}
	tests := []struct {
		name    string
		p       func() *kardinalv1alpha1.Pipeline
		bundles []kardinalv1alpha1.Bundle
		steps   []kardinalv1alpha1.PromotionStep
		gates   []kardinalv1alpha1.PolicyGate
		ready   metav1.Condition
		want    []want
		summary string
		line    string
	}{{
		name:    "no version yet",
		p:       appPipeline,
		ready:   validReady,
		want:    []want{{env: "test"}, {env: "uat"}, {env: "prod"}},
		summary: "No version yet", line: "test=- uat=- prod=-",
	}, {
		name:    "live everywhere",
		p:       appPipeline,
		bundles: []kardinalv1alpha1.Bundle{envBundle("b1", "1.4.0", "Verified", envT0)},
		steps:   v1,
		ready:   validReady,
		want: []want{{env: "test", bundle: "b1", version: "1.4.0", state: "Verified"},
			{env: "uat", bundle: "b1", version: "1.4.0", state: "Verified"},
			{env: "prod", bundle: "b1", version: "1.4.0", state: "Verified"}},
		summary: "1.4.0 live in test, uat, prod", line: "test=1.4.0 uat=1.4.0 prod=1.4.0",
	}, {
		name: "a newer version waits for its PR merge in prod",
		p:    appPipeline,
		bundles: []kardinalv1alpha1.Bundle{envBundle("b1", "1.4.0", "Superseded", envT0),
			envBundle("b2", "1.5.0", "Promoting", t2)},
		steps: append(v1[:0:0], v1[2], projStep("b2", "test", "Verified", t2, t2),
			projStep("b2", "uat", "Verified", t2, t2), projStep("b2", "prod", "WaitingForMerge", t2, time.Time{})),
		ready: validReady,
		want: []want{{env: "test", bundle: "b2", version: "1.5.0", state: "Verified"},
			{env: "uat", bundle: "b2", version: "1.5.0", state: "Verified"},
			{env: "prod", bundle: "b1", version: "1.4.0", incoming: "b2", state: "WaitingForMerge"}},
		summary: "1.5.0 live in test, uat; waiting for PR merge in prod (#42)",
		line:    "test=1.5.0 uat=1.5.0 prod=1.4.0",
	}, {
		name: "a gate holds the newer version before its prod step exists",
		p:    appPipeline,
		bundles: []kardinalv1alpha1.Bundle{envBundle("b1", "1.4.0", "Verified", envT0),
			envBundle("b2", "1.5.0", "Promoting", t2)},
		steps: append(v1[:0:0], v1[2], projStep("b2", "test", "Verified", t2, t2),
			projStep("b2", "uat", "Verified", t2, t2)),
		gates: []kardinalv1alpha1.PolicyGate{envGate("b2", "prod", "no-weekend-deploys")},
		ready: validReady,
		want: []want{{env: "test", bundle: "b2", version: "1.5.0", state: "Verified"},
			{env: "uat", bundle: "b2", version: "1.5.0", state: "Verified"},
			{env: "prod", bundle: "b1", version: "1.4.0", incoming: "b2", state: "Pending",
				blockedBy: []string{"no-weekend-deploys"}}},
		summary: "1.5.0 live in test, uat; held by gate no-weekend-deploys in prod",
		line:    "test=1.5.0 uat=1.5.0 prod=1.4.0",
	}, {
		name: "a rollback hold keeps prod on the older version",
		p: func() *kardinalv1alpha1.Pipeline {
			p := appPipeline()
			p.Spec.Holds = []kardinalv1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "b1", Reason: "incident"}}
			return p
		},
		bundles: []kardinalv1alpha1.Bundle{envBundle("b1", "1.4.0", "Verified", envT0),
			envBundle("b2", "1.5.0", "Promoting", t2)},
		steps: append(v1[:0:0], v1[2], projStep("b2", "test", "Verified", t2, t2),
			projStep("b2", "uat", "Verified", t2, t2), projStep("b2", "prod", "Pending", t2, time.Time{})),
		ready: validReady,
		want: []want{{env: "test", bundle: "b2", version: "1.5.0", state: "Verified"},
			{env: "uat", bundle: "b2", version: "1.5.0", state: "Verified"},
			{env: "prod", bundle: "b1", version: "1.4.0", incoming: "b2", state: "Pending", held: true}},
		summary: "1.5.0 live in test, uat; held by rollback in prod",
		line:    "test=1.5.0 uat=1.5.0 prod=1.4.0",
	}, {
		name:    "a failed step in uat; prod not reached",
		p:       appPipeline,
		bundles: []kardinalv1alpha1.Bundle{envBundle("b2", "1.5.0", "Failed", t2)},
		steps:   []kardinalv1alpha1.PromotionStep{projStep("b2", "test", "Verified", t2, t2), projStep("b2", "uat", "Failed", t2, time.Time{})},
		ready:   validReady,
		want: []want{{env: "test", bundle: "b2", version: "1.5.0", state: "Verified"},
			{env: "uat", incoming: "b2", state: "Failed"}, {env: "prod"}},
		summary: "1.5.0 live in test; failed in uat",
		line:    "test=1.5.0 uat=- prod=-",
	}, {
		name: "an invalid, paused Pipeline says so first",
		p: func() *kardinalv1alpha1.Pipeline {
			p := appPipeline()
			p.Spec.Paused = true
			return p
		},
		ready:   metav1.Condition{Type: "Ready", Status: metav1.ConditionFalse, Reason: "ValidationFailed"},
		want:    []want{{env: "test"}, {env: "uat"}, {env: "prod"}},
		summary: "Not ready (ValidationFailed); paused; no version yet", line: "test=- uat=- prod=-",
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := tt.p()
			got := pipeline.ProjectEnvironments(p, tt.bundles, tt.steps, tt.gates, nil, now)
			require.Len(t, got, len(tt.want))
			for i, w := range tt.want {
				e := got[i]
				assert.Equal(t, w.env, e.Name)
				assert.Equal(t, w.bundle, e.Bundle, w.env)
				assert.Equal(t, w.version, e.Version, w.env)
				assert.Equal(t, w.incoming, e.IncomingBundle, w.env)
				assert.Equal(t, w.state, e.State, w.env)
				assert.Equal(t, w.blockedBy, e.BlockedBy, w.env)
				assert.Equal(t, w.held, e.Held, w.env)
				if w.state == "" {
					assert.Nil(t, e.Since, w.env)
				} else {
					assert.NotNil(t, e.Since, w.env)
				}
			}
			assert.Equal(t, tt.summary, pipeline.Summarize(p, tt.ready, got))
			assert.Equal(t, tt.line, pipeline.EnvironmentLine(got))
		})
	}
}

// TestProjectEnvironments_Since (P1): Since of a Verified environment is its
// step's Verified time; any other state keeps the Since it was first
// recorded with, so a later reconcile does not move it, and a new state
// starts a new one.
//
// Covers PIPE-ENVSTATUS-02.
func TestProjectEnvironments_Since(t *testing.T) {
	p := newPipeline("app", []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}})
	verified := envT0.Add(time.Minute)
	bundles := []kardinalv1alpha1.Bundle{envBundle("b1", "1.4.0", "Promoting", envT0)}
	steps := []kardinalv1alpha1.PromotionStep{projStep("b1", "test", "Verified", envT0, verified),
		projStep("b1", "prod", "WaitingForMerge", envT0, time.Time{})}

	first := pipeline.ProjectEnvironments(p, bundles, steps, nil, nil, envT0.Add(10*time.Minute+500*time.Millisecond))
	require.Len(t, first, 2)
	assert.True(t, first[0].Since.Time.Equal(verified), "Verified: the step's Verified time")
	assert.True(t, first[1].Since.Time.Equal(envT0.Add(10*time.Minute)), "first seen, to the second")

	again := pipeline.ProjectEnvironments(p, bundles, steps, nil, first, envT0.Add(time.Hour))
	assert.Equal(t, first, again, "the same input gives the same projection")

	steps[1].Status.State = "HealthChecking"
	moved := pipeline.ProjectEnvironments(p, bundles, steps, nil, first, envT0.Add(time.Hour))
	assert.True(t, moved[1].Since.Time.Equal(envT0.Add(time.Hour)), "a new state starts a new Since")
}

// TestPipelineReconciler_EnvironmentsIdempotent (P1): the reconciler writes
// status.environments, summary and environmentSummary, and a second
// reconcile with nothing changed, later, writes nothing.
//
// Covers PIPE-ENVSTATUS-02.
func TestPipelineReconciler_EnvironmentsIdempotent(t *testing.T) {
	p := newPipeline("app", []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}})
	b := envBundle("b1", "1.4.0", "Promoting", envT0)
	s1 := projStep("b1", "test", "Verified", envT0, envT0.Add(time.Minute))
	s2 := projStep("b1", "prod", "WaitingForMerge", envT0, time.Time{})
	c := newClientWithIndex(newScheme(), p, &b, &s1, &s2)
	clock := envT0.Add(time.Hour)
	r := &pipeline.Reconciler{Client: c, Now: func() time.Time { return clock }}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "app", Namespace: "default"}}

	_, err := r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(p), &got))
	require.Len(t, got.Status.Environments, 2)
	assert.Equal(t, "1.4.0 live in test; waiting for PR merge in prod (#42)", got.Status.Summary)
	assert.Equal(t, "test=1.4.0 prod=-", got.Status.EnvironmentSummary)
	assert.Equal(t, "WaitingForMerge", got.Status.Environments[1].State)
	assert.Equal(t, "https://github.com/me/gitops/pull/42", got.Status.Environments[1].PRURL)
	rv := got.ResourceVersion

	clock = clock.Add(time.Hour)
	_, err = r.Reconcile(context.Background(), req)
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(p), &got))
	assert.Equal(t, rv, got.ResourceVersion, "nothing changed: no status write")
	assert.True(t, got.Status.Environments[1].Since.Time.Equal(envT0.Add(time.Hour)))
}

// TestSummarizeGroups: status.summary counts a fleet's targets under the
// fleet's name and the environments of a wave longer than a clause names
// under "wave N"; the first clause that counts a group gives its size. Other
// environments stay named, at most six per clause, then "and N more", and so
// do a clause's PRs.
func TestSummarizeGroups(t *testing.T) {
	fleet := func(name string, n int) kardinalv1alpha1.EnvironmentSpec {
		f := &kardinalv1alpha1.FleetSpec{}
		for i := range n {
			f.Targets = append(f.Targets, kardinalv1alpha1.FleetTarget{Name: fmt.Sprintf("t%02d", i)})
		}
		return kardinalv1alpha1.EnvironmentSpec{Name: name, Fleet: f}
	}
	st := func(name, version, state string) kardinalv1alpha1.PipelineEnvironmentStatus {
		e := kardinalv1alpha1.PipelineEnvironmentStatus{Name: name, Version: version, State: state}
		if state == "WaitingForMerge" {
			e.PRURL = "https://github.com/me/gitops/pull/" + strings.TrimPrefix(name, "e")
		}
		return e
	}
	// targets returns the status of fleet targets from..to-1 in state.
	targets := func(fleet string, from, to int, version, state string) []kardinalv1alpha1.PipelineEnvironmentStatus {
		var out []kardinalv1alpha1.PipelineEnvironmentStatus
		for i := from; i < to; i++ {
			out = append(out, st(fmt.Sprintf("%s-t%02d", fleet, i), version, state))
		}
		return out
	}
	named := func(n int, wave int) []kardinalv1alpha1.EnvironmentSpec {
		var out []kardinalv1alpha1.EnvironmentSpec
		for i := range n {
			out = append(out, kardinalv1alpha1.EnvironmentSpec{Name: fmt.Sprintf("e%d", i+1), Wave: wave})
		}
		return out
	}
	statuses := func(n int, version, state string) []kardinalv1alpha1.PipelineEnvironmentStatus {
		var out []kardinalv1alpha1.PipelineEnvironmentStatus
		for i := range n {
			out = append(out, st(fmt.Sprintf("e%d", i+1), version, state))
		}
		return out
	}
	cat := func(parts ...[]kardinalv1alpha1.PipelineEnvironmentStatus) []kardinalv1alpha1.PipelineEnvironmentStatus {
		var out []kardinalv1alpha1.PipelineEnvironmentStatus
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	tests := []struct {
		name string
		envs []kardinalv1alpha1.EnvironmentSpec
		st   []kardinalv1alpha1.PipelineEnvironmentStatus
		want string
	}{
		{name: "a fleet is counted under its name",
			envs: []kardinalv1alpha1.EnvironmentSpec{{Name: "canary"}, fleet("pops", 42)},
			st: cat([]kardinalv1alpha1.PipelineEnvironmentStatus{st("canary", "7.2.0", "Verified")},
				targets("pops", 0, 27, "7.2.0", "Verified"), targets("pops", 27, 32, "7.2.0", "Promoting"),
				targets("pops", 32, 42, "7.2.0", "Pending")),
			want: "7.2.0 live in canary and 27 of 42 pops; promoting in 5 pops; queued in 10 pops"},
		{name: "a fleet live everywhere",
			envs: []kardinalv1alpha1.EnvironmentSpec{{Name: "canary"}, fleet("pops", 3)},
			st:   cat([]kardinalv1alpha1.PipelineEnvironmentStatus{st("canary", "7.2.0", "Verified")}, targets("pops", 0, 3, "7.2.0", "Verified")),
			want: "7.2.0 live in canary and all 3 pops"},
		{name: "places keep spec order",
			envs: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, fleet("prod", 50), {Name: "post"}},
			st: cat([]kardinalv1alpha1.PipelineEnvironmentStatus{st("test", "2.0", "Verified")},
				targets("prod", 0, 50, "2.0", "Verified"), []kardinalv1alpha1.PipelineEnvironmentStatus{st("post", "2.0", "Verified")}),
			want: "2.0 live in test, all 50 prod and post"},
		{name: "two fleets in one clause",
			envs: []kardinalv1alpha1.EnvironmentSpec{fleet("eu", 4), fleet("us", 5)},
			st:   cat(targets("eu", 0, 4, "1.0", "Verified"), targets("us", 0, 2, "1.0", "Verified"), targets("us", 2, 5, "1.0", "Promoting")),
			want: "1.0 live in all 4 eu and 2 of 5 us; promoting in 3 us"},
		{name: "a short wave stays named",
			envs: append([]kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}, named(3, 1)...),
			st:   cat([]kardinalv1alpha1.PipelineEnvironmentStatus{st("test", "1.0", "Verified")}, statuses(3, "1.0", "Verified")),
			want: "1.0 live in test, e1, e2, e3"},
		{name: "a long wave is counted as the wave",
			envs: append([]kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}, named(9, 2)...),
			st: cat([]kardinalv1alpha1.PipelineEnvironmentStatus{st("test", "1.0", "Verified")},
				statuses(9, "1.0", "Verified")[:7], statuses(9, "1.0", "Failed")[7:]),
			want: "1.0 live in test and 7 of 9 in wave 2; failed in 2 in wave 2"},
		{name: "more than six named environments",
			envs: named(9, 0),
			st:   statuses(9, "1.0", "Verified"),
			want: "1.0 live in e1, e2, e3, e4, e5, e6 and 3 more"},
		{name: "N more comes before a group",
			envs: append(named(8, 0), fleet("pops", 10)),
			st:   cat(statuses(8, "1.0", "Verified"), targets("pops", 0, 4, "1.0", "Verified")),
			want: "1.0 live in e1, e2, e3, e4, e5, e6, 2 more and 4 of 10 pops"},
		{name: "a whole long wave",
			envs: named(9, 3),
			st:   statuses(9, "1.0", "Verified"),
			want: "1.0 live in all 9 in wave 3"},
		{name: "more than six PRs",
			envs: named(8, 0),
			st:   statuses(8, "1.0", "WaitingForMerge"),
			want: "1.0 waiting for PR merge in e1, e2, e3, e4, e5, e6 and 2 more (#1, #2, #3, #4, #5, #6, 2 more)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPipeline("app", tt.envs)
			assert.Equal(t, tt.want, pipeline.Summarize(p, validReady, tt.st))
		})
	}
}
