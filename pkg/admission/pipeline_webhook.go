// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package admission

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/rs/zerolog"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

const maxAdmissionBody = 1 << 20 // 1 MB

// PipelineWebhookHandler returns an http.HandlerFunc that validates Pipeline
// objects submitted to the Kubernetes API server.
//
// The handler:
//  1. Decodes the AdmissionReview request.
//  2. Unmarshals the Pipeline object from request.object.raw.
//  3. Calls graph.DetectCycle to check the environment ordering (cycles,
//     unknown dependsOn, no environments). DELETE is always allowed.
//  4. Returns an AdmissionReview response: allowed=true, or allowed=false
//     with the ordering error as the message.
//
// Bodies over 1 MB are rejected with 413 rather than truncated.
//
// Mount at POST /webhook/validate/pipeline on the webhook server.
// Requires a ValidatingWebhookConfiguration pointing at this path to be
// installed in the cluster by the operator (out of scope for this PR).
//
// Design ref: docs/design/15-production-readiness.md §Lens 4 O1–O9
func PipelineWebhookHandler(log zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAdmissionBody))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
				return
			}
			log.Error().Err(err).Msg("admission: failed to read body")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		var review admissionv1.AdmissionReview
		if err := json.Unmarshal(body, &review); err != nil {
			log.Error().Err(err).Msg("admission: failed to decode AdmissionReview")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if review.Request == nil {
			log.Warn().Msg("admission: received AdmissionReview with nil request")
			http.Error(w, "bad request: nil request", http.StatusBadRequest)
			return
		}

		resp := validatePipeline(review.Request, log)
		resp.UID = review.Request.UID

		out := admissionv1.AdmissionReview{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "admission.k8s.io/v1",
				Kind:       "AdmissionReview",
			},
			Response: resp,
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(out); err != nil {
			log.Error().Err(err).Msg("admission: failed to encode response")
		}
	}
}

// validatePipeline decodes the Pipeline from the raw object bytes and checks
// its environment ordering (unknown dependsOn, no environments, cycles).
// DELETE is always allowed: the request carries no object and there is
// nothing to validate. Returns an AdmissionResponse.
func validatePipeline(req *admissionv1.AdmissionRequest, log zerolog.Logger) *admissionv1.AdmissionResponse {
	if req.Operation == admissionv1.Delete {
		return allow()
	}
	var pipeline kardinalv1alpha1.Pipeline
	if err := json.Unmarshal(req.Object.Raw, &pipeline); err != nil {
		log.Error().Err(err).Msg("admission: failed to unmarshal Pipeline")
		return deny(fmt.Sprintf("failed to decode Pipeline: %v", err))
	}

	if err := graph.DetectCycle(&pipeline); err != nil {
		// The error names the actual problem (cycle, unknown dependsOn, or no
		// environments), so report it as-is.
		log.Info().
			Str("pipeline", pipeline.Name).
			Err(err).
			Msg("admission: Pipeline rejected — invalid environment ordering")
		return deny(fmt.Sprintf("Pipeline rejected: %v", err))
	}

	log.Debug().
		Str("pipeline", pipeline.Name).
		Msg("admission: Pipeline admitted — no cycle detected")
	return allow()
}

func allow() *admissionv1.AdmissionResponse {
	return &admissionv1.AdmissionResponse{Allowed: true}
}

func deny(msg string) *admissionv1.AdmissionResponse {
	return &admissionv1.AdmissionResponse{
		Allowed: false,
		Result: &metav1.Status{
			Code:    http.StatusBadRequest,
			Message: msg,
		},
	}
}
