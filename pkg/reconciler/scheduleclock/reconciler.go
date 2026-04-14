// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package scheduleclock implements the ScheduleClockReconciler.
//
// The ScheduleClock CRD exists solely to generate Kubernetes watch events on a
// configurable interval by updating status.tick. PolicyGate reconcilers that
// Watch ScheduleClock objects re-evaluate their expressions on every tick without
// needing a separate RequeueAfter timer loop.
//
// Architecture context:
//
//	This reconciler is an Owned node (Q2 in the Graph-first question stack):
//	  - It writes to its own CRD status (status.tick) — the only CRD field it touches.
//	  - It calls time.Now() only inside a CRD status write — no logic leak.
//	  - It has no external HTTP calls, no cross-CRD mutations, no exec.Command.
//
// This eliminates PG-4 from docs/design/11-graph-purity-tech-debt.md:
// the PolicyGate reconciler no longer needs ctrl.Result{RequeueAfter: recheckInterval}
// for time-based gates — instead it Watches ScheduleClock and re-evaluates on tick.
package scheduleclock

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

const (
	// defaultInterval is the tick interval when spec.interval is empty or invalid.
	defaultInterval = time.Minute
	// minInterval prevents reconciler hot-loops from spec misconfiguration.
	minInterval = 5 * time.Second
)

// Reconciler writes status.tick on every spec.interval, generating watch events.
// It is idempotent and safe to re-run after a crash.
type Reconciler struct {
	client.Client
	// NowFn returns the current time. Overridable for testing.
	NowFn func() time.Time
}

// Reconcile processes a single ScheduleClock object.
//
// State machine (all paths write status.tick, then requeue):
//  1. Not found → deleted, skip.
//  2. Write status.tick = time.Now().UTC().Format(time.RFC3339).
//  3. Requeue after spec.interval (default 1m, minimum 5s).
//
// The tick write generates a Kubernetes watch event that flows to any
// controller Watching this ScheduleClock (e.g. the PolicyGate reconciler).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().
		Str("scheduleclock", req.Name).
		Str("namespace", req.Namespace).
		Logger()

	var clock kardinalv1alpha1.ScheduleClock
	if err := r.Get(ctx, req.NamespacedName, &clock); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get scheduleclock: %w", err)
	}

	interval := parseInterval(clock.Spec.Interval)

	// Write status.tick — this is the sole purpose of this reconciler.
	// time.Now() is called here (inside a CRD status write) — no logic leak.
	now := r.now()
	patch := client.MergeFrom(clock.DeepCopy())
	clock.Status.Tick = now.UTC().Format(time.RFC3339)
	if err := r.Status().Patch(ctx, &clock, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating tick: %w", err)
	}

	log.Debug().
		Str("tick", clock.Status.Tick).
		Dur("interval", interval).
		Msg("scheduleclock tick")

	return ctrl.Result{RequeueAfter: interval}, nil
}

// SetupWithManager registers the ScheduleClockReconciler with the controller-runtime Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kardinalv1alpha1.ScheduleClock{}).
		Complete(r)
}

// now returns the current time, using NowFn if set (for testing).
func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now().UTC()
}

// parseInterval parses a Go duration string, returning defaultInterval on error.
// Enforces a minimum of minInterval to prevent hot loops.
func parseInterval(s string) time.Duration {
	if s == "" {
		return defaultInterval
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return defaultInterval
	}
	if d < minInterval {
		return minInterval
	}
	return d
}
