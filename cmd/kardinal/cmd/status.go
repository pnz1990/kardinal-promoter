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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show controller health and cluster resource summary",
		Long: `Show the health of the kardinal controller and a summary of managed resources.

Displays:
  - Controller pod status and version
  - Count of Pipelines and active Bundles
  - Any pipelines currently in a failed/stuck state

For detailed diagnostics, use 'kardinal doctor'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd)
		},
	}
}

func runStatus(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()

	client, _, err := buildClient()
	if err != nil {
		return err // buildClient already provides actionable message
	}
	ctx := context.Background()

	// 1. Controller version from ConfigMap
	var versionCM corev1.ConfigMap
	ctrlVersion := "(unknown)"
	if err := client.Get(ctx, types.NamespacedName{
		Name:      "kardinal-version",
		Namespace: "kardinal-system",
	}, &versionCM); err == nil {
		if v := versionCM.Data["version"]; v != "" {
			ctrlVersion = v
		}
	}

	// 2. Pipeline count (all namespaces)
	var pipelines v1alpha1.PipelineList
	if err := client.List(ctx, &pipelines); err != nil {
		_, _ = fmt.Fprintln(out, "Controller: "+ctrlVersion)
		return fmt.Errorf("list pipelines: %w", err)
	}

	// 3. Bundle counts
	var bundles v1alpha1.BundleList

	_ = client.List(ctx, &bundles)

	var activeBundles, failedPipelines int
	failedPipelineNames := []string{}
	for _, b := range bundles.Items {
		phase := b.Status.Phase
		if phase == "Promoting" || phase == "Pending" {
			activeBundles++
		}
	}
	for _, p := range pipelines.Items {
		if p.Status.Phase == "Failed" || p.Status.Phase == "Error" {
			failedPipelines++
			failedPipelineNames = append(failedPipelineNames, p.Name)
		}
	}

	// Print summary
	_, _ = fmt.Fprintf(out, "Controller:  %s\n", ctrlVersion)
	_, _ = fmt.Fprintf(out, "Pipelines:   %d", len(pipelines.Items))
	if failedPipelines > 0 {
		_, _ = fmt.Fprintf(out, " (%d failed: %v)", failedPipelines, failedPipelineNames)
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "Bundles:     %d (%d active)\n", len(bundles.Items), activeBundles)

	if failedPipelines > 0 {
		_, _ = fmt.Fprintf(out, "\nWarning: %d pipeline(s) in failed state — run 'kardinal get pipelines' for details\n", failedPipelines)
	}

	return nil
}
