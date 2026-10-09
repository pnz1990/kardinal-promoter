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

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func newGetStepsCmd() *cobra.Command {
	var watchFlag bool

	cmd := &cobra.Command{
		Use:         "steps <pipeline>",
		Annotations: map[string]string{outputAnnotation: "true"},
		Aliases:     []string{"step"},
		Short:       "List PromotionSteps for a pipeline",
		Long: `List PromotionSteps for a pipeline.

Use --watch / -w to stream live updates (polls every 2s, Ctrl-C to quit).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGetSteps(cmd, args, watchFlag)
		},
	}
	cmd.Flags().BoolVarP(&watchFlag, "watch", "w", false,
		"Stream live updates (polls every 2s, Ctrl-C to quit)")
	return cmd
}

func runGetSteps(cmd *cobra.Command, args []string, watch bool) error {
	c, ns, err := buildClient()
	if err != nil {
		return fmt.Errorf("get steps: %w", err)
	}

	pipeline := args[0]

	if !watch {
		return getStepsOnce(cmd.OutOrStdout(), c, ns, pipeline)
	}

	w := cmd.OutOrStdout()
	return watchLoop(cmd.Context(), w, getPipelinesWatchInterval, func() error {
		return getStepsOnce(w, c, ns, pipeline)
	})
}

// getStepsOnce fetches and renders a single snapshot of PromotionStep status.
func getStepsOnce(w io.Writer, c sigs_client.Client, ns, pipeline string) error {
	ctx := context.Background()

	var steps v1alpha1.PromotionStepList
	if err := c.List(ctx, &steps,
		sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
	); err != nil {
		return fmt.Errorf("list promotion steps: %w", err)
	}

	// Steps of Superseded Bundles are history, not the current view.
	var bundles v1alpha1.BundleList
	if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}
	// Retired Bundles (#1492) keep their steps in status.retiredSteps.
	steps.Items = lifecycle.AddRetiredSteps(steps.Items, bundles.Items, map[string]string{"kardinal.io/pipeline": pipeline})
	activeBundles := make(map[string]bool)
	for _, b := range bundles.Items {
		if b.Spec.Pipeline == pipeline && b.Status.Phase != "Superseded" {
			activeBundles[b.Name] = true
		}
	}
	activeSteps := []v1alpha1.PromotionStep{}
	for _, s := range steps.Items {
		if activeBundles[s.Spec.BundleName] {
			activeSteps = append(activeSteps, s)
		}
	}

	switch OutputFormat() {
	case "json":
		return WriteJSON(w, activeSteps)
	case "yaml":
		return WriteYAML(w, activeSteps)
	default:
		if len(activeBundles) == 0 {
			if _, err := fmt.Fprintf(w, "No active bundles for pipeline %q.\n", pipeline); err != nil {
				return fmt.Errorf("write: %w", err)
			}
			return nil
		}
		return FormatStepsTable(w, activeSteps)
	}
}
