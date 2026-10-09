// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestCompactUnsupportedFeature checks that a compact Graph is refused for a
// Pipeline using a feature the compact shape does not carry (a check in
// compactUnsupported), with ErrInvalid and the feature's name, while the node
// shape still builds.
func TestCompactUnsupportedFeature(t *testing.T) {
	saved := compactUnsupported
	t.Cleanup(func() { compactUnsupported = saved })
	compactUnsupported = append(compactUnsupported, func(in BuildInput) string {
		if in.Pipeline.Annotations["feature"] == "on" {
			return "pre-deploy hooks"
		}
		return ""
	})
	pipeline := func(shape string) *kardinalv1alpha1.Pipeline {
		return &kardinalv1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default",
				Annotations: map[string]string{"feature": "on", AnnotationGraphShape: shape}},
			Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}},
		}
	}
	bundle := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: "default"},
		Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app",
			Images: []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v1"}}}}

	_, err := NewBuilder().Build(BuildInput{Pipeline: pipeline(GraphShapeCompact), Bundle: bundle})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalid))
	assert.Contains(t, err.Error(), "the compact shape does not support pre-deploy hooks yet")

	_, err = NewBuilder().Build(BuildInput{Pipeline: pipeline(GraphShapeNodes), Bundle: bundle})
	assert.NoError(t, err, "the node shape carries the feature")

	// A Bundle whose Graph is already compact is told only new Bundles can
	// switch.
	_, err = NewBuilder().Build(BuildInput{Pipeline: pipeline(GraphShapeNodes), Bundle: bundle, Shape: GraphShapeCompact})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalid))
	assert.Contains(t, err.Error(), "a Bundle keeps the shape its Graph was created with")
	assert.Contains(t, err.Error(), "only new Bundles can switch")

	assert.Equal(t, []string{"pre-deploy hooks"}, CompactUnsupported(BuildInput{Pipeline: pipeline("")}))
	assert.True(t, NewBuilder().WouldBeCompact(pipeline(GraphShapeCompact), 2))
	assert.False(t, NewBuilder().WouldBeCompact(pipeline(GraphShapeNodes), 200))
	assert.True(t, NewBuilder().WouldBeCompact(pipeline(""), 101))
	assert.False(t, NewBuilder().WouldBeCompact(pipeline("flat"), 101), "an invalid annotation is reported on its own")
}
