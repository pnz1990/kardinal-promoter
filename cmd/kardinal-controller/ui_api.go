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

// Package main (ui_api.go) implements the REST API that backs the embedded
// kardinal-ui React application: reads, plus the promote, rollback, pause,
// resume, gate-approve and bundle-create actions.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	graphpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/policygate"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// maxGateOverrideMinutes bounds a UI gate override to one day.
const maxGateOverrideMinutes = 24 * 60

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
	// BlockerCount is the number of the active bundle's PolicyGates with
	// ready=false in environments it has reached (every upstream Verified)
	// but not started; see blockingGateCount.
	BlockerCount int `json:"blockerCount,omitempty"`
	// FailedStepCount is the number of PromotionSteps with state=Failed for the active bundle.
	FailedStepCount int `json:"failedStepCount,omitempty"`
	// InventoryAgeDays is the number of days since the latest bundle was created.
	// A high value indicates stale inventory (no recent deploys).
	// A pointer so that 0 ("created today") is sent; nil means no active bundle.
	InventoryAgeDays *int `json:"inventoryAgeDays,omitempty"`
	// LastMergedAt is the RFC3339 timestamp of the last env that reached Verified.
	// Empty string when no environment has been verified yet.
	LastMergedAt string `json:"lastMergedAt,omitempty"`
	// DeploymentMetrics is the Pipeline's status.deploymentMetrics, written by
	// the PipelineReconciler (DORA throughput and stability for the last
	// environment). Nil until a Bundle is Verified there.
	DeploymentMetrics *v1alpha1.PipelineDeploymentMetrics `json:"deploymentMetrics,omitempty"`
	// ActiveBundleVersion is what the active Bundle ships (scm.BundleVersion:
	// the image tag, or "config <sha>"), for the fleet board.
	ActiveBundleVersion string `json:"activeBundleVersion,omitempty"`
	// Deployed is, per environment, the Bundle whose change the environment
	// runs: the newest PromotionStep there that landed its change
	// (lifecycle.DeployedBundle, as kardinal status reports). Environments
	// that never received one are absent.
	Deployed map[string]uiDeployed `json:"deployed,omitempty"`
}

// uiDeployed is what one environment runs, for the fleet board.
type uiDeployed struct {
	Bundle string `json:"bundle"`
	// Version is scm.BundleVersion of that Bundle; empty when the Bundle is
	// gone.
	Version string `json:"version,omitempty"`
	// VerifiedAt is the RFC 3339 time its step in the environment was
	// Verified (the latest region); empty while it is still health checking.
	VerifiedAt string `json:"verifiedAt,omitempty"`
	// ConfigFrom, under an image Bundle, is the last Bundle that deployed a
	// config commit there, and ConfigVersion that commit ("config abc1234").
	// ImagesFrom, under a config Bundle, is the last Bundle that deployed
	// images, and ImagesVersion their tags. Image and config Bundles do not
	// supersede each other, so the environment runs both (#1353), as
	// kardinal status reports.
	ConfigFrom    string `json:"configFrom,omitempty"`
	ConfigVersion string `json:"configVersion,omitempty"`
	ImagesFrom    string `json:"imagesFrom,omitempty"`
	ImagesVersion string `json:"imagesVersion,omitempty"`
}

// pick returns pointers to the steps at idx.
func pick(steps []v1alpha1.PromotionStep, idx []int) []*v1alpha1.PromotionStep {
	out := make([]*v1alpha1.PromotionStep, len(idx))
	for j, i := range idx {
		out[j] = &steps[i]
	}
	return out
}

// stepsByEnv groups the steps at idx (one Pipeline's) by environment, as
// pointers into steps.
func stepsByEnv(steps []v1alpha1.PromotionStep, idx []int) map[string][]*v1alpha1.PromotionStep {
	out := map[string][]*v1alpha1.PromotionStep{}
	for _, i := range idx {
		env := steps[i].Spec.Environment
		out[env] = append(out[env], &steps[i])
	}
	return out
}

