//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// packVersionsPipeline is the Pipeline the scenario's versions name. It is
// never created: the versions are history (Superseded), written as the
// controller writes them, so nothing promotes them again and their gate
// instances are not evaluated.
const packVersionsPipeline = "pack"

// TestScale_EvidencePackVersions (#2238): the evidence pack of PackVersions
// versions (500 in ci, 5,000 in full) spread over PackNamespaces namespaces
// (10, 50), each with an Approval, a gate instance and two
// PromotionReceipts (uat and prod, about 4 KiB each: 1,000 and 10,000), as a
// cluster with a long history holds them. kardinal audit evidence-pack -A
// exports every one within PackVersionsMaxTime, with each file's record
// count in the manifest. Its peak RSS is at most PackMaxRSSGrowthMiB above
// that of a period holding a tenth of the versions: what grows with
// versions (the pack's per-version names that select their Approvals, gate
// instances and receipts) stays bounded, and within PackMaxRSSMiB. A
// namespace's pack holds its versions only, and a second export of the
// same period is the same bytes.
//
// Covers SCALE-EVIDENCE-PACK-02.
func TestScale_EvidencePackVersions(t *testing.T) {
	r := scale.Begin(t)
	ctx := context.Background()
	n, nns := r.P.PackVersions, r.P.PackNamespaces
	as := scale.ControllerClient(t, r.E)
	nss := make([]string, nns)
	for i := range nss {
		nss[i] = r.E.Namespace(t)
	}
	since := time.Now().Add(-time.Minute).Truncate(time.Second)

	// A tenth of the versions first, then the rest: a period that ends
	// between them holds the tenth.
	start := time.Now()
	createPackVersions(t, r, as, nss, 0, n/10)
	time.Sleep(2 * time.Second)
	tenth := time.Now().Truncate(time.Second)
	time.Sleep(2 * time.Second)
	createPackVersions(t, r, as, nss, n/10, n)
	r.Note("packVersions", n)
	r.Note("packVersionNamespaces", nns)
	r.Note("packVersionsSetupSeconds", int(time.Since(start).Seconds()))
	until := time.Now().Add(time.Minute).Truncate(time.Second)

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	r.E.WriteKubeconfig(t, kubeconfig, map[string]string{r.E.Context: ""}, r.E.Context)
	export := func(name string, to time.Time, scope ...string) (records map[string]int, pack []byte, took time.Duration, rssMiB int64) {
		t.Helper()
		out := filepath.Join(t.TempDir(), name)
		args := append(append([]string{"--context", r.E.Context}, scope...), "audit", "evidence-pack",
			"--since", since.Format(time.RFC3339), "--until", to.Format(time.RFC3339), "-o", out)
		cmd := exec.CommandContext(ctx, os.Getenv(framework.EnvCLI), args...)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		began := time.Now()
		require.NoError(t, cmd.Run(), stderr.String())
		took = time.Since(began)
		rssMiB = cmd.ProcessState.SysUsage().(*syscall.Rusage).Maxrss / 1024 // KiB on Linux
		pack, err := os.ReadFile(out)
		require.NoError(t, err)
		return packRecords(t, pack), pack, took, rssMiB
	}

	_, _, tenthTook, tenthRSS := export("tenth.tar.gz", tenth, "-A")
	records, pack, took, rss := export("pack.tar.gz", until, "-A")
	r.Note("packVersionsSeconds", took.Seconds())
	r.Note("packVersionsPeakRSSMiB", rss)
	r.Note("packVersionsTenthPeakRSSMiB", tenthRSS)
	r.Note("packVersionsMiB", float64(len(pack))/(1<<20))
	t.Logf("evidence pack of %d versions in %d namespaces (-A): %s, peak RSS %d MiB, %d bytes; of %d: %s, peak RSS %d MiB",
		n, nns, took.Round(time.Millisecond), rss, len(pack), n/10, tenthTook.Round(time.Millisecond), tenthRSS)

	// Every version and its records: Bundles, Approvals, gate instances and
	// receipts of the period, and the namespaces' records only.
	for file, want := range map[string]int{"bundles.ndjson": n, "approvals.ndjson": n, "policygates.ndjson": n, "receipts.ndjson": 2 * n} {
		assert.Equal(t, want, records[file], "records of %s in the pack", file)
	}
	assert.LessOrEqual(t, rss-tenthRSS, int64(r.P.PackMaxRSSGrowthMiB),
		"peak RSS of the pack of %d versions against %d, MiB (PackMaxRSSGrowthMiB): what grows with versions stays bounded", n, n/10)
	assert.LessOrEqual(t, rss, int64(r.P.PackMaxRSSMiB), "peak RSS of the export, MiB (PackMaxRSSMiB)")
	assert.LessOrEqual(t, took, r.P.PackVersionsMaxTime, "time of the export (PackVersionsMaxTime)")

	// One namespace: its versions only.
	one, _, oneTook, oneRSS := export("one.tar.gz", until, "-n", nss[0])
	t.Logf("evidence pack of namespace %s: %s, peak RSS %d MiB", nss[0], oneTook.Round(time.Millisecond), oneRSS)
	per := (n + nns - 1) / nns
	assert.Equal(t, per, one["bundles.ndjson"], "a namespace's pack holds its versions")
	assert.Equal(t, 2*per, one["receipts.ndjson"])

	// The pack also holds cluster-scoped kinds (ChangeWindows,
	// AgentIdentities, the admission policies): the scale suite runs one
	// test at a time (LOADLOCK), so nothing changes them between the two.
	_, again, _, _ := export("again.tar.gz", until, "-A")
	assert.True(t, bytes.Equal(pack, again), "a second export of the same period is the same bytes")

	r.Finish(func(o *invariants.Options) {
		o.AllowEmpty = true
		o.Skip = map[string]string{"not-empty": "the test creates no Pipeline: its versions are history it writes, then exports"}
	})
}

