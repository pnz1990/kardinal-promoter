// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Attestation age (attestations[].maxAge, #2044): an ImageVerification is
// terminal once Verified, so a scan it verified would be what every gate of
// the version reads for as long as the version is in flight, however old.
// With maxAge the reconciler writes when each result goes stale (staleAt,
// from the attestation's own time), flips stale when that time passes, and
// looks for a newer attestation in the image's referrers until one
// replaces it. The phase stays Verified: gates decide on
// attestations.<name>.stale. The clock is read only to write this status.

const (
	// ReasonAttestationStale: a required attestation is older than its
	// policy's maxAge.
	ReasonAttestationStale = "AttestationStale"

	// minRefetch and maxRefetch bound how often a stale attestation's
	// referrers are read again: a quarter of its maxAge, within them.
	minRefetch = 5 * time.Minute
	maxRefetch = time.Hour

	// futureSkew is how far ahead of the reconciler's clock a signed time
	// may be: beyond it the attestation cannot be dated (QA-3 on #2121).
	futureSkew = 5 * time.Minute
	// maxSightings bounds AttestationResult.FirstSeen.
	maxSightings = 16

	// The time sources of an attestation's age (AttestationResult.TimeSource).
	timeStatement   = "statement"
	timeCertificate = "certificate"
	timeLog         = "log"
	// datedByLogTime is AttestationPolicy.DatedBy's opt-in to log time.
	datedByLogTime = "logTime"
	// maxCertLifetime is the longest a signing certificate may live for its
	// verified time to date an attestation (Fulcio's live ten minutes): a
	// longer-lived one, from a private CA, bounds nothing useful, and is
	// dated like a key signature.
	maxCertLifetime = time.Hour
)

// policyMaxAge is p's maxAge, 0 when it has none. The CRD's pattern admits
// only Go durations; a zero or negative one is no maxAge.
func policyMaxAge(p v1alpha1.AttestationPolicy) time.Duration {
	if p.MaxAge == "" {
		return 0
	}
	d, err := time.ParseDuration(p.MaxAge)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// statementTimeOf is the time a signed statement of predicateType gives
// itself: a scan's scannedAt, a SLSA provenance's
// runDetails.metadata.finishedOn (v1) or metadata.buildFinishedOn (v0.2;
// with both present, whatever the version, the earlier), a SLSA
// verification summary's timeVerified.
func statementTimeOf(predicateType string, predicate []byte) (time.Time, bool) {
	var raws []string
	switch {
	case predicateType == PredicateCosignVuln || predicateType == PredicateInTotoVulns:
		if _, meta, err := parseScan(predicateType, predicate); err == nil {
			raws = []string{meta.scannedAt}
		}
	case strings.HasPrefix(predicateType, "https://slsa.dev/provenance/"):
		var prov struct {
			RunDetails struct {
				Metadata struct {
					FinishedOn string `json:"finishedOn"`
				} `json:"metadata"`
			} `json:"runDetails"`
			Metadata struct {
				BuildFinishedOn string `json:"buildFinishedOn"`
			} `json:"metadata"`
		}
		if json.Unmarshal(predicate, &prov) == nil {
			raws = []string{prov.RunDetails.Metadata.FinishedOn, prov.Metadata.BuildFinishedOn}
		}
	case predicateType == PredicateSLSAVSA:
		var vsa struct {
			TimeVerified string `json:"timeVerified"`
		}
		if json.Unmarshal(predicate, &vsa) == nil {
			raws = []string{vsa.TimeVerified}
		}
	}
	var out time.Time
	for _, raw := range raws {
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			continue
		}
		if out.IsZero() || t.Before(out) {
			out = t
		}
	}
	return out, !out.IsZero()
}

// shortLived reports whether b's signing certificate lives at most
// maxCertLifetime.
func shortLived(b *bundle.Bundle) bool {
	vc, err := b.VerificationContent()
	if err != nil {
		return false
	}
	c := vc.Certificate()
	return c != nil && c.NotAfter.Sub(c.NotBefore) <= maxCertLifetime
}

// dating is what vs's age counts from under p: the time its signed
// statement gives; else, for a keyless signature whose certificate lives
// at most maxCertLifetime, its verified time (the certificate was valid
// then, so it cannot be stamped again much later); else, with datedBy
// logTime, its
// verified log or timestamp time; else nothing: it cannot be dated, since
// anyone can log an old key signature again or stamp it anew.
func dating(vs verifiedStatement, p v1alpha1.AttestationPolicy) (*metav1.Time, string) {
	if t, ok := statementTimeOf(p.PredicateType, vs.predicate); ok {
		m := metav1.NewTime(t.UTC())
		return &m, timeStatement
	}
	switch {
	case vs.at.IsZero():
		return nil, ""
	case vs.shortCert:
		return nil, timeCertificate
	case p.DatedBy == datedByLogTime:
		return nil, timeLog
	}
	return nil, ""
}

