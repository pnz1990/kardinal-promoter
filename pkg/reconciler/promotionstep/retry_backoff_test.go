// Copyright 2026 The kardinal-promoter Authors.
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

package promotionstep_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// pastBackoff returns a NowFn whose every call is three minutes after the
// one before, past the longest retry delay (2m), so each reconcile of a step
// that retries runs the retry instead of waiting for status.nextRetryAt.
func pastBackoff() func() time.Time {
	var mu sync.Mutex
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(3 * time.Minute)
		return now
	}
}

// flakyGit is a git client whose clone fails with a transient error while
// fail is set. It counts the clones.
type flakyGit struct {
	mockGit
	fail   bool
	clones int
}

func (g *flakyGit) Clone(_ context.Context, _, _, _ string, _ scm.GitAuth) error {
	g.clones++
	if g.fail {
		return errors.New("git clone: 503 Service Unavailable")
	}
	return nil
}

// TestRetryBackoff_HoldsWhenWokenEarly covers B87: a step that failed with a
// retryable error ran the retry on its next reconcile, however soon, not after
// the delay its message gave. A PolicyGate re-evaluated every 10s woke the
// step at each of its status writes (policyGateMapper), so the five retries
// ran in about 40s instead of 4.5 minutes and the step failed. The step now
// waits for status.nextRetryAt: a reconcile before it does not run the step
// or write its status, and requeues for the rest of the wait.
func TestRetryBackoff_HoldsWhenWokenEarly(t *testing.T) {
	delays := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 2 * time.Minute}
	for _, succeeds := range []bool{false, true} {
		name := "gives up after the whole backoff"
		if succeeds {
			name = "a retry that succeeds clears nextRetryAt"
		}
		t.Run(name, func(t *testing.T) {
			pipeline := makePipeline("nginx-demo")
			ps := asPromoting(makeStep("step-retry", "nginx-demo", "b1", "test"), pipeline)
			c := fake.NewClientBuilder().WithScheme(buildScheme(t)).
				WithStatusSubresource(&v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}).
				WithObjects(ps, pipeline, makeBundle("b1", "nginx-demo")).Build()
			git := &flakyGit{fail: true}
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			now := start
			workDir := filepath.Join(t.TempDir(), "w")
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: git,
				Recorder: events.NewFakeRecorder(50), WorkDirFn: func(_, _ string) string { return workDir },
				NowFn: func() time.Time { return now }}
			reconcile := func() time.Duration {
				t.Helper()
				res, err := r.Reconcile(context.Background(), reqFor("step-retry"))
				require.NoError(t, err)
				return res.RequeueAfter
			}

			for i, delay := range delays {
				require.Equal(t, delay, reconcile(), "retry %d requeues after its delay", i+1)
				require.Equal(t, i+1, git.clones)
				got := getStep(t, c, "step-retry")
				require.Equal(t, "Promoting", got.Status.State, got.Status.Message)
				require.Equal(t, i+1, got.Status.RetryCount)
				require.NotNil(t, got.Status.NextRetryAt)
				require.True(t, got.Status.NextRetryAt.Time.Equal(now.Add(delay)),
					"nextRetryAt %s, want %s", got.Status.NextRetryAt.Time, now.Add(delay))
				assert.Contains(t, got.Status.Message, "retrying in "+delay.String())

				// Woken every 5s during the wait (a gate re-evaluated, a
				// PRStatus written, a restart): the step does not run.
				due := now.Add(delay)
				for now = now.Add(5 * time.Second); now.Before(due); now = now.Add(5 * time.Second) {
					assert.Equal(t, due.Sub(now), reconcile(), "a reconcile at %s requeues for the rest", now.Sub(start))
					after := getStep(t, c, "step-retry")
					assert.Equal(t, got.ResourceVersion, after.ResourceVersion, "a reconcile before nextRetryAt writes nothing")
				}
				require.Equal(t, i+1, git.clones, "no clone before nextRetryAt")
				now = due
				if succeeds && i == 2 {
					break
				}
			}

			if !succeeds {
				reconcile()
				got := getStep(t, c, "step-retry")
				assert.Equal(t, "Failed", got.Status.State)
				assert.Contains(t, got.Status.Message, "gave up after 5 retries")
				assert.Equal(t, 6, git.clones)
				assert.Equal(t, 10+20+40+80+120, int(now.Sub(start).Seconds()), "the step fails after the whole backoff")
				return
			}
			git.fail = false
			reconcile()
			got := getStep(t, c, "step-retry")
			assert.Equal(t, "HealthChecking", got.Status.State, got.Status.Message)
			assert.Nil(t, got.Status.NextRetryAt)
			assert.Zero(t, got.Status.RetryCount)
			assert.Equal(t, 4, git.clones)
		})
	}
}

