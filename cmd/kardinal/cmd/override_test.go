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

// makeTestGate returns a gate instance (it carries kardinal.io/bundle), named
// directly by the tests below.
func makeTestGate(name, ns string) *v1alpha1.PolicyGate {
	return &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"kardinal.io/bundle": "my-app-x7k2m"},
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
	gate := makeTestGate("no-weekend-deploy", "default")
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
	gate := makeTestGate("my-gate", "default")
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
	gate := makeTestGate("rate-limit-gate", "default")
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
	gate := makeTestGate("global-gate", "default")
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

// gateInstance returns an instance of template for pipeline and env, as a
// Graph stamps it.
func gateInstance(name, template, pipeline, env string) *v1alpha1.PolicyGate {
	g := makeTestGate(name, "default")
	g.Labels["kardinal.io/pipeline"] = pipeline
	g.Labels["kardinal.io/environment"] = env
	g.Labels["kardinal.io/gate-template"] = template
	return g
}

// TestOverrideFn_TemplateName verifies that the documented form, --gate with
// the gate template name, records the override on the instances the
// PolicyGate reconciler evaluates, and never on the template (C01-graph-05,
// C09b-cli-05, C12-examples-demo-03, E2E-20).
func TestOverrideFn_TemplateName(t *testing.T) {
	tests := []struct {
		name      string
		stage     string
		want      []string // instances that get the override
		wantErr   string
		instances bool
	}{
		{name: "stage", stage: "prod", instances: true, want: []string{"no-weekend-deploy-prod-b1"}},
		{name: "every stage", instances: true, want: []string{"no-weekend-deploy-prod-b1", "no-weekend-deploy-uat-b1"}},
		{name: "stage without instance", stage: "test", instances: true, wantErr: "is a template"},
		{name: "no instances", stage: "prod", wantErr: "is a template"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			template := &v1alpha1.PolicyGate{
				ObjectMeta: metav1.ObjectMeta{Name: "no-weekend-deploy", Namespace: "default",
					Labels: map[string]string{"kardinal.io/applies-to": "prod,uat"}},
				Spec: v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
			}
			objs := []sigs_client.Object{template,
				// Another pipeline's instance of the same template.
				gateInstance("no-weekend-deploy-prod-other", "no-weekend-deploy", "other-app", "prod")}
			if tt.instances {
				objs = append(objs,
					gateInstance("no-weekend-deploy-prod-b1", "no-weekend-deploy", "my-app", "prod"),
					gateInstance("no-weekend-deploy-uat-b1", "no-weekend-deploy", "my-app", "uat"))
			}
			fc := fake.NewClientBuilder().WithScheme(newOverrideTestScheme()).WithObjects(objs...).Build()

			var buf bytes.Buffer
			err := cmd.ExportedOverrideFn(&buf, fc, "default", "my-app", tt.stage, "no-weekend-deploy",
				"P0 hotfix", "1h")
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)

			var list v1alpha1.PolicyGateList
			require.NoError(t, fc.List(context.Background(), &list))
			var got []string
			for _, g := range list.Items {
				if len(g.Spec.Overrides) > 0 {
					got = append(got, g.Name)
					assert.Equal(t, tt.stage, g.Spec.Overrides[0].Stage)
				}
			}
			assert.ElementsMatch(t, tt.want, got)
			for _, name := range tt.want {
				assert.Contains(t, buf.String(), "gate="+name)
			}
		})
	}
}

// C09b-cli-06: an override that lands between our read and our write is kept.
func TestOverrideFn_ConcurrentOverridesBothKept(t *testing.T) {
	calls := 0
	c := fake.NewClientBuilder().WithScheme(newOverrideTestScheme()).
		WithObjects(makeTestGate("g", "default")).
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
