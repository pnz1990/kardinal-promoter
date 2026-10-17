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
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	actorpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

func newRollbackCmd() *cobra.Command {
	var (
		envFlag     string
		toFlag      string
		holdFlag    bool
		reasonFlag  string
		expiresFlag time.Duration
	)

	cmd := &cobra.Command{
		Use:   "rollback <pipeline> <env>",
		Short: "Roll back an environment to the version before",
		Long: `Roll back <env> to the last version Verified there that deploys something
else (or --to a named Bundle), as a new Bundle through <env>'s gates and PR
flow; upstream environments that verified it are not promoted again. With
--hold and --reason, <env> stays on the rollback until kardinal unhold (or
--hold-expires-in); its blocking gates pass as audited exemptions. See docs/rollback.md.`,
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
			r := rollbackRequest{pipeline: pipeline, env: env, to: toFlag, hold: holdFlag, reason: reasonFlag, expires: expiresFlag}
			if err := r.check(); err != nil {
				return err
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("rollback: %w", err)
			}
			return rollbackDo(contextOf(cmd), cmd.OutOrStdout(), cmd.ErrOrStderr(), c, ns, r, func() error {
				return confirm(cmd, fmt.Sprintf("Roll %s of %s back? The rollback goes through every environment before it.", env, pipeline))
			})
		},
	}

	addEnvFlag(cmd, &envFlag, "Environment to roll back (or give it after the pipeline)")
	cmd.Flags().StringVar(&toFlag, "to", "", "The Bundle, or version (image tag), to roll back to (default: the version Verified there before)")
	cmd.Flags().BoolVar(&holdFlag, "hold", false,
		"Keep the environment on the rollback until kardinal unhold; the rollback passes blocking gates, each pass audited")
	cmd.Flags().StringVar(&reasonFlag, "reason", "", "Why the environment is held (required with --hold)")
	cmd.Flags().Var(durationValue{&expiresFlag}, "hold-expires-in", "End the hold this long after it is made (30m, 24h, 2d); default: until kardinal unhold")
	addYesFlag(cmd)

	return withStructuredOutput(cmd)
}

// rollbackRequest is what kardinal rollback (and kardinal mcp's rollback
// tool) asks for: roll env of pipeline back, to the Bundle or version to
// (default: the one Verified there before), and with hold keep it there.
type rollbackRequest struct {
	pipeline, env, to, reason string
	hold                      bool
	expires                   time.Duration
}

// check refuses a reason without a hold, a hold without a reason, and an
// expiry without a hold.
func (r rollbackRequest) check() error {
	if r.reason != "" && !r.hold {
		return fmt.Errorf("rollback: --reason is the reason of a hold; add --hold")
	}
	if r.hold && strings.TrimSpace(r.reason) == "" {
		return fmt.Errorf("rollback --hold needs --reason: say why the environment is held")
	}
	if r.expires != 0 && (!r.hold || r.expires < 0) {
		return fmt.Errorf("rollback: --hold-expires-in is a positive duration of a hold; add --hold")
	}
	return nil
}

