// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// terminalStates are PromotionStep states that indicate the step will not
// progress further. The follow loop exits when all filtered steps are terminal.
var terminalStates = map[string]bool{
	"Verified":       true,
	"Failed":         true,
	"Superseded":     true,
	"AbortedByAlarm": true,
}

func newLogsCmd() *cobra.Command {
	var (
		envFlag    string
		bundleFlag string
		followFlag bool
	)

	cmd := &cobra.Command{
		Use:   "logs <pipeline>",
		Short: "Show promotion step execution logs for a pipeline (Kargo parity)",
		Long: `Show the execution history and output of PromotionSteps for a pipeline.

For each active PromotionStep, shows:
  - Current state (Promoting, WaitingForMerge, HealthChecking, Verified, Failed)
  - Step message (error details, health check results, PR URLs)
  - Step outputs (branch name, PR URL, PR number)
  - Conditions from the status

Use --follow (-f) to stream step progress in real time, polling every 2 seconds
until all steps reach a terminal state (Verified, Failed, or Superseded).

Example:
  kardinal logs nginx-demo
  kardinal logs nginx-demo --env prod
  kardinal logs nginx-demo --bundle nginx-demo-v1-29-0
  kardinal logs nginx-demo --follow`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("logs: %w", err)
			}
			if followFlag {
				return logsFollowFn(cmd.Context(), cmd.OutOrStdout(), c, ns, args[0], envFlag, bundleFlag)
			}
			return logsFn(cmd.OutOrStdout(), c, ns, args[0], envFlag, bundleFlag)
		},
	}

	cmd.Flags().StringVar(&envFlag, "env", "", "Filter by environment")
	cmd.Flags().StringVar(&bundleFlag, "bundle", "", "Show logs for a specific bundle (default: most recent active)")
	cmd.Flags().BoolVarP(&followFlag, "follow", "f", false, "Stream step progress, polling every 2s until terminal state")

	return cmd
}

// LogsFnForTest is an exported wrapper for testing logsFn.
func LogsFnForTest(w io.Writer, c sigs_client.Client, ns, pipeline, envFilter, bundleFilter string) error {
	return logsFn(w, c, ns, pipeline, envFilter, bundleFilter)
}

