//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// burstPromotions is how many promotions TestReceiptsKeyless_Burst makes at
// once: KARDINAL_E2E_RECEIPT_BURST, default 100.
func burstPromotions(t *testing.T) int {
	t.Helper()
	if v := os.Getenv("KARDINAL_E2E_RECEIPT_BURST"); v != "" {
		n, err := strconv.Atoi(v)
		require.NoError(t, err, "KARDINAL_E2E_RECEIPT_BURST")
		return n
	}
	return 100
}

const (
	// burstFulcioRPS is how many requests a calendar second Fulcio answers
	// in the burst; the rest get 429 with Retry-After: burstRetryAfter.
	burstFulcioRPS  = 10
	burstRetryAfter = 3 * time.Second
)

// burstAttempt is one failed attempt of a receipt, as its status recorded.
type burstAttempt struct {
	attempts   int
	at, nextAt time.Time
}

// TestReceiptsKeyless_Burst (#2062, evidence-scale): burstPromotions
// promotions (100 by default, one Pipeline each) reach Verified while
// Fulcio refuses every request (503), so all their receipts wait at once:
// the barrier. Fulcio then answers at most 10 requests a calendar second
// and the rest 429 with Retry-After: 3 (faultproxy ratelimit), counted from
// a reset taken once no other receipt in the cluster is in flight.
//
//   - The promotions are not held up: every one is Verified within
//     promoteTimeout while Fulcio refuses.
//   - The receipts back off as designed: all are Stored within the budget
//     the backoff and the rate give, none is ever Failed, the limit engaged
//     (429s answered), and no receipt makes more than the ceiling of
//     attempts.
//   - Retry-After is honoured: after a 429, a receipt's nextRetryAt is at
//     least 3 seconds after its attempt, and its next attempt never runs
//     before that nextRetryAt.
//   - Signed once: Fulcio issued exactly one certificate per receipt.
//   - Every receipt marked signed verifies with kardinal verify-receipt
//     against the private Sigstore's trusted root.
//
// Covers RECEIPT-LIVE-08.
func TestReceiptsKeyless_Burst(t *testing.T) {
	root := keylessTrustedRoot(t)
	e := framework.New(t)
	ctx := context.Background()
	n := burstPromotions(t)

	// Nothing else signs through the proxy during the burst: every receipt
	// of the earlier tests is Stored or Failed.
	framework.Eventually(t, 15*time.Minute, "no other receipt in flight", func(ctx context.Context) (bool, string) {
		var all v1alpha1.PromotionReceiptList
		if err := e.Client.List(ctx, &all); err != nil {
			return false, err.Error()
		}
		var busy []string
		for _, rc := range all.Items {
			if rc.Status.Phase != v1alpha1.ReceiptStored && rc.Status.Phase != v1alpha1.ReceiptFailed {
				busy = append(busy, rc.Namespace+"/"+rc.Name)
			}
		}
		return len(busy) == 0, fmt.Sprintf("in flight: %v", busy)
	})

	f := scale.NewFleet(t, e, scale.NewRNG(1, t.Name()))
	pipelines := f.Pipelines(t, "burst", n, []v1alpha1.EnvironmentSpec{{Name: "test"}})

	// The barrier: Fulcio refuses, so the receipts gather.
	setFault(t, e, "fulcio", "status", "code", "503")
	t.Cleanup(func() { setFault(t, e, "fulcio", "pass") })
	require.NoError(t, scale.Parallel(16, n, func(i int) error {
		_, err := f.CreateBundle(ctx, pipelines[i], scale.Tag(pipelines[i], 1))
		return err
	}))
	start := time.Now()
	framework.Eventually(t, promoteTimeout, fmt.Sprintf("%d promotions Verified while Fulcio refuses", n), func(ctx context.Context) (bool, string) {
		var steps v1alpha1.PromotionStepList
		if err := e.Client.List(ctx, &steps, client.InNamespace(f.NS)); err != nil {
			return false, err.Error()
		}
		verified := 0
		for _, s := range steps.Items {
			if s.Status.State == "Verified" {
				verified++
			}
		}
		return verified == n, fmt.Sprintf("%d of %d Verified", verified, n)
	})
	t.Logf("%d promotions Verified in %s with Fulcio refusing", n, time.Since(start).Round(time.Second))
	var lastScheduled time.Time
	framework.Eventually(t, 3*time.Minute, fmt.Sprintf("%d receipts waiting", n), func(ctx context.Context) (bool, string) {
		var list v1alpha1.PromotionReceiptList
		if err := e.Client.List(ctx, &list, client.InNamespace(f.NS)); err != nil {
			return false, err.Error()
		}
		waiting := 0
		for _, rc := range list.Items {
			if rc.Status.Attempts >= 1 && rc.Status.Bundle == "" && rc.Status.NextRetryAt != nil {
				waiting++
				if rc.Status.NextRetryAt.After(lastScheduled) {
					lastScheduled = rc.Status.NextRetryAt.Time
				}
			}
		}
		return waiting == n, fmt.Sprintf("%d of %d receipts have failed once", waiting, n)
	})

	// The burst: every receipt retries against the limit.
	resetFault(t, e, "fulcio")
	setFault(t, e, "fulcio", "ratelimit", "rps", strconv.Itoa(burstFulcioRPS), "ra", strconv.Itoa(int(burstRetryAfter/time.Second)))
	burst := time.Now()
	// The budget the backoff gives: until the last retry the 503s
	// scheduled, then n receipts at burstFulcioRPS, each wait between
	// retries at most twice the Retry-After (the jitter), twice over for
	// slack, and a minute.
	budget := time.Until(lastScheduled) + 2*time.Duration(n/burstFulcioRPS+1)*2*burstRetryAfter + time.Minute
	t.Logf("drain budget %s (the last 503 retry is due %s from now)", budget.Round(time.Second), time.Until(lastScheduled).Round(time.Second))
	ceiling := 2*(n/burstFulcioRPS+1) + 3
	seen := map[string]burstAttempt{} // the last failed attempt seen per receipt
	maxAttempts := map[string]int{}
	var list v1alpha1.PromotionReceiptList
	framework.Eventually(t, budget, fmt.Sprintf("%d receipts Stored", n), func(ctx context.Context) (bool, string) {
		if err := e.Client.List(ctx, &list, client.InNamespace(f.NS)); err != nil {
			return false, err.Error()
		}
		stored := 0
		for _, rc := range list.Items {
			if rc.Status.Phase == v1alpha1.ReceiptFailed {
				t.Fatalf("receipt %s Failed: %v", rc.Name, rc.Status.Conditions)
			}
			if rc.Status.Phase == v1alpha1.ReceiptStored {
				stored++
			}
			maxAttempts[rc.Name] = max(maxAttempts[rc.Name], rc.Status.Attempts)
			if rc.Status.LastAttemptAt == nil || rc.Status.NextRetryAt == nil || rc.Status.Attempts == 0 {
				continue
			}
			now := burstAttempt{rc.Status.Attempts, rc.Status.LastAttemptAt.Time, rc.Status.NextRetryAt.Time}
			if now.at.After(burst) {
				// A 429 from the limit: Retry-After honoured (second
				// precision in status: one second of slack).
				assert.GreaterOrEqual(t, now.nextAt.Sub(now.at), burstRetryAfter-time.Second,
					"%s attempt %d: nextRetryAt %s honours Retry-After from %s", rc.Name, now.attempts, now.nextAt, now.at)
			}
			if prev, ok := seen[rc.Name]; ok && now.attempts > prev.attempts {
				assert.False(t, now.at.Before(prev.nextAt),
					"%s attempt %d ran at %s, before nextRetryAt %s", rc.Name, now.attempts, now.at, prev.nextAt)
			}
			seen[rc.Name] = now
		}
		return stored == n, fmt.Sprintf("%d of %d Stored, fulcio %v", stored, n, faultCounts(t, e, "fulcio"))
	})
	counts := faultCounts(t, e, "fulcio")
	t.Logf("%d receipts Stored %s after the burst began; fulcio answered %v", n, time.Since(burst).Round(time.Second), counts)
	assert.Positive(t, counts["429"], "the limit engaged: the burst is faster than %d a second", burstFulcioRPS)
	assert.Equal(t, n, successes(counts), "one certificate per receipt: each signed once")
	most, who := 0, ""
	for name, a := range maxAttempts {
		if a > most {
			most, who = a, name
		}
	}
	t.Logf("most attempts: %d (%s)", most, who)
	assert.LessOrEqual(t, most, ceiling, "the retry ceiling per receipt: %s made %d attempts", who, most)
	require.Len(t, list.Items, n)

	// Every receipt marked signed verifies.
	dir := t.TempDir()
	for i, rc := range list.Items {
		require.NotEmpty(t, rc.Status.Bundle, "%s Stored with its bundle", rc.Name)
		file := filepath.Join(dir, fmt.Sprintf("r%04d.sigstore.json", i))
		require.NoError(t, os.WriteFile(file, []byte(rc.Status.Bundle), 0o600))
		_, err := e.Kardinal(t, f.NS, append(append([]string{"verify-receipt", "--file", file}, filePins(t, e, f.NS, &rc)...),
			"--identity", keylessIdentity, "--issuer", keylessIssuer, "--trusted-root", root)...)
		assert.NoError(t, err, "receipt %s verifies", rc.Name)
	}
}
