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

// Package main (ui_api.go) implements the read-only REST API that backs the
// embedded kardinal-ui React application.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"github.com/rs/zerolog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel/library"
)

// uiPipelineResponse is the JSON shape for a Pipeline in the UI API.
type uiPipelineResponse struct {
	Name             string `json:"name"`
	Namespace        string `json:"namespace"`
	Phase            string `json:"phase"`
	EnvironmentCount int    `json:"environmentCount"`
	ActiveBundleName string `json:"activeBundleName,omitempty"`
}

// uiBundleResponse is the JSON shape for a Bundle in the UI API.
type uiBundleResponse struct {
	Name       string                     `json:"name"`
	Namespace  string                     `json:"namespace"`
	Phase      string                     `json:"phase"`
	Type       string                     `json:"type"`
	Pipeline   string                     `json:"pipeline"`
	Provenance *v1alpha1.BundleProvenance `json:"provenance,omitempty"`
}

// uiGraphNode is a node in the promotion DAG.
type uiGraphNode struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"` // "PromotionStep" or "PolicyGate"
	Label       string            `json:"label"`
	Environment string            `json:"environment"`
	State       string            `json:"state"`
	Message     string            `json:"message,omitempty"`
	PRURL       string            `json:"prURL,omitempty"`
	Outputs     map[string]string `json:"outputs,omitempty"`
}

// uiGraphEdge is a directed dependency edge in the promotion DAG.
type uiGraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// uiGraphResponse is the DAG response for a Bundle.
type uiGraphResponse struct {
	Nodes []uiGraphNode `json:"nodes"`
	Edges []uiGraphEdge `json:"edges"`
}

// uiStepResponse is the JSON shape for a PromotionStep.
type uiStepResponse struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Pipeline    string            `json:"pipeline"`
	Bundle      string            `json:"bundle"`
	Environment string            `json:"environment"`
	StepType    string            `json:"stepType"`
	State       string            `json:"state"`
	Message     string            `json:"message,omitempty"`
	PRURL       string            `json:"prURL,omitempty"`
	Outputs     map[string]string `json:"outputs,omitempty"`
}

// uiGateResponse is the JSON shape for a PolicyGate.
type uiGateResponse struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Expression      string `json:"expression"`
	Ready           bool   `json:"ready"`
	Reason          string `json:"reason,omitempty"`
	LastEvaluatedAt string `json:"lastEvaluatedAt,omitempty"`
}

// uiAPIServer serves the read-only REST API for the embedded UI.
type uiAPIServer struct {
	client client.Client
	log    zerolog.Logger
}

func newUIAPIServer(k8s client.Client, log zerolog.Logger) *uiAPIServer {
	return &uiAPIServer{client: k8s, log: log}
}

// RegisterRoutes registers all /api/v1/ui/* routes on the given mux.
func (s *uiAPIServer) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/ui/pipelines", s.handlePipelines)
	mux.HandleFunc("/api/v1/ui/pipelines/", s.handlePipelinesSubpath)
	mux.HandleFunc("/api/v1/ui/bundles/", s.handleBundleSubresource)
	mux.HandleFunc("/api/v1/ui/gates", s.handleGates)
	mux.HandleFunc("/api/v1/ui/promote", s.handlePromote)
	mux.HandleFunc("/api/v1/ui/validate-cel", s.handleValidateCEL)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "encoding error", http.StatusInternalServerError)
	}
}

// handlePipelines handles GET /api/v1/ui/pipelines (list only).
func (s *uiAPIServer) handlePipelines(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var list v1alpha1.PipelineList
	if err := s.client.List(r.Context(), &list); err != nil {
		s.log.Error().Err(err).Msg("ui: list pipelines")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	result := make([]uiPipelineResponse, 0, len(list.Items))
	for _, p := range list.Items {
		result = append(result, uiPipelineResponse{
			Name:             p.Name,
			Namespace:        p.Namespace,
			Phase:            pipelinePhase(&p),
			EnvironmentCount: len(p.Spec.Environments),
		})
	}
	writeJSON(w, result)
}

// handlePipelinesSubpath handles GET /api/v1/ui/pipelines/{name}/bundles.
func (s *uiAPIServer) handlePipelinesSubpath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// path = "<name>/bundles"
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/ui/pipelines/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 2 && parts[1] == "bundles" {
		s.handleBundlesForPipeline(w, r, parts[0])
		return
	}
	http.NotFound(w, r)
}

