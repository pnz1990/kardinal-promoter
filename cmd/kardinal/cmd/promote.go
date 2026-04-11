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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newPromoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "promote <pipeline> --env <environment>",
		Short: "Trigger promotion of a pipeline to a specific environment",
		Long: `Trigger promotion by creating a Bundle that targets a specific environment.

The Bundle flows through all upstream environments first, then targets the
specified environment. PolicyGates and approval mode apply as configured.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, _ := cmd.Flags().GetString("env")
			if env == "" {
				return fmt.Errorf("--env is required")
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("promote: %w", err)
			}
			return promoteFn(cmd.OutOrStdout(), c, ns, args[0], env)
		},
	}

	cmd.Flags().StringP("env", "e", "", "Target environment name (required)")
	_ = cmd.MarkFlagRequired("env")

	return cmd
}

// promoteFn is the testable implementation of the promote command.
// It validates the pipeline and environment exist, then creates a Bundle targeting
// the specified environment via spec.intent.targetEnvironment.
func promoteFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline, env string) error {
	ctx := context.Background()

	// Look up the Pipeline
	var pl v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Name: pipeline, Namespace: ns}, &pl); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("pipeline not found: %s", pipeline)
		}
		return fmt.Errorf("get pipeline %s: %w", pipeline, err)
	}

	// Validate that the environment exists in the pipeline
	found := false
	for _, e := range pl.Spec.Environments {
		if e.Name == env {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("environment %q not found in pipeline %s", env, pipeline)
	}

	// Create a Bundle with intent targeting the specified environment
	bundle := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pipeline + "-",
			Namespace:    ns,
		},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: pipeline,
			Intent: &v1alpha1.BundleIntent{
				TargetEnvironment: env,
			},
		},
	}

	if err := c.Create(ctx, bundle); err != nil {
		return fmt.Errorf("create promote bundle for pipeline %s env %s: %w", pipeline, env, err)
	}

	if _, err := fmt.Fprintf(w,
		"Promoting %s to %s: bundle %s created\n"+
			"Track with: kardinal get bundles %s\n",
		pipeline, env, bundle.Name, pipeline,
	); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}
