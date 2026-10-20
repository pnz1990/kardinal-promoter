//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// leaderRSS is the controller leader's resident memory in MiB, from its
// own /metrics.
func leaderRSS(t *testing.T, e *framework.Env) float64 {
	t.Helper()
	return leaderMetrics(t, e).Sum("process_resident_memory_bytes", nil) / (1 << 20)
}

// leaderHeap is the controller leader's Go heap in use in MiB, read just
// after its garbage collections (go_memstats_last_gc_time_seconds moved
// since the reading before): the lowest such reading over at least 30
// seconds, or after the first collection when they are rarer (the runtime
// forces one at least every two minutes). Read at any other time the heap
// in use also holds garbage not yet collected: sampled that way, two
// quiet-host runs measured 1,204 and 2,818 bytes a receipt for the same
// cache. Not RSS: the scale suite's controller is built with -race, whose
// shadow memory RSS holds and never returns (test/e2e/README.md#scale-suite).
func leaderHeap(t *testing.T, e *framework.Env) float64 {
	t.Helper()
	low, lastGC := 0.0, 0.0
	start := time.Now()
	for low == 0 || time.Since(start) < 30*time.Second {
		require.Less(t, time.Since(start), 3*time.Minute, "the leader collects garbage within 3 minutes (go_memstats_last_gc_time_seconds)")
		m := leaderMetrics(t, e)
		gc, v := m.Sum("go_memstats_last_gc_time_seconds", nil), m.Sum("go_memstats_heap_inuse_bytes", nil)/(1<<20)
		require.Positive(t, v, "the leader reports go_memstats_heap_inuse_bytes")
		require.Positive(t, gc, "the leader reports go_memstats_last_gc_time_seconds")
		if lastGC != 0 && gc != lastGC && (low == 0 || v < low) {
			low = v
		}
		lastGC = gc
		time.Sleep(time.Second)
	}
	return low
}

// storageBytes is the API server's view of its etcd database size.
func storageBytes(t *testing.T, e *framework.Env) float64 {
	t.Helper()
	raw, err := e.Kube.CoreV1().RESTClient().Get().AbsPath("/metrics").DoRaw(context.Background())
	require.NoError(t, err)
	m, err := framework.ParseMetrics(string(raw))
	require.NoError(t, err)
	if v := m.Sum("apiserver_storage_size_bytes", nil); v > 0 {
		return v
	}
	return m.Sum("apiserver_storage_db_total_size_in_bytes", nil)
}