// rollbackDo resolves r.to, asks (ask, when not nil) before it rolls back
// the last environment of the pipeline, and rolls back, with the hold when
// r.hold. Warnings go to errw.
func rollbackDo(ctx context.Context, w, errw io.Writer, c sigs_client.Client, ns string, r rollbackRequest, ask func() error) error {
	to, err := resolveBundleRef(ctx, c, ns, r.pipeline, r.to)
	if err != nil {
		return fmt.Errorf("rollback: --to: %w", err)
	}
	if ask != nil && lastEnvironment(ctx, c, ns, r.pipeline, r.env) {
		if err := ask(); err != nil {
			return err
		}
	}
	if r.hold {
		// The admission policy accepts a hold only with createdBy set to
		// the requesting Kubernetes user.
		actor, err := kubeUser(ctx, c)
		if err != nil {
			_, _ = fmt.Fprintf(errw, "warning: %v; recording %s as the hold's createdBy\n", err, currentUser())
			actor = currentUser()
		}
		return rollbackHoldFn(w, c, ns, r.pipeline, r.env, to, r.reason, actor, r.expires)
	}
	return rollbackFn(w, c, ns, r.pipeline, r.env, to)
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
	if err := stampCreator(ctx, c, plan.Bundle); err != nil {
		return fmt.Errorf("rollback: %w", err)
	}
	if createErr := c.Create(ctx, plan.Bundle); createErr != nil {
		return fmt.Errorf("create rollback bundle: %w", createErr)
	}

	if structured() {
		out := createdOf(plan.Bundle)
		out.Environment, out.RollbackOf, out.Replaces = envFilter, plan.Target.Name, plan.CurrentName
		out.Fleet = plan.Fleet
		return writeStructured(w, out)
	}
	from := plan.CurrentName
	if from == "" {
		from = "(unknown)"
	}
	if plan.Fleet != nil {
		return writeFleetRollback(w, c, ns, pipeline, envFilter, plan)
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

// fleetRollbackNamed is how many changing targets the rollback of a fleet
// names before "and N more".
const fleetRollbackNamed = 6

// writeFleetRollback prints the rollback of a whole fleet: the version it
// goes back to, which targets it changes (from what), which run it already,
// and that a paused fleet starts none until it is resumed.
func writeFleetRollback(w io.Writer, c sigs_client.Client, ns, pipeline, fleet string, plan *lifecycle.RollbackPlan) error {
	var changing []string
	stay := 0
	for _, t := range plan.Fleet {
		switch {
		case !t.Changes:
			stay++
		case t.Runs == "":
			changing = append(changing, t.Environment+" (nothing yet)")
		default:
			changing = append(changing, t.Environment+" from "+t.Runs)
		}
	}
	named := changing
	if len(named) > fleetRollbackNamed {
		named = append(named[:fleetRollbackNamed:fleetRollbackNamed], fmt.Sprintf("and %d more", len(changing)-fleetRollbackNamed))
	}
	if err := writef(w, "Rolling back fleet %s of %s to %s (%s)\nBundle %s created (rollbackOf=%s)\n",
		fleet, pipeline, plan.Target.Name, artifactSummary(plan.Bundle), plan.Bundle.Name, plan.Target.Name); err != nil {
		return err
	}
	if err := writef(w, "%d of %d targets go back: %s\n", len(changing), len(plan.Fleet), strings.Join(named, ", ")); err != nil {
		return err
	}
	if stay > 0 {
		if err := writef(w, "%d run %s already and are Verified at once.\n", stay, plan.Target.Name); err != nil {
			return err
		}
	}
	var p v1alpha1.Pipeline
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: pipeline}, &p); err == nil {
		if lifecycle.FleetPaused(&p, fleet) {
			if err := writef(w, "Fleet %s is paused: the rollback starts no target until kardinal resume %s --env %s\n",
				fleet, pipeline, fleet); err != nil {
				return err
			}
		}
	}
	return writef(w, "The fleet's maxConcurrent paces the rollback. Track with: kardinal get pipelines %s\n", pipeline)
}

