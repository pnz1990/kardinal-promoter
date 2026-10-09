// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// gateGraph is a Graph whose PolicyGateData declares the named instances,
// with ResourcesConverged set to status/reason/message.
func gateGraph(names []string, status metav1.ConditionStatus, reason, message string) *graph.Graph {
	var items []interface{}
	for _, n := range names {
		items = append(items, map[string]interface{}{"name": n, "environment": "prod", "t": 0})
	}
	g := &graph.Graph{}
	if len(items) > 0 {
		g.Spec.Nodes = []graph.GraphNode{{ID: graph.NodePolicyGateData, Def: map[string]interface{}{
			"templates": []interface{}{map[string]interface{}{}}, "gates": items}}}
	}
	g.Status.Conditions = []metav1.Condition{{Type: "ResourcesConverged", Status: status, Reason: reason, Message: message}}
	return g
}

// TestCheckGatesCreated covers the GatesCreated condition: True when every
// declared gate instance exists; False, naming the missing ones and kro's
// message, when kro cannot apply them; untouched while they are still being
// created; removed for a Graph without gates.
func TestCheckGatesCreated(t *testing.T) {
	quota := `apply "PolicyGates": item 1: policygates.kardinal.io "b-prod-gate" is forbidden: exceeded quota`
	many := make([]string, 8)
	for i := range many {
		many[i] = fmt.Sprintf("gate-%d", i)
	}
	tests := []struct {
		name       string
		graph      *graph.Graph
		existing   []string
		wantStatus metav1.ConditionStatus // "" means no condition
		wantMsg    []string
	}{
		{name: "all created", graph: gateGraph([]string{"b-prod-gate"}, metav1.ConditionTrue, "Applied", ""),
			existing: []string{"b-prod-gate"}, wantStatus: metav1.ConditionTrue, wantMsg: []string{"all 1 PolicyGate instances exist"}},
		{name: "apply failed", graph: gateGraph([]string{"b-prod-gate", "b-uat-gate"}, metav1.ConditionFalse, "ApplyFailed", quota),
			existing: []string{"b-uat-gate"}, wantStatus: metav1.ConditionFalse,
			wantMsg: []string{"1 of 2 PolicyGate instances are not created (b-prod-gate)", "every gated environment waits", "exceeded quota"}},
		{name: "soft failure naming the gate", graph: gateGraph([]string{"b-prod-gate"}, metav1.ConditionFalse, "WaitingForReadiness",
			`apply "PolicyGates": b-prod-gate: invalid request`), wantStatus: metav1.ConditionFalse, wantMsg: []string{"b-prod-gate"}},
		{name: "still being created", graph: gateGraph([]string{"b-prod-gate"}, metav1.ConditionFalse, "DataPending",
			`apply "prod": index out of bounds`)},
		{name: "many missing", graph: gateGraph(many, metav1.ConditionFalse, "ApplyFailed", "denied"),
			wantStatus: metav1.ConditionFalse, wantMsg: []string{"8 of 8", "gate-0, gate-1, gate-2, gate-3, gate-4 and 3 more"}},
		{name: "no gates", graph: gateGraph(nil, metav1.ConditionTrue, "Applied", "")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := runtime.NewScheme()
			require.NoError(t, kardinalv1alpha1.AddToScheme(s))
			var objs []client.Object
			for _, n := range tc.existing {
				objs = append(objs, &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: "default",
					Labels: map[string]string{"kardinal.io/bundle": "b"}}})
			}
			// A gate of another Bundle with a declared name does not count.
			objs = append(objs, &kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "b-prod-gate-other", Namespace: "default",
				Labels: map[string]string{"kardinal.io/bundle": "other"}}})
			r := &Reconciler{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()}
			b := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "default"}}
			b.Status.Conditions = []metav1.Condition{{Type: condGatesCreated, Status: metav1.ConditionTrue, Reason: "Created"}}
			if tc.name == "still being created" {
				b.Status.Conditions = nil
			}
			require.NoError(t, r.checkGatesCreated(context.Background(), b, tc.graph))
			c := meta.FindStatusCondition(b.Status.Conditions, condGatesCreated)
			if tc.wantStatus == "" {
				assert.Nil(t, c)
				return
			}
			require.NotNil(t, c)
			assert.Equal(t, tc.wantStatus, c.Status)
			for _, m := range tc.wantMsg {
				assert.Contains(t, c.Message, m)
			}
			// Idempotent: a second check changes nothing.
			before := c.DeepCopy()
			require.NoError(t, r.checkGatesCreated(context.Background(), b, tc.graph))
			after := meta.FindStatusCondition(b.Status.Conditions, condGatesCreated)
			assert.Equal(t, before.Message, after.Message)
			assert.Equal(t, before.LastTransitionTime, after.LastTransitionTime)
		})
	}
}
