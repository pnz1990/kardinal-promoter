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

package cmd_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/cmd"
)

func newOverrideTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

func makeTestGate(name string) *v1alpha1.PolicyGate {
	return &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression: "!schedule.isWeekend",
			Message:    "No weekend deploys",
		},
	}
}

// TestOverrideFn_BasicOverride verifies that the override CLI function
// appends an override to PolicyGate.spec.overrides[].
func TestOverrideFn_BasicOverride(t *testing.T) {
	gate := makeTestGate("no-weekend-deploy")
	fc := fake.NewClientBuilder().
		WithScheme(newOverrideTestScheme()).
		WithObjects(gate).
		Build()

	var buf bytes.Buffer
	err := cmd.ExportedOverrideFn(&buf, fc, "default", "my-app", "prod", "no-weekend-deploy",
		"P0 hotfix — incident #4521", "1h")
	require.NoError(t, err)

	// Output should confirm the override
	assert.Contains(t, buf.String(), "Override applied")
	assert.Contains(t, buf.String(), "no-weekend-deploy")
	assert.Contains(t, buf.String(), "P0 hotfix")

	// Verify the override was written to the gate
	var updatedGate v1alpha1.PolicyGate
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{
		Name:      "no-weekend-deploy",
		Namespace: "default",
	}, &updatedGate))

	require.Len(t, updatedGate.Spec.Overrides, 1)
	o := updatedGate.Spec.Overrides[0]
	assert.Equal(t, "P0 hotfix — incident #4521", o.Reason)
	assert.Equal(t, "prod", o.Stage)
	assert.False(t, o.ExpiresAt.IsZero())
}

// TestOverrideFn_InvalidExpiry verifies that an invalid --expires-in returns an error.
func TestOverrideFn_InvalidExpiry(t *testing.T) {
	gate := makeTestGate("my-gate")
	fc := fake.NewClientBuilder().
		WithScheme(newOverrideTestScheme()).
		WithObjects(gate).
		Build()

	var buf bytes.Buffer
	err := cmd.ExportedOverrideFn(&buf, fc, "default", "my-app", "prod", "my-gate",
		"some reason", "not-a-duration")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --expires-in")
}

// TestOverrideFn_GateNotFound verifies that missing gate returns an error.
func TestOverrideFn_GateNotFound(t *testing.T) {
	fc := fake.NewClientBuilder().
		WithScheme(newOverrideTestScheme()).
		Build()

	var buf bytes.Buffer
	err := cmd.ExportedOverrideFn(&buf, fc, "default", "my-app", "prod", "nonexistent-gate",
		"some reason", "1h")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "get policygate")
}

// TestOverrideFn_MultipleOverrides verifies that multiple overrides accumulate.
func TestOverrideFn_MultipleOverrides(t *testing.T) {
	gate := makeTestGate("rate-limit-gate")
	fc := fake.NewClientBuilder().
		WithScheme(newOverrideTestScheme()).
		WithObjects(gate).
		Build()

	var buf bytes.Buffer
	require.NoError(t, cmd.ExportedOverrideFn(&buf, fc, "default", "my-app", "prod", "rate-limit-gate",
		"first override", "1h"))
	require.NoError(t, cmd.ExportedOverrideFn(&buf, fc, "default", "my-app", "prod", "rate-limit-gate",
		"second override", "2h"))

	var updatedGate v1alpha1.PolicyGate
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{
		Name:      "rate-limit-gate",
		Namespace: "default",
	}, &updatedGate))

	assert.Len(t, updatedGate.Spec.Overrides, 2)
	assert.Equal(t, "first override", updatedGate.Spec.Overrides[0].Reason)
	assert.Equal(t, "second override", updatedGate.Spec.Overrides[1].Reason)
}

// TestOverrideFn_EmptyStageAppliesGlobally verifies that an empty stage
// means the override applies to all environments.
func TestOverrideFn_EmptyStageAppliesGlobally(t *testing.T) {
	gate := makeTestGate("global-gate")
	fc := fake.NewClientBuilder().
		WithScheme(newOverrideTestScheme()).
		WithObjects(gate).
		Build()

	var buf bytes.Buffer
	// No --stage means stage="" which applies to all environments
	err := cmd.ExportedOverrideFn(&buf, fc, "default", "my-app", "", "global-gate",
		"global override", "30m")
	require.NoError(t, err)

	var updatedGate v1alpha1.PolicyGate
	require.NoError(t, fc.Get(context.Background(), types.NamespacedName{
		Name:      "global-gate",
		Namespace: "default",
	}, &updatedGate))

	require.Len(t, updatedGate.Spec.Overrides, 1)
	assert.Equal(t, "", updatedGate.Spec.Overrides[0].Stage)
}

// C09b-cli-06: an override that lands between our read and our write is kept.
func TestOverrideFn_ConcurrentOverridesBothKept(t *testing.T) {
	calls := 0
	c := fake.NewClientBuilder().WithScheme(newOverrideTestScheme()).
		WithObjects(makeTestGate("g")).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, cl sigs_client.WithWatch, obj sigs_client.Object,
				patch sigs_client.Patch, opts ...sigs_client.PatchOption) error {
				calls++
				if calls == 1 {
					require.NoError(t, cmd.ExportedOverrideFn(io.Discard, cl, "default", "demo", "", "g",
						"second operator", "2h"))
				}
				return cl.Patch(ctx, obj, patch, opts...)
			},
		}).Build()

	require.NoError(t, cmd.ExportedOverrideFn(io.Discard, c, "default", "demo", "prod", "g", "first operator", "1h"))

	var g v1alpha1.PolicyGate
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Name: "g", Namespace: "default"}, &g))
	var reasons []string
	for _, o := range g.Spec.Overrides {
		reasons = append(reasons, o.Reason)
	}
	assert.Equal(t, []string{"second operator", "first operator"}, reasons)
}
