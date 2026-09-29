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
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// getPipelinesWatchInterval is the polling interval of get --watch: fast
// enough to see step transitions.
const getPipelinesWatchInterval = 2 * time.Second

// watchLoop calls render every interval until ctx is done or render fails.
// The screen is cleared only for a table written to a terminal, and the footer
// is printed only for a table, so -o json|yaml output stays machine-readable.
func watchLoop(ctx context.Context, w io.Writer, interval time.Duration, render func() error) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	table := OutputFormat() == "" || OutputFormat() == "table"
	clearScreen := table && isTerminal(w)
	for {
		if clearScreen {
			_, _ = fmt.Fprint(w, "\033[H\033[2J")
		}
		if err := render(); err != nil {
			return err
		}
		if table {
			_, _ = fmt.Fprintln(w, "\n(watching — press Ctrl-C to quit)")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func newGetPipelinesCmd() *cobra.Command {
	var (
		allNamespaces bool
		watchFlag     bool
	)

	cmd := &cobra.Command{
		Use:         "pipelines [name]",
		Annotations: map[string]string{outputAnnotation: "true"},
		Aliases:     []string{"pipeline"},
		Short:       "List Pipelines",
		Long: `List Pipelines and their per-environment promotion status.

Use --watch / -w to stream live updates (polls every 2s, Ctrl-C to quit).

When a Bundle promotion fails (e.g. due to an invalid dependsOn reference
or a circular dependency in the Pipeline spec), an ERROR: line is printed
after the table with the pipeline name and root cause:

  ERROR: pipeline my-app: build: environment "prod" dependsOn unknown environment "staging"

This avoids the need to run kubectl describe bundle to find the root cause
of a stalled promotion.`,
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

	w := cmd.OutOrStdout()
	return watchLoop(cmd.Context(), w, getPipelinesWatchInterval, func() error {
		return getPipelinesOnce(w, c, ns, args, allNamespaces)
	})
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

	// Fetch Subscriptions for the SUB column. On error: pass nil to omit the column
	// rather than showing misleading zeros.
	var subsItems []v1alpha1.Subscription
	var subsList v1alpha1.SubscriptionList
	if err := c.List(ctx, &subsList, opts...); err == nil {
		subsItems = subsList.Items
	}

	// Fetch Bundles so we can surface Failed-phase error conditions after the table.
	// Non-fatal: if the list fails, we still render the table without error notices.
	var bundlesItems []v1alpha1.Bundle
	var bundlesList v1alpha1.BundleList
	if err := c.List(ctx, &bundlesList, opts...); err == nil {
		bundlesItems = bundlesList.Items
	}

	// If a specific name was given, filter pipelines (and the bundle list to match).
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

		// Also filter bundles to only those belonging to this pipeline.
		var filteredBundles []v1alpha1.Bundle
		for _, b := range bundlesItems {
			if b.Spec.Pipeline == name {
				filteredBundles = append(filteredBundles, b)
			}
		}
		bundlesItems = filteredBundles
	}

	switch OutputFormat() {
	case "json":
		return WriteJSON(w, items)
	case "yaml":
		return WriteYAML(w, items)
	default:
		if err := FormatPipelineTableFull(w, items, promotionSteps.Items, subsItems, allNamespaces); err != nil {
			return err
		}
		// Surface Bundle-level errors (e.g. dependsOn validation failures) that
		// would otherwise be invisible in the table. Non-fatal: ignore write errors
		// so a partial error notice does not mask the successfully rendered table.
		_ = FormatBundleErrors(w, bundlesItems, allNamespaces)
		return nil
	}
}
