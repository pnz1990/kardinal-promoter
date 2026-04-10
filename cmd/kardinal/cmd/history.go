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
func historyFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline string) error {
	ctx := context.Background()

	var bundles v1alpha1.BundleList
	if listErr := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); listErr != nil {
		return fmt.Errorf("list bundles: %w", listErr)
	}

	var filtered []v1alpha1.Bundle
	for _, b := range bundles.Items {
		if b.Spec.Pipeline == pipeline {
			filtered = append(filtered, b)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].CreationTimestamp.After(filtered[j].CreationTimestamp.Time)
	})

	if len(filtered) == 0 {
		if _, err := fmt.Fprintf(w, "No bundles found for pipeline %q\n", pipeline); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		return nil
	}

	return FormatBundleTable(w, filtered)
}