// rollbackHoldFn rolls back and holds the environment
// (lifecycle.RollbackAndHold, the implementation the UI shares).
func rollbackHoldFn(w io.Writer, c sigs_client.Client, ns, pipeline, env, toBundle, reason, actor string,
	expiresIn time.Duration) error {
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("rollback --hold needs --reason: say why the environment is held")
	}
	var stamp *v1alpha1.Actor
	if id, idErr := identityOf(context.Background(), c); idErr == nil {
		var err error
		if stamp, err = stampOf(context.Background(), c, id, actorpkg.Where{Namespace: ns, Pipeline: pipeline}); err != nil {
			return fmt.Errorf("rollback: %w", err)
		}
	}
	plan, hold, err := lifecycle.RollbackAndHold(context.Background(), c, lifecycle.HoldRequest{
		RollbackRequest: lifecycle.RollbackRequest{
			Namespace:   ns,
			Pipeline:    pipeline,
			Environment: env,
			ToBundle:    toBundle,
			Actor:       actor,
			Stamp:       stamp,
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
		out.Fleet = plan.Fleet
		return writeStructured(w, out)
	}
	from := plan.CurrentName
	if from == "" {
		from = "(unknown)"
	}
	if _, err := fmt.Fprintf(w,
		"Rolling back %s in %s from %s to %s (%s)\nBundle %s created (rollbackOf=%s)\n"+
			"Environment %s held on %s: no other version promotes there until: kardinal unhold %s %s\n"+
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
	var envFlag, reason string
	cmd := &cobra.Command{
		Use:     "unhold <pipeline> <env>",
		Aliases: []string{"release-hold"},
		Short:   "Release an environment's hold (kardinal hold, or rollback --hold)",
		Long: `Release the hold on <env>, whether kardinal hold or kardinal rollback --hold
put it there. The newest version held back then promotes into <env> through
its normal gates. release-hold is the same command.`,
		Example: `  kardinal unhold my-app prod
  kardinal release-hold my-app prod`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pipeline, env, err := requireEnv(cmd, args, envFlag)
			if err != nil {
				return err
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("unhold: %w", err)
			}
			return releaseHoldFn(cmd.OutOrStdout(), c, ns, pipeline, env, reason)
		},
	}
	addEnvFlag(cmd, &envFlag, "Held environment to release (or give it after the pipeline)")
	cmd.Flags().StringVar(&reason, "reason", "", "why, recorded with your name in the HoldReleased AuditEvent")
	return withStructuredOutput(cmd)
}

// releaseHoldFn is the testable implementation of unhold (release-hold).
func releaseHoldFn(w io.Writer, c sigs_client.Client, ns, pipeline, env, reason string) error {
	who, err := changeBy(context.Background(), c, reason, actorpkg.Where{Namespace: ns, Pipeline: pipeline, Environment: env})
	if err != nil {
		return fmt.Errorf("unhold: %w", err)
	}
	h, err := lifecycle.ReleaseHold(context.Background(), c, ns, pipeline, env, who)
	if err != nil {
		return err
	}
	if structured() {
		return writeStructured(w, releasedHold{Pipeline: pipeline, Namespace: ns, Environment: env,
			Bundle: h.Bundle, CreatedBy: h.CreatedBy, Reason: h.Reason, URL: uiURL(ns, pipeline)})
	}
	held := h.Bundle
	var b v1alpha1.Bundle
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: h.Bundle}, &b); err == nil {
		held = versionLabel(createdOf(&b).Version, h.Bundle)
	}
	if _, err := fmt.Fprintf(w, "Released the hold of %s on %s (held on %s by %s: %s).\n",
		pipeline, env, held, orUnknown(h.CreatedBy), h.Reason); err != nil {
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
		Long: `Hold <env> on the version that runs there now: no other version promotes into
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
	h, existed, err := lifecycle.HoldCurrent(context.Background(), c, lifecycle.HoldCurrentRequest{
		Namespace: ns, Pipeline: pipeline, Environment: env, Reason: reason, Actor: actor, Stamp: bestStamp(c, actorpkg.Where{Namespace: ns, Pipeline: pipeline, Environment: env}),
		ExpiresIn: expiresIn, Now: time.Now(),
	})
	if err != nil {
		return err
	}
	if existed && !structured() {
		return writef(w, "%s %s of %s is held on %s already, for that reason: nothing changed\n",
			sty.Paint(display.ToneHeld, "Held"), env, pipeline, h.Bundle)
	}
	if structured() {
		return writeStructured(w, heldEnvironment{Pipeline: pipeline, Namespace: ns, Environment: env, Bundle: h.Bundle,
			Reason: h.Reason, CreatedBy: h.CreatedBy, ExpiresAt: h.ExpiresAt})
	}
	until := "kardinal unhold " + pipeline + " " + env
	if h.ExpiresAt != nil {
		until += " (or " + h.ExpiresAt.UTC().Format(time.RFC3339) + ")"
	}
	return writef(w, "%s %s of %s on %s: no other version promotes there until %s\n",
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

// bestStamp is the actor stamp of this CLI's user for a write at where, nil
// when it cannot be read: a hold or a freeze names its createdBy all the
// same, and its AuditEvent then names no actor kind.
func bestStamp(c sigs_client.Client, where actorpkg.Where) *v1alpha1.Actor {
	id, err := identityOf(context.Background(), c)
	if err != nil {
		return nil
	}
	stamp, err := stampOf(context.Background(), c, id, where)
	if err != nil {
		return nil
	}
	return stamp
}
