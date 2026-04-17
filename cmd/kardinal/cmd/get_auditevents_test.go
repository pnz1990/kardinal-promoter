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

package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

func buildGetAuditEventsScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, v1alpha1.AddToScheme(s))
	return s
}

// TestGetAuditEventsFn_Basic verifies that AuditEvents are listed in timestamp
// descending order with the expected column headers.
func TestGetAuditEventsFn_Basic(t *testing.T) {
	scheme := buildGetAuditEventsScheme(t)
	now := time.Now().UTC()

	ae1 := &v1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "evt1",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec: v1alpha1.AuditEventSpec{
			Timestamp:    metav1.NewTime(now.Add(-2 * time.Minute)),
			PipelineName: "nginx-demo",
			BundleName:   "nginx-demo-v1",
			Environment:  "prod",
			Action:       "PromotionStarted",
			Outcome:      "Pending",
			Message:      "Promotion started for prod",
		},
	}
	ae2 := &v1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "evt2",
			Namespace: "default",
			Labels:    map[string]string{"kardinal.io/pipeline": "nginx-demo"},
		},
		Spec: v1alpha1.AuditEventSpec{
			Timestamp:    metav1.NewTime(now.Add(-1 * time.Minute)),
			PipelineName: "nginx-demo",
			BundleName:   "nginx-demo-v1",
			Environment:  "prod",
			Action:       "PromotionSucceeded",
			Outcome:      "Success",
			Message:      "Promotion completed",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ae1, ae2).Build()

	var buf bytes.Buffer
	require.NoError(t, getAuditEventsFn(&buf, c, "default", "", "", "", 20))

	output := buf.String()
	assert.Contains(t, output, "TIMESTAMP")
	assert.Contains(t, output, "PIPELINE")
	assert.Contains(t, output, "ACTION")
	assert.Contains(t, output, "PromotionSucceeded")
	assert.Contains(t, output, "PromotionStarted")

	// Most recent should appear first (descending order).
	succIdx := strings.Index(output, "PromotionSucceeded")
	startIdx := strings.Index(output, "PromotionStarted")
	assert.Greater(t, startIdx, succIdx,
		"PromotionSucceeded (newer) must appear before PromotionStarted (older)")
}

// TestGetAuditEventsFn_PipelineFilter verifies that pipeline filter works.
func TestGetAuditEventsFn_PipelineFilter(t *testing.T) {
	scheme := buildGetAuditEventsScheme(t)
	now := time.Now().UTC()

	ae1 := &v1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "evt-nginx",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline": "nginx-demo",
				"kardinal.io/bundle":   "nginx-v1",
			},
		},
		Spec: v1alpha1.AuditEventSpec{
			Timestamp:    metav1.NewTime(now),
			PipelineName: "nginx-demo",
			BundleName:   "nginx-v1",
			Environment:  "prod",
			Action:       "PromotionStarted",
			Outcome:      "Pending",
		},
	}
	ae2 := &v1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "evt-other",
			Namespace: "default",
			Labels: map[string]string{
				"kardinal.io/pipeline": "other-pipeline",
				"kardinal.io/bundle":   "other-v1",
			},
		},
		Spec: v1alpha1.AuditEventSpec{
			Timestamp:    metav1.NewTime(now),
			PipelineName: "other-pipeline",
			BundleName:   "other-v1",
			Environment:  "test",
			Action:       "PromotionStarted",
			Outcome:      "Pending",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ae1, ae2).Build()

	var buf bytes.Buffer
	require.NoError(t, getAuditEventsFn(&buf, c, "default", "nginx-demo", "", "", 20))

	output := buf.String()
	assert.Contains(t, output, "nginx-demo")
	assert.NotContains(t, output, "other-pipeline",
		"--pipeline filter must exclude other-pipeline events")
}

// TestGetAuditEventsFn_Empty verifies the empty state message.
func TestGetAuditEventsFn_Empty(t *testing.T) {
	scheme := buildGetAuditEventsScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	var buf bytes.Buffer
	require.NoError(t, getAuditEventsFn(&buf, c, "default", "", "", "", 20))
	assert.Contains(t, buf.String(), "No audit events found")
}

// TestGetAuditEventsFn_Limit verifies --limit truncates results.
func TestGetAuditEventsFn_Limit(t *testing.T) {
	scheme := buildGetAuditEventsScheme(t)
	now := time.Now().UTC()

	var objs []v1alpha1.AuditEvent
	for i := 0; i < 5; i++ {
		objs = append(objs, v1alpha1.AuditEvent{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "evt-" + string(rune('a'+i)),
				Namespace: "default",
			},
			Spec: v1alpha1.AuditEventSpec{
				Timestamp:    metav1.NewTime(now.Add(time.Duration(i) * time.Minute)),
				PipelineName: "nginx",
				BundleName:   "v1",
				Environment:  "test",
				Action:       "PromotionStarted",
				Outcome:      "Pending",
			},
		})
	}

	clientObjs := make([]runtime.Object, len(objs))
	for i := range objs {
		clientObjs[i] = &objs[i]
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&objs[0], &objs[1], &objs[2], &objs[3], &objs[4],
	).Build()

	var buf bytes.Buffer
	require.NoError(t, getAuditEventsFn(&buf, c, "default", "", "", "", 2))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	// 1 header + 2 data rows = 3 lines
	assert.Len(t, lines, 3, "--limit 2 must produce header + 2 rows")
}
