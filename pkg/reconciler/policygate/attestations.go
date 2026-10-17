// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"context"
	"fmt"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
// build level, the summed vulnerability counts, the oldest scan time, the
// signer, source and predicate only when every image agrees on them (left
// out otherwise, and when omitted, so an expression that reads it fails and
// the gate blocks), and each image's own result under images. A version
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
// one per image. The aggregates are safe for a gate whatever the number of
// images: verified when every image is, the lowest SLSA build level, the
// summed counts, the oldest scan. A field that identifies one image's
// attestation (signer, authority, digest, predicate, the SLSA builder,
// source and level source, the scanner) is set only when every image has
// the same non-empty value; otherwise it is left out, so an expression that
// reads it fails and the gate blocks, instead of deciding on the first
// image only. images lists each image's own value for expressions that
// check them one by one (attestations.<name>.images.all(i, ...)).
func foldAttestation(results []kardinalv1alpha1.AttestationResult) map[string]interface{} {
	verified, stale := true, false
	var staleAt *metav1.Time
	images := make([]interface{}, 0, len(results))
	for i := range results {
		verified = verified && results[i].Verified
		stale = stale || results[i].Stale
		if at := results[i].StaleAt; at != nil && (staleAt == nil || at.Before(staleAt)) {
			staleAt = at
		}
		images = append(images, imageAttestation(&results[i]))
	}
	v := map[string]interface{}{
		"verified":      verified,
		"stale":         stale,
		"predicateType": results[0].PredicateType,
		"images":        images,
	}
	if staleAt != nil {
		v["staleAt"] = staleAt.UTC().Format(time.RFC3339)
	}
	same(v, "signer", results, func(r *kardinalv1alpha1.AttestationResult) string { return r.Signer })
	same(v, "authority", results, func(r *kardinalv1alpha1.AttestationResult) string { return r.Authority })
	same(v, "digest", results, func(r *kardinalv1alpha1.AttestationResult) string { return r.Digest })
	same(v, "predicate", results, func(r *kardinalv1alpha1.AttestationResult) string { return r.Predicate })
	if slices.ContainsFunc(results, func(r kardinalv1alpha1.AttestationResult) bool { return r.SLSA != nil }) {
		level := int64(-1)
		for _, r := range results {
			if r.SLSA != nil && (level < 0 || int64(r.SLSA.BuildLevel) < level) {
				level = int64(r.SLSA.BuildLevel)
			}
		}
		for _, r := range results {
			if r.SLSA == nil {
				level = 0
			} else if l := int64(r.SLSA.BuildLevel); l < level {
				level = l
			}
		}
		slsa := map[string]interface{}{"buildLevel": level}
		slsaField := func(f func(*kardinalv1alpha1.SLSAResult) string) func(*kardinalv1alpha1.AttestationResult) string {
			return func(r *kardinalv1alpha1.AttestationResult) string {
				if r.SLSA == nil {
					return ""
				}
				return f(r.SLSA)
			}
		}
		same(slsa, "levelSource", results, slsaField(func(s *kardinalv1alpha1.SLSAResult) string { return s.LevelSource }))
		same(slsa, "builderID", results, slsaField(func(s *kardinalv1alpha1.SLSAResult) string { return s.BuilderID }))
		same(slsa, "sourceCommit", results, slsaField(func(s *kardinalv1alpha1.SLSAResult) string { return s.SourceCommit }))
		same(slsa, "sourceURI", results, slsaField(func(s *kardinalv1alpha1.SLSAResult) string { return s.SourceURI }))
		v["slsa"] = slsa
	}
	if slices.ContainsFunc(results, func(r kardinalv1alpha1.AttestationResult) bool { return r.Vulns != nil }) {
		var before, effective kardinalv1alpha1.SeverityCounts
		ids := []interface{}{}
		seen := map[string]bool{}
		scannedAt := ""
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
			"scannedAt":     scannedAt,
			"before":        counts(before),
			"effective":     counts(effective),
			"effectiveIDs":  ids,
			"vexStatements": statements,
		}
		vulnField := func(f func(*kardinalv1alpha1.VulnsResult) string) func(*kardinalv1alpha1.AttestationResult) string {
			return func(r *kardinalv1alpha1.AttestationResult) string {
				if r.Vulns == nil {
					return ""
				}
				return f(r.Vulns)
			}
		}
		same(vulns, "scanner", results, vulnField(func(s *kardinalv1alpha1.VulnsResult) string { return s.Scanner }))
		same(vulns, "scannerVersion", results, vulnField(func(s *kardinalv1alpha1.VulnsResult) string { return s.ScannerVersion }))
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

// same sets m[key] to the value every result has for field, when they all
// have the same non-empty one, and leaves it out otherwise.
func same(m map[string]interface{}, key string, results []kardinalv1alpha1.AttestationResult,
	field func(*kardinalv1alpha1.AttestationResult) string) {
	want := field(&results[0])
	if want == "" {
		return
	}
	for i := range results[1:] {
		if field(&results[i+1]) != want {
			return
		}
	}
	m[key] = want
}

// imageAttestation is one image's result, an element of
// attestations.<name>.images. Fields the result does not have are left
// out, as for the folded value.
func imageAttestation(r *kardinalv1alpha1.AttestationResult) map[string]interface{} {
	v := map[string]interface{}{"image": r.Image, "verified": r.Verified, "stale": r.Stale}
	if r.StaleAt != nil {
		v["staleAt"] = r.StaleAt.UTC().Format(time.RFC3339)
	}
	for k, val := range map[string]string{"signer": r.Signer, "authority": r.Authority, "digest": r.Digest, "predicate": r.Predicate} {
		if val != "" {
			v[k] = val
		}
	}
	if r.SLSA != nil {
		slsa := map[string]interface{}{"buildLevel": int64(r.SLSA.BuildLevel)}
		for k, val := range map[string]string{"levelSource": r.SLSA.LevelSource, "builderID": r.SLSA.BuilderID,
			"sourceCommit": r.SLSA.SourceCommit, "sourceURI": r.SLSA.SourceURI} {
			if val != "" {
				slsa[k] = val
			}
		}
		v["slsa"] = slsa
	}
	if r.Vulns != nil {
		ids := make([]interface{}, 0, len(r.Vulns.EffectiveIDs))
		for _, id := range r.Vulns.EffectiveIDs {
			if len(ids) < maxContextIDs {
				ids = append(ids, id)
			}
		}
		vulns := map[string]interface{}{"before": counts(r.Vulns.Before), "effective": counts(r.Vulns.Effective),
			"effectiveIDs": ids, "vexStatements": int64(r.Vulns.VEXStatements)}
		for k, val := range map[string]string{"scanner": r.Vulns.Scanner, "scannerVersion": r.Vulns.ScannerVersion,
			"scannedAt": r.Vulns.ScannedAt} {
			if val != "" {
				vulns[k] = val
			}
		}
		v["vulns"] = vulns
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
