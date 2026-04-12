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
	"sort"
	"strings"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newHistoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "history <pipeline>",
		Short: "Show Bundle promotion history for a pipeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("history: %w", err)
			}
			return historyFn(cmd.OutOrStdout(), c, ns, args[0])
		},
	}
}

// historyFn is the testable implementation of history.
// It queries Bundles and PromotionSteps to build the per-environment promotion history table.
func historyFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline string) error {
	ctx := context.Background()

	var bundles v1alpha1.BundleList
	if listErr := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); listErr != nil {
		return fmt.Errorf("list bundles: %w", listErr)
	}

	// Build a set of bundle names for this pipeline.
	bundleSet := make(map[string]v1alpha1.Bundle) // name → Bundle
	for _, b := range bundles.Items {
		if b.Spec.Pipeline == pipeline {
			bundleSet[b.Name] = b
		}
	}

	if len(bundleSet) == 0 {
		if _, err := fmt.Fprintf(w, "No bundles found for pipeline %q\n", pipeline); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		return nil
	}

	// List PromotionSteps for this pipeline.
	var steps v1alpha1.PromotionStepList
	if listErr := c.List(ctx, &steps, sigs_client.InNamespace(ns)); listErr != nil {
		return fmt.Errorf("list promotion steps: %w", listErr)
	}

	// Build history rows from PromotionSteps. Use one row per bundle×env pair,
	// keeping the latest-created step for each combination.
	type rowKey struct {
		bundle string
		env    string
	}
	rowMap := make(map[rowKey]v1alpha1.PromotionStep)

	for _, step := range steps.Items {
		if _, ok := bundleSet[step.Spec.BundleName]; !ok {
			continue // not for this pipeline
		}
		key := rowKey{bundle: step.Spec.BundleName, env: step.Spec.Environment}
		existing, exists := rowMap[key]
		if !exists || step.CreationTimestamp.After(existing.CreationTimestamp.Time) {
			rowMap[key] = step
		}
	}

	// If no steps exist yet, fall back to listing bundles only.
	if len(rowMap) == 0 {
		// Sort bundles newest-first.
		var sorted []v1alpha1.Bundle
		for _, b := range bundleSet {
			sorted = append(sorted, b)
		}
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].CreationTimestamp.After(sorted[j].CreationTimestamp.Time)
		})
		var rows []HistoryRow
		for _, b := range sorted {
			action := "promote"
			if b.Spec.Provenance != nil && b.Spec.Provenance.RollbackOf != "" {
				action = "rollback"
			}
			rows = append(rows, HistoryRow{
				Bundle:    b.Name,
				Action:    action,
				Env:       "--",
				PR:        "--",
				Approver:  "--",
				Duration:  "--",
				Timestamp: b.CreationTimestamp.UTC().Format("2006-01-02 15:04"),
			})
		}
		return FormatHistoryTable(w, rows)
	}

	// Convert rowMap to sorted HistoryRow slice.
	var rows []HistoryRow
	for _, step := range rowMap {
		b := bundleSet[step.Spec.BundleName]
		action := "promote"
		if b.Spec.Provenance != nil && b.Spec.Provenance.RollbackOf != "" {
			action = "rollback"
		}

		prDisplay := "--"
		if step.Status.PRURL != "" {
			prDisplay = extractPRNumber(step.Status.PRURL)
		}

		approver := "(auto)"
		if mergedBy, ok := step.Status.Outputs["mergedBy"]; ok && mergedBy != "" {
			approver = mergedBy
		} else if step.Status.PRURL != "" && step.Status.State != "Verified" {
			approver = "--"
		}

		duration := "--"
		if !step.CreationTimestamp.IsZero() {
			duration = HumanAge(step.CreationTimestamp.Time)
		}

		timestamp := "--"
		if !step.CreationTimestamp.IsZero() {
			timestamp = step.CreationTimestamp.UTC().Format("2006-01-02 15:04")
		}

		rows = append(rows, HistoryRow{
			Bundle:    step.Spec.BundleName,
			Action:    action,
			Env:       step.Spec.Environment,
			PR:        prDisplay,
			Approver:  approver,
			Duration:  duration,
			Timestamp: timestamp,
		})
	}

	// Sort by timestamp descending (newest first), then bundle, then env.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Timestamp != rows[j].Timestamp {
			return rows[i].Timestamp > rows[j].Timestamp
		}
		if rows[i].Bundle != rows[j].Bundle {
			return rows[i].Bundle > rows[j].Bundle
		}
		return rows[i].Env < rows[j].Env
	})

	return FormatHistoryTable(w, rows)
}

// extractPRNumber extracts the PR number from a GitHub PR URL and formats it as "#N".
// Falls back to returning the URL unchanged if no number is found.
func extractPRNumber(prURL string) string {
	// GitHub PR URL format: https://github.com/<owner>/<repo>/pull/<number>
	idx := strings.LastIndex(prURL, "/")
	if idx >= 0 && idx < len(prURL)-1 {
		num := prURL[idx+1:]
		if num != "" {
			return "#" + num
		}
	}
	return prURL
}