// packRecords are the per-file record counts of an evidence pack's
// manifest.
func packRecords(t *testing.T, pack []byte) map[string]int {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "pack.tar.gz")
	require.NoError(t, os.WriteFile(path, pack, 0o600))
	out, err := exec.Command("tar", "-xzf", path, "-C", dir).CombinedOutput()
	require.NoError(t, err, string(out))
	raw, err := os.ReadFile(filepath.Join(dir, "evidence-pack", "manifest.json"))
	require.NoError(t, err)
	var m struct {
		Files []struct {
			Name    string `json:"name"`
			Records int    `json:"records"`
		} `json:"files"`
	}
	require.NoError(t, json.Unmarshal(raw, &m))
	records := map[string]int{}
	for _, f := range m.Files {
		records[f.Name] = f.Records
	}
	return records
}

// createPackVersions writes versions [from, to) of the scenario, version i
// in namespace nss[i % len(nss)], with 16 writers: its Bundle, Superseded
// as the controller marks it (status written as the controller, so nothing
// promotes it); its gate instance on prod; an Approval of it for prod by
// the test's user; and two receipts, for uat and prod (about 4 KiB, the
// fixture of TestScale_Receipts), Pending: the export reads them as the
// controller wrote them, signing is TestScale_Receipts' concern.
func createPackVersions(t *testing.T, r *scale.Run, as client.Client, nss []string, from, to int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, scale.Parallel(16, to-from, func(k int) error {
		i := from + k
		ns, name := nss[i%len(nss)], fmt.Sprintf("pack-%06d", i)
		labels := func(extra map[string]string) map[string]string {
			l := map[string]string{"kardinal.io/pipeline": packVersionsPipeline}
			for k, v := range extra {
				l[k] = v
			}
			return l
		}
		b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels(nil)},
			Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: packVersionsPipeline,
				Images: []v1alpha1.ImageRef{{Repository: scale.ImageRepo, Tag: fmt.Sprintf("v%06d", i)}}}}
		if err := as.Create(ctx, b); err != nil {
			return fmt.Errorf("create Bundle %s/%s: %w", ns, name, err)
		}
		if err := markSuperseded(ctx, as, ns, name); err != nil {
			return err
		}
		gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name + "-prod-approval",
			Labels: labels(map[string]string{"kardinal.io/bundle": name, "kardinal.io/environment": "prod",
				"kardinal.io/gate-template": "prod-approval"})},
			Spec: v1alpha1.PolicyGateSpec{Expression: "true"}}
		if err := as.Create(ctx, gate); err != nil {
			return fmt.Errorf("create gate instance %s/%s: %w", ns, gate.Name, err)
		}
		ap := &v1alpha1.Approval{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name + "-prod"},
			Spec: v1alpha1.ApprovalSpec{Bundle: name, Environment: "prod", Decision: "approve"}}
		if err := r.E.Client.Create(ctx, ap); err != nil {
			return fmt.Errorf("create Approval %s/%s: %w", ns, ap.Name, err)
		}
		for _, env := range []string{"uat", "prod"} {
			rn := name + "-" + env
			stmt, digest := scale.ReceiptStatement(ns, rn, packVersionsPipeline, 4<<10)
			rc := &v1alpha1.PromotionReceipt{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: rn,
				Labels: labels(map[string]string{"kardinal.io/bundle": name, "kardinal.io/environment": env})},
				Spec: v1alpha1.PromotionReceiptSpec{Statement: stmt, StatementDigest: digest}}
			if err := as.Create(ctx, rc); err != nil {
				return fmt.Errorf("create PromotionReceipt %s/%s: %w", ns, rn, err)
			}
		}
		return nil
	}))
}

// markSuperseded sets a Bundle's status phase to Superseded, as the
// controller does, retrying a conflict with the controller's own first
// status write.
func markSuperseded(ctx context.Context, as client.Client, ns, name string) error {
	for try := 0; ; try++ {
		var b v1alpha1.Bundle
		if err := as.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
			return fmt.Errorf("get Bundle %s/%s: %w", ns, name, err)
		}
		if b.Status.Phase == "Superseded" {
			return nil
		}
		patch := client.MergeFromWithOptions(b.DeepCopy(), client.MergeFromWithOptimisticLock{})
		b.Status.Phase = "Superseded"
		err := as.Status().Patch(ctx, &b, patch)
		if err == nil || try >= 20 {
			if err != nil {
				return fmt.Errorf("mark Bundle %s/%s Superseded: %w", ns, name, err)
			}
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}
