// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package admission

import (
	"encoding/json"
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
//  3. Calls graph.DetectCycle to check for circular dependsOn.
//  4. Returns an AdmissionReview response: allowed=true (no cycle) or
//     allowed=false with a descriptive message (cycle detected).
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

		body, err := io.ReadAll(io.LimitReader(r.Body, maxAdmissionBody))
		if err != nil {
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
// for circular dependsOn. Returns an AdmissionResponse.
func validatePipeline(req *admissionv1.AdmissionRequest, log zerolog.Logger) *admissionv1.AdmissionResponse {
	var pipeline kardinalv1alpha1.Pipeline
	if err := json.Unmarshal(req.Object.Raw, &pipeline); err != nil {
		log.Error().Err(err).Msg("admission: failed to unmarshal Pipeline")
		return deny(fmt.Sprintf("failed to decode Pipeline: %v", err))
	}

	if err := graph.DetectCycle(&pipeline); err != nil {
		log.Info().
			Str("pipeline", pipeline.Name).
			Err(err).
			Msg("admission: Pipeline rejected — circular dependsOn detected")
		return deny(fmt.Sprintf("Pipeline rejected: circular dependsOn detected — %v. "+
			"Fix by removing one dependsOn reference to break the cycle.", err))
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
