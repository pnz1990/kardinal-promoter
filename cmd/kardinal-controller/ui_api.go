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
	"sort"
	"strings"
	"time"

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
	Paused           bool   `json:"paused,omitempty"` // true when spec.paused=true (#328)
	// #342: per-environment state counts for the multi-segment health bar.
	// Derived from the active Bundle's status.environments.
	// Keys are environment names, values are the promotion phase.
	EnvironmentStates map[string]string `json:"environmentStates,omitempty"`

	// #525: static pipeline topology from spec — rendered even when no Bundle is promoting.
	// Each entry is one environment in pipeline order with its dependsOn edges.
	EnvironmentTopology []uiEnvironmentNode `json:"environmentTopology,omitempty"`

	// Operations table columns (#462): derived from active Bundle + steps + gates.
	// BlockerCount is the number of PolicyGates with ready=false for the active bundle.
	BlockerCount int `json:"blockerCount,omitempty"`
	// FailedStepCount is the number of PromotionSteps with state=Failed for the active bundle.
	FailedStepCount int `json:"failedStepCount,omitempty"`
	// InventoryAgeDays is the number of days since the latest bundle was created.
	// A high value indicates stale inventory (no recent deploys).
	InventoryAgeDays int `json:"inventoryAgeDays,omitempty"`
	// LastMergedAt is the RFC3339 timestamp of the last env that reached Verified.
	// Empty string when no environment has been verified yet.
	LastMergedAt string `json:"lastMergedAt,omitempty"`
	// CDLevel summarises how automated this pipeline is.
	// "full-cd" = no manual gates, "mostly-cd" = 1–2 gates, "manual" = 3+ gates.
	CDLevel string `json:"cdLevel,omitempty"`
}

// uiEnvironmentNode is the static topology shape for one environment in a Pipeline.
// Used by the frontend to render the DAG when no active Bundle is promoting (#525).
type uiEnvironmentNode struct {
	// Name is the environment name (e.g. "test", "uat", "prod").
	Name string `json:"name"`
	// DependsOn is the list of environment names this one depends on.
	// Empty means this environment starts at the root of the DAG.
	DependsOn []string `json:"dependsOn,omitempty"`
	// Approval is "auto" or "pr-review" — shown as a badge on the node.
	Approval string `json:"approval,omitempty"`
}

// uiBundleResponse is the JSON shape for a Bundle in the UI API.
type uiBundleResponse struct {
	Name       string                     `json:"name"`
	Namespace  string                     `json:"namespace"`
	Phase      string                     `json:"phase"`
	Type       string                     `json:"type"`
	Pipeline   string                     `json:"pipeline"`
	CreatedAt  string                     `json:"createdAt,omitempty"` // ISO 8601 creation time for timeline sorting (#337)
	Provenance *v1alpha1.BundleProvenance `json:"provenance,omitempty"`
	// #503: Per-environment statuses for the bundle timeline view.
	Environments []uiBundleEnvStatus `json:"environments,omitempty"`
}

// uiBundleEnvStatus is the per-environment status summary for the timeline (#503).
type uiBundleEnvStatus struct {
	Name  string `json:"name"`
	Phase string `json:"phase,omitempty"`
	PRURL string `json:"prURL,omitempty"`
}

