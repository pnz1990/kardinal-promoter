// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestSetPhase_RecordsWhenThePhaseChanged (#1945): status.phaseTransitionTime
// moves when the phase changes, and only then, so a re-run reconcile and a
// retried notification keep the time of the change.
func TestSetPhase_RecordsWhenThePhaseChanged(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)
	b := &kardinalv1alpha1.Bundle{}
	setPhase(b, phasePromoting, t0)
	require.NotNil(t, b.Status.PhaseTransitionTime)
	assert.Equal(t, t0, b.Status.PhaseTransitionTime.UTC())

	setPhase(b, phasePromoting, t0.Add(time.Minute))
	assert.Equal(t, t0, b.Status.PhaseTransitionTime.UTC(), "same phase: the time stays")

	setPhase(b, phaseVerified, t0.Add(8*time.Minute))
	assert.Equal(t, phaseVerified, b.Status.Phase)
	assert.Equal(t, t0.Add(8*time.Minute), b.Status.PhaseTransitionTime.UTC())

	supersede(b, t0.Add(9*time.Minute))
	assert.Equal(t, t0.Add(9*time.Minute), b.Status.PhaseTransitionTime.UTC())
}
