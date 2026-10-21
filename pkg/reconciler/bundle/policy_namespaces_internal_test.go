// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPolicyNamespacesChanged (#2286): a Pipeline update whose
// status.policyNamespaces changed wakes its Bundles (a version waiting for a
// namespace to agree); any other status update does not.
func TestPolicyNamespacesChanged(t *testing.T) {
	pipeline := func(st *kardinalv1alpha1.PipelinePolicyNamespaces, phase string) *kardinalv1alpha1.Pipeline {
		return &kardinalv1alpha1.Pipeline{Status: kardinalv1alpha1.PipelineStatus{PolicyNamespaces: st, Phase: phase}}
	}
	refused := &kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: 2, NotShared: []string{"team-b"}}
	agreed := &kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: 2}
	for name, tc := range map[string]struct {
		old, new *kardinalv1alpha1.Pipeline
		want     bool
	}{
		"the namespace agreed":       {pipeline(refused, "Ready"), pipeline(agreed, "Ready"), true},
		"first checked":              {pipeline(nil, "Ready"), pipeline(agreed, "Ready"), true},
		"another generation checked": {pipeline(agreed, "Ready"), pipeline(&kardinalv1alpha1.PipelinePolicyNamespaces{ObservedGeneration: 3}, "Ready"), true},
		"another status field":       {pipeline(refused, "Ready"), pipeline(refused, "Promoting"), false},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, policyNamespacesChanged.Update(event.UpdateEvent{ObjectOld: tc.old, ObjectNew: tc.new}))
		})
	}
}
