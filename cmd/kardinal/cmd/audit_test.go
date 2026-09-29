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