// ageBase is the time res's age counts from at now, by its TimeSource: its
// statement's time, or its verified time; with both, the earlier, so a
// statement that claims a later time than it was signed at does not stay
// fresh longer; never later than datedAt, when kardinal first saw it. Zero
// when it cannot be dated: no time source, or a time more than futureSkew
// ahead of now (a scanner with a wrong clock, or a forged date), so such an
// attestation is stale at once and never picked over a dated one.
func ageBase(res v1alpha1.AttestationResult, now time.Time) time.Time {
	var base time.Time
	limit := now.Add(futureSkew)
	switch res.TimeSource {
	case timeStatement:
		if res.StatementTime == nil {
			return time.Time{}
		}
		base = res.StatementTime.Time
	case timeCertificate, timeLog:
		if res.VerifiedAt == nil {
			return time.Time{}
		}
		base = res.VerifiedAt.Time
	default:
		return time.Time{}
	}
	if base.After(limit) {
		return time.Time{}
	}
	if res.VerifiedAt != nil {
		if res.VerifiedAt.After(limit) {
			return time.Time{}
		}
		if res.VerifiedAt.Time.Before(base) {
			base = res.VerifiedAt.Time
		}
	}
	if !base.IsZero() && res.DatedAt != nil && res.DatedAt.Before(&metav1.Time{Time: base}) {
		base = res.DatedAt.Time
	}
	return base
}

// dateAttestation sets res.DatedAt: kept from prev when it records the same
// attestation (digest), else when res.FirstSeen (or prev's) first saw that
// digest, else now.
func dateAttestation(res *v1alpha1.AttestationResult, prev *v1alpha1.AttestationResult, now time.Time) {
	if !res.Verified {
		res.DatedAt = nil
		return
	}
	if prev != nil && prev.DatedAt != nil && prev.Verified && prev.Digest != "" && prev.Digest == res.Digest {
		res.DatedAt = prev.DatedAt
		return
	}
	for _, seen := range [][]v1alpha1.AttestationSighting{res.FirstSeen, firstSeenOf(prev)} {
		if at, ok := sightingOf(seen, res.Digest); ok {
			res.DatedAt = &at
			return
		}
	}
	t := metav1.NewTime(now.UTC().Truncate(time.Second))
	res.DatedAt = &t
}

func firstSeenOf(res *v1alpha1.AttestationResult) []v1alpha1.AttestationSighting {
	if res == nil {
		return nil
	}
	return res.FirstSeen
}

// sightingOf is when seen first saw digest.
func sightingOf(seen []v1alpha1.AttestationSighting, digest string) (metav1.Time, bool) {
	if digest == "" {
		return metav1.Time{}, false
	}
	for _, s := range seen {
		if s.Digest == digest {
			return s.At, true
		}
	}
	return metav1.Time{}, false
}

// sightings is prev's FirstSeen with every digest of found it lacks, seen
// now, the oldest dropped beyond maxSightings.
func sightings(prev []v1alpha1.AttestationSighting, found []verifiedStatement, now time.Time) []v1alpha1.AttestationSighting {
	out := append([]v1alpha1.AttestationSighting(nil), prev...)
	at := metav1.NewTime(now.UTC().Truncate(time.Second))
	for _, vs := range found {
		if _, ok := sightingOf(out, vs.payloadDigest); !ok && vs.payloadDigest != "" {
			out = append(out, v1alpha1.AttestationSighting{Digest: vs.payloadDigest, At: at})
		}
	}
	if len(out) > maxSightings {
		sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(&out[j].At) })
		out = out[len(out)-maxSightings:]
	}
	return out
}

// setAge sets res.StaleAt and res.Stale for a maxAge of d at now. A result
// that did not verify, or a policy without maxAge, has neither. A verified
// result with no time to count from is stale at once: its age cannot be
// judged.
func setAge(res *v1alpha1.AttestationResult, d time.Duration, now time.Time) {
	if d == 0 || !res.Verified {
		res.StaleAt, res.Stale = nil, false
		return
	}
	at := now
	if base := ageBase(*res, now); !base.IsZero() {
		at = base.Add(d)
	}
	t := metav1.NewTime(at.UTC().Truncate(time.Second))
	res.StaleAt = &t
	res.Stale = !now.Before(t.Time)
}

// ageStale reports whether res, as recorded, is past p's maxAge at now.
func ageStale(res v1alpha1.AttestationResult, p v1alpha1.AttestationPolicy, now time.Time) bool {
	if policyMaxAge(p) == 0 || !res.Verified {
		return false
	}
	return res.Stale || (res.StaleAt != nil && !now.Before(res.StaleAt.Time))
}

