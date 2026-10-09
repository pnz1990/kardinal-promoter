// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package cmd implements the kardinal metrics command.
package cmd

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func newMetricsCmd() *cobra.Command {
	var (
		pipelineFlag string
		envFlag      string
		daysFlag     int
	)

	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "Show promotion metrics (DORA-style) for a pipeline",
		Long: `Show promotion performance metrics for a pipeline.

The metrics are computed from the Bundles created in the last --days days and
their PromotionSteps in --env:
  bundles_total          Bundles created in the window
  deployment_frequency   steps Verified in --env per day
  lead_time_avg          mean time from Bundle creation to Verified in --env
  change_fail_rate       Failed Bundles / bundles_total
  rollback_count         rollback Bundles in the window

When --env is the pipeline's last environment and --days is 30 (the defaults,
whether the flags are given or not), the controller's metrics from
Pipeline.status.deploymentMetrics are shown instead when present: rollouts_last_30d, p50/p90_commit_to_prod,
auto_rollback_rate, operator_intervention_rate and stale_prod_days, over
the last 30 Bundles Verified in the last environment, and change_failure_rate
and time_to_restore (DORA stability) over the last 30 deployments to it.

Example:
  kardinal metrics --pipeline nginx-demo
  kardinal metrics --pipeline nginx-demo --env prod --days 7`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("metrics: %w", err)
			}
			return metricsFn(cmd.OutOrStdout(), c, ns, pipelineFlag, envFlag, daysFlag, time.Now().UTC())
		},
	}

	cmd.Flags().StringVar(&pipelineFlag, "pipeline", "", "Pipeline name (required)")
	cmd.Flags().StringVar(&envFlag, "env", "", "Target environment (default: the pipeline's last environment)")
	cmd.Flags().IntVar(&daysFlag, "days", 30, "Lookback period in days")
	_ = cmd.MarkFlagRequired("pipeline")

	return cmd
}

// metricsFn is the testable implementation. env "" means the pipeline's last
// environment.
func metricsFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline, env string,
	days int, now time.Time) error {
	ctx := context.Background()
	if days <= 0 {
		return fmt.Errorf("--days must be positive, got %d", days)
	}

	var p v1alpha1.Pipeline
	if err := c.Get(ctx, sigs_client.ObjectKey{Name: pipeline, Namespace: ns}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("pipeline %q not found in namespace %q", pipeline, ns)
		}
		return fmt.Errorf("get pipeline %q: %w", pipeline, err)
	}
	finalEnv := ""
	if n := len(p.Spec.Environments); n > 0 {
		finalEnv = p.Spec.Environments[n-1].Name
	}
	if env == "" {
		env = finalEnv
	}

	// The controller's Pipeline.status.deploymentMetrics cover the last
	// environment over its own 30-day/30-Bundle window: use them only when
	// that is what was asked for.
	if dm := p.Status.DeploymentMetrics; dm != nil && dm.SampleSize > 0 && env == finalEnv && days == 30 {
		return renderFromCRD(w, pipeline, env, dm, now)
	}

	// Compute from Bundle + PromotionStep CRD reads.
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)

	// List all bundles for this pipeline.
	var bundleList v1alpha1.BundleList
	if err := c.List(ctx, &bundleList, sigs_client.InNamespace(ns)); err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}

	// List all PromotionSteps for this pipeline.
	var stepList v1alpha1.PromotionStepList
	if err := c.List(ctx, &stepList,
		sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
	); err != nil {
		return fmt.Errorf("list steps: %w", err)
	}
	// Retired Bundles (#1492) keep their steps in status.retiredSteps.
	stepList.Items = lifecycle.AddRetiredSteps(stepList.Items, bundleList.Items, map[string]string{"kardinal.io/pipeline": pipeline})

	// Filter bundles for this pipeline in the lookback window.
	var pipelineBundles []v1alpha1.Bundle
	for _, b := range bundleList.Items {
		if b.Spec.Pipeline != pipeline {
			continue
		}
		if b.CreationTimestamp.UTC().Before(cutoff) {
			continue
		}
		pipelineBundles = append(pipelineBundles, b)
	}

	// Build map: bundleName → verified time in target env
	type stepKey struct{ bundle, env string }
	verifiedAt := make(map[stepKey]time.Time)
	for _, s := range stepList.Items {
		if s.Status.State == "Verified" && s.Spec.Environment == env {
			bundle := s.Spec.BundleName
			// Use the step's last transition time — fall back to now.
			var t time.Time
			for _, cond := range s.Status.Conditions {
				if cond.Type == "Verified" {
					t = cond.LastTransitionTime.Time
					break
				}
			}
			if t.IsZero() {
				// approximate with the step's creation time + some offset
				t = s.CreationTimestamp.UTC()
			}
			key := stepKey{bundle, env}
			if existing, ok := verifiedAt[key]; !ok || t.After(existing) {
				verifiedAt[key] = t
			}
		}
	}

	// Compute metrics.
	var deployCount int
	var totalLeadTime time.Duration
	var leadTimeSamples int
	var failCount int
	var rollbackCount int

	// Count verified deployments to target env from PromotionSteps directly.
	// This is more accurate than bundle.phase which only shows "Verified" when
	// ALL environments complete.
	for _, s := range stepList.Items {
		if s.Spec.Environment != env || s.Status.State != "Verified" {
			continue
		}
		// Only count steps within the lookback window.
		verifyTime := verifiedAt[stepKey{s.Spec.BundleName, env}]
		if verifyTime.IsZero() || verifyTime.Before(cutoff) {
			continue
		}
		deployCount++
	}

	// Lead time and other bundle-level metrics.
	for _, b := range pipelineBundles {
		if vt, ok := verifiedAt[stepKey{b.Name, env}]; ok && !vt.IsZero() {
			lead := vt.Sub(b.CreationTimestamp.UTC())
			if lead > 0 {
				totalLeadTime += lead
				leadTimeSamples++
			}
		}
		if b.Status.Phase == "Failed" {
			failCount++
		}
		if b.Spec.Provenance != nil && b.Spec.Provenance.RollbackOf != "" {
			rollbackCount++
		}
	}

	// Deployment frequency: deployments / lookback days
	deployFreq := float64(deployCount) / float64(days)

	// Fail rate
	failRate := 0.0
	total := len(pipelineBundles)
	if total > 0 {
		failRate = float64(failCount) / float64(total) * 100
	}

	// Average lead time
	var avgLead time.Duration
	if leadTimeSamples > 0 {
		avgLead = totalLeadTime / time.Duration(leadTimeSamples)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintf(tw, "METRIC\tVALUE\tNOTES\n")
	_, _ = fmt.Fprintf(tw, "pipeline\t%s\t(last %d days)\n", pipeline, days)
	_, _ = fmt.Fprintf(tw, "target_env\t%s\t\n", env)
	_, _ = fmt.Fprintf(tw, "bundles_total\t%d\t\n", total)
	_, _ = fmt.Fprintf(tw, "deployment_frequency\t%.2f/day\t(%d verified in target env)\n", deployFreq, deployCount)

	if leadTimeSamples > 0 {
		_, _ = fmt.Fprintf(tw, "lead_time_avg\t%s\t(creation → %s verified, %d samples)\n",
			formatDuration(avgLead), env, leadTimeSamples)
	} else {
		_, _ = fmt.Fprintf(tw, "lead_time_avg\t-\t(no completed promotions to %s in window)\n", env)
	}

	_, _ = fmt.Fprintf(tw, "change_fail_rate\t%.1f%%\t(%d failed / %d total)\n", failRate, failCount, total)
	_, _ = fmt.Fprintf(tw, "rollback_count\t%d\t\n", rollbackCount)

	return tw.Flush()
}

