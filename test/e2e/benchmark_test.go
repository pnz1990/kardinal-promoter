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

// Concurrency tests for the promotion loop.
//
// TestPromotionLoop_100Concurrent drives 100 single-environment Bundles to
// Verified at the same time through one shared fake client and one shared
// PromotionStep reconciler, so `go test -race` checks the reconciler's shared
// state. It runs against a fake client and mock SCM/Git, so its wall time is
// not a performance number; the 60s limit only stops a hung run (the steps
// take turns pushing to their one branch, #1578).
//
// The benchmarks run the same loop:
//   go test ./test/e2e/... -run=^$ -bench=BenchmarkPromotion -benchmem

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	psrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

const (
	// concurrentBundles is how many Bundles the concurrent test and benchmark drive at once.
	concurrentBundles = 100
	// loopTimeout stops a run whose reconcile loop hangs. It is not a latency target.
	loopTimeout = 60 * time.Second
)

// BenchmarkPromotionLoop_Single measures driving one PromotionStep from
// Pending to Verified.
func BenchmarkPromotionLoop_Single(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := runPromotionLoops(b, fmt.Sprintf("single-%d", i), 1); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPromotionLoop_100Concurrent measures driving 100 PromotionSteps to
// Verified at once through one shared client and reconciler.
func BenchmarkPromotionLoop_100Concurrent(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := runPromotionLoops(b, fmt.Sprintf("perf-%d", i), concurrentBundles); err != nil {
			b.Fatal(err)
		}
	}
}

// TestPromotionLoop_100Concurrent drives 100 Bundles to Verified in parallel
// through one shared fake client and one shared reconciler (#1295).
func TestPromotionLoop_100Concurrent(t *testing.T) {
	start := time.Now()
	if err := runPromotionLoops(t, "perf", concurrentBundles); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d concurrent Bundles reached Verified in %v", concurrentBundles, time.Since(start).Round(time.Millisecond))
}

// runPromotionLoops creates n single-environment Pipelines, Bundles and
// PromotionSteps in one fake client, then drives every step to Verified from
// its own goroutine through one shared reconciler. It returns the errors of
// the steps that did not reach Verified within loopTimeout.
func runPromotionLoops(tb testing.TB, prefix string, n int) error {
	tb.Helper()

	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		return fmt.Errorf("add scheme: %w", err)
	}
	stepNames := make([]string, n)
	objs := make([]client.Object, 0, 3*n)
	for i := 0; i < n; i++ {
		pipeline := fmt.Sprintf("%s-pipeline-%d", prefix, i)
		bundle := fmt.Sprintf("%s-bundle-%d", prefix, i)
		stepNames[i] = pipeline + "-" + bundle + "-test"
		objs = append(objs,
			&v1alpha1.Pipeline{
				ObjectMeta: metav1.ObjectMeta{Name: pipeline, Namespace: "default"},
				Spec: v1alpha1.PipelineSpec{
					Git:          v1alpha1.PipelineGit{URL: "https://github.com/pnz1990/kardinal-demo"},
					Environments: []v1alpha1.EnvironmentSpec{{Name: "test", Approval: "auto"}},
				},
			},
			&v1alpha1.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: bundle, Namespace: "default"},
				Spec:       v1alpha1.BundleSpec{Type: "image", Pipeline: pipeline},
			},
			&v1alpha1.PromotionStep{
				ObjectMeta: metav1.ObjectMeta{
					Name:      stepNames[i],
					Namespace: "default",
					Labels:    map[string]string{"kardinal.io/pipeline": pipeline},
				},
				Spec: v1alpha1.PromotionStepSpec{
					PipelineName: pipeline,
					BundleName:   bundle,
					Environment:  "test",
					StepType:     "kustomize-set-image",
				},
			},
		)
	}

	c := fake.NewClientBuilder().WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Bundle{}, &v1alpha1.PromotionStep{}).
		Build()
	workRoot := tb.TempDir()
	rec := &psrec.Reconciler{
		Client:    c,
		SCM:       &mockSCMForLoop{prURL: "https://github.com/pnz1990/kardinal-demo/pull/1", prNumber: 1},
		GitClient: &mockGitForLoop{},
		WorkDirFn: func(pipeline, bundle string) string { return filepath.Join(workRoot, pipeline, bundle) },
	}

	ctx, cancel := context.WithTimeout(context.Background(), loopTimeout)
	defer cancel()
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range stepNames {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = reconcileStepToVerified(ctx, c, rec, stepNames[i])
		}(i)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// reconcileStepToVerified reconciles one PromotionStep until it is Verified.
// Every Pipeline here pushes to one branch, so the steps take turns (#1578):
// a reconcile that only waited for the branch's turn is not counted against
// the 50 iterations, and the next one comes after its requeue (at most
// turnPoll), as the controller would wake it.
func reconcileStepToVerified(ctx context.Context, c client.Client, rec *psrec.Reconciler, name string) error {
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}}
	const turnPoll = 20 * time.Millisecond
	for i := 0; i < 50; i++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("step %s: not Verified within %v: %w", name, loopTimeout, err)
		}
		result, err := rec.Reconcile(ctx, req)
		if err != nil {
			return fmt.Errorf("step %s: reconcile iteration %d: %w", name, i, err)
		}

		var ps v1alpha1.PromotionStep
		if err := c.Get(ctx, req.NamespacedName, &ps); err != nil {
			return fmt.Errorf("step %s: get iteration %d: %w", name, i, err)
		}
		switch {
		case ps.Status.State == "Verified":
			return nil
		case ps.Status.State == "Failed":
			return fmt.Errorf("step %s reached Failed: %s", name, ps.Status.Message)
		case result.RequeueAfter == 0 && !result.Requeue: //nolint:staticcheck
			return fmt.Errorf("step %s stopped without Verified (state=%s)", name, ps.Status.State)
		case strings.HasPrefix(ps.Status.Message, "waiting for its turn to push"):
			i--
			select {
			case <-ctx.Done():
			case <-time.After(min(result.RequeueAfter, turnPoll)):
			}
		}
	}
	return fmt.Errorf("step %s did not reach Verified in 50 iterations", name)
}
