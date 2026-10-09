// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// staleStepClient serves the PromotionStep from *stale while it is set, as a
// controller cache that has not seen the latest write yet.
func staleStepClient(t *testing.T, stale **v1alpha1.PromotionStep, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(buildScheme(t)).
		WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if s, ok := obj.(*v1alpha1.PromotionStep); ok && *stale != nil {
				(*stale).DeepCopyInto(s)
				return nil
			}
			return cl.Get(ctx, key, obj, opts...)
		}}).Build()
}

func eventReasons(rec *events.FakeRecorder) []string {
	var reasons []string
	for _, e := range drain(rec) {
		reasons = append(reasons, strings.Join(strings.Fields(e)[:2], " "))
	}
	return reasons
}

// TestStaleRead_StateTransitionRecordedOnce (QA #1541): a reconcile that
// reads the step from a stale cache, before the Pending -> Promoting
// transition the reconcile before it wrote, repeats the transition. The state
// patch is locked on the resourceVersion it read, so the stale reconcile gets
// a Conflict and records nothing: one Promoting Event and one
// PromotionStarted AuditEvent, and the promotion still reaches Verified.
func TestStaleRead_StateTransitionRecordedOnce(t *testing.T) {
	var stale *v1alpha1.PromotionStep
	c := staleStepClient(t, &stale, labelled(makeStep("step", "p", "b1", "test")), makePipeline("p"), makeBundle("b1", "p"))
	rec := events.NewFakeRecorder(50)
	r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{}, Recorder: rec,
		WorkDirFn: func(_, _ string) string { return t.TempDir() }}

	before := getStep(t, c, "step")
	reconcileStep(t, r, "step")
	after := getStep(t, c, "step")
	require.Equal(t, "Promoting", after.Status.State)
	require.Equal(t, []string{"Normal Promoting"}, eventReasons(rec))

	stale = &before
	reconcileStep(t, r, "step")
	stale = nil
	assert.Empty(t, eventReasons(rec), "no second Promoting Event")
	assert.Equal(t, []string{"PromotionStarted"}, auditActions(t, c), "no second PromotionStarted AuditEvent")
	assert.Equal(t, after.Status, getStep(t, c, "step").Status, "the stale reconcile wrote nothing")

	for i := 0; i < 10 && getStep(t, c, "step").Status.State != "Verified"; i++ {
		reconcileStep(t, r, "step")
	}
	require.Equal(t, "Verified", getStep(t, c, "step").Status.State)
	assert.Equal(t, []string{"Normal HealthChecking", "Normal Verified"}, eventReasons(rec))
	assert.Equal(t, []string{"PromotionStarted", "PromotionSucceeded"}, auditActions(t, c))
}
