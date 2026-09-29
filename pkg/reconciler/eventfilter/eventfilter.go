// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package eventfilter holds the watch predicate shared by the reconcilers that
// write their own status on every reconcile (PolicyGate, ScheduleClock,
// ChangeWindow, MetricCheck, NotificationHook, RollbackPolicy, Subscription).
package eventfilter

import "sigs.k8s.io/controller-runtime/pkg/predicate"

// SpecOrAnnotationChanged passes create, delete and generic events, and an
// update only when the spec (metadata.generation) or the annotations changed.
//
// The reconciler's own status write changes neither, so it does not
// re-trigger the reconcile (C04-gates-36). An annotation change does, so
// `kubectl annotate <kind> <name> kardinal.io/force-recheck=$(date +%s)
// --overwrite` forces a re-evaluation, as docs/troubleshooting.md documents.
// Any annotation key works; the documented ones are conventions.
var SpecOrAnnotationChanged = predicate.Or(
	predicate.GenerationChangedPredicate{},
	predicate.AnnotationChangedPredicate{},
)
