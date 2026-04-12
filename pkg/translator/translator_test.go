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

package translator

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func translatorTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = kardinalv1alpha1.AddToScheme(s)
	return s
}

// TestNew_DefaultPolicyNS verifies that New defaults policyNS to platform-policies.
func TestNew_DefaultPolicyNS(t *testing.T) {
	s := translatorTestScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()

	tr := New(nil, nil, c, nil, zerolog.Nop())
	require.NotNil(t, tr)
	assert.Equal(t, []string{"platform-policies"}, tr.policyNS,
		"empty policyNS must default to platform-policies")
}

// TestNew_CustomPolicyNS verifies that New uses the provided policyNS.
func TestNew_CustomPolicyNS(t *testing.T) {
	s := translatorTestScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()

	tr := New(nil, nil, c, []string{"my-team-policies", "corp-policies"}, zerolog.Nop())
	require.NotNil(t, tr)
	assert.Equal(t, []string{"my-team-policies", "corp-policies"}, tr.policyNS)
}

// TestCollectGates_NoGates verifies that collectGates returns empty slice when no gates exist.
func TestCollectGates_NoGates(t *testing.T) {
	s := translatorTestScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()

	tr := New(nil, nil, c, []string{"platform-policies"}, zerolog.Nop())
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
	}

	gates, err := tr.collectGates(context.Background(), pipeline)
	require.NoError(t, err)
	assert.Empty(t, gates)
}

// TestCollectGates_PlatformPoliciesNamespace verifies that gates in the platform
// policies namespace are collected.
func TestCollectGates_PlatformPoliciesNamespace(t *testing.T) {
	s := translatorTestScheme()
	orgGate := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "no-weekend-deploys", Namespace: "platform-policies"},
		Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(orgGate).Build()

	tr := New(nil, nil, c, []string{"platform-policies"}, zerolog.Nop())
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
	}

	gates, err := tr.collectGates(context.Background(), pipeline)
	require.NoError(t, err)
	require.Len(t, gates, 1)
	assert.Equal(t, "no-weekend-deploys", gates[0].Name)
}

// TestCollectGates_PipelineNamespace verifies that gates in the Pipeline's own
// namespace are also collected.
func TestCollectGates_PipelineNamespace(t *testing.T) {
	s := translatorTestScheme()
	teamGate := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "team-gate", Namespace: "my-team"},
		Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: "bundle.type == 'image'"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(teamGate).Build()

	tr := New(nil, nil, c, []string{"platform-policies"}, zerolog.Nop())
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "my-team"},
	}

	gates, err := tr.collectGates(context.Background(), pipeline)
	require.NoError(t, err)
	require.Len(t, gates, 1)
	assert.Equal(t, "team-gate", gates[0].Name)
}

// TestCollectGates_DeduplicatesOrgGates verifies that gates in policyNS that happen
// to equal the pipeline namespace are not duplicated.
func TestCollectGates_DeduplicatesOrgGates(t *testing.T) {
	s := translatorTestScheme()
	orgGate := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "gate-a", Namespace: "platform-policies"},
		Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: "true"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(orgGate).Build()

	// Pipeline is in "platform-policies" (same as policy namespace).
	tr := New(nil, nil, c, []string{"platform-policies"}, zerolog.Nop())
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "platform-policies"},
	}

	gates, err := tr.collectGates(context.Background(), pipeline)
	require.NoError(t, err)
	assert.Len(t, gates, 1, "gate must not be duplicated when pipeline NS == policy NS")
}

// TestCollectGates_MultipleNamespaces verifies that gates from multiple namespaces
// are all collected.
func TestCollectGates_MultipleNamespaces(t *testing.T) {
	s := translatorTestScheme()
	gate1 := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "org-gate", Namespace: "platform-policies"},
		Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
	}
	gate2 := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "team-gate", Namespace: "my-team"},
		Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: "bundle.type == 'image'"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate1, gate2).Build()

	tr := New(nil, nil, c, []string{"platform-policies"}, zerolog.Nop())
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "my-team"},
	}

	gates, err := tr.collectGates(context.Background(), pipeline)
	require.NoError(t, err)
	assert.Len(t, gates, 2)

	gateNames := []string{gates[0].Name, gates[1].Name}
	assert.Contains(t, gateNames, "org-gate")
	assert.Contains(t, gateNames, "team-gate")
}

// TestCollectGates_PipelineSpecPolicyNamespaces verifies that
// pipeline.spec.policyNamespaces overrides the controller-wide default.
func TestCollectGates_PipelineSpecPolicyNamespaces(t *testing.T) {
	s := translatorTestScheme()
	// Gate in custom policy namespace
	customGate := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-gate", Namespace: "custom-policies"},
		Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: "true"},
	}
	// Gate in controller default namespace (should NOT be collected when overridden)
	defaultGate := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "default-gate", Namespace: "platform-policies"},
		Spec:       kardinalv1alpha1.PolicyGateSpec{Expression: "false"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(customGate, defaultGate).Build()

	tr := New(nil, nil, c, []string{"platform-policies"}, zerolog.Nop())
	pipeline := &kardinalv1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: "my-app", Namespace: "default"},
		Spec: kardinalv1alpha1.PipelineSpec{
			PolicyNamespaces: []string{"custom-policies"},
		},
	}

	gates, err := tr.collectGates(context.Background(), pipeline)
	require.NoError(t, err)
	require.Len(t, gates, 1)
	assert.Equal(t, "custom-gate", gates[0].Name,
		"pipeline.spec.policyNamespaces must override controller-wide default")
}
