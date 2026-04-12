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

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newGetPipelinesCmd() *cobra.Command {
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:     "pipelines [name]",
		Aliases: []string{"pipeline"},
		Short:   "List Pipelines",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetPipelines(cmd, args, allNamespaces)
		},
	}
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false,
		"List pipelines across all namespaces (adds NAMESPACE column)")
	return cmd
}

func runGetPipelines(cmd *cobra.Command, args []string, allNamespaces bool) error {
	client, ns, err := buildClient()
	if err != nil {
		return fmt.Errorf("get pipelines: %w", err)
	}

	ctx := context.Background()
	var opts []sigs_client.ListOption
	if !allNamespaces {
		opts = append(opts, sigs_client.InNamespace(ns))
	}

	var pipelines v1alpha1.PipelineList
	if err := client.List(ctx, &pipelines, opts...); err != nil {
		return fmt.Errorf("list pipelines: %w", err)
	}

	// Fetch PromotionSteps in the same namespace(s) for per-environment status columns.
	var promotionSteps v1alpha1.PromotionStepList
	if err := client.List(ctx, &promotionSteps, opts...); err != nil {
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

	out := cmd.OutOrStdout()
	switch OutputFormat() {
	case "json":
		return WriteJSON(out, items)
	case "yaml":
		return WriteYAML(out, items)
	default:
		return FormatPipelineTableWithOptions(out, items, promotionSteps.Items, allNamespaces)
	}
}
