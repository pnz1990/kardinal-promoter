// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestValidateCommitTime (#1942): provenance.commitTime, where the lead time
// metrics start, may be unset or in the past (a few minutes ahead for clock
// skew); a zero time or one in the future is refused when the version is
// created through the Bundle API or the CLI.
func TestValidateCommitTime(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		t       *metav1.Time
		wantErr string
	}{
		{"unset", nil, ""},
		{"in the past", &metav1.Time{Time: now.Add(-48 * time.Hour)}, ""},
		{"within the clock skew", &metav1.Time{Time: now.Add(4 * time.Minute)}, ""},
		{"zero", &metav1.Time{}, "zero time"},
		{"in the future", &metav1.Time{Time: now.Add(time.Hour)}, "in the future"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCommitTime(tc.t, now)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
	future := metav1.NewTime(time.Now().Add(24 * time.Hour))
	err := ValidateNewBundle(&v1alpha1.BundleSpec{Pipeline: "app", Images: []v1alpha1.ImageRef{{Repository: "r", Tag: "t"}},
		Provenance: &v1alpha1.BundleProvenance{CommitTime: &future}})
	require.Error(t, err, "ValidateNewBundle refuses a future commit time")
	assert.Contains(t, err.Error(), "provenance.commitTime")
}

// TestLeadTimeStart: the commit time when set and not after the Bundle's
// creation; else the creation. A rollback starts at its creation.
func TestLeadTimeStart(t *testing.T) {
	created := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	commit := metav1.NewTime(created.Add(-10 * time.Minute))
	late := metav1.NewTime(created.Add(time.Minute))
	tests := []struct {
		name       string
		prov       *v1alpha1.BundleProvenance
		want       time.Time
		fromCommit bool
	}{
		{"no provenance", nil, created, false},
		{"commit time", &v1alpha1.BundleProvenance{CommitTime: &commit}, commit.Time, true},
		{"zero commit time", &v1alpha1.BundleProvenance{CommitTime: &metav1.Time{}}, created, false},
		{"after the Bundle's creation", &v1alpha1.BundleProvenance{CommitTime: &late}, created, false},
		{"a rollback", &v1alpha1.BundleProvenance{CommitTime: &commit, RollbackOf: "app-v1"}, created, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created)},
				Spec: v1alpha1.BundleSpec{Provenance: tc.prov}}
			got, fromCommit := LeadTimeStart(b)
			assert.True(t, tc.want.Equal(got), "start %s, want %s", got, tc.want)
			assert.Equal(t, tc.fromCommit, fromCommit)
		})
	}
}
