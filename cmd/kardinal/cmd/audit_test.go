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

package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// C09a-cli-17: --since takes a whole number of days or a Go duration, and
// must be positive.
func TestParseSinceDuration(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "7d", want: 7 * 24 * time.Hour},
		{in: "24h", want: 24 * time.Hour},
		{in: "90m", want: 90 * time.Minute},
		{in: "7xd", wantErr: true},
		{in: "1.5d", wantErr: true},
		{in: "-1d", wantErr: true},
		{in: "-24h", wantErr: true},
		{in: "0d", wantErr: true},
		{in: "0s", wantErr: true},
		{in: "", wantErr: true},
		{in: "d", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSinceDuration(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// C09a-cli-17: a promotion that started before the window and succeeded in it
// does not push the success rate over 100%.
func TestAuditSummary_SuccessRateOverCompleted(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	event := func(name, action string, at time.Time) sigs_client.Object {
		return &v1alpha1.AuditEvent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: v1alpha1.AuditEventSpec{
				Timestamp: metav1.NewTime(at), PipelineName: "demo", BundleName: name, Environment: "prod", Action: action,
			},
		}
	}
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(
		event("a-start", "PromotionStarted", now.Add(-2*time.Hour)),
		event("a-done", "PromotionSucceeded", now.Add(-30*time.Minute)),
		event("b-done", "PromotionSucceeded", now.Add(-20*time.Minute)),
		event("c-done", "PromotionFailed", now.Add(-10*time.Minute)),
	).Build()

	var buf bytes.Buffer
	require.NoError(t, auditSummaryFn(&buf, c, "default", "", "1h", now))
	assert.Contains(t, buf.String(), "Promotions:   0 started, 2 succeeded, 1 failed, 0 superseded\n")
	assert.Contains(t, buf.String(), "Success rate: 66.7%\n")

	err := auditSummaryFn(&buf, c, "default", "", "-1d", now)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid --since "-1d": must be positive`)
}

// E2E-R07: every rollback, manual or automatic, creates a rollback Bundle
// (lifecycle.PlanRollback), and the summary counts those created in the
// window. A RollbackStarted event (onHealthFailure=rollback, one per region)
// counts only when its rollback Bundle is gone.
func TestAuditSummary_CountsRollbackBundles(t *testing.T) {
	now := time.Date(2026, 9, 30, 2, 0, 0, 0, time.UTC)
	event := func(name, pipeline, bundle, action string) *v1alpha1.AuditEvent {
		return &v1alpha1.AuditEvent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
				Labels: map[string]string{"kardinal.io/pipeline": pipeline}},
			Spec: v1alpha1.AuditEventSpec{Timestamp: metav1.NewTime(now.Add(-time.Hour)), PipelineName: pipeline,
				BundleName: bundle, Environment: "prod", Action: action},
		}
	}
	rollback := func(name, pipeline, from string, age time.Duration) *v1alpha1.Bundle {
		return &v1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
				CreationTimestamp: metav1.NewTime(now.Add(-age)),
				Labels:            map[string]string{"kardinal.io/rollback": "true", "kardinal.io/pipeline": pipeline},
				Annotations:       map[string]string{"kardinal.io/rollback-from": from}},
			Spec: v1alpha1.BundleSpec{Pipeline: pipeline, Type: "image",
				Intent:     &v1alpha1.BundleIntent{TargetEnvironment: "prod"},
				Provenance: &v1alpha1.BundleProvenance{RollbackOf: "kardinal-test-app-9tptr"}},
		}
	}
	labelOnly := rollback("kardinal-test-app-rollback-label", "kardinal-test-app", "kardinal-test-app-x", time.Hour)
	labelOnly.Spec.Provenance = nil
	started := event("started", "kardinal-test-app", "kardinal-test-app-9smn4", "PromotionStarted")
	rb := "kardinal-test-app-4cqnl-rollback-policy"
	succeededRollback := []sigs_client.Object{
		rollback(rb, "kardinal-test-app", "kardinal-test-app-4cqnl", 30*time.Minute),
		event("rb-started", "kardinal-test-app", rb, "PromotionStarted"),
		event("rb-succeeded", "kardinal-test-app", rb, "PromotionSucceeded"),
		event("rb-rollback-succeeded", "kardinal-test-app", rb, "RollbackSucceeded")}
	cases := []struct {
		name     string
		pipeline string
		objs     []sigs_client.Object
		want     string
	}{
		{name: "manual rollback", want: "Rollbacks:    1 triggered, 0 succeeded\n", objs: []sigs_client.Object{started,
			rollback("kardinal-test-app-rollback-bkgwk", "kardinal-test-app", "kardinal-test-app-4cqnl", 30*time.Minute)}},
		{name: "manual rollback without other events", want: "Rollbacks:    1 triggered, 0 succeeded\n", objs: []sigs_client.Object{
			rollback("kardinal-test-app-rollback-bkgwk", "kardinal-test-app", "kardinal-test-app-4cqnl", 30*time.Minute)}},
		{name: "rollback marked by the label only", want: "Rollbacks:    1 triggered, 0 succeeded\n",
			objs: []sigs_client.Object{started, labelOnly}},
		{name: "automatic rollback in two regions", want: "Rollbacks:    1 triggered, 0 succeeded\n", objs: []sigs_client.Object{
			rollback("kardinal-test-app-9smn4-rollback-alarm", "kardinal-test-app", "kardinal-test-app-9smn4", time.Hour),
			event("rb-east", "kardinal-test-app", "kardinal-test-app-9smn4", "RollbackStarted"),
			event("rb-west", "kardinal-test-app", "kardinal-test-app-9smn4", "RollbackStarted")}},
		{name: "automatic rollback whose bundle was deleted", want: "Rollbacks:    1 triggered, 0 succeeded\n",
			objs: []sigs_client.Object{event("rb-east", "kardinal-test-app", "kardinal-test-app-9smn4", "RollbackStarted")}},
		{name: "rollback before the window", want: "Rollbacks:    0 triggered, 0 succeeded\n", objs: []sigs_client.Object{started,
			rollback("kardinal-test-app-rollback-old", "kardinal-test-app", "kardinal-test-app-4cqnl", 48*time.Hour)}},
		{name: "rollback of another pipeline", pipeline: "kardinal-test-app", want: "Rollbacks:    0 triggered, 0 succeeded\n",
			objs: []sigs_client.Object{started, rollback("other-rollback-x", "other", "other-1", time.Hour)}},
		// B50: a rollback Bundle Verified in prod writes PromotionSucceeded
		// and RollbackSucceeded; the second counts neither as a promotion
		// nor as another rollback.
		{name: "succeeded rollback: one promotion", want: "Promotions:   1 started, 1 succeeded, 0 failed, 0 superseded\n",
			objs: succeededRollback},
		{name: "succeeded rollback: one rollback, succeeded", want: "Rollbacks:    1 triggered, 1 succeeded\n", objs: succeededRollback},
		{name: "rollback verified upstream only: not succeeded", want: "Rollbacks:    1 triggered, 0 succeeded\n",
			objs: []sigs_client.Object{rollback(rb, "kardinal-test-app", "kardinal-test-app-4cqnl", 30*time.Minute),
				func() *v1alpha1.AuditEvent {
					ae := event("rb-test-rollback-succeeded", "kardinal-test-app", rb, "RollbackSucceeded")
					ae.Spec.Environment = "test"
					return ae
				}()}},
		// succeeded counts only the rollbacks triggered counts, so it is
		// never more than triggered.
		{name: "rollback whose bundle was deleted: not counted", want: "Rollbacks:    0 triggered, 0 succeeded\n",
			objs: []sigs_client.Object{started,
				event("rb-test-rollback-succeeded", "kardinal-test-app", rb, "RollbackSucceeded"),
				event("rb-prod-rollback-succeeded", "kardinal-test-app", rb, "RollbackSucceeded")}},
		{name: "rollback created before the window that succeeds in it: not counted",
			want: "Rollbacks:    0 triggered, 0 succeeded\n", objs: []sigs_client.Object{
				rollback(rb, "kardinal-test-app", "kardinal-test-app-4cqnl", 25*time.Hour),
				event("rb-rollback-succeeded", "kardinal-test-app", rb, "RollbackSucceeded")}},
		{name: "not a rollback", want: "Rollbacks:    0 triggered, 0 succeeded\n", objs: []sigs_client.Object{started,
			&v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "kardinal-test-app-9smn4", Namespace: "default",
				CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
				Spec: v1alpha1.BundleSpec{Pipeline: "kardinal-test-app", Type: "image"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(tc.objs...).Build()
			var buf bytes.Buffer
			require.NoError(t, auditSummaryFn(&buf, c, "default", tc.pipeline, "24h", now))
			assert.Contains(t, buf.String(), tc.want)
		})
	}
}
