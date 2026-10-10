// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// TestPipelineReconciler_RenderedBranchConflict (QA on #1515): of two
// Pipelines whose layout: branch environments render to the same branch of
// the same repository (in any namespace), the newer one is Ready=False with
// reason RenderedBranchConflict naming the other; the older one is not, and
// a different branch or repository is no conflict.
func TestPipelineReconciler_RenderedBranchConflict(t *testing.T) {
	at := func(p *kardinalv1alpha1.Pipeline, ns string, created time.Time) *kardinalv1alpha1.Pipeline {
		p.Namespace = ns
		p.CreationTimestamp = metav1.NewTime(created)
		p.UID = types.UID(ns + "-" + p.Name)
		return p
	}
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	branch := func(name string) []kardinalv1alpha1.EnvironmentSpec {
		return []kardinalv1alpha1.EnvironmentSpec{{Name: "prod", Layout: "branch", Render: &kardinalv1alpha1.RenderConfig{Branch: name}}}
	}
	old := at(newPipeline("web", branch("env/prod")), "team-a", base)
	newer := at(newPipeline("api", branch("env/prod")), "team-b", base.Add(time.Hour))
	other := at(newPipeline("jobs", branch("env/jobs")), "team-b", base.Add(time.Hour))
	elsewhere := at(newPipeline("site", branch("env/prod")), "team-c", base.Add(time.Hour))
	elsewhere.Spec.Git.URL = "https://github.com/myorg/other.git"
	sameRepoOtherSpelling := at(newPipeline("dup", branch("env/prod")), "team-d", base.Add(2*time.Hour))
	sameRepoOtherSpelling.Spec.Git.URL = "https://github.com/MyOrg/gitops"

	c := newClientWithIndex(newScheme(), old, newer, other, elsewhere, sameRepoOtherSpelling)
	r := &pipeline.Reconciler{Client: c}
	ready := func(p *kardinalv1alpha1.Pipeline) *metav1.Condition {
		t.Helper()
		_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: p.Namespace, Name: p.Name}})
		require.NoError(t, err)
		var got kardinalv1alpha1.Pipeline
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: p.Namespace, Name: p.Name}, &got))
		return meta.FindStatusCondition(got.Status.Conditions, "Ready")
	}
	assert.Equal(t, metav1.ConditionTrue, ready(old).Status, "the older Pipeline keeps its branch")
	cond := ready(newer)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "RenderedBranchConflict", cond.Reason)
	assert.Contains(t, cond.Message, `environment "prod" renders to env/prod, which a Pipeline in another namespace renders to`)
	assert.NotContains(t, cond.Message, "team-a", "another namespace's Pipeline is not named")
	sameNS := at(newPipeline("web-copy", branch("env/prod")), "team-a", base.Add(3*time.Hour))
	require.NoError(t, c.Create(context.Background(), sameNS))
	assert.Contains(t, ready(sameNS).Message, `which environment "prod" of Pipeline team-a/web renders to`, "in the same namespace it is named")
	assert.Equal(t, metav1.ConditionTrue, ready(other).Status, "another branch")
	assert.Equal(t, metav1.ConditionTrue, ready(elsewhere).Status, "another repository")
	assert.Equal(t, "RenderedBranchConflict", ready(sameRepoOtherSpelling).Reason, "the same repository, spelt differently")

	// A fleet target's default branch (env/<fleet>-<target>) counts: a
	// newer Pipeline whose fleet target renders to an older Pipeline's
	// explicit branch conflicts.
	fleetP := at(newPipeline("edge", []kardinalv1alpha1.EnvironmentSpec{{Name: "prod", Layout: "branch",
		Fleet: &kardinalv1alpha1.FleetSpec{Targets: []kardinalv1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}}}}}),
		"team-a", base.Add(4*time.Hour))
	require.NoError(t, c.Create(context.Background(), fleetP))
	cond = ready(fleetP)
	assert.Equal(t, metav1.ConditionTrue, cond.Status, "no older Pipeline renders to env/prod-eu yet: %s", cond.Message)
	taker := at(newPipeline("taker", branch("env/prod-eu")), "team-a", base.Add(5*time.Hour))
	require.NoError(t, c.Create(context.Background(), taker))
	cond = ready(taker)
	assert.Equal(t, "RenderedBranchConflict", cond.Reason)
	assert.Contains(t, cond.Message, `which environment "prod-eu" of Pipeline team-a/edge renders to`, "the fleet target is named")
}