// TestScale_Receipts (#2062, evidence-scale): with key-signed receipts on,
// the profile's Receipts receipts (1,000 in ci, 10,000 in full; one in
// ReceiptLargeEvery near the 256 KiB cap, the others about 4 KiB) arrive at
// once, written as the controller writes them. The controller signs and
// stores every one within ReceiptDrain, its leader's resident
// memory growing by at most ReceiptRSSMiB (the cache keeps a Stored
// receipt without its statement and bundle). After every controller Pod is
// deleted, a new receipt is Stored within ReceiptResync. Retention then
// deletes up to 2,000 old receipts in one run while a new receipt is still
// signed within a minute. The etcd database growth is reported.
//
// Covers SCALE-RECEIPTS-01.
func TestScale_Receipts(t *testing.T) {
	r := scale.Begin(t)
	e, ns, ctx := r.E, r.Fleet.NS, context.Background()

	// Receipts on, with a key; off again when the test ends.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	_, err = e.Kube.CoreV1().Secrets(framework.ControllerNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "scale-receipt-key"},
		Data:       map[string][]byte{"cosign.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	rel := e.ExistingRelease(t, "kardinal-promoter", framework.ControllerNamespace)
	upgrade := func(sets string) {
		t.Helper()
		run := e.Shell(t, `helm upgrade kardinal-promoter "$KARDINAL_E2E_CHART" --namespace kardinal-system --reuse-values `+sets+` --wait --timeout 5m`)
		require.Zero(t, run.Code, run.Output)
		rel.WaitRolledOut(t, 5*time.Minute)
	}
	upgrade(`--set receipts.enabled=true --set receipts.signing.mode=key --set receipts.signing.key.secretRef.name=scale-receipt-key`)
	t.Cleanup(func() {
		run := e.Shell(t, `helm upgrade kardinal-promoter "$KARDINAL_E2E_CHART" --namespace kardinal-system --reuse-values `+
			`--set receipts.enabled=false --set receipts.retention.maxAge= --wait --timeout 5m`)
		if run.Code != 0 {
			t.Errorf("turn receipts off: %s", run.Output)
		}
		// Until the last Pod with receipts on is gone: it loses its RBAC on
		// receipts first and logs the failed watch, which the next test's
		// controller-logs invariant would count.
		rel.WaitRolledOut(t, 5*time.Minute)
		_ = e.Kube.CoreV1().Secrets(framework.ControllerNamespace).Delete(context.Background(), "scale-receipt-key", metav1.DeleteOptions{})
	})

	// The controller's identity: only it may write receipts.
	as := scale.ControllerClient(t, e)
	create := func(i int) error {
		name := fmt.Sprintf("scale-receipt-%06d", i)
		size := 4 << 10
		if r.P.ReceiptLargeEvery > 0 && i%r.P.ReceiptLargeEvery == 0 {
			size = 200 << 10
		}
		stmt, digest := scale.ReceiptStatement(ns, name, fmt.Sprintf("receipts-%03d", i/100), size)
		rc := &v1alpha1.PromotionReceipt{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
			Labels: map[string]string{"kardinal.io/pipeline": fmt.Sprintf("receipts-%03d", i/100)}},
			Spec: v1alpha1.PromotionReceiptSpec{Statement: stmt, StatementDigest: digest}}
		if err := as.Create(ctx, rc); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		return nil
	}

	time.Sleep(30 * time.Second) // the new leader's caches settle
	baseRSS, baseHeap, baseDB := leaderRSS(t, e), leaderHeap(t, e), storageBytes(t, e)
	require.Positive(t, baseRSS, "the leader reports process_resident_memory_bytes")
	r.Note("receiptsBaselineHeapMiB", int(baseHeap))
	require.Positive(t, baseDB, "the API server reports its storage size")
	r.Note("receiptsBaselineRSSMiB", int(baseRSS))
	n := r.P.Receipts
	// batch creates count receipts from index from at once and waits until
	// want receipts of the namespace are Stored, none Failed.
	batch := func(from, count, want int) time.Duration {
		t.Helper()
		start := time.Now()
		require.NoError(t, scale.Parallel(16, count, func(i int) error { return create(from + i) }))
		framework.Eventually(t, r.P.ReceiptDrain, fmt.Sprintf("%d receipts Stored", want), func(ctx context.Context) (bool, string) {
			stored, err := scale.CountReceipts(ctx, e, ns, v1alpha1.ReceiptStored)
			if err != nil {
				return false, err.Error()
			}
			if failed, _ := scale.CountReceipts(ctx, e, ns, v1alpha1.ReceiptFailed); failed > 0 {
				t.Fatalf("%d receipts Failed", failed)
			}
			return stored == want, fmt.Sprintf("%d of %d Stored", stored, want)
		})
		return time.Since(start)
	}
	drain := batch(0, n, n)
	r.Note("receiptsDrainSeconds", int(drain.Seconds()))
	r.Note("receiptsPerSecond", float64(n)/drain.Seconds())
	rssN, heapN := leaderRSS(t, e), leaderHeap(t, e)
	r.Note("receiptsRSSMiB", int(rssN))
	r.Note("receiptsHeapMiB", int(heapN))
	assert.LessOrEqual(t, heapN, r.P.ReceiptRSSFactor*baseHeap,
		"leader heap with %d receipts Stored is at most ReceiptRSSFactor (%.1f) x its baseline %.0f MiB", n, r.P.ReceiptRSSFactor, baseHeap)
	// The slope: n more receipts may add at most ReceiptBytesPerReceipt
	// each to the heap. The cache keeps a Stored receipt without its
	// statement and bundle (about 6 KiB and 8.5 KiB on average here: the
	// bundle's DSSE envelope carries the statement in base64): runs
	// measured 1.2 to 2.9 KiB a receipt, so caching either would be over
	// the budget.
	batch(n, n, 2*n)
	rss2N, heap2N, db := leaderRSS(t, e), leaderHeap(t, e), storageBytes(t, e)
	slope := (heap2N - heapN) * (1 << 20) / float64(n)
	r.Note("receiptsRSS2NMiB", int(rss2N))
	r.Note("receiptsHeap2NMiB", int(heap2N))
	r.Note("receiptsBytesPerReceipt", int(slope))
	r.Note("receiptsStorageGrowthMiB", int((db-baseDB)/(1<<20)))
	assert.LessOrEqual(t, slope, float64(r.P.ReceiptBytesPerReceipt),
		"leader heap per receipt between %d and %d Stored receipts (ReceiptBytesPerReceipt)", n, 2*n)

	// A sample of the Stored receipts: each verifies with the key's public
	// half.
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})
	var list v1alpha1.PromotionReceiptList
	require.NoError(t, e.Client.List(ctx, &list, client.InNamespace(ns)))
	for _, rc := range list.Items[:min(len(list.Items), 50)] {
		_, err := receipt.Verify([]byte(rc.Status.Bundle), receipt.VerifyOptions{Key: pubPEM, Namespace: ns})
		assert.NoError(t, err, "%s verifies", rc.Name)
	}

	// The UI steps API with ReceiptSteps Verified steps, each with its
	// receipt, while the bulk receipts are in the cache.
	pipelines := r.Fleet.Pipelines(t, "rcpt", max(1, r.P.ReceiptSteps/3), scale.Chain(3))
	var bundles []string
	for _, p := range pipelines {
		bundles = append(bundles, r.Fleet.MustCreateBundle(t, p, scale.Tag(p, 1)).Name)
	}
	require.True(t, r.Fleet.WaitSettled(t, r.P.Settle), "the receipt Pipelines' versions settle")
	ui := mainUI(t, e)
	var lat []time.Duration
	for round := range 3 {
		for _, b := range bundles {
			began := time.Now()
			res := ui.Get(t, uiAPI+"/bundles/"+b+"/steps?namespace="+ns)
			lat = append(lat, time.Since(began))
			require.Equal(t, 200, res.Status, res.String())
			if round > 0 {
				continue
			}
			var steps []struct {
				Environment string `json:"environment"`
				State       string `json:"state"`
				// verified, or signed once #2174 renames it.
				Receipt *struct {
					Name     string `json:"name"`
					Verified bool   `json:"verified"`
					Signed   bool   `json:"signed"`
				} `json:"receipt"`
			}
			res.JSON(t, &steps)
			for _, st := range steps {
				if assert.NotNil(t, st.Receipt, "%s/%s shows its receipt", b, st.Environment) {
					assert.True(t, st.Receipt.Verified || st.Receipt.Signed, "%s/%s receipt %s signed", b, st.Environment, st.Receipt.Name)
				}
			}
		}
	}
	slices.Sort(lat)
	p95 := lat[len(lat)*95/100]
	r.Note("receiptStepsAPIp95Ms", p95.Milliseconds())
	assert.LessOrEqual(t, p95, r.P.ReceiptStepsP95, "UI steps API p95 over %d Bundles with receipts (ReceiptStepsP95)", len(bundles))

	// Every controller Pod restarted while a batch is being signed: no
	// receipt is lost, none is signed again, every one ends Stored once.
	type signedAs struct{ statement, bundle, phase string }
	snapshot := func() map[string]signedAs {
		var l v1alpha1.PromotionReceiptList
		require.NoError(t, e.Client.List(ctx, &l, client.InNamespace(ns)))
		out := map[string]signedAs{}
		for _, rc := range l.Items {
			sum := sha256.Sum256([]byte(rc.Status.Bundle))
			b := ""
			if rc.Status.Bundle != "" {
				b = hex.EncodeToString(sum[:])
			}
			out[rc.Name] = signedAs{rc.Spec.StatementDigest, b, rc.Status.Phase}
		}
		return out
	}
	extra := max(10, n/10)
	from := 2 * n
	require.NoError(t, scale.Parallel(16, extra, func(i int) error { return create(from + i) }))
	before := snapshot()
	pending := 0
	for _, v := range before {
		if v.phase != v1alpha1.ReceiptStored {
			pending++
		}
	}
	r.Note("receiptsPendingAtRestart", pending)
	require.Positive(t, pending, "receipts are still being signed when the controller restarts")
	restart := time.Now()
	for _, p := range rel.Pods(t) {
		require.NoError(t, e.Client.Delete(ctx, &p))
	}
	rel.WaitRolledOut(t, 5*time.Minute)
	framework.Eventually(t, r.P.ReceiptResync, "every receipt Stored after the restart", func(ctx context.Context) (bool, string) {
		stored, err := scale.CountReceipts(ctx, e, ns, v1alpha1.ReceiptStored)
		if err != nil {
			return false, err.Error()
		}
		return stored == len(before), fmt.Sprintf("%d of %d Stored", stored, len(before))
	})
	r.Note("receiptsResyncSeconds", int(time.Since(restart).Seconds()))
	after := snapshot()
	assert.Equal(t, len(before), len(after), "the same receipts")
	for name, b := range before {
		a, ok := after[name]
		if !assert.True(t, ok, "%s survives the restart", name) {
			continue
		}
		assert.Equal(t, v1alpha1.ReceiptStored, a.phase, name)
		assert.Equal(t, b.statement, a.statement, "%s keeps its statement", name)
		assert.NotEmpty(t, a.bundle, "%s is signed", name)
		if b.bundle != "" {
			assert.Equal(t, b.bundle, a.bundle, "%s, signed before the restart, is not signed again", name)
		}
	}

	// Retention: receipts older than a minute go, at most 2,000 a run, and
	// a receipt created meanwhile is still signed promptly and kept.
	time.Sleep(time.Minute)
	total, err := scale.CountReceipts(ctx, e, ns, "")
	require.NoError(t, err)
	upgrade(`--set receipts.retention.maxAge=1m`)
	prune := time.Now()
	young := from + extra
	require.NoError(t, create(young))
	youngName := fmt.Sprintf("scale-receipt-%06d", young)
	framework.Eventually(t, time.Minute, "a receipt Stored while retention deletes", func(ctx context.Context) (bool, string) {
		var rc v1alpha1.PromotionReceipt
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: youngName}, &rc); err != nil {
			return false, err.Error()
		}
		return rc.Status.Phase == v1alpha1.ReceiptStored, "phase " + rc.Status.Phase
	})
	// Every receipt but the young one is older than maxAge; one run deletes
	// at most 2,000 of them and the next is an hour away.
	want := total - min(total, 2000) + 1
	framework.Eventually(t, 10*time.Minute, "retention deleted its first run's receipts", func(ctx context.Context) (bool, string) {
		left, err := scale.CountReceipts(ctx, e, ns, "")
		if err != nil {
			return false, err.Error()
		}
		return left == want, fmt.Sprintf("%d receipts left, want %d", left, want)
	})
	r.Note("receiptsRetentionSeconds", int(time.Since(prune).Seconds()))
	framework.Consistently(t, 30*time.Second, "no more than one run's deletes", func(ctx context.Context) (bool, string) {
		left, err := scale.CountReceipts(ctx, e, ns, "")
		return err == nil && left == want, fmt.Sprintf("%d left, want %d (%v)", left, want, err)
	})
	var kept v1alpha1.PromotionReceipt
	assert.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: youngName}, &kept), "the young receipt is kept")

	r.Finish(scale.AllVerified)
}
