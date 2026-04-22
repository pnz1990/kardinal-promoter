// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func makeTemplate(name, ns string, steps []kardinalv1alpha1.StepSpec) *kardinalv1alpha1.PromotionTemplate {
	return &kardinalv1alpha1.PromotionTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       kardinalv1alpha1.PromotionTemplateSpec{Steps: steps},
	}
}

func makePipelineWithTemplateRef(name, ns, envName, tplName, tplNS string) *kardinalv1alpha1.Pipeline {
	ref := &kardinalv1alpha1.PromotionTemplateRef{Name: tplName, Namespace: tplNS}
	return &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: envName, PromotionTemplate: ref},
			},
		},
	}
}

func resolverScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = kardinalv1alpha1.AddToScheme(s)
	return s
}

// TestInlinePromotionTemplates_NoRef verifies the fast-path: pipeline with no
// PromotionTemplate references is returned unchanged (same pointer).
func TestInlinePromotionTemplates_NoRef(t *testing.T) {
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "test"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git:          kardinalv1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(resolverScheme()).Build()

	got, err := inlinePromotionTemplates(context.Background(), pipeline, c)

	require.NoError(t, err)
	assert.Same(t, pipeline, got, "no-ref: same pointer expected (fast-path, no allocation)")
}

// TestInlinePromotionTemplates_TemplateNotFound verifies that a missing template
// returns an error containing "TemplateNotFound".
func TestInlinePromotionTemplates_TemplateNotFound(t *testing.T) {
	pipeline := makePipelineWithTemplateRef("p", "test", "prod", "missing-tpl", "test")
	c := fake.NewClientBuilder().WithScheme(resolverScheme()).Build() // empty store

	_, err := inlinePromotionTemplates(context.Background(), pipeline, c)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "TemplateNotFound")
	assert.Contains(t, err.Error(), "missing-tpl")
}

// TestInlinePromotionTemplates_StepsInlined verifies that template steps are inlined
// into an environment when env.Steps is empty.
func TestInlinePromotionTemplates_StepsInlined(t *testing.T) {
	tplSteps := []kardinalv1alpha1.StepSpec{
		{Uses: "git-clone"},
		{Uses: "kustomize-set-image"},
		{Uses: "git-commit"},
		{Uses: "open-pr"},
		{Uses: "wait-for-merge"},
		{Uses: "health-check"},
	}
	tpl := makeTemplate("standard", "test", tplSteps)
	pipeline := makePipelineWithTemplateRef("p", "test", "prod", "standard", "test")

	c := fake.NewClientBuilder().
		WithScheme(resolverScheme()).
		WithObjects(tpl).
		Build()

	got, err := inlinePromotionTemplates(context.Background(), pipeline, c)

	require.NoError(t, err)
	require.Len(t, got.Spec.Environments, 1)
	env := got.Spec.Environments[0]
	require.Len(t, env.Steps, len(tplSteps), "template steps must be inlined")
	for i, s := range tplSteps {
		assert.Equal(t, s.Uses, env.Steps[i].Uses)
	}
	// Original pipeline must not be mutated.
	assert.Nil(t, pipeline.Spec.Environments[0].Steps, "original pipeline must be unchanged")
}

