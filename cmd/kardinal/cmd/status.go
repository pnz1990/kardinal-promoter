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

package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [pipeline]",
		Short: "Show controller health or per-pipeline in-flight promotion details",
		Long: `Show the health of the kardinal controller and cluster resource summary.

When called without arguments: displays controller version, pipeline count, and
active bundle count.

When called with a pipeline name: shows in-flight promotion details for that
pipeline — active bundle, PromotionStep states (with active steps highlighted),
blocking PolicyGates (with CEL expression and current reason), and open PR URLs.
This is the first command to run when a promotion is stuck.

Examples:
  # Cluster-level summary
  kardinal status

  # Per-pipeline in-flight view
  kardinal status nginx-demo

For detailed gate diagnostics, use 'kardinal explain <pipeline>'.
For step-level log output, use 'kardinal logs <pipeline>'.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return runStatusPipeline(cmd, args[0])
			}
			return runStatus(cmd)
		},
	}
}

// runStatus shows the cluster-level controller summary.
// Preserves the existing behaviour when no pipeline argument is given.
func runStatus(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()

	client, _, err := buildClient()
	if err != nil {
		return err // buildClient already provides actionable message
	}
	ctx := context.Background()

	// 1. Controller version from ConfigMap
	var versionCM corev1.ConfigMap
	ctrlVersion := "(unknown)"
	if err := client.Get(ctx, types.NamespacedName{
		Name:      "kardinal-version",
		Namespace: "kardinal-system",
	}, &versionCM); err == nil {
		if v := versionCM.Data["version"]; v != "" {
			ctrlVersion = v
		}
	}

	// 2. Pipeline count (all namespaces)
	var pipelines v1alpha1.PipelineList
	if err := client.List(ctx, &pipelines); err != nil {
		_, _ = fmt.Fprintln(out, "Controller: "+ctrlVersion)
		return fmt.Errorf("list pipelines: %w", err)
	}

	// 3. Bundle counts
	var bundles v1alpha1.BundleList
	_ = client.List(ctx, &bundles)

	var activeBundles, failedPipelines int
	failedPipelineNames := []string{}
	for _, b := range bundles.Items {
		phase := b.Status.Phase
		if phase == "Promoting" || phase == "Pending" {
			activeBundles++
		}
	}
	for _, p := range pipelines.Items {
		if p.Status.Phase == "Failed" || p.Status.Phase == "Error" {
			failedPipelines++
			failedPipelineNames = append(failedPipelineNames, p.Name)
		}
	}

	// Print summary
	_, _ = fmt.Fprintf(out, "Controller:  %s\n", ctrlVersion)
	_, _ = fmt.Fprintf(out, "Pipelines:   %d", len(pipelines.Items))
	if failedPipelines > 0 {
		_, _ = fmt.Fprintf(out, " (%d failed: %v)", failedPipelines, failedPipelineNames)
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "Bundles:     %d (%d active)\n", len(bundles.Items), activeBundles)

	if failedPipelines > 0 {
		_, _ = fmt.Fprintf(out, "\nWarning: %d pipeline(s) in failed state — run 'kardinal get pipelines' for details\n", failedPipelines)
	}

	return nil
}

// runStatusPipeline shows in-flight promotion details for a specific pipeline.
// It answers: "what is <pipeline> doing right now?"
func runStatusPipeline(cmd *cobra.Command, pipeline string) error {
	c, ns, err := buildClient()
	if err != nil {
		return err
	}
	return statusPipelineWriter(cmd.OutOrStdout(), c, ns, pipeline)
}

// StatusPipelineWriterForTest is an exported wrapper around statusPipelineWriter
// for use in tests. It allows tests to inject a fake client without going through
// the CLI flag parsing.
func StatusPipelineWriterForTest(w io.Writer, c sigs_client.Client, ns, pipeline string) error {
	return statusPipelineWriter(w, c, ns, pipeline)
}

// statusPipelineWriter renders the per-pipeline status to w using client c.
// Separated from runStatusPipeline to allow unit testing with a fake client.
func statusPipelineWriter(w io.Writer, c sigs_client.Client, ns, pipeline string) error {
	ctx := context.Background()

	// Verify the pipeline exists.
	var pl v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Name: pipeline, Namespace: ns}, &pl); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("pipeline %q not found in namespace %q", pipeline, ns)
		}
		return fmt.Errorf("get pipeline: %w", err)
	}

	_, _ = fmt.Fprintf(w, "Pipeline: %s   Namespace: %s\n\n", pipeline, ns)

	// List PromotionSteps for this pipeline.
	var steps v1alpha1.PromotionStepList
	if err := c.List(ctx, &steps,
		sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
	); err != nil {
		return fmt.Errorf("list promotion steps: %w", err)
	}

	// List PolicyGates for this pipeline.
	var gates v1alpha1.PolicyGateList
	if err := c.List(ctx, &gates,
		sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
	); err != nil {
		return fmt.Errorf("list policy gates: %w", err)
	}

	if len(steps.Items) == 0 {
		_, _ = fmt.Fprintln(w, "No active promotions.")
		return nil
	}

	// Determine the most active bundle per environment (same priority logic as explain).
	type envBest struct {
		bundleName string
		priority   int
		createdAt  time.Time
	}
	activeBundleByEnv := make(map[string]envBest)
	for i := range steps.Items {
		s := &steps.Items[i]
		env := s.Spec.Environment
		state := s.Status.State
		if state == "" {
			state = "Pending"
		}
		pri := stepStatePriority(state)
		existing, ok := activeBundleByEnv[env]
		if !ok || pri > existing.priority ||
			(pri == existing.priority && s.CreationTimestamp.After(existing.createdAt)) {
			activeBundleByEnv[env] = envBest{
				bundleName: s.Spec.BundleName,
				priority:   pri,
				createdAt:  s.CreationTimestamp.Time,
			}
		}
	}

	// Build the active steps view, keyed by environment.
	type stepRow struct {
		env        string
		state      string
		activeStep string // currently executing step (from status.steps[])
		prURL      string
		age        string
		bundleName string
	}
	stepsByEnv := make(map[string]stepRow)

	for i := range steps.Items {
		s := &steps.Items[i]
		env := s.Spec.Environment
		best, ok := activeBundleByEnv[env]
		if !ok || s.Spec.BundleName != best.bundleName {
			continue
		}

		state := s.Status.State
		if state == "" {
			state = "Pending"
		}

		// Find the currently executing step (first non-terminal step in status.steps).
		activeStep := "-"
		for _, ss := range s.Status.Steps {
			if ss.State != "Completed" && ss.State != "Failed" && ss.State != "" {
				activeStep = ss.Name
				break
			}
		}

		prURL := s.Status.PRURL
		if prURL == "" {
			prURL = "-"
		}

		age := "-"
		if !s.CreationTimestamp.IsZero() {
			age = HumanAge(s.CreationTimestamp.Time)
		}

		stepsByEnv[env] = stepRow{
			env:        env,
			state:      state,
			activeStep: activeStep,
			prURL:      prURL,
			age:        age,
			bundleName: s.Spec.BundleName,
		}
	}

	// Collect and sort environments.
	envs := make([]string, 0, len(stepsByEnv))
	for e := range stepsByEnv {
		envs = append(envs, e)
	}
	sort.Strings(envs)

	// Print active bundle summary.
	bundleNames := map[string]struct{}{}
	for _, r := range stepsByEnv {
		bundleNames[r.bundleName] = struct{}{}
	}
	bnames := make([]string, 0, len(bundleNames))
	for b := range bundleNames {
		bnames = append(bnames, b)
	}
	sort.Strings(bnames)
	_, _ = fmt.Fprintf(w, "Active bundle(s): %s\n\n", strings.Join(bnames, ", "))

	// Print PromotionSteps table.
	_, _ = fmt.Fprintln(w, "Promotion Steps")
	_, _ = fmt.Fprintln(w, strings.Repeat("─", 72))
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ENVIRONMENT\tSTATE\tACTIVE STEP\tPR\tAGE")
	for _, env := range envs {
		r := stepsByEnv[env]
		// Mark in-progress states with a pointer.
		marker := "  "
		switch r.state {
		case "Promoting", "WaitingForMerge", "HealthChecking":
			marker = "▶ "
		}
		prDisplay := r.prURL
		if len(prDisplay) > 40 {
			prDisplay = prDisplay[len(prDisplay)-40:]
		}
		_, _ = fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t%s\n",
			marker, env, r.state, r.activeStep, prDisplay, r.age)
	}
	_ = tw.Flush()

	// Show blocking PolicyGates (status.ready == false).
	var blockingGates []v1alpha1.PolicyGate
	for i := range gates.Items {
		g := &gates.Items[i]
		if !g.Status.Ready {
			blockingGates = append(blockingGates, *g)
		}
	}

	if len(blockingGates) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Blocking Policy Gates")
		_, _ = fmt.Fprintln(w, strings.Repeat("─", 72))
		gtw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(gtw, "GATE\tENV\tEXPRESSION\tREASON\tLAST CHECKED")
		for i := range blockingGates {
			g := &blockingGates[i]
			// Gate environment from label (set by translator) or fall back to name.
			env := g.Labels["kardinal.io/environment"]

			expr := g.Spec.Expression
			if len(expr) > 40 {
				expr = expr[:37] + "..."
			}
			reason := g.Status.Reason
			if reason == "" {
				reason = "-"
			}
			if len(reason) > 35 {
				reason = reason[:32] + "..."
			}
			lastChecked := "-"
			if g.Status.LastEvaluatedAt != nil && !g.Status.LastEvaluatedAt.IsZero() {
				lastChecked = HumanAge(g.Status.LastEvaluatedAt.Time) + " ago"
			}
			_, _ = fmt.Fprintf(gtw, "%s\t%s\t%s\t%s\t%s\n",
				g.Name, env, expr, reason, lastChecked)
		}
		_ = gtw.Flush()
	}

	// Show a hint if everything is terminal.
	allTerminal := true
	for _, r := range stepsByEnv {
		switch r.state {
		case "Verified", "Failed", "AbortedByAlarm":
			// terminal
		default:
			allTerminal = false
		}
	}
	if allTerminal && len(stepsByEnv) > 0 {
		_, _ = fmt.Fprintln(w, "\n(all steps are in a terminal state — no active promotion)")
	}

	return nil
}
