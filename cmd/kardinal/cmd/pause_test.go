// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPause_PatchesPipelinePaused verifies that pauseFn patches Pipeline.spec.paused=true
// and creates a freeze PolicyGate (Graph-observable pause enforcement, PS-2 / BU-2 fix).
func TestPause_PatchesPipelinePaused(t *testing.T) {
	s := cliTestScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git:          v1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline).Build()

	var buf bytes.Buffer
	err := pauseFn(&buf, c, "default", "nginx-demo")
	require.NoError(t, err)

	var updated v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo", Namespace: "default"}, &updated))
	assert.True(t, updated.Spec.Paused)
	assert.Contains(t, buf.String(), "paused")

	// Freeze gate must have been created.
	var gate v1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "freeze-nginx-demo", Namespace: "default"}, &gate))
	assert.Equal(t, "false", gate.Spec.Expression, "freeze gate must always evaluate to false")
	assert.Equal(t, "true", gate.Labels["kardinal.io/freeze"])
}

// TestPause_Idempotent verifies that pauseFn is safe to call multiple times
// (AlreadyExists on the freeze gate is silently swallowed).
func TestPause_Idempotent(t *testing.T) {
	s := cliTestScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git:          v1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline).Build()

	var buf bytes.Buffer
	require.NoError(t, pauseFn(&buf, c, "default", "nginx-demo"))
	// Second call must succeed without error (gate already exists).
	require.NoError(t, pauseFn(&buf, c, "default", "nginx-demo"), "second pauseFn must be idempotent")
}

// TestResume_UnpausesPipeline verifies that resumeFn patches Pipeline.spec.paused=false
// and deletes the freeze PolicyGate (PS-2 / BU-2 fix).
func TestResume_UnpausesPipeline(t *testing.T) {
	s := cliTestScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git:          v1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}},
			Paused:       true,
		},
	}
	freezeGate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name: "freeze-nginx-demo", Namespace: "default",
			Labels: map[string]string{"kardinal.io/freeze": "true"},
		},
		Spec: v1alpha1.PolicyGateSpec{Expression: "false"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline, freezeGate).Build()

	var buf bytes.Buffer
	err := resumeFn(&buf, c, "default", "nginx-demo")
	require.NoError(t, err)

	var updated v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo", Namespace: "default"}, &updated))
	assert.False(t, updated.Spec.Paused)
	assert.Contains(t, buf.String(), "resumed")

	// Freeze gate must have been deleted.
	var gate v1alpha1.PolicyGate
	err = c.Get(context.Background(), types.NamespacedName{Name: "freeze-nginx-demo", Namespace: "default"}, &gate)
	assert.True(t, apierrors.IsNotFound(err), "freeze gate must be deleted on resume")
}

// TestResume_Idempotent verifies that resumeFn is safe when the freeze gate is absent
// (e.g. manual deletion or first resume after an older-format pause).
func TestResume_Idempotent(t *testing.T) {
	s := cliTestScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git:          v1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}},
			Paused:       true,
		},
	}
	// No freeze gate in the cluster — should still succeed.
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline).Build()

	var buf bytes.Buffer
	require.NoError(t, resumeFn(&buf, c, "default", "nginx-demo"), "resumeFn must succeed even if freeze gate is absent")
}

// TestPauseResume_FreezeGateCreatedAndDeleted verifies the full pause/resume lifecycle
// from the CLI perspective (issue #406):
// 1. pauseFn creates a freeze-<pipeline> PolicyGate with expression "false"
// 2. resumeFn deletes the freeze gate
// 3. The pipeline's Spec.Paused is set/unset correctly
func TestPauseResume_FreezeGateCreatedAndDeleted(t *testing.T) {
	s := cliTestScheme(t)
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "nginx-demo", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git:          v1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "prod"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pipeline).Build()

	// Step 1: pause
	var pauseBuf bytes.Buffer
	require.NoError(t, pauseFn(&pauseBuf, c, "default", "nginx-demo"),
		"pause must succeed")
	assert.Contains(t, pauseBuf.String(), "paused")

	// Verify Pipeline.Spec.Paused = true
	var paused v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo", Namespace: "default"}, &paused))
	assert.True(t, paused.Spec.Paused, "Pipeline.Spec.Paused must be true after pause")

	// Verify freeze gate exists with expression "false"
	var freezeGate v1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Name: "freeze-nginx-demo", Namespace: "default"}, &freezeGate),
		"freeze gate must be created by pauseFn")
	assert.Equal(t, "false", freezeGate.Spec.Expression,
		"freeze gate expression must be 'false' to block all promotions")
	assert.Equal(t, "true", freezeGate.Labels["kardinal.io/freeze"],
		"freeze gate must have kardinal.io/freeze=true label")

	// Step 2: resume
	var resumeBuf bytes.Buffer
	require.NoError(t, resumeFn(&resumeBuf, c, "default", "nginx-demo"),
		"resume must succeed")
	assert.Contains(t, resumeBuf.String(), "resumed")

	// Verify Pipeline.Spec.Paused = false
	var resumed v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "nginx-demo", Namespace: "default"}, &resumed))
	assert.False(t, resumed.Spec.Paused, "Pipeline.Spec.Paused must be false after resume")

	// Verify freeze gate is deleted
	var deletedGate v1alpha1.PolicyGate
	err := c.Get(context.Background(),
		types.NamespacedName{Name: "freeze-nginx-demo", Namespace: "default"}, &deletedGate)
	assert.True(t, apierrors.IsNotFound(err),
		"freeze gate must be deleted by resumeFn; got: %v", err)
}

// TestPause_PipelineNotFound verifies pauseFn returns an error when the pipeline does not exist.
func TestPause_PipelineNotFound(t *testing.T) {
	s := cliTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := pauseFn(&buf, c, "default", "nonexistent-pipeline")
	assert.Error(t, err, "pause must fail when pipeline does not exist")
	assert.Contains(t, err.Error(), "nonexistent-pipeline")
}

// TestResume_PipelineNotFound verifies resumeFn returns an error when the pipeline does not exist.
func TestResume_PipelineNotFound(t *testing.T) {
	s := cliTestScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	var buf bytes.Buffer
	err := resumeFn(&buf, c, "default", "nonexistent-pipeline")
	assert.Error(t, err, "resume must fail when pipeline does not exist")
	assert.Contains(t, err.Error(), "nonexistent-pipeline")
}
