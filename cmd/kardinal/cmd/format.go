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

// Package cmd implements the cobra subcommand tree for the kardinal CLI.
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigsyaml "sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// HumanAge returns a human-readable age string for the given creation time.
func HumanAge(t time.Time) string {
	return humanDuration(time.Since(t))
}

// humanDuration renders d in its largest whole unit: 45s, 4m, 3h, 2d.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// orDash returns s, or "-" when s is empty.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncateRunes shortens s to n runes, the last three being "...", without
// splitting a UTF-8 character.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}

// stepStatePriority returns a sort priority for a PromotionStep state.
// Higher priority = displayed first. Used by the pipeline, steps and explain views.
// Active states (Promoting/WaitingForMerge/HealthChecking/Verifying) take precedence,
// then Pending (step queued but not started), then Verified, AbortedByAlarm
// and RollingBack, then Failed. AbortedByAlarm and RollingBack rank with
// Verified because the alarm fires after merge: the environment runs that
// bundle, so the newer of the two is what is deployed. A RollingBack step
// stays RollingBack for good (the rollback Bundle's own step carries the
// promotion on), so ranking it as active would hide that newer step once it
// is Verified. Failed ranks lowest because most failures happen before merge.
// This ensures in-flight promotions and rollbacks are shown over older
// terminal-state bundles (#260).
func stepStatePriority(state string) int {
	switch state {
	case "Promoting", "WaitingForMerge", "HealthChecking", "Verifying":
		return 4
	case "Pending":
		return 3
	case "Verified", "AbortedByAlarm", "RollingBack":
		return 2
	case "Failed":
		return 1
	default:
		return 0
	}
}

// FormatPipelineTableFull writes a table of pipelines to w. BUNDLE is each
// pipeline's current Bundle (lifecycle.CurrentBundle, the UI's
// activeBundleName) and every environment column describes that Bundle
// (pipelineEnvStates). bundles and steps may span namespaces and pipelines.
// When subs is non-nil a SUB column counts the Watching Subscriptions per
// pipeline. showNamespace prepends a NAMESPACE column (--all-namespaces).
func FormatPipelineTableFull(w io.Writer, pipelines []v1alpha1.Pipeline, bundles []v1alpha1.Bundle,
	steps []v1alpha1.PromotionStep, subs []v1alpha1.Subscription, showNamespace bool) error {
	var subCount map[string]int
	if subs != nil {
		// namespace/pipeline → count of active Subscriptions (Phase=="Watching").
		subCount = make(map[string]int)
		for _, s := range subs {
			if s.Spec.Pipeline == "" {
				continue
			}
			if s.Status.Phase == "Watching" {
				subCount[s.Namespace+"/"+s.Spec.Pipeline]++
			}
		}
	}
	return formatPipelineTableInternal(w, pipelines, pipelineEnvStates(pipelines, bundles, steps), subCount, showNamespace)
}

// pipelineRow is the table content of one pipeline: its current Bundle and
// that Bundle's state in each environment.
type pipelineRow struct {
	bundle string
	envs   map[string]string
	// hints are the roll-back hints of a current Bundle that is Rejected
	// with its change live (lifecycle.RejectedLiveStep).
	hints []string
}

