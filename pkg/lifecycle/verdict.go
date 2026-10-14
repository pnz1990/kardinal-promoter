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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// VerdictRequest is a verifier's verdict on a Bundle (kardinal verify).
type VerdictRequest struct {
	Namespace, Bundle, Environment, Check string
	// Result is pass, fail or inconclusive; ignored with Revoke.
	Result      string
	Summary     string
	EvidenceURL string
	Findings    []kardinalv1alpha1.VerdictFinding
	// By and Groups are the writer, as the API server authenticates them.
	By     string
	Groups []string
	// Actor is the writer's actor stamp, nil when it has none.
	Actor *kardinalv1alpha1.Actor
	// Revoke deletes the writer's latest Verdict for the Bundle,
	// environment and check (Name: that one) instead of recording one.
	Revoke bool
	Name   string
	// Now names the Verdict; zero means time.Now.
	Now time.Time
}

// VerdictName is the name of a Verdict by by on bundle in env for check,
// written at now: readable, unique per writer and time, at most 253
// characters.
func VerdictName(bundle, env, check, by string, now time.Time) string {
	sum := sha256.Sum256([]byte(by + "\x00" + now.UTC().Format(time.RFC3339Nano)))
	suffix := "-" + hex.EncodeToString(sum[:])[:10]
	base := strings.ToLower(bundle + "-" + env + "-" + check)
	if n := validation.DNS1123SubdomainMaxLength - len(suffix); len(base) > n {
		base = strings.TrimRight(base[:n], "-.")
	}
	return base + suffix
}

// RecordVerdict creates the Verdict of req, owned by its Bundle with the
// labels the Graph selects on, or with req.Revoke deletes the writer's
// latest one. It returns the Verdict written or deleted.
func RecordVerdict(ctx context.Context, c client.Client, req VerdictRequest) (*kardinalv1alpha1.Verdict, error) {
	var b kardinalv1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Bundle}, &b); err != nil {
		return nil, fmt.Errorf("get bundle %s: %w", req.Bundle, err)
	}
	if req.Revoke {
		return revokeVerdict(ctx, c, req)
	}
	switch req.Result {
	case kardinalv1alpha1.VerdictPass, kardinalv1alpha1.VerdictFail, kardinalv1alpha1.VerdictInconclusive:
	default:
		return nil, fmt.Errorf("the result must be pass, fail or inconclusive, not %q", req.Result)
	}
	if req.Result != kardinalv1alpha1.VerdictPass && strings.TrimSpace(req.Summary) == "" {
		return nil, fmt.Errorf("a %s verdict says why: give a summary", req.Result)
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	yes := true
	v := &kardinalv1alpha1.Verdict{
		ObjectMeta: metav1.ObjectMeta{
			Name:      VerdictName(b.Name, req.Environment, req.Check, req.By, now),
			Namespace: b.Namespace,
			Labels: map[string]string{"kardinal.io/bundle": b.Name, "kardinal.io/environment": req.Environment,
				"kardinal.io/check": req.Check, "kardinal.io/pipeline": b.Spec.Pipeline},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: kardinalv1alpha1.GroupVersion.String(), Kind: "Bundle",
				Name: b.Name, UID: b.UID, BlockOwnerDeletion: &yes}},
		},
		Spec: kardinalv1alpha1.VerdictSpec{Pipeline: b.Spec.Pipeline, Bundle: b.Name, BundleUID: string(b.UID),
			Environment: req.Environment, Check: req.Check, Result: req.Result, Summary: req.Summary,
			EvidenceURL: req.EvidenceURL, Findings: req.Findings, By: req.By, Groups: req.Groups, Actor: req.Actor},
	}
	if err := c.Create(ctx, v); err != nil {
		return nil, fmt.Errorf("create verdict: %w", err)
	}
	return v, nil
}

// VerdictsOf lists the Verdicts of bundle, for env and check when they are
// set, oldest first.
func VerdictsOf(ctx context.Context, c client.Reader, ns, bundle, env, check string) ([]kardinalv1alpha1.Verdict, error) {
	sel := client.MatchingLabels{"kardinal.io/bundle": bundle}
	if env != "" {
		sel["kardinal.io/environment"] = env
	}
	if check != "" {
		sel["kardinal.io/check"] = check
	}
	var list kardinalv1alpha1.VerdictList
	if err := c.List(ctx, &list, client.InNamespace(ns), sel); err != nil {
		return nil, fmt.Errorf("list verdicts: %w", err)
	}
	sort.SliceStable(list.Items, func(i, j int) bool {
		a, b := list.Items[i].CreationTimestamp, list.Items[j].CreationTimestamp
		if !a.Equal(&b) {
			return a.Before(&b)
		}
		return list.Items[i].Name < list.Items[j].Name
	})
	return list.Items, nil
}

func revokeVerdict(ctx context.Context, c client.Client, req VerdictRequest) (*kardinalv1alpha1.Verdict, error) {
	list, err := VerdictsOf(ctx, c, req.Namespace, req.Bundle, req.Environment, req.Check)
	if err != nil {
		return nil, err
	}
	var target *kardinalv1alpha1.Verdict
	for i := range list {
		v := &list[i]
		if v.Spec.By != req.By || (req.Name != "" && v.Name != req.Name) {
			continue
		}
		target = v // the latest of the caller's
	}
	if target == nil {
		return nil, fmt.Errorf("you (%s) have no verdict on %s for %s, check %s", req.By, req.Bundle, req.Environment, req.Check)
	}
	if err := c.Delete(ctx, target); err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("delete verdict %s: %w", target.Name, err)
	}
	return target, nil
}
