// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
decision it replaces yours. An Approval belongs to its Bundle and is deleted
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

// approvalName is the name of user's Approval of bundle for env: one per user,
// Bundle and environment, so a repeated approve finds it. It is a DNS
// subdomain of at most 253 characters ending in a hash of the user.
func approvalName(bundle, env, user string) string {
	sum := sha256.Sum256([]byte(user))
	suffix := "-" + env + "-" + hex.EncodeToString(sum[:])[:10]
	if limit := 253 - len(suffix); len(bundle) > limit {
		bundle = strings.TrimRight(bundle[:limit], "-.")
	}
	return bundle + suffix
}

// approveFn is the testable implementation of approve.
func approveFn(ctx context.Context, w io.Writer, c sigs_client.Client, ns, bundleName string, o approveOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if o.decision != "approve" && o.decision != "reject" {
		return fmt.Errorf("approve: --decision must be approve or reject, not %q", o.decision)
	}
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: bundleName}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("approve: bundle %s not found in namespace %s", bundleName, ns)
		}
		return fmt.Errorf("approve: get bundle %s: %w", bundleName, err)
	}
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: b.Spec.Pipeline}, &p); err != nil {
		return fmt.Errorf("approve: get pipeline %s of bundle %s: %w", b.Spec.Pipeline, bundleName, err)
	}
	if !slices.ContainsFunc(p.Spec.Environments, func(e v1alpha1.EnvironmentSpec) bool { return e.Name == o.env }) {
		return fmt.Errorf("approve: pipeline %s has no environment %q", p.Name, o.env)
	}
	if !o.revoke && lifecycle.Halted(&b) {
		return fmt.Errorf("approve: bundle %s is %s and never promotes again; approve a newer Bundle", bundleName, b.Status.Phase)
	}
	id, err := identityOf(ctx, c)
	if err != nil {
		return fmt.Errorf("approve: %w", err)
	}
	name := approvalName(bundleName, o.env, id.Username)
	key := types.NamespacedName{Namespace: ns, Name: name}

	var existing v1alpha1.Approval
	getErr := c.Get(ctx, key, &existing)
	switch {
	case getErr == nil && o.revoke:
		if err := c.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("approve: revoke %s: %w", name, err)
		}
		return writef(w, "Revoked: %s no longer %ss %s for %s (Approval %s deleted)\n",
			id.Username, existing.Spec.Decision, bundleName, o.env, name)
	case apierrors.IsNotFound(getErr) && o.revoke:
		return fmt.Errorf("approve: %s has no Approval of %s for %s to revoke", id.Username, bundleName, o.env)
	case getErr == nil && existing.Spec.Decision == o.decision && existing.Spec.Comment == o.comment:
		return writef(w, "Already recorded: %s %ss %s for %s (Approval %s)\n", id.Username, o.decision, bundleName, o.env, name)
	case getErr == nil:
		// An Approval is immutable: a new decision replaces it.
		if err := c.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("approve: replace %s: %w", name, err)
		}
	case !apierrors.IsNotFound(getErr):
		return fmt.Errorf("approve: get approval %s: %w", name, getErr)
	}

	groups := id.Groups
	if groups == nil {
		groups = []string{}
	}
	a := &v1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"kardinal.io/bundle":      bundleName,
				"kardinal.io/environment": o.env,
				"kardinal.io/pipeline":    b.Spec.Pipeline,
			},
			// Deleted with its Bundle (garbage collection).
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1alpha1.GroupVersion.String(), Kind: "Bundle", Name: b.Name, UID: b.UID,
			}},
		},
		Spec: v1alpha1.ApprovalSpec{
			Bundle: bundleName, Environment: o.env, User: id.Username, Groups: groups,
			Decision: o.decision, Comment: o.comment,
		},
	}
	if err := c.Create(ctx, a); err != nil {
		return fmt.Errorf("approve: create approval %s: %w", name, err)
	}
	return writef(w, "Recorded: %s %ss %s for %s (Approval %s)\n", id.Username, o.decision, bundleName, o.env, name)
}

func writef(w io.Writer, format string, args ...interface{}) error {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
