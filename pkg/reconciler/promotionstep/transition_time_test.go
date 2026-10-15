// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// TestTransition_RecordsStateTransitionTime (#1945): a state change records
// when it happened in status.stateTransitionTime, from the reconciler's
// clock; a reconcile that changes nothing keeps it.
func TestTransition_RecordsStateTransitionTime(t *testing.T) {
	now := time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)
	ps := makeStep("step", "nginx-demo", "bundle-1", "test")
	c := newClient(t, ps, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"))
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
		WorkDirFn: func(_, _ string) string { return t.TempDir() }, NowFn: func() time.Time { return now }}
	reconcileStep(t, r, "step")
	got := getStep(t, c, "step")
	require.NotEqual(t, "", got.Status.State)
	require.NotNil(t, got.Status.StateTransitionTime, "state %s", got.Status.State)
	assert.Equal(t, now, got.Status.StateTransitionTime.UTC())
}
