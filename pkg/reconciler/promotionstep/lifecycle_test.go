// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// TestPause_FreezeGateHoldsStep covers C03-promotionstep-10, C02-bundle-08,
// C09b-cli-01 and E2E-02: while the pipeline's freeze gate exists, a Pending
// step does not start and a Promoting step does not run its next git step;
// a merged change keeps going; a user gate that merely shares the name does
// not pause; and resume lets the held step continue.
func TestPause_FreezeGateHoldsStep(t *testing.T) {
	freeze := func() *v1alpha1.PolicyGate {
		p := makePipeline("nginx-demo")
		p.UID = "uid-nginx-demo"
		return lifecycle.DesiredFreezeGate(p)
	}
	userGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: lifecycle.FreezeGateName("nginx-demo"), Namespace: "default"},
		Spec:       v1alpha1.PolicyGateSpec{Expression: "true"},
	}
	tests := []struct {
		name      string
		state     string
		gate      *v1alpha1.PolicyGate
		wantState string
		wantHeld  bool
	}{
		{name: "pending step does not start", state: "", gate: freeze(), wantState: "", wantHeld: true},
		{name: "explicit pending step does not start", state: "Pending", gate: freeze(), wantState: "Pending", wantHeld: true},
		{name: "promoting step does not run its next git step", state: "Promoting", gate: freeze(), wantState: "Promoting", wantHeld: true},
		{name: "a step waiting for its merge carries on", state: "WaitingForMerge", gate: freeze(), wantState: "WaitingForMerge"},
		{name: "a user gate with the same name does not pause", state: "", gate: userGate, wantState: "Promoting"},
		{name: "no gate: the step starts", state: "", wantState: "Promoting"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			step := makeStep("step-test", "nginx-demo", "bundle-1", "test")
			step.Status.State = tc.state
			objs := []client.Object{step, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo")}
			if tc.state == "WaitingForMerge" {
				// A step waiting for its merge always has a PRStatus (C03-promotionstep-30).
				step.Spec.PRStatusRef = "prs"
				objs = append(objs, openPRStatus("prs", "org/repo", 42))
			}
			if tc.gate != nil {
				objs = append(objs, tc.gate.DeepCopy())
			}
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
				WithObjects(objs...).Build()
			r := &promotionstep.Reconciler{
				Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() },
			}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "step-test", Namespace: "default"}}
			key := req.NamespacedName

			res, err := r.Reconcile(ctx, req)
			require.NoError(t, err)
			var got v1alpha1.PromotionStep
			require.NoError(t, c.Get(ctx, key, &got))
			assert.Equal(t, tc.wantState, got.Status.State)
			if !tc.wantHeld {
				assert.NotEqual(t, lifecycle.PausedMessage("nginx-demo"), got.Status.Message)
				return
			}
			assert.Equal(t, lifecycle.PausedMessage("nginx-demo"), got.Status.Message)
			assert.Positive(t, res.RequeueAfter, "a held step is requeued as a fallback to the PolicyGate watch")
			assert.Empty(t, got.Status.Outputs, "no git step ran")

			// Idempotent: a second reconcile holds again without changes.
			rv := got.ResourceVersion
			_, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
			require.NoError(t, c.Get(ctx, key, &got))
			assert.Equal(t, rv, got.ResourceVersion, "a held step is not rewritten")

			// Resume deletes the gate; the step continues.
			require.NoError(t, lifecycle.RemoveFreezeGate(ctx, c, "default", "nginx-demo"))
			_, err = r.Reconcile(ctx, req)
			require.NoError(t, err)
			require.NoError(t, c.Get(ctx, key, &got))
			assert.NotEqual(t, tc.wantState, got.Status.State, "after resume the step advances")
		})
	}
}
