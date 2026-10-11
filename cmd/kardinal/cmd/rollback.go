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
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func newRollbackCmd() *cobra.Command {
	var (
		envFlag       string
		toFlag        string
		emergencyFlag bool // deprecated, ignored (#1288)
		holdFlag      bool
		reasonFlag    string
		expiresFlag   time.Duration
	)

	cmd := &cobra.Command{
		Use:   "rollback <pipeline> <env>",
		Short: "Roll back a pipeline environment to a previous Bundle",
		Long: `Roll back <env> to the last Bundle Verified there that deploys something else
(or --to a named Bundle), as a new Bundle that goes through the gates, the PR
flow and every upstream environment first. With --hold and --reason, <env>
stays on the rollback until kardinal release-hold (or --hold-expires-in); its
blocking gates pass as audited exemptions meanwhile. See docs/rollback.md.`,
		Example: `  # roll prod back to the previous verified version
  kardinal rollback my-app prod

  # roll back to a named Bundle and keep prod there for a day
  kardinal rollback my-app prod --to my-app-v1-4 --hold --reason "incident 4521" --hold-expires-in 1d`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pipeline, env, err := requireEnv(cmd, args, envFlag)
			if err != nil {
				return err
			}
			if reasonFlag != "" && !holdFlag {
				return fmt.Errorf("rollback: --reason is the reason of a hold; add --hold")
			}
			if holdFlag && strings.TrimSpace(reasonFlag) == "" {
				return fmt.Errorf("rollback --hold needs --reason: say why the environment is held")
			}
			if expiresFlag != 0 && (!holdFlag || expiresFlag < 0) {
				return fmt.Errorf("rollback: --hold-expires-in is a positive duration of a hold; add --hold")
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("rollback: %w", err)
			}
			if lastEnvironment(cmd.Context(), c, ns, pipeline, env) {
				if err := confirm(cmd, fmt.Sprintf("Roll %s of %s back? A rollback Bundle goes through every environment before it.", env, pipeline)); err != nil {
					return err
				}
			}
			if holdFlag {
				// The admission policy accepts a hold only with createdBy
				// set to the requesting Kubernetes user.
				actor, err := kubeUser(cmd.Context(), c)
				if err != nil {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v; recording %s as the hold's createdBy\n", err, currentUser())
					actor = currentUser()
				}
				return rollbackHoldFn(cmd.OutOrStdout(), c, ns, pipeline, env, toFlag, reasonFlag, actor, expiresFlag)
			}
			return rollbackFn(cmd.OutOrStdout(), c, ns, pipeline, env, toFlag)
		},
	}

	addEnvFlag(cmd, &envFlag, "Environment to roll back (or give it after the pipeline)")
	cmd.Flags().StringVar(&toFlag, "to", "", "Specific Bundle name to roll back to")
	cmd.Flags().BoolVar(&holdFlag, "hold", false,
		"Keep the environment on the rollback until kardinal release-hold; the rollback passes blocking gates, each pass audited")
	cmd.Flags().StringVar(&reasonFlag, "reason", "", "Why the environment is held (required with --hold)")
	cmd.Flags().Var(durationValue{&expiresFlag}, "hold-expires-in", "End the hold this long after it is made (30m, 24h, 2d); default: until kardinal release-hold")
	// Deprecated: --emergency never bypassed a gate and has no effect; use
	// `kardinal override` to pass a blocking gate. Deleted in the next minor
	// release (#1288).
	cmd.Flags().BoolVar(&emergencyFlag, "emergency", false, "Deprecated: has no effect")
	_ = cmd.Flags().MarkDeprecated("emergency", "it has no effect and is removed in v0.11; use kardinal override to pass a blocking gate")
	addYesFlag(cmd)

	return withStructuredOutput(cmd)
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
	stampCreator(ctx, c, plan.Bundle)
	if createErr := c.Create(ctx, plan.Bundle); createErr != nil {
		return fmt.Errorf("create rollback bundle: %w", createErr)
	}

	if structured() {
		out := createdOf(plan.Bundle)
		out.Environment, out.RollbackOf, out.Replaces = envFilter, plan.Target.Name, plan.CurrentName
		return writeStructured(w, out)
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
func rollbackHoldFn(w io.Writer, c sigs_client.Client, ns, pipeline, env, toBundle, reason, actor string,
	expiresIn time.Duration) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("rollback --hold needs --reason: say why the environment is held")
	}
	plan, hold, err := lifecycle.RollbackAndHold(context.Background(), c, lifecycle.HoldRequest{
		RollbackRequest: lifecycle.RollbackRequest{
			Namespace:   ns,
			Pipeline:    pipeline,
			Environment: env,
			ToBundle:    toBundle,
			Actor:       actor,
			Now:         time.Now(),
		},
		HoldReason: reason,
		ExpiresIn:  expiresIn,
		Creator:    creatorOf(context.Background(), c),
	})
	if err != nil {
		return err
	}
	if structured() {
		out := createdOf(plan.Bundle)
		out.Environment, out.RollbackOf, out.Replaces = env, plan.Target.Name, plan.CurrentName
		out.Hold = &createdHold{Environment: env, Bundle: hold.Bundle, Reason: hold.Reason, ExpiresAt: hold.ExpiresAt}
		return writeStructured(w, out)
	}
	from := plan.CurrentName
	if from == "" {
		from = "(unknown)"
	}
	if _, err := fmt.Fprintf(w,
		"Rolling back %s in %s from %s to %s (%s)\nBundle %s created (rollbackOf=%s)\n"+
			"Environment %s held on %s: no other Bundle promotes there until: kardinal release-hold %s --env %s\n"+
			"Gates of %s that would block the rollback pass as EXEMPT (audited) while the controller verifies it restores what was Verified there.\n"+
			"Track with: kardinal explain %s --env %s\n",
		pipeline, env, from, plan.Target.Name, artifactSummary(plan.Bundle),
		plan.Bundle.Name, plan.Target.Name, env, hold.Bundle, pipeline, env, env, pipeline, env,
	); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if hold.ExpiresAt != nil {
		if _, err := fmt.Fprintf(w, "The hold expires at %s.\n", hold.ExpiresAt.UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}
	return nil
}

