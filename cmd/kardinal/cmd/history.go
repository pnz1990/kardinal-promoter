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
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"

	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
)

func newHistoryCmd() *cobra.Command {
	var envFlag string
	var limitFlag int

	cmd := &cobra.Command{
		Use:   "history <pipeline> [<env>]",
		Short: "Show a pipeline's promotion history, newest first",
		Long: `Show which versions were promoted to (or rolled back in) each environment of
<pipeline>, newest first: the version, promote or rollback, the environment,
the PR, how long it took, and when it started.`,
		Example: `  kardinal history my-app
  kardinal history my-app prod --limit 5`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pipeline, env, err := envArgs(cmd, args, envFlag)
			if err != nil {
				return err
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("history: %w", err)
			}
			return historyFn(cmd.OutOrStdout(), c, ns, pipeline, env, limitFlag)
		},
	}

	addEnvFlag(cmd, &envFlag, "Only this environment (or give it after the pipeline)")
	cmd.Flags().IntVar(&limitFlag, "limit", 20, "Maximum number of entries to show")
	return withStructuredOutput(cmd)
}

// HistoryRow represents one row in the history output.
type HistoryRow struct {
	Bundle string
	// Version is the Bundle's version, for the table ("" when unknown).
	Version   string
	Action    string
	Env       string
	PR        string
	Duration  string
	Timestamp string
	// State, PRURL, Created, Seconds and Running are the step's, for
	// -o json|yaml: Seconds is how long it took (nil when no end is
	// recorded), Running whether it still runs.
	State   string
	PRURL   string
	Created time.Time
	Seconds *int64
	Running bool
}

// historyEntry is one entry of kardinal history -o json|yaml.
type historyEntry struct {
	Bundle string `json:"bundle"`
	// Action is promote or rollback.
	Action      string `json:"action"`
	Environment string `json:"environment"`
	// State is the step's state, as the API has it.
	State string `json:"state"`
	PRURL string `json:"prURL,omitempty"`
	// DurationSeconds is how long the step took; omitted while it runs or
	// when no end is recorded.
	DurationSeconds *int64 `json:"durationSeconds,omitempty"`
	// Running is true while the step runs.
	Running   bool      `json:"running,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// writeHistoryStructured writes rows as -o json|yaml asks.
func writeHistoryStructured(w io.Writer, rows []HistoryRow) error {
	out := make([]historyEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, historyEntry{Bundle: r.Bundle, Action: r.Action, Environment: r.Env, State: r.State,
			PRURL: r.PRURL, DurationSeconds: r.Seconds, Running: r.Running, CreatedAt: r.Created.UTC()})
	}
	return writeStructured(w, out)
}

// historyFn is the testable implementation of history.
// It builds a per-(bundle,env) row from PromotionSteps, sorted newest first.
func historyFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline, envFilter string, limit int) error {
	ctx := context.Background()

	// Retired Bundles (#1492) keep their steps in status.retiredSteps.
	items, listErr := lifecycle.ListPromotionSteps(ctx, c, ns, sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline})
	if listErr != nil {
		return fmt.Errorf("list promotion steps: %w", listErr)
	}
	steps := v1alpha1.PromotionStepList{Items: items}

	if len(steps.Items) == 0 {
		// A typo would look like an idle pipeline: say so.
		if err := checkPipelineEnv(ctx, c, ns, pipeline, envFilter); err != nil {
			return err
		}
		if structured() {
			return writeHistoryStructured(w, nil)
		}
		if _, err := fmt.Fprintf(w, "No promotion history found for pipeline %q\n", pipeline); err != nil {
			return fmt.Errorf("write empty: %w", err)
		}
		return nil
	}

	// Rollback is a property of the Bundle (spec.provenance.rollbackOf), not
	// of its PromotionSteps.
	bundles, err := lifecycle.ListPipelineBundles(ctx, c, ns, pipeline)
	if err != nil {
		return err
	}
	rollbacks := map[string]bool{}
	for _, b := range bundles {
		if b.Spec.Pipeline == pipeline && isRollbackBundle(b) {
			rollbacks[b.Name] = true
		}
	}

	rows := buildHistoryRows(steps.Items, rollbacks, envFilter, limit)
	versions := map[string]string{}
	for i := range bundles {
		versions[bundles[i].Name] = createdOf(&bundles[i]).Version
	}
	for i := range rows {
		rows[i].Version = versions[rows[i].Bundle]
	}

	if len(rows) == 0 {
		if err := checkPipelineEnv(ctx, c, ns, pipeline, envFilter); err != nil {
			return err
		}
		if structured() {
			return writeHistoryStructured(w, nil)
		}
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

	if structured() {
		return writeHistoryStructured(w, rows)
	}
	return formatHistoryTable(w, rows)
}

// buildHistoryRows converts PromotionSteps into history rows, newest first.
// rollbacks holds the names of rollback Bundles.
func buildHistoryRows(steps []v1alpha1.PromotionStep, rollbacks map[string]bool, envFilter string, limit int) []HistoryRow {
	// Sort steps newest first by creation timestamp. Timestamps have whole
	// seconds, so steps created in the same second are ordered by name, and
	// the order does not change between runs.
	sorted := make([]v1alpha1.PromotionStep, len(steps))
	copy(sorted, steps)
	sort.SliceStable(sorted, func(i, j int) bool {
		ti, tj := sorted[i].CreationTimestamp.Time, sorted[j].CreationTimestamp.Time
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return sorted[i].Name < sorted[j].Name
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
			State:     s.Status.State,
			PRURL:     s.Status.PRURL,
			Created:   s.CreationTimestamp.Time,
			Seconds:   stepSeconds(s),
			Running:   duration == "...",
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

// stepSeconds is deriveDuration in whole seconds, nil when the step runs or
// no end is recorded.
func stepSeconds(s v1alpha1.PromotionStep) *int64 {
	d, ok := stepDuration(s)
	if !ok {
		return nil
	}
	sec := int64(d / time.Second)
	return &sec
}

// deriveDuration is the time from step creation to its end: the Verified
// condition's transition, else the last completed step. "..." while the step
// is running, "--" when no end time is recorded. RollingBack is an end state:
// the step stops there, and the rollback Bundle carries the promotion on.
func deriveDuration(s v1alpha1.PromotionStep) string {
	switch s.Status.State {
	case "", "Pending", "Promoting", "WaitingForMerge", "HealthChecking", "Verifying":
		return "..."
	}
	d, ok := stepDuration(s)
	if !ok {
		return "--"
	}
	return humanDuration(d)
}

// stepDuration is how long a finished step took (deriveDuration); false
// while it runs or when no end time is recorded.
func stepDuration(s v1alpha1.PromotionStep) (time.Duration, bool) {
	switch s.Status.State {
	case "Verified", "Failed", "AbortedByAlarm", "RollingBack", "Superseded":
	default:
		return 0, false
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
		return 0, false
	}
	return end.Sub(s.CreationTimestamp.Time), true
}

// formatHistoryTable writes the history table to w.
func formatHistoryTable(w io.Writer, rows []HistoryRow) error {
	tw := style.NewTable(w, 3)
	if _, err := fmt.Fprintln(tw, "VERSION\tACTION\tENV\tPR\tDURATION\tTIMESTAMP"); err != nil {
		return fmt.Errorf("write history header: %w", err)
	}
	for _, r := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			versionLabel(r.Version, r.Bundle), r.Action, r.Env, r.PR, r.Duration, r.Timestamp); err != nil {
			return fmt.Errorf("write history row: %w", err)
		}
	}
	return tw.Flush()
}
