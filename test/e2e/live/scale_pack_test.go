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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// packEventsPerPipeline keeps the test's AuditEvents under audit retention's
// per-Pipeline cap (1,000 by default): retention must not delete them while
// the test exports them.
const packEventsPerPipeline = 500

// TestScale_EvidencePack (#2047, spec 22 section 4.6) creates the profile's
// PackEvents AuditEvents (100,000 in full) and exports them with kardinal
// audit evidence-pack, as an auditor would: the pack holds every one, the
// CLI stays within PackMaxRSSMiB of resident memory (it reads a page at a
// time, so memory does not grow with the count) and PackMaxTime, and a
// second export of the same period is the same bytes.
//
// Covers SCALE-EVIDENCE-PACK-01.
func TestScale_EvidencePack(t *testing.T) {
	r := scale.Begin(t)
	ctx := context.Background()
	n := r.P.PackEvents
	since := time.Now().Add(-time.Hour).Truncate(time.Second)
	start := time.Now()
	createAuditEvents(t, r, n, since)
	r.Note("packEvents", n)
	r.Note("packSetupSeconds", int(time.Since(start).Seconds()))

	until := time.Now().Add(time.Minute).Truncate(time.Second)
	export := func(name string) ([]byte, time.Duration, int64) {
		t.Helper()
		out := filepath.Join(t.TempDir(), name)
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		r.E.WriteKubeconfig(t, kubeconfig, map[string]string{r.E.Context: ""}, r.E.Context)
		cmd := exec.CommandContext(ctx, os.Getenv(framework.EnvCLI), "-n", r.Fleet.NS, "--context", r.E.Context,
			"audit", "evidence-pack", "--since", since.Format(time.RFC3339), "--until", until.Format(time.RFC3339), "-o", out)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		began := time.Now()
		require.NoError(t, cmd.Run(), stderr.String())
		took := time.Since(began)
		rss := cmd.ProcessState.SysUsage().(*syscall.Rusage).Maxrss / 1024 // KiB on Linux
		b, err := os.ReadFile(out)
		require.NoError(t, err)
		return b, took, rss
	}
	pack, took, rss := export("pack.tar.gz")
	r.Note("packSeconds", took.Seconds())
	r.Note("packPeakRSSMiB", rss)
	r.Note("packMiB", float64(len(pack))/(1<<20))
	t.Logf("evidence pack of %d AuditEvents: %s, peak RSS %d MiB, %d bytes", n, took.Round(time.Millisecond), rss, len(pack))

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
	assert.Equal(t, n, records["auditevents.ndjson"], "every AuditEvent of the period is in the pack")
	assert.Equal(t, n, records["auditevents.ocsf.ndjson"])

	assert.LessOrEqual(t, rss, int64(r.P.PackMaxRSSMiB), "peak RSS of the export, MiB (PackMaxRSSMiB)")
	assert.LessOrEqual(t, took, r.P.PackMaxTime, "time of the export (PackMaxTime)")

	again, _, _ := export("again.tar.gz")
	assert.True(t, bytes.Equal(pack, again), "a second export of the same period is the same bytes")
	r.Finish()
}

// createAuditEvents creates n AuditEvents in the test's namespace, one
// second apart from since, spread over Pipelines of packEventsPerPipeline
// events each, with 16 writers.
func createAuditEvents(t *testing.T, r *scale.Run, n int, since time.Time) {
	t.Helper()
	ctx := context.Background()
	work := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				pipeline := fmt.Sprintf("pack-%04d", i/packEventsPerPipeline)
				ev := &v1alpha1.AuditEvent{
					ObjectMeta: metav1.ObjectMeta{Namespace: r.Fleet.NS, Name: fmt.Sprintf("pack-%07d", i),
						Labels: map[string]string{"kardinal.io/pipeline": pipeline}},
					Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.NewTime(since.Add(time.Duration(i) * time.Second / 100)),
						PipelineName: pipeline, BundleName: fmt.Sprintf("%s-%d", pipeline, i%20), Environment: "prod",
						Action: "PromotionSucceeded", Outcome: "Success", Message: "promoted to prod"},
				}
				if err := r.E.Client.Create(ctx, ev); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("create %s: %w", ev.Name, err)
					}
					mu.Unlock()
				}
			}
		}()
	}
	for i := range n {
		work <- i
	}
	close(work)
	wg.Wait()
	require.NoError(t, firstErr)
}
