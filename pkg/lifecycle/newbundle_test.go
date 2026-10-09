// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestValidateNewBundle pins the rules the Bundle API and kardinal create
// bundle share (#1285).
func TestValidateNewBundle(t *testing.T) {
	images := []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v1"}}
	commit := &v1alpha1.ConfigRef{CommitSHA: "abc123"}
	tests := []struct {
		name     string
		spec     v1alpha1.BundleSpec
		wantErr  string
		wantType string
	}{
		{name: "no pipeline", spec: v1alpha1.BundleSpec{Images: images}, wantErr: "pipeline is required"},
		{name: "pipeline not an object name", spec: v1alpha1.BundleSpec{Pipeline: "My_App", Images: images},
			wantErr: "pipeline must be a valid Kubernetes object name"},
		{name: "unknown type", spec: v1alpha1.BundleSpec{Pipeline: "app", Type: "helm", Images: images},
			wantErr: `type must be one of image, config, mixed, chart (got "helm")`},
		{name: "empty type defaults to image", spec: v1alpha1.BundleSpec{Pipeline: "app", Images: images}, wantType: "image"},
		{name: "image without images", spec: v1alpha1.BundleSpec{Pipeline: "app", Type: "image"},
			wantErr: `type "image" requires at least one entry in images`},
		{name: "config without a commit", spec: v1alpha1.BundleSpec{Pipeline: "app", Type: "config"},
			wantErr: `type "config" requires configRef.commitSHA`},
		{name: "mixed without a commit", spec: v1alpha1.BundleSpec{Pipeline: "app", Type: "mixed", Images: images},
			wantErr: `type "mixed" requires configRef.commitSHA`},
		{name: "config", spec: v1alpha1.BundleSpec{Pipeline: "app", Type: "config", ConfigRef: commit}, wantType: "config"},
		{name: "bad ciRunURL",
			spec: v1alpha1.BundleSpec{Pipeline: "app", Images: images,
				Provenance: &v1alpha1.BundleProvenance{CIRunURL: "javascript:alert(1)"}},
			wantErr: "provenance.ciRunURL must be an absolute http or https URL"},
		{name: "https ciRunURL",
			spec: v1alpha1.BundleSpec{Pipeline: "app", Images: images,
				Provenance: &v1alpha1.BundleProvenance{CIRunURL: "https://github.com/o/r/actions/runs/1"}},
			wantType: "image"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.spec
			err := lifecycle.ValidateNewBundle(&spec)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantType, spec.Type)
		})
	}
}