// pipelineEnvStates returns, per namespace/pipeline, the BUNDLE and
// per-environment states of the pipeline table, all about one Bundle: the
// pipeline's current Bundle, lifecycle.CurrentBundle (E2E-R17). An
// environment shows that Bundle's step state there, the highest
// stepStatePriority over its region steps (an unstarted step is Pending).
// Without a step it shows "Waiting" while the Bundle is in flight and will
// still come (held by a gate, or upstream not Verified yet), and nothing
// ("-") when the Bundle is terminal or does not promote the environment
// (skipped, or past its target). Another Bundle's state is never shown, so a
// row cannot pair a Bundle with a state it does not have.
func pipelineEnvStates(pipelines []v1alpha1.Pipeline, bundles []v1alpha1.Bundle,
	steps []v1alpha1.PromotionStep) map[string]pipelineRow {
	bundlesByPipeline := make(map[string][]v1alpha1.Bundle)
	for i := range bundles {
		if bundles[i].Spec.Pipeline != "" {
			key := bundles[i].Namespace + "/" + bundles[i].Spec.Pipeline
			bundlesByPipeline[key] = append(bundlesByPipeline[key], bundles[i])
		}
	}

	out := make(map[string]pipelineRow, len(pipelines))
	for i := range pipelines {
		p := &pipelines[i]
		key := p.Namespace + "/" + p.Name
		b := lifecycle.CurrentBundle(bundlesByPipeline[key], steps)
		if b == nil {
			continue
		}
		row := pipelineRow{bundle: b.Name, envs: make(map[string]string, len(p.Spec.Environments))}
		if lifecycle.Rejected(b) {
			// Current only because its change is live somewhere
			// (lifecycle.CurrentBundle): say so.
			row.bundle = b.Name + "(Rejected)"
			for _, env := range lifecycle.RejectedLiveEnvs(b, steps) {
				row.hints = append(row.hints, fmt.Sprintf("WARNING: pipeline %s: bundle %s is Rejected in %s: %s (kardinal rollback %s --env %s)",
					p.Name, b.Name, env, lifecycle.RejectedLiveHint, p.Name, env))
			}
		}
		if lifecycle.InFlightPhase(b.Status.Phase) {
			for _, e := range p.Spec.Environments {
				if _, err := graph.DirectUpstreams(p, b, e.Name); err == nil {
					row.envs[e.Name] = "Waiting"
				}
			}
		}
		best := make(map[string]int)
		for j := range steps {
			s := &steps[j]
			if s.Namespace != p.Namespace || s.Spec.PipelineName != p.Name || s.Spec.BundleName != b.Name {
				continue
			}
			env, state := s.Spec.Environment, s.Status.State
			if state == "" {
				state = "Pending"
			}
			if prio, seen := best[env]; !seen || stepStatePriority(state) > prio {
				best[env] = stepStatePriority(state)
				row.envs[env] = state
			}
		}
		out[key] = row
	}
	return out
}

// formatPipelineTableInternal renders the pipeline table. rows maps
// namespace/pipeline → its current Bundle and environment states; a pipeline
// or environment without an entry shows "-". subCount maps
// namespace/pipeline → active subscription count; nil means no SUB column.
func formatPipelineTableInternal(w io.Writer, pipelines []v1alpha1.Pipeline, rows map[string]pipelineRow,
	subCount map[string]int, showNamespace bool) error {
	if len(pipelines) == 0 {
		_, _ = fmt.Fprintln(w, "No pipelines found.")
		_, _ = fmt.Fprintln(w, "  To get started, apply a Pipeline CRD:")
		_, _ = fmt.Fprintln(w, "    kubectl apply -f examples/quickstart/pipeline.yaml")
		_, _ = fmt.Fprintln(w, "  Or check CRD installation with: kardinal doctor")
		return nil
	}

	// Collect all environment names in spec order across all pipelines so that
	// when multiple pipelines are printed their columns align.
	// We preserve spec-order per pipeline and use the union across all.
	envOrder := make([]string, 0)
	envSeen := make(map[string]bool)
	for _, p := range pipelines {
		for _, e := range p.Spec.Environments {
			if !envSeen[e.Name] {
				envSeen[e.Name] = true
				envOrder = append(envOrder, e.Name)
			}
		}
	}

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)

	// Build header: [NAMESPACE] PIPELINE BUNDLE <ENV1> <ENV2> ... [SUB] AGE
	header := ""
	if showNamespace {
		header = "NAMESPACE\t"
	}
	header += "PIPELINE\tBUNDLE"
	for _, env := range envOrder {
		header += "\t" + strings.ToUpper(env)
	}
	if subCount != nil {
		header += "\tSUB"
	}
	header += "\tAGE"
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return fmt.Errorf("write pipeline table header: %w", err)
	}

	for _, p := range pipelines {
		pipelineKey := p.Namespace + "/" + p.Name
		pr := rows[pipelineKey]
		bundleDisplay := orDash(pr.bundle)

		// Build the row. Append [PAUSED] to the pipeline name when the pipeline
		// has spec.paused=true so operators immediately see the frozen state.
		pipelineDisplay := p.Name
		if p.Spec.Paused {
			pipelineDisplay = p.Name + " [PAUSED]"
		}
		row := fmt.Sprintf("%s\t%s", pipelineDisplay, bundleDisplay)
		if showNamespace {
			row = fmt.Sprintf("%s\t%s\t%s", p.Namespace, pipelineDisplay, bundleDisplay)
		}
		for _, env := range envOrder {
			row += "\t" + orDash(pr.envs[env])
		}
		if subCount != nil {
			row += "\t" + fmt.Sprintf("%d", subCount[pipelineKey])
		}
		row += "\t" + HumanAge(p.CreationTimestamp.Time)

		if _, err := fmt.Fprintln(tw, row); err != nil {
			return fmt.Errorf("write pipeline row: %w", err)
		}
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush pipeline table: %w", err)
	}
	for _, p := range pipelines {
		for _, h := range rows[p.Namespace+"/"+p.Name].hints {
			if _, err := fmt.Fprintln(w, h); err != nil {
				return fmt.Errorf("write pipeline hint: %w", err)
			}
		}
	}
	return nil
}

