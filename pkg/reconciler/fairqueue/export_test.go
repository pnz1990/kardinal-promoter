// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package fairqueue

import (
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Test-only helpers: production code does not call these (make hygiene).

// Aging sets how long a key may be due before it is raised back to its
// base priority (DefaultAging). Zero turns aging off.
func Aging(d time.Duration) Option { return func(q *Queue) { q.aging = d } }

// New wraps a controller-runtime priority queue named name with
// rateLimiter. It has the signature of controller.Options.NewQueue.
func New(name string, rateLimiter workqueue.TypedRateLimiter[reconcile.Request]) workqueue.TypedRateLimitingInterface[reconcile.Request] {
	return newQueue(name, rateLimiter, logr.Discard())
}
