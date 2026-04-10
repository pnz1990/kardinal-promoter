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

func newExplainCmd() *cobra.Command {
	var (
		envFlag   string
		watchFlag bool
	)

	cmd := &cobra.Command{
		Use:   "explain <pipeline>",
		Short: "Explain the current state of a promotion pipeline",
		Long: `Explain displays the PromotionSteps and PolicyGates for a pipeline.
It shows the current state, reason, and any PR URLs for each environment.

Use --env to filter to a specific environment.
Use --watch to stream live updates.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExplain(cmd, args, envFlag, watchFlag)
		},
	}

	cmd.Flags().StringVar(&envFlag, "env", "", "Filter to a specific environment")
	cmd.Flags().BoolVar(&watchFlag, "watch", false, "Stream updates (polling)")

	return cmd
}

func runExplain(cmd *cobra.Command, args []string, envFilter string, watch bool) error {
	c, ns, err := buildClient()
	if err != nil {
		return fmt.Errorf("explain: %w", err)
	}

	pipeline := args[0]

	if !watch {
		return explainOnce(cmd.OutOrStdout(), c, ns, pipeline, envFilter)
	}

	// Watch mode: poll every 3 seconds and refresh the output.
	for {
		// Clear screen using ANSI escape.
		_, _ = fmt.Fprint(cmd.OutOrStdout(), "\033[H\033[2J")
		if err := explainOnce(cmd.OutOrStdout(), c, ns, pipeline, envFilter); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\n(watching — press Ctrl-C to quit)")
		time.Sleep(3 * time.Second)
	}
}

func explainOnce(w io.Writer, c sigs_client.Client, ns, pipeline, envFilter string) error {
	ctx := context.Background()

	var steps v1alpha1.PromotionStepList
	if err := c.List(ctx, &steps,
		sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
	); err != nil {
		return fmt.Errorf("list promotion steps: %w", err)
	}

	var gates v1alpha1.PolicyGateList
	if err := c.List(ctx, &gates,
		sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
	); err != nil {
		return fmt.Errorf("list policy gates: %w", err)
	}

	type explainRow struct {
		environment string
		kind        string // "PromotionStep" or "PolicyGate"
		name        string
		state       string
		reason      string
	}

	var rows []explainRow

	for _, s := range steps.Items {
		if envFilter != "" && s.Spec.Environment != envFilter {
			continue
		}
		state := s.Status.State
		if state == "" {
			state = "Pending"
		}
		reason := s.Status.Message
		if reason == "" {
			reason = "-"
		}
		rows = append(rows, explainRow{
			environment: s.Spec.Environment,
			kind:        "Step",
			name:        s.Spec.StepType,
			state:       state,
			reason:      reason,
		})
	}

	for _, g := range gates.Items {
		env := g.Labels["kardinal.io/environment"]
		if envFilter != "" && env != envFilter {
			continue
		}
		gateState := "Pending"
		if g.Status.Ready {
			gateState = "Pass"
		} else if g.Status.LastEvaluatedAt != nil {
			gateState = "Block"
		}
		reason := g.Status.Reason
		if reason == "" {
			reason = "-"
		}
		rows = append(rows, explainRow{
			environment: env,
			kind:        "PolicyGate",
			name:        g.Name,
			state:       gateState,
			reason:      reason,
		})
	}

	// Sort by environment, then kind, then name for stable output.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].environment != rows[j].environment {
			return rows[i].environment < rows[j].environment
		}
		if rows[i].kind != rows[j].kind {
			// PolicyGate before Step within same env
			return rows[i].kind < rows[j].kind
		}
		return rows[i].name < rows[j].name
	})

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ENVIRONMENT\tTYPE\tNAME\tSTATE\tREASON"); err != nil {
		return fmt.Errorf("write explain header: %w", err)
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			row.environment, row.kind, row.name, row.state, row.reason,
		); err != nil {
			return fmt.Errorf("write explain row: %w", err)
		}
	}

	if len(rows) == 0 {
		var emptyMsg string
		if envFilter != "" {
			emptyMsg = fmt.Sprintf("No steps or gates found for pipeline %q environment %q\n", pipeline, envFilter)
		} else {
			emptyMsg = fmt.Sprintf("No steps or gates found for pipeline %q\n", pipeline)
		}
		if _, err := fmt.Fprint(tw, emptyMsg); err != nil {
			return fmt.Errorf("write empty message: %w", err)
		}
	}

	return tw.Flush()
}
