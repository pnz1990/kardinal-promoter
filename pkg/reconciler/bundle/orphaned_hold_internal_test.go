// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPipelineSpecHash_OrphanedHolds (#1629): an orphaned hold changes the
// Graph without a spec change, so it changes the hash and the Pipeline watch
// passes it; with no orphaned hold the hash is the spec's as before.
//
// Covers RB-HOLD-03.
func TestPipelineSpecHash_OrphanedHolds(t *testing.T) {
	p := &kardinalv1alpha1.Pipeline{Spec: kardinalv1alpha1.PipelineSpec{
		Holds: []kardinalv1alpha1.EnvironmentHold{{Environment: "prod", Bundle: "app-rollback-1", Reason: "r"}}}}
	base := pipelineSpecHashFor(p)

	missing := p.DeepCopy()
	missing.Status.HoldStates = []kardinalv1alpha1.EnvironmentHoldState{
		{Environment: "prod", Bundle: "app-rollback-1", State: kardinalv1alpha1.HoldStateBundleMissing}}
	assert.Equal(t, base, pipelineSpecHashFor(missing), "within the grace the hold still holds")
	assert.False(t, orphanedHoldsChanged.Update(event.UpdateEvent{ObjectOld: p, ObjectNew: missing}))

	orphaned := missing.DeepCopy()
	orphaned.Status.HoldStates[0].State = kardinalv1alpha1.HoldStateOrphaned
	assert.NotEqual(t, base, pipelineSpecHashFor(orphaned), "orphaned: the Graph is rebuilt")
	assert.True(t, orphanedHoldsChanged.Update(event.UpdateEvent{ObjectOld: missing, ObjectNew: orphaned}))
	assert.True(t, orphanedHoldsChanged.Update(event.UpdateEvent{ObjectOld: orphaned, ObjectNew: p}), "active again")
}
