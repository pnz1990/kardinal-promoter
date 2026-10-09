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

// metrics.go — aggregate DORA-style deployment metrics for a Pipeline.
//
// ComputeDeploymentMetrics reads Bundle and PromotionStep CRD status fields
// (already written by their respective reconcilers) and aggregates them into
// a PipelineDeploymentMetrics snapshot. This is a pure CRD status read:
// no external API calls, no time.Now() outside the ComputedAt timestamp write.
//
// Graph-first compliance: this function reads status written by other reconcilers
// but writes only to Pipeline.status.deploymentMetrics (its own CRD). It does not
// mutate any other CRD.

package pipeline

import (
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// metricsLookbackBundles caps the lead-time and rate sample to the last N
// bundles Verified in the final environment. RolloutsLast30Days is counted
// over every bundle, not just the sample.
const metricsLookbackBundles = 30

// ComputeDeploymentMetrics computes aggregate promotion metrics for a pipeline
// from the most recent Verified Bundles (up to metricsLookbackBundles).
//
// Parameters:
//   - pipeline: the Pipeline being reconciled
//   - bundles: all Bundles in the pipeline namespace (will be filtered by pipeline name)
//   - steps: the PromotionSteps of this pipeline (the caller lists them by the
//     spec.pipelineName field index)
//   - now: the current time (injected for testability; use time.Now() in production)
//
// Returns nil when there are no Verified bundles (metrics not yet meaningful).
func ComputeDeploymentMetrics(
	pipeline *kardinalv1alpha1.Pipeline,
	bundles []kardinalv1alpha1.Bundle,
	steps []kardinalv1alpha1.PromotionStep,
	now time.Time,
) *kardinalv1alpha1.PipelineDeploymentMetrics {
	finalEnv := finalEnvironment(pipeline)

	// Build map: bundleName → time it was Verified in finalEnv
	// Uses PromotionStep.status conditions for precision; falls back to step creation time.
	finalVerifiedAt := make(map[string]time.Time)
	for i := range steps {
		s := &steps[i]
		if s.Spec.Environment != finalEnv || s.Status.State != "Verified" {
			continue
		}
		t := extractVerifiedTime(s)
		if existing, ok := finalVerifiedAt[s.Spec.BundleName]; !ok || t.After(existing) {
			finalVerifiedAt[s.Spec.BundleName] = t
		}
	}

	// Filter bundles: this pipeline, Verified, within the sample window.
	type verifiedBundle struct {
		bundle     *kardinalv1alpha1.Bundle
		verifiedAt time.Time
	}
	var verified []verifiedBundle
	for i := range bundles {
		b := &bundles[i]
		if b.Spec.Pipeline != pipeline.Name {
			continue
		}
		vt, ok := finalVerifiedAt[b.Name]
		if !ok {
			continue // not Verified in final env
		}
		verified = append(verified, verifiedBundle{b, vt})
	}

	if len(verified) == 0 {
		return nil
	}

	// Sort newest-first by verified time.
	sort.Slice(verified, func(i, j int) bool {
		return verified[i].verifiedAt.After(verified[j].verifiedAt)
	})

	// --- Rollouts last 30 days --- counted over every bundle, before the
	// sample is capped, so a team deploying more than once a day is not
	// reported as exactly metricsLookbackBundles.
	cutoff30d := now.Add(-30 * 24 * time.Hour)
	rolloutsLast30 := 0
	for _, vb := range verified {
		if vb.verifiedAt.After(cutoff30d) {
			rolloutsLast30++
		}
	}

	if len(verified) > metricsLookbackBundles {
		verified = verified[:metricsLookbackBundles]
	}
	sampleSize := len(verified)

	// --- Lead time (commit → final env Verified) ---
	leadMinutes := make([]int64, 0, sampleSize)
	for _, vb := range verified {
		lead := vb.verifiedAt.Sub(vb.bundle.CreationTimestamp.UTC())
		if lead > 0 {
			leadMinutes = append(leadMinutes, int64(lead.Minutes()))
		}
	}
	sort.Slice(leadMinutes, func(i, j int) bool { return leadMinutes[i] < leadMinutes[j] })
	p50, p90 := percentiles(leadMinutes)

	// --- Auto-rollback rate ---
	// A Bundle is considered a rollback deployment when spec.provenance.rollbackOf is set
	// (written by `kardinal rollback`). This is set at creation time, not at Verified time,
	// so it's reliable even when status.metrics is not yet populated.
	rollbackCount := 0
	for _, vb := range verified {
		if vb.bundle.Spec.Provenance != nil && vb.bundle.Spec.Provenance.RollbackOf != "" {
			rollbackCount++
		}
	}
	rollbackRateMillis := ratioMillis(rollbackCount, sampleSize)

	// --- Operator intervention rate ---
	// Counts bundles with at least one PolicyGate override: the Bundle
	// reconciler writes status.metrics.operatorInterventions when the Bundle
	// becomes Verified (#1308).
	interventionCount := 0
	for _, vb := range verified {
		if vb.bundle.Status.Metrics != nil && vb.bundle.Status.Metrics.OperatorInterventions > 0 {
			interventionCount++
		}
	}
	interventionRateMillis := ratioMillis(interventionCount, sampleSize)

	// --- Stale prod days ---
	// Days since the last promotion Verified in the final env. verified is
	// non-empty here and sorted newest-first.
	staleProdDays := int(now.Sub(verified[0].verifiedAt).Hours() / 24)
	if staleProdDays < 0 {
		staleProdDays = 0
	}

	stability := computeStability(pipeline.Name, finalEnv, bundles, steps, finalVerifiedAt)

	computedAt := metav1.NewTime(now)
	return &kardinalv1alpha1.PipelineDeploymentMetrics{
		Deployments:                    stability.deployments,
		FailedDeployments:              stability.failed,
		ChangeFailureRateMillis:        ratioMillis(stability.failed, stability.deployments),
		MeanTimeToRestoreMinutes:       stability.mttrMinutes,
		RestoredFailures:               stability.restored,
		RolloutsLast30Days:             rolloutsLast30,
		P50CommitToProdMinutes:         p50,
		P90CommitToProdMinutes:         p90,
		AutoRollbackRateMillis:         rollbackRateMillis,
		OperatorInterventionRateMillis: interventionRateMillis,
		StaleProdDays:                  staleProdDays,
		SampleSize:                     sampleSize,
		ComputedAt:                     &computedAt,
	}
}

// finalEnvironment returns the production environment whose Verified time
// drives the metrics: a DAG sink (an environment nothing depends on). When the
// pipeline fans out to several sinks, one named prod or production wins, else
// the last sink in promotion order. An invalid ordering falls back to the last
// list entry.
func finalEnvironment(p *kardinalv1alpha1.Pipeline) string {
	if len(p.Spec.Environments) == 0 {
		return ""
	}
	sinks, err := graph.SinkEnvironments(p)
	if err != nil || len(sinks) == 0 {
		return p.Spec.Environments[len(p.Spec.Environments)-1].Name
	}
	for _, env := range sinks {
		if n := strings.ToLower(env); n == "prod" || n == "production" {
			return env
		}
	}
	return sinks[len(sinks)-1]
}

// extractVerifiedTime returns the time at which a PromotionStep reached
// Verified: the Verified condition's transition time, else its creation time
// (which understates lead time but is never zero).
func extractVerifiedTime(s *kardinalv1alpha1.PromotionStep) time.Time {
	if t, ok := lifecycle.VerifiedTime(s); ok {
		return t
	}
	return s.CreationTimestamp.UTC()
}

// percentiles returns (p50, p90) of a sorted ascending int64 slice.
// Returns (0, 0) for empty slices.
func percentiles(sorted []int64) (p50, p90 int64) {
	n := len(sorted)
	if n == 0 {
		return 0, 0
	}
	p50 = sorted[clamp(n*50/100, 0, n-1)]
	p90 = sorted[clamp(n*90/100, 0, n-1)]
	return p50, p90
}

// ratioMillis returns (count/total)*1000 as an integer (e.g. 83 = 8.3%).
// Returns 0 when total is 0.
func ratioMillis(count, total int) int {
	if total == 0 {
		return 0
	}
	return count * 1000 / total
}

// clamp returns v clamped to [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// healthCheckStepName is the last step of every step sequence. Its startedAt
// is when the change reached the environment: the engine runs it right after
// git-push (auto) or the merge (pr-review).
const healthCheckStepName = "health-check"

// stability is the DORA stability pair over the last metricsLookbackBundles
// deployments to the final environment.
type stability struct {
	deployments, failed, restored int
	mttrMinutes                   int64
}

// deployment is one Bundle's change reaching the final environment.
type deployment struct {
	bundle     string
	deployedAt time.Time
	failed     bool
	failedAt   time.Time
}

// stepDeployedAt returns when s's change reached its environment: the
// startedAt of its health-check step. ok is false when it never did (the
// step failed or is still before git-push or the merge).
func stepDeployedAt(s *kardinalv1alpha1.PromotionStep) (time.Time, bool) {
	for i := range s.Status.Steps {
		st := &s.Status.Steps[i]
		if st.Name == healthCheckStepName && st.StartedAt != nil {
			return st.StartedAt.UTC(), true
		}
	}
	return time.Time{}, false
}

// stepFailedAt is when a failed step failed: the completedAt of its failed
// step entry, else its health-check's, else its creation time.
func stepFailedAt(s *kardinalv1alpha1.PromotionStep) time.Time {
	var hc time.Time
	for i := range s.Status.Steps {
		st := &s.Status.Steps[i]
		if st.CompletedAt == nil {
			continue
		}
		if st.State == kardinalv1alpha1.StepExecutionFailed {
			return st.CompletedAt.UTC()
		}
		if st.Name == healthCheckStepName {
			hc = st.CompletedAt.UTC()
		}
	}
	if !hc.IsZero() {
		return hc
	}
	return s.CreationTimestamp.UTC()
}

// computeStability computes the change failure rate and the time to restore
// in finalEnv from PromotionStep and Bundle status, both written by their own
// reconcilers.
//
// A deployment is a Bundle at least one of whose finalEnv steps started its
// health check. It failed when such a step ended Failed, AbortedByAlarm or
// RollingBack, or when a rollback Bundle targeting finalEnv names it in
// kardinal.io/rollback-from (rolled back by a human or a RollbackPolicy).
// Its restore is the first Verified time in finalEnv, of any Bundle, after
// the failure.
func computeStability(pipelineName, finalEnv string, bundles []kardinalv1alpha1.Bundle,
	steps []kardinalv1alpha1.PromotionStep, finalVerifiedAt map[string]time.Time) stability {
	byBundle := map[string]*deployment{}
	for i := range steps {
		s := &steps[i]
		if s.Spec.Environment != finalEnv || s.Spec.PipelineName != pipelineName {
			continue
		}
		at, ok := stepDeployedAt(s)
		if !ok {
			continue
		}
		d := byBundle[s.Spec.BundleName]
		if d == nil {
			d = &deployment{bundle: s.Spec.BundleName, deployedAt: at}
			byBundle[s.Spec.BundleName] = d
		}
		if at.Before(d.deployedAt) {
			d.deployedAt = at
		}
		switch s.Status.State {
		case "Failed", "AbortedByAlarm", "RollingBack":
			f := stepFailedAt(s)
			if !d.failed || f.Before(d.failedAt) {
				d.failed, d.failedAt = true, f
			}
		}
	}
	for i := range bundles {
		b := &bundles[i]
		if b.Spec.Pipeline != pipelineName || b.Labels[lifecycle.LabelRollback] != "true" {
			continue
		}
		if b.Spec.Intent != nil && b.Spec.Intent.TargetEnvironment != "" && b.Spec.Intent.TargetEnvironment != finalEnv {
			continue
		}
		from := b.Annotations[lifecycle.AnnotationRollbackFrom]
		d := byBundle[from]
		if from == "" || d == nil {
			continue
		}
		if at := b.CreationTimestamp.UTC(); !d.failed || at.Before(d.failedAt) {
			d.failed, d.failedAt = true, at
		}
	}

	deps := make([]*deployment, 0, len(byBundle))
	for _, d := range byBundle {
		deps = append(deps, d)
	}
	sort.Slice(deps, func(i, j int) bool {
		if !deps[i].deployedAt.Equal(deps[j].deployedAt) {
			return deps[i].deployedAt.After(deps[j].deployedAt)
		}
		return deps[i].bundle < deps[j].bundle
	})
	if len(deps) > metricsLookbackBundles {
		deps = deps[:metricsLookbackBundles]
	}

	verifiedTimes := make([]time.Time, 0, len(finalVerifiedAt))
	for _, t := range finalVerifiedAt {
		verifiedTimes = append(verifiedTimes, t)
	}
	sort.Slice(verifiedTimes, func(i, j int) bool { return verifiedTimes[i].Before(verifiedTimes[j]) })

	out := stability{deployments: len(deps)}
	var restoreSum time.Duration
	for _, d := range deps {
		if !d.failed {
			continue
		}
		out.failed++
		k := sort.Search(len(verifiedTimes), func(i int) bool { return verifiedTimes[i].After(d.failedAt) })
		if k == len(verifiedTimes) {
			continue // not restored yet
		}
		out.restored++
		restoreSum += verifiedTimes[k].Sub(d.failedAt)
	}
	if out.restored > 0 {
		out.mttrMinutes = int64((restoreSum / time.Duration(out.restored)).Minutes())
	}
	return out
}