// logsFollowFn implements the --follow streaming mode.
// It polls every 2 seconds and prints only new status.steps[] entries since the
// last poll. Exits when all filtered PromotionSteps reach a terminal state,
// or when the context is cancelled (SIGINT).
func logsFollowFn(ctx context.Context, w io.Writer, c sigs_client.Client, ns, pipeline, envFilter, bundleFilter string) error {
	// Catch SIGINT for clean Ctrl+C exit.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// cursor tracks the last-seen step count per PromotionStep name.
	cursor := make(map[string]int)

	_, _ = fmt.Fprintf(w, "Following logs for pipeline %s (Ctrl+C to stop)...\n", pipeline)

	for {
		select {
		case <-sigCtx.Done():
			_, _ = fmt.Fprintln(w, "\nStopped.")
			return nil
		default:
		}

		filtered, err := fetchFilteredSteps(sigCtx, c, ns, pipeline, envFilter, bundleFilter)
		if err != nil {
			return fmt.Errorf("follow: %w", err)
		}

		if len(filtered) == 0 {
			_, _ = fmt.Fprintf(w, "No promotion steps found for pipeline %s\n", pipeline)
		}

		// Print new step entries since last poll.
		for _, s := range filtered {
			key := s.Spec.Environment + "/" + s.Spec.BundleName
			prev := cursor[key]
			newSteps := s.Status.Steps
			if len(newSteps) > prev {
				for _, step := range newSteps[prev:] {
					dur := "-"
					if step.DurationMs > 0 {
						dur = fmt.Sprintf("%.1fs", float64(step.DurationMs)/1000.0)
					}
					_, _ = fmt.Fprintf(w, "[%s/%s] %-25s %-15s %s %s\n",
						pipeline, s.Spec.Environment,
						step.Name, string(step.State), dur, step.Message)
				}
				cursor[key] = len(newSteps)
			}

			// Print state change when step transitions to terminal.
			if prev < len(newSteps) || cursor[key] == 0 {
				if terminalStates[s.Status.State] && prev == cursor[key] {
					_, _ = fmt.Fprintf(w, "[%s/%s] → %s\n", pipeline, s.Spec.Environment, s.Status.State)
				}
			}
		}

		// Check if all steps are terminal.
		if len(filtered) > 0 && allTerminal(filtered) {
			_, _ = fmt.Fprintln(w, "All steps reached terminal state.")
			return nil
		}

		// Wait 2 seconds before next poll.
		select {
		case <-sigCtx.Done():
			_, _ = fmt.Fprintln(w, "\nStopped.")
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

// allTerminal returns true when every PromotionStep in the list is in a terminal state.
func allTerminal(steps []v1alpha1.PromotionStep) bool {
	for _, s := range steps {
		if !terminalStates[s.Status.State] {
			return false
		}
	}
	return true
}

// fetchFilteredSteps retrieves and filters PromotionSteps for the given pipeline.
func fetchFilteredSteps(ctx context.Context, c sigs_client.Client, ns, pipeline, envFilter, bundleFilter string) ([]v1alpha1.PromotionStep, error) {
	var stepList v1alpha1.PromotionStepList
	if err := c.List(ctx, &stepList,
		sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
	); err != nil {
		return nil, fmt.Errorf("list promotion steps: %w", err)
	}

	var filtered []v1alpha1.PromotionStep
	for _, s := range stepList.Items {
		if envFilter != "" && s.Spec.Environment != envFilter {
			continue
		}
		if bundleFilter != "" && s.Spec.BundleName != bundleFilter {
			continue
		}
		filtered = append(filtered, s)
	}

	if bundleFilter == "" {
		var bundles v1alpha1.BundleList
		if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err == nil {
			activeBundles := make(map[string]bool)
			for _, b := range bundles.Items {
				if b.Spec.Pipeline == pipeline && b.Status.Phase != "Superseded" {
					activeBundles[b.Name] = true
				}
			}
			if len(activeBundles) > 0 {
				var active []v1alpha1.PromotionStep
				for _, s := range filtered {
					if activeBundles[s.Labels["kardinal.io/bundle"]] {
						active = append(active, s)
					}
				}
				if len(active) > 0 {
					filtered = active
				}
			}
		}
	}

	sort.Slice(filtered, func(i, j int) bool {
		ei, ej := filtered[i].Spec.Environment, filtered[j].Spec.Environment
		if ei != ej {
			return ei < ej
		}
		return filtered[i].CreationTimestamp.Before(&filtered[j].CreationTimestamp)
	})

	return filtered, nil
}

func logsFn(w io.Writer, c sigs_client.Client, ns, pipeline, envFilter, bundleFilter string) error {
	ctx := context.Background()

	filtered, err := fetchFilteredSteps(ctx, c, ns, pipeline, envFilter, bundleFilter)
	if err != nil {
		return err
	}

	if len(filtered) == 0 {
		_, _ = fmt.Fprintf(w, "No promotion steps found for pipeline %s", pipeline)
		if envFilter != "" {
			_, _ = fmt.Fprintf(w, " (env=%s)", envFilter)
		}
		_, _ = fmt.Fprintln(w)
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, s := range filtered {
		_, _ = fmt.Fprintf(tw, "=== %s/%s (%s) [%s] ===\n",
			pipeline, s.Spec.Environment, s.Spec.BundleName, s.Status.State)

		// Message (execution log)
		if s.Status.Message != "" {
			_, _ = fmt.Fprintf(tw, "  message:\t%s\n", s.Status.Message)
		}

		// PR URL
		if s.Status.PRURL != "" {
			_, _ = fmt.Fprintf(tw, "  pr_url:\t%s\n", s.Status.PRURL)
		}

		// Step outputs
		if len(s.Status.Outputs) > 0 {
			_, _ = fmt.Fprintf(tw, "  outputs:\n")
			for k, v := range s.Status.Outputs {
				_, _ = fmt.Fprintf(tw, "    %s:\t%s\n", k, v)
			}
		}

		// Health failure count
		if s.Status.ConsecutiveHealthFailures > 0 {
			_, _ = fmt.Fprintf(tw, "  health_failures:\t%d\n", s.Status.ConsecutiveHealthFailures)
		}

		// Conditions
		for _, cond := range s.Status.Conditions {
			_, _ = fmt.Fprintf(tw, "  [%s] %s: %s\n",
				cond.LastTransitionTime.UTC().Format("15:04:05"),
				cond.Type,
				cond.Message)
		}

		// Per-step execution history (status.steps[])
		if len(s.Status.Steps) > 0 {
			_, _ = fmt.Fprintf(tw, "  steps:\n")
			_, _ = fmt.Fprintf(tw, "    STEP\tSTATE\tDURATION\tMESSAGE\n")
			_, _ = fmt.Fprintf(tw, "    ----\t-----\t--------\t-------\n")
			for _, step := range s.Status.Steps {
				dur := "-"
				if step.DurationMs > 0 {
					dur = fmt.Sprintf("%.1fs", float64(step.DurationMs)/1000.0)
				}
				msg := step.Message
				if len(msg) > 80 {
					msg = msg[:80] + "..."
				}
				_, _ = fmt.Fprintf(tw, "    %s\t%s\t%s\t%s\n",
					step.Name, string(step.State), dur, msg)
			}
		}

		_, _ = fmt.Fprintln(tw)
	}

	return tw.Flush()
}
