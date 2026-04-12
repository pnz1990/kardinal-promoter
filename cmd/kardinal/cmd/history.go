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
  ACTION      promote or rollback
  ENV         Target environment
  PR          Pull request number or --
  DURATION    Time to complete (from step creation to Verified)
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

	rows := buildHistoryRows(steps.Items, envFilter, limit)

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

// buildHistoryRows converts PromotionSteps into history rows.
// Only steps that have reached a terminal state (Verified or Failed) produce rows.
// Steps still in progress are included with their current state.
func buildHistoryRows(steps []v1alpha1.PromotionStep, envFilter string, limit int) []HistoryRow {
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

		action := deriveAction(s)
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

// deriveAction determines whether a step is a promote or rollback action.
func deriveAction(s v1alpha1.PromotionStep) string {
	if s.Labels["kardinal.io/rollback"] == "true" {
		return "rollback"
	}
	return "promote"
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

// deriveDuration estimates duration as age since creation (a proxy when no completion time is stored).
// Returns "--" for in-progress steps.
func deriveDuration(s v1alpha1.PromotionStep) string {
	state := s.Status.State
	if state == "" || state == "Pending" || state == "Promoting" ||
		state == "WaitingForMerge" || state == "HealthChecking" {
		return "..."
	}
	// Completed: use the step age as a proxy (creation → now).
	return HumanAge(s.CreationTimestamp.Time)
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
