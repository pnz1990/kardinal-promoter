// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package imageverification_test

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	iv "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification"
)

// scanAt is trivyScan finished at t.
func scanAt(t time.Time, vulns ...[2]string) map[string]any {
	s := trivyScan(vulns...)
	s["metadata"] = map[string]any{"scanStartedOn": t.Add(-time.Minute).Format(time.RFC3339), "scanFinishedOn": t.Format(time.RFC3339)}
	return s
}

// TestAttestations_MaxAge (#2044): a scan's age counts from its scannedAt;
// a Verified ImageVerification stays Verified and its result goes stale at
// staleAt; the stale result is looked for again, at most every
// refetchInterval, and a newer scan replaces it.
func TestAttestations_MaxAge(t *testing.T) {
	k := newKeys(t)
	secrets := k.secrets(t)
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	policy := v1alpha1.AttestationPolicy{Name: "vulns", PredicateType: iv.PredicateCosignVuln, Authorities: []string{"release"}, MaxAge: "24h"}
	h := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
	old := attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0, [2]string{"CVE-2026-1", "HIGH"}))
	h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{old}}

	h.now = t0.Add(time.Hour)
	got, res := h.reconcile()
	require.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
	r := resultOf(t, got, "vulns")
	require.NotNil(t, r.StaleAt)
	assert.Equal(t, t0.Add(24*time.Hour), r.StaleAt.UTC())
	assert.False(t, r.Stale)
	assert.InDelta(t, (23 * time.Hour).Seconds(), res.RequeueAfter.Seconds(), 1, "back when the scan goes stale")

	// Before staleAt: nothing changes, nothing is read.
	calls := h.reg.calls
	h.now = t0.Add(12 * time.Hour)
	again, _ := h.reconcile()
	assert.Equal(t, got.Status, again.Status)
	assert.Equal(t, calls, h.reg.calls)

	// Past staleAt: stale, still Verified, the referrers read once more.
	h.now = t0.Add(25 * time.Hour)
	got, res = h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase)
	r = resultOf(t, got, "vulns")
	assert.True(t, r.Stale)
	assert.Contains(t, r.Message, "no newer attestation verifies yet")
	assert.Equal(t, calls+1, h.reg.calls)
	assert.Equal(t, time.Hour, res.RequeueAfter, "a 24h maxAge is looked for again hourly")

	// Within the interval: not read again.
	h.now = t0.Add(25*time.Hour + 10*time.Minute)
	h.reconcile()
	assert.Equal(t, calls+1, h.reg.calls)

	// A newer scan arrives: it replaces the stale result.
	fresh := attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0.Add(25*time.Hour)))
	h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{old, fresh}}
	h.now = t0.Add(26*time.Hour + time.Minute)
	got, _ = h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase)
	r = resultOf(t, got, "vulns")
	assert.False(t, r.Stale, r.Message)
	assert.Equal(t, t0.Add(49*time.Hour), r.StaleAt.UTC())
	require.NotNil(t, r.Vulns)
	assert.Equal(t, 0, r.Vulns.Effective.High, "the newer scan's findings")
}

// TestAttestations_MaxAgeRequired (#2044): a required attestation that is
// already older than maxAge is waited for like a missing one.
func TestAttestations_MaxAgeRequired(t *testing.T) {
	k := newKeys(t)
	secrets := k.secrets(t)
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	policy := v1alpha1.AttestationPolicy{Name: "vulns", PredicateType: iv.PredicateCosignVuln, Authorities: []string{"release"},
		MaxAge: "24h", Required: true}
	h := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
	h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)},
		Attestations: [][]byte{attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0))}}
	h.now = t0.Add(30 * time.Hour)
	got, _ := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationPending, got.Status.Phase)
	assert.Equal(t, iv.ReasonAttestationStale, got.Status.Reason)
	assert.Contains(t, got.Status.Message, "older than maxAge 24h")

	h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)},
		Attestations: [][]byte{attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0.Add(29*time.Hour)))}}
	got, _ = h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
	assert.False(t, resultOf(t, got, "vulns").Stale)
}

