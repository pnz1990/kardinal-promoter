// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestWaitEventsWaitsForALateEvent (#2188): the Event a test waits for is
// recorded a moment after the status it already saw; WaitEvents lists until
// it exists, where one list would have missed it.
func TestWaitEventsWaitsForALateEvent(t *testing.T) {
	kube := fake.NewSimpleClientset()
	e := &Env{Kube: kube}
	regarding := corev1.ObjectReference{Kind: "PromotionStep", Name: "step"}
	early, err := e.Events(context.Background(), "ns", "PromotionStep", "step")
	require.NoError(t, err)
	assert.Empty(t, early, "a single list before the Event is recorded finds nothing")
	go func() {
		time.Sleep(2 * Poll)
		_, _ = kube.EventsV1().Events("ns").Create(context.Background(), &eventsv1.Event{
			ObjectMeta: metav1.ObjectMeta{Name: "ev", Namespace: "ns"}, Regarding: regarding,
			Reason: "GitOpsRefreshSkipped", Type: corev1.EventTypeWarning}, metav1.CreateOptions{})
	}()
	evs := e.WaitEvents(t, "ns", "PromotionStep", "step", time.Minute, "the late Event",
		func(ev eventsv1.Event) bool { return ev.Reason == "GitOpsRefreshSkipped" })
	require.Len(t, evs, 1)
	assert.Equal(t, corev1.EventTypeWarning, evs[0].Type)
}
