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

package policygate

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// TestWriteGateAuditEvent_Pass verifies that writeGateAuditEvent creates an
// AuditEvent with action=GateEvaluated and outcome=Success on gate pass.
func TestWriteGateAuditEvent_Pass(t *testing.T) {
	scheme := newTestScheme(t)
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-weekend-deploys",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "nginx-demo",
				"kardinal.io/bundle":      "nginx-v1",
				"kardinal.io/environment": "prod",
			},
		},
		Spec: v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend"},
	}
	c := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(gate).Build()
	ctx := context.Background()

	writeGateAuditEvent(ctx, c, gate, "Success", "isWeekend=false")

	var aeList v1alpha1.AuditEventList
	require.NoError(t, c.List(ctx, &aeList))
	require.Len(t, aeList.Items, 1)

	ae := aeList.Items[0]
	assert.Equal(t, AuditActionGateEvaluated, ae.Spec.Action)
	assert.Equal(t, "Success", ae.Spec.Outcome)
	assert.Equal(t, "nginx-demo", ae.Spec.PipelineName)
	assert.Equal(t, "nginx-v1", ae.Spec.BundleName)
	assert.Equal(t, "prod", ae.Spec.Environment)
	assert.Contains(t, ae.Spec.Message, "no-weekend-deploys")
	assert.Contains(t, ae.Spec.Message, "isWeekend=false")
	assert.Equal(t, "kardinal.io/action", "kardinal.io/action") // label key constant
	assert.Equal(t, AuditActionGateEvaluated, ae.Labels["kardinal.io/action"])
}

// TestWriteGateAuditEvent_Fail verifies that outcome=Failure is recorded.
func TestWriteGateAuditEvent_Fail(t *testing.T) {
	scheme := newTestScheme(t)
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "staging-soak",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "myapp",
				"kardinal.io/bundle":      "myapp-v2",
				"kardinal.io/environment": "prod",
			},
		},
		Spec: v1alpha1.PolicyGateSpec{Expression: "upstream.staging.soakMinutes >= 30"},
	}
	c := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(gate).Build()
	ctx := context.Background()

	writeGateAuditEvent(ctx, c, gate, "Failure", "soakMinutes=5 < 30")

	var aeList v1alpha1.AuditEventList
	require.NoError(t, c.List(ctx, &aeList))
	require.Len(t, aeList.Items, 1)
	assert.Equal(t, "Failure", aeList.Items[0].Spec.Outcome)
}

// TestWriteGateAuditEvent_NilClient verifies that nil client is handled gracefully.
func TestWriteGateAuditEvent_NilClient(t *testing.T) {
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "gate", Namespace: "default"},
	}
	// Must not panic.
	writeGateAuditEvent(context.Background(), nil, gate, "Success", "")
}

// TestWriteGateAuditEvent_MissingLabels verifies that gates without required labels
// do not produce an AuditEvent (missing pipeline/bundle labels = incomplete event).
func TestWriteGateAuditEvent_MissingLabels(t *testing.T) {
	scheme := newTestScheme(t)
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan-gate", Namespace: "default"},
	}
	c := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(gate).Build()
	ctx := context.Background()

	writeGateAuditEvent(ctx, c, gate, "Success", "")

	var aeList v1alpha1.AuditEventList
	require.NoError(t, c.List(ctx, &aeList))
	assert.Len(t, aeList.Items, 0, "no AuditEvent when labels are missing")
}
