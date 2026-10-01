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

package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newHistoryCmd() *cobra.Command {
	var envFlag string
	var limitFlag int

	cmd := &cobra.Command{
		Use:   "history <pipeline>",
		Short: "Show Bundle promotion history for a pipeline",
		Long: `Show the promotion history for a Pipeline, including which Bundles
were promoted to which environments and when.

Output columns:
  BUNDLE      Bundle name
  ACTION      promote, or rollback when the Bundle is a rollback Bundle
  ENV         Target environment
  PR          Pull request number or --
  DURATION    Time from step creation to Verified (or to its last completed
              step when it failed); ... while running, -- when unknown
  TIMESTAMP   When the step was created`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("history: %w", err)
			}
			return historyFn(cmd.OutOrStdout(), c, ns, args[0], envFlag, limitFlag)
		},
	}

	cmd.Flags().StringVar(&envFlag, "env", "", "Filter by environment name")
	cmd.Flags().IntVar(&limitFlag, "limit", 20, "Maximum number of entries to show")
	return cmd
}

// HistoryRow represents one row in the history output.
type HistoryRow struct {
	Bundle    string
	Action    string
	Env       string
	PR        string
	Duration  string
	Timestamp string
}

// historyFn is the testable implementation of history.
// It builds a per-(bundle,env) row from PromotionSteps, sorted newest first.
func historyFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline, envFilter string, limit int) error {
	ctx := context.Background()

	var steps v1alpha1.PromotionStepList
	if listErr := c.List(ctx, &steps,
		sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
	); listErr != nil {
		return fmt.Errorf("list promotion steps: %w", listErr)
	}

	if len(steps.Items) == 0 {
		if _, err := fmt.Fprintf(w, "No promotion history found for pipeline %q\n", pipeline); err != nil {
			return fmt.Errorf("write empty: %w", err)
		}
		return nil
	}

	// Rollback is a property of the Bundle (spec.provenance.rollbackOf), not
	// of its PromotionSteps.
	var bundles v1alpha1.BundleList
	if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}
	rollbacks := map[string]bool{}
	for _, b := range bundles.Items {
		if b.Spec.Pipeline == pipeline && isRollbackBundle(b) {
			rollbacks[b.Name] = true
		}
	}

	rows := buildHistoryRows(steps.Items, rollbacks, envFilter, limit)

	if len(rows) == 0 {
		if envFilter != "" {
			if _, err := fmt.Fprintf(w, "No promotion history found for pipeline %q env %q\n", pipeline, envFilter); err != nil {
				return fmt.Errorf("write empty: %w", err)
			}
		} else {
			if _, err := fmt.Fprintf(w, "No promotion history found for pipeline %q\n", pipeline); err != nil {
				return fmt.Errorf("write empty: %w", err)
			}
		}
		return nil
	}

	return formatHistoryTable(w, rows)
}

// buildHistoryRows converts PromotionSteps into history rows, newest first.
// rollbacks holds the names of rollback Bundles.
func buildHistoryRows(steps []v1alpha1.PromotionStep, rollbacks map[string]bool, envFilter string, limit int) []HistoryRow {
	// Sort steps newest first by creation timestamp.
	sorted := make([]v1alpha1.PromotionStep, len(steps))
	copy(sorted, steps)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].CreationTimestamp.After(sorted[j].CreationTimestamp.Time)
	})

	var rows []HistoryRow
	for _, s := range sorted {
		if limit > 0 && len(rows) >= limit {
			break
		}

		env := s.Spec.Environment
		if envFilter != "" && env != envFilter {
			continue
		}

		action := "promote"
		if rollbacks[s.Spec.BundleName] {
			action = "rollback"
		}
		pr := derivePR(s)
		duration := deriveDuration(s)
		ts := s.CreationTimestamp.Time.UTC().Format("2006-01-02 15:04")

		rows = append(rows, HistoryRow{
			Bundle:    s.Spec.BundleName,
			Action:    action,
			Env:       env,
			PR:        pr,
			Duration:  duration,
			Timestamp: ts,
		})
	}
	return rows
}

// isRollbackBundle reports whether b was created by a rollback (CLI, UI,
// auto-rollback or RollbackPolicy all set provenance.rollbackOf and the label).
func isRollbackBundle(b v1alpha1.Bundle) bool {
	return (b.Spec.Provenance != nil && b.Spec.Provenance.RollbackOf != "") ||
		b.Labels["kardinal.io/rollback"] == "true"
}

// derivePR extracts the PR display string (e.g. "#144" or "--").
func derivePR(s v1alpha1.PromotionStep) string {
	if s.Status.PRURL == "" {
		return "--"
	}
	return shortenPRURL(s.Status.PRURL)
}

// shortenPRURL converts a full GitHub PR URL to "#NNN" format.
// Returns the raw URL if the pattern does not match.
func shortenPRURL(url string) string {
	// Extract the number after the last slash.
	n := len(url)
	if n == 0 {
		return "--"
	}
	last := url[n-1]
	if last < '0' || last > '9' {
		return url
	}
	i := n - 1
	for i > 0 && url[i-1] >= '0' && url[i-1] <= '9' {
		i--
	}
	if i > 0 && url[i-1] == '/' {
		return "#" + url[i:]
	}
	return url
}

// deriveDuration is the time from step creation to its end: the Verified
// condition's transition, else the last completed step. "..." while the step
// is running, "--" when no end time is recorded. RollingBack is an end state:
// the step stops there, and the rollback Bundle carries the promotion on.
func deriveDuration(s v1alpha1.PromotionStep) string {
	switch s.Status.State {
	case "Verified", "Failed", "AbortedByAlarm", "RollingBack":
	case "", "Pending", "Promoting", "WaitingForMerge", "HealthChecking":
		return "..."
	default:
		return "--"
	}
	var end time.Time
	if s.Status.State == "Verified" {
		for _, c := range s.Status.Conditions {
			if c.Type == "Verified" && c.Status == "True" {
				end = c.LastTransitionTime.Time
			}
		}
	}
	if end.IsZero() {
		for _, st := range s.Status.Steps {
			if st.CompletedAt != nil && st.CompletedAt.After(end) {
				end = st.CompletedAt.Time
			}
		}
	}
	if end.IsZero() || s.CreationTimestamp.IsZero() || end.Before(s.CreationTimestamp.Time) {
		return "--"
	}
	return humanDuration(end.Sub(s.CreationTimestamp.Time))
}

// formatHistoryTable writes the history table to w.
func formatHistoryTable(w io.Writer, rows []HistoryRow) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "BUNDLE\tACTION\tENV\tPR\tDURATION\tTIMESTAMP"); err != nil {
		return fmt.Errorf("write history header: %w", err)
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Bundle, r.Action, r.Env, r.PR, r.Duration, r.Timestamp); err != nil {
			return fmt.Errorf("write history row: %w", err)
		}
	}
	return tw.Flush()
}