// uiGraphNode is a node in the promotion DAG.
type uiGraphNode struct {
	ID              string            `json:"id"`
	Type            string            `json:"type"` // "PromotionStep" or "PolicyGate"
	Label           string            `json:"label"`
	Environment     string            `json:"environment"`
	State           string            `json:"state"`
	Message         string            `json:"message,omitempty"`
	PRURL           string            `json:"prURL,omitempty"`
	Outputs         map[string]string `json:"outputs,omitempty"`
	Expression      string            `json:"expression,omitempty"`
	LastEvaluatedAt string            `json:"lastEvaluatedAt,omitempty"`
	StartedAt       string            `json:"startedAt,omitempty"` // ISO 8601 step creation time for elapsed timers (#330)
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
	Name             string            `json:"name"`
	Namespace        string            `json:"namespace"`
	Pipeline         string            `json:"pipeline"`
	Bundle           string            `json:"bundle"`
	Environment      string            `json:"environment"`
	StepType         string            `json:"stepType"`
	State            string            `json:"state"`
	Message          string            `json:"message,omitempty"`
	PRURL            string            `json:"prURL,omitempty"`
	Outputs          map[string]string `json:"outputs,omitempty"`
	CurrentStepIndex int               `json:"currentStepIndex"` // index into step sequence (#359)
	// #341: Kubernetes conditions — shown in NodeDetail conditions panel.
	Conditions []uiCondition `json:"conditions,omitempty"`
	// #501: Bake countdown fields — shown in StageDetailPanel.
	// BakeElapsedMinutes is contiguous healthy minutes so far in the current bake window.
	BakeElapsedMinutes int64 `json:"bakeElapsedMinutes,omitempty"`
	// BakeTargetMinutes is the required contiguous healthy duration from Pipeline spec.
	// Zero means no bake is configured for this environment.
	BakeTargetMinutes int `json:"bakeTargetMinutes,omitempty"`
	// BakeResets is the number of times the bake timer was reset due to a health alarm.
	BakeResets int `json:"bakeResets,omitempty"`
}

