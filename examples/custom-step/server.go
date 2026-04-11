// Copyright 2026 The kardinal-promoter Authors.
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

// Package main is an example custom promotion step server for kardinal-promoter.
//
// It implements the kardinal custom step HTTP API:
//
//	POST /step
//	Request:  {"bundle": {...}, "environment": "prod", "inputs": {...}, "outputs_so_far": {...}}
//	Response: {"result": "pass|fail", "outputs": {"key": "value"}, "message": "..."}
//
// This example performs a simple version check: it rejects promotions where the
// image tag contains "alpha" or "beta" to the prod environment.
//
// Deploy as a Kubernetes Service and reference it in your Pipeline:
//
//	spec:
//	  environments:
//	    - name: prod
//	      steps:
//	        - uses: my-team/version-gate
//	          webhook:
//	            url: http://custom-step-server.custom-steps.svc.cluster.local/step
//	            timeoutSeconds: 30
//	            secretRef:
//	              name: custom-step-token
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// StepRequest matches the JSON body sent by the kardinal PromotionStep reconciler.
type StepRequest struct {
	Bundle       BundleSpec        `json:"bundle"`
	Environment  string            `json:"environment"`
	Inputs       map[string]string `json:"inputs"`
	OutputsSoFar map[string]string `json:"outputs_so_far"`
}

// BundleSpec is the portion of the Bundle CRD spec that the custom step receives.
type BundleSpec struct {
	Type   string     `json:"type"`
	Images []ImageRef `json:"images,omitempty"`
}

// ImageRef is a container image reference.
type ImageRef struct {
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
}

// StepResponse is the JSON body returned to the kardinal PromotionStep reconciler.
type StepResponse struct {
	// Result is "pass" or "fail".
	Result string `json:"result"`
	// Outputs are key/value pairs made available to subsequent steps.
	Outputs map[string]string `json:"outputs,omitempty"`
	// Message is a human-readable explanation shown in kardinal explain.
	Message string `json:"message,omitempty"`
}

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	http.HandleFunc("/step", handleStep)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("custom step server listening on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// handleStep implements the kardinal custom step HTTP contract.
// It is idempotent: calling it multiple times with the same request produces the same result.
func handleStep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req StepRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
		return
	}

	resp := evaluate(req)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("encode response: %v", err)
	}
}

// evaluate runs the version-gate logic: reject pre-release tags in prod.
func evaluate(req StepRequest) StepResponse {
	// Only enforce in prod-like environments.
	env := strings.ToLower(req.Environment)
	if env != "prod" && env != "production" {
		return StepResponse{
			Result:  "pass",
			Message: fmt.Sprintf("version gate: not enforced in environment %q", req.Environment),
			Outputs: map[string]string{"gated_at": time.Now().UTC().Format(time.RFC3339)},
		}
	}

	for _, img := range req.Bundle.Images {
		tag := strings.ToLower(img.Tag)
		for _, suffix := range []string{"alpha", "beta", "rc", "snapshot", "dev"} {
			if strings.Contains(tag, suffix) {
				return StepResponse{
					Result:  "fail",
					Message: fmt.Sprintf("version gate: image %s:%s is a pre-release tag (%q suffix); promote a stable release to prod", img.Repository, img.Tag, suffix),
				}
			}
		}
	}

	return StepResponse{
		Result:  "pass",
		Message: "version gate: all images are stable releases",
		Outputs: map[string]string{"gated_at": time.Now().UTC().Format(time.RFC3339)},
	}
}