// hasMaxAge reports whether one of iv's attestation policies has a maxAge.
func hasMaxAge(iv *v1alpha1.ImageVerification) bool {
	for _, p := range iv.Spec.Policy.Attestations {
		if policyMaxAge(p) > 0 {
			return true
		}
	}
	return false
}

// refetchInterval is how often a stale attestation of maxAge d is looked
// for again: d/4, between minRefetch and maxRefetch.
func refetchInterval(d time.Duration) time.Duration {
	return min(max(d/4, minRefetch), maxRefetch)
}

// nextAgeCheck is when iv's results need their age checked again after now:
// the earliest staleAt still ahead, or, with a stale result, the next
// re-fetch. Zero when none.
func nextAgeCheck(iv *v1alpha1.ImageVerification, now time.Time) time.Duration {
	policies := attestationPolicies(iv)
	var next time.Duration
	consider := func(d time.Duration) {
		if d > 0 && (next == 0 || d < next) {
			next = d
		}
	}
	for _, res := range iv.Status.Attestations {
		d := policyMaxAge(policies[res.Name])
		switch {
		case d == 0 || !res.Verified || res.StaleAt == nil:
		case res.Stale:
			last := now
			if iv.Status.LastCheckedAt != nil {
				last = iv.Status.LastCheckedAt.Time
			}
			consider(max(last.Add(refetchInterval(d)).Sub(now), time.Second))
		default:
			consider(max(res.StaleAt.Sub(now), time.Second))
		}
	}
	return next
}

func attestationPolicies(iv *v1alpha1.ImageVerification) map[string]v1alpha1.AttestationPolicy {
	out := make(map[string]v1alpha1.AttestationPolicy, len(iv.Spec.Policy.Attestations))
	for _, p := range iv.Spec.Policy.Attestations {
		out[p.Name] = p
	}
	return out
}

// refreshAges keeps a Verified ImageVerification's attestation ages
// current: results whose staleAt passed become stale, and the stale ones
// are looked for again (every refetchInterval) in their image's referrers;
// a newer attestation that verifies and is not stale replaces the result.
// The phase stays Verified.
func (r *Reconciler) refreshAges(ctx context.Context, log zerolog.Logger, iv *v1alpha1.ImageVerification) (ctrl.Result, error) {
	base := iv.DeepCopy()
	now := r.now()
	policies := attestationPolicies(iv)
	staleByImage := map[string][]int{}
	shortest := time.Duration(0)
	for i := range iv.Status.Attestations {
		res := &iv.Status.Attestations[i]
		d := policyMaxAge(policies[res.Name])
		if d == 0 || !res.Verified {
			continue
		}
		if res.StaleAt == nil {
			// Verified before maxAge was read: date it now.
			dateAttestation(res, nil, now)
			setAge(res, d, now)
		}
		if !res.Stale && !now.Before(res.StaleAt.Time) {
			res.Stale = true
			log.Info().Str("attestation", res.Name).Str("image", res.Image).Msg("attestation is older than its maxAge")
		}
		if res.Stale {
			staleByImage[res.Image] = append(staleByImage[res.Image], i)
			if shortest == 0 || d < shortest {
				shortest = d
			}
		}
	}
	due := len(staleByImage) > 0 &&
		(iv.Status.LastCheckedAt == nil || !now.Before(iv.Status.LastCheckedAt.Add(refetchInterval(shortest))))
	if due {
		r.refetchStale(ctx, log, iv, staleByImage, policies, now)
		t := metav1.NewTime(now)
		iv.Status.LastCheckedAt = &t
		boundPredicates(iv.Status.Attestations)
	}
	if !equality.Semantic.DeepEqual(base.Status, iv.Status) {
		if err := r.patch(ctx, base, iv); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: nextAgeCheck(iv, now)}, nil
}

