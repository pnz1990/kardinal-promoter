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

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// approveOptions are the flags of kardinal approve.
type approveOptions struct {
	env      string
	comment  string
	decision string
	revoke   bool
}

func newApproveCmd() *cobra.Command {
	var o approveOptions
	cmd := &cobra.Command{
		Use:   "approve <bundle> --env <environment>",
		Short: "Approve (or reject) a Bundle for an environment's approval gates",
		Long: `Approve a Bundle for an environment.

Creates an Approval object for the Bundle and environment in your name: your
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
with it. See docs/policy-gates.md (Approval gates).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("approve: %w", err)
			}
			return approveFn(cmd.Context(), cmd.OutOrStdout(), c, ns, args[0], o)
		},
	}
	cmd.Flags().StringVar(&o.env, "env", "", "Environment the approval is for (required)")
	cmd.Flags().StringVar(&o.comment, "comment", "", "Note shown with the decision")
	cmd.Flags().StringVar(&o.decision, "decision", "approve", "approve or reject")
	cmd.Flags().BoolVar(&o.revoke, "revoke", false, "Delete your Approval instead of recording one")
	_ = cmd.MarkFlagRequired("env")
	return cmd
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
