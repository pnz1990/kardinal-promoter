// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// lockedSCM is mockSCM safe for concurrent use, as every real provider is.
type lockedSCM struct {
	mu sync.Mutex
	mockSCM
	next int
}

func (m *lockedSCM) OpenPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	m.openCalled++
	return fmt.Sprintf("https://github.com/test/repo/pull/%d", m.next), m.next, nil
}

func (m *lockedSCM) GetPRStatus(ctx context.Context, repo string, n int) (bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mockSCM.GetPRStatus(ctx, repo, n)
}

func (m *lockedSCM) AddLabelsToPR(ctx context.Context, repo string, n int, l []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mockSCM.AddLabelsToPR(ctx, repo, n, l)
}

func (m *lockedSCM) CommentOnPR(ctx context.Context, repo string, n int, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mockSCM.CommentOnPR(ctx, repo, n, body)
}

// TestReconciler_StepsRunSideBySide (#1509): one Reconciler, as with
// --promotionstep-workers above 1, runs the steps of many Pipelines at once,
// each through git and open-pr to WaitingForMerge with its own PR, and a
// reconcile run again on each opens no second PR. Run with -race: it fails
// on any state the reconciler shares between reconciles of different steps.
func TestReconciler_StepsRunSideBySide(t *testing.T) {
	const pipelines = 12
	var objs []client.Object
	var steps []*v1alpha1.PromotionStep
	for i := 0; i < pipelines; i++ {
		pl, b := makePipeline(fmt.Sprintf("app-%d", i)), makeBundle(fmt.Sprintf("app-%d-v1", i), fmt.Sprintf("app-%d", i))
		ps := builtStep(t, pl, b, "prod")
		steps = append(steps, ps)
		objs = append(objs, pl, b, ps, openPRStatus(ps.Spec.PRStatusRef, "", 0))
	}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.PRStatus{}, &v1alpha1.Bundle{}).
		WithObjects(objs...).Build()
	m := &lockedSCM{mockSCM: mockSCM{open: true}}
	var _ scm.SCMProvider = m
	r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{}, Workers: 16,
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}

	for round := 0; round < 3; round++ {
		var wg sync.WaitGroup
		for _, ps := range steps {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				_, err := r.Reconcile(context.Background(), reqFor(name))
				assert.NoError(t, err, name)
			}(ps.Name)
		}
		wg.Wait()
	}
	prs := map[string]bool{}
	for _, ps := range steps {
		got := getStep(t, c, ps.Name)
		require.Equal(t, "WaitingForMerge", got.Status.State, "%s: %s", ps.Name, got.Status.Message)
		assert.False(t, prs[got.Status.Outputs["prNumber"]], "each step has its own PR")
		prs[got.Status.Outputs["prNumber"]] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Equal(t, pipelines, m.openCalled, "one PR per step, none opened twice")
}