// deployedByEnv returns, per environment of p, the Bundles it runs. byEnv
// holds p's steps by environment (stepsByEnv), so the cost is linear in the
// steps.
func deployedByEnv(p *v1alpha1.Pipeline, byEnv map[string][]*v1alpha1.PromotionStep, bundles map[string]*v1alpha1.Bundle) map[string]uiDeployed {
	out := make(map[string]uiDeployed, len(p.Spec.Environments))
	// Most environments run the same few Bundles: name each version once.
	versions := map[string]string{}
	version := func(name string, spec v1alpha1.BundleSpec) string {
		v, ok := versions[name]
		if !ok {
			v = scm.BundleVersion(spec)
			versions[name] = v
		}
		return v
	}
	for _, env := range p.Spec.Environments {
		envSteps := byEnv[env.Name]
		dep := lifecycle.DeployedInSteps(envSteps, p.Name, env.Name, bundles)
		if dep.Bundle == "" {
			continue
		}
		d := uiDeployed{Bundle: dep.Bundle}
		if b := bundles[dep.Bundle]; b != nil {
			d.Version = version(dep.Bundle, b.Spec)
		}
		if b := bundles[dep.ConfigFrom]; b != nil {
			d.ConfigFrom = dep.ConfigFrom
			d.ConfigVersion = version("config/"+dep.ConfigFrom, v1alpha1.BundleSpec{Type: "config", ConfigRef: b.Spec.ConfigRef})
		}
		if b := bundles[dep.ImagesFrom]; b != nil {
			d.ImagesFrom = dep.ImagesFrom
			d.ImagesVersion = version("images/"+dep.ImagesFrom, v1alpha1.BundleSpec{Type: "image", Images: b.Spec.Images})
		}
		var at time.Time
		for _, s := range envSteps {
			if s.Spec.BundleName != dep.Bundle {
				continue
			}
			if t, ok := lifecycle.VerifiedTime(s); ok && t.After(at) {
				at = t
			}
		}
		if !at.IsZero() {
			d.VerifiedAt = at.UTC().Format(time.RFC3339)
		}
		out[env.Name] = d
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
	// Upstreams are the environments this one waits for as the controller
	// resolves them (dependsOn, waves, or the previous entry), sorted; empty
	// for a root. Absent when the Pipeline's ordering is invalid.
	Upstreams []string `json:"upstreams,omitempty"`
	// Hold is the environment's hold (spec.holds, kardinal rollback --hold),
	// nil when it is not held.
	Hold *uiHoldResponse `json:"hold,omitempty"`
}

// uiHoldResponse is a Pipeline environment hold (spec.holds).
type uiHoldResponse struct {
	// Bundle is the rollback Bundle the environment is held on.
	Bundle string `json:"bundle"`
	// Reason says why.
	Reason string `json:"reason"`
	// CreatedBy is who held the environment.
	CreatedBy string `json:"createdBy,omitempty"`
	// CreatedAt is when, RFC 3339.
	CreatedAt string `json:"createdAt,omitempty"`
	// ExpiresAt is when the controller removes the hold, RFC 3339; empty:
	// when it is released.
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// holdResponse is the UI shape of the hold of env in p, or nil.
func holdResponse(p *v1alpha1.Pipeline, env string) *uiHoldResponse {
	h := lifecycle.HoldOf(p, env)
	if h == nil {
		return nil
	}
	out := &uiHoldResponse{Bundle: h.Bundle, Reason: h.Reason, CreatedBy: h.CreatedBy}
	if h.CreatedAt != nil {
		out.CreatedAt = h.CreatedAt.UTC().Format(time.RFC3339)
	}
	if h.ExpiresAt != nil {
		out.ExpiresAt = h.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
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
	// #503: Per-environment statuses. The bundle comparison shows them and the
	// release metrics bar reads HealthCheckedAt; the timeline does not.
	Environments []uiBundleEnvStatus `json:"environments,omitempty"`
	// #563: Container images in this Bundle — used by NodeDetail diff preview.
	Images []v1alpha1.ImageRef `json:"images,omitempty"`
	// RejectedLiveEnvironments are the environments where this Rejected
	// Bundle's change is live (lifecycle.RejectedLiveEnvs): the UI keeps it
	// current there, marked Rejected, with a roll-back hint.
	RejectedLiveEnvironments []string `json:"rejectedLiveEnvironments,omitempty"`
	// Rejected is spec.rejected: who rejected the Bundle (kardinal reject),
	// why and when. Absent when it is not rejected.
	Rejected *uiBundleRejection `json:"rejected,omitempty"`
}

// uiBundleEnvStatus is the per-environment status summary of a Bundle (#503).
// The UI reads Name, Phase and HealthCheckedAt; nothing in it reads PRURL.
type uiBundleEnvStatus struct {
	Name  string `json:"name"`
	Phase string `json:"phase,omitempty"`
	PRURL string `json:"prURL,omitempty"`
	// HealthCheckedAt is when the post-merge health check for this environment
	// completed (RFC 3339). The release metrics bar uses it for time-to-production.
	HealthCheckedAt string `json:"healthCheckedAt,omitempty"`
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
	// Holding is set on a PolicyGate node whose gate holds the bundle back
	// (graph.GateHolds, the rule blockerCount uses); its State is "Block".
	// See graph.GateState for the other gate states.
	Holding bool `json:"holding,omitempty"`
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
	// #501: Bake countdown fields (served; the UI does not show them yet).
	// BakeElapsedMinutes is contiguous healthy minutes so far in the current bake window.
	BakeElapsedMinutes int64 `json:"bakeElapsedMinutes,omitempty"`
	// BakeTargetMinutes is the required contiguous healthy duration from Pipeline spec.
	// Zero means no bake is configured for this environment.
	BakeTargetMinutes int `json:"bakeTargetMinutes,omitempty"`
	// BakeResets is the number of times the bake timer was reset due to a health alarm.
	BakeResets int `json:"bakeResets,omitempty"`
	// Steps is the per-step progress from status.steps[], in execution order.
	// The UI renders it as the step progress list.
	Steps []uiStepStatus `json:"steps,omitempty"`
}

// uiStepStatus is the JSON shape for one entry of PromotionStep status.steps[].
type uiStepStatus struct {
	Name        string `json:"name"`
	State       string `json:"state"` // Pending, InProgress, Completed or Failed
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
	Message     string `json:"message,omitempty"`
}

// uiCondition is the JSON shape for a Kubernetes condition.
type uiCondition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
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
	// Pipeline, Bundle and Environment come from the kardinal.io/* labels the
	// graph builder sets on gate instances. They are empty on templates.
	Pipeline    string `json:"pipeline,omitempty"`
	Bundle      string `json:"bundle,omitempty"`
	Environment string `json:"environment,omitempty"`
	// Template is true for a PolicyGate template (no kardinal.io/bundle label).
	// Templates are never evaluated for a bundle and always report ready=false,
	// so the UI must not count them as blocking.
	Template bool `json:"template,omitempty"`
	// Holding is true when this gate instance holds its bundle back
	// (graph.GateHolds, the rule blockerCount uses). A not-ready gate that is
	// not holding is waiting for the bundle, not blocking it.
	Holding bool `json:"holding,omitempty"`
	// State is the state the UI shows: Pass, Block, Superseded, Pending or
	// Waiting (graph.GateState), the same as the gate's node in the bundle
	// graph and the STATE kardinal explain prints.
	State string `json:"state"`
	// #502: Override history from spec.overrides[].
	Overrides []uiGateOverride `json:"overrides,omitempty"`
	// Approval is the gate's approval quorum and who approved (an approval
	// gate, spec.approval). Absent for a gate without one.
	Approval *uiGateApproval `json:"approval,omitempty"`
}

// uiBundleRejection is spec.rejected of a Bundle.
type uiBundleRejection struct {
	Reason string `json:"reason"`
	By     string `json:"by"`
	At     string `json:"at,omitempty"`
}

// uiGateApproval is a gate's approval policy (spec.approval) and how the
// gate counted the decisions it has (status.approvals).
type uiGateApproval struct {
	// Required is how many distinct allowed people must approve (default 1).
	Required      int      `json:"required"`
	AllowedUsers  []string `json:"allowedUsers,omitempty"`
	AllowedGroups []string `json:"allowedGroups,omitempty"`
	ExcludeAuthor bool     `json:"excludeAuthor,omitempty"`
	// Approved is the number of counted approve decisions; Rejected is true
	// when a counted reject blocks the gate.
	Approved int  `json:"approved"`
	Rejected bool `json:"rejected,omitempty"`
	// Decisions are status.approvals, in the order the gate recorded them.
	Decisions []uiGateDecision `json:"decisions,omitempty"`
}

// uiGateDecision is one approve or reject decision as the gate counted it.
type uiGateDecision struct {
	User        string `json:"user"`
	Decision    string `json:"decision"`
	Counted     bool   `json:"counted"`
	Reason      string `json:"reason,omitempty"`
	Comment     string `json:"comment,omitempty"`
	FirstSeenAt string `json:"firstSeenAt,omitempty"`
}

// gateApproval is the uiGateApproval of g, or nil without spec.approval.
func gateApproval(g *v1alpha1.PolicyGate) *uiGateApproval {
	pol := g.Spec.Approval
	if pol == nil {
		return nil
	}
	out := &uiGateApproval{Required: max(pol.Required, 1), AllowedUsers: pol.AllowedUsers,
		AllowedGroups: pol.AllowedGroups, ExcludeAuthor: pol.ExcludeAuthor}
	for _, a := range g.Status.Approvals {
		d := uiGateDecision{User: a.User, Decision: a.Decision, Counted: a.Counted, Reason: a.Reason, Comment: a.Comment}
		if a.FirstSeenAt != nil {
			d.FirstSeenAt = a.FirstSeenAt.UTC().Format(time.RFC3339)
		}
		if a.Counted {
			switch a.Decision {
			case "approve":
				out.Approved++
			case "reject":
				out.Rejected = true
			}
		}
		out.Decisions = append(out.Decisions, d)
	}
	return out
}

// uiGateOverride is the JSON shape for a PolicyGateOverride (K-09 audit record).
type uiGateOverride struct {
	Reason    string `json:"reason"`
	Stage     string `json:"stage,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	CreatedBy string `json:"createdBy,omitempty"`
	// CreatedByVerified is true when the chart's identity admission policy
	// checked createdBy (status.overrides): false for an override recorded
	// while the policy was not bound, or before the upgrade that added it.
	CreatedByVerified bool `json:"createdByVerified"`
}

// uiAPIServer serves the REST API for the embedded UI.
type uiAPIServer struct {
	client client.Client
	log    zerolog.Logger
	// upstreams caches each Pipeline's resolved environment upstreams by
	// generation: the list is polled every few seconds and a Pipeline's
	// ordering changes only with its spec.
	upstreams upstreamCache
}

// upstreamCache memoizes graph.AllEnvironmentUpstreams per Pipeline UID and
// generation. It is a read-side cache of a pure function of the spec, so a
// stale or empty cache only costs time. Entries of Pipelines absent from a
// list are dropped (prune).
type upstreamCache struct {
	mu      sync.Mutex
	entries map[types.UID]upstreamEntry
}

type upstreamEntry struct {
	generation int64
	ups        map[string][]string
	err        error
}

// get returns the upstreams of p, computing them on a miss. A nil cache
// computes every time.
func (c *upstreamCache) get(p *v1alpha1.Pipeline) (map[string][]string, error) {
	if c == nil || p.UID == "" {
		return graphpkg.AllEnvironmentUpstreams(p)
	}
	c.mu.Lock()
	e, ok := c.entries[p.UID]
	c.mu.Unlock()
	if ok && e.generation == p.Generation {
		return e.ups, e.err
	}
	ups, err := graphpkg.AllEnvironmentUpstreams(p)
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[types.UID]upstreamEntry{}
	}
	c.entries[p.UID] = upstreamEntry{generation: p.Generation, ups: ups, err: err}
	c.mu.Unlock()
	return ups, err
}

// prune drops the entries of Pipelines not in pipelines.
func (c *upstreamCache) prune(pipelines []v1alpha1.Pipeline) {
	if c == nil {
		return
	}
	live := make(map[types.UID]bool, len(pipelines))
	for i := range pipelines {
		live[pipelines[i].UID] = true
	}
	c.mu.Lock()
	for uid := range c.entries {
		if !live[uid] {
			delete(c.entries, uid)
		}
	}
	c.mu.Unlock()
}

func newUIAPIServer(k8s client.Client, log zerolog.Logger) *uiAPIServer {
	return &uiAPIServer{client: k8s, log: log}
}

// RegisterRoutes registers all /api/v1/ui/* routes on the given mux.
func (s *uiAPIServer) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/ui/pipelines", s.handlePipelines)
	mux.HandleFunc("/api/v1/ui/pipelines/", s.handlePipelinesSubpath)
	mux.HandleFunc("/api/v1/ui/bundles", s.handleBundles)
	mux.HandleFunc("/api/v1/ui/bundles/", s.handleBundleSubresource)
	mux.HandleFunc("/api/v1/ui/gates", s.handleGates)
	mux.HandleFunc("/api/v1/ui/gates/", s.handleGatesSubpath)
	mux.HandleFunc("/api/v1/ui/promote", s.handlePromote)
	mux.HandleFunc("/api/v1/ui/rollback", s.handleRollback)
	mux.HandleFunc("/api/v1/ui/release-hold", s.handleReleaseHold)
	mux.HandleFunc("/api/v1/ui/approvals", s.handleApproval)
	mux.HandleFunc("/api/v1/ui/pause", s.handlePause)
	mux.HandleFunc("/api/v1/ui/resume", s.handleResume)
	mux.HandleFunc("/api/v1/ui/validate-cel", s.handleValidateCEL)
	mux.HandleFunc("/api/v1/ui/steps/", s.handleStepsSubpath)
	mux.HandleFunc(openAPIPath, handleOpenAPI)
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

	var bundleList v1alpha1.BundleList
	if err := s.client.List(r.Context(), &bundleList); err != nil {
		s.log.Error().Err(err).Msg("ui: list bundles")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var stepList v1alpha1.PromotionStepList
	if err := s.client.List(r.Context(), &stepList); err != nil {
		s.log.Error().Err(err).Msg("ui: list promotion steps")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var gateList v1alpha1.PolicyGateList
	if err := s.client.List(r.Context(), &gateList); err != nil {
		s.log.Error().Err(err).Msg("ui: list policy gates")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Retired Bundles (#1492) keep their steps in status.retiredSteps.
	steps := lifecycle.AddRetiredSteps(stepList.Items, bundleList.Items, nil)
	writeJSON(w, pipelineListResponse(list.Items, bundleList.Items, steps, gateList.Items, time.Now().UTC(), &s.upstreams))
	s.upstreams.prune(list.Items)
}

// pipelineListResponse builds GET /api/v1/ui/pipelines from the listed
// objects. It indexes them once, so its cost is linear in the objects
// (BenchmarkPipelineListResponse).
func pipelineListResponse(pipelines []v1alpha1.Pipeline, bundles []v1alpha1.Bundle, steps []v1alpha1.PromotionStep,
	gates []v1alpha1.PolicyGate, now time.Time, cache *upstreamCache) []uiPipelineResponse {
	// Build per-pipeline active bundle index (#342): the pipeline's current
	// bundle, whose per-environment states feed the health bar and whose steps
	// and gates feed the ops counts.
	// Index: namespace/pipeline → current bundle (lifecycle.CurrentBundle): the
	// newest non-Superseded bundle (lifecycle.CompareCreation), whatever its
	// phase, so a newer Failed bundle is never hidden behind an older Verified
	// or Promoting one (E2E-R15). When every bundle is Superseded, the newest
	// one is used. kardinal get pipelines and web/src/bundleSelection.ts
	// pickDefaultBundle apply the same rule.
	type activeBundleEntry struct {
		bundle       *v1alpha1.Bundle
		name         string
		envStates    map[string]string
		createdAt    time.Time
		lastVerified time.Time // most recent HealthCheckedAt across all envs in this bundle
	}
	bundlesByPipeline := make(map[string][]v1alpha1.Bundle)
	for _, b := range bundles {
		if b.Spec.Pipeline != "" {
			key := b.Namespace + "/" + b.Spec.Pipeline
			bundlesByPipeline[key] = append(bundlesByPipeline[key], b)
		}
	}
	// The steps also pick the current bundle: a Rejected bundle whose change
	// is live stays current (lifecycle.CurrentBundle).
	activeBundles := make(map[string]*activeBundleEntry, len(bundlesByPipeline))
	for key, bundles := range bundlesByPipeline {
		b := lifecycle.CurrentBundle(bundles, steps)
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
			bundle:       b,
			name:         b.Name,
			envStates:    envStates,
			createdAt:    b.CreationTimestamp.Time,
			lastVerified: lastVerified,
		}
	}

	// PromotionSteps feed the failed step count and, with the gates, the
	// blocker count (ops table #462). Index both by namespace/bundle.
	// Indexes hold positions in steps, not copies: a PromotionStep is large,
	// and copying 100 000 of them several times dominated the request.
	// Keys are namespace/name pairs, not concatenated strings.
	type nsName struct{ ns, name string }
	failedStepsByBundle := make(map[nsName]int)
	stepsByBundle := make(map[nsName][]int, len(bundles))
	stepsByPipeline := make(map[nsName][]int, len(pipelines))
	for i := range steps {
		ps := &steps[i]
		key := nsName{ps.Namespace, ps.Spec.BundleName}
		stepsByBundle[key] = append(stepsByBundle[key], i)
		pk := nsName{ps.Namespace, ps.Spec.PipelineName}
		stepsByPipeline[pk] = append(stepsByPipeline[pk], i)
		// AbortedByAlarm is a failure too: the health alarm stopped the promotion.
		if ps.Status.State == "Failed" || ps.Status.State == "AbortedByAlarm" {
			failedStepsByBundle[key]++
		}
	}

	// Index: namespace/bundle → the bundle's gate instances that are not ready.
	notReadyByBundle := make(map[string][]v1alpha1.PolicyGate)
	for _, g := range gates {
		if bundleLabel := g.Labels["kardinal.io/bundle"]; bundleLabel != "" && !g.Status.Ready {
			key := g.Namespace + "/" + bundleLabel
			notReadyByBundle[key] = append(notReadyByBundle[key], g)
		}
	}

	result := make([]uiPipelineResponse, 0, len(pipelines))
	for _, p := range pipelines {
		key := fmt.Sprintf("%s/%s", p.Namespace, p.Name)
		resp := uiPipelineResponse{
			Name:             p.Name,
			Namespace:        p.Namespace,
			Phase:            pipelinePhase(&p),
			EnvironmentCount: len(p.Spec.Environments),
			Paused:           p.Spec.Paused,
		}
		// #525: build static environment topology from Pipeline.Spec so the UI can render
		// the DAG even when no Bundle is actively promoting.
		if len(p.Spec.Environments) > 0 {
			topo := make([]uiEnvironmentNode, 0, len(p.Spec.Environments))
			// One ordering resolution per Pipeline: per environment it was
			// cubic in the environment count.
			upstreams, upErr := cache.get(&p)
			for _, env := range p.Spec.Environments {
				node := uiEnvironmentNode{
					Name:      env.Name,
					DependsOn: env.DependsOn,
					Approval:  env.Approval,
					Hold:      holdResponse(&p, env.Name),
				}
				if upErr == nil && len(upstreams[env.Name]) > 0 {
					node.Upstreams = upstreams[env.Name]
				}
				topo = append(topo, node)
			}
			resp.EnvironmentTopology = topo
		}
		byName := make(map[string]*v1alpha1.Bundle, len(bundlesByPipeline[key]))
		for i := range bundlesByPipeline[key] {
			byName[bundlesByPipeline[key][i].Name] = &bundlesByPipeline[key][i]
		}
		resp.Deployed = deployedByEnv(&p, stepsByEnv(steps, stepsByPipeline[nsName{p.Namespace, p.Name}]), byName)
		resp.DeploymentMetrics = p.Status.DeploymentMetrics
		if ab := activeBundles[key]; ab != nil {
			resp.ActiveBundleName = ab.name
			resp.ActiveBundleVersion = scm.BundleVersion(ab.bundle.Spec)
			if len(ab.envStates) > 0 {
				resp.EnvironmentStates = ab.envStates
			}
			// Ops table: blocker + failed step counts derived from active bundle.
			bundleKey := p.Namespace + "/" + ab.name
			if n := blockingGateCount(&p, ab.bundle, notReadyByBundle[bundleKey], pick(steps, stepsByBundle[nsName{p.Namespace, ab.name}])); n > 0 {
				resp.BlockerCount = n
			}
			if n := failedStepsByBundle[nsName{p.Namespace, ab.name}]; n > 0 {
				resp.FailedStepCount = n
			}
			// Inventory age: days since the active bundle was created.
			if !ab.createdAt.IsZero() {
				days := int(now.Sub(ab.createdAt).Hours() / 24)
				resp.InventoryAgeDays = &days
			}
			// Last merged at: most recent HealthCheckedAt across all envs.
			if !ab.lastVerified.IsZero() {
				resp.LastMergedAt = ab.lastVerified.UTC().Format(time.RFC3339)
			}
		}
		result = append(result, resp)
	}
	return result
}

// blockingGateCount counts the not-ready gate instances of bundle that hold it
// back (E2E-R18), graph.GateHolds: the gate's environment has no step of
// bundle yet and every upstream environment is Verified, or a Pending step
// there waits on the gate. A gate of an environment the
// bundle has not reached is not the reason it is waiting, so it is not
// counted, and neither is a gate of a Failed or Superseded bundle. kardinal
// status lists the same gates as Blocking Policy Gates.
func blockingGateCount(p *v1alpha1.Pipeline, bundle *v1alpha1.Bundle, notReady []v1alpha1.PolicyGate,
	steps []*v1alpha1.PromotionStep) int {
	n := 0
	for i := range notReady {
		if graphpkg.GateHoldsSteps(p, bundle, &notReady[i], steps) {
			n++
		}
	}
	return n
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

// handleBundlesForPipeline lists the Bundles of one pipeline, newest first
// (creationTimestamp descending, then name descending), so every call returns
// the same order. The optional ?namespace= query parameter keeps same-named
// pipelines in other namespaces out of the result.
func (s *uiAPIServer) handleBundlesForPipeline(w http.ResponseWriter, r *http.Request, pipelineName string) {
	var list v1alpha1.BundleList
	if err := s.client.List(r.Context(), &list); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	namespace := r.URL.Query().Get("namespace")
	items := make([]v1alpha1.Bundle, 0, len(list.Items))
	for _, b := range list.Items {
		if b.Spec.Pipeline != pipelineName {
			continue
		}
		if namespace != "" && b.Namespace != namespace {
			continue
		}
		items = append(items, b)
	}
	sort.SliceStable(items, func(i, j int) bool {
		ti, tj := items[i].CreationTimestamp.Time, items[j].CreationTimestamp.Time
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return items[i].Name > items[j].Name
	})
	// The steps say where a Rejected Bundle's change is live; they are listed
	// only when a Rejected Bundle is in the result.
	var steps []v1alpha1.PromotionStep
	for i := range items {
		if lifecycle.Rejected(&items[i]) {
			var stepList v1alpha1.PromotionStepList
			if err := s.client.List(r.Context(), &stepList,
				client.MatchingLabels{"kardinal.io/pipeline": pipelineName}); err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			// A retired Bundle (#1492) keeps its steps in status.retiredSteps.
			steps = lifecycle.AddRetiredSteps(stepList.Items, items,
				map[string]string{"kardinal.io/pipeline": pipelineName})
			break
		}
	}
	result := make([]uiBundleResponse, 0, len(items))
	for _, b := range items {
		// #503: Per-environment statuses.
		envStatuses := make([]uiBundleEnvStatus, 0, len(b.Status.Environments))
		for _, env := range b.Status.Environments {
			es := uiBundleEnvStatus{
				Name:  env.Name,
				Phase: env.Phase,
				PRURL: env.PRURL,
			}
			if env.HealthCheckedAt != nil {
				es.HealthCheckedAt = env.HealthCheckedAt.UTC().Format(time.RFC3339)
			}
			envStatuses = append(envStatuses, es)
		}
		resp := uiBundleResponse{
			Name:       b.Name,
			Namespace:  b.Namespace,
			Phase:      b.Status.Phase,
			Type:       b.Spec.Type,
			Pipeline:   b.Spec.Pipeline,
			CreatedAt:  b.CreationTimestamp.Format("2006-01-02T15:04:05Z07:00"),
			Provenance: b.Spec.Provenance,
			Images:     b.Spec.Images, // #563: expose images for diff preview
		}
		if len(envStatuses) > 0 {
			resp.Environments = envStatuses
		}
		resp.RejectedLiveEnvironments = lifecycle.RejectedLiveEnvs(&b, steps)
		if rj := b.Spec.Rejected; rj != nil {
			resp.Rejected = &uiBundleRejection{Reason: rj.Reason, By: rj.By}
			if rj.At != nil {
				resp.Rejected.At = rj.At.UTC().Format(time.RFC3339)
			}
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

// findBundle returns the Bundle called name and the namespace its steps and
// gates live in. namespace (the optional ?namespace= query parameter) narrows
// the lookup when bundle names repeat across namespaces. When no Bundle
// matches, the Bundle is nil and the namespace is returned as given.
func (s *uiAPIServer) findBundle(ctx context.Context, name, namespace string) (*v1alpha1.Bundle, string, error) {
	var bl v1alpha1.BundleList
	if err := s.client.List(ctx, &bl, client.InNamespace(namespace)); err != nil {
		return nil, "", fmt.Errorf("list bundles: %w", err)
	}
	for i := range bl.Items {
		if bl.Items[i].Name == name {
			return &bl.Items[i], bl.Items[i].Namespace, nil
		}
	}
	return nil, namespace, nil
}

// handleBundleGraph builds the DAG for a single Bundle:
//   - one PromotionStep node per environment (synthetic "NotStarted" when the
//     step does not exist yet);
//   - one PolicyGate node per gate and environment it guards, in the state
//     graph.GateState gives it (Pass, Block, Superseded, Pending or Waiting);
//   - edges that follow the Pipeline's dependencies (the Graph builder's
//     rules: waves, dependsOn, else the previous environment), with each
//     environment's gates between its upstream steps and its own step.
//
// Steps and gates are read from the Bundle's namespace. The optional
// ?namespace= query parameter picks the Bundle when names repeat across
// namespaces.
func (s *uiAPIServer) handleBundleGraph(w http.ResponseWriter, r *http.Request, bundleName string) {
	ctx := r.Context()
	fail := func(err error, what string) {
		s.log.Error().Err(err).Str("bundle", bundleName).Msg("ui: bundle graph: " + what)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}

	// 1. Find the Bundle; its namespace scopes the steps, gates and Pipeline.
	bundle, namespace, err := s.findBundle(ctx, bundleName, r.URL.Query().Get("namespace"))
	if err != nil {
		fail(err, "list bundles")
		return
	}
	byBundle := client.MatchingLabels{"kardinal.io/bundle": bundleName}
	var psList v1alpha1.PromotionStepList
	if err := s.client.List(ctx, &psList, client.InNamespace(namespace), byBundle); err != nil {
		fail(err, "list promotion steps")
		return
	}
	if bundle != nil {
		// A retired Bundle (#1492) keeps its steps in status.retiredSteps.
		psList.Items = lifecycle.AddRetiredSteps(psList.Items, []v1alpha1.Bundle{*bundle}, byBundle)
	}
	var gateList v1alpha1.PolicyGateList
	if err := s.client.List(ctx, &gateList, client.InNamespace(namespace), byBundle); err != nil {
		fail(err, "list policy gates")
		return
	}

	// 2. Environment order and dependencies from the Pipeline.
	var envOrder []string
	var deps map[string][]string
	var pipeline *v1alpha1.Pipeline
	if bundle != nil && bundle.Spec.Pipeline != "" {
		var pl v1alpha1.Pipeline
		err := s.client.Get(ctx, client.ObjectKey{Name: bundle.Spec.Pipeline, Namespace: bundle.Namespace}, &pl)
		switch {
		case err == nil:
			pipeline = &pl
			envOrder, deps = pipelineEnvDeps(&pl)
		case !apierrors.IsNotFound(err):
			fail(err, "get pipeline")
			return
		}
	}
	// Fallback when the Pipeline is gone: a chain in step order.
	if len(envOrder) == 0 {
		seen := map[string]bool{}
		for _, ps := range psList.Items {
			if !seen[ps.Spec.Environment] {
				envOrder = append(envOrder, ps.Spec.Environment)
				seen[ps.Spec.Environment] = true
			}
		}
		deps = linearEnvDeps(envOrder)
	}

	// 3. PromotionStep lookup by environment.
	stepByEnv := map[string]*v1alpha1.PromotionStep{}
	for i := range psList.Items {
		ps := &psList.Items[i]
		stepByEnv[ps.Spec.Environment] = ps
	}

	// 4. Gates keyed by gate name and environment. The kardinal.io/gate-name
	//    label holds the user-defined gate name (e.g. "no-weekend-deploys") and
	//    is carried through cross-product instances by the graph builder. When
	//    several instances share a name and environment, prefer the one whose
	//    own name equals the gate name, else the lexicographically first.
	type gateKey struct{ name, env string }
	gates := map[gateKey]*v1alpha1.PolicyGate{}
	for i := range gateList.Items {
		g := &gateList.Items[i]
		name := g.Labels["kardinal.io/gate-name"]
		if name == "" {
			name = g.Labels["kardinal.io/gate-template"]
		}
		if name == "" {
			name = g.Name
		}
		env := g.Labels["kardinal.io/environment"]
		if env == "" {
			env = g.Labels["kardinal.io/applies-to"]
		}
		if env == "" {
			continue
		}
		k := gateKey{name: name, env: env}
		prev, exists := gates[k]
		switch {
		case !exists:
			gates[k] = g
		case prev.Name == name:
		case g.Name == name || g.Name < prev.Name:
			gates[k] = g
		}
	}
	gatesByEnv := map[string][]string{} // env → sorted gate names
	for k := range gates {
		gatesByEnv[k.env] = append(gatesByEnv[k.env], k.name)
	}
	for env := range gatesByEnv {
		sort.Strings(gatesByEnv[env])
	}

	// 5. Nodes: one step per environment, then its gates.
	nodes := make([]uiGraphNode, 0, len(envOrder)+len(gates))
	edges := make([]uiGraphEdge, 0)
	stepIDs := make(map[string]string, len(envOrder))
	for _, env := range envOrder {
		stepIDs[env] = "step-" + env
		if ps, ok := stepByEnv[env]; ok {
			stepIDs[env] = ps.Name
		}
	}
	for _, env := range envOrder {
		stepID := stepIDs[env]
		stepNode := uiGraphNode{
			ID:          stepID,
			Type:        "PromotionStep",
			Label:       env,
			Environment: env,
			State:       "NotStarted",
		}
		if ps, ok := stepByEnv[env]; ok {
			if ps.Status.State != "" {
				stepNode.State = ps.Status.State
			}
			stepNode.Message = ps.Status.Message
			stepNode.PRURL = ps.Status.PRURL
			stepNode.Outputs = ps.Status.Outputs
			if !ps.CreationTimestamp.IsZero() {
				stepNode.StartedAt = ps.CreationTimestamp.Format("2006-01-02T15:04:05Z07:00")
			}
		}
		nodes = append(nodes, stepNode)

		// Entry points of this environment: its gates, or the step itself.
		entries := []string{stepID}
		if names := gatesByEnv[env]; len(names) > 0 {
			entries = entries[:0]
			for _, name := range names {
				g := gates[gateKey{name: name, env: env}]
				gateID := "gate-" + g.Name
				// The Bundle may be gone while its steps and gates remain, and
				// without its Pipeline the rule cannot run; nothing holds then.
				state := graphpkg.GateState(pipeline, bundle, g, psList.Items)
				holding := state == graphpkg.GateStateBlock
				lastEval := ""
				if g.Status.LastEvaluatedAt != nil {
					lastEval = g.Status.LastEvaluatedAt.Format("2006-01-02T15:04:05Z07:00")
				}
				nodes = append(nodes, uiGraphNode{
					ID:              gateID,
					Type:            "PolicyGate",
					Label:           name,
					Environment:     env,
					State:           state,
					Message:         g.Status.Reason,
					Expression:      g.Spec.Expression,
					LastEvaluatedAt: lastEval,
					Holding:         holding,
				})
				edges = append(edges, uiGraphEdge{From: gateID, To: stepID})
				entries = append(entries, gateID)
			}
		}
		for _, dep := range deps[env] {
			from, ok := stepIDs[dep]
			if !ok {
				continue
			}
			for _, to := range entries {
				edges = append(edges, uiGraphEdge{From: from, To: to})
			}
		}
	}

	writeJSON(w, uiGraphResponse{Nodes: nodes, Edges: edges})
}

// pipelineEnvDeps returns the Pipeline's environments in execution order and
// the environments each one waits for, using the Graph builder's rules. An
// invalid topology (the Pipeline is Ready=False for a cycle) falls back to a
// chain in list order so the UI still renders.
func pipelineEnvDeps(pl *v1alpha1.Pipeline) ([]string, map[string][]string) {
	order, orderErr := graphpkg.EnvironmentOrder(pl)
	deps, depsErr := graphpkg.EnvironmentDependencies(pl)
	if orderErr == nil && depsErr == nil {
		return order, deps
	}
	order = make([]string, 0, len(pl.Spec.Environments))
	for _, e := range pl.Spec.Environments {
		order = append(order, e.Name)
	}
	return order, linearEnvDeps(order)
}

// linearEnvDeps makes each environment depend on the one before it.
func linearEnvDeps(order []string) map[string][]string {
	deps := make(map[string][]string, len(order))
	for i := 1; i < len(order); i++ {
		deps[order[i]] = []string{order[i-1]}
	}
	return deps
}

// handleBundleSteps handles GET /api/v1/ui/bundles/{name}/steps[?namespace=].
// Steps are read from the Bundle's namespace only (see findBundle).
func (s *uiAPIServer) handleBundleSteps(w http.ResponseWriter, r *http.Request, bundleName string) {
	bundle, namespace, err := s.findBundle(r.Context(), bundleName, r.URL.Query().Get("namespace"))
	if err != nil {
		s.log.Error().Err(err).Str("bundle", bundleName).Msg("ui: bundle steps: list bundles")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var list v1alpha1.PromotionStepList
	if err := s.client.List(r.Context(), &list, client.InNamespace(namespace)); err != nil {
		s.log.Error().Err(err).Str("bundle", bundleName).Msg("ui: bundle steps: list promotion steps")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if bundle != nil {
		// A retired Bundle (#1492) keeps its steps in status.retiredSteps.
		list.Items = lifecycle.AddRetiredSteps(list.Items, []v1alpha1.Bundle{*bundle},
			map[string]string{lifecycle.LabelBundle: bundleName})
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
			Steps:              buildUIStepStatuses(ps.Status.Steps),
		})
	}
	writeJSON(w, result)
}

// buildUIStepStatuses converts status.steps[] to the UI shape.
func buildUIStepStatuses(steps []v1alpha1.StepStatus) []uiStepStatus {
	if len(steps) == 0 {
		return nil
	}
	out := make([]uiStepStatus, 0, len(steps))
	for _, st := range steps {
		u := uiStepStatus{
			Name:       st.Name,
			State:      string(st.State),
			DurationMs: st.DurationMs,
			Message:    st.Message,
		}
		if st.StartedAt != nil {
			u.StartedAt = st.StartedAt.UTC().Format(time.RFC3339)
		}
		if st.CompletedAt != nil {
			u.CompletedAt = st.CompletedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, u)
	}
	return out
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
	states, err := s.gateStates(r.Context(), list.Items)
	if err != nil {
		s.log.Error().Err(err).Msg("ui: gates: state")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	result := make([]uiGateResponse, 0, len(list.Items))
	for _, g := range list.Items {
		state := states[g.Namespace+"/"+g.Name]
		resp := uiGateResponse{
			Name:        g.Name,
			Namespace:   g.Namespace,
			Expression:  g.Spec.Expression,
			Ready:       g.Status.Ready,
			Reason:      g.Status.Reason,
			Pipeline:    g.Labels["kardinal.io/pipeline"],
			Bundle:      g.Labels["kardinal.io/bundle"],
			Environment: g.Labels["kardinal.io/environment"],
			Template:    g.Labels["kardinal.io/bundle"] == "",
			Holding:     state == graphpkg.GateStateBlock,
			State:       state,
			Approval:    gateApproval(&g),
		}
		if g.Status.LastEvaluatedAt != nil {
			resp.LastEvaluatedAt = g.Status.LastEvaluatedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		// #502: Populate override history from spec.overrides[].
		for i := range g.Spec.Overrides {
			ov := &g.Spec.Overrides[i]
			o := uiGateOverride{
				Reason:            ov.Reason,
				Stage:             ov.Stage,
				CreatedBy:         ov.CreatedBy,
				CreatedByVerified: policygate.OverrideVerified(&g, ov),
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

// gateStates returns, by namespace/name, the state of each gate in gates
// (graph.GateState). It reads the Pipelines, Bundles and PromotionSteps only
// when a gate instance is not ready: only such a gate can hold its bundle or
// belong to a Superseded one.
func (s *uiAPIServer) gateStates(ctx context.Context, gates []v1alpha1.PolicyGate) (map[string]string, error) {
	out := make(map[string]string, len(gates))
	if !slices.ContainsFunc(gates, func(g v1alpha1.PolicyGate) bool {
		return !g.Status.Ready && g.Labels["kardinal.io/bundle"] != ""
	}) {
		for i := range gates {
			g := &gates[i]
			out[g.Namespace+"/"+g.Name] = graphpkg.GateState(nil, nil, g, nil)
		}
		return out, nil
	}
	var pipelines v1alpha1.PipelineList
	if err := s.client.List(ctx, &pipelines); err != nil {
		return nil, fmt.Errorf("list pipelines: %w", err)
	}
	var bundles v1alpha1.BundleList
	if err := s.client.List(ctx, &bundles); err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	var steps v1alpha1.PromotionStepList
	if err := s.client.List(ctx, &steps); err != nil {
		return nil, fmt.Errorf("list promotion steps: %w", err)
	}
	pipelineByKey := make(map[string]*v1alpha1.Pipeline, len(pipelines.Items))
	for i := range pipelines.Items {
		p := &pipelines.Items[i]
		pipelineByKey[p.Namespace+"/"+p.Name] = p
	}
	bundleByKey := make(map[string]*v1alpha1.Bundle, len(bundles.Items))
	for i := range bundles.Items {
		b := &bundles.Items[i]
		bundleByKey[b.Namespace+"/"+b.Name] = b
	}
	stepsByBundle := make(map[string][]v1alpha1.PromotionStep)
	for _, ps := range steps.Items {
		key := ps.Namespace + "/" + ps.Spec.BundleName
		stepsByBundle[key] = append(stepsByBundle[key], ps)
	}
	for i := range gates {
		g := &gates[i]
		var p *v1alpha1.Pipeline
		var bundleSteps []v1alpha1.PromotionStep
		b := bundleByKey[g.Namespace+"/"+g.Labels["kardinal.io/bundle"]]
		if b != nil {
			p = pipelineByKey[b.Namespace+"/"+b.Spec.Pipeline]
			bundleSteps = stepsByBundle[b.Namespace+"/"+b.Name]
		}
		out[g.Namespace+"/"+g.Name] = graphpkg.GateState(p, b, g, bundleSteps)
	}
	return out, nil
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

// handleValidateCEL compiles a PolicyGate expression while the
// user types to provide syntax feedback without needing the full evaluation context.
//
// POST /api/v1/ui/validate-cel
// Request: {"expression": "!schedule.isWeekend"}
// Response: {"valid": true} or {"valid": false, "error": "no such key: ..."}
//
// It compiles in the PolicyGate reconciler's own CEL environment
// (policygate.ValidateExpression), so the UI accepts exactly the expressions the
// controller evaluates. Stateless, no CRD writes.
func (s *uiAPIServer) handleValidateCEL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req uiValidateCELRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Expression == "" {
		http.Error(w, "expression field required", http.StatusBadRequest)
		return
	}

	compileErr := policygate.ValidateExpression(req.Expression)
	w.Header().Set("Content-Type", "application/json")
	if compileErr != nil {
		// Normalise error to a short, user-friendly message.
		msg := compileErr.Error()
		if len(msg) > 200 {
			msg = msg[:197] + "…"
		}
		_ = json.NewEncoder(w).Encode(uiValidateCELResponse{Valid: false, Error: fmt.Sprintf("CEL compile error: %s", msg)})
		return
	}
	_ = json.NewEncoder(w).Encode(uiValidateCELResponse{Valid: true})
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
// The namespace in the path wins; the body namespace is used only with the
// {name}/approve form. expiresInMinutes defaults to 60 and must be 1..1440.
// The override's createdBy is the requester (uiRequester).
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

	var req uiGateOverrideRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Reason == "" {
		http.Error(w, "reason is required", http.StatusBadRequest)
		return
	}
	if len(parts) == 2 && req.Namespace != "" {
		gateNS = req.Namespace
	}
	expiresMins := req.ExpiresInMinutes
	if expiresMins == 0 {
		expiresMins = 60 // default 1h
	}
	if expiresMins < 1 || expiresMins > maxGateOverrideMinutes {
		http.Error(w, fmt.Sprintf("expiresInMinutes must be between 1 and %d", maxGateOverrideMinutes), http.StatusBadRequest)
		return
	}
	createdBy := uiRequester(r.Context())

	now := time.Now().UTC()
	expiresAt := metav1.Time{Time: now.Add(time.Duration(expiresMins) * time.Minute)}
	createdAt := metav1.Time{Time: now}
	override := v1alpha1.PolicyGateOverride{
		Reason:    req.Reason,
		Stage:     req.Stage,
		ExpiresAt: expiresAt,
		CreatedAt: &createdAt,
		CreatedBy: createdBy,
	}
	// Re-read and re-apply on a conflict: the reconciler and other approvers
	// write the same gate, and a stale resourceVersion is not a user error.
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var gate v1alpha1.PolicyGate
		if err := s.client.Get(r.Context(), client.ObjectKey{Name: gateName, Namespace: gateNS}, &gate); err != nil {
			return err
		}
		gate.Spec.Overrides = append(gate.Spec.Overrides, override)
		return s.client.Update(r.Context(), &gate)
	})
	switch {
	case apierrors.IsNotFound(err):
		http.Error(w, "gate not found", http.StatusNotFound)
		return
	case err != nil:
		s.log.Error().Err(err).Str("gate", gateName).Msg("ui: approve gate")
		http.Error(w, "failed to update gate", http.StatusInternalServerError)
		return
	}

	s.log.Info().
		Str("gate", gateName).
		Str("reason", req.Reason).
		Str("createdBy", createdBy).
		Time("expiresAt", expiresAt.Time).
		Msg("ui: gate approved via override")

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(uiMessageResponse{Message: "gate overridden until " + expiresAt.UTC().Format(time.RFC3339)})
}

// buildUIConditions converts Kubernetes metav1.Condition slice to UI-friendly shape (#341).
// Sorted: failing/unknown conditions first, healthy (True) conditions last (#529).
func buildUIConditions(conditions []metav1.Condition) []uiCondition {
	if len(conditions) == 0 {
		return nil
	}
	result := make([]uiCondition, 0, len(conditions))
	for _, c := range conditions {
		uc := uiCondition{
			Type:    c.Type,
			Status:  string(c.Status),
			Reason:  c.Reason,
			Message: c.Message,
		}
		if !c.LastTransitionTime.IsZero() {
			uc.LastTransitionTime = c.LastTransitionTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		result = append(result, uc)
	}
	// Sort: False/Unknown conditions first, True last (#529).
	sort.Slice(result, func(i, j int) bool {
		si := conditionSortKey(result[i].Status)
		sj := conditionSortKey(result[j].Status)
		if si != sj {
			return si < sj
		}
		return result[i].Type < result[j].Type
	})
	return result
}

// conditionSortKey returns a sort priority: False=0, Unknown=1, True=2.
func conditionSortKey(status string) int {
	switch status {
	case "False":
		return 0
	case "Unknown":
		return 1
	default:
		return 2
	}
}

// uiEventResponse is the JSON shape for a Kubernetes event in the UI API (#527).
type uiEventResponse struct {
	Type           string `json:"type"`           // Normal or Warning
	Reason         string `json:"reason"`         // short CamelCase reason
	Message        string `json:"message"`        // human-readable event message
	Count          int32  `json:"count"`          // number of times event occurred
	FirstTimestamp string `json:"firstTimestamp"` // RFC3339 of first occurrence
	LastTimestamp  string `json:"lastTimestamp"`  // RFC3339 of most recent occurrence
}

// handleStepsSubpath routes /api/v1/ui/steps/{namespace}/{name}/events (#527).
func (s *uiAPIServer) handleStepsSubpath(w http.ResponseWriter, r *http.Request) {
	// Path: /api/v1/ui/steps/{namespace}/{name}/events
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/ui/steps/")
	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) != 3 || parts[2] != "events" {
		http.NotFound(w, r)
		return
	}
	namespace := parts[0]
	stepName := parts[1]
	if namespace == "" || stepName == "" {
		http.Error(w, "namespace and name required", http.StatusBadRequest)
		return
	}
	s.handleStepEvents(w, r, namespace, stepName)
}

// handleStepEvents returns the last 20 Kubernetes events for the named PromotionStep,
// newest first (#527). The step must
// exist (404 otherwise) and only events whose involvedObject is that step are
// returned, so the endpoint cannot read events of other objects.
func (s *uiAPIServer) handleStepEvents(w http.ResponseWriter, r *http.Request, namespace, stepName string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var step v1alpha1.PromotionStep
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: namespace, Name: stepName}, &step); err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, "promotion step not found", http.StatusNotFound)
			return
		}
		s.log.Error().Err(err).Str("step", stepName).Msg("ui: get promotion step")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// The manager client reads Events from the API server (they are not
	// cached, see uncachedObjects), which applies the field selector. Clients
	// that cannot, such as the fake client, fall back to listing the
	// namespace and filtering here.
	var eventList corev1.EventList
	if err := s.client.List(r.Context(), &eventList,
		client.InNamespace(namespace),
		client.MatchingFields{"involvedObject.kind": "PromotionStep", "involvedObject.name": stepName},
	); err != nil {
		var fallbackList corev1.EventList
		if ferr := s.client.List(r.Context(), &fallbackList, client.InNamespace(namespace)); ferr != nil {
			s.log.Error().Err(ferr).Str("step", stepName).Msg("ui: list events")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, ev := range fallbackList.Items {
			if ev.InvolvedObject.Name == stepName {
				eventList.Items = append(eventList.Items, ev)
			}
		}
	}

	// Keep only events about this PromotionStep (the field selector may not
	// have filtered, and other kinds can share the name). A UID mismatch is
	// an earlier step with the same name.
	filtered := make([]corev1.Event, 0, len(eventList.Items))
	for _, ev := range eventList.Items {
		obj := ev.InvolvedObject
		if obj.Kind != "PromotionStep" || obj.Name != stepName {
			continue
		}
		if obj.UID != "" && step.UID != "" && obj.UID != step.UID {
			continue
		}
		filtered = append(filtered, ev)
	}

	// Sort newest first.
	sort.SliceStable(filtered, func(i, j int) bool {
		return eventLastSeen(&filtered[i]).After(eventLastSeen(&filtered[j]))
	})

	// Cap at 20 events.
	const maxEvents = 20
	if len(filtered) > maxEvents {
		filtered = filtered[:maxEvents]
	}

	result := make([]uiEventResponse, 0, len(filtered))
	for i := range filtered {
		ev := &filtered[i]
		r := uiEventResponse{
			Type:    ev.Type,
			Reason:  ev.Reason,
			Message: ev.Message,
			Count:   eventCount(ev),
		}
		if first := eventFirstSeen(ev); !first.IsZero() {
			r.FirstTimestamp = first.UTC().Format(time.RFC3339)
		}
		if last := eventLastSeen(ev); !last.IsZero() {
			r.LastTimestamp = last.UTC().Format(time.RFC3339)
		}
		result = append(result, r)
	}
	writeJSON(w, result)
}

// The reconcilers write Events through events.k8s.io/v1. Read through core/v1,
// such an Event has no firstTimestamp, lastTimestamp or count: its times are
// eventTime and series.lastObservedTime, and its count is series.count (no
// series means it happened once). Events written through core/v1 before the
// upgrade carry the old fields. These helpers read both.

// eventLastSeen is when the Event last happened.
func eventLastSeen(ev *corev1.Event) time.Time {
	if !ev.LastTimestamp.IsZero() {
		return ev.LastTimestamp.Time
	}
	if ev.Series != nil && !ev.Series.LastObservedTime.IsZero() {
		return ev.Series.LastObservedTime.Time
	}
	return ev.EventTime.Time
}

// eventFirstSeen is when the Event first happened.
func eventFirstSeen(ev *corev1.Event) time.Time {
	if !ev.FirstTimestamp.IsZero() {
		return ev.FirstTimestamp.Time
	}
	return ev.EventTime.Time
}

// eventCount is how many times the Event happened.
func eventCount(ev *corev1.Event) int32 {
	if ev.Count > 0 {
		return ev.Count
	}
	if ev.Series != nil && ev.Series.Count > 0 {
		return ev.Series.Count
	}
	return 1
}

// handleBundles handles POST /api/v1/ui/bundles — creates a Bundle from the UI
// "Create Bundle" dialog. This is the UI equivalent of `kardinal create bundle`.
//
// Request body (JSON):
//
//	{
//	  "pipeline":  "nginx-demo",
//	  "image":     "ghcr.io/example/app:sha-abc1234",
//	  "commitSHA": "abc1234",   // optional
//	  "author":    "alice",      // optional
//	  "namespace": "default"     // optional
//	}
//
// Response (JSON on success, HTTP 201):
//
//	{"bundle": "nginx-demo-20260421120000-1234", "message": "bundle created"}
//
// It applies the rules of POST /api/v1/bundles and kardinal create bundle:
// 400 when lifecycle.ValidateNewBundle rejects the spec or the API server
// refuses the Bundle, 404 when the Pipeline does not exist in the namespace,
// 403 when the caller may not create it. Like a UI promote, the Bundle names
// the requester (uiRequester) in kardinal.io/requested-by; "author" is the
// build's author (spec.provenance.author), which the caller types.
func (s *uiAPIServer) handleBundles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req uiCreateBundleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Pipeline == "" {
		http.Error(w, "pipeline is required", http.StatusBadRequest)
		return
	}
	if req.Image == "" {
		http.Error(w, "image is required", http.StatusBadRequest)
		return
	}
	ns := req.Namespace
	if ns == "" {
		ns = "default"
	}

	// Parse the image reference into repository + tag or digest.
	imageRef := parseUIImageRef(req.Image)

	// Build provenance from optional fields.
	provenance := &v1alpha1.BundleProvenance{
		CommitSHA: req.CommitSHA,
		Author:    req.Author,
		Timestamp: metav1.Now(),
	}

	spec := v1alpha1.BundleSpec{
		Type:       "image",
		Pipeline:   req.Pipeline,
		Images:     []v1alpha1.ImageRef{imageRef},
		Provenance: provenance,
	}
	// The same rules as POST /api/v1/bundles and kardinal create bundle.
	if err := lifecycle.ValidateNewBundle(&spec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var pipeline v1alpha1.Pipeline
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: req.Pipeline}, &pipeline); err != nil {
		if apierrors.IsNotFound(err) {
			http.Error(w, fmt.Sprintf("pipeline %s/%s not found", ns, req.Pipeline), http.StatusNotFound)
			return
		}
		s.writeLifecycleError(w, "look up pipeline", err)
		return
	}

	requester := uiRequester(r.Context())
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: sanitizeName(req.Pipeline) + "-",
			Namespace:    ns,
			Labels: map[string]string{
				"kardinal.io/pipeline": req.Pipeline,
			},
			Annotations: map[string]string{lifecycle.AnnotationRequestedBy: requester},
		},
		Spec: spec,
	}
	lifecycle.StampCreatedAt(bundle, time.Now())

	if err := lifecycle.CreateBundleAs(r.Context(), s.client, bundle, requester); err != nil {
		if apierrors.IsInvalid(err) {
			http.Error(w, "bundle rejected by validation: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.writeLifecycleError(w, "create bundle", err)
		return
	}

	s.log.Info().
		Str("bundle", bundle.Name).
		Str("pipeline", req.Pipeline).
		Str("image", req.Image).
		Str("requestedBy", requester).
		Msg("ui: bundle created")

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(uiCreateBundleResponse{
		Bundle:  bundle.Name,
		Message: "bundle created — track with kardinal get bundles " + req.Pipeline,
	})
}

// parseUIImageRef parses an image string into an ImageRef.
// It handles three formats:
//   - repo@sha256:digest       → {Repository: "repo", Digest: "sha256:..."}
//   - repo:tag                 → {Repository: "repo", Tag: "tag"}
//   - repo                     → {Repository: "repo"}
func parseUIImageRef(image string) v1alpha1.ImageRef {
	// Digest reference: split on @ (last @ to handle registry:port@sha256:...)
	if idx := strings.LastIndex(image, "@"); idx >= 0 {
		return v1alpha1.ImageRef{
			Repository: image[:idx],
			Digest:     image[idx+1:],
		}
	}
	// Tag reference: split on last colon, but skip if it looks like a host:port with no tag
	if idx := strings.LastIndex(image, ":"); idx >= 0 {
		repo := image[:idx]
		tag := image[idx+1:]
		// Avoid splitting registry:port/image as repo="registry" tag="port/image"
		if !strings.Contains(tag, "/") {
			return v1alpha1.ImageRef{Repository: repo, Tag: tag}
		}
	}
	return v1alpha1.ImageRef{Repository: image}
}
