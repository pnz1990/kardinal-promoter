// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package prstatus_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/prstatus"
)

// mergerSCM is a fakeSCM that names who merged the PR.
type mergerSCM struct {
	fakeSCM
	merger      string
	mergerCalls int
	mergeCommit string
	// mergerErrs are returned first, in order.
	mergerErrs []error
}

func (m *mergerSCM) GetPRMerger(context.Context, string, int) (string, error) {
	m.mergerCalls++
	if len(m.mergerErrs) > 0 {
		err := m.mergerErrs[0]
		m.mergerErrs = m.mergerErrs[1:]
		return "", err
	}
	return m.merger, nil
}

func (m *mergerSCM) GetPRMergeCommit(context.Context, string, int) (string, error) {
	return m.mergeCommit, nil
}

// TestReconciler_PolledMergeNamesTheMerger (#1939): a merge found by
// polling records who merged the PR (asking the SCM once) and that polling
// found it; a merge recorded before kardinal recorded mergers (no
// mergedVia) asks the SCM nothing.
func TestReconciler_PolledMergeNamesTheMerger(t *testing.T) {
	ctx := context.Background()
	prs := &v1alpha1.PRStatus{ObjectMeta: metav1.ObjectMeta{Name: "pr", Namespace: "default"},
		Spec:   v1alpha1.PRStatusSpec{PRURL: "https://github.com/acme/app/pull/5", PRNumber: 5, Repo: "acme/app"},
		Status: v1alpha1.PRStatusStatus{Open: true}}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(prs).WithStatusSubresource(prs).Build()
	m := &mergerSCM{fakeSCM: fakeSCM{merged: true}, merger: "user1", mergeCommit: "abc123"}
	r := &prstatus.Reconciler{Client: c, SCM: m}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pr", Namespace: "default"}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	var got v1alpha1.PRStatus
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	assert.True(t, got.Status.Merged)
	assert.Equal(t, "user1", got.Status.MergedBy)
	assert.Equal(t, v1alpha1.PRMergedViaPoll, got.Status.MergedVia)
	assert.Equal(t, "abc123", got.Status.MergeCommitSHA)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 1, m.mergerCalls, "asked once")

	legacy := &v1alpha1.PRStatus{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "default"},
		Spec:   prs.Spec,
		Status: v1alpha1.PRStatusStatus{Merged: true, MergeCommitSHA: "abc123"}}
	c = fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(legacy).WithStatusSubresource(legacy).Build()
	m = &mergerSCM{merger: "user1"}
	r = &prstatus.Reconciler{Client: c, SCM: m}
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "old", Namespace: "default"}})
	require.NoError(t, err)
	assert.Zero(t, m.mergerCalls, "a merge from before the upgrade is left as it is")
}

// TestReconciler_MergerLookupRetried (#2003 QA B2): a transient error on
// the merger lookup leaves the name pending (not unavailable), and the next
// reconcile records it.
func TestReconciler_MergerLookupRetried(t *testing.T) {
	ctx := context.Background()
	prs := &v1alpha1.PRStatus{ObjectMeta: metav1.ObjectMeta{Name: "pr", Namespace: "default"},
		Spec:   v1alpha1.PRStatusSpec{PRURL: "https://github.com/acme/app/pull/5", PRNumber: 5, Repo: "acme/app"},
		Status: v1alpha1.PRStatusStatus{Open: true}}
	c := fake.NewClientBuilder().WithScheme(buildScheme(t)).WithObjects(prs).WithStatusSubresource(prs).Build()
	m := &mergerSCM{fakeSCM: fakeSCM{merged: true}, merger: "user1", mergeCommit: "abc123",
		mergerErrs: []error{errors.New("503 Service Unavailable")}}
	r := &prstatus.Reconciler{Client: c, SCM: m}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "pr", Namespace: "default"}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	var got v1alpha1.PRStatus
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	require.True(t, got.Status.Merged)
	assert.Empty(t, got.Status.MergedBy)
	assert.False(t, got.Status.MergedByUnavailable, "a transient error is retried, not given up")
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	assert.Equal(t, "user1", got.Status.MergedBy)
}