// TestAttestations_MaxAgeWithoutATime: an attestation with no verified time
// and no scan time (a key signature without a log entry) cannot be dated:
// with a maxAge it is stale at once.
func TestAttestations_MaxAgeWithoutATime(t *testing.T) {
	k := newKeys(t)
	secrets := k.secrets(t)
	policy := v1alpha1.AttestationPolicy{Name: "tests", PredicateType: "https://in-toto.io/attestation/test-result/v0.1",
		Authorities: []string{"release"}, MaxAge: "1h"}
	h := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
	h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{
		attest(t, k.release, "https://in-toto.io/attestation/test-result/v0.1", map[string]any{"result": "PASSED"})}}
	got, _ := h.reconcile()
	assert.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, "not required: a gate decides on stale")
	r := resultOf(t, got, "tests")
	assert.True(t, r.Stale)
	assert.Nil(t, r.VerifiedAt)
}

// TestAttestations_NoMaxAge: without maxAge a Verified ImageVerification is
// terminal as before: no requeue, no staleAt.
func TestAttestations_NoMaxAge(t *testing.T) {
	k := newKeys(t)
	secrets := k.secrets(t)
	policy := v1alpha1.AttestationPolicy{Name: "vulns", PredicateType: iv.PredicateCosignVuln, Authorities: []string{"release"}}
	h := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
	h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)},
		Attestations: [][]byte{attest(t, k.release, iv.PredicateCosignVuln, scanAt(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))}}
	got, res := h.reconcile()
	require.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
	assert.Zero(t, res.RequeueAfter)
	r := resultOf(t, got, "vulns")
	assert.Nil(t, r.StaleAt)
	assert.False(t, r.Stale)
	calls := h.reg.calls
	h.now = h.now.Add(1000 * time.Hour)
	h.reconcile()
	assert.Equal(t, calls, h.reg.calls)
}

// TestAttestations_MaxAgeRegistryLoad (#2044, the scale question): 500
// (KARDINAL_SCALE_FULL=1; 20 otherwise)
// Verified ImageVerifications in flight with a 1h maxAge, each scan going
// stale and never replaced, reconciled exactly when the reconciler asks
// (RequeueAfter) over 24 simulated hours. Each reads the registry once to
// verify and then at most once per refetchInterval (15m for 1h) once
// stale: the registry sees at most 4 requests per hour per stale version,
// and nothing before a result goes stale.
func TestAttestations_MaxAgeRegistryLoad(t *testing.T) {
	n := scaleN()
	k := newKeys(t)
	secrets := k.secrets(t)
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	policy := v1alpha1.AttestationPolicy{Name: "vulns", PredicateType: iv.PredicateCosignVuln, Authorities: []string{"release"}, MaxAge: "1h"}
	objs := []client.Object{secrets[0], secrets[1]}
	for i := range n {
		v := attestIV("", policy)
		v.Name = fmt.Sprintf("v%03d-verify", i)
		objs = append(objs, v)
	}
	h := newHarness(t, nil, objs...)
	h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)},
		Attestations: [][]byte{attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0))}}

	// An event queue of (when, name), served in time order.
	type event struct {
		at   time.Time
		name string
	}
	var queue []event
	for i := range n {
		queue = append(queue, event{t0.Add(time.Duration(i) * time.Second), fmt.Sprintf("v%03d-verify", i)})
	}
	end := t0.Add(24 * time.Hour)
	reconciles := 0
	for len(queue) > 0 {
		sort.Slice(queue, func(i, j int) bool { return queue[i].at.Before(queue[j].at) })
		ev := queue[0]
		queue = queue[1:]
		if ev.at.After(end) {
			continue
		}
		h.now = ev.at
		res, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: ev.name}})
		require.NoError(t, err)
		reconciles++
		if res.RequeueAfter > 0 {
			queue = append(queue, event{ev.at.Add(res.RequeueAfter), ev.name})
		}
	}
	perVersionPerHour := float64(h.reg.calls-2*n) / float64(n) / 23 // the first hour verifies
	t.Logf("%d ImageVerifications, maxAge 1h, 24h: %d registry requests (%d to verify), %.2f per stale version per hour, %d reconciles",
		n, h.reg.calls, 2*n, perVersionPerHour, reconciles)
	// Verifying reads twice (the signature, then the attestations); once
	// stale, one read when it goes stale and one per 15m until hour 24.
	assert.LessOrEqual(t, h.reg.calls, 2*n+n*(4*23+1), "at most one read per 15m once stale")
	assert.GreaterOrEqual(t, h.reg.calls, 2*n+n*4*22, "stale scans are looked for again")
}

