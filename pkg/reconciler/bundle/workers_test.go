// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/bundle"
)

// slowTranslator takes a while to create the Graph, as kro does, and counts
// the Graphs it creates.
type slowTranslator struct{ calls atomic.Int32 }

func (s *slowTranslator) Translate(_ context.Context, _ *kardinalv1alpha1.Pipeline, b *kardinalv1alpha1.Bundle) (string, error) {
	s.calls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return b.Name + "-graph", nil
}

// TestReconciler_WorkersKeepTheSlotCap (#1509): with several workers, the
// Available Bundles of one Pipeline are reconciled at once, and each counts
// the Promoting ones before it takes a slot. The count and the Promoting
// write run under the Pipeline's lock, so no more than
// maxConcurrentPromotions Bundles promote; Bundles of other Pipelines are
// not held by that lock. Run with -race.
//
// Covers PERF-WORKERS-01.
func TestReconciler_WorkersKeepTheSlotCap(t *testing.T) {
	// A newer Bundle supersedes the older ones of its type, so the
	// Bundles that compete for the one slot have different types.
	const limit = 1
	kinds := []string{"image", "config", "mixed"}
	t0 := time.Now().UTC().Add(-time.Hour)
	objs := []client.Object{}
	for _, name := range []string{"app", "other"} {
		p := lcPipeline(name, lcEnvs("test")...)
		p.Spec.MaxConcurrentPromotions = limit
		objs = append(objs, p)
	}
	var names []string
	for i, typ := range kinds {
		for _, pl := range []string{"app", "other"} {
			b := lcBundle(fmt.Sprintf("%s-%d", pl, i), typ, "Available", t0.Add(time.Duration(i)*time.Minute))
			b.Spec.Pipeline = pl
			b.Labels = map[string]string{"kardinal.io/pipeline": pl}
			objs = append(objs, b)
			names = append(names, b.Name)
		}
	}
	c := indexedBuilder(newScheme()).WithObjects(objs...).
		WithStatusSubresource(&kardinalv1alpha1.Bundle{}, &kardinalv1alpha1.Pipeline{}).Build()
	tr := &slowTranslator{}
	r := &bundle.Reconciler{Client: c, APIReader: c, Translator: tr, Workers: 16}

	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}})
			assert.NoError(t, err, name)
		}(name)
	}
	wg.Wait()

	promoting := map[string]int{}
	var list kardinalv1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &list))
	for _, b := range list.Items {
		if b.Status.Phase == "Promoting" {
			promoting[b.Spec.Pipeline]++
		}
	}
	assert.Equal(t, map[string]int{"app": limit, "other": limit}, promoting, "each Pipeline promotes exactly its cap")
	assert.Equal(t, int32(2*limit), tr.calls.Load(), "no Graph past the cap")
}

// TestReconciler_RecoveryAndAvailableShareTheSlotLock (QA #1554): with cap 1
// and no Bundle Promoting, a Failed Bundle whose step recovered and an
// Available Bundle of another type are reconciled at once. Both count a free
// slot; the count and the Promoting write run under the Pipeline's lock in
// both paths (handleSyncEvidence and handleAvailable), so exactly one ends
// Promoting. The slot count reads are slowed so the two reconciles overlap.
// Run with -race.
func TestReconciler_RecoveryAndAvailableShareTheSlotLock(t *testing.T) {
	t0 := time.Now().UTC().Add(-time.Hour)
	for trial := 0; trial < 20; trial++ {
		p := lcPipeline("app", lcEnvs("test")...)
		p.Spec.MaxConcurrentPromotions = 1
		failed := lcBundle("app-a", "image", "Failed", t0)
		failed.Status.GraphRef = "app-app-a"
		failed.Labels = map[string]string{"kardinal.io/pipeline": "app"}
		avail := lcBundle("app-b", "config", "Available", t0.Add(time.Minute))
		avail.Labels = map[string]string{"kardinal.io/pipeline": "app"}
		// The failed step was retried (kro recreated it): the Bundle would
		// go back to Promoting if a slot is free.
		step := lcStep("app-a", "test", "s-a", "Pending")
		base := indexedBuilder(newScheme()).WithObjects(p, failed, avail, step).
			WithStatusSubresource(&kardinalv1alpha1.Bundle{}, &kardinalv1alpha1.Pipeline{}, &kardinalv1alpha1.PromotionStep{}).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*kardinalv1alpha1.BundleList); ok {
						time.Sleep(5 * time.Millisecond)
					}
					return c.List(ctx, list, opts...)
				},
			}).Build()
		r := &bundle.Reconciler{Client: base, APIReader: base, Translator: &slowTranslator{}, Workers: 2}

		var wg sync.WaitGroup
		for _, name := range []string{"app-a", "app-b"} {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: name}})
				assert.NoError(t, err, name)
			}(name)
		}
		wg.Wait()

		var list kardinalv1alpha1.BundleList
		require.NoError(t, base.List(context.Background(), &list))
		var promoting []string
		for _, b := range list.Items {
			if b.Status.Phase == "Promoting" {
				promoting = append(promoting, b.Name)
			}
		}
		require.LessOrEqual(t, len(promoting), 1, "trial %d: one slot, Promoting %v", trial, promoting)
	}
}
