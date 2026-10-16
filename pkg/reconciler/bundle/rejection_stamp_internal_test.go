// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// TestRejectionStamp (#2001 QA B3, #2024 QA): a rejection's stamp is
// recorded only when it names the rejecting user and, for an agent, when the
// AgentIndex vouches for it: while the index is missing a person's stamp is
// still recorded and an agent's is not (QA's case: a person stamping agent
// fixer of carol).
func TestRejectionStamp(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	ready := &kardinalv1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"},
		Status: kardinalv1alpha1.AgentIndexStatus{
			Users:      map[string]kardinalv1alpha1.AgentIndexEntry{"system:serviceaccount:ci:fixer": {Agent: "fixer", Sponsor: "alice"}},
			Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Built", LastTransitionTime: metav1.Now()}}}}
	alice := &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorHuman, Principal: "alice"}
	forged := &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorAgent, Principal: "alice", Agent: "fixer", Sponsor: "carol"}
	fx := &kardinalv1alpha1.Actor{Kind: kardinalv1alpha1.ActorAgent, Principal: "system:serviceaccount:ci:fixer",
		Agent: "fixer", Sponsor: "alice"}
	for _, tc := range []struct {
		name  string
		index bool
		rej   *kardinalv1alpha1.BundleRejection
		kept  bool
	}{
		{"a person's stamp", true, &kardinalv1alpha1.BundleRejection{By: "alice", Actor: alice}, true},
		{"a person's stamp, index missing", false, &kardinalv1alpha1.BundleRejection{By: "alice", Actor: alice}, true},
		{"someone else's stamp", true, &kardinalv1alpha1.BundleRejection{By: "bob", Actor: alice}, false},
		{"a person stamped agent, index missing", false, &kardinalv1alpha1.BundleRejection{By: "alice", Actor: forged}, false},
		{"a person stamped agent", true, &kardinalv1alpha1.BundleRejection{By: "alice", Actor: forged}, false},
		{"the agent", true, &kardinalv1alpha1.BundleRejection{By: fx.Principal, Actor: fx}, true},
		{"the agent, index missing", false, &kardinalv1alpha1.BundleRejection{By: fx.Principal, Actor: fx}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(s)
			if tc.index {
				b = b.WithObjects(ready)
			}
			var c client.Client = b.Build()
			r := &Reconciler{Agents: &actor.Source{Reader: c, Name: "kardinal-promoter"}}
			got, note := r.rejectionStamp(context.Background(), tc.rej)
			assert.Equal(t, tc.kept, got != nil, note)
			assert.Equal(t, !tc.kept, note != "", "a dropped stamp is said in the message")
		})
	}
}
