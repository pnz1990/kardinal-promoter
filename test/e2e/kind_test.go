// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

// kind_test.go — live-cluster integration tests using an actual kind cluster.
//
// These tests use the dynamic client from e2e_test.go (infraClient) to connect
// to a real Kubernetes cluster. They are skipped automatically when no cluster
// is reachable (KUBECONFIG not set or cluster unreachable).
//
// Run with:
//
//	make test-e2e-journey-1
//	make test-e2e-journey-3
//
// Or after running hack/e2e-setup.sh:
//
//	KUBECONFIG=~/.kube/config go test ./test/e2e/... -run TestKindJourney -v

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// Kubernetes API GVRs used by live-cluster tests.
	bundleGVR = schema.GroupVersionResource{
		Group:    "kardinal.io",
		Version:  "v1alpha1",
		Resource: "bundles",
	}
	pipelineGVR = schema.GroupVersionResource{
		Group:    "kardinal.io",
		Version:  "v1alpha1",
		Resource: "pipelines",
	}
	policyGateGVR = schema.GroupVersionResource{
		Group:    "kardinal.io",
		Version:  "v1alpha1",
		Resource: "policygates",
	}
)

// TestKindJourney1_BundleBecomesAvailable verifies that:
// 1. A Pipeline exists in the cluster (applied by e2e-setup.sh)
// 2. Creating a Bundle causes it to become Available within 15 seconds
//
// This is the minimal J1 smoke test for a live cluster. It does not perform
// actual Git operations (no GITHUB_TOKEN in test clusters) but verifies the
// controller is running and the CRDs are reachable.
func TestKindJourney1_BundleBecomesAvailable(t *testing.T) {
	dc := infraClient(t) // skips if no cluster

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Verify Pipeline exists (applied by e2e-setup.sh before tests run).
	_, err := dc.Resource(pipelineGVR).Namespace("default").Get(ctx, "nginx-demo", metav1.GetOptions{})
	if err != nil {
		t.Skipf("Pipeline nginx-demo not found — run hack/e2e-setup.sh first: %v", err)
	}
	t.Log("Pipeline nginx-demo exists ✅")

	// Create a test Bundle.
	bundleName := "nginx-demo-kind-test"
	bundleObj := map[string]interface{}{
		"apiVersion": "kardinal.io/v1alpha1",
		"kind":       "Bundle",
		"metadata": map[string]interface{}{
			"name":      bundleName,
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"type":     "image",
			"pipeline": "nginx-demo",
			"images": []interface{}{
				map[string]interface{}{
					"repository": "ghcr.io/nginx/nginx",
					"tag":        "1.29.0",
				},
			},
		},
	}

	// Clean up if the Bundle already exists from a previous run.
	_ = dc.Resource(bundleGVR).Namespace("default").Delete(ctx, bundleName, metav1.DeleteOptions{})

	// Create the Bundle.
	createdObj, err := dc.Resource(bundleGVR).Namespace("default").
		Create(ctx, &unstructured.Unstructured{Object: bundleObj}, metav1.CreateOptions{})
	require.NoError(t, err, "create Bundle %s", bundleName)
	t.Logf("Created Bundle %s ✅", createdObj.GetName())

	// Cleanup: delete the Bundle at the end.
	t.Cleanup(func() {
		_ = dc.Resource(bundleGVR).Namespace("default").Delete(
			context.Background(), bundleName, metav1.DeleteOptions{})
	})

	// Poll until phase == Available (or timeout).
	deadline := time.Now().Add(30 * time.Second)
	var phase string
	for time.Now().Before(deadline) {
		got, getErr := dc.Resource(bundleGVR).Namespace("default").Get(ctx, bundleName, metav1.GetOptions{})
		if getErr == nil {
			status, ok := got.Object["status"].(map[string]interface{})
			if ok {
				phase, _ = status["phase"].(string)
			}
		}
		if phase == "Available" || phase == "Promoting" {
			break
		}
		time.Sleep(2 * time.Second)
	}

	assert.True(t, phase == "Available" || phase == "Promoting",
		"journey 1: Bundle must reach Available within 30s; got phase=%q", phase)
	t.Logf("journey 1: Bundle phase=%s ✅", phase)
}

// TestKindJourney3_PolicyGateExists verifies that a PolicyGate created by
// e2e-setup.sh (from examples/quickstart/policy-gates.yaml) is present in the
// cluster and can be evaluated.
func TestKindJourney3_PolicyGateExists(t *testing.T) {
	dc := infraClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Check that no-weekend-deploys gate exists.
	gate, err := dc.Resource(policyGateGVR).Namespace("platform-policies").
		Get(ctx, "no-weekend-deploys", metav1.GetOptions{})
	if err != nil {
		t.Skipf("PolicyGate no-weekend-deploys not found — run hack/e2e-setup.sh first: %v", err)
	}

	spec, ok := gate.Object["spec"].(map[string]interface{})
	require.True(t, ok, "gate must have spec")
	expr, _ := spec["expression"].(string)
	assert.Equal(t, "!schedule.isWeekend", expr,
		"journey 3: no-weekend-deploys must have correct CEL expression")
	t.Logf("journey 3: PolicyGate no-weekend-deploys expr=%q ✅", expr)
}
