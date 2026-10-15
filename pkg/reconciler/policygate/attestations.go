// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// attestationsRoot is the CEL variable of the version's attestations
// (docs/design/20-attestations.md §6).
const attestationsRoot = "attestations"

// maxContextIDs bounds attestations.<name>.vulns.effectiveIDs.
const maxContextIDs = 50

// buildAttestationContext is attestations.*: the version's newest
// ImageVerification (label kardinal.io/bundle), with verified (it is
// Verified) and, per attestation policy name, the results of its images
// folded into one: verified when every image's verified, the lowest SLSA
// build level, the summed vulnerability counts, the oldest scan time, and
// the first image's signer, source and predicate (left out when omitted,
// so an expression that reads it fails and the gate blocks). A version
// with no ImageVerification has verified false and no names: an expression
// that reads a name fails closed.
func (r *Reconciler) buildAttestationContext(ctx context.Context, ns, bundle string) (map[string]interface{}, error) {
	var list kardinalv1alpha1.ImageVerificationList
	if err := r.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{labelBundle: bundle}); err != nil {
		return nil, fmt.Errorf("list imageverifications of bundle %s: %w", bundle, err)
	}
	out := map[string]interface{}{"verified": false}
	if len(list.Items) == 0 {
		return out, nil
	}
	// A policy edit gives a new ImageVerification (its name hashes the
	// spec): the newest decides.
	iv := slices.MaxFunc(list.Items, func(a, b kardinalv1alpha1.ImageVerification) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		if a.Name < b.Name {
			return -1
		}
		return 1
	})
	out["verified"] = iv.Status.Phase == kardinalv1alpha1.ImageVerificationVerified
	byName := map[string][]kardinalv1alpha1.AttestationResult{}
	var names []string
	for _, res := range iv.Status.Attestations {
		if _, ok := byName[res.Name]; !ok {
			names = append(names, res.Name)
		}
		byName[res.Name] = append(byName[res.Name], res)
	}
	for _, name := range names {
		out[name] = foldAttestation(byName[name])
	}
	return out, nil
}

// foldAttestation is one attestation name's CEL value from its results,
// one per image.
func foldAttestation(results []kardinalv1alpha1.AttestationResult) map[string]interface{} {
	first := results[0]
	verified := true
	for _, r := range results {
		verified = verified && r.Verified
	}
	v := map[string]interface{}{
		"verified":      verified,
		"signer":        first.Signer,
		"authority":     first.Authority,
		"predicateType": first.PredicateType,
		"digest":        first.Digest,
	}
	if first.Predicate != "" {
		v["predicate"] = first.Predicate
	}
	if first.SLSA != nil {
		level := int64(first.SLSA.BuildLevel)
		for _, r := range results {
			if r.SLSA == nil {
				level = 0
			} else if l := int64(r.SLSA.BuildLevel); l < level {
				level = l
			}
		}
		v["slsa"] = map[string]interface{}{
			"buildLevel":   level,
			"levelSource":  first.SLSA.LevelSource,
			"builderID":    first.SLSA.BuilderID,
			"sourceCommit": first.SLSA.SourceCommit,
			"sourceURI":    first.SLSA.SourceURI,
		}
	}
	if first.Vulns != nil {
		var before, effective kardinalv1alpha1.SeverityCounts
		ids := []interface{}{}
		seen := map[string]bool{}
		scannedAt := first.Vulns.ScannedAt
		statements := int64(0)
		for _, r := range results {
			if r.Vulns == nil {
				continue
			}
			sum(&before, r.Vulns.Before)
			sum(&effective, r.Vulns.Effective)
			for _, id := range r.Vulns.EffectiveIDs {
				if !seen[id] && len(ids) < maxContextIDs {
					seen[id] = true
					ids = append(ids, id)
				}
			}
			if r.Vulns.ScannedAt != "" && (scannedAt == "" || r.Vulns.ScannedAt < scannedAt) {
				scannedAt = r.Vulns.ScannedAt
			}
			statements += int64(r.Vulns.VEXStatements)
		}
		vulns := map[string]interface{}{
			"scanner":        first.Vulns.Scanner,
			"scannerVersion": first.Vulns.ScannerVersion,
			"scannedAt":      scannedAt,
			"before":         counts(before),
			"effective":      counts(effective),
			"effectiveIDs":   ids,
			"vexStatements":  statements,
		}
		v["vulns"] = vulns
		// The scan's fields are also on the attestation itself, so
		// attestations.vulns.effective.critical reads as the spec's
		// examples write it (§6) as well as .vulns.effective.critical.
		for k, val := range vulns {
			v[k] = val
		}
	}
	return v
}

func sum(dst *kardinalv1alpha1.SeverityCounts, c kardinalv1alpha1.SeverityCounts) {
	dst.Critical += c.Critical
	dst.High += c.High
	dst.Medium += c.Medium
	dst.Low += c.Low
	dst.Unknown += c.Unknown
}

func counts(c kardinalv1alpha1.SeverityCounts) map[string]interface{} {
	return map[string]interface{}{
		"critical": int64(c.Critical), "high": int64(c.High), "medium": int64(c.Medium),
		"low": int64(c.Low), "unknown": int64(c.Unknown),
	}
}

// imageVerificationRequests re-evaluates the gate instances of the
// ImageVerification's Bundle that read attestations.*.
func (r *Reconciler) imageVerificationRequests(ctx context.Context, obj client.Object) []reconcile.Request {
	bundle := obj.GetLabels()[labelBundle]
	if bundle == "" {
		return nil
	}
	return r.instanceGateRequests(ctx, "ImageVerification", attestationsRoot,
		client.InNamespace(obj.GetNamespace()), client.MatchingLabels{labelBundle: bundle})
}

// imageVerificationStatusChanged passes ImageVerification creates and
// deletes, and the updates that change its phase or attestation results,
// what attestations.* reads (not its retry bookkeeping).
var imageVerificationStatusChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		o, ok1 := e.ObjectOld.(*kardinalv1alpha1.ImageVerification)
		n, ok2 := e.ObjectNew.(*kardinalv1alpha1.ImageVerification)
		if !ok1 || !ok2 {
			return true
		}
		return o.Status.Phase != n.Status.Phase || !equality.Semantic.DeepEqual(o.Status.Attestations, n.Status.Attestations)
	},
}
