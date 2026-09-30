// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestAutoRollbackName_FitsBundleLabel: automatic rollback Bundle names are
// deterministic and valid kardinal.io/bundle label values, however long the
// failing Bundle's name is.
func TestAutoRollbackName_FitsBundleLabel(t *testing.T) {
	long := strings.Repeat("a", 60)
	tests := []struct {
		name   string
		bundle string
		want   string
	}{
		{name: "short name is readable", bundle: "app-v2", want: "app-v2-rollback-alarm"},
		{name: "long name is shortened", bundle: long},
		{name: "long name ending in a dash is shortened", bundle: strings.Repeat("a", 40) + "-" + strings.Repeat("b", 30)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := lifecycle.AutoRollbackName(tc.bundle, "alarm")
			if tc.want != "" {
				assert.Equal(t, tc.want, got)
			}
			assert.LessOrEqual(t, len(got), 63)
			assert.Empty(t, validation.IsValidLabelValue(got))
			assert.Empty(t, validation.IsDNS1123Subdomain(got))
			assert.Equal(t, got, lifecycle.AutoRollbackName(tc.bundle, "alarm"), "deterministic")
			assert.NotEqual(t, got, lifecycle.AutoRollbackName(tc.bundle, "policy"), "distinct per source")
		})
	}
	assert.NotEqual(t, lifecycle.AutoRollbackName(long+"x", "alarm"), lifecycle.AutoRollbackName(long+"y", "alarm"),
		"shortened names stay distinct")
}

// TestFindRollback: FindRollback matches on the rolled-back Bundle and the environment.
func TestFindRollback(t *testing.T) {
	rb := func(name, from, env string) client.Object {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "default",
				Labels:      map[string]string{lifecycle.LabelRollback: "true", lifecycle.LabelPipeline: "app"},
				Annotations: map[string]string{lifecycle.AnnotationRollbackFrom: from},
			},
			Spec: v1alpha1.BundleSpec{Pipeline: "app", Intent: &v1alpha1.BundleIntent{TargetEnvironment: env}},
		}
	}
	c := newClient(t, rb("rb-uat", "app-v2", "uat"), rb("rb-other", "app-v1", "prod"))
	ctx := context.Background()

	got, err := lifecycle.FindRollback(ctx, c, "default", "app", "uat", "app-v2")
	require.NoError(t, err)
	assert.Equal(t, "rb-uat", got)

	got, err = lifecycle.FindRollback(ctx, c, "default", "app", "prod", "app-v2")
	require.NoError(t, err)
	assert.Empty(t, got, "a rollback in another environment does not count")
}
