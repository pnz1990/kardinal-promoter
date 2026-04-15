// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

// Package e2e — performance benchmarks for the promotion loop.
//
// BenchmarkPromotionLoop measures end-to-end latency of driving a PromotionStep
// from Pending through Verified using a fake Kubernetes client and mock SCM/Git.
//
// Run with:
//   go test ./test/e2e/... -run=^$ -bench=BenchmarkPromotion -benchtime=10s -benchmem
//
// Enterprise readiness target (queue-023 item 517):
//   100 concurrent Bundles (each a single-step pipeline) must all reach Verified
//   within 30 seconds wall-clock time on a developer workstation.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// BenchmarkPromotionLoop_Single measures the latency of driving a single
// PromotionStep from Pending → Verified.
func BenchmarkPromotionLoop_Single(b *testing.B) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		benchDriveStep(b, fmt.Sprintf("bench-pipeline-%d", i), fmt.Sprintf("bench-bundle-%d", i), "test")
	}
}

// BenchmarkPromotionLoop_100Concurrent is the enterprise readiness benchmark.
// 100 Bundles (each single-step auto-approve) must all reach Verified in < 30s.
func BenchmarkPromotionLoop_100Concurrent(b *testing.B) {
	const numBundles = 100
	const maxWall = 30 * time.Second

	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		start := time.Now()

		var wg sync.WaitGroup
		var failed atomic.Int32

		for j := 0; j < numBundles; j++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				err := benchDriveStep(b,
					fmt.Sprintf("perf-pipeline-%d", idx),
					fmt.Sprintf("perf-bundle-%d", idx),
					"test",
				)
				if err != nil {
					failed.Add(1)
				}
			}(j)
		}

		wg.Wait()
		elapsed := time.Since(start)

		if failed.Load() > 0 {
			b.Fatalf("BenchmarkPromotionLoop_100Concurrent: %d/%d bundles failed to reach Verified",
				failed.Load(), numBundles)
		}
		if elapsed > maxWall {
			b.Fatalf("BenchmarkPromotionLoop_100Concurrent: %d bundles took %v, exceeds %v SLO",
				numBundles, elapsed.Round(time.Millisecond), maxWall)
		}
		b.ReportMetric(float64(elapsed.Milliseconds()), "ms/100bundles")
	}
}

// TestPromotionLoop_100Concurrent is the same benchmark surfaced as a regular test
// so it runs in CI without requiring -bench flag.
// Pass criteria: 100 concurrent Bundles reach Verified in < 30s wall clock.
func TestPromotionLoop_100Concurrent(t *testing.T) {
	const numBundles = 100
	const maxWall = 30 * time.Second

	start := time.Now()
	var wg sync.WaitGroup
	var failed atomic.Int32

	for j := 0; j < numBundles; j++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := benchDriveStep(t,
				fmt.Sprintf("perf-pipeline-%d", idx),
				fmt.Sprintf("perf-bundle-%d", idx),
				"test",
			)
			if err != nil {
				failed.Add(1)
			}
		}(j)
	}

	wg.Wait()
	elapsed := time.Since(start)

	if failed.Load() > 0 {
		t.Errorf("%d/%d bundles failed to reach Verified", failed.Load(), numBundles)
	}
	t.Logf("100 concurrent Bundles: all Verified in %v (SLO: %v)", elapsed.Round(time.Millisecond), maxWall)
	if elapsed > maxWall {
		t.Errorf("100 concurrent Bundles took %v, exceeds %v SLO", elapsed.Round(time.Millisecond), maxWall)
	}
}

// benchLogger abstracts testing.T and testing.B for the helper.
type benchLogger interface {
	Helper()
	Logf(format string, args ...any)
}

// benchDriveStep creates an isolated fake-client environment and drives one
// PromotionStep to Verified. Returns non-nil error if it fails.
func benchDriveStep(tb benchLogger, pipeline, bundle, env string) error {
	tb.Helper()

	ctx := context.Background()
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		return fmt.Errorf("add scheme: %w", err)
	}

	stepName := pipeline + "-" + bundle + "-" + env
	step := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stepName,
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": pipeline},
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: pipeline,
			BundleName:   bundle,
			Environment:  env,
			StepType:     "kustomize-set-image",
		},
	}

	pip := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: pipeline, Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/pnz1990/kardinal-demo"},
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: env, Approval: "auto"},
			},
		},
	}
	bun := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: bundle, Namespace: "default"},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: pipeline,
		},
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(pip, bun, step).
		WithStatusSubresource(&v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}).
		Build()

	rec := &psrec.Reconciler{
		Client:    c,
		SCM:       &mockSCMForLoop{prURL: "https://github.com/pnz1990/kardinal-demo/pull/1", prNumber: 1},
		GitClient: &mockGitForLoop{},
		WorkDirFn: func(_, _ string) string { return "/tmp/bench-" + stepName },
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: stepName, Namespace: "default"}}

	for i := 0; i < 50; i++ {
		result, err := rec.Reconcile(ctx, req)
		if err != nil {
			return fmt.Errorf("reconcile iteration %d: %w", i, err)
		}

		var ps v1alpha1.PromotionStep
		if err := c.Get(ctx, req.NamespacedName, &ps); err != nil {
			return fmt.Errorf("get step iteration %d: %w", i, err)
		}

		if ps.Status.State == "Verified" {
			tb.Logf("step %s reached Verified in %d iterations", stepName, i+1)
			return nil
		}
		if ps.Status.State == "Failed" {
			return fmt.Errorf("step %s reached Failed: %s", stepName, ps.Status.Message)
		}
		if result.RequeueAfter == 0 && !result.Requeue { //nolint:staticcheck
			return fmt.Errorf("step %s stopped without Verified (state=%s)", stepName, ps.Status.State)
		}
	}

	return fmt.Errorf("step %s did not reach Verified in 50 iterations", stepName)
}
