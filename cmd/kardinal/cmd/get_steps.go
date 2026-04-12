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

func newGetStepsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "steps <pipeline>",
		Aliases: []string{"step"},
		Short:   "List PromotionSteps for a pipeline",
		Args:    cobra.ExactArgs(1),
		RunE:    runGetSteps,
	}
}

func runGetSteps(cmd *cobra.Command, args []string) error {
	client, ns, err := buildClient()
	if err != nil {
		return fmt.Errorf("get steps: %w", err)
	}

	pipeline := args[0]
	ctx := context.Background()

	var steps v1alpha1.PromotionStepList
	if err := client.List(ctx, &steps,
		sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
	); err != nil {
		return fmt.Errorf("list promotion steps: %w", err)
	}

	// Exclude steps from Superseded bundles. A Superseded bundle's steps are
	// historical and should not appear in the current view. We build a set of
	// non-Superseded bundle names, then filter steps accordingly.
	activeBundles := make(map[string]bool)
	var bundles v1alpha1.BundleList
	if listErr := client.List(ctx, &bundles, sigs_client.InNamespace(ns)); listErr == nil {
		for _, b := range bundles.Items {
			if b.Spec.Pipeline == pipeline && b.Status.Phase != "Superseded" {
				activeBundles[b.Name] = true
			}
		}
	}

	var activeSteps []v1alpha1.PromotionStep
	if len(activeBundles) > 0 {
		for _, s := range steps.Items {
			bundleName := s.Labels["kardinal.io/bundle"]
			if bundleName == "" || activeBundles[bundleName] {
				activeSteps = append(activeSteps, s)
			}
		}
	} else {
		// Fallback: show all steps if bundle list failed or no active bundles.
		activeSteps = steps.Items
	}

	out := cmd.OutOrStdout()
	switch OutputFormat() {
	case "json":
		return WriteJSON(out, activeSteps)
	case "yaml":
		return WriteYAML(out, activeSteps)
	default:
		return FormatStepsTable(out, activeSteps)
	}
}
