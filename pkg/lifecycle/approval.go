// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// AnnotationRecordedVia on an Approval says which kardinal client wrote it
// for its user: "ui" for the UI API, which writes as the controller after
// authenticating the user (TokenReview). The chart's approvals admission
// policy lets the controller create an Approval only with this annotation,
// and delete only Approvals that carry it.
const AnnotationRecordedVia = "kardinal.io/recorded-via"

// Approval decisions.
const (
	DecisionApprove = "approve"
	DecisionReject  = "reject"
)

// ApprovalRequest is one approve, reject or revoke of a Bundle for an
// environment's approval gates (kardinal approve, the UI).
type ApprovalRequest struct {
	Namespace   string
	Bundle      string
	Environment string
	// User and Groups are who decides: the authenticated identity, never a
	// name the caller typed.
	User   string
	Groups []string
	// Decision is DecisionApprove or DecisionReject; ignored with Revoke.
	Decision string
	Comment  string
	// Revoke deletes the user's Approval instead of recording one.
	Revoke bool
	// Via, when set, is written as AnnotationRecordedVia.
	Via string
}

// ApprovalOutcome is what RecordApproval did.
type ApprovalOutcome string

// RecordApproval outcomes.
const (
	ApprovalRecorded  ApprovalOutcome = "Recorded"
	ApprovalUnchanged ApprovalOutcome = "Already recorded"
	ApprovalRevoked   ApprovalOutcome = "Revoked"
)

// ApprovalName is the name of user's Approval of bundle for env: one per
// user, Bundle and environment, so a repeated approve finds it. It is a DNS
// subdomain of at most 253 characters ending in a hash of the user.
func ApprovalName(bundle, env, user string) string {
	sum := sha256.Sum256([]byte(user))
	suffix := "-" + env + "-" + hex.EncodeToString(sum[:])[:10]
	if limit := 253 - len(suffix); len(bundle) > limit {
		bundle = strings.TrimRight(bundle[:limit], "-.")
	}
	return bundle + suffix
}

// generatePrefix is a generateName for name: the API server appends five
// characters, and the result must stay a DNS subdomain of 253.
func generatePrefix(name string) string {
	if len(name) > 247 {
		name = strings.TrimRight(name[:247], "-.")
	}
	return name + "-"
}

// RecordApproval records req.User's decision on a Bundle for an environment,
// replaces an earlier one, or revokes it. The user's Approval is found by its
// labels and spec.user, not by name: anyone can create an object under the
// name ApprovalName derives, and an Approval of an earlier Bundle of the same
// name is not counted (the Graph matches spec.bundleUID). An Approval is
// immutable, so a new decision replaces the old one. It returns what it did
// and the Approval concerned.
func RecordApproval(ctx context.Context, c client.Client, req ApprovalRequest) (ApprovalOutcome, *v1alpha1.Approval, error) {
	if !req.Revoke && req.Decision != DecisionApprove && req.Decision != DecisionReject {
		return "", nil, fmt.Errorf("decision must be approve or reject, not %q: %w", req.Decision, ErrInvalid)
	}
	if req.User == "" {
		return "", nil, fmt.Errorf("an approval needs an authenticated user: %w", ErrInvalid)
	}
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Bundle}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil, fmt.Errorf("bundle %s not found in namespace %s: %w", req.Bundle, req.Namespace, ErrNotFound)
		}
		return "", nil, fmt.Errorf("get bundle %s: %w", req.Bundle, err)
	}
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: b.Spec.Pipeline}, &p); err != nil {
		return "", nil, fmt.Errorf("get pipeline %s of bundle %s: %w", b.Spec.Pipeline, req.Bundle, err)
	}
	if !graph.HasEnvironment(&p, req.Environment) { // a fleet target included
		return "", nil, fmt.Errorf("pipeline %s has no environment %q: %w", p.Name, req.Environment, ErrInvalid)
	}
	if !req.Revoke && Halted(&b) {
		return "", nil, fmt.Errorf("bundle %s is %s and never promotes again; approve a newer Bundle: %w",
			req.Bundle, b.Status.Phase, ErrConflict)
	}
	var list v1alpha1.ApprovalList
	if err := c.List(ctx, &list, client.InNamespace(req.Namespace),
		client.MatchingLabels{"kardinal.io/bundle": req.Bundle, "kardinal.io/environment": req.Environment}); err != nil {
		return "", nil, fmt.Errorf("list approvals of %s: %w", req.Bundle, err)
	}
	var mine []v1alpha1.Approval
	for _, a := range list.Items {
		if a.Spec.User != req.User {
			continue // never count, replace or revoke someone else's
		}
		if a.Spec.BundleUID != string(b.UID) {
			if err := c.Delete(ctx, &a); err != nil && !apierrors.IsNotFound(err) {
				return "", nil, fmt.Errorf("delete %s, the Approval of an earlier Bundle %s: %w", a.Name, req.Bundle, err)
			}
			continue
		}
		mine = append(mine, a)
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Name < mine[j].Name })

	switch {
	case req.Revoke && len(mine) == 0:
		return "", nil, fmt.Errorf("%s has no Approval of %s for %s to revoke: %w", req.User, req.Bundle, req.Environment, ErrNotFound)
	case req.Revoke:
		for i := range mine {
			if err := c.Delete(ctx, &mine[i]); err != nil && !apierrors.IsNotFound(err) {
				return "", nil, fmt.Errorf("revoke %s: %w", mine[i].Name, err)
			}
		}
		return ApprovalRevoked, &mine[0], nil
	case len(mine) == 1 && mine[0].Spec.Decision == req.Decision && mine[0].Spec.Comment == req.Comment:
		return ApprovalUnchanged, &mine[0], nil
	}
	for i := range mine {
		if err := c.Delete(ctx, &mine[i]); err != nil && !apierrors.IsNotFound(err) {
			return "", nil, fmt.Errorf("replace %s: %w", mine[i].Name, err)
		}
	}
	name := ApprovalName(req.Bundle, req.Environment, req.User)
	groups := req.Groups
	if groups == nil {
		groups = []string{}
	}
	a := &v1alpha1.Approval{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: req.Namespace,
			Labels: map[string]string{
				"kardinal.io/bundle":      req.Bundle,
				"kardinal.io/environment": req.Environment,
				"kardinal.io/pipeline":    b.Spec.Pipeline,
			},
			// Deleted with its Bundle (garbage collection).
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1alpha1.GroupVersion.String(), Kind: "Bundle", Name: b.Name, UID: b.UID,
			}},
		},
		Spec: v1alpha1.ApprovalSpec{
			Bundle: req.Bundle, BundleUID: string(b.UID), Environment: req.Environment, User: req.User, Groups: groups,
			Decision: req.Decision, Comment: req.Comment,
		},
	}
	if req.Via != "" {
		a.Annotations = map[string]string{AnnotationRecordedVia: req.Via}
	}
	err := c.Create(ctx, a)
	if apierrors.IsAlreadyExists(err) {
		// The name is taken (by someone else's object, or a revoke still
		// finishing): let the API server pick one.
		a.Name, a.GenerateName = "", generatePrefix(name)
		a.ResourceVersion = ""
		err = c.Create(ctx, a)
	}
	if err != nil {
		return "", nil, fmt.Errorf("create approval for %s: %w", req.Bundle, err)
	}
	return ApprovalRecorded, a, nil
}
