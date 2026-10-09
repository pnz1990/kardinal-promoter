// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestImageVerificationHoldsRootStep: a step that waits for the Bundle's
// ImageVerification stays Pending until the mirror shows it Verified, fails
// when it Failed, and then waits for its pre hooks as usual.
func TestImageVerificationHoldsRootStep(t *testing.T) {
	cases := []struct {
		name      string
		live      *v1alpha1.LiveImageVerification
		preHooks  bool
		wantState string
		wantMsg   string
	}{
		{"no mirror yet", nil, false, "", "waiting for image verification app-v1-verify"},
		{"pending with message", &v1alpha1.LiveImageVerification{Phase: "Pending", Message: "no signature found yet"}, false, "",
			"waiting for image verification app-v1-verify: no signature found yet"},
		{"failed", &v1alpha1.LiveImageVerification{Phase: "Failed", Message: "bad signature"}, false, "Failed",
			"image verification app-v1-verify failed: bad signature"},
		{"verified", &v1alpha1.LiveImageVerification{Phase: "Verified"}, false, "Promoting", "initialized"},
		{"verified, pre hook next", &v1alpha1.LiveImageVerification{Phase: "Verified"}, true, "", "waiting for pre-deploy hook migrate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := labelled(makeStep("step", "p", "b1", "test"))
			ps.Spec.ImageVerification = "app-v1-verify"
			ps.Spec.Live = &v1alpha1.PromotionStepLive{ImageVerification: tc.live}
			if tc.preHooks {
				ps.Spec.PreHooks = []string{"migrate"}
			}
			c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
			got, _ := reconcileHookStep(t, c, "step")
			assert.Equal(t, tc.wantState, got.Status.State)
			assert.Contains(t, got.Status.Message, tc.wantMsg)
		})
	}
}
