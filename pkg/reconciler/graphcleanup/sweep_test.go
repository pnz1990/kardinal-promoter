// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graphcleanup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/graphcleanup"
)

// TestSweep covers B46: the sweep deletes the reader RoleBindings for
// namespaces that are gone or hold no Graph reading through them, including
// the ones an older controller left behind, and leaves every other
// RoleBinding alone.
func TestSweep(t *testing.T) {
	reader := func(ns, graphNS string) string { return ns + "/" + graph.DefaultReaderClusterRole + "-" + graphNS }
	unlabeled := readerBinding("argocd", "team-x")
	unlabeled.Labels = nil
	otherRole := readerBinding("argocd", "team-y")
	otherRole.RoleRef.Name = "admin"
	tests := []struct {
		name     string
		allowed  []string
		objs     []client.Object
		graphs   map[string][]*graph.Graph
		wantLeft []string
	}{
		{
			name: "a namespace that is gone",
			objs: []client.Object{readerBinding("argocd", "gone"), readerBinding("flux-system", "gone")},
		},
		{
			name: "a namespace with no Graph left",
			objs: []client.Object{applierBinding("idle", false, "argocd"), readerBinding("argocd", "idle")},
		},
		{
			name:     "a namespace whose Graph reads argocd keeps it",
			objs:     []client.Object{applierBinding("live", false, "argocd,flux-system"), readerBinding("argocd", "live"), readerBinding("flux-system", "live")},
			graphs:   map[string][]*graph.Graph{"live": {readsGraph("live", "argocd")}},
			wantLeft: []string{reader("argocd", "live")},
		},
		{
			name:     "with every namespace allowed, a binding outside the named ones",
			allowed:  []string{graph.AllNamespaces},
			objs:     []client.Object{readerBinding("prod", "gone"), readerBinding("prod", "live")},
			graphs:   map[string][]*graph.Graph{"live": {readsGraph("live", "prod")}},
			wantLeft: []string{reader("prod", "live")},
		},
		{
			name:     "bindings the controller did not create stay",
			objs:     []client.Object{unlabeled, otherRole},
			wantLeft: []string{reader("argocd", "team-x"), reader("argocd", "team-y")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(tt.objs...).Build()
			allowed := tt.allowed
			if allowed == nil {
				allowed = graph.DefaultReaderNamespaces
			}
			s := &graphcleanup.Sweep{APIReader: c, Graphs: &graphLister{graphs: tt.graphs},
				Identity: &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: allowed}}

			require.NoError(t, s.Run(context.Background()))
			var list rbacv1.RoleBindingList
			require.NoError(t, c.List(context.Background(), &list))
			var left []string
			for _, rb := range list.Items {
				if rb.Name != graph.DefaultApplierClusterRole {
					left = append(left, rb.Namespace+"/"+rb.Name)
				}
			}
			assert.ElementsMatch(t, tt.wantLeft, left)
		})
	}
}

// TestSweep_ListError keeps going past a namespace whose Graphs cannot be
// listed and reports the error.
func TestSweep_ListError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(readerBinding("argocd", "gone")).Build()
	s := &graphcleanup.Sweep{APIReader: c, Graphs: &graphLister{err: errors.New("boom")},
		Identity: &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: graph.DefaultReaderNamespaces}}
	require.ErrorContains(t, s.Run(context.Background()), "boom")
	assert.Len(t, readerBindingKeys(t, c), 1)
}

// TestSweep_Start runs a sweep at once, again every Interval, and returns when
// the context ends. It runs on the leader only.
func TestSweep_Start(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(readerBinding("argocd", "gone")).Build()
	lister := &graphLister{}
	s := &graphcleanup.Sweep{APIReader: c, Graphs: lister, Interval: time.Hour,
		Identity: &graph.IdentityProvisioner{Writer: c, Reader: c, ReaderNamespaces: graph.DefaultReaderNamespaces}}
	assert.True(t, s.NeedLeaderElection())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- s.Start(ctx) }()
	require.Eventually(t, func() bool { return len(readerBindingKeys(t, c)) == 0 }, 5*time.Second, 10*time.Millisecond,
		"the startup sweep deletes the leaked binding")
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after the context ended")
	}
}

func TestSweep_NilIdentity(t *testing.T) {
	assert.NoError(t, (&graphcleanup.Sweep{}).Run(context.Background()))
}
