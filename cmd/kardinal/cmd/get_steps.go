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
	"sort"

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

	// Steps of Superseded and Rejected Bundles are history, not the current
	// view, except the steps of a Rejected Bundle whose change is live
	// (lifecycle.RejectedLiveStep): those are what is deployed, shown with a
	// roll-back hint.
	var bundles v1alpha1.BundleList
	if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}
	// Retired Bundles (#1492) keep their steps in status.retiredSteps.
	steps.Items = lifecycle.AddRetiredSteps(steps.Items, bundles.Items, map[string]string{"kardinal.io/pipeline": pipeline})
	activeSteps, hints := currentSteps(pipeline, bundles.Items, steps.Items)
	activeBundles := make(map[string]bool)
	for _, s := range activeSteps {
		activeBundles[s.Spec.BundleName] = true
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
		if err := FormatStepsTable(w, activeSteps); err != nil {
			return err
		}
		for _, h := range hints {
			if _, err := fmt.Fprintln(w, h); err != nil {
				return fmt.Errorf("write: %w", err)
			}
		}
		return nil
	}
}

// currentSteps returns the steps of pipeline that the current views list:
// the steps of its Bundles that are not Halted, and the steps of a Rejected
// Bundle whose change is live (lifecycle.RejectedLiveStep), with one
// roll-back hint per such environment.
func currentSteps(pipeline string, bundles []v1alpha1.Bundle, steps []v1alpha1.PromotionStep) ([]v1alpha1.PromotionStep, []string) {
	byName := make(map[string]*v1alpha1.Bundle, len(bundles))
	for i := range bundles {
		if bundles[i].Spec.Pipeline == pipeline {
			byName[bundles[i].Name] = &bundles[i]
		}
	}
	out := []v1alpha1.PromotionStep{}
	var hints []string
	seen := map[string]bool{}
	for i := range steps {
		s := &steps[i]
		b := byName[s.Spec.BundleName]
		switch {
		case b == nil:
			continue
		case !lifecycle.Halted(b):
			out = append(out, *s)
		case lifecycle.RejectedLiveStep(b, s):
			out = append(out, *s)
			if key := b.Name + "/" + s.Spec.Environment; !seen[key] {
				seen[key] = true
				hints = append(hints, fmt.Sprintf("WARNING: bundle %s is Rejected in %s: %s (kardinal rollback %s --env %s)",
					b.Name, s.Spec.Environment, lifecycle.RejectedLiveHint, pipeline, s.Spec.Environment))
			}
		}
	}
	sort.Strings(hints)
	return out, hints
}
