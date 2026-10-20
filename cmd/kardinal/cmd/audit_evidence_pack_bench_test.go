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

// page serves items [from, to) of n for a List with opts: the continue
// token is the index of the next item, as syntheticAuditEvents pages.
func page(opts []sigs_client.ListOption, n int) (from, to int, next string) {
	lo := (&sigs_client.ListOptions{}).ApplyOptions(opts)
	if lo.Continue != "" {
		from, _ = strconv.Atoi(lo.Continue)
	}
	to = n
	if lo.Limit > 0 {
		to = min(from+int(lo.Limit), n)
	}
	if to < n {
		next = strconv.Itoa(to)
	}
	return from, to, next
}

// packReceiptStatement is a receipt statement of about 4 KiB, as a real one
// with its gates: valid JSON, so the pack decodes it to check it for
// credentials, as it does every receipt.
var packReceiptStatement = func() string {
	gates := make([]string, 0, 30)
	for i := range 30 {
		gates = append(gates, fmt.Sprintf(`{"name":"gate-%02d","result":"Passed","message":"%s"}`, i, strings.Repeat("x", 100)))
	}
	return `{"_type":"https://in-toto.io/Statement/v1","predicateType":"https://slsa.dev/verification_summary/v1",` +
		`"subject":[{"name":"ghcr.io/acme/app","digest":{"sha256":"` + strings.Repeat("ab", 32) + `"}}],` +
		`"predicate":{"verifier":{"id":"https://kardinal.io/controller"},"verificationResult":"PASSED","kardinal":{"gates":[` +
		strings.Join(gates, ",") + `]}}}`
}()

// syntheticVersions is a client whose Lists serve, page by page, versions
// Bundles spread over namespaces namespaces, each with an Approval, a gate
// instance, two PromotionReceipts (uat and prod, ~4 KiB statements, signed
// or not) and two AuditEvents, made on the fly from the continue token: the
// fixture is never in memory, so what a measurement sees is the pack's own
// memory, its per-version state included. Version i is created at
// start + i seconds. Every other kind lists from an empty fake client.
func syntheticVersions(t testing.TB, versions, namespaces int, start time.Time) sigs_client.Client {
	t.Helper()
	s := packScheme(t)
	at := func(i int) metav1.Time { return metav1.NewTime(start.Add(time.Duration(i) * time.Second)) }
	meta := func(i int, name string, labels map[string]string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Namespace: fmt.Sprintf("team-%02d", i%namespaces), Name: name, UID: "u",
			CreationTimestamp: at(i), Labels: labels}
	}
	bundle := func(i int) string { return fmt.Sprintf("app-%06d", i) }
	nsOf := func(opts []sigs_client.ListOption) string {
		return (&sigs_client.ListOptions{}).ApplyOptions(opts).Namespace
	}
	// in selects, for a namespace-scoped List, the indexes of that namespace.
	in := func(opts []sigs_client.ListOption, i int) bool {
		ns := nsOf(opts)
		return ns == "" || ns == fmt.Sprintf("team-%02d", i%namespaces)
	}
	return fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c sigs_client.WithWatch, list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
			switch l := list.(type) {
			case *v1alpha1.BundleList:
				from, to, next := page(opts, versions)
				l.Items, l.Continue = l.Items[:0], next
				for i := from; i < to; i++ {
					if in(opts, i) {
						l.Items = append(l.Items, v1alpha1.Bundle{ObjectMeta: meta(i, bundle(i), map[string]string{"kardinal.io/pipeline": "app"}),
							Spec: v1alpha1.BundleSpec{Pipeline: "app", Type: "image"}})
					}
				}
			case *v1alpha1.ApprovalList:
				from, to, next := page(opts, versions)
				l.Items, l.Continue = l.Items[:0], next
				for i := from; i < to; i++ {
					if in(opts, i) {
						l.Items = append(l.Items, v1alpha1.Approval{ObjectMeta: meta(i, bundle(i)+"-prod", nil),
							Spec: v1alpha1.ApprovalSpec{Bundle: bundle(i), Environment: "prod", Decision: "approve"}})
					}
				}
			case *v1alpha1.PolicyGateList:
				from, to, next := page(opts, versions)
				l.Items, l.Continue = l.Items[:0], next
				for i := from; i < to; i++ {
					if in(opts, i) {
						l.Items = append(l.Items, v1alpha1.PolicyGate{ObjectMeta: meta(i, bundle(i)+"-prod-approval",
							map[string]string{"kardinal.io/bundle": bundle(i), "kardinal.io/environment": "prod", "kardinal.io/gate-template": "prod-approval"}),
							Spec: v1alpha1.PolicyGateSpec{Expression: "true"}})
					}
				}
			case *v1alpha1.PromotionReceiptList:
				from, to, next := page(opts, 2*versions)
				l.Items, l.Continue = l.Items[:0], next
				for j := from; j < to; j++ {
					i, env := j/2, []string{"uat", "prod"}[j%2]
					if in(opts, i) {
						l.Items = append(l.Items, v1alpha1.PromotionReceipt{
							ObjectMeta: meta(i, bundle(i)+"-"+env, map[string]string{"kardinal.io/bundle": bundle(i), "kardinal.io/environment": env}),
							Spec:       v1alpha1.PromotionReceiptSpec{Statement: packReceiptStatement, StatementDigest: "sha256:" + strings.Repeat("cd", 32)}})
					}
				}
			case *v1alpha1.AuditEventList:
				from, to, next := page(opts, 2*versions)
				l.Items, l.Continue = l.Items[:0], next
				for j := from; j < to; j++ {
					i := j / 2
					if in(opts, i) {
						l.Items = append(l.Items, v1alpha1.AuditEvent{ObjectMeta: meta(i, fmt.Sprintf("ev-%07d", j), nil),
							Spec: v1alpha1.AuditEventSpec{Timestamp: at(i), PipelineName: "app", BundleName: bundle(i),
								Environment: []string{"uat", "prod"}[j%2], Action: "PromotionSucceeded", Outcome: "Success",
								Actor: &v1alpha1.Actor{Kind: v1alpha1.ActorHuman, Principal: "alice@example.com"}}})
					}
				}
			default:
				return c.List(ctx, list, opts...)
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

// BenchmarkEvidencePackVersions (#2238): an evidence pack of 1k, 5k and 10k
// versions across 50 namespaces, cluster-wide (-A), each version with an
// Approval, a gate instance, two receipts and two AuditEvents: what grows
// with versions is the pack's per-version state (the names that select the
// Approvals, gate instances, Verdicts, receipts and change requests). Run
// one size per process to read its RSS alone:
//
//	go test ./cmd/kardinal/cmd/ -run '^$' -bench 'EvidencePackVersions/10k' -benchtime 1x
func BenchmarkEvidencePackVersions(b *testing.B) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, n := range []int{1_000, 5_000, 10_000} {
		b.Run(fmt.Sprintf("%dk", n/1000), func(b *testing.B) {
			c := syntheticVersions(b, n, 50, start)
			opts := evidencePackOptions{Since: start, Until: start.Add(time.Duration(n+1) * time.Second),
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
