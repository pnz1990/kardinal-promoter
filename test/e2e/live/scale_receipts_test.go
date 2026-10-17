//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// scaleReceiptStatement is a receipt statement for receipt name of pipeline
// in env, padded with gate messages to about size bytes (under the cap).
// It runs in the writers' goroutines, so it does not touch t.
func scaleReceiptStatement(ns, name, pipeline string, size int) (string, string) {
	st := receipt.Statement{
		Type: receipt.StatementType, PredicateType: receipt.PredicateVSA,
		Subject: []receipt.Subject{{Name: scale.ImageRepo, Digest: map[string]string{"sha256": fmt.Sprintf("%064x", len(name))}}},
		Predicate: receipt.Predicate{
			Verifier:     receipt.Verifier{ID: "https://kardinal.io/controller"},
			TimeVerified: time.Now().UTC().Format(time.RFC3339),
			ResourceURI:  receipt.ResourceURI("", ns, pipeline, "prod"), VerificationResult: "PASSED",
			VerifiedLevels: []string{receipt.LevelHealthy}, SlsaVersion: "1.0",
			Kardinal: receipt.Record{Receipt: name, Pipeline: pipeline,
				Version: receipt.Version{Bundle: name, BundleUID: name, Type: "image"}},
		},
	}
	msg := strings.Repeat("x", 1000)
	for {
		b, _ := json.Marshal(st) // plain structs: it cannot fail
		if len(b)+1100 > size || len(b)+1100 > v1alpha1.MaxReceiptStatementBytes {
			return string(b), receipt.Digest(b)
		}
		st.Predicate.Kardinal.Gates = append(st.Predicate.Kardinal.Gates,
			receipt.Gate{Name: fmt.Sprintf("gate-%03d", len(st.Predicate.Kardinal.Gates)), Result: "Passed", Message: msg})
	}
}

// leaderRSS is the controller leader's resident memory in MiB, from its
// own /metrics.
func leaderRSS(t *testing.T, e *framework.Env) float64 {
	t.Helper()
	ctx := context.Background()
	pod, err := scale.Leader(ctx, e)
	require.NoError(t, err)
	var p corev1.Pod
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: framework.ControllerNamespace, Name: pod}, &p))
	port := 0
	for _, c := range p.Spec.Containers {
		for _, cp := range c.Ports {
			if cp.Name == "metrics" {
				port = int(cp.ContainerPort)
			}
		}
	}
	require.NotZero(t, port, "the leader Pod has a metrics port")
	raw, err := e.Kube.CoreV1().RESTClient().Get().
		AbsPath("/api/v1/namespaces/" + framework.ControllerNamespace + "/pods/http:" + pod + ":" + strconv.Itoa(port) + "/proxy/metrics").DoRaw(ctx)
	require.NoError(t, err)
	m, err := framework.ParseMetrics(string(raw))
	require.NoError(t, err)
	return m.Sum("process_resident_memory_bytes", nil) / (1 << 20)
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

