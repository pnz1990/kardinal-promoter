// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package notificationhook

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestEventTimeIsWhenItHappened covers #1945: a Bundle or PromotionStep
// event carries the time of the change it reports (the CloudEvents time,
// and the order events are sent in), not the object's creation, and a
// Bundle written before status.phaseTransitionTime existed falls back to
// its Ready condition's transition.
func TestEventTimeIsWhenItHappened(t *testing.T) {
	created := time.Date(2026, 10, 15, 3, 56, 35, 0, time.UTC)
	later := func(m int) *metav1.Time { t := metav1.NewTime(created.Add(time.Duration(m) * time.Minute)); return &t }
	bundle := func(name, phase string, changed *metav1.Time) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", CreationTimestamp: metav1.NewTime(created)},
			Spec: v1alpha1.BundleSpec{Pipeline: "app"}, Status: v1alpha1.BundleStatus{Phase: phase, PhaseTransitionTime: changed}}
	}
	legacy := bundle("legacy", "Verified", nil)
	legacy.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Verified",
		LastTransitionTime: *later(9)}}
	step := func(name, state string, changed *metav1.Time, steps ...v1alpha1.StepStatus) *v1alpha1.PromotionStep {
		return &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", CreationTimestamp: metav1.NewTime(created)},
			Spec: v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: "b", Environment: "prod"},
			Status: v1alpha1.PromotionStepStatus{State: state, StateTransitionTime: changed, Steps: steps,
				PRURL: map[bool]string{true: "https://scm/pr/1"}[len(steps) > 0]}}
	}
	objs := []client.Object{
		bundle("verified", "Verified", later(8)),
		bundle("superseded", "Superseded", later(12)),
		bundle("failed", "Failed", later(5)),
		legacy,
		step("failed-step", "Failed", later(4)),
		step("waiting", "WaitingForMerge", later(7), v1alpha1.StepStatus{Name: "open-pr", State: v1alpha1.StepExecutionCompleted,
			CompletedAt: later(6)}),
	}
	s := k8sruntime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()}
	hook := &v1alpha1.NotificationHook{ObjectMeta: metav1.ObjectMeta{Name: "h", Namespace: "ns"},
		Spec: v1alpha1.NotificationHookSpec{Events: []v1alpha1.NotificationHookEventType{
			v1alpha1.NotificationEventBundleVerified, v1alpha1.NotificationEventBundleSuperseded, v1alpha1.NotificationEventBundleFailed,
			v1alpha1.NotificationEventPromotionStepFailed, v1alpha1.NotificationEventPromotionStepPROpened,
			v1alpha1.NotificationEventPromotionStepWaitingForApproval}}}
	events, err := r.qualifyingEvents(context.Background(), hook)
	require.NoError(t, err)
	got := map[string]time.Duration{}
	var order []string
	for _, ev := range events {
		got[ev.eventKey] = ev.at.Sub(created)
		order = append(order, ev.eventKey)
	}
	assert.Equal(t, map[string]time.Duration{
		"Bundle.Verified/verified":                 8 * time.Minute,
		"Bundle.Superseded/superseded":             12 * time.Minute,
		"Bundle.Failed/failed":                     5 * time.Minute,
		"Bundle.Verified/legacy":                   9 * time.Minute,
		"PromotionStep.Failed/failed-step":         4 * time.Minute,
		"PromotionStep.PROpened/waiting":           6 * time.Minute,
		"PromotionStep.WaitingForApproval/waiting": 7 * time.Minute,
	}, got)
	assert.Equal(t, []string{"PromotionStep.Failed/failed-step", "Bundle.Failed/failed", "PromotionStep.PROpened/waiting",
		"PromotionStep.WaitingForApproval/waiting", "Bundle.Verified/verified", "Bundle.Verified/legacy",
		"Bundle.Superseded/superseded"}, order, "sent oldest first, by when each happened")
}
