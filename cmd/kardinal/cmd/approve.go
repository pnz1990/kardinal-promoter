// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	"slices"
	"strings"
	"time"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	pipelinerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// approveOptions are the flags of kardinal approve.
type approveOptions struct {
	version  string
	env      string
	comment  string
	decision string
	revoke   bool
}

func newApproveCmd() *cobra.Command {
	var o approveOptions
	cmd := &cobra.Command{
		Use:   "approve <pipeline> <env>",
		Short: "Approve (or reject) the version waiting for an environment's approval gates",
		Long: `Record your approval (or, with --decision reject, a rejection) of the version
waiting to enter <env> of <pipeline>, in your Kubernetes name; give a Bundle
instead with kardinal approve <bundle> --env <env>. An approval gate passes
once enough allowed people approved and nobody rejected. --revoke deletes
your Approval.`,
		Example: `  kardinal approve my-app prod --comment "checked the canary"

  # a named Bundle, the older form
  kardinal approve my-app-v1-5 --env prod

  # decline (a gate rejection, not kardinal reject)
  kardinal approve my-app prod --decision reject

  # take your decision back
  kardinal approve my-app prod --revoke`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("approve: %w", err)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			bundle, env, err := approveTarget(ctx, c, ns, cmd, args, o.env)
			if err != nil {
				return err
			}
			o.env = env
			// Say which version it is, refuse one --version does not
			// name, and ask on a terminal (--yes answers): a newer
			// version may have arrived since it was reviewed.
			version, err := approvalVersion(ctx, c, ns, bundle)
			if err != nil {
				return err
			}
			if o.version != "" && o.version != version {
				return fmt.Errorf("approve: the version waiting for %s is %s (%s), not %s: nothing recorded",
					env, versionOr(version, bundle), bundle, o.version)
			}
			verb := o.decision
			if o.revoke {
				verb = "revoke your decision on"
			}
			if err := confirm(cmd, fmt.Sprintf("%s %s (%s) for %s?", upperFirst(verb), versionOr(version, bundle), bundle, env)); err != nil {
				return err
			}
			return approveFn(ctx, cmd.OutOrStdout(), c, ns, bundle, o)
		},
	}
	addEnvFlag(cmd, &o.env, "Environment the approval is for (or give it after the pipeline)")
	cmd.Flags().StringVar(&o.comment, "comment", "", "Note shown with the decision")
	cmd.Flags().StringVar(&o.decision, "decision", "approve", "approve or reject")
	cmd.Flags().BoolVar(&o.revoke, "revoke", false, "Delete your Approval instead of recording one")
	cmd.Flags().StringVar(&o.version, "version", "", "Approve only if this is the version waiting (the image tag, config commit or chart version)")
	addYesFlag(cmd)
	withReference(cmd, approveReference)
	return cmd
}

// approveTarget is the Bundle and environment kardinal approve records a
// decision for: <bundle> --env <env> (the older form: args[0] names a
// Bundle), or <pipeline> <env>, the Bundle waiting to enter env
// (waitingBundle).
func approveTarget(ctx context.Context, c sigs_client.Client, ns string, cmd *cobra.Command, args []string, flagEnv string) (string, string, error) {
	first, env, err := envArgs(cmd, args, flagEnv)
	if err != nil {
		return "", "", err
	}
	if len(args) == 1 {
		var b v1alpha1.Bundle
		err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: first}, &b)
		if err == nil {
			if env == "" {
				return "", "", fmt.Errorf("which environment? Give it with --env: kardinal approve %s --env <env>", first)
			}
			return first, env, nil
		}
		if !apierrors.IsNotFound(err) {
			return "", "", fmt.Errorf("approve: get bundle %s: %w", first, err)
		}
		var p v1alpha1.Pipeline
		if perr := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: first}, &p); perr != nil {
			return "", "", fmt.Errorf("approve: bundle %s not found in namespace %s (nor a pipeline of that name)", first, ns)
		}
	}
	if env == "" {
		return "", "", fmt.Errorf("which environment? Give it after the pipeline: kardinal approve %s <env>", first)
	}
	bundle, err := waitingBundle(ctx, c, ns, first, env)
	if err != nil {
		return "", "", err
	}
	return bundle, env, nil
}

