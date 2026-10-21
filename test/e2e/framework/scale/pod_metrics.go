// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// PodMetrics is controller Pod pod's own /metrics page, read through the API
// server's Pod proxy on the Pod's metrics port.
func PodMetrics(ctx context.Context, e *framework.Env, pod string) (framework.Metrics, error) {
	raw, err := PodMetricsText(ctx, e, pod)
	if err != nil {
		return nil, err
	}
	return framework.ParseMetrics(string(raw))
}

// PodMetricsText is controller Pod pod's /metrics page as it serves it (the
// enterprise-day soak's dump keeps it).
func PodMetricsText(ctx context.Context, e *framework.Env, pod string) ([]byte, error) {
	var p corev1.Pod
	if err := e.Client.Get(ctx, client.ObjectKey{Namespace: framework.ControllerNamespace, Name: pod}, &p); err != nil {
		return nil, fmt.Errorf("get controller Pod %s: %w", pod, err)
	}
	port := 0
	for _, c := range p.Spec.Containers {
		for _, cp := range c.Ports {
			if cp.Name == "metrics" {
				port = int(cp.ContainerPort)
			}
		}
	}
	if port == 0 {
		return nil, fmt.Errorf("controller Pod %s has no metrics port", pod)
	}
	raw, err := e.Kube.CoreV1().RESTClient().Get().
		AbsPath("/api/v1/namespaces/" + framework.ControllerNamespace + "/pods/http:" + pod + ":" + strconv.Itoa(port) + "/proxy/metrics").DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("metrics of %s: %w", pod, err)
	}
	return raw, nil
}

// LeaderMetrics is the controller leader's own /metrics page.
func LeaderMetrics(t *testing.T, e *framework.Env) framework.Metrics {
	t.Helper()
	ctx := context.Background()
	pod, err := Leader(ctx, e)
	if err != nil {
		t.Fatalf("the controller leader: %v", err)
	}
	m, err := PodMetrics(ctx, e, pod)
	if err != nil {
		t.Fatalf("the leader's metrics: %v", err)
	}
	return m
}

// LeaderRSS is the controller leader's resident memory in MiB, from its own
// /metrics.
func LeaderRSS(t *testing.T, e *framework.Env) float64 {
	t.Helper()
	return LeaderMetrics(t, e).Sum("process_resident_memory_bytes", nil) / (1 << 20)
}

// LeaderHeap is the controller leader's Go heap in use in MiB, read just
// after its garbage collections (PodHeap).
func LeaderHeap(t *testing.T, e *framework.Env) float64 {
	t.Helper()
	pod, err := Leader(context.Background(), e)
	if err != nil {
		t.Fatalf("the controller leader: %v", err)
	}
	v, err := PodHeap(context.Background(), e, pod)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// PodHeap is controller Pod pod's Go heap in use in MiB, read just after its
// garbage collections (go_memstats_last_gc_time_seconds moved since the
// reading before): the lowest such reading over at least 30 seconds, or
// after the first collection when they are rarer (the runtime forces one at
// least every two minutes). Read at any other time the heap in use also
// holds garbage not yet collected: sampled that way, two quiet-host runs
// measured 1,204 and 2,818 bytes a receipt for the same cache. Not RSS: a
// controller built with -race keeps shadow memory in RSS and never returns
// it (test/e2e/README.md#scale-suite).
func PodHeap(ctx context.Context, e *framework.Env, pod string) (float64, error) {
	low, lastGC := 0.0, 0.0
	start := time.Now()
	for low == 0 || time.Since(start) < 30*time.Second {
		if time.Since(start) > 3*time.Minute {
			return 0, fmt.Errorf("controller Pod %s collected no garbage within 3 minutes (go_memstats_last_gc_time_seconds)", pod)
		}
		m, err := PodMetrics(ctx, e, pod)
		if err != nil {
			return 0, err
		}
		gc, v := m.Sum("go_memstats_last_gc_time_seconds", nil), m.Sum("go_memstats_heap_inuse_bytes", nil)/(1<<20)
		if v <= 0 || gc <= 0 {
			return 0, fmt.Errorf("controller Pod %s reports no go_memstats_heap_inuse_bytes or go_memstats_last_gc_time_seconds", pod)
		}
		if lastGC != 0 && gc != lastGC && (low == 0 || v < low) {
			low = v
		}
		lastGC = gc
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return low, nil
}
