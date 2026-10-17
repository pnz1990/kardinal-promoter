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

// burstFulcioRPS is the rate the burst test lets Fulcio answer.
const burstFulcioRPS = 10

// TestReceiptsKeyless_Burst (#2062, evidence-scale): burstPromotions
// promotions (100 by default, one Pipeline each) reach Verified at once
// while Fulcio answers at most 10 requests a second (faultproxy ratelimit:
// 429 with Retry-After beyond it). The promotions are not held up: every one
// is Verified within promoteTimeout. Their receipts back off and are all
// Stored: none is ever Failed, Fulcio issued exactly one certificate per
// receipt (signed once), the controller made at most 6 Fulcio requests per
// receipt (backoff, no hot loop), and every receipt marked signed verifies
// with kardinal verify-receipt against the private Sigstore's trusted root.
//
// Covers RECEIPT-LIVE-08.
func TestReceiptsKeyless_Burst(t *testing.T) {
	root := keylessTrustedRoot(t)
	e := framework.New(t)
	ctx := context.Background()
	n := burstPromotions(t)
	f := scale.NewFleet(t, e, scale.NewRNG(1, t.Name()))
	pipelines := f.Pipelines(t, "burst", n, []v1alpha1.EnvironmentSpec{{Name: "test"}})

	resetFault(t, e, "fulcio")
	setFault(t, e, "fulcio", "ratelimit", "rps", strconv.Itoa(burstFulcioRPS))
	t.Cleanup(func() { setFault(t, e, "fulcio", "pass") })

	start := time.Now()
	bundles := make([]string, n)
	require.NoError(t, scale.Parallel(16, n, func(i int) error {
		c, err := f.CreateBundle(ctx, pipelines[i], scale.Tag(pipelines[i], 1))
		bundles[i] = c.Name
		return err
	}))
	framework.Eventually(t, promoteTimeout, fmt.Sprintf("%d promotions Verified", n), func(ctx context.Context) (bool, string) {
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
	t.Logf("%d promotions Verified in %s", n, time.Since(start).Round(time.Second))

	// The receipts back off and are all signed; none fails.
	var list v1alpha1.PromotionReceiptList
	framework.Eventually(t, 30*time.Minute, fmt.Sprintf("%d receipts Stored", n), func(ctx context.Context) (bool, string) {
		if err := e.Client.List(ctx, &list, client.InNamespace(f.NS)); err != nil {
			return false, err.Error()
		}
		stored, waiting, maxAttempts := 0, 0, 0
		for _, rc := range list.Items {
			switch rc.Status.Phase {
			case v1alpha1.ReceiptFailed:
				t.Fatalf("receipt %s Failed: %v", rc.Name, rc.Status.Conditions)
			case v1alpha1.ReceiptStored:
				stored++
			default:
				waiting++
				maxAttempts = max(maxAttempts, rc.Status.Attempts)
			}
		}
		return stored == n, fmt.Sprintf("%d Stored, %d waiting (most attempts %d), fulcio %v",
			stored, waiting, maxAttempts, faultCounts(t, e, "fulcio"))
	})
	drained := time.Since(start)
	counts := faultCounts(t, e, "fulcio")
	t.Logf("%d receipts Stored in %s; fulcio answered %v", n, drained.Round(time.Second), counts)
	assert.Positive(t, counts["429"], "the rate limit engaged: the burst is faster than %d a second", burstFulcioRPS)
	assert.Equal(t, n, successes(counts), "one certificate per receipt: each signed once")
	total := 0
	for _, v := range counts {
		total += v
	}
	assert.LessOrEqual(t, total, 6*n, "Fulcio requests (%d) per receipt stay bounded: backoff, not a hot loop", total)

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
