// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package eventfilter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/eventfilter"
)

func gate(generation int64, annotations map[string]string, reason string) *kardinalv1alpha1.PolicyGate {
	return &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "g", Namespace: "default", Generation: generation, Annotations: annotations,
		},
		Status: kardinalv1alpha1.PolicyGateStatus{Reason: reason},
	}
}

// TestSpecOrAnnotationChanged: a status-only write does not re-trigger the
// reconcile, but a spec edit and an annotation change (the documented
// kardinal.io/force-recheck and kardinal.io/manual-tick) do.
func TestSpecOrAnnotationChanged(t *testing.T) {
	recheck := map[string]string{"kardinal.io/force-recheck": "1"}
	tests := []struct {
		name     string
		old, new *kardinalv1alpha1.PolicyGate
		want     bool
	}{
		{name: "status-only write", old: gate(1, nil, "a"), new: gate(1, nil, "b"), want: false},
		{name: "status-only write keeps annotations", old: gate(1, recheck, "a"), new: gate(1, recheck, "b"), want: false},
		{name: "spec edit", old: gate(1, nil, "a"), new: gate(2, nil, "a"), want: true},
		{name: "annotation added", old: gate(1, nil, "a"), new: gate(1, recheck, "a"), want: true},
		{name: "annotation value changed",
			old: gate(1, recheck, "a"), new: gate(1, map[string]string{"kardinal.io/force-recheck": "2"}, "a"), want: true},
		{name: "manual-tick annotation",
			old: gate(1, nil, "a"), new: gate(1, map[string]string{"kardinal.io/manual-tick": "1"}, "a"), want: true},
		{name: "annotation removed", old: gate(1, recheck, "a"), new: gate(1, nil, "a"), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eventfilter.SpecOrAnnotationChanged.Update(event.UpdateEvent{ObjectOld: tt.old, ObjectNew: tt.new})
			assert.Equal(t, tt.want, got)
		})
	}

	g := gate(1, nil, "")
	assert.True(t, eventfilter.SpecOrAnnotationChanged.Create(event.CreateEvent{Object: g}),
		"existing objects are reconciled when the controller starts")
	assert.True(t, eventfilter.SpecOrAnnotationChanged.Delete(event.DeleteEvent{Object: g}))
	assert.True(t, eventfilter.SpecOrAnnotationChanged.Generic(event.GenericEvent{Object: g}))
}