func newReleaseHoldCmd() *cobra.Command {
	var envFlag string
	cmd := &cobra.Command{
		Use:     "release-hold <pipeline> <env>",
		Aliases: []string{"unhold"},
		Short:   "Release the hold of a rollback on an environment",
		Long: `Release the hold kardinal rollback --hold put on <env>. The newest Bundle
held back then promotes into <env> through its normal gates.`,
		Example: `  kardinal release-hold my-app prod
  kardinal unhold my-app prod`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pipeline, env, err := requireEnv(cmd, args, envFlag)
			if err != nil {
				return err
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("release-hold: %w", err)
			}
			return releaseHoldFn(cmd.OutOrStdout(), c, ns, pipeline, env)
		},
	}
	addEnvFlag(cmd, &envFlag, "Held environment to release (or give it after the pipeline)")
	return withStructuredOutput(cmd)
}

// releaseHoldFn is the testable implementation of release-hold.
func releaseHoldFn(w io.Writer, c sigs_client.Client, ns, pipeline, env string) error {
	h, err := lifecycle.ReleaseHold(context.Background(), c, ns, pipeline, env)
	if err != nil {
		return err
	}
	if structured() {
		return writeStructured(w, releasedHold{Pipeline: pipeline, Namespace: ns, Environment: env,
			Bundle: h.Bundle, CreatedBy: h.CreatedBy, Reason: h.Reason, URL: uiURL(ns, pipeline)})
	}
	if _, err := fmt.Fprintf(w, "Released the hold of %s on %s (rollback %s, held by %s: %s).\n",
		pipeline, env, h.Bundle, orUnknown(h.CreatedBy), h.Reason); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func newHoldCmd() *cobra.Command {
	var (
		envFlag, reason string
		expiresIn       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "hold <pipeline> <env> --reason <text>",
		Short: "Keep an environment on what runs there now",
		Long: `Hold <env> on the Bundle that runs there now: no other Bundle promotes into
it until kardinal unhold (or --expires-in). Nothing is rolled back and no
Bundle is created; to go back to an earlier version and keep it, use
kardinal rollback <pipeline> <env> --hold.`,
		Example: `  kardinal hold my-app prod --reason "freeze during the audit"
  kardinal hold my-app prod --reason "INC-4521" --expires-in 4h
  kardinal unhold my-app prod`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pipeline, env, err := requireEnv(cmd, args, envFlag)
			if err != nil {
				return err
			}
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("hold needs --reason: say why the environment is held")
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("hold: %w", err)
			}
			// The admission policy accepts a hold only with createdBy set
			// to the requesting Kubernetes user.
			actor, err := kubeUser(cmd.Context(), c)
			if err != nil {
				_, _ = fmt.Fprintf(errOut, "warning: %v; recording %s as the hold's createdBy\n", err, currentUser())
				actor = currentUser()
			}
			return holdFn(cmd.OutOrStdout(), c, ns, pipeline, env, reason, actor, expiresIn)
		},
	}
	addEnvFlag(cmd, &envFlag, "Environment to hold (or give it after the pipeline)")
	cmd.Flags().StringVar(&reason, "reason", "", "Why the environment is held (required)")
	cmd.Flags().Var(durationValue{&expiresIn}, "expires-in", "End the hold this long after it is made (30m, 4h, 2d); default: until kardinal unhold")
	return withStructuredOutput(cmd)
}

