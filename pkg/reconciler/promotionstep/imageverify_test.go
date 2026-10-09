// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"strings"
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

// TestImageVerificationImagesMustMatch: a Verified result is used only when
// it is the step's ImageVerification and the Bundle's images are the ones
// it verified (repositories normalized); otherwise the step waits or fails
// before any git write (regression, QA #1521).
func TestImageVerificationImagesMustMatch(t *testing.T) {
	d1 := "sha256:" + strings.Repeat("1", 64)
	d2 := "sha256:" + strings.Repeat("2", 64)
	cases := []struct {
		name      string
		images    []v1alpha1.ImageRef
		live      v1alpha1.LiveImageVerification
		wantState string
		wantMsg   string
	}{
		{"same image, other spelling", []v1alpha1.ImageRef{{Repository: "GHCR.io:443/org/app", Tag: "1", Digest: d1}},
			v1alpha1.LiveImageVerification{Name: "app-v1-verify", Phase: "Verified", Images: []string{"ghcr.io/org/app@" + d1}}, "Promoting", "initialized"},
		{"other digest", []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Digest: d2}},
			v1alpha1.LiveImageVerification{Name: "app-v1-verify", Phase: "Verified", Images: []string{"ghcr.io/org/app@" + d1}}, "Failed",
			"refusing to promote: the Bundle's image ghcr.io/org/app has digest"},
		{"verified image missing", []v1alpha1.ImageRef{{Repository: "ghcr.io/org/other", Digest: d1}},
			v1alpha1.LiveImageVerification{Name: "app-v1-verify", Phase: "Verified", Images: []string{"ghcr.io/org/app@" + d1}}, "Failed",
			"the verified image ghcr.io/org/app@" + d1 + " is not in the Bundle"},
		{"result of an older ImageVerification", []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Digest: d1}},
			v1alpha1.LiveImageVerification{Name: "app-v1-verify-old", Phase: "Verified", Images: []string{"ghcr.io/org/app@" + d1}}, "",
			"waiting for image verification app-v1-verify: the result shown is for app-v1-verify-old"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := labelled(makeStep("step", "p", "b1", "test"))
			ps.Spec.ImageVerification = "app-v1-verify"
			live := tc.live
			ps.Spec.Live = &v1alpha1.PromotionStepLive{ImageVerification: &live}
			b := makeBundle("b1", "p")
			b.Spec.Images = tc.images
			c := newClient(t, ps, makePipeline("p"), b)
			got, _ := reconcileHookStep(t, c, "step")
			assert.Equal(t, tc.wantState, got.Status.State)
			assert.Contains(t, got.Status.Message, tc.wantMsg)
		})
	}
}