// TestSupersession_CloseRetryWaitsForBackoff covers B89: a superseded step
// whose PR close failed ran the close again at its next reconcile, however
// soon, as B87 did for a step retry. Its gates, its PRStatus and its Bundle
// wake it during the backoff, so an SCM outage of a minute used up the five
// retries and the step failed, "close it by hand". A close retry now waits
// for status.nextRetryAt.
func TestSupersession_CloseRetryWaitsForBackoff(t *testing.T) {
	delays := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 2 * time.Minute}
	for _, succeeds := range []bool{false, true} {
		name := "gives up after the whole backoff"
		if succeeds {
			name = "a close that succeeds clears the condition"
		}
		t.Run(name, func(t *testing.T) {
			ps := labelled(makeStep("step", "p", "b1", "prod"))
			ps.Status.State = "WaitingForMerge"
			prs := openPRStatus("prs", "org/repo", 42)
			ps.Spec.PRStatusRef = prs.Name
			bundle := makeBundle("b1", "p")
			bundle.Status.Phase = "Superseded"
			c := newClient(t, ps, makePipeline("p"), bundle, prs)
			closeErrs := make([]error, len(delays)+1)
			for i := range closeErrs {
				closeErrs[i] = errors.New("close PR: HTTP 502")
			}
			if succeeds {
				closeErrs = closeErrs[:3]
			}
			m := &mockSCM{open: true, closeErrs: closeErrs}
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			now := start
			r := &promotionstep.Reconciler{Client: c, SCM: m, GitClient: &mockGit{},
				NowFn: func() time.Time { return now }}
			reconcile := func() time.Duration {
				t.Helper()
				res, err := r.Reconcile(context.Background(), reqFor("step"))
				require.NoError(t, err)
				return res.RequeueAfter
			}

			for i, delay := range delays {
				require.Equal(t, delay, reconcile(), "close retry %d requeues after its delay", i+1)
				require.Len(t, m.closed, i+1)
				got := getStep(t, c, "step")
				require.Equal(t, "WaitingForMerge", got.Status.State, got.Status.Message)
				require.Equal(t, i+1, got.Status.RetryCount)
				require.NotNil(t, got.Status.NextRetryAt)
				require.True(t, got.Status.NextRetryAt.Time.Equal(now.Add(delay)),
					"nextRetryAt %s, want %s", got.Status.NextRetryAt.Time, now.Add(delay))
				assert.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, promotionstep.ConditionSupersededCloseFailed))
				assert.Contains(t, got.Status.Message, "retrying in "+delay.String())

				// Woken every 5s during the wait: the close is not retried.
				due := now.Add(delay)
				for now = now.Add(5 * time.Second); now.Before(due); now = now.Add(5 * time.Second) {
					assert.Equal(t, due.Sub(now), reconcile(), "a reconcile at %s requeues for the rest", now.Sub(start))
					after := getStep(t, c, "step")
					assert.Equal(t, got.ResourceVersion, after.ResourceVersion, "a reconcile before nextRetryAt writes nothing")
				}
				require.Len(t, m.closed, i+1, "no close before nextRetryAt")
				now = due
				if succeeds && i == 2 {
					break
				}
			}

			reconcile()
			got := getStep(t, c, "step")
			assert.Equal(t, "Failed", got.Status.State, got.Status.Message)
			assert.Nil(t, got.Status.NextRetryAt)
			if !succeeds {
				assert.Contains(t, got.Status.Message, "closing its PR failed after 5 retries")
				assert.Len(t, m.closed, 6)
				assert.True(t, meta.IsStatusConditionTrue(got.Status.Conditions, promotionstep.ConditionSupersededCloseFailed))
				assert.Equal(t, 10+20+40+80+120, int(now.Sub(start).Seconds()), "the step fails after the whole backoff")
				return
			}
			assert.Contains(t, got.Status.Message, "promotion cancelled")
			assert.NotContains(t, got.Status.Message, "closing its PR failed")
			assert.Len(t, m.closed, 4)
			assert.Nil(t, meta.FindStatusCondition(got.Status.Conditions, promotionstep.ConditionSupersededCloseFailed))
		})
	}
}
