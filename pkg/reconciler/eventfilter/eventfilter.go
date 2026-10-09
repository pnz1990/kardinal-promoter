// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package eventfilter holds the watch predicate shared by the reconcilers that
// write their own status on every reconcile (PolicyGate, ScheduleClock,
// ChangeWindow, MetricCheck, NotificationHook, RollbackPolicy, Subscription).
package eventfilter

import (
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

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

// kroLabelPrefix marks the labels kro stamps on the objects a Graph creates
// (kro.run/node-id, kro.run/collection-index, kro.run/collection-size, ...).
const kroLabelPrefix = "kro.run/"

// LabelChangedExceptKro passes an update only when a label other than kro's
// own (kro.run/...) was added, removed or changed. Create, delete and generic
// events pass.
//
// kro re-stamps kro.run/collection-size on every item of a collection each
// time the collection grows (ledger gap G11), so with LabelChangedPredicate
// each new PromotionStep of a compact Graph would reconcile every step of
// the Bundle again.
var LabelChangedExceptKro = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		if e.ObjectOld == nil || e.ObjectNew == nil {
			return true
		}
		return !equalExceptKro(e.ObjectOld.GetLabels(), e.ObjectNew.GetLabels())
	},
}

func equalExceptKro(a, b map[string]string) bool {
	count := func(m map[string]string) int {
		n := 0
		for k := range m {
			if !strings.HasPrefix(k, kroLabelPrefix) {
				n++
			}
		}
		return n
	}
	if count(a) != count(b) {
		return false
	}
	for k, v := range a {
		if strings.HasPrefix(k, kroLabelPrefix) {
			continue
		}
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}
