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

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newRollbackCmd() *cobra.Command {
	var (
		envFlag       string
		toFlag        string
		emergencyFlag bool
	)

	cmd := &cobra.Command{
		Use:   "rollback <pipeline>",
		Short: "Roll back a pipeline environment to a previous Bundle",
		Long: `Roll back a pipeline environment to a previous Bundle.

Creates a new Bundle with spec.provenance.rollbackOf pointing to the
target Bundle. Goes through the same pipeline, PolicyGates, and PR flow.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("rollback: %w", err)
			}
			return rollbackFn(cmd.OutOrStdout(), c, ns, args[0], envFlag, toFlag, emergencyFlag)
		},
	}

	cmd.Flags().StringVar(&envFlag, "env", "", "Target environment to roll back (required)")
	cmd.Flags().StringVar(&toFlag, "to", "", "Specific Bundle name to roll back to")
	cmd.Flags().BoolVar(&emergencyFlag, "emergency", false, "Emergency rollback: bypass skipPermission PolicyGates")
	_ = cmd.MarkFlagRequired("env")

	return cmd
}

// rollbackFn is the testable implementation of rollback.
func rollbackFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline, envFilter, toBundle string, emergency bool) error {
	ctx := context.Background()

	rollbackOf := toBundle
	if rollbackOf == "" {
		var bundles v1alpha1.BundleList
		if listErr := c.List(ctx, &bundles,
			sigs_client.InNamespace(ns),
			sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
		); listErr != nil {
			return fmt.Errorf("list bundles: %w", listErr)
		}

		var latest *v1alpha1.Bundle
		for i := range bundles.Items {
			b := &bundles.Items[i]
			if b.Status.Phase != "Verified" {
				continue
			}
			if latest == nil || b.CreationTimestamp.After(latest.CreationTimestamp.Time) {
				latest = b
			}
		}
		if latest == nil {
			return fmt.Errorf("no Verified bundles found for pipeline %s", pipeline)
		}
		rollbackOf = latest.Name
	}

	// Copy the bundle type from the target bundle so the rollback is type-compatible.
	// Falls back to "image" if the target bundle cannot be fetched.
	bundleType := "image"
	var originalBundle v1alpha1.Bundle
	if getErr := c.Get(ctx, types.NamespacedName{Name: rollbackOf, Namespace: ns}, &originalBundle); getErr == nil {
		bundleType = originalBundle.Spec.Type
	}

	labels := map[string]string{"kardinal.io/rollback": "true"}
	if emergency {
		labels["kardinal.io/emergency"] = "true"
	}

	rollbackBundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pipeline + "-rollback-",
			Namespace:    ns,
			Labels:       labels,
		},
		Spec: v1alpha1.BundleSpec{
			Type:     bundleType,
			Pipeline: pipeline,
			Intent:   &v1alpha1.BundleIntent{TargetEnvironment: envFilter},
			Provenance: &v1alpha1.BundleProvenance{
				RollbackOf: rollbackOf,
			},
		},
	}

	if createErr := c.Create(ctx, rollbackBundle); createErr != nil {
		return fmt.Errorf("create rollback bundle: %w", createErr)
	}

	if _, err := fmt.Fprintf(w,
		"Rolling back %s in %s\nBundle %s created (rollbackOf=%s)\nTrack with: kardinal explain %s --env %s\n",
		pipeline, envFilter, rollbackBundle.Name, rollbackOf, pipeline, envFilter,
	); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}
