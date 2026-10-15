// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// simQueue is a work queue on a simulated clock with the semantics of
// client-go's and the fair queue: a key is held once, and a key added again
// keeps its earliest ready time.
type simQueue struct {
	workqueue.TypedRateLimitingInterface[reconcile.Request]
	now   time.Time
	ready map[reconcile.Request]time.Time
}

func newSimQueue() *simQueue {
	return &simQueue{now: time.Unix(0, 0), ready: map[reconcile.Request]time.Time{}}
}

func (q *simQueue) Add(r reconcile.Request) { q.AddAfter(r, 0) }

func (q *simQueue) AddAfter(r reconcile.Request, d time.Duration) {
	at := q.now.Add(d)
	if cur, ok := q.ready[r]; !ok || at.Before(cur) {
		q.ready[r] = at
	}
}

// work takes every key that is ready, as a worker that keeps up does, and
// returns how many it took.
func (q *simQueue) work() int {
	n := 0
	for r, at := range q.ready {
		if !at.After(q.now) {
			delete(q.ready, r)
			n++
		}
	}
	return n
}

// reconcilesFor runs a wave: changes PromotionStep state changes of
// Pipeline "big" from Promoting to to, one every gap, through h, and returns
// the Pipeline reconciles, each a write of its whole status when the status
// changed.
func reconcilesFor(h handler.EventHandler, changes int, gap time.Duration, to string) int {
	q := newSimQueue()
	step := func(i int, state string) *kardinalv1alpha1.PromotionStep {
		return &kardinalv1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: fmt.Sprintf("big-v1-env%d", i)},
			Spec:   kardinalv1alpha1.PromotionStepSpec{PipelineName: "big"},
			Status: kardinalv1alpha1.PromotionStepStatus{State: state}}
	}
	reconciles := 0
	for i := range changes {
		h.Update(context.Background(), event.UpdateEvent{ObjectOld: step(i, "Promoting"), ObjectNew: step(i, to)}, q)
		reconciles += q.work()
		q.now = q.now.Add(gap)
		reconciles += q.work()
	}
	q.now = q.now.Add(statusCoalesce)
	return reconciles + q.work()
}

// TestCoalesced_OneStatusWritePerWindow (#1719): the step watch adds the
// Pipeline statusCoalesce later for a step that moves to an intermediate
// state, so a wave's changes within one window cost one Pipeline reconcile,
// and one write of the whole status.environments, instead of one each. A
// step that settles (Verified, Failed, ...) is not delayed: the end of a
// promotion shows at once.
func TestCoalesced_OneStatusWritePerWindow(t *testing.T) {
	tests := []struct {
		name          string
		changes       int
		gap           time.Duration
		to            string
		before, after int
	}{
		{"wave, a change every 100 ms", 100, 100 * time.Millisecond, "HealthChecking", 100, 5},
		{"burst, 500 changes at once", 500, 0, "WaitingForMerge", 500, 1},
		{"one change", 1, 0, "HealthChecking", 1, 1},
		{"changes further apart than the window", 3, 3 * time.Second, "HealthChecking", 3, 3},
		{"settled steps are not delayed", 100, 100 * time.Millisecond, "Verified", 100, 100},
		{"failed steps are not delayed", 3, 100 * time.Millisecond, "Failed", 3, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.before, reconcilesFor(handler.EnqueueRequestsFromMapFunc(pipelineForStep), tc.changes, tc.gap, tc.to), "before")
			assert.Equal(t, tc.after, reconcilesFor(coalesced(pipelineForStep, stepSettled), tc.changes, tc.gap, tc.to), "coalesced")
		})
	}
}

// TestCoalesced_MapsEveryEvent: the coalesced handler maps create, update
// (both objects), delete and generic events statusCoalesce later, and
// nothing for an object its map function skips.
func TestCoalesced_MapsEveryEvent(t *testing.T) {
	q := newSimQueue()
	h := coalesced(pipelineForBundle, func(event.UpdateEvent) bool { return false })
	b := func(p string) *kardinalv1alpha1.Bundle {
		return &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: p + "-v1"},
			Spec: kardinalv1alpha1.BundleSpec{Pipeline: p}}
	}
	ctx := context.Background()
	h.Create(ctx, event.CreateEvent{Object: b("a")}, q)
	h.Update(ctx, event.UpdateEvent{ObjectOld: b("b"), ObjectNew: b("c")}, q)
	h.Delete(ctx, event.DeleteEvent{Object: b("d")}, q)
	h.Generic(ctx, event.GenericEvent{Object: b("e")}, q)
	h.Create(ctx, event.CreateEvent{Object: b("")}, q)
	assert.Zero(t, q.work(), "nothing is ready before statusCoalesce")
	var got []string
	for r, at := range q.ready {
		assert.Equal(t, q.now.Add(statusCoalesce), at, r.Name)
		got = append(got, r.Namespace+"/"+r.Name)
	}
	assert.ElementsMatch(t, []string{"team/a", "team/b", "team/c", "team/d", "team/e"}, got)
}