// uiCondition is the JSON shape for a Kubernetes condition.
type uiCondition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Message            string `json:"message,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// uiGateResponse is the JSON shape for a PolicyGate.
type uiGateResponse struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Expression      string `json:"expression"`
	Ready           bool   `json:"ready"`
	Reason          string `json:"reason,omitempty"`
	LastEvaluatedAt string `json:"lastEvaluatedAt,omitempty"`
	// #502: Override history from spec.overrides[] — shown in GateDetailPanel.
	Overrides []uiGateOverride `json:"overrides,omitempty"`
}

// uiGateOverride is the JSON shape for a PolicyGateOverride (K-09 audit record).
type uiGateOverride struct {
	Reason    string `json:"reason"`
	Stage     string `json:"stage,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	CreatedBy string `json:"createdBy,omitempty"`
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
	mux.HandleFunc("/api/v1/ui/gates/", s.handleGatesSubpath)
	mux.HandleFunc("/api/v1/ui/promote", s.handlePromote)
	mux.HandleFunc("/api/v1/ui/rollback", s.handleRollback)
	mux.HandleFunc("/api/v1/ui/pause", s.handlePause)
	mux.HandleFunc("/api/v1/ui/resume", s.handleResume)
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

	// Build per-pipeline active bundle index:
	// For each pipeline, find the most recent Promoting (or Verified) Bundle
	// and read its per-environment states for the health bar (#342).
	var bundleList v1alpha1.BundleList
	// List all bundles — ignore error (best-effort; env states will be nil on error)
	_ = s.client.List(r.Context(), &bundleList)
	// Index: pipelineName → active bundle (prefer Promoting > Available > Verified)
	type activeBundleEntry struct {
		name         string
		envStates    map[string]string
		createdAt    time.Time
		lastVerified time.Time // most recent HealthCheckedAt across all envs in this bundle
	}
	activeBundles := make(map[string]*activeBundleEntry)
	phaseOrder := map[string]int{"Promoting": 3, "Available": 2, "Verified": 1, "Failed": 0, "Superseded": -1}
	// Build a name→phase index for existing bundle lookup.
	bundlePhase := make(map[string]string, len(bundleList.Items))
	for _, b := range bundleList.Items {
		bundlePhase[b.Name] = b.Status.Phase
	}
	for _, b := range bundleList.Items {
		if b.Spec.Pipeline == "" {
			continue
		}
		key := fmt.Sprintf("%s/%s", b.Namespace, b.Spec.Pipeline)
		existing := activeBundles[key]
		newScore := phaseOrder[b.Status.Phase]
		existingScore := -99
		if existing != nil {
			existingScore = phaseOrder[bundlePhase[existing.name]]
		}
		if existing == nil || newScore > existingScore {
			envStates := make(map[string]string, len(b.Status.Environments))
			var lastVerified time.Time
			for _, env := range b.Status.Environments {
				if env.Phase != "" {
					envStates[env.Name] = env.Phase
				}
				// Track most recent HealthCheckedAt across all envs for lastMergedAt.
				if env.HealthCheckedAt != nil && env.HealthCheckedAt.After(lastVerified) {
					lastVerified = env.HealthCheckedAt.Time
				}
			}
			activeBundles[key] = &activeBundleEntry{
				name:         b.Name,
				envStates:    envStates,
				createdAt:    b.CreationTimestamp.Time,
				lastVerified: lastVerified,
			}
		}
	}

	// Build per-pipeline blocker count from PolicyGates (ops table #462).
	// Index: bundleName → count of gates with ready=false.
	var gateList v1alpha1.PolicyGateList
	_ = s.client.List(r.Context(), &gateList)
	blockersByBundle := make(map[string]int, len(gateList.Items))
	for _, g := range gateList.Items {
		if !g.Status.Ready {
			bundleLabel := g.Labels["kardinal.io/bundle"]
			if bundleLabel != "" {
				blockersByBundle[bundleLabel]++
			}
		}
	}

	// Build per-pipeline failed step count from PromotionSteps (ops table #462).
	var stepList v1alpha1.PromotionStepList
	_ = s.client.List(r.Context(), &stepList)
	failedStepsByBundle := make(map[string]int, len(stepList.Items))
	for _, ps := range stepList.Items {
		if ps.Status.State == "Failed" {
			failedStepsByBundle[ps.Spec.BundleName]++
		}
	}

	now := time.Now().UTC()

	result := make([]uiPipelineResponse, 0, len(list.Items))
	for _, p := range list.Items {
		key := fmt.Sprintf("%s/%s", p.Namespace, p.Name)
		resp := uiPipelineResponse{
			Name:             p.Name,
			Namespace:        p.Namespace,
			Phase:            pipelinePhase(&p),
			EnvironmentCount: len(p.Spec.Environments),
			Paused:           p.Spec.Paused,
			CDLevel:          pipelineCDLevel(&p),
		}
		// #525: build static environment topology from Pipeline.Spec so the UI can render
		// the DAG even when no Bundle is actively promoting.
		if len(p.Spec.Environments) > 0 {
			topo := make([]uiEnvironmentNode, 0, len(p.Spec.Environments))
			for _, env := range p.Spec.Environments {
				node := uiEnvironmentNode{
					Name:      env.Name,
					DependsOn: env.DependsOn,
					Approval:  string(env.Approval),
				}
				topo = append(topo, node)
			}
			resp.EnvironmentTopology = topo
		}
		if ab := activeBundles[key]; ab != nil {
			resp.ActiveBundleName = ab.name
			if len(ab.envStates) > 0 {
				resp.EnvironmentStates = ab.envStates
			}
			// Ops table: blocker + failed step counts derived from active bundle.
			if n := blockersByBundle[ab.name]; n > 0 {
				resp.BlockerCount = n
			}
			if n := failedStepsByBundle[ab.name]; n > 0 {
				resp.FailedStepCount = n
			}
			// Inventory age: days since the active bundle was created.
			if !ab.createdAt.IsZero() {
				resp.InventoryAgeDays = int(now.Sub(ab.createdAt).Hours() / 24)
			}
			// Last merged at: most recent HealthCheckedAt across all envs.
			if !ab.lastVerified.IsZero() {
				resp.LastMergedAt = ab.lastVerified.UTC().Format(time.RFC3339)
			}
		}
		result = append(result, resp)
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
		// #503: Per-environment statuses for the timeline view.
		envStatuses := make([]uiBundleEnvStatus, 0, len(b.Status.Environments))
		for _, env := range b.Status.Environments {
			envStatuses = append(envStatuses, uiBundleEnvStatus{
				Name:  env.Name,
				Phase: env.Phase,
				PRURL: env.PRURL,
			})
		}
		resp := uiBundleResponse{
			Name:       b.Name,
			Namespace:  b.Namespace,
			Phase:      b.Status.Phase,
			Type:       b.Spec.Type,
			Pipeline:   b.Spec.Pipeline,
			CreatedAt:  b.CreationTimestamp.Format("2006-01-02T15:04:05Z07:00"),
			Provenance: b.Spec.Provenance,
		}
		if len(envStatuses) > 0 {
			resp.Environments = envStatuses
		}
		result = append(result, resp)
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

// handleBundleGraph builds a clean, readable DAG for a single Bundle:
//   - One PromotionStep node per environment (synthetic "NotStarted" when not yet created)
//   - One PolicyGate node per unique gate template that applies to this bundle (deduped by kardinal.io/gate-template label)
//   - Directed edges: env[i] → gate(s) for env[i+1] → env[i+1] → ...
func (s *uiAPIServer) handleBundleGraph(w http.ResponseWriter, r *http.Request, bundleName string) {
	ctx := r.Context()

	// 1. Look up the Bundle to find its pipeline name and namespace.
	var bundle v1alpha1.Bundle
	var psList v1alpha1.PromotionStepList
	if err := s.client.List(ctx, &psList, client.MatchingLabels{"kardinal.io/bundle": bundleName}); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var bl v1alpha1.BundleList
	if err := s.client.List(ctx, &bl); err == nil {
		for _, b := range bl.Items {
			if b.Name == bundleName {
				bundle = b
				break
			}
		}
	}

	// 2. Fetch the Pipeline spec for ordered environments.
	var envOrder []string
	if bundle.Spec.Pipeline != "" {
		var pl v1alpha1.Pipeline
		if err := s.client.Get(ctx, client.ObjectKey{Name: bundle.Spec.Pipeline, Namespace: bundle.Namespace}, &pl); err == nil {
			for _, e := range pl.Spec.Environments {
				envOrder = append(envOrder, e.Name)
			}
		}
	}
	// Fallback: derive order from existing PromotionSteps if pipeline not found.
	if len(envOrder) == 0 {
		seen := map[string]bool{}
		for _, ps := range psList.Items {
			if !seen[ps.Spec.Environment] {
				envOrder = append(envOrder, ps.Spec.Environment)
				seen[ps.Spec.Environment] = true
			}
		}
	}

	// 3. Build PromotionStep lookup by environment.
	stepByEnv := map[string]*v1alpha1.PromotionStep{}
	for i := range psList.Items {
		ps := &psList.Items[i]
		stepByEnv[ps.Spec.Environment] = ps
	}

	// 4. Fetch PolicyGates for this bundle, deduplicate by kardinal.io/gate-name label.
	//    The gate-name label holds the user-defined gate name (e.g. "no-weekend-deploys")
	//    and is propagated through cross-product instantiations by the graph builder.
	//    We pick the canonical instance: prefer the gate whose own name equals the gate-name
	//    (i.e. the direct instantiation for this bundle), falling back to lexicographic first.
	var gateList v1alpha1.PolicyGateList
	_ = s.client.List(ctx, &gateList, client.MatchingLabels{"kardinal.io/bundle": bundleName})

	// namedGates: gate-name → best PolicyGate for that human-readable gate name
	namedGates := map[string]*v1alpha1.PolicyGate{}
	for i := range gateList.Items {
		g := &gateList.Items[i]
		// Prefer kardinal.io/gate-name (stable, human-readable, set by graph builder).
		// Fall back to gate-template, then to own name.
		gateName := g.Labels["kardinal.io/gate-name"]
		if gateName == "" {
			gateName = g.Labels["kardinal.io/gate-template"]
		}
		if gateName == "" {
			gateName = g.Name
		}
		prev, exists := namedGates[gateName]
		if !exists {
			namedGates[gateName] = g
			continue
		}
		// Prefer the gate whose own name equals the gate-name (direct instance for this bundle).
		if g.Name == gateName {
			namedGates[gateName] = g
		} else if prev.Name != gateName {
			// Both are cross-product copies — pick lexicographically first for stability.
			if g.Name < prev.Name {
				namedGates[gateName] = g
			}
		}
	}
	// templateGates is an alias for namedGates to keep the rest of the code readable.
	templateGates := namedGates

	// Group deduplicated gates by the environment they apply to.
	// Sort gate names within each env group for stable rendering.
	gatesByEnv := map[string][]string{} // env → []gateName (sorted)
	for gateName, g := range templateGates {
		env := g.Labels["kardinal.io/environment"]
		if env == "" {
			env = g.Labels["kardinal.io/applies-to"]
		}
		if env != "" {
			gatesByEnv[env] = append(gatesByEnv[env], gateName)
		}
	}
	for env := range gatesByEnv {
		sort.Strings(gatesByEnv[env])
	}

	// 5. Build nodes and edges in pipeline env order.
	nodes := make([]uiGraphNode, 0)
	edges := make([]uiGraphEdge, 0)

	prevIDs := []string{} // last node(s) in the chain before current env

	for _, env := range envOrder {
		// PromotionStep node (synthetic if not yet created).
		stepID := "step-" + env
		if ps, ok := stepByEnv[env]; ok {
			stepID = ps.Name
			state := ps.Status.State
			if state == "" {
				state = "NotStarted"
			}
			startedAt := ""
			if !ps.CreationTimestamp.IsZero() {
				startedAt = ps.CreationTimestamp.Format("2006-01-02T15:04:05Z07:00")
			}
			nodes = append(nodes, uiGraphNode{
				ID:          stepID,
				Type:        "PromotionStep",
				Label:       env,
				Environment: env,
				State:       state,
				Message:     ps.Status.Message,
				PRURL:       ps.Status.PRURL,
				Outputs:     ps.Status.Outputs,
				StartedAt:   startedAt,
			})
		} else {
			nodes = append(nodes, uiGraphNode{
				ID:          stepID,
				Type:        "PromotionStep",
				Label:       env,
				Environment: env,
				State:       "NotStarted",
			})
		}

		// Edges from previous chain tail → this step node.
		for _, pid := range prevIDs {
			edges = append(edges, uiGraphEdge{From: pid, To: stepID})
		}

		// Gate nodes that guard the *next* environment after this one.
		// Attach them after the current step, before the next step.
		nextEnvIdx := -1
		for i, e := range envOrder {
			if e == env {
				nextEnvIdx = i + 1
				break
			}
		}
		var nextEnv string
		if nextEnvIdx < len(envOrder) {
			nextEnv = envOrder[nextEnvIdx]
		}
		gateTemplates := gatesByEnv[nextEnv]
		if len(gateTemplates) == 0 {
			// No gates guard the next env — current step is the tail.
			prevIDs = []string{stepID}
		} else {
			// Insert gate nodes between current step and next step.
			gateIDs := make([]string, 0, len(gateTemplates))
			for _, tmpl := range gateTemplates {
				g := templateGates[tmpl]
				gateID := "gate-" + tmpl
				state := "Pending"
				if g.Status.Ready {
					state = "Pass"
				} else if g.Status.LastEvaluatedAt != nil {
					state = "Block"
				}
				lastEval := ""
				if g.Status.LastEvaluatedAt != nil {
					lastEval = g.Status.LastEvaluatedAt.Format("2006-01-02T15:04:05Z07:00")
				}
				nodes = append(nodes, uiGraphNode{
					ID:              gateID,
					Type:            "PolicyGate",
					Label:           tmpl,
					Environment:     tmpl,
					State:           state,
					Message:         g.Status.Reason,
					Expression:      g.Spec.Expression,
					LastEvaluatedAt: lastEval,
				})
				edges = append(edges, uiGraphEdge{From: stepID, To: gateID})
				gateIDs = append(gateIDs, gateID)
			}
			prevIDs = gateIDs
		}
	}

	writeJSON(w, uiGraphResponse{Nodes: nodes, Edges: edges})
}

func (s *uiAPIServer) handleBundleSteps(w http.ResponseWriter, r *http.Request, bundleName string) {
	var list v1alpha1.PromotionStepList
	if err := s.client.List(r.Context(), &list); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Build a bake target index: pipelineName+envName → bake minutes.
	// Populated lazily from the first step's Pipeline reference (#501).
	bakeTarget := make(map[string]int) // key: "pipelineName/envName"
	pipelinesLoaded := make(map[string]bool)

	result := make([]uiStepResponse, 0)
	for _, ps := range list.Items {
		if ps.Spec.BundleName != bundleName {
			continue
		}
		// Load bake target minutes from Pipeline spec (once per pipeline) (#501).
		plKey := ps.Namespace + "/" + ps.Spec.PipelineName
		if !pipelinesLoaded[plKey] {
			pipelinesLoaded[plKey] = true
			var pl v1alpha1.Pipeline
			if err := s.client.Get(r.Context(),
				client.ObjectKey{Name: ps.Spec.PipelineName, Namespace: ps.Namespace}, &pl); err == nil {
				for _, env := range pl.Spec.Environments {
					if env.Bake != nil {
						bakeTarget[ps.Spec.PipelineName+"/"+env.Name] = env.Bake.Minutes
					}
				}
			}
		}
		bakeMinutes := bakeTarget[ps.Spec.PipelineName+"/"+ps.Spec.Environment]
		result = append(result, uiStepResponse{
			Name:               ps.Name,
			Namespace:          ps.Namespace,
			Pipeline:           ps.Spec.PipelineName,
			Bundle:             ps.Spec.BundleName,
			Environment:        ps.Spec.Environment,
			StepType:           ps.Spec.StepType,
			State:              ps.Status.State,
			Message:            ps.Status.Message,
			PRURL:              ps.Status.PRURL,
			Outputs:            ps.Status.Outputs,
			CurrentStepIndex:   ps.Status.CurrentStepIndex,
			Conditions:         buildUIConditions(ps.Status.Conditions),
			BakeElapsedMinutes: ps.Status.BakeElapsedMinutes,
			BakeTargetMinutes:  bakeMinutes,
			BakeResets:         ps.Status.BakeResets,
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
		// #502: Populate override history from spec.overrides[].
		for _, ov := range g.Spec.Overrides {
			o := uiGateOverride{
				Reason:    ov.Reason,
				Stage:     ov.Stage,
				CreatedBy: ov.CreatedBy,
			}
			if !ov.ExpiresAt.IsZero() {
				o.ExpiresAt = ov.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
			}
			if !ov.CreatedAt.IsZero() {
				o.CreatedAt = ov.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")
			}
			resp.Overrides = append(resp.Overrides, o)
		}
		result = append(result, resp)
	}
	writeJSON(w, result)
}

// pipelinePhase returns the overall pipeline phase for the UI sidebar.
// Prefers status.phase (set by the pipeline reconciler) over the condition reason
// so the sidebar reflects the real runtime phase (Promoting, Degraded, etc.) (#349).
func pipelinePhase(p *v1alpha1.Pipeline) string {
	if p.Status.Phase != "" {
		return p.Status.Phase
	}
	// Fallback: derive from Ready condition (pre-reconciler or transitional state).
	for _, cond := range p.Status.Conditions {
		if cond.Type == "Ready" {
			if cond.Status == "True" {
				return "Ready"
			}
			if cond.Reason != "" {
				return cond.Reason
			}
		}
	}
	return "Initializing"
}

// pipelineCDLevel returns a human-readable CD automation level for the pipeline (#462).
// Derived from the number of PolicyGate references in the pipeline spec.
// "full-cd" = 0 gates (fully automated), "mostly-cd" = 1–2 gates, "manual" = 3+ gates.
func pipelineCDLevel(p *v1alpha1.Pipeline) string {
	gateCount := len(p.Spec.PolicyGates)
	switch {
	case gateCount == 0:
		return "full-cd"
	case gateCount <= 2:
		return "mostly-cd"
	default:
		return "manual"
	}
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

// handleRollback handles POST /api/v1/ui/rollback — creates a rollback Bundle
// for the given pipeline and environment. UI equivalent of `kardinal rollback`.
//
// Request body (JSON):
//
//	{"pipeline": "nginx-demo", "environment": "prod", "namespace": "default", "toBundle": "nginx-demo-abc"}
//
// Response (JSON on success):
//
//	{"bundle": "nginx-demo-rollback-xyz", "message": "rollback started"}
func (s *uiAPIServer) handleRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Pipeline    string `json:"pipeline"`
		Environment string `json:"environment"`
		Namespace   string `json:"namespace"`
		ToBundle    string `json:"toBundle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Pipeline == "" {
		http.Error(w, "pipeline is required", http.StatusBadRequest)
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}

	rollbackOf := req.ToBundle
	if rollbackOf == "" {
		// Find the most recently Verified PromotionStep for this pipeline+env.
		var steps v1alpha1.PromotionStepList
		labelSel := client.MatchingLabels{"kardinal.io/pipeline": req.Pipeline}
		if req.Environment != "" {
			labelSel["kardinal.io/environment"] = req.Environment
		}
		if err := s.client.List(r.Context(), &steps, client.InNamespace(ns), labelSel); err != nil {
			http.Error(w, "failed to list steps", http.StatusInternalServerError)
			return
		}
		var latest *v1alpha1.PromotionStep
		for i := range steps.Items {
			ps := &steps.Items[i]
			if ps.Status.State != "Verified" {
				continue
			}
			if latest == nil || ps.CreationTimestamp.After(latest.CreationTimestamp.Time) {
				latest = ps
			}
		}
		if latest == nil {
			http.Error(w, "no verified promotion found to roll back to", http.StatusNotFound)
			return
		}
		rollbackOf = latest.Spec.BundleName
	}

	// Copy bundle type from the target bundle.
	bundleType := "image"
	var srcBundle v1alpha1.Bundle
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: rollbackOf, Namespace: ns}, &srcBundle); err == nil {
		if srcBundle.Spec.Type != "" {
			bundleType = srcBundle.Spec.Type
		}
	}

	rollbackBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: req.Pipeline + "-rollback-",
			Namespace:    ns,
			Labels:       map[string]string{"kardinal.io/rollback": "true"},
		},
		Spec: v1alpha1.BundleSpec{
			Type:     bundleType,
			Pipeline: req.Pipeline,
			Provenance: &v1alpha1.BundleProvenance{
				RollbackOf: rollbackOf,
			},
		},
	}
	if req.Environment != "" {
		rollbackBundle.Spec.Intent = &v1alpha1.BundleIntent{
			TargetEnvironment: req.Environment,
		}
	}
	if err := s.client.Create(r.Context(), rollbackBundle); err != nil {
		s.log.Error().Err(err).Str("pipeline", req.Pipeline).Msg("ui: create rollback bundle")
		http.Error(w, "failed to create rollback bundle", http.StatusInternalServerError)
		return
	}

	s.log.Info().
		Str("bundle", rollbackBundle.Name).
		Str("pipeline", req.Pipeline).
		Str("rollbackOf", rollbackOf).
		Msg("ui: rollback triggered")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"bundle":  rollbackBundle.Name,
		"message": "rollback started — rolling back to " + rollbackOf,
	})
}

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

// handleGatesSubpath handles:
//
//	POST /api/v1/ui/gates/{name}/approve — add a time-limited override to a PolicyGate.
//	POST /api/v1/ui/gates/{namespace}/{name}/approve
//
// Request body (JSON):
//
//	{"reason": "emergency deploy", "namespace": "default", "expiresInMinutes": 60}
//
// Response (JSON on success):
//
//	{"message": "gate overridden until 2026-04-14T15:04:05Z"}
func (s *uiAPIServer) handleGatesSubpath(w http.ResponseWriter, r *http.Request) {
	// path = "{name}/approve" or "{namespace}/{name}/approve"
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/ui/gates/")
	parts := strings.Split(path, "/")

	var gateName, gateNS, action string
	switch len(parts) {
	case 2: // {name}/approve
		gateName = parts[0]
		action = parts[1]
		gateNS = "default"
	case 3: // {namespace}/{name}/approve
		gateNS = parts[0]
		gateName = parts[1]
		action = parts[2]
	default:
		http.NotFound(w, r)
		return
	}

	if action != "approve" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Reason           string `json:"reason"`
		Namespace        string `json:"namespace"`
		Stage            string `json:"stage"`
		ExpiresInMinutes int    `json:"expiresInMinutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Reason == "" {
		http.Error(w, "reason is required", http.StatusBadRequest)
		return
	}
	if req.Namespace != "" {
		gateNS = req.Namespace
	}
	expiresMins := req.ExpiresInMinutes
	if expiresMins <= 0 {
		expiresMins = 60 // default 1h
	}

	var gate v1alpha1.PolicyGate
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: gateName, Namespace: gateNS}, &gate); err != nil {
		http.Error(w, "gate not found", http.StatusNotFound)
		return
	}

	now := time.Now().UTC()
	expiresAt := metav1.Time{Time: now.Add(time.Duration(expiresMins) * time.Minute)}
	createdAt := metav1.Time{Time: now}
	override := v1alpha1.PolicyGateOverride{
		Reason:    req.Reason,
		Stage:     req.Stage,
		ExpiresAt: expiresAt,
		CreatedAt: &createdAt,
		CreatedBy: "ui-action",
	}
	gate.Spec.Overrides = append(gate.Spec.Overrides, override)
	if err := s.client.Update(r.Context(), &gate); err != nil {
		s.log.Error().Err(err).Str("gate", gateName).Msg("ui: approve gate")
		http.Error(w, "failed to update gate", http.StatusInternalServerError)
		return
	}

	s.log.Info().
		Str("gate", gateName).
		Str("reason", req.Reason).
		Time("expiresAt", expiresAt.Time).
		Msg("ui: gate approved via override")

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"message": "gate overridden until " + expiresAt.UTC().Format(time.RFC3339),
	})
}