// FormatBundleErrors writes a plain-text error notice for each pipeline whose
// newest Bundle that is not Superseded or Rejected is Failed. A newer Bundle, promoting or
// Verified, means the failure is history, so it is not reported (E2E-R16).
// The notice is printed after the pipeline table so the root cause of a
// silent "Phase: Error" is visible without `kubectl describe graph`.
//
// Output format (one line per pipeline, sorted by pipeline):
//
//	ERROR: pipeline [<namespace>/]<pipeline>: <condition-message>
//
// The message comes from a True cause condition: InvalidSpec before Failed,
// and within a type CircularDependency, then TranslationError, then the first
// one. Other True conditions (GraphSynced) are not the cause and are skipped.
// With no cause condition the message is a describe hint.
// showNamespace prefixes the pipeline with its namespace (--all-namespaces).
// When no Failed bundles are present, nothing is written.
func FormatBundleErrors(w io.Writer, bundles []v1alpha1.Bundle, showNamespace bool) error {
	sorted := make([]v1alpha1.Bundle, 0, len(bundles))
	for _, b := range bundles {
		if !lifecycle.Halted(&b) {
			sorted = append(sorted, b)
		}
	}
	// Newest first (lifecycle.CompareCreation, the order supersession uses),
	// so the first bundle seen per pipeline is the current one.
	sort.SliceStable(sorted, func(i, j int) bool {
		return lifecycle.CompareCreation(&sorted[i], &sorted[j]) > 0
	})

	typeRank := map[string]int{"InvalidSpec": 20, "Failed": 10}
	reasonRank := map[string]int{"CircularDependency": 2, "TranslationError": 1}
	type pipelineError struct{ pipeline, message string }
	seen := make(map[string]bool)
	var errs []pipelineError
	for _, b := range sorted {
		pipeline := b.Spec.Pipeline
		if pipeline == "" {
			pipeline = b.Name // fallback: use bundle name if pipeline ref missing
		}
		if showNamespace && b.Namespace != "" {
			pipeline = b.Namespace + "/" + pipeline
		}
		key := b.Namespace + "/" + pipeline
		if seen[key] {
			continue
		}
		seen[key] = true
		if b.Status.Phase != "Failed" {
			continue
		}

		msg, rank := "", -1
		for _, cond := range b.Status.Conditions {
			t, cause := typeRank[cond.Type]
			if cond.Status != metav1.ConditionTrue || !cause {
				continue
			}
			if r := t + reasonRank[cond.Reason]; r > rank {
				msg, rank = cond.Message, r
			}
		}
		if msg == "" {
			msg = "promotion failed — run `kubectl describe bundle " + b.Name + "` for details"
		}
		errs = append(errs, pipelineError{pipeline: pipeline, message: msg})
	}
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].pipeline < errs[j].pipeline })

	for _, e := range errs {
		if _, err := fmt.Fprintf(w, "ERROR: pipeline %s: %s\n", e.pipeline, e.message); err != nil {
			return fmt.Errorf("write bundle error: %w", err)
		}
	}
	return nil
}

// FormatBundleTable writes a tabwriter-formatted table of bundles to w.
func FormatBundleTable(w io.Writer, bundles []v1alpha1.Bundle) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "BUNDLE\tTYPE\tPHASE\tAGE"); err != nil {
		return fmt.Errorf("write bundle table header: %w", err)
	}
	for _, b := range bundles {
		phase := b.Status.Phase
		if phase == "" {
			phase = "Unknown"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			b.Name,
			b.Spec.Type,
			phase,
			HumanAge(b.CreationTimestamp.Time),
		); err != nil {
			return fmt.Errorf("write bundle row: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush bundle table: %w", err)
	}
	return nil
}