// waitingBundle is the Bundle of pipeline waiting to enter env: the newer
// Bundle on its way in (status.environments' incomingBundle), else the
// pipeline's current Bundle while it promotes and has env still ahead
// (approving before it reaches the gate is fine). An environment that runs
// the newest version already, with nothing on its way, is an error.
func waitingBundle(ctx context.Context, c sigs_client.Client, ns, pipeline, env string) (string, error) {
	p, err := requirePipeline(ctx, c, ns, pipeline)
	if err != nil {
		return "", err
	}
	if err := checkPipelineEnv(ctx, c, ns, pipeline, env); err != nil {
		return "", err
	}
	var bundles v1alpha1.BundleList
	if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err != nil {
		return "", fmt.Errorf("approve: list bundles: %w", err)
	}
	envs := p.Status.Environments
	var steps []v1alpha1.PromotionStep
	if len(envs) == 0 {
		// A controller that predates status.environments.
		var sl v1alpha1.PromotionStepList
		var gl v1alpha1.PolicyGateList
		sel := sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline}
		if err := c.List(ctx, &sl, sigs_client.InNamespace(ns), sel); err != nil {
			return "", fmt.Errorf("approve: list promotion steps: %w", err)
		}
		if err := c.List(ctx, &gl, sigs_client.InNamespace(ns), sel); err != nil {
			return "", fmt.Errorf("approve: list policy gates: %w", err)
		}
		steps = sl.Items
		envs = pipelinerec.ProjectEnvironments(p, bundles.Items, sl.Items, gl.Items, nil, time.Time{})
	}
	running := ""
	for _, e := range envs {
		if e.Name != env {
			continue
		}
		if e.IncomingBundle != "" {
			return e.IncomingBundle, nil
		}
		running = e.Bundle
	}
	var mine []v1alpha1.Bundle
	for _, b := range bundles.Items {
		if b.Spec.Pipeline == pipeline {
			mine = append(mine, b)
		}
	}
	if cur := lifecycle.CurrentBundle(mine, steps); cur != nil && cur.Name != running && lifecycle.InFlightPhase(cur.Status.Phase) {
		if promoted, err := graph.PromotedEnvironments(p, cur); err == nil && slices.Contains(promoted, env) {
			return cur.Name, nil
		}
	}
	what := "nothing runs there yet"
	if running != "" {
		what = running + " runs there"
	}
	return "", fmt.Errorf("approve: no version waits for %s of %s: %s and no newer one is on its way; name one: kardinal approve <bundle> --env %s",
		env, pipeline, what, env)
}

// approvalVersion is what Bundle name ships, announced on stderr before a
// decision on it.
func approvalVersion(ctx context.Context, c sigs_client.Client, ns, name string) (string, error) {
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("approve: bundle %s not found in namespace %s", name, ns)
		}
		return "", fmt.Errorf("approve: get bundle %s: %w", name, err)
	}
	version := createdOf(&b).Version
	_, _ = fmt.Fprintf(errOut, "%s %s (%s)\n", sty.Dim("Version:"), versionOr(version, name), name)
	return version, nil
}

// upperFirst is s with its first letter in upper case.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// approveFn is the testable implementation of approve. The decision is
// recorded by lifecycle.RecordApproval, which the UI shares.
func approveFn(ctx context.Context, w io.Writer, c sigs_client.Client, ns, bundleName string, o approveOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if o.decision != lifecycle.DecisionApprove && o.decision != lifecycle.DecisionReject {
		return fmt.Errorf("approve: --decision must be approve or reject, not %q", o.decision)
	}
	// The bundle and environment are checked before the identity is read,
	// so a typo is reported as such.
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: bundleName}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("approve: bundle %s not found in namespace %s", bundleName, ns)
		}
		return fmt.Errorf("approve: get bundle %s: %w", bundleName, err)
	}
	id, err := identityOf(ctx, c)
	if err != nil {
		return fmt.Errorf("approve: %w", err)
	}
	outcome, a, err := lifecycle.RecordApproval(ctx, c, lifecycle.ApprovalRequest{
		Namespace: ns, Bundle: bundleName, Environment: o.env, User: id.Username, Groups: id.Groups,
		Decision: o.decision, Comment: o.comment, Revoke: o.revoke,
	})
	if err != nil {
		return fmt.Errorf("approve: %w", err)
	}
	switch outcome {
	case lifecycle.ApprovalRevoked:
		return writef(w, "Revoked: %s no longer %ss %s for %s (Approval %s deleted)\n",
			id.Username, a.Spec.Decision, bundleName, o.env, a.Name)
	case lifecycle.ApprovalUnchanged:
		return writef(w, "Already recorded: %s %ss %s for %s (Approval %s)\n", id.Username, o.decision, bundleName, o.env, a.Name)
	}
	return writef(w, "Recorded: %s %ss %s for %s (Approval %s)\n", id.Username, o.decision, bundleName, o.env, a.Name)
}

func writef(w io.Writer, format string, args ...interface{}) error {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// approveReference is the detail of the command for its reference page.
const approveReference = `Creates an Approval object for the Bundle and environment in your name: your
Kubernetes username and groups, read from the API server with a
SelfSubjectReview (as kubectl auth whoami does). The chart's
ValidatingAdmissionPolicy refuses an Approval in anyone else's name.

A PolicyGate with spec.approval on that environment counts the Approvals of
the Bundle: it is ready when its expression is true, at least
approval.required allowed people (approval.allowedUsers or allowedGroups)
approved, and none of them rejected. The gate then lets the environment's
PromotionStep start, for approval: auto environments as for pr-review ones.
Approving before the Bundle reaches the gate is fine.

  --decision reject   records a rejection: it blocks the gate.
  --revoke            deletes your Approval for the Bundle and environment.

Running approve again with the same decision does nothing; with another
decision it replaces yours. Your Approval is found by its labels and
spec.user, not by its name; if the name it would get is taken, the API
server generates one. An Approval belongs to its Bundle and is deleted
with it. See docs/approvals.md.`
