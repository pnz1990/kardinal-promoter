// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package admission

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rs/zerolog"
	admissionv1 "k8s.io/api/admission/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// BundleWebhookHandler returns an http.HandlerFunc that validates Bundles
// created through the Kubernetes API server: spec.provenance.ciRunURL must
// pass graph.ValidateCIRunURL (empty, or an absolute http(s) URL), the check
// the bundle API makes. Only CREATE is checked; every other operation is
// allowed, so existing Bundles, whatever their ciRunURL, can still be updated
// and deleted.
//
// Mount at POST /webhook/validate/bundle on the webhook server
// (--bundle-admission-webhook). Like the Pipeline webhook it needs a
// ValidatingWebhookConfiguration installed by the operator.
func BundleWebhookHandler(log zerolog.Logger) http.HandlerFunc {
	return reviewHandler(log, validateBundle)
}

// validateBundle decodes a Bundle being created and checks its ciRunURL.
func validateBundle(req *admissionv1.AdmissionRequest, log zerolog.Logger) *admissionv1.AdmissionResponse {
	if req.Operation != admissionv1.Create {
		return allow()
	}
	var bundle kardinalv1alpha1.Bundle
	if err := json.Unmarshal(req.Object.Raw, &bundle); err != nil {
		log.Error().Err(err).Msg("admission: failed to unmarshal Bundle")
		return deny(fmt.Sprintf("failed to decode Bundle: %v", err))
	}
	if p := bundle.Spec.Provenance; p != nil {
		if err := graph.ValidateCIRunURL(p.CIRunURL); err != nil {
			log.Info().
				Str("bundle", bundle.Name).
				Err(err).
				Msg("admission: Bundle rejected — invalid provenance.ciRunURL")
			return deny(fmt.Sprintf("Bundle rejected: %v", err))
		}
	}
	return allow()
}