// scaleN is the number of versions TestAttestations_MaxAgeRegistryLoad
// simulates: 20 in go test ./..., 500 with KARDINAL_SCALE_FULL=1.
func scaleN() int {
	if os.Getenv("KARDINAL_SCALE_FULL") == "1" {
		return 500
	}
	return 20
}

// TestAttestations_MaxAgeFutureScannedAt (QA-3 on #2121): a signed scan
// that claims a time more than futureSkew ahead (2099) cannot be dated: it
// is stale at once and never picked over a scan that can be, in each of
// QA-3's sequences, whatever it reports:
//   - A: at the first check, beside a fresh clean scan;
//   - B: recorded alone (stale), replaced by a fresh scan, and not brought
//     back when that one goes stale while the 2099 scan is still listed;
//   - C: a clean 2099 scan does not hide a fresh failing one.
func TestAttestations_MaxAgeFutureScannedAt(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	policy := v1alpha1.AttestationPolicy{Name: "vulns", PredicateType: iv.PredicateCosignVuln, Authorities: []string{"release"}, MaxAge: "1h"}
	critical := [2]string{"CVE-2026-1", "CRITICAL"}

	t.Run("A first check", func(t *testing.T) {
		for _, futureFirst := range []bool{true, false} {
			k := newKeys(t)
			secrets := k.secrets(t)
			x := attest(t, k.release, iv.PredicateCosignVuln, scanAt(future, critical))
			y := attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0.Add(-time.Minute)))
			order := [][]byte{y, x}
			if futureFirst {
				order = [][]byte{x, y}
			}
			h := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
			h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: order}
			h.now = t0
			got, _ := h.reconcile()
			require.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
			r := resultOf(t, got, "vulns")
			assert.False(t, r.Stale, "future first %v: %s", futureFirst, r.Message)
			assert.Equal(t, 0, r.Vulns.Effective.Critical, "future first %v: the fresh scan, not the 2099 one", futureFirst)
			assert.Len(t, r.FirstSeen, 2, "both scans recorded as seen")
		}
	})

	t.Run("A beside a stale scan", func(t *testing.T) {
		// Both are stale; the result is the old scan, which can be dated,
		// not the 2099 one.
		k := newKeys(t)
		secrets := k.secrets(t)
		x := attest(t, k.release, iv.PredicateCosignVuln, scanAt(future, critical))
		old := attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0.Add(-2*time.Hour)))
		h := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
		h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{old, x}}
		h.now = t0
		got, _ := h.reconcile()
		r := resultOf(t, got, "vulns")
		assert.True(t, r.Stale)
		assert.Equal(t, 0, r.Vulns.Effective.Critical, "the old scan's findings, not the 2099 scan's")
	})

	t.Run("B never brought back", func(t *testing.T) {
		k := newKeys(t)
		secrets := k.secrets(t)
		x := attest(t, k.release, iv.PredicateCosignVuln, scanAt(future, critical))
		h := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
		h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{x}}
		h.now = t0
		got, _ := h.reconcile()
		require.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
		r := resultOf(t, got, "vulns")
		assert.True(t, r.Stale, "B1: a 2099 scan cannot be dated: stale at once")
		xDigest := r.Digest

		y2 := attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0.Add(15*time.Minute)))
		h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{x, y2}}
		h.now = t0.Add(16 * time.Minute)
		got, _ = h.reconcile()
		r = resultOf(t, got, "vulns")
		assert.False(t, r.Stale, "B2: the fresh scan replaces it: %s", r.Message)
		assert.Equal(t, 0, r.Vulns.Effective.Critical)
		y2Digest := r.Digest
		require.NotEqual(t, xDigest, y2Digest)

		// Y2 goes stale at t0+75m; X is still listed and nothing newer.
		h.now = t0.Add(80 * time.Minute)
		h.reconcile()
		h.now = t0.Add(96 * time.Minute)
		got, _ = h.reconcile()
		r = resultOf(t, got, "vulns")
		assert.True(t, r.Stale, "B3: stale, waiting for a newer scan")
		assert.Equal(t, y2Digest, r.Digest, "B3: the 2099 scan is not brought back")
		assert.Equal(t, 0, r.Vulns.Effective.Critical)
	})

	t.Run("C a clean 2099 scan does not hide a failing one", func(t *testing.T) {
		k := newKeys(t)
		secrets := k.secrets(t)
		xClean := attest(t, k.release, iv.PredicateCosignVuln, scanAt(future))
		z := attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0.Add(-time.Minute), critical))
		h := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
		h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{z, xClean}}
		h.now = t0
		got, _ := h.reconcile()
		r := resultOf(t, got, "vulns")
		assert.False(t, r.Stale, r.Message)
		assert.Equal(t, 1, r.Vulns.Effective.Critical, "the fresh failing scan is what the gate reads")
	})
}