// handlePause handles POST /api/v1/ui/pause — sets Pipeline.spec.paused=true.
//
// Request body (JSON):
//
//	{"pipeline": "nginx-demo", "namespace": "default"}
//
// Response (JSON on success):
//
//	{"message": "pipeline nginx-demo paused"}
func (s *uiAPIServer) handlePause(w http.ResponseWriter, r *http.Request) {
	s.handlePauseResume(w, r, true)
}

// handleResume handles POST /api/v1/ui/resume — sets Pipeline.spec.paused=false.
func (s *uiAPIServer) handleResume(w http.ResponseWriter, r *http.Request) {
	s.handlePauseResume(w, r, false)
}

func (s *uiAPIServer) handlePauseResume(w http.ResponseWriter, r *http.Request, pause bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Pipeline  string `json:"pipeline"`
		Namespace string `json:"namespace"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Pipeline == "" {
		http.Error(w, "pipeline is required", http.StatusBadRequest)
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}

	var pl v1alpha1.Pipeline
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: req.Pipeline, Namespace: ns}, &pl); err != nil {
		http.Error(w, "pipeline not found", http.StatusNotFound)
		return
	}

	pl.Spec.Paused = pause
	if err := s.client.Update(r.Context(), &pl); err != nil {
		action := "pause"
		if !pause {
			action = "resume"
		}
		s.log.Error().Err(err).Str("pipeline", req.Pipeline).Msg("ui: " + action + " pipeline")
		http.Error(w, "failed to update pipeline", http.StatusInternalServerError)
		return
	}

	action := "paused"
	if !pause {
		action = "resumed"
	}
	s.log.Info().Str("pipeline", req.Pipeline).Bool("paused", pause).Msg("ui: pipeline " + action)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"message": "pipeline " + req.Pipeline + " " + action,
	})
}

// buildUIConditions converts Kubernetes metav1.Condition slice to UI-friendly shape (#341).
func buildUIConditions(conditions []metav1.Condition) []uiCondition {
	if len(conditions) == 0 {
		return nil
	}
	result := make([]uiCondition, 0, len(conditions))
	for _, c := range conditions {
		uc := uiCondition{
			Type:    c.Type,
			Status:  string(c.Status),
			Message: c.Message,
		}
		if !c.LastTransitionTime.IsZero() {
			uc.LastTransitionTime = c.LastTransitionTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		result = append(result, uc)
	}
	return result
}
