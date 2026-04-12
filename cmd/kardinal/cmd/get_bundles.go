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

func newGetBundlesCmd() *cobra.Command {
	var activeOnly bool

	cmd := &cobra.Command{
		Use:     "bundles [pipeline]",
		Aliases: []string{"bundle"},
		Short:   "List Bundles, optionally filtered by pipeline name",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetBundles(cmd, args, activeOnly)
		},
	}
	cmd.Flags().BoolVar(&activeOnly, "active", false,
		"Show only active bundles (Promoting/Verified/Failed — excludes Superseded)")
	return cmd
}

func runGetBundles(cmd *cobra.Command, args []string, activeOnly bool) error {
	client, ns, err := buildClient()
	if err != nil {
		return fmt.Errorf("get bundles: %w", err)
	}

	opts := []sigs_client.ListOption{sigs_client.InNamespace(ns)}
	if len(args) == 1 {
		opts = append(opts, sigs_client.MatchingFields{"spec.pipeline": args[0]})
	}

	var bundles v1alpha1.BundleList
	if err := client.List(context.Background(), &bundles, opts...); err != nil {
		// Fall back to unfiltered list and filter in-process if field indexer
		// is not available (e.g. no cache).
		var all v1alpha1.BundleList
		if err2 := client.List(context.Background(), &all, sigs_client.InNamespace(ns)); err2 != nil {
			return fmt.Errorf("list bundles: %w", err)
		}
		if len(args) == 1 {
			pipeline := args[0]
			for _, b := range all.Items {
				if b.Spec.Pipeline == pipeline {
					bundles.Items = append(bundles.Items, b)
				}
			}
		} else {
			bundles = all
		}
	}

	// Apply --active filter if requested: exclude Superseded bundles.
	items := bundles.Items
	if activeOnly {
		filtered := items[:0]
		for _, b := range items {
			if b.Status.Phase != "Superseded" {
				filtered = append(filtered, b)
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
		return FormatBundleTable(out, items)
	}
}