// TestAttestations_MaxAgeFirstSeenKept (QA-3 on #2121): every verified
// attestation's first sighting is kept in status, not only the recorded
// one's, so one seen before keeps the date it was first seen when the
// result is picked again; a new one is dated when it appears.
func TestAttestations_MaxAgeFirstSeenKept(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	k := newKeys(t)
	secrets := k.secrets(t)
	policy := v1alpha1.AttestationPolicy{Name: "vulns", PredicateType: iv.PredicateCosignVuln, Authorities: []string{"release"}, MaxAge: "1h"}
	a := attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0.Add(-2*time.Minute)))
	b := attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0.Add(-time.Minute)))
	h := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
	h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{a, b}}
	h.now = t0
	got, _ := h.reconcile()
	r := resultOf(t, got, "vulns")
	require.Len(t, r.FirstSeen, 2, "both verified scans are recorded as seen, the one not picked too")
	for _, s := range r.FirstSeen {
		assert.Equal(t, t0, s.At.UTC())
	}

	c := attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0.Add(69*time.Minute)))
	h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{a, b, c}}
	h.now = t0.Add(70 * time.Minute)
	got, _ = h.reconcile()
	r = resultOf(t, got, "vulns")
	assert.False(t, r.Stale, r.Message)
	require.Len(t, r.FirstSeen, 3)
	seen := map[string]time.Time{}
	for _, s := range r.FirstSeen {
		seen[s.Digest] = s.At.UTC()
	}
	assert.Equal(t, t0.Add(70*time.Minute), seen[r.Digest], "the new scan is dated when it appears")
	n := 0
	for d, at := range seen {
		if d != r.Digest {
			assert.Equal(t, t0, at, "a scan seen before keeps its first sighting")
			n++
		}
	}
	assert.Equal(t, 2, n)
}

