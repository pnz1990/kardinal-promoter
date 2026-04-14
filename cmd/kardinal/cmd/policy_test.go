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

// policy_test.go — Tests for policy list and simulate commands (#483).
package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func buildPolicyScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

// makeTemplateGate creates a PolicyGate template (no bundle label) in the given namespace.
func makeTemplateGate(name, ns, expression string) *v1alpha1.PolicyGate {
	return &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"kardinal.io/scope":      "org",
				"kardinal.io/applies-to": "prod",
			},
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression: expression,
			Message:    "Blocked by " + name,
		},
	}
}

// TestPolicySimulate_FindsGatesInOtherNamespace is the regression test for #483.
// The org-level gate is in "platform-policies" but the caller's kubectl namespace
// is "default". The command must find the gate and return BLOCKED.
func TestPolicySimulate_FindsGatesInOtherNamespace(t *testing.T) {
	// Gate is in platform-policies (not default).
	gate := makeTemplateGate("no-weekend-deploys", "platform-policies", "schedule.isWeekend == false")
	gate.Status.Ready = false

	s := buildPolicyScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate).Build()

	var out bytes.Buffer
	// Simulate with Saturday 3pm UTC — should be BLOCKED.
	// ns="default" — the gate is in platform-policies.
	err := policySimulateFn(&out, c, "default", "my-pipeline", "prod", "Saturday 3pm", 0)
	require.NoError(t, err)

	result := out.String()
	// Must report BLOCKED (weekend gate finds the gate in platform-policies).
	assert.Contains(t, result, "BLOCKED", "should report BLOCKED for weekend gate in other namespace")
}

// TestPolicySimulate_NoGatesShowsNoGatesFound verifies empty output when no gates exist.
func TestPolicySimulate_NoGatesShowsNoGatesFound(t *testing.T) {
	s := buildPolicyScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build() // empty

	var out bytes.Buffer
	err := policySimulateFn(&out, c, "default", "my-pipeline", "prod", "Tuesday 10am", 0)
	require.NoError(t, err)

	result := out.String()
	// With no gates: PASS (nothing blocks).
	assert.Contains(t, result, "PASS", "no gates → PASS")
}

// TestPolicySimulate_WeekdayPassesWeekendGate verifies that the same gate passes on a weekday.
func TestPolicySimulate_WeekdayPassesWeekendGate(t *testing.T) {
	gate := makeTemplateGate("no-weekend-deploys", "platform-policies", "schedule.isWeekend == false")
	gate.Status.Ready = false

	s := buildPolicyScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate).Build()

	var out bytes.Buffer
	// Tuesday 10am — should PASS (not a weekend).
	err := policySimulateFn(&out, c, "default", "my-pipeline", "prod", "Tuesday 10am", 0)
	require.NoError(t, err)

	result := out.String()
	// With a non-weekend gate on Tuesday, the gate should pass.
	assert.Contains(t, result, "PASS", "weekday with no-weekend gate → PASS")
}

// TestPolicySimulate_GateInSameNamespaceAlsoWorks verifies backward compatibility:
// gates in the same namespace as the caller still work.
func TestPolicySimulate_GateInSameNamespaceAlsoWorks(t *testing.T) {
	gate := makeTemplateGate("team-gate", "default", "schedule.isWeekend == false")
	gate.Status.Ready = false

	s := buildPolicyScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(gate).Build()

	var out bytes.Buffer
	err := policySimulateFn(&out, c, "default", "my-pipeline", "prod", "Saturday 3pm", 0)
	require.NoError(t, err)

	result := out.String()
	assert.Contains(t, result, "BLOCKED", "gate in same namespace → BLOCKED on Saturday")
}
