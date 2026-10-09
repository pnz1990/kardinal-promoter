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
		holdFlag      bool
		reasonFlag    string
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
whichever Bundles deployed them. An image or config Bundle goes back to the
newest earlier images or config commit, also when a mixed Bundle deployed them,
and only those: the rest stays as deployed. --to a Bundle whose type cannot
deploy what the deployed Bundle changed is refused.

With --hold (and --reason), the environment stays on the rollback: no other
Bundle promotes into it until kardinal release-hold <pipeline> --env <env>.
The rollback is never superseded, and it passes every PolicyGate on its way
that would block it, each pass shown as EXEMPT with who held the
environment and why, recorded as a GateEvaluated AuditEvent and a
GateExempted Warning Event. See docs/rollback.md.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if reasonFlag != "" && !holdFlag {
				return fmt.Errorf("rollback: --reason is the reason of a hold; add --hold")
			}
			if holdFlag && strings.TrimSpace(reasonFlag) == "" {
				return fmt.Errorf("rollback --hold needs --reason: say why the environment is held")
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("rollback: %w", err)
			}
			if holdFlag {
				return rollbackHoldFn(cmd.OutOrStdout(), c, ns, args[0], envFlag, toFlag, reasonFlag)
			}
			return rollbackFn(cmd.OutOrStdout(), c, ns, args[0], envFlag, toFlag)
		},
	}

	cmd.Flags().StringVar(&envFlag, "env", "", "Target environment to roll back (required)")
	cmd.Flags().StringVar(&toFlag, "to", "", "Specific Bundle name to roll back to")
	cmd.Flags().BoolVar(&holdFlag, "hold", false,
		"Keep the environment on the rollback until kardinal release-hold; the rollback passes blocking gates, each pass audited")
	cmd.Flags().StringVar(&reasonFlag, "reason", "", "Why the environment is held (required with --hold)")
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

// rollbackHoldFn rolls back and holds the environment
// (lifecycle.RollbackAndHold, the implementation the UI shares).
func rollbackHoldFn(w io.Writer, c sigs_client.Client, ns, pipeline, env, toBundle, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("rollback --hold needs --reason: say why the environment is held")
	}
	plan, hold, err := lifecycle.RollbackAndHold(context.Background(), c, lifecycle.HoldRequest{
		RollbackRequest: lifecycle.RollbackRequest{
			Namespace:   ns,
			Pipeline:    pipeline,
			Environment: env,
			ToBundle:    toBundle,
			Actor:       currentUser(),
			Now:         time.Now(),
		},
		HoldReason: reason,
	})
	if err != nil {
		return err
	}
	from := plan.CurrentName
	if from == "" {
		from = "(unknown)"
	}
	if _, err := fmt.Fprintf(w,
		"Rolling back %s in %s from %s to %s (%s)\nBundle %s created (rollbackOf=%s)\n"+
			"Environment %s held on %s: no other Bundle promotes there until: kardinal release-hold %s --env %s\n"+
			"Gates that would block the rollback pass as EXEMPT (audited).\nTrack with: kardinal explain %s --env %s\n",
		pipeline, env, from, plan.Target.Name, artifactSummary(plan.Bundle),
		plan.Bundle.Name, plan.Target.Name, env, hold.Bundle, pipeline, env, pipeline, env,
	); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func newReleaseHoldCmd() *cobra.Command {
	var envFlag string
	cmd := &cobra.Command{
		Use:   "release-hold <pipeline>",
		Short: "Release the hold of a rollback on an environment",
		Long: `Release the hold kardinal rollback --hold put on an environment.

Removes the environment from the Pipeline's spec.holds. The newest Bundle that
was held back then promotes into the environment through its normal gates,
and the rollback Bundle's gates are evaluated without the exemption again.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("release-hold: %w", err)
			}
			return releaseHoldFn(cmd.OutOrStdout(), c, ns, args[0], envFlag)
		},
	}
	cmd.Flags().StringVar(&envFlag, "env", "", "Held environment to release (required)")
	_ = cmd.MarkFlagRequired("env")
	return cmd
}

// releaseHoldFn is the testable implementation of release-hold.
func releaseHoldFn(w io.Writer, c sigs_client.Client, ns, pipeline, env string) error {
	h, err := lifecycle.ReleaseHold(context.Background(), c, ns, pipeline, env)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Released the hold of %s on %s (rollback %s, held by %s: %s).\n",
		pipeline, env, h.Bundle, orUnknown(h.CreatedBy), h.Reason); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
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