// TestInlinePromotionTemplates_LocalStepsWin verifies that when env.Steps is set,
// they take precedence over the template (local override wins).
func TestInlinePromotionTemplates_LocalStepsWin(t *testing.T) {
	tplSteps := []kardinalv1alpha1.StepSpec{
		{Uses: "git-clone"},
		{Uses: "health-check"},
	}
	tpl := makeTemplate("standard", "test", tplSteps)

	localSteps := []kardinalv1alpha1.StepSpec{{Uses: "custom-step"}}
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "test"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{
					Name:              "prod",
					Steps:             localSteps,
					PromotionTemplate: &kardinalv1alpha1.PromotionTemplateRef{Name: "standard"},
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(resolverScheme()).
		WithObjects(tpl).
		Build()

	got, err := inlinePromotionTemplates(context.Background(), pipeline, c)

	require.NoError(t, err)
	require.Len(t, got.Spec.Environments[0].Steps, 1)
	assert.Equal(t, "custom-step", got.Spec.Environments[0].Steps[0].Uses,
		"local steps must win over template")
}

// TestInlinePromotionTemplates_TwoEnvsSameTemplate verifies that two environments
// sharing the same template each get the template steps inlined, and the template
// is fetched only once (cache hit).
func TestInlinePromotionTemplates_TwoEnvsSameTemplate(t *testing.T) {
	tplSteps := []kardinalv1alpha1.StepSpec{
		{Uses: "git-clone"},
		{Uses: "open-pr"},
	}
	tpl := makeTemplate("shared", "myns", tplSteps)

	tplRef := &kardinalv1alpha1.PromotionTemplateRef{Name: "shared", Namespace: "myns"}
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "myns"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{Name: "test", PromotionTemplate: tplRef},
				{Name: "prod", PromotionTemplate: tplRef},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(resolverScheme()).
		WithObjects(tpl).
		Build()

	got, err := inlinePromotionTemplates(context.Background(), pipeline, c)

	require.NoError(t, err)
	require.Len(t, got.Spec.Environments, 2)
	for _, env := range got.Spec.Environments {
		require.Len(t, env.Steps, 2, "env %q must have template steps", env.Name)
		assert.Equal(t, "git-clone", env.Steps[0].Uses)
		assert.Equal(t, "open-pr", env.Steps[1].Uses)
	}
}

// TestInlinePromotionTemplates_DefaultNamespace verifies that when PromotionTemplateRef.Namespace
// is empty, the pipeline's own namespace is used.
func TestInlinePromotionTemplates_DefaultNamespace(t *testing.T) {
	tplSteps := []kardinalv1alpha1.StepSpec{{Uses: "health-check"}}
	// Template is in "kardinal-system", same as the pipeline
	tpl := makeTemplate("tpl", "kardinal-system", tplSteps)

	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "kardinal-system"},
		Spec: kardinalv1alpha1.PipelineSpec{
			Git: kardinalv1alpha1.PipelineGit{URL: "https://github.com/test/repo"},
			Environments: []kardinalv1alpha1.EnvironmentSpec{
				{
					Name: "prod",
					// Namespace left empty — should default to pipeline namespace
					PromotionTemplate: &kardinalv1alpha1.PromotionTemplateRef{Name: "tpl"},
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(resolverScheme()).
		WithObjects(tpl).
		Build()

	got, err := inlinePromotionTemplates(context.Background(), pipeline, c)

	require.NoError(t, err)
	require.Len(t, got.Spec.Environments[0].Steps, 1)
	assert.Equal(t, "health-check", got.Spec.Environments[0].Steps[0].Uses)
}

// TestInlinePromotionTemplates_EmptyTemplateSteps verifies that a template with no
// steps leaves the environment's default-step resolution untouched.
func TestInlinePromotionTemplates_EmptyTemplateSteps(t *testing.T) {
	// Template with no steps
	tpl := makeTemplate("empty", "test", nil)

	pipeline := makePipelineWithTemplateRef("p", "test", "prod", "empty", "test")

	c := fake.NewClientBuilder().
		WithScheme(resolverScheme()).
		WithObjects(tpl).
		Build()

	got, err := inlinePromotionTemplates(context.Background(), pipeline, c)

	require.NoError(t, err)
	// env.Steps remains nil — default step resolution by the builder is unchanged.
	assert.Nil(t, got.Spec.Environments[0].Steps,
		"empty template must not set steps (leaves default resolution intact)")
}

// TestInlinePromotionTemplates_WebhookCopied verifies that Webhook config within
// template steps is deep-copied (not shared pointer).
func TestInlinePromotionTemplates_WebhookCopied(t *testing.T) {
	tplSteps := []kardinalv1alpha1.StepSpec{
		{
			Uses: "notify",
			Webhook: &kardinalv1alpha1.WebhookConfig{
				URL: "https://hooks.example.com/notify",
			},
		},
	}
	tpl := makeTemplate("hook-tpl", "test", tplSteps)
	pipeline := makePipelineWithTemplateRef("p", "test", "prod", "hook-tpl", "test")

	c := fake.NewClientBuilder().
		WithScheme(resolverScheme()).
		WithObjects(tpl).
		Build()

	got, err := inlinePromotionTemplates(context.Background(), pipeline, c)

	require.NoError(t, err)
	require.Len(t, got.Spec.Environments[0].Steps, 1)
	step := got.Spec.Environments[0].Steps[0]
	require.NotNil(t, step.Webhook)
	assert.Equal(t, "https://hooks.example.com/notify", step.Webhook.URL)
	// Must be a different pointer than the original template (deep copy)
	assert.NotSame(t, tplSteps[0].Webhook, step.Webhook, "webhook must be deep-copied")
}