// refetchStale reads the referrers of each image with stale results and
// replaces a result with the newest attestation that verifies and is not
// stale. A registry or authority error keeps the stale result, with the
// reason in its message, until the next re-fetch.
func (r *Reconciler) refetchStale(ctx context.Context, log zerolog.Logger, iv *v1alpha1.ImageVerification,
	staleByImage map[string][]int, policies map[string]v1alpha1.AttestationPolicy, now time.Time) {
	note := func(idx []int, msg string) {
		for _, i := range idx {
			iv.Status.Attestations[i].Message = clip(msg)
		}
	}
	auths, err := r.authorities(ctx, iv)
	var agents []v1alpha1.AgentIdentity
	if err == nil {
		// A newer attestation's signer maps to an agent as at first
		// verification.
		agents, err = r.signingAgents(ctx)
	}
	opts, optErr := r.registryOptions(ctx, iv)
	for _, img := range iv.Spec.Images {
		ref := img.Repository + "@" + img.Digest
		idx := staleByImage[ref]
		if len(idx) == 0 {
			continue
		}
		if err != nil || optErr != nil {
			note(idx, fmt.Sprintf("stale; looking for a newer attestation: %v", firstErr(err, optErr)))
			continue
		}
		o := opts
		o.AttestationTypes = attestationTypes(iv.Spec.Policy.Attestations)
		fetchCtx, cancel := context.WithTimeout(ctx, durationOr(r.RegistryTimeout, DefaultRegistryTimeout))
		sigs, ferr := r.Registry.Signatures(fetchCtx, img.Repository, img.Digest, o)
		cancel()
		if ferr != nil {
			log.Warn().Err(ferr).Str("image", ref).Msg("attestation age: registry error; retrying")
			note(idx, "stale; looking for a newer attestation: registry: "+ferr.Error())
			continue
		}
		for _, i := range idx {
			old := iv.Status.Attestations[i]
			p := policies[old.Name]
			pick := newAgePick(p, &old, now)
			fresh := pick.result(evaluateAttestation(img, p, sigs.Attestations, auths, agents, pick.chooser()))
			dateAttestation(&fresh, &old, now)
			setAge(&fresh, policyMaxAge(p), now)
			if fresh.Verified && !fresh.Stale {
				log.Info().Str("attestation", old.Name).Str("image", ref).Msg("a newer attestation replaces a stale one")
				iv.Status.Attestations[i] = fresh
				continue
			}
			iv.Status.Attestations[i].Message = clip(fmt.Sprintf("older than maxAge %s since %s; no newer attestation verifies yet",
				p.MaxAge, old.StaleAt.UTC().Format(time.RFC3339)))
		}
	}
}

// maxAgeCandidates bounds how many verified attestations of one policy
// agePick dates, newest first.
const maxAgeCandidates = 32

// agePick picks, for a policy with a maxAge, the attestation that stays
// fresh longest at now, and records when each verified one was first seen.
type agePick struct {
	p    v1alpha1.AttestationPolicy
	prev *v1alpha1.AttestationResult
	now  time.Time
	// seen is the FirstSeen the pick dated with, for the result.
	seen []v1alpha1.AttestationSighting
}

// newAgePick is the pick for p after prev, nil for a policy without a
// maxAge (the newest is used).
func newAgePick(p v1alpha1.AttestationPolicy, prev *v1alpha1.AttestationResult, now time.Time) *agePick {
	if policyMaxAge(p) == 0 {
		return nil
	}
	return &agePick{p: p, prev: prev, now: now}
}

// choose is the index of found to use: of the attestations that can be
// dated, the one with the latest staleAt, each dated as setAge dates it
// (its first sighting: prev's datedAt for the same digest, FirstSeen for
// one seen before, now for a new one); on a tie, the newer. Only when none
// can be dated, the newest. Picking by age, not by place, matters when
// attestations have no verified signing time: a registry lists referrers
// in no defined order, so a stale scan could hide a fresh one; and a scan
// that claims a future time can never win.
func (a *agePick) choose(found []verifiedStatement) int {
	d := policyMaxAge(a.p)
	a.seen = sightings(firstSeenOf(a.prev), found, a.now)
	best, bestAt := len(found)-1, time.Time{}
	for i := len(found) - 1; i >= 0 && i >= len(found)-maxAgeCandidates; i-- {
		at, dated := a.staleAt(found[i], d)
		if dated && at.After(bestAt) {
			best, bestAt = i, at
		}
	}
	return best
}

// result is res with the sightings the pick recorded.
func (a *agePick) result(res v1alpha1.AttestationResult) v1alpha1.AttestationResult {
	if a != nil && res.Verified {
		res.FirstSeen = a.seen
	}
	return res
}

// staleAt is when vs would go stale under a maxAge of d, and whether it
// can be dated at all.
func (a *agePick) staleAt(vs verifiedStatement, d time.Duration) (time.Time, bool) {
	res := v1alpha1.AttestationResult{Verified: true, Digest: vs.payloadDigest, FirstSeen: a.seen}
	if !vs.at.IsZero() {
		t := metav1.NewTime(vs.at.UTC())
		res.VerifiedAt = &t
	}
	res.StatementTime, res.TimeSource = dating(vs, a.p)
	dateAttestation(&res, a.prev, a.now)
	setAge(&res, d, a.now)
	return res.StaleAt.Time, !ageBase(res, a.now).IsZero()
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// chooser is choose for evaluateAttestation, nil without a maxAge.
func (a *agePick) chooser() func([]verifiedStatement) int {
	if a == nil {
		return nil
	}
	return a.choose
}
