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
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

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

// FormatPipelineTable writes a tabwriter-formatted table of pipelines to w.
// steps is the list of PromotionSteps in the same namespace; it is used to
// derive per-environment status and the active bundle version for each pipeline.
// If steps is nil or empty the environment columns will show "-".
func FormatPipelineTable(w io.Writer, pipelines []v1alpha1.Pipeline, steps []v1alpha1.PromotionStep) error {
	// Build a lookup: pipeline → environment → (state, bundleName)
	type envState struct {
		state      string
		bundleName string
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
		pipelineEnvMap[pipe][env] = envState{
			state:      state,
			bundleName: s.Spec.BundleName,
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

	// Build header: PIPELINE BUNDLE <ENV1> <ENV2> ... AGE
	header := "PIPELINE\tBUNDLE"
	for _, env := range envOrder {
		header += "\t" + strings.ToUpper(env)
	}
	header += "\tAGE"
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return fmt.Errorf("write pipeline table header: %w", err)
	}

	for _, p := range pipelines {
		// Determine active bundle version.
		// Use the bundle name from any PromotionStep for this pipeline, preferring
		// the most-advanced (Verified) environment's bundle name.
		bundleDisplay := "-"
		if envMap, ok := pipelineEnvMap[p.Name]; ok {
			// Prefer Verified env bundle, else take any non-empty bundle name.
			for _, est := range envMap {
				if est.bundleName != "" && bundleDisplay == "-" {
					bundleDisplay = est.bundleName
				}
				if est.state == "Verified" && est.bundleName != "" {
					bundleDisplay = est.bundleName
				}
			}
		}

		// Build the row.
		row := fmt.Sprintf("%s\t%s", p.Name, bundleDisplay)
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
func FormatStepsTable(w io.Writer, steps []v1alpha1.PromotionStep) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ENVIRONMENT\tSTEP-TYPE\tSTATE\tMESSAGE"); err != nil {
		return fmt.Errorf("write steps table header: %w", err)
	}
	for _, s := range steps {
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
