// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// ValidateNewBundle checks the spec of a Bundle a caller asks to create. The
// Bundle API (POST /api/v1/bundles) and kardinal create bundle both call it,
// so the two accept the same Bundles (#1285):
//
//   - spec.pipeline is a valid object name that also fits in a label value;
//   - spec.type is image, config, mixed or chart; an empty type is inferred
//     from the fields (InferBundleType);
//   - the type has the artifacts it needs (graph.ValidateBundleArtifacts):
//     images for image and mixed, configRef.commitSHA for config and mixed;
//   - an image or chart Bundle has no configRef, which it would ignore;
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
		spec.Type = InferBundleType(spec)
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
	// A chart Bundle promotes its chart version only (update.strategy helm):
	// a configRef would be dropped without a word, whether the type was set
	// or inferred from chart.
	if spec.Type == "chart" && spec.ConfigRef != nil {
		return fmt.Errorf("type \"chart\" does not use configRef; drop configRef, or create a separate config Bundle for configRef.commitSHA")
	}
	if spec.Provenance != nil {
		// The PR body and the UI link it (E2E-R22).
		if err := graph.ValidateCIRunURL(spec.Provenance.CIRunURL); err != nil {
			return err
		}
		if err := validateCommitTime(spec.Provenance.CommitTime, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

// commitTimeSkew is how far in the future a provenance.commitTime may be:
// CI runners' clocks drift a little from the API server's.
const commitTimeSkew = 5 * time.Minute

// validateCommitTime refuses a provenance.commitTime that is zero or later
// than now (beyond commitTimeSkew): the lead time metrics start at it
// (LeadTimeStart), and neither can be when a commit was made.
func validateCommitTime(t *metav1.Time, now time.Time) error {
	switch {
	case t == nil:
		return nil
	case t.IsZero():
		return fmt.Errorf("provenance.commitTime is the zero time; leave it unset when the commit time is not known")
	case t.After(now.Add(commitTimeSkew)):
		return fmt.Errorf("provenance.commitTime %s is in the future (now %s)", t.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	return nil
}

// InferBundleType is the type of a Bundle whose spec.type is unset, from
// what it carries: chart for spec.chart, mixed for images and a configRef,
// config for a configRef alone, else image (POST /api/v1/bundles; the CRD
// still requires the field, so it is set before the Bundle is created).
func InferBundleType(spec *v1alpha1.BundleSpec) string {
	switch {
	case spec.Chart != nil:
		return "chart"
	case spec.ConfigRef != nil && len(spec.Images) > 0:
		return "mixed"
	case spec.ConfigRef != nil:
		return "config"
	default:
		return "image"
	}
}