// countReceipts counts the namespace's receipts in phase ("" any).
func countReceipts(ctx context.Context, e *framework.Env, ns, phase string) (int, error) {
	var list v1alpha1.PromotionReceiptList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range list.Items {
		if phase == "" || r.Status.Phase == phase {
			n++
		}
	}
	return n, nil
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
		_ = e.Kube.CoreV1().Secrets(framework.ControllerNamespace).Delete(context.Background(), "scale-receipt-key", metav1.DeleteOptions{})
	})

	// The controller's identity: only it may write receipts.
	cfg := rest.CopyConfig(e.Config)
	cfg.QPS, cfg.Burst = 200, 400
	cfg.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + framework.ControllerNamespace + ":kardinal-promoter",
		Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + framework.ControllerNamespace, "system:authenticated"}}
	as, err := client.New(cfg, client.Options{Scheme: framework.Scheme()})
	require.NoError(t, err)
	create := func(i int) error {
		name := fmt.Sprintf("scale-receipt-%06d", i)
		size := 4 << 10
		if r.P.ReceiptLargeEvery > 0 && i%r.P.ReceiptLargeEvery == 0 {
			size = 200 << 10
		}
		stmt, digest := scaleReceiptStatement(ns, name, fmt.Sprintf("receipts-%03d", i/100), size)
		rc := &v1alpha1.PromotionReceipt{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
			Labels: map[string]string{"kardinal.io/pipeline": fmt.Sprintf("receipts-%03d", i/100)}},
			Spec: v1alpha1.PromotionReceiptSpec{Statement: stmt, StatementDigest: digest}}
		if err := as.Create(ctx, rc); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		return nil
	}

	time.Sleep(30 * time.Second) // the new leader's caches settle
	baseRSS, baseDB := leaderRSS(t, e), storageBytes(t, e)
	r.Note("receiptsBaselineRSSMiB", int(baseRSS))
	n := r.P.Receipts
	start := time.Now()
	require.NoError(t, scale.Parallel(16, n, create))
	r.Note("receiptsCreateSeconds", int(time.Since(start).Seconds()))
	framework.Eventually(t, r.P.ReceiptDrain, fmt.Sprintf("%d receipts Stored", n), func(ctx context.Context) (bool, string) {
		stored, err := countReceipts(ctx, e, ns, v1alpha1.ReceiptStored)
		if err != nil {
			return false, err.Error()
		}
		failed, _ := countReceipts(ctx, e, ns, v1alpha1.ReceiptFailed)
		if failed > 0 {
			t.Fatalf("%d receipts Failed", failed)
		}
		return stored == n, fmt.Sprintf("%d of %d Stored", stored, n)
	})
	drain := time.Since(start)
	r.Note("receiptsDrainSeconds", int(drain.Seconds()))
	r.Note("receiptsPerSecond", float64(n)/drain.Seconds())
	rss, db := leaderRSS(t, e), storageBytes(t, e)
	r.Note("receiptsRSSMiB", int(rss))
	r.Note("receiptsStorageGrowthMiB", int((db-baseDB)/(1<<20)))
	assert.LessOrEqual(t, rss-baseRSS, float64(r.P.ReceiptRSSMiB), "leader RSS growth with %d receipts Stored, MiB (ReceiptRSSMiB)", n)

	// A sample of the Stored receipts: each holds its key-signed bundle.
	var list v1alpha1.PromotionReceiptList
	require.NoError(t, e.Client.List(ctx, &list, client.InNamespace(ns)))
	for _, rc := range list.Items[:min(len(list.Items), 50)] {
		assert.NotEmpty(t, rc.Status.Bundle, "%s has its signed bundle", rc.Name)
		assert.Equal(t, v1alpha1.ReceiptSigningKey, rc.Status.SignedBy.Mode)
	}

	// Every controller Pod restarted: the new leader resyncs n receipts and
	// signs a new one within ReceiptResync.
	pods := rel.Pods(t)
	restart := time.Now()
	for _, p := range pods {
		require.NoError(t, e.Client.Delete(ctx, &p))
	}
	rel.WaitRolledOut(t, 5*time.Minute)
	require.NoError(t, create(n))
	framework.Eventually(t, r.P.ReceiptResync, "a receipt Stored after the restart", func(ctx context.Context) (bool, string) {
		var rc v1alpha1.PromotionReceipt
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: fmt.Sprintf("scale-receipt-%06d", n)}, &rc); err != nil {
			return false, err.Error()
		}
		return rc.Status.Phase == v1alpha1.ReceiptStored, "phase " + rc.Status.Phase
	})
	r.Note("receiptsResyncSeconds", int(time.Since(restart).Seconds()))

	// Retention: receipts older than a minute go, at most 2,000 a run, and
	// a receipt created meanwhile is still signed promptly.
	time.Sleep(time.Minute)
	upgrade(`--set receipts.retention.maxAge=1m`)
	prune := time.Now()
	require.NoError(t, create(n+1))
	framework.Eventually(t, time.Minute, "a receipt Stored while retention deletes", func(ctx context.Context) (bool, string) {
		var rc v1alpha1.PromotionReceipt
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: fmt.Sprintf("scale-receipt-%06d", n+1)}, &rc); err != nil {
			return false, err.Error()
		}
		return rc.Status.Phase == v1alpha1.ReceiptStored, "phase " + rc.Status.Phase
	})
	want := n + 2 - min(n+1, 2000)
	framework.Eventually(t, 10*time.Minute, "retention deleted its first run's receipts", func(ctx context.Context) (bool, string) {
		left, err := countReceipts(ctx, e, ns, "")
		if err != nil {
			return false, err.Error()
		}
		return left <= want, fmt.Sprintf("%d receipts left, want at most %d", left, want)
	})
	r.Note("receiptsRetentionSeconds", int(time.Since(prune).Seconds()))

	r.Finish(func(o *invariants.Options) { o.AllowEmpty = true }) // the test promotes no Bundle
}
