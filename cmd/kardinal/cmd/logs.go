// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newLogsCmd() *cobra.Command {
	var (
		envFlag    string
		bundleFlag string
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

Example:
  kardinal logs nginx-demo
  kardinal logs nginx-demo --env prod
  kardinal logs nginx-demo --bundle nginx-demo-v1-29-0`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("logs: %w", err)
			}
			return logsFn(cmd.OutOrStdout(), c, ns, args[0], envFlag, bundleFlag)
		},
	}

	cmd.Flags().StringVar(&envFlag, "env", "", "Filter by environment")
	cmd.Flags().StringVar(&bundleFlag, "bundle", "", "Show logs for a specific bundle (default: most recent active)")

	return cmd
}

func logsFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline, envFilter, bundleFilter string) error {
	ctx := context.Background()

	var steps v1alpha1.PromotionStepList
	if err := c.List(ctx, &steps,
		sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
	); err != nil {
		return fmt.Errorf("list promotion steps: %w", err)
	}

	// Filter by environment if specified.
	var filtered []v1alpha1.PromotionStep
	for _, s := range steps.Items {
		if envFilter != "" && s.Spec.Environment != envFilter {
			continue
		}
		if bundleFilter != "" && s.Spec.BundleName != bundleFilter {
			continue
		}
		filtered = append(filtered, s)
	}

	// If no bundle filter, keep only steps from non-Superseded bundles.
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

	if len(filtered) == 0 {
		_, _ = fmt.Fprintf(w, "No promotion steps found for pipeline %s", pipeline)
		if envFilter != "" {
			_, _ = fmt.Fprintf(w, " (env=%s)", envFilter)
		}
		_, _ = fmt.Fprintln(w)
		return nil
	}

	// Sort by environment, then by bundle creation time.
	sort.Slice(filtered, func(i, j int) bool {
		ei, ej := filtered[i].Spec.Environment, filtered[j].Spec.Environment
		if ei != ej {
			return ei < ej
		}
		return filtered[i].CreationTimestamp.Before(&filtered[j].CreationTimestamp)
	})

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
		_, _ = fmt.Fprintln(tw)
	}

	return tw.Flush()
}
