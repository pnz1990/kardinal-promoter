// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
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

// ageBase is the time res's age counts from: the earlier of its scan's
// scannedAt and its verified signing time, so a scan that claims a later
// scannedAt than it was signed at does not stay fresh longer. Zero when it
// has neither.
func ageBase(res v1alpha1.AttestationResult) time.Time {
	var base time.Time
	if res.VerifiedAt != nil {
		base = res.VerifiedAt.Time
	}
	if res.Vulns != nil && res.Vulns.ScannedAt != "" {
		if t, err := time.Parse(time.RFC3339, res.Vulns.ScannedAt); err == nil && (base.IsZero() || t.Before(base)) {
			base = t
		}
	}
	return base
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
	if base := ageBase(*res); !base.IsZero() {
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
			setAge(res, d, now) // verified before maxAge was read: date it now
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
			fresh := evaluateAttestation(img, p, sigs.Attestations, auths)
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

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
