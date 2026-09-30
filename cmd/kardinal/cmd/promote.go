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
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func newPromoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "promote <pipeline> --env <environment>",
		Short: "Promote the Bundle verified upstream into an environment",
		Long: `Promote the newest Bundle that is Verified in every upstream environment
into the given environment.

Creates a Bundle that copies that Bundle's images, config ref and provenance,
with intent.targetEnvironment set to the environment. PolicyGates and approval
mode apply as configured. The command refuses, and creates nothing, when no
Bundle is Verified upstream yet, when that Bundle is already Verified or being
promoted in the environment, or when a newer Bundle is still promoting (the new
Bundle would supersede it). The first environment of a pipeline has nothing
upstream; use kardinal create bundle for it.`,
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

// promoteFn is the testable implementation of the promote command. The
// source Bundle and the new Bundle come from lifecycle.PlanPromote, the
// implementation the UI shares.
func promoteFn(w io.Writer, c sigs_client.Client, ns, pipeline, env string) error {
	ctx := context.Background()

	plan, err := lifecycle.PlanPromote(ctx, c, lifecycle.PromoteRequest{
		Namespace:   ns,
		Pipeline:    pipeline,
		Environment: env,
		Actor:       currentUser(),
		Now:         time.Now(),
	})
	if err != nil {
		return err
	}
	if err := c.Create(ctx, plan.Bundle); err != nil {
		return fmt.Errorf("create promote bundle for pipeline %s env %s: %w", pipeline, env, err)
	}

	if _, err := fmt.Fprintf(w,
		"Promoting %s to %s: bundle %s created from %s (Verified in %s; %s)\n"+
			"Track with: kardinal get bundles %s\n",
		pipeline, env, plan.Bundle.Name, plan.Source.Name, strings.Join(plan.Upstreams, ", "),
		artifactSummary(plan.Bundle), pipeline,
	); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}
