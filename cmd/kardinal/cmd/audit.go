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
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newAuditCmd() *cobra.Command {
	audit := &cobra.Command{
		Use:   "audit",
		Short: "Audit log commands — view and summarize promotion events",
	}
	audit.AddCommand(newAuditSummaryCmd())
	return audit
}

func newAuditSummaryCmd() *cobra.Command {
	var (
		pipeline string
		since    string
	)

	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Aggregate promotion metrics from AuditEvent records",
		Long: `Show a summary of promotion activity from the AuditEvent log.

Includes: promotion counts, success rate, average duration, gate block rate, and rollbacks.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAuditSummary(cmd, pipeline, since)
		},
	}

	cmd.Flags().StringVar(&pipeline, "pipeline", "", "Filter by pipeline name (default: all pipelines)")
	cmd.Flags().StringVar(&since, "since", "24h", "Time window for events (e.g. 24h, 7d, 30d)")

	return cmd
}

func runAuditSummary(cmd *cobra.Command, pipeline, sinceDuration string) error {
	out := cmd.OutOrStdout()

	client, ns, err := buildClient()
	if err != nil {
		return fmt.Errorf("audit summary: %w", err)
	}

	// Parse the --since duration.
	dur, err := parseSinceDuration(sinceDuration)
	if err != nil {
		return fmt.Errorf("invalid --since %q: %w", sinceDuration, err)
	}
	cutoff := metav1.NewTime(time.Now().UTC().Add(-dur))

	// List AuditEvents.
	var aeList v1alpha1.AuditEventList
	listOpts := []sigs_client.ListOption{sigs_client.InNamespace(ns)}
	if pipeline != "" {
		listOpts = append(listOpts, sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline})
	}
	if err := client.List(context.Background(), &aeList, listOpts...); err != nil {
		return fmt.Errorf("list auditevents: %w", err)
	}

	// Filter to time window.
	var events []v1alpha1.AuditEvent
	for _, ae := range aeList.Items {
		if !ae.Spec.Timestamp.Before(&cutoff) {
			events = append(events, ae)
		}
	}

	if len(events) == 0 {
		_, _ = fmt.Fprintf(out, "No audit events found in the last %s.\n", sinceDuration)
		if pipeline != "" {
			_, _ = fmt.Fprintf(out, "Pipeline filter: %s\n", pipeline)
		}
		return nil
	}

	// Compute metrics.
	type promotionKey struct{ pipeline, bundle, env string }
	startTimes := make(map[promotionKey]time.Time)

	var (
		started, succeeded, failed, superseded int
		totalDuration                          time.Duration
		durationCount                          int
		gateTotal, gateBlocked                 int
		rollbacks                              int
		pipelines                              = make(map[string]bool)
	)

	for _, ae := range events {
		pipelines[ae.Spec.PipelineName] = true
		key := promotionKey{ae.Spec.PipelineName, ae.Spec.BundleName, ae.Spec.Environment}
		switch ae.Spec.Action {
		case "PromotionStarted":
			started++
			startTimes[key] = ae.Spec.Timestamp.Time
		case "PromotionSucceeded":
			succeeded++
			if st, ok := startTimes[key]; ok {
				totalDuration += ae.Spec.Timestamp.Time.Sub(st)
				durationCount++
				delete(startTimes, key)
			}
		case "PromotionFailed":
			failed++
			delete(startTimes, key)
		case "PromotionSuperseded":
			superseded++
			delete(startTimes, key)
		case "GateEvaluated":
			gateTotal++
			if ae.Spec.Outcome == "Failure" {
				gateBlocked++
			}
		case "RollbackStarted":
			rollbacks++
		}
	}

	// Format output.
	pipelineLabel := "all pipelines"
	if pipeline != "" {
		pipelineLabel = pipeline
	} else if len(pipelines) == 1 {
		for p := range pipelines {
			pipelineLabel = p
		}
	} else if len(pipelines) > 1 {
		pipelineLabel = fmt.Sprintf("%d pipelines", len(pipelines))
	}

	_, _ = fmt.Fprintf(out, "Pipeline: %s  (last %s)\n\n", pipelineLabel, sinceDuration)

	successRate := float64(0)
	if started > 0 {
		successRate = float64(succeeded) / float64(started) * 100
	}
	_, _ = fmt.Fprintf(out, "Promotions:   %d started, %d succeeded, %d failed, %d superseded\n",
		started, succeeded, failed, superseded)
	_, _ = fmt.Fprintf(out, "Success rate: %.1f%%\n", successRate)
	if durationCount > 0 {
		avgDur := totalDuration / time.Duration(durationCount)
		_, _ = fmt.Fprintf(out, "Avg duration: %s\n", formatAuditDuration(avgDur))
	}
	_, _ = fmt.Fprintln(out)

	blockRate := float64(0)
	if gateTotal > 0 {
		blockRate = float64(gateBlocked) / float64(gateTotal) * 100
	}
	_, _ = fmt.Fprintf(out, "Gates:        %d evaluations, %d blocked (%.1f%% block rate)\n",
		gateTotal, gateBlocked, blockRate)
	_, _ = fmt.Fprintf(out, "Rollbacks:    %d triggered\n", rollbacks)

	return nil
}

// parseSinceDuration parses strings like "24h", "7d", "30d" into a time.Duration.
func parseSinceDuration(s string) (time.Duration, error) {
	if len(s) == 0 {
		return 0, fmt.Errorf("empty duration")
	}
	// Support "d" (days) as shorthand.
	if s[len(s)-1] == 'd' {
		days := s[:len(s)-1]
		var n int
		if _, err := fmt.Sscanf(days, "%d", &n); err != nil {
			return 0, fmt.Errorf("invalid days: %s", days)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// formatAuditDuration formats a duration as "Xm Ys" for display.
func formatAuditDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	mins := int(d.Minutes())
	secs := int(d.Seconds()) % 60
	if secs == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dm %ds", mins, secs)
}
