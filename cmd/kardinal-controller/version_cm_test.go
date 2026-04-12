// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func buildVersionTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = kardinalv1alpha1.AddToScheme(s)
	return s
}

// TestEnsureVersionConfigMap_Creates verifies that the ConfigMap is created when
// it does not exist.
func TestEnsureVersionConfigMap_Creates(t *testing.T) {
	s := buildVersionTestScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	log := zerolog.Nop()

	ensureVersionConfigMap(context.Background(), c, "kardinal-system", "v0.5.0", log)

	var cm corev1.ConfigMap
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "kardinal-version", Namespace: "kardinal-system"}, &cm))
	assert.Equal(t, "v0.5.0", cm.Data["version"])
}

// TestEnsureVersionConfigMap_Idempotent verifies that calling ensureVersionConfigMap
// twice with the same version does not error.
func TestEnsureVersionConfigMap_Idempotent(t *testing.T) {
	s := buildVersionTestScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	log := zerolog.Nop()

	ensureVersionConfigMap(context.Background(), c, "kardinal-system", "v0.5.0", log)
	ensureVersionConfigMap(context.Background(), c, "kardinal-system", "v0.5.0", log)

	var cm corev1.ConfigMap
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "kardinal-version", Namespace: "kardinal-system"}, &cm))
	assert.Equal(t, "v0.5.0", cm.Data["version"])
}

// TestEnsureVersionConfigMap_Updates verifies that the ConfigMap version is updated
// when the controller version changes (e.g. after an upgrade).
func TestEnsureVersionConfigMap_Updates(t *testing.T) {
	s := buildVersionTestScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	log := zerolog.Nop()

	// First run: create with v0.5.0
	ensureVersionConfigMap(context.Background(), c, "kardinal-system", "v0.5.0", log)

	// Simulate upgrade: call again with v0.6.0
	ensureVersionConfigMap(context.Background(), c, "kardinal-system", "v0.6.0", log)

	var cm corev1.ConfigMap
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "kardinal-version", Namespace: "kardinal-system"}, &cm))
	assert.Equal(t, "v0.6.0", cm.Data["version"],
		"version must be updated when controller version changes")
}
