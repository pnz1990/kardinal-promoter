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

func newGetStepsCmd() *cobra.Command {
	var watchFlag bool

	cmd := &cobra.Command{
		Use:     "steps <pipeline>",
		Aliases: []string{"step"},
		Short:   "List PromotionSteps for a pipeline",
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

	// Watch mode: poll every 2s and refresh the terminal output.
	for {
		_, _ = fmt.Fprint(cmd.OutOrStdout(), "\033[H\033[2J")
		if err := getStepsOnce(cmd.OutOrStdout(), c, ns, pipeline); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\n(watching — press Ctrl-C to quit)")
		time.Sleep(getPipelinesWatchInterval) // reuse 2s constant from get_pipelines.go
	}
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

	// Exclude steps from Superseded bundles. A Superseded bundle's steps are
	// historical and should not appear in the current view. We build a set of
	// non-Superseded bundle names, then filter steps accordingly.
	activeBundles := make(map[string]bool)
	var bundles v1alpha1.BundleList
	if listErr := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); listErr == nil {
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

	switch OutputFormat() {
	case "json":
		return WriteJSON(w, activeSteps)
	case "yaml":
		return WriteYAML(w, activeSteps)
	default:
		return FormatStepsTable(w, activeSteps)
	}
}
