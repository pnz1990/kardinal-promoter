// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestGateAudit_FlipsWithinOneSecond is the root cause of #1484: a gate that
// flips Success, Failure, Success within one second. spec.timestamp is stored
// with one-second resolution, so ordering by it alone put the records in name
// order, and the second Success had the first one's name (the name carried
// Unix seconds) and was dropped as AlreadyExists. Now every flip has its own
// record and lifecycle.CompareAuditEvents lists them in the order of the
// flips, also when written again (idempotent).
func TestGateAudit_FlipsWithinOneSecond(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	c := fake.NewClientBuilder().WithScheme(s).Build()
	gate := &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "prod-audited", Namespace: "default",
		Labels: map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/bundle": "app-v2",
			"kardinal.io/environment": "prod", "kardinal.io/gate-name": "audited"}}}
	base := time.Date(2026, 10, 9, 12, 0, 0, 100_000_000, time.UTC)
	flips := []struct {
		outcome string
		at      time.Duration
	}{{"Failure", 0}, {"Success", 300 * time.Millisecond}, {"Failure", 600 * time.Millisecond}, {"Success", 800 * time.Millisecond}}
	for range 2 {
		for _, f := range flips {
			writeGateAuditEvent(context.Background(), c, gate, f.outcome, "r", metav1.NewTime(base.Add(f.at)))
		}
	}
	var list kardinalv1alpha1.AuditEventList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("default")))
	require.Len(t, list.Items, len(flips), "one record per flip, none dropped, none duplicated")
	for i := range 5 { // whatever order the list comes in
		items := append([]kardinalv1alpha1.AuditEvent(nil), list.Items...)
		for j := range items {
			k := (j + i) % len(items)
			items[j], items[k] = items[k], items[j]
		}
		sort.Slice(items, func(a, b int) bool { return lifecycle.CompareAuditEvents(&items[a], &items[b]) < 0 })
		var got []string
		for _, ae := range items {
			got = append(got, ae.Spec.Outcome)
			assert.Equal(t, base.Truncate(time.Second), ae.Spec.Timestamp.UTC(), "stored with second resolution")
		}
		assert.Equal(t, []string{"Failure", "Success", "Failure", "Success"}, got)
	}
}
