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

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func newRollbackCmd() *cobra.Command {
	var (
		envFlag       string
		toFlag        string
		emergencyFlag bool // deprecated, ignored (#1288)
	)

	cmd := &cobra.Command{
		Use:   "rollback <pipeline>",
		Short: "Roll back a pipeline environment to a previous Bundle",
		Long: `Roll back a pipeline environment to a previous Bundle.

Without --to, the target is the most recent Bundle, other than the one deployed
now, that was Verified in the environment and deploys different artifacts. A
Bundle that an earlier rollback in the environment rolled back from is skipped.
With --to, the named Bundle must belong to the pipeline, carry images or a
config commit, differ from what is deployed now, and have been Verified in the
environment.

Creates a new Bundle that copies the target's images and config ref, sets
spec.provenance.rollbackOf to the target and intent.targetEnvironment to the
environment. An image the deployed Bundle changed and the target does not name
gets the version from the newest earlier Bundle Verified in the environment;
when there is none, the rollback is refused and names the image. The rollback
goes through the same PolicyGates and PR flow as any Bundle, and through every
environment upstream of the target first.

Config and mixed Bundles get back their config commit the same way. Without
--to, a mixed Bundle goes back to the newest earlier images and config commit,
whichever Bundles deployed them; --to a Bundle whose type cannot deploy what the
deployed Bundle changed is refused. See docs/rollback.md.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("rollback: %w", err)
			}
			return rollbackFn(cmd.OutOrStdout(), c, ns, args[0], envFlag, toFlag)
		},
	}

	cmd.Flags().StringVar(&envFlag, "env", "", "Target environment to roll back (required)")
	cmd.Flags().StringVar(&toFlag, "to", "", "Specific Bundle name to roll back to")
	// Deprecated: --emergency never bypassed a gate and has no effect; use
	// `kardinal override` to pass a blocking gate. Deleted in the next minor
	// release (#1288).
	cmd.Flags().BoolVar(&emergencyFlag, "emergency", false, "Deprecated: has no effect")
	_ = cmd.Flags().MarkDeprecated("emergency", "it has no effect; use kardinal override to pass a blocking gate")
	_ = cmd.MarkFlagRequired("env")

	return cmd
}

// rollbackFn is the testable implementation of rollback. The target and the
// rollback Bundle come from lifecycle.PlanRollback, the implementation the UI
// and the automatic rollback share.
func rollbackFn(w io.Writer, c sigs_client.Client, ns, pipeline, envFilter, toBundle string) error {
	ctx := context.Background()

	plan, err := lifecycle.PlanRollback(ctx, c, lifecycle.RollbackRequest{
		Namespace:   ns,
		Pipeline:    pipeline,
		Environment: envFilter,
		ToBundle:    toBundle,
		Actor:       currentUser(),
		Now:         time.Now(),
	})
	if err != nil {
		return err
	}
	if createErr := c.Create(ctx, plan.Bundle); createErr != nil {
		return fmt.Errorf("create rollback bundle: %w", createErr)
	}

	from := plan.CurrentName
	if from == "" {
		from = "(unknown)"
	}
	if _, err := fmt.Fprintf(w,
		"Rolling back %s in %s from %s to %s (%s)\nBundle %s created (rollbackOf=%s)\nTrack with: kardinal explain %s --env %s\n",
		pipeline, envFilter, from, plan.Target.Name, artifactSummary(plan.Bundle),
		plan.Bundle.Name, plan.Target.Name, pipeline, envFilter,
	); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

// artifactSummary lists the images and config commit a Bundle deploys.
func artifactSummary(b *v1alpha1.Bundle) string {
	var parts []string
	for _, img := range b.Spec.Images {
		switch {
		case img.Digest != "":
			parts = append(parts, img.Repository+"@"+img.Digest)
		case img.Tag != "":
			parts = append(parts, img.Repository+":"+img.Tag)
		default:
			parts = append(parts, img.Repository)
		}
	}
	if b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != "" {
		parts = append(parts, "config "+b.Spec.ConfigRef.CommitSHA)
	}
	return strings.Join(parts, ", ")
}
