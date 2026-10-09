// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newRejectCmd() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "reject <bundle>",
		Short: "Reject a Bundle: it is never promoted again, and rollback never picks it",
		Long: `Reject a Bundle.

Sets spec.rejected on the Bundle with your Kubernetes username (read from the
API server with a SelfSubjectReview, as kubectl auth whoami does) and the
reason. The Bundle turns Rejected, whatever its phase:

  - no new PromotionStep is created for it;
  - its steps that have not delivered the change (Pending, Promoting, or
    WaitingForMerge with the PR still open) fail, and their PRs are closed;
  - a step whose change is live (HealthChecking, or a PR that merged) keeps
    going and is health-checked;
  - kardinal rollback, onHealthFailure=rollback and kardinal promote never
    pick it or any Bundle carrying its images or config commit, and a
    Subscription creates no Bundle for them.

Rejecting is final: spec.rejected cannot be changed or removed. It does not
revert an environment that already runs the Bundle; roll that environment
back with kardinal rollback. See docs/rollback.md.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("reject: %w", err)
			}
			return rejectFn(cmd.Context(), cmd.OutOrStdout(), c, ns, args[0], reason, time.Now())
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Why the Bundle is rejected (required)")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

// rejectFn is the testable implementation of reject. It sets spec.rejected
// with a merge patch guarded by the Bundle's resourceVersion, so a rejection
// written by someone else in between is reported, not overwritten (the CRD
// refuses the change anyway).
func rejectFn(ctx context.Context, w io.Writer, c sigs_client.Client, ns, name, reason string, now time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("reject: --reason is required")
	}
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("reject: bundle %s not found in namespace %s", name, ns)
		}
		return fmt.Errorf("reject: get bundle %s: %w", name, err)
	}
	if r := b.Spec.Rejected; r != nil {
		return fmt.Errorf("reject: bundle %s was already rejected by %s: %s", name, r.By, r.Reason)
	}
	id, err := identityOf(ctx, c)
	if err != nil {
		return fmt.Errorf("reject: %w", err)
	}
	patch := sigs_client.MergeFromWithOptions(b.DeepCopy(), sigs_client.MergeFromWithOptimisticLock{})
	at := metav1.NewTime(now.UTC().Truncate(time.Second))
	b.Spec.Rejected = &v1alpha1.BundleRejection{Reason: reason, By: id.Username, At: &at}
	if err := c.Patch(ctx, &b, patch); err != nil {
		if apierrors.IsConflict(err) {
			return fmt.Errorf("reject: bundle %s changed while rejecting it; run the command again: %w", name, err)
		}
		return fmt.Errorf("reject: patch bundle %s: %w", name, err)
	}
	phase := b.Status.Phase
	if phase == "" {
		phase = "new"
	}
	if _, err := fmt.Fprintf(w,
		"Bundle %s rejected by %s (was %s): %s\nIt is never promoted again; its unfinished steps are cancelled.\n"+
			"An environment that already runs it keeps it: roll back with kardinal rollback %s --env <env>.\n",
		name, id.Username, phase, reason, b.Spec.Pipeline); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
