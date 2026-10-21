// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// TestSourceResolve_IndexNotReady (#2451): while the AgentIndex is missing
// or not Ready (never built, IndexFull, a later release's CompactFormat), a
// requester who is not a ServiceAccount is resolved as unverified, for the
// controller (Name set) and the CLI (no Name) alike, never human and never
// refused; a ServiceAccount gets ErrIndexNotReady. A Ready index derives the
// kind again. Describe says unverified.
// Covers ACTOR-INDEX-06.
func TestSourceResolve_IndexNotReady(t *testing.T) {
	sch := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(sch))
	index := func(reason string) *v1alpha1.AgentIndex {
		idx := &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}}
		switch reason {
		case "":
		case "Built":
			idx.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: reason}}
		default:
			idx.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: reason}}
		}
		return idx
	}
	const sa = "system:serviceaccount:ci:deployer"
	for name, objs := range map[string][]client.Object{
		"missing":       nil,
		"never built":   {index("")},
		"IndexFull":     {index("IndexFull")},
		"CompactFormat": {index(actor.ReasonCompactFormat)},
	} {
		for _, src := range []struct {
			who  string
			name string
		}{{"the controller", "kardinal-promoter"}, {"the CLI", ""}} {
			t.Run(name+"/"+src.who, func(t *testing.T) {
				s := &actor.Source{Reader: fake.NewClientBuilder().WithScheme(sch).WithObjects(objs...).Build(), Name: src.name}
				r, err := s.Resolve(context.Background(), actor.User{Username: "alice", Groups: []string{"oidc:release"}}, actor.Where{Namespace: "x"})
				require.NoError(t, err)
				assert.Equal(t, v1alpha1.Actor{Kind: v1alpha1.ActorUnverified, Principal: "alice"}, r.Actor)
				assert.Nil(t, r.Entry)
				assert.Equal(t, "alice (unverified)", actor.Describe(r.Actor))
				_, err = s.Resolve(context.Background(), actor.User{Username: sa}, actor.Where{Namespace: "x"})
				assert.ErrorIs(t, err, actor.ErrIndexNotReady, "a ServiceAccount waits for the index")
			})
		}
	}
	s := &actor.Source{Reader: fake.NewClientBuilder().WithScheme(sch).WithObjects(index("Built")).Build(), Name: "kardinal-promoter"}
	r, err := s.Resolve(context.Background(), actor.User{Username: "alice"}, actor.Where{Namespace: "x"})
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.ActorHuman, r.Actor.Kind, "a Ready index: a person is human")
	r, err = s.Resolve(context.Background(), actor.User{Username: sa}, actor.Where{Namespace: "x"})
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.ActorBot, r.Actor.Kind, "a Ready index: a ServiceAccount is a bot")
}
