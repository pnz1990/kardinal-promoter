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
