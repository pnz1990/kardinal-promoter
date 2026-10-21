// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
)

// TestStampOf_IndexNotReady (#2451): while no AgentIndex is Ready (none
// yet, one never built, IndexFull, a later release's CompactFormat) the CLI
// stamps a person unverified, as the chart's actor policies require then,
// never human; a ServiceAccount gets no stamp and a warning (the policy
// refuses its checked write until the index is Ready). A Ready index stamps
// a person human again.
// Covers ACTOR-INDEX-06.
func TestStampOf_IndexNotReady(t *testing.T) {
	notReady := func(reason string) *v1alpha1.AgentIndex {
		idx := &v1alpha1.AgentIndex{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter"}}
		if reason != "" {
			idx.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: reason}}
		}
		return idx
	}
	for name, objs := range map[string][]sigs_client.Object{
		"no AgentIndex": nil,
		"never built":   {notReady("")},
		"IndexFull":     {notReady("IndexFull")},
		"CompactFormat": {notReady(actor.ReasonCompactFormat)},
	} {
		t.Run(name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(rejectScheme(t)).WithObjects(objs...).Build()
			var warn bytes.Buffer
			ctx := withEnv(context.Background(), cliEnv{output: "text", sty: style.Plain(), errOut: &warn})
			stamp, err := stampOf(ctx, c, Identity{Username: "alice"}, actor.Where{Namespace: "x"})
			require.NoError(t, err)
			require.NotNil(t, stamp)
			assert.Equal(t, v1alpha1.ActorUnverified, stamp.Kind, "a person is never stamped human without a Ready index")
			assert.Equal(t, "alice", stamp.Principal)
			assert.Empty(t, warn.String())

			stamp, err = stampOf(ctx, c, Identity{Username: "system:serviceaccount:ci:deployer"}, actor.Where{Namespace: "x"})
			require.NoError(t, err)
			assert.Nil(t, stamp, "a ServiceAccount waits for the index")
			assert.Contains(t, warn.String(), "writing without an actor stamp")
		})
	}
	c := fake.NewClientBuilder().WithScheme(rejectScheme(t)).WithObjects(readyAgentIndex()).Build()
	stamp, err := stampOf(context.Background(), c, Identity{Username: "alice"}, actor.Where{Namespace: "x"})
	require.NoError(t, err)
	assert.Equal(t, v1alpha1.ActorHuman, stamp.Kind, "a Ready index: a person is human")
}
