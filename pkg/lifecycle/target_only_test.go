// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// The upstream of a fleet target is the path to the fleet, not its other
// targets; the upstream of the fleet is the same, without any target.
func TestUpstreamOfTarget(t *testing.T) {
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{
			{Name: "test"}, {Name: "uat", DependsOn: []string{"test"}},
			{Name: "prod", DependsOn: []string{"uat"}, Fleet: &v1alpha1.FleetSpec{MaxConcurrent: 1,
				Targets: []v1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}, {Name: "ap"}}}},
			{Name: "post", DependsOn: []string{"prod"}},
		}}}
	for target, want := range map[string][]string{"prod-ap": {"test", "uat"}, "prod": {"test", "uat"}, "post": {"test", "uat", "prod-eu", "prod-us", "prod-ap"}, "test": {}} {
		b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "rb"}, Spec: v1alpha1.BundleSpec{Pipeline: "app", Type: "image",
			Images: []v1alpha1.ImageRef{{Repository: "r", Tag: "1"}},
			Intent: &v1alpha1.BundleIntent{TargetEnvironment: target, TargetOnly: true}}}
		got, err := lifecycle.UpstreamOfTarget(p, b)
		require.NoError(t, err, target)
		assert.ElementsMatch(t, want, got, target)
	}
}

func TestUnpinnedArtifacts(t *testing.T) {
	b := &v1alpha1.Bundle{Spec: v1alpha1.BundleSpec{
		Images: []v1alpha1.ImageRef{{Repository: "r", Tag: "1"}, {Repository: "s", Tag: "2", Digest: "sha256:x"}},
		Chart:  &v1alpha1.ChartRef{Name: "c", Version: "1.0.0"}}}
	assert.Equal(t, []string{"r:1", "chart c 1.0.0"}, lifecycle.UnpinnedArtifacts(b))
	b.Spec.Chart.Digest = "sha256:y"
	b.Spec.Images = b.Spec.Images[1:]
	assert.Empty(t, lifecycle.UnpinnedArtifacts(b))
}