// heldEnvironment is hold -o json|yaml: the hold it made.
type heldEnvironment struct {
	Pipeline    string `json:"pipeline"`
	Namespace   string `json:"namespace"`
	Environment string `json:"environment"`
	// Bundle is the Bundle the environment is held on: what runs there.
	Bundle    string       `json:"bundle"`
	Reason    string       `json:"reason"`
	CreatedBy string       `json:"createdBy,omitempty"`
	ExpiresAt *metav1.Time `json:"expiresAt,omitempty"`
}

// holdFn is the testable implementation of hold (lifecycle.HoldCurrent).
func holdFn(w io.Writer, c sigs_client.Client, ns, pipeline, env, reason, actor string, expiresIn time.Duration) error {
	h, err := lifecycle.HoldCurrent(context.Background(), c, lifecycle.HoldCurrentRequest{
		Namespace: ns, Pipeline: pipeline, Environment: env, Reason: reason, Actor: actor, ExpiresIn: expiresIn, Now: time.Now(),
	})
	if err != nil {
		return err
	}
	if structured() {
		return writeStructured(w, heldEnvironment{Pipeline: pipeline, Namespace: ns, Environment: env, Bundle: h.Bundle,
			Reason: h.Reason, CreatedBy: h.CreatedBy, ExpiresAt: h.ExpiresAt})
	}
	until := "kardinal unhold " + pipeline + " " + env
	if h.ExpiresAt != nil {
		until += " (or " + h.ExpiresAt.UTC().Format(time.RFC3339) + ")"
	}
	return writef(w, "%s %s of %s on %s: no other Bundle promotes there until %s\n",
		sty.Paint(display.ToneHeld, "Held"), env, pipeline, h.Bundle, until)
}

// releasedHold is release-hold -o json|yaml: the hold it released.
type releasedHold struct {
	Pipeline    string `json:"pipeline"`
	Namespace   string `json:"namespace"`
	Environment string `json:"environment"`
	// Bundle is the rollback the environment was held on.
	Bundle    string `json:"bundle"`
	CreatedBy string `json:"createdBy,omitempty"`
	Reason    string `json:"reason,omitempty"`
	// URL is the pipeline in the UI (createdBundle.URL).
	URL string `json:"url"`
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// kubeUser is the Kubernetes user name of the CLI's credentials, from a
// SelfSubjectReview (authentication.k8s.io/v1, Kubernetes 1.28+).
func kubeUser(ctx context.Context, c sigs_client.Client) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ssr := &authenticationv1.SelfSubjectReview{}
	if err := c.Create(ctx, ssr); err != nil {
		return "", fmt.Errorf("cannot read your Kubernetes user name (SelfSubjectReview): %w", err)
	}
	if ssr.Status.UserInfo.Username == "" {
		return "", fmt.Errorf("the SelfSubjectReview returned no user name")
	}
	return ssr.Status.UserInfo.Username, nil
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