// TestAttestations_MaxAgeReferrerOrder (live run on #2121): key-signed
// scans have no verified signing time, and a registry lists referrers in no
// defined order. Whichever place the fresh scan has, it replaces the stale
// one at the re-fetch, and it is the one picked at first verification.
func TestAttestations_MaxAgeReferrerOrder(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	policy := v1alpha1.AttestationPolicy{Name: "vulns", PredicateType: iv.PredicateCosignVuln, Authorities: []string{"release"}, MaxAge: "5m"}
	for _, freshFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("fresh listed first %v", freshFirst), func(t *testing.T) {
			k := newKeys(t)
			secrets := k.secrets(t)
			order := func(old, fresh []byte) [][]byte {
				if freshFirst {
					return [][]byte{fresh, old}
				}
				return [][]byte{old, fresh}
			}
			old := attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0.Add(-10*time.Minute), [2]string{"CVE-2026-1", "CRITICAL"}))

			// The re-fetch: the stale scan is recorded, then a fresh one is listed.
			h := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
			h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: [][]byte{old}}
			h.now = t0
			got, _ := h.reconcile()
			require.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
			require.True(t, resultOf(t, got, "vulns").Stale)
			fresh := attest(t, k.release, iv.PredicateCosignVuln, scanAt(t0.Add(time.Minute)))
			h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: order(old, fresh)}
			h.now = t0.Add(5*time.Minute + 30*time.Second)
			got, _ = h.reconcile()
			r := resultOf(t, got, "vulns")
			assert.False(t, r.Stale, r.Message)
			require.NotNil(t, r.Vulns)
			assert.Equal(t, 0, r.Vulns.Effective.Critical, "the fresh scan's findings")

			// First verification with both listed: the fresh one is picked.
			h2 := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
			h2.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)}, Attestations: order(old, fresh)}
			h2.now = t0.Add(2 * time.Minute)
			got, _ = h2.reconcile()
			require.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
			r = resultOf(t, got, "vulns")
			assert.False(t, r.Stale, r.Message)
			assert.Equal(t, 2, r.Count)
			assert.Equal(t, 0, r.Vulns.Effective.Critical)
		})
	}
}

// TestAttestations_MaxAgeStatementTime (QA-3 on #2121): with a maxAge, a
// key-signed SLSA provenance counts from its signed finishedOn
// (timeSource statement): an old build is stale, a fresh one is not. A
// key-signed attestation whose statement gives no time cannot be dated and
// is stale at once (timeSource empty), whatever its referrers carry.
func TestAttestations_MaxAgeStatementTime(t *testing.T) {
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	provenanceAt := func(finished time.Time) map[string]any {
		p := provenance("https://github.com/actions/runner")
		p["runDetails"].(map[string]any)["metadata"] = map[string]any{"finishedOn": finished.Format(time.RFC3339)}
		return p
	}
	for _, tc := range []struct {
		name          string
		predicateType string
		predicate     map[string]any
		stale         bool
		source        string
	}{
		{"an old build", iv.PredicateSLSAProvenance, provenanceAt(t0.Add(-3 * time.Hour)), true, "statement"},
		{"a fresh build", iv.PredicateSLSAProvenance, provenanceAt(t0.Add(-10 * time.Minute)), false, "statement"},
		{"provenance without finishedOn", iv.PredicateSLSAProvenance, provenance("https://github.com/actions/runner"), true, ""},
		{"a custom predicate", "https://example.com/custom/v1", map[string]any{"ok": true}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := newKeys(t)
			secrets := k.secrets(t)
			policy := v1alpha1.AttestationPolicy{Name: "att", PredicateType: tc.predicateType, Authorities: []string{"release"}, MaxAge: "1h"}
			h := newHarness(t, nil, attestIV("", policy), secrets[0], secrets[1])
			h.reg.sigs[digestA] = iv.Signatures{Bundles: [][]byte{sign(t, k.release)},
				Attestations: [][]byte{attest(t, k.release, tc.predicateType, tc.predicate)}}
			h.now = t0
			got, _ := h.reconcile()
			require.Equal(t, v1alpha1.ImageVerificationVerified, got.Status.Phase, got.Status.Message)
			r := resultOf(t, got, "att")
			require.True(t, r.Verified, r.Message)
			assert.Equal(t, tc.stale, r.Stale)
			assert.Equal(t, tc.source, r.TimeSource)
			if tc.source == "statement" {
				require.NotNil(t, r.StatementTime)
			}
		})
	}
}
