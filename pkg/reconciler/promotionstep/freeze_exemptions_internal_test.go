// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
)

// TestRecordedExemptions (QA-3 #2115): status.freezeExemptions records each
// freeze and exemption once (the same pair again adds nothing), at most
// MaxStepFreezeExemptions of them (the CRD's MaxItems: a ninth would make
// every status patch fail), until the record's capped end when the
// controller capped it, else the exemption's expiresAt.
func TestRecordedExemptions(t *testing.T) {
	expires := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	capped := time.Date(2026, 10, 14, 11, 0, 0, 0, time.UTC)
	window := func(name string) *v1alpha1.ChangeWindow {
		return &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.ChangeWindowSpec{Freeze: &v1alpha1.ChangeWindowFreeze{Actors: v1alpha1.FreezeEveryone}}}
	}
	ex := func(reason string) *v1alpha1.FreezeExemption {
		return &v1alpha1.FreezeExemption{Namespace: "default", Pipeline: "p", Reason: reason, CreatedBy: "carol",
			ExpiresAt: metav1.NewTime(expires)}
	}
	match := func(w, reason string, until time.Time) changewindow.FreezeMatch {
		return changewindow.FreezeMatch{Window: window(w), Exemption: ex(reason), Until: until}
	}

	t.Run("the same freeze and exemption twice is one entry", func(t *testing.T) {
		out := recordedExemptions(nil, []changewindow.FreezeMatch{match("f", "hotfix", time.Time{}), match("f", "hotfix", time.Time{})})
		require.Len(t, out, 1)
		out = recordedExemptions(out, []changewindow.FreezeMatch{match("f", "hotfix", time.Time{})})
		assert.Len(t, out, 1, "a later reconcile adds nothing")
		out = recordedExemptions(out, []changewindow.FreezeMatch{match("f", "other reason", time.Time{}), match("g", "hotfix", time.Time{})})
		assert.Len(t, out, 3, "another exemption, another freeze")
	})
	t.Run("at most MaxStepFreezeExemptions", func(t *testing.T) {
		var ms []changewindow.FreezeMatch
		for i := 0; i < MaxStepFreezeExemptions+1; i++ {
			ms = append(ms, match(fmt.Sprintf("f%d", i), "hotfix", time.Time{}))
		}
		out := recordedExemptions(nil, ms)
		assert.Len(t, out, MaxStepFreezeExemptions)
		assert.Equal(t, 8, MaxStepFreezeExemptions, "the CRD's MaxItems")
	})
	t.Run("the controller's cap is recorded", func(t *testing.T) {
		out := recordedExemptions(nil, []changewindow.FreezeMatch{match("f", "hotfix", capped)})
		require.Len(t, out, 1)
		assert.Equal(t, capped, out[0].Until.UTC(), "capped earlier than expiresAt")
	})
	t.Run("no cap: the exemption's expiresAt", func(t *testing.T) {
		out := recordedExemptions(nil, []changewindow.FreezeMatch{match("f", "hotfix", time.Time{})})
		require.Len(t, out, 1)
		assert.Equal(t, expires, out[0].Until.UTC())
	})
	t.Run("a freeze that holds records nothing", func(t *testing.T) {
		out := recordedExemptions(nil, []changewindow.FreezeMatch{{Window: window("f")}})
		assert.Empty(t, out)
	})
}
