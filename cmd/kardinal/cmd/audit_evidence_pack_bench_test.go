// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// syntheticAuditEvents is a client whose AuditEvent List serves n events
// page by page, made on the fly from the continue token, as the API server
// pages a large log: the events are never all in memory, so what a
// measurement sees is the pack's own memory, not the fixture's. Every other
// kind lists from an empty fake client.
func syntheticAuditEvents(t testing.TB, n int, start time.Time) sigs_client.Client {
	t.Helper()
	s := packScheme(t)
	return fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c sigs_client.WithWatch, list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
			evs, ok := list.(*v1alpha1.AuditEventList)
			if !ok {
				return c.List(ctx, list, opts...)
			}
			lo := (&sigs_client.ListOptions{}).ApplyOptions(opts)
			from := 0
			if lo.Continue != "" {
				from, _ = strconv.Atoi(lo.Continue)
			}
			to := min(from+int(lo.Limit), n)
			if lo.Limit == 0 {
				to = n
			}
			evs.Items = evs.Items[:0]
			for i := from; i < to; i++ {
				evs.Items = append(evs.Items, v1alpha1.AuditEvent{
					ObjectMeta: metav1.ObjectMeta{Namespace: "pay", Name: fmt.Sprintf("ev-%07d", i), UID: "u"},
					Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.NewTime(start.Add(time.Duration(i) * time.Second)),
						PipelineName: "app", BundleName: fmt.Sprintf("app-%d", i/20), Environment: "prod", Action: "PromotionSucceeded",
						Outcome: "Success", Message: "promoted app to prod: https://github.com/acme/deploy/pull/412",
						Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "alice@example.com"}},
				})
			}
			evs.Continue = ""
			if to < n {
				evs.Continue = strconv.Itoa(to)
			}
			return nil
		},
	}).Build()
}

// peakRSSMiB is the process's peak resident set (VmHWM), in MiB.
func peakRSSMiB(t testing.TB) float64 {
	t.Helper()
	f, err := os.Open("/proc/self/status")
	if err != nil {
		t.Skip("no /proc/self/status")
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "VmHWM:"); ok {
			kb, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(v), " kB"))
			return float64(kb) / 1024
		}
	}
	return 0
}

// BenchmarkEvidencePack (#2047, spec 22 section 4.6): an evidence pack of
// 10k, 50k and 100k AuditEvents, read page by page. It reports the pack's
// size and the process's peak RSS (peak-rss-MiB, VmHWM), so run one size
// per process to read the RSS of that size alone:
//
//	go test ./cmd/kardinal/cmd/ -run '^$' -bench 'EvidencePack/100k' -benchtime 1x
//
// Memory stays bounded by a page (500 events) whatever the size; time grows
// with it.
func BenchmarkEvidencePack(b *testing.B) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, n := range []int{10_000, 50_000, 100_000} {
		b.Run(fmt.Sprintf("%dk", n/1000), func(b *testing.B) {
			c := syntheticAuditEvents(b, n, start)
			opts := evidencePackOptions{Namespace: "pay", Since: start, Until: start.Add(time.Duration(n+1) * time.Second),
				ExportedBy: "auditor", TempDir: b.TempDir()}
			var size countWriter
			b.ResetTimer()
			for range b.N {
				size = countWriter{}
				if err := writeEvidencePack(context.Background(), c, opts, "", &size, io.Discard); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(size.n)/(1<<20), "pack-MiB")
			b.ReportMetric(peakRSSMiB(b), "peak-rss-MiB")
		})
	}
}
