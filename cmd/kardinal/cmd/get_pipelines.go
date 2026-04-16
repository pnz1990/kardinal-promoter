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
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// watchInterval is the polling interval for --watch mode.
// 2 seconds matches the issue requirement and is fast enough to see step transitions.
const getPipelinesWatchInterval = 2 * time.Second

func newGetPipelinesCmd() *cobra.Command {
	var (
		allNamespaces bool
		watchFlag     bool
	)

	cmd := &cobra.Command{
		Use:     "pipelines [name]",
		Aliases: []string{"pipeline"},
		Short:   "List Pipelines",
		Long: `List Pipelines and their per-environment promotion status.

Use --watch / -w to stream live updates (polls every 2s, Ctrl-C to quit).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetPipelines(cmd, args, allNamespaces, watchFlag)
		},
	}
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false,
		"List pipelines across all namespaces (adds NAMESPACE column)")
	cmd.Flags().BoolVarP(&watchFlag, "watch", "w", false,
		"Stream live updates (polls every 2s, Ctrl-C to quit)")
	return cmd
}

func runGetPipelines(cmd *cobra.Command, args []string, allNamespaces, watch bool) error {
	c, ns, err := buildClient()
	if err != nil {
		return fmt.Errorf("get pipelines: %w", err)
	}

	if !watch {
		return getPipelinesOnce(cmd.OutOrStdout(), c, ns, args, allNamespaces)
	}

	// Watch mode: poll every 2s and refresh the terminal output.
	for {
		// Clear screen using ANSI escape (same pattern as explain --watch).
		_, _ = fmt.Fprint(cmd.OutOrStdout(), "\033[H\033[2J")
		if err := getPipelinesOnce(cmd.OutOrStdout(), c, ns, args, allNamespaces); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\n(watching — press Ctrl-C to quit)")
		time.Sleep(getPipelinesWatchInterval)
	}
}

// getPipelinesOnce fetches and renders a single snapshot of pipeline status.
func getPipelinesOnce(w io.Writer, c sigs_client.Client, ns string, args []string, allNamespaces bool) error {
	ctx := context.Background()
	var opts []sigs_client.ListOption
	if !allNamespaces {
		opts = append(opts, sigs_client.InNamespace(ns))
	}

	var pipelines v1alpha1.PipelineList
	if err := c.List(ctx, &pipelines, opts...); err != nil {
		return fmt.Errorf("list pipelines: %w", err)
	}

	// Fetch PromotionSteps in the same namespace(s) for per-environment status columns.
	var promotionSteps v1alpha1.PromotionStepList
	if err := c.List(ctx, &promotionSteps, opts...); err != nil {
		// Non-fatal: fall back to empty step list (env columns will show "-").
		promotionSteps.Items = nil
	}

	// If a specific name was given, filter.
	items := pipelines.Items
	if len(args) == 1 {
		name := args[0]
		filtered := items[:0]
		for _, p := range items {
			if p.Name == name {
				filtered = append(filtered, p)
			}
		}
		items = filtered
	}

	switch OutputFormat() {
	case "json":
		return WriteJSON(w, items)
	case "yaml":
		return WriteYAML(w, items)
	default:
		return FormatPipelineTableWithOptions(w, items, promotionSteps.Items, allNamespaces)
	}
}
