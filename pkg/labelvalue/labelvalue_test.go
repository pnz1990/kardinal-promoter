// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package labelvalue_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/labelvalue"
)

// TestFit: a valid name is unchanged; a longer one becomes a valid label
// value that keeps its start, is stable, and differs for different names.
func TestFit(t *testing.T) {
	assert.Equal(t, "my-app", labelvalue.Fit("my-app"))
	sixtyThree := strings.Repeat("a", 63)
	assert.Equal(t, sixtyThree, labelvalue.Fit(sixtyThree), "63 characters still fit")
	long := "kardinal-freeze-agents-e2e-testgate-freezeagentsandstoptheline-cb39f208"
	got := labelvalue.Fit(long)
	assert.Empty(t, validation.IsValidLabelValue(got), got)
	assert.LessOrEqual(t, len(got), validation.LabelValueMaxLength)
	assert.True(t, strings.HasPrefix(got, long[:52]), got)
	assert.Equal(t, got, labelvalue.Fit(long), "stable")
	assert.NotEqual(t, got, labelvalue.Fit(long+"x"), "a different name, a different value")
	// A cut that would end in punctuation is trimmed, so the value still
	// ends in an alphanumeric before the hash.
	dashed := strings.Repeat("a", 51) + "--" + strings.Repeat("b", 20)
	assert.Empty(t, validation.IsValidLabelValue(labelvalue.Fit(dashed)))
	for _, n := range []string{strings.Repeat("x", 253), "a.b_c-" + strings.Repeat("z", 100)} {
		assert.Empty(t, validation.IsValidLabelValue(labelvalue.Fit(n)), n)
	}
}

// TestFit_Pinned: the exact output for long names. Bundles, MetricChecks,
// AnalysisRuns and AuditEvents already carry these values, and the
// Subscription finds its Bundles by them: a change of format would make a
// new controller miss them (duplicate Bundles after an upgrade). The hashes
// are sha256sum's of the names.
func TestFit_Pinned(t *testing.T) {
	assert.Equal(t, "kardinal-freeze-agents-e2e-testgate-freezeagentsands-40b30b602c",
		labelvalue.Fit("kardinal-freeze-agents-e2e-testgate-freezeagentsandstoptheline-cb39f208"))
	assert.Equal(t, "checkout-service-images-from-the-ghcr-registry-of-th-faf055a9fc",
		labelvalue.Fit("checkout-service-images-from-the-ghcr-registry-of-the-payments-team-v2"))
}
