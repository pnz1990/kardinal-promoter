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
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newApproveCmd() *cobra.Command {
	var envFlag string

	cmd := &cobra.Command{
		Use:   "approve <bundle>",
		Short: "Approve a Bundle for promotion, bypassing upstream gate requirements",
		Long: `Approve a Bundle for promotion to a specific environment.

Approval is expressed by patching the Bundle with the label
kardinal.io/approved=true (and optionally kardinal.io/approved-for=<env>).

This is useful for hotfix deployments that must skip the normal
upstream soak / gate requirements.

Example:
  kardinal approve nginx-demo-v1-29-0 --env prod`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("approve: %w", err)
			}
			return approveFn(cmd.OutOrStdout(), c, ns, args[0], envFlag)
		},
	}

	cmd.Flags().StringVar(&envFlag, "env", "", "Target environment to approve for (optional)")
	return cmd
}

// approveFn patches a Bundle with approval labels.
func approveFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, bundleName, env string) error {
	ctx := context.Background()

	// Fetch the existing bundle to verify it exists.
	var bundle v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: bundleName}, &bundle); err != nil {
		return fmt.Errorf("get bundle %q: %w", bundleName, err)
	}

	// Apply approval labels via a merge patch.
	labels := map[string]string{
		"kardinal.io/approved": "true",
	}
	if env != "" {
		labels["kardinal.io/approved-for"] = env
	}

	patch := bundle.DeepCopy()
	if patch.Labels == nil {
		patch.Labels = make(map[string]string)
	}
	for k, v := range labels {
		patch.Labels[k] = v
	}

	if err := c.Patch(ctx, patch, sigs_client.MergeFrom(&bundle)); err != nil {
		return fmt.Errorf("patch bundle %q: %w", bundleName, err)
	}

	if env != "" {
		if _, err := fmt.Fprintf(w, "Bundle %q approved for %q.\n  Label: kardinal.io/approved=true, kardinal.io/approved-for=%s\n  To track: kardinal explain %s --env %s\n",
			bundleName, env, env, bundle.Spec.Pipeline, env); err != nil {
			return fmt.Errorf("write: %w", err)
		}
	} else {
		if _, err := fmt.Fprintf(w, "Bundle %q approved.\n  Label: kardinal.io/approved=true\n  To track: kardinal explain %s\n",
			bundleName, bundle.Spec.Pipeline); err != nil {
			return fmt.Errorf("write: %w", err)
		}
	}
	return nil
}
