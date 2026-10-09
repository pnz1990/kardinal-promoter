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
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func newStatusCmd() *cobra.Command {
	var controllerNS string
	cmd := &cobra.Command{
		Use:   "status [pipeline]",
		Short: "Show controller health or per-pipeline in-flight promotion details",
		Long: `Show the health of the kardinal controller and cluster resource summary.

When called without arguments: displays the controller version (the
kardinal-version ConfigMap in --controller-namespace), the pipeline count with
any Degraded pipelines, and the bundle count (active = Available or Promoting).

When called with a pipeline name: shows in-flight promotion details for that
pipeline — the current bundle per environment (the newest bundle that is not
Superseded and has a PromotionStep there, or a gate instance there and has not
failed; see kardinal explain), its PromotionSteps (one row per environment it
has a step in, ▶ marking a step that is Promoting, WaitingForMerge or
HealthChecking, with the Bundle each row belongs to, the step it is on, and the
last 40 characters of the step's PR URL, open or merged; REGION is - unless the
step was created by a Graph built before multi-region fan-out was removed;
spec.regions is deprecated), the Bundle deployed in every environment (the
one whose change landed there last, as kardinal rollback judges it, with its
image tags or config commit, and, as in kardinal explain, the config or
image Bundle the rest of what runs there came from; "none" when no change has
landed yet), and the
PolicyGates holding it back (with their CEL expression cut to 40 characters,
current reason and when each was last checked). A gate is listed as blocking only while it holds the bundle back: it
is not ready and either every upstream environment is Verified for that bundle
and the bundle has no PromotionStep in the gate's environment yet, or the
bundle's Pending PromotionStep there waits on it. A gate of an environment the
bundle has not reached yet is not listed. This is the first command to run
when a promotion is stuck.

Examples:
  # Cluster-level summary
  kardinal status

  # Per-pipeline in-flight view
  kardinal status nginx-demo

For detailed gate diagnostics, use 'kardinal explain <pipeline>'.
For step-level log output, use 'kardinal logs <pipeline>'.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return err // buildClient already provides actionable message
			}
			if len(args) == 1 {
				return statusPipelineWriter(cmd.OutOrStdout(), c, ns, args[0])
			}
			return statusSummaryFn(cmd.OutOrStdout(), c, controllerNS)
		},
	}
	cmd.Flags().StringVar(&controllerNS, "controller-namespace", defaultControllerNamespace,
		"Namespace kardinal-promoter is installed in")
	return cmd
}

// statusSummaryFn writes the cluster-level controller summary.
func statusSummaryFn(out io.Writer, c sigs_client.Reader, controllerNS string) error {
	ctx := context.Background()
	ctrlVersion := controllerVersion(ctx, c, controllerNS)

	var pipelines v1alpha1.PipelineList
	if err := c.List(ctx, &pipelines); err != nil {
		return fmt.Errorf("list pipelines: %w", err)
	}
	var bundles v1alpha1.BundleList
	if err := c.List(ctx, &bundles); err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}

	activeBundles := 0
	for _, b := range bundles.Items {
		if b.Status.Phase == "Available" || b.Status.Phase == "Promoting" {
			activeBundles++
		}
	}
	var degraded []string
	for _, p := range pipelines.Items {
		if p.Status.Phase == "Degraded" {
			degraded = append(degraded, p.Namespace+"/"+p.Name)
		}
	}
	sort.Strings(degraded)

	_, _ = fmt.Fprintf(out, "Controller:  %s\n", ctrlVersion)
	_, _ = fmt.Fprintf(out, "Pipelines:   %d", len(pipelines.Items))
	if len(degraded) > 0 {
		_, _ = fmt.Fprintf(out, " (%d degraded: %s)", len(degraded), strings.Join(degraded, ", "))
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "Bundles:     %d (%d active)\n", len(bundles.Items), activeBundles)
	if len(degraded) > 0 {
		_, _ = fmt.Fprintf(out,
			"\nWarning: %d pipeline(s) Degraded — run 'kardinal get pipelines' for details\n", len(degraded))
	}
	return nil
}

// statusPipelineWriter renders the per-pipeline status to w using client c.
// It is separate from the command so tests can pass a fake client.
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

	var bundles v1alpha1.BundleList
	if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}
	// Retired Bundles (#1492) keep their steps in status.retiredSteps.
	steps.Items = lifecycle.AddRetiredSteps(steps.Items, bundles.Items, map[string]string{"kardinal.io/pipeline": pipeline})

	active := currentBundleByEnv(bundles.Items, steps.Items, gates.Items)

	// One row per PromotionStep of the active Bundle, so the regions of a
	// multi-region environment each get a row.
	type stepRow struct {
		env        string
		region     string
		bundle     string
		state      string
		activeStep string // currently executing step (from status.steps[])
		prURL      string
		age        string
	}
	var rows []stepRow
	stepped := make(map[string]bool) // env with a step of its active Bundle
	for i := range steps.Items {
		s := &steps.Items[i]
		env := s.Spec.Environment
		if s.Spec.BundleName != active[env] {
			continue
		}
		stepped[env] = true

		// Find the currently executing step (first non-terminal step in status.steps).
		activeStep := "-"
		for _, ss := range s.Status.Steps {
			if ss.State != "Completed" && ss.State != "Failed" && ss.State != "" {
				activeStep = ss.Name
				break
			}
		}
		age := "-"
		if !s.CreationTimestamp.IsZero() {
			age = HumanAge(s.CreationTimestamp.Time)
		}
		rows = append(rows, stepRow{
			env:        env,
			region:     orDash(s.Spec.Region), //nolint:staticcheck // SA1019: shows steps from a Graph built before regions were removed
			bundle:     s.Spec.BundleName,
			state:      stepState(*s),
			activeStep: activeStep,
			prURL:      orDash(s.Status.PRURL),
			age:        age,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].env != rows[j].env {
			return rows[i].env < rows[j].env
		}
		return rows[i].region < rows[j].region
	})

	// A gate blocks when it belongs to the current Bundle of its environment
	// and holds that Bundle back there (graph.GateHolds, the rule the UI's
	// blockerCount uses): it is not ready, and either the Bundle has no step
	// there yet and every upstream step is Verified, or a Pending step there
	// waits on it (E2E-R18).
	byName := make(map[string]*v1alpha1.Bundle, len(bundles.Items))
	for i := range bundles.Items {
		byName[bundles.Items[i].Name] = &bundles.Items[i]
	}
	var blockingGates []v1alpha1.PolicyGate
	for i := range gates.Items {
		g := &gates.Items[i]
		env, bundle := g.Labels["kardinal.io/environment"], g.Labels["kardinal.io/bundle"]
		if bundle == "" || bundle != active[env] {
			continue
		}
		if b := byName[bundle]; b != nil && graph.GateHolds(&pl, b, g, steps.Items) {
			blockingGates = append(blockingGates, *g)
		}
	}
	sort.Slice(blockingGates, func(i, j int) bool {
		ei, ej := blockingGates[i].Labels["kardinal.io/environment"], blockingGates[j].Labels["kardinal.io/environment"]
		if ei != ej {
			return ei < ej
		}
		return gateDisplayName(blockingGates[i]) < gateDisplayName(blockingGates[j])
	})

	if len(rows) == 0 && len(blockingGates) == 0 {
		_, _ = fmt.Fprintln(w, "No active promotions.")
		return nil
	}

	// Print active bundle summary.
	bundleNames := map[string]struct{}{}
	for _, b := range active {
		bundleNames[b] = struct{}{}
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
	_, _ = fmt.Fprintln(tw, "ENVIRONMENT\tREGION\tBUNDLE\tSTATE\tACTIVE STEP\tPR\tAGE")
	for _, r := range rows {
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
		_, _ = fmt.Fprintf(tw, "%s%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			marker, r.env, r.region, r.bundle, r.state, r.activeStep, prDisplay, r.age)
	}
	_ = tw.Flush()

	// What runs in each environment now, whether or not the active Bundle
	// has reached it (lifecycle.DeployedBundle, the Bundle rollback starts
	// from).
	envs := slices.Sorted(slices.Values(pipelineEnvNames(&pl)))
	deployed := deployedBundles(steps.Items, pipeline, envs, byName)
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Deployed")
	_, _ = fmt.Fprintln(w, strings.Repeat("─", 72))
	dtw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(dtw, "ENVIRONMENT\tBUNDLE")
	for _, env := range envs {
		_, _ = fmt.Fprintf(dtw, "%s\t%s\n", env, deployedLabelOf(deployed[env], byName))
	}
	_ = dtw.Flush()

	if len(blockingGates) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Blocking Policy Gates")
		_, _ = fmt.Fprintln(w, strings.Repeat("─", 72))
		gtw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(gtw, "GATE\tENV\tEXPRESSION\tREASON\tLAST CHECKED")
		for i := range blockingGates {
			g := &blockingGates[i]
			lastChecked := "-"
			if g.Status.LastEvaluatedAt != nil && !g.Status.LastEvaluatedAt.IsZero() {
				lastChecked = HumanAge(g.Status.LastEvaluatedAt.Time) + " ago"
			}
			// The gate's message is shown whole: it is what its author wrote
			// for this table. The CEL detail is in kardinal explain.
			reason := graph.BlockedMessage(g)
			if reason == "" {
				reason = truncateRunes(orDash(g.Status.Reason), 35)
			}
			_, _ = fmt.Fprintf(gtw, "%s\t%s\t%s\t%s\t%s\n",
				gateDisplayName(*g), g.Labels["kardinal.io/environment"],
				truncateRunes(g.Spec.Expression, 40), reason, lastChecked)
		}
		_ = gtw.Flush()
	}

	// Show a hint if everything is terminal. An environment whose current
	// Bundle has not started there yet (only its gate instances exist) is
	// still to come, so the promotion is not over.
	allTerminal := len(blockingGates) == 0
	for _, r := range rows {
		if !terminalStates[r.state] {
			allTerminal = false
		}
	}
	for env := range active {
		if !stepped[env] {
			allTerminal = false
		}
	}
	if allTerminal && len(rows) > 0 {
		_, _ = fmt.Fprintln(w, "\n(all steps are in a terminal state — no active promotion)")
	}

	return nil
}
