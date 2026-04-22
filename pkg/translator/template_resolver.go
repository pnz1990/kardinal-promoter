// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// inlinePromotionTemplates resolves any PromotionTemplateRef on each environment
// in the pipeline and inlines the template's steps into the environment spec.
//
// Rules (spec O3):
//   - When env.PromotionTemplate != nil AND env.Steps is empty:
//     copy the template's steps into env.Steps.
//   - When env.PromotionTemplate != nil AND env.Steps is non-empty:
//     env.Steps takes precedence (local override wins). Template is still fetched
//     to validate it exists (returns error on missing template).
//   - When env.PromotionTemplate is nil: no-op.
//
// The function modifies a deep copy of the pipeline (never mutates the original).
// The caller (Translator.Translate) must use the returned pipeline for graph building.
//
// Spec O4: Resolution happens here (translator layer), not in the Builder.
// The Builder receives an already-resolved Pipeline and remains k8s-client-free.
func inlinePromotionTemplates(
	ctx context.Context,
	pipeline *kardinalv1alpha1.Pipeline,
	reader client.Reader,
) (*kardinalv1alpha1.Pipeline, error) {
	// Check whether any environment uses a template reference at all.
	// If none do, return the pipeline unchanged (fast path — no allocation).
	hasAny := false
	for i := range pipeline.Spec.Environments {
		if pipeline.Spec.Environments[i].PromotionTemplate != nil {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return pipeline, nil
	}

	// Deep copy so we never mutate the original.
	resolved := pipeline.DeepCopy()

	pipelineNS := resolved.Namespace
	if pipelineNS == "" {
		pipelineNS = "default"
	}

	// Cache resolved templates to avoid fetching the same template multiple times
	// (common: one template used by many environments).
	cache := make(map[string]*kardinalv1alpha1.PromotionTemplate)

	for i := range resolved.Spec.Environments {
		env := &resolved.Spec.Environments[i]
		if env.PromotionTemplate == nil {
			continue
		}

		ref := env.PromotionTemplate
		ns := ref.Namespace
		if ns == "" {
			ns = pipelineNS
		}
		cacheKey := ns + "/" + ref.Name

		tpl, ok := cache[cacheKey]
		if !ok {
			tpl = &kardinalv1alpha1.PromotionTemplate{}
			if err := reader.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ns}, tpl); err != nil {
				return nil, fmt.Errorf(
					"translator: environment %q references PromotionTemplate %q in namespace %q: TemplateNotFound: %w",
					env.Name, ref.Name, ns, err,
				)
			}
			cache[cacheKey] = tpl
		}

		// Local override: if env.Steps is set, keep it (template is validated only).
		// If env.Steps is empty, inline the template's steps.
		if len(env.Steps) == 0 && len(tpl.Spec.Steps) > 0 {
			copied := make([]kardinalv1alpha1.StepSpec, len(tpl.Spec.Steps))
			for j, s := range tpl.Spec.Steps {
				var ws *kardinalv1alpha1.WebhookConfig
				if s.Webhook != nil {
					wc := *s.Webhook
					ws = &wc
				}
				copied[j] = kardinalv1alpha1.StepSpec{
					Uses:    s.Uses,
					Webhook: ws,
				}
			}
			env.Steps = copied
		}
	}

	return resolved, nil
}
