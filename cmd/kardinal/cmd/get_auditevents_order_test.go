// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestGetAuditEventsFn_FlipsWithinOneSecond (#1484, #1513): records stored
// within one second (spec.timestamp has one-second resolution) list newest
// first by kardinal.io/created-at, not by name; a record without the
// annotation (an older controller) sorts before those that have it; records
// equal in both keep name order; --limit keeps the newest.
//
// Covers GATE-AUDIT-02.
func TestGetAuditEventsFn_FlipsWithinOneSecond(t *testing.T) {
	second := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	ae := func(name, outcome string, at time.Duration, stamped bool) *v1alpha1.AuditEvent {
		e := &v1alpha1.AuditEvent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
				Labels: map[string]string{"kardinal.io/pipeline": "app"}},
			Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.NewTime(second), PipelineName: "app", BundleName: "app-v1",
				Environment: "prod", Action: "GateEvaluated", Outcome: outcome},
		}
		if stamped {
			lifecycle.StampCreatedAt(e, second.Add(at))
		}
		return e
	}
	// Names in the opposite order of the flips, so name order is wrong.
	objs := []*v1alpha1.AuditEvent{
		ae("z-first", "Failure", 100*time.Millisecond, true),
		ae("y-second", "Success", 300*time.Millisecond, true),
		ae("x-third", "Failure", 600*time.Millisecond, true),
		ae("w-old-controller", "Success", 0, false),
		ae("b-same", "Success", 900*time.Millisecond, true),
		ae("a-same", "Success", 900*time.Millisecond, true),
	}
	b := fake.NewClientBuilder().WithScheme(buildGetAuditEventsScheme(t))
	for _, o := range objs {
		b = b.WithObjects(o)
	}
	c := b.Build()

	names := func(limit int) []string {
		t.Helper()
		var buf bytes.Buffer
		prev := globalOutput
		globalOutput = "json"
		defer func() { globalOutput = prev }()
		require.NoError(t, getAuditEventsFn(&buf, c, "default", "", "", "", limit))
		var got []v1alpha1.AuditEvent
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		var n []string
		for _, e := range got {
			n = append(n, e.Name)
		}
		return n
	}
	assert.Equal(t, []string{"b-same", "a-same", "x-third", "y-second", "z-first", "w-old-controller"}, names(0))
	assert.Equal(t, []string{"b-same", "a-same", "x-third"}, names(3), "--limit keeps the newest")
}