func (s *uiAPIServer) handleBundlesForPipeline(w http.ResponseWriter, r *http.Request, pipelineName string) {
	var list v1alpha1.BundleList
	if err := s.client.List(r.Context(), &list); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	result := make([]uiBundleResponse, 0)
	for _, b := range list.Items {
		if b.Spec.Pipeline != pipelineName {
			continue
		}
		result = append(result, uiBundleResponse{
			Name:       b.Name,
			Namespace:  b.Namespace,
			Phase:      b.Status.Phase,
			Type:       b.Spec.Type,
			Pipeline:   b.Spec.Pipeline,
			Provenance: b.Spec.Provenance,
		})
	}
	writeJSON(w, result)
}

// handleBundleSubresource handles:
//
//	GET /api/v1/ui/bundles/{name}/graph
//	GET /api/v1/ui/bundles/{name}/steps
func (s *uiAPIServer) handleBundleSubresource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/ui/bundles/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	bundleName, resource := parts[0], parts[1]

	switch resource {
	case "graph":
		s.handleBundleGraph(w, r, bundleName)
	case "steps":
		s.handleBundleSteps(w, r, bundleName)
	default:
		http.NotFound(w, r)
	}
}

func (s *uiAPIServer) handleBundleGraph(w http.ResponseWriter, r *http.Request, bundleName string) {
	var psList v1alpha1.PromotionStepList
	if err := s.client.List(r.Context(), &psList); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var gateList v1alpha1.PolicyGateList
	_ = s.client.List(r.Context(), &gateList) // best-effort

	nodes := make([]uiGraphNode, 0)
	edges := make([]uiGraphEdge, 0)

	for _, ps := range psList.Items {
		if ps.Spec.BundleName != bundleName {
			continue
		}
		node := uiGraphNode{
			ID:          ps.Name,
			Type:        "PromotionStep",
			Label:       ps.Spec.Environment,
			Environment: ps.Spec.Environment,
			State:       ps.Status.State,
			Message:     ps.Status.Message,
			PRURL:       ps.Status.PRURL,
			Outputs:     ps.Status.Outputs,
		}
		nodes = append(nodes, node)
	}

	for _, gate := range gateList.Items {
		state := "Pending"
		if gate.Status.Ready {
			state = "Pass"
		} else if gate.Status.LastEvaluatedAt != nil {
			state = "Fail"
		}
		node := uiGraphNode{
			ID:          gate.Namespace + "/" + gate.Name,
			Type:        "PolicyGate",
			Label:       gate.Name,
			Environment: gate.Name,
			State:       state,
			Message:     gate.Status.Reason,
		}
		nodes = append(nodes, node)
	}

	writeJSON(w, uiGraphResponse{Nodes: nodes, Edges: edges})
}

func (s *uiAPIServer) handleBundleSteps(w http.ResponseWriter, r *http.Request, bundleName string) {
	var list v1alpha1.PromotionStepList
	if err := s.client.List(r.Context(), &list); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	result := make([]uiStepResponse, 0)
	for _, ps := range list.Items {
		if ps.Spec.BundleName != bundleName {
			continue
		}
		result = append(result, uiStepResponse{
			Name:        ps.Name,
			Namespace:   ps.Namespace,
			Pipeline:    ps.Spec.PipelineName,
			Bundle:      ps.Spec.BundleName,
			Environment: ps.Spec.Environment,
			StepType:    ps.Spec.StepType,
			State:       ps.Status.State,
			Message:     ps.Status.Message,
			PRURL:       ps.Status.PRURL,
			Outputs:     ps.Status.Outputs,
		})
	}
	writeJSON(w, result)
}

// handleGates handles GET /api/v1/ui/gates.
func (s *uiAPIServer) handleGates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var list v1alpha1.PolicyGateList
	if err := s.client.List(r.Context(), &list); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	result := make([]uiGateResponse, 0, len(list.Items))
	for _, g := range list.Items {
		resp := uiGateResponse{
			Name:       g.Name,
			Namespace:  g.Namespace,
			Expression: g.Spec.Expression,
			Ready:      g.Status.Ready,
			Reason:     g.Status.Reason,
		}
		if g.Status.LastEvaluatedAt != nil {
			resp.LastEvaluatedAt = g.Status.LastEvaluatedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		result = append(result, resp)
	}
	writeJSON(w, result)
}

