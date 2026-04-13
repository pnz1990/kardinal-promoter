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

	sigsyaml "sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// HumanAge returns a human-readable age string for the given creation time.
func HumanAge(t time.Time) string {
	d := time.Since(t)
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

// stepStatePriority returns a sort priority for a PromotionStep state.
// Higher priority = displayed first. Used by FormatPipelineTable and FormatStepsTable.
// Active states (Promoting/WaitingForMerge/HealthChecking) take precedence,
// then Pending (step queued but not started), then Verified (success),
// then Failed (terminal error). This ensures in-flight promotions are
// shown over older terminal-state bundles (#260).
func stepStatePriority(state string) int {
	switch state {
	case "Promoting", "WaitingForMerge", "HealthChecking":
		return 4
	case "Pending":
		return 3
	case "Verified":
		return 2
	case "Failed":
		return 1
	default:
		return 0
	}
}

// FormatPipelineTable writes a tabwriter-formatted table of pipelines to w.
// steps is the list of PromotionSteps in the same namespace; it is used to
// derive per-environment status and the active bundle version for each pipeline.
// If steps is nil or empty the environment columns will show "-".
func FormatPipelineTable(w io.Writer, pipelines []v1alpha1.Pipeline, steps []v1alpha1.PromotionStep) error {
	return FormatPipelineTableWithOptions(w, pipelines, steps, false)
}

// FormatPipelineTableWithOptions is like FormatPipelineTable but supports an additional
// showNamespace parameter. When showNamespace is true, a NAMESPACE column is prepended
// to each row (used by --all-namespaces flag).
func FormatPipelineTableWithOptions(w io.Writer, pipelines []v1alpha1.Pipeline, steps []v1alpha1.PromotionStep, showNamespace bool) error {
	// When multiple PromotionSteps exist for the same pipeline+env (from different
	// bundles), prefer the one with the highest state priority, then most recently
	// created.
	type envState struct {
		state      string
		bundleName string
		priority   int
		createdAt  time.Time
	}
	// pipelineEnvMap[pipelineName][envName] = envState
	pipelineEnvMap := make(map[string]map[string]envState)
	for _, s := range steps {
		pipe := s.Spec.PipelineName
		env := s.Spec.Environment
		if pipe == "" || env == "" {
			continue
		}
		if _, ok := pipelineEnvMap[pipe]; !ok {
			pipelineEnvMap[pipe] = make(map[string]envState)
		}
		state := s.Status.State
		if state == "" {
			state = "Pending"
		}
		priority := stepStatePriority(state)
		existing, hasExisting := pipelineEnvMap[pipe][env]
		// Replace if: higher priority, or same priority + more recent step.
		if !hasExisting ||
			priority > existing.priority ||
			(priority == existing.priority && s.CreationTimestamp.After(existing.createdAt)) {
			pipelineEnvMap[pipe][env] = envState{
				state:      state,
				bundleName: s.Spec.BundleName,
				priority:   priority,
				createdAt:  s.CreationTimestamp.Time,
			}
		}
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

	// Build header: [NAMESPACE] PIPELINE BUNDLE <ENV1> <ENV2> ... AGE
	header := ""
	if showNamespace {
		header = "NAMESPACE\t"
	}
	header += "PIPELINE\tBUNDLE"
	for _, env := range envOrder {
		header += "\t" + strings.ToUpper(env)
	}
	header += "\tAGE"
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return fmt.Errorf("write pipeline table header: %w", err)
	}

	for _, p := range pipelines {
		// Determine active bundle version.
		// Pick the bundle name from the highest-priority step across all environments.
		// This ensures the most-active bundle (Promoting > Verified > Failed) is shown,
		// not an older superseded bundle that happens to be Verified.
		bundleDisplay := "-"
		var bestPriority int
		var bestCreatedAt time.Time
		// Use pipeline name scoped to namespace for multi-namespace step lookup.
		pipelineKey := p.Name
		if showNamespace {
			pipelineKey = p.Name // step lookup uses pipelineName label (same across namespaces)
		}
		if envMap, ok := pipelineEnvMap[pipelineKey]; ok {
			for _, est := range envMap {
				if est.bundleName == "" {
					continue
				}
				if bundleDisplay == "-" ||
					est.priority > bestPriority ||
					(est.priority == bestPriority && est.createdAt.After(bestCreatedAt)) {
					bundleDisplay = est.bundleName
					bestPriority = est.priority
					bestCreatedAt = est.createdAt
				}
			}
		}

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
			state := "-"
			if envMap, ok := pipelineEnvMap[p.Name]; ok {
				if est, ok := envMap[env]; ok {
					state = est.state
				}
			}
			row += "\t" + state
		}
		row += "\t" + HumanAge(p.CreationTimestamp.Time)

		if _, err := fmt.Fprintln(tw, row); err != nil {
			return fmt.Errorf("write pipeline row: %w", err)
		}
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush pipeline table: %w", err)
	}
	return nil
}

// PolicyGatePhase derives the three-way display state for a PolicyGate:
//   - "Pass"    — ready == true (expression evaluated to true)
//   - "Block"   — ready == false AND lastEvaluatedAt is set (expression evaluated to false)
//   - "Pending" — not yet evaluated (lastEvaluatedAt is nil)
//
// This function is the single source of truth for this derivation; both
// explain.go and policy.go must call it rather than re-implementing the logic.
func PolicyGatePhase(g v1alpha1.PolicyGate) string {
	if g.Status.Ready {
		return "Pass"
	}
	if g.Status.LastEvaluatedAt != nil {
		return "Block"
	}
	return "Pending"
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
// the same priority logic as FormatPipelineTable: active states (Promoting,
// WaitingForMerge, HealthChecking) take precedence over terminal states (Verified,
// Failed). Within same priority, the most recently created step wins.
func FormatStepsTable(w io.Writer, steps []v1alpha1.PromotionStep) error {
	// Filter to the best step per environment (same priority as FormatPipelineTable).
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

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
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
