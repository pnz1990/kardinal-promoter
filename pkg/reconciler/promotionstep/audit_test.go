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

package promotionstep

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

func TestWriteAuditEvent_PromotionStarted(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-bundle-v1-prod",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "nginx-demo",
				"kardinal.io/bundle":      "nginx-demo-v1",
				"kardinal.io/environment": "prod",
			},
		},
	}

	c := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(ps).Build()
	ctx := context.Background()

	writeAuditEvent(ctx, c, ps, AuditActionPromotionStarted, AuditOutcomePending, "test message")

	var aeList v1alpha1.AuditEventList
	require.NoError(t, c.List(ctx, &aeList))
	require.Len(t, aeList.Items, 1)
	ae := aeList.Items[0]
	assert.Equal(t, "nginx-demo", ae.Spec.PipelineName)
	assert.Equal(t, "nginx-demo-v1", ae.Spec.BundleName)
	assert.Equal(t, "prod", ae.Spec.Environment)
	assert.Equal(t, AuditActionPromotionStarted, ae.Spec.Action)
	assert.Equal(t, AuditOutcomePending, ae.Spec.Outcome)
	assert.Equal(t, "test message", ae.Spec.Message)
	assert.False(t, ae.Spec.Timestamp.IsZero(), "timestamp must be set")
}

func TestWriteAuditEvent_Idempotent(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(scheme))

	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-bundle-v1-prod",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline":    "nginx-demo",
				"kardinal.io/bundle":      "nginx-demo-v1",
				"kardinal.io/environment": "prod",
			},
		},
	}

	c := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(ps).Build()
	ctx := context.Background()

	// Write twice — second write should be a no-op (AlreadyExists ignored).
	writeAuditEvent(ctx, c, ps, AuditActionPromotionSucceeded, AuditOutcomeSuccess, "first")
	writeAuditEvent(ctx, c, ps, AuditActionPromotionSucceeded, AuditOutcomeSuccess, "second")

	var aeList v1alpha1.AuditEventList
	require.NoError(t, c.List(ctx, &aeList))
	assert.Len(t, aeList.Items, 1, "idempotent: second write must not create a duplicate")
	assert.Equal(t, "first", aeList.Items[0].Spec.Message, "first message must be preserved")
}

func TestWriteAuditEvent_NilClient(t *testing.T) {
	ps := &v1alpha1.PromotionStep{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
	}
	// Must not panic.
	writeAuditEvent(context.Background(), nil, ps, AuditActionPromotionStarted, AuditOutcomePending, "")
}

func TestSanitizeK8sName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"simple-name", "simple-name"},
		{"UPPER-CASE", "upper-case"},
		{"has spaces", "has-spaces"},
		{"-leading-hyphen", "leading-hyphen"},
		{"trailing-hyphen-", "trailing-hyphen"},
		{"has__underscores", "has--underscores"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeK8sName(tc.input))
		})
	}
}