// pipelinePhase returns the overall pipeline phase from its status conditions.
func pipelinePhase(p *v1alpha1.Pipeline) string {
	for _, cond := range p.Status.Conditions {
		if cond.Type == "Ready" {
			if cond.Status == "True" {
				return "Ready"
			}
			return cond.Reason
		}
	}
	return "Initializing"
}

// handlePromote handles POST /api/v1/ui/promote — creates a Bundle targeting the
// given pipeline and environment, triggering a new promotion. This is the UI
// equivalent of `kardinal promote <pipeline> --env <env>`.
//
// Request body (JSON):
//
//	{"pipeline": "nginx-demo", "environment": "prod", "namespace": "default"}
//
// Response (JSON on success):
//
//	{"bundle": "nginx-demo-abc123", "message": "promotion started"}
func (s *uiAPIServer) handlePromote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Pipeline    string `json:"pipeline"`
		Environment string `json:"environment"`
		Namespace   string `json:"namespace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Pipeline == "" || req.Environment == "" {
		http.Error(w, "pipeline and environment are required", http.StatusBadRequest)
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}

	// Verify the pipeline exists.
	var pl v1alpha1.Pipeline
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: req.Pipeline, Namespace: ns}, &pl); err != nil {
		http.Error(w, "pipeline not found", http.StatusNotFound)
		return
	}

	// Verify the environment exists in the pipeline.
	found := false
	for _, e := range pl.Spec.Environments {
		if e.Name == req.Environment {
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "environment not found in pipeline", http.StatusBadRequest)
		return
	}

	// Create a Bundle targeting the specified environment (same as CLI promote).
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: req.Pipeline + "-",
			Namespace:    ns,
		},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: req.Pipeline,
			Intent: &v1alpha1.BundleIntent{
				TargetEnvironment: req.Environment,
			},
		},
	}
	if err := s.client.Create(r.Context(), bundle); err != nil {
		s.log.Error().Err(err).Str("pipeline", req.Pipeline).Str("env", req.Environment).Msg("ui: create promote bundle")
		http.Error(w, "failed to create bundle", http.StatusInternalServerError)
		return
	}

	s.log.Info().
		Str("bundle", bundle.Name).
		Str("pipeline", req.Pipeline).
		Str("env", req.Environment).
		Msg("ui: promote triggered")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"bundle":  bundle.Name,
		"message": "promotion started — track with kardinal get bundles " + req.Pipeline,
	})
}

// handleValidateCEL validates a PolicyGate CEL expression using the same CEL
// environment as the backend evaluator. The UI calls this on-the-fly as the
// user types to provide syntax feedback without needing the full evaluation context.
//
// POST /api/v1/ui/validate-cel
// Request: {"expression": "!schedule.isWeekend"}
// Response: {"valid": true} or {"valid": false, "error": "no such key: ..."}
//
// This endpoint uses pkg/cel/library directly (not pkg/cel) — library is explicitly
// allowed outside policygate and creates no logic leaks (stateless, no CRD writes).
func (s *uiAPIServer) handleValidateCEL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Expression string `json:"expression"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Expression == "" {
		http.Error(w, "expression field required", http.StatusBadRequest)
		return
	}

	// Build a CEL environment with all kro library extensions — same function set
	// as the backend PolicyGate evaluator. Uses pkg/cel/library directly.
	env, err := cel.NewEnv(
		cel.Variable("bundle", cel.DynType),
		cel.Variable("schedule", cel.DynType),
		cel.Variable("environment", cel.DynType),
		cel.Variable("metrics", cel.DynType),
		cel.Variable("upstream", cel.DynType),
		cel.Variable("previousBundle", cel.DynType),
		ext.Strings(),
		library.JSON(),
		library.Maps(),
		library.Lists(),
		library.Random(),
	)
	if err != nil {
		http.Error(w, "failed to build CEL environment", http.StatusInternalServerError)
		return
	}

	_, issues := env.Compile(req.Expression)
	w.Header().Set("Content-Type", "application/json")
	if issues != nil && issues.Err() != nil {
		// Normalise error to a short, user-friendly message.
		msg := issues.Err().Error()
		if len(msg) > 200 {
			msg = msg[:197] + "…"
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"valid": false,
			"error": fmt.Sprintf("CEL compile error: %s", msg),
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"valid": true,
	})
}