// FormatStepsTable writes a tabwriter-formatted table of promotion steps to w.
// When multiple bundles have steps for the same environment (e.g. after rapid
// successive deploys), only the most-active step per environment is shown using
// the same priority logic as the pipeline table: active states (Promoting,
// WaitingForMerge, HealthChecking) take precedence over terminal states (Verified,
// Failed). Within same priority, the most recently created step wins.
func FormatStepsTable(w io.Writer, steps []v1alpha1.PromotionStep) error {
	// Filter to the best step per environment (same priority as the pipeline table).
	type stepKey struct{ env string }
	type bestEntry struct {
		step     v1alpha1.PromotionStep
		priority int
	}
	best := make(map[stepKey]bestEntry)
	for _, s := range steps {
		key := stepKey{env: s.Spec.Environment}
		state := s.Status.State
		if state == "" {
			state = "Pending"
		}
		priority := stepStatePriority(state)
		existing, ok := best[key]
		if !ok ||
			priority > existing.priority ||
			(priority == existing.priority && s.CreationTimestamp.After(existing.step.CreationTimestamp.Time)) {
			best[key] = bestEntry{step: s, priority: priority}
		}
	}

	// Sort by environment name for stable output.
	envOrder := make([]string, 0, len(best))
	for k := range best {
		envOrder = append(envOrder, k.env)
	}
	sort.Strings(envOrder)

	// Determine if any PromotionStep has per-step detail populated.
	hasSubSteps := false
	for _, e := range best {
		if len(e.step.Status.Steps) > 0 {
			hasSubSteps = true
			break
		}
	}

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if hasSubSteps {
		if _, err := fmt.Fprintln(tw, "ENVIRONMENT\tSTEP\tSTATE\tDURATION\tMESSAGE"); err != nil {
			return fmt.Errorf("write steps table header: %w", err)
		}
		for _, env := range envOrder {
			s := best[stepKey{env: env}].step
			if len(s.Status.Steps) == 0 {
				// Fallback: no per-step detail, show environment-level row.
				state := s.Status.State
				if state == "" {
					state = "Pending"
				}
				msg := s.Status.Message
				if msg == "" {
					msg = "-"
				}
				if _, err := fmt.Fprintf(tw, "%s\t(no step detail)\t%s\t-\t%s\n", env, state, msg); err != nil {
					return fmt.Errorf("write step row: %w", err)
				}
				continue
			}
			for i, ss := range s.Status.Steps {
				dur := "-"
				if ss.DurationMs > 0 {
					dur = fmt.Sprintf("%dms", ss.DurationMs)
				}
				msg := ss.Message
				if msg == "" {
					msg = "-"
				}
				envCol := env
				if i > 0 {
					envCol = "" // only print env once per step group
				}
				if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					envCol, ss.Name, string(ss.State), dur, msg,
				); err != nil {
					return fmt.Errorf("write sub-step row: %w", err)
				}
			}
		}
	} else {
		if _, err := fmt.Fprintln(tw, "ENVIRONMENT\tSTEP-TYPE\tSTATE\tMESSAGE"); err != nil {
			return fmt.Errorf("write steps table header: %w", err)
		}
		for _, env := range envOrder {
			s := best[stepKey{env: env}].step
			state := s.Status.State
			if state == "" {
				state = "Pending"
			}
			msg := s.Status.Message
			if msg == "" {
				msg = "-"
			}
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				s.Spec.Environment,
				s.Spec.StepType,
				state,
				msg,
			); err != nil {
				return fmt.Errorf("write step row: %w", err)
			}
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush steps table: %w", err)
	}
	return nil
}

// ─── Output format helpers ────────────────────────────────────────────────────

// OutputFormat returns the global output format flag value, normalised to lower-case.
// Valid values: "" (table), "json", "yaml".
func OutputFormat() string {
	return strings.ToLower(globalOutput)
}

// WriteJSON serialises v as indented JSON to w.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

// WriteYAML serialises v as YAML to w.
func WriteYAML(w io.Writer, v any) error {
	data, err := sigsyaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write yaml: %w", err)
	}
	return nil
}
