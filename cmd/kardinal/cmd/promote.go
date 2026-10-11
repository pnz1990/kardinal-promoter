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
	var envFlag string
	cmd := &cobra.Command{
		Use:   "promote <pipeline> <env>",
		Short: "Promote the version verified upstream into an environment",
		Long: `Promote the newest Bundle that is Verified in every upstream environment
into <env>, as a new Bundle that targets it; gates and approval apply.
It refuses, creating nothing, when nothing is Verified upstream yet, when
that Bundle is already in <env>, or when a newer Bundle is still promoting.
The first environment has nothing upstream: use kardinal ship there.`,
		Example: `  # promote what uat verified into prod
  kardinal promote my-app prod

  # the same, with the flag form
  kardinal promote my-app --env prod`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pipeline, env, err := requireEnv(cmd, args, envFlag)
			if err != nil {
				return err
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("promote: %w", err)
			}
			return promoteFn(cmd.OutOrStdout(), c, ns, pipeline, env)
		},
	}

	addEnvFlag(cmd, &envFlag, "Target environment (or give it after the pipeline)")
	return withStructuredOutput(cmd)
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
	stampCreator(ctx, c, plan.Bundle)
	if err := c.Create(ctx, plan.Bundle); err != nil {
		return fmt.Errorf("create promote bundle for pipeline %s env %s: %w", pipeline, env, err)
	}

	if structured() {
		out := createdOf(plan.Bundle)
		out.Environment, out.From = env, plan.Source.Name
		return writeStructured(w, out)
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
