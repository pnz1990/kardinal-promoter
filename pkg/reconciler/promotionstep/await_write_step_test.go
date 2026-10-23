// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// TestAwaitWrite_TheStepWaitsForItsBranchWrite (#2366): a step the Graph gave
// a BranchWrite (spec.write) runs await-write instead of cloning, committing
// and pushing: it records its request (status.writeRequest, with its push
// intent, the #1603 guard's) and waits in Promoting without touching git or
// taking the branch turn; once the Graph mirrors the BranchWrite's answer onto
// spec.live.writes it takes the commit and goes on to the health check, which
// waits for that commit.
func TestAwaitWrite_TheStepWaitsForItsBranchWrite(t *testing.T) {
	ctx := context.Background()
	p := makePipeline("web")
	p.Spec.Git.Writes = v1alpha1.WritesBatched
	step := makeStep("web-b1-test", "web", "b1", "test")
	step.Spec.Write = "b1-write"
	step.UID = "uid-test"
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		WithObjects(step, p, makeBundle("b1", "web")).Build()
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &gitNeverCalled{mockGit{cloneErr: assert.AnError}},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}
	key := types.NamespacedName{Name: step.Name, Namespace: "default"}
	reconcile := func() *v1alpha1.PromotionStep {
		t.Helper()
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		require.NoError(t, err)
		var got v1alpha1.PromotionStep
		require.NoError(t, c.Get(ctx, key, &got))
		return &got
	}

	got := reconcile() // Pending → Promoting, step list recorded
	require.Equal(t, "Promoting", got.Status.State)
	var names []string
	for _, s := range got.Status.Steps {
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"await-write", "health-check"}, names)
	got = reconcile()
	assert.Equal(t, "Promoting", got.Status.State)
	assert.EqualValues(t, 1, got.Status.WriteRequest, "the Graph lists the request on the BranchWrite")
	assert.NotContains(t, got.Status.Outputs, "writeRequest", "the output is only the signal")
	assert.NotEmpty(t, got.Status.Outputs["pushIntent"], "the request is the step's push intent")
	got = reconcile()
	assert.Equal(t, "Promoting", got.Status.State, "waits for the BranchWrite")
	assert.EqualValues(t, 1, got.Status.WriteRequest, "it asks once")
	assert.Contains(t, got.Status.Message, "waiting for BranchWrite b1-write")

	const sha = "0123456789abcdef0123456789abcdef01234567"
	got.Spec.Live = &v1alpha1.PromotionStepLive{Writes: []v1alpha1.LiveBranchWrite{{Name: "b1-write",
		Result: &v1alpha1.BranchWriteResult{Step: step.Name, UID: "uid-test", Request: 1, Round: 1, CommitSHA: sha}}}}
	require.NoError(t, c.Update(ctx, got))
	got = reconcile()
	assert.Equal(t, "HealthChecking", got.Status.State)
	assert.Equal(t, sha, got.Status.Outputs["commitSHA"], "the health check waits for the environment's own commit")
	assert.Equal(t, "b1-write/1", got.Status.Outputs["writtenBy"])
}
