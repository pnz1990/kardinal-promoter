// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestFreezeCommands: freeze writes the scope's window as the caller,
// lists freezes, override --freeze adds the caller's exemption, and
// unfreeze lifts it by name or by scope.
func TestFreezeCommands(t *testing.T) {
	StubIdentity(t, "alice")
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithStatusSubresource(&v1alpha1.ChangeWindow{}).Build()
	now := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

	var out bytes.Buffer
	require.NoError(t, freezeFn(t.Context(), &out, c, v1alpha1.FreezeAgents, "", "INC-4211", 0, now))
	assert.Contains(t, out.String(), "Frozen: kardinal-freeze-agents (agents, every namespace), until kardinal unfreeze kardinal-freeze-agents")
	var cw v1alpha1.ChangeWindow
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "kardinal-freeze-agents"}, &cw))
	assert.Equal(t, "alice", cw.Spec.Freeze.CreatedBy)

	out.Reset()
	require.NoError(t, freezeFn(t.Context(), &out, c, v1alpha1.FreezeEveryone, "payments", "payments incident", 2*time.Hour, now))
	assert.Contains(t, out.String(), "kardinal-freeze-everyone-payments (everyone, namespaces payments), until 2026-10-14T11:00:00Z")

	// The window reconciler marks them active.
	for _, n := range []string{"kardinal-freeze-agents", "kardinal-freeze-everyone-payments"} {
		require.NoError(t, c.Get(ctx, types.NamespacedName{Name: n}, &cw))
		cw.Status.Active, cw.Status.ObservedGeneration = true, cw.Generation
		require.NoError(t, c.Status().Update(ctx, &cw))
	}

	out.Reset()
	require.NoError(t, listFreezesFn(&out, c))
	assert.Contains(t, out.String(), "kardinal-freeze-agents")
	assert.Contains(t, out.String(), "payments incident")

	StubIdentity(t, "bob")
	out.Reset()
	require.NoError(t, overrideFreezeFn(&out, c, "payments", "checkout", "prod", "", "hotfix", 30*time.Minute, now))
	assert.Contains(t, out.String(), "kardinal-freeze-agents, kardinal-freeze-everyone-payments")
	assert.Contains(t, out.String(), "Created by: bob")
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "kardinal-freeze-agents"}, &cw))
	require.Len(t, cw.Spec.Freeze.Exemptions, 1)
	assert.Equal(t, "bob", cw.Spec.Freeze.Exemptions[0].CreatedBy)
	assert.Equal(t, "checkout", cw.Spec.Freeze.Exemptions[0].Pipeline)

	out.Reset()
	require.NoError(t, unfreezeFn(t.Context(), &out, c, "kardinal-freeze-agents"))
	assert.Contains(t, out.String(), "Freeze kardinal-freeze-agents lifted")
	assert.Error(t, unfreezeFn(t.Context(), &out, c, "kardinal-freeze-agents"))

	// #2034: the freeze audit finalizer keeps an unfrozen window for up to
	// two minutes. kardinal freeze does not list it, and override --freeze
	// adds nothing to it (TestGate_FreezeAgentsAndStopTheLine).
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "kardinal-freeze-everyone-payments"}, &cw))
	cw.Finalizers = []string{"kardinal.io/freeze-audit"}
	require.NoError(t, c.Update(ctx, &cw))
	require.NoError(t, freezeFn(t.Context(), &out, c, v1alpha1.FreezeAgents, "", "INC-4212", 0, now))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "kardinal-freeze-agents"}, &cw))
	cw.Status.Active, cw.Status.ObservedGeneration = true, cw.Generation
	require.NoError(t, c.Status().Update(ctx, &cw))
	require.NoError(t, unfreezeFn(t.Context(), &out, c, "kardinal-freeze-everyone-payments"))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "kardinal-freeze-everyone-payments"}, &cw), "kept by its finalizer")
	out.Reset()
	require.NoError(t, listFreezesFn(&out, c))
	assert.NotContains(t, out.String(), "kardinal-freeze-everyone-payments")
	out.Reset()
	require.NoError(t, overrideFreezeFn(&out, c, "payments", "checkout", "prod", "", "hotfix", 30*time.Minute, now))
	assert.Contains(t, out.String(), "Exemption added to kardinal-freeze-agents:")
	assert.NotContains(t, out.String(), "kardinal-freeze-everyone-payments")

	// Freezing that scope again while it is being lifted is refused, not an
	// update that then vanishes (#2034 QA).
	out.Reset()
	err := freezeFn(t.Context(), &out, c, v1alpha1.FreezeEveryone, "payments", "again", 0, now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "freeze kardinal-freeze-everyone-payments is being lifted; try again shortly (up to 2 minutes; the controller must be running)")
}

// TestFreezeFlags: the scope flags name one actor set and one scope.
func TestFreezeFlags(t *testing.T) {
	cases := []struct {
		name       string
		f          freezeFlags
		nsSet      bool
		wantActors string
		wantNS     string
		wantOK     bool
		wantErr    bool
	}{
		{name: "none lists", wantOK: false},
		{name: "agents everywhere", f: freezeFlags{agents: true}, wantActors: "agents", wantOK: true},
		{name: "everyone in a namespace", f: freezeFlags{everyone: true}, nsSet: true, wantActors: "everyone", wantNS: "team", wantOK: true},
		{name: "both actor sets", f: freezeFlags{agents: true, everyone: true}, wantErr: true},
		{name: "cluster and a namespace", f: freezeFlags{agents: true, cluster: true}, nsSet: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &cobra.Command{}
			c.Flags().StringP("namespace", "n", "", "")
			if tc.nsSet {
				require.NoError(t, c.Flags().Set("namespace", "team"))
			}
			actors, ns, ok, err := tc.f.scope(c, "team")
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantActors, actors)
			assert.Equal(t, tc.wantNS, ns)
		})
	}
}
