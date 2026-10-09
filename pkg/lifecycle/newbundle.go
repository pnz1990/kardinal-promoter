// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// ValidateNewBundle checks the spec of a Bundle a caller asks to create. The
// Bundle API (POST /api/v1/bundles) and kardinal create bundle both call it,
// so the two accept the same Bundles (#1285):
//
//   - spec.pipeline is a valid object name that also fits in a label value;
//   - spec.type is image, config or mixed; an empty type is set to image;
//   - the type has the artifacts it needs (graph.ValidateBundleArtifacts):
//     images for image and mixed, configRef.commitSHA for config and mixed;
//   - an image Bundle has no configRef, which it would ignore;
//   - provenance.ciRunURL passes graph.ValidateCIRunURL.
//
// It does not check that the Pipeline exists: each caller does that with its
// own client and error. The error message is written for the caller.
func ValidateNewBundle(spec *v1alpha1.BundleSpec) error {
	if spec.Pipeline == "" {
		return fmt.Errorf("pipeline is required")
	}
	if len(validation.IsDNS1123Subdomain(spec.Pipeline)) > 0 || len(validation.IsValidLabelValue(spec.Pipeline)) > 0 {
		return fmt.Errorf("pipeline must be a valid Kubernetes object name of at most 63 characters")
	}
	if spec.Type == "" {
		spec.Type = "image"
	}
	switch spec.Type {
	case "image", "config", "mixed", "chart":
	default:
		return fmt.Errorf("type must be one of image, config, mixed, chart (got %q)", spec.Type)
	}
	if err := graph.ValidateBundleArtifacts(spec); err != nil {
		return err
	}
	// An image Bundle deploys only its images: a configRef on it would be
	// ignored without a word (#1353).
	if spec.Type == "image" && spec.ConfigRef != nil {
		return fmt.Errorf("type \"image\" does not use configRef; set type config or mixed with configRef.commitSHA, or drop configRef")
	}
	if spec.Provenance != nil {
		// The PR body and the UI link it (E2E-R22).
		if err := graph.ValidateCIRunURL(spec.Provenance.CIRunURL); err != nil {
			return err
		}
	}
	return nil
}