// formatDuration returns a human-readable duration (hours, minutes, or seconds).
func formatDuration(d time.Duration) string {
	if d >= 24*time.Hour {
		days := int(d.Hours() / 24)
		h := int(d.Hours()) % 24
		return fmt.Sprintf("%dd%dh", days, h)
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// renderFromCRD renders the pre-computed PipelineDeploymentMetrics from CRD status.
// Used when Pipeline.status.deploymentMetrics is populated by the PipelineReconciler.
func renderFromCRD(w interface{ Write([]byte) (int, error) }, pipelineName, env string,
	dm *v1alpha1.PipelineDeploymentMetrics, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintf(tw, "METRIC\tVALUE\tNOTES\n")
	_, _ = fmt.Fprintf(tw, "pipeline\t%s\t(controller: last %d bundles verified in %s)\n",
		pipelineName, dm.SampleSize, env)
	_, _ = fmt.Fprintf(tw, "target_env\t%s\t\n", env)
	_, _ = fmt.Fprintf(tw, "rollouts_last_30d\t%d\t\n", dm.RolloutsLast30Days)
	if dm.SampleSize > 0 {
		_, _ = fmt.Fprintf(tw, "p50_commit_to_prod\t%dm\t\n", dm.P50CommitToProdMinutes)
		_, _ = fmt.Fprintf(tw, "p90_commit_to_prod\t%dm\t\n", dm.P90CommitToProdMinutes)
	} else {
		_, _ = fmt.Fprintf(tw, "p50_commit_to_prod\t-\t(nothing Verified yet)\n")
		_, _ = fmt.Fprintf(tw, "p90_commit_to_prod\t-\t(nothing Verified yet)\n")
	}
	_, _ = fmt.Fprintf(tw, "auto_rollback_rate\t%.1f%%\t(%d per thousand)\n",
		float64(dm.AutoRollbackRateMillis)/10, dm.AutoRollbackRateMillis)
	_, _ = fmt.Fprintf(tw, "operator_intervention_rate\t%.1f%%\t(%d per thousand)\n",
		float64(dm.OperatorInterventionRateMillis)/10, dm.OperatorInterventionRateMillis)
	if dm.SampleSize > 0 {
		_, _ = fmt.Fprintf(tw, "stale_prod_days\t%d\t\n", dm.StaleProdDays)
	} else {
		_, _ = fmt.Fprintf(tw, "stale_prod_days\t-\t(nothing Verified yet)\n")
	}
	_, _ = fmt.Fprintf(tw, "change_failure_rate\t%.1f%%\t(%d of %d deployments failed)\n",
		float64(dm.ChangeFailureRateMillis)/10, dm.FailedDeployments, dm.Deployments)
	if dm.RestoredFailures > 0 {
		_, _ = fmt.Fprintf(tw, "time_to_restore\t%dm\t(mean of %d restored failures)\n",
			dm.MeanTimeToRestoreMinutes, dm.RestoredFailures)
	} else {
		_, _ = fmt.Fprintf(tw, "time_to_restore\t-\t(no restored failure)\n")
	}
	if dm.ComputedAt != nil {
		_, _ = fmt.Fprintf(tw, "metrics_age\t%s\t(last computed by controller)\n",
			formatDuration(now.Sub(dm.ComputedAt.Time)))
	}
	return tw.Flush()
}
