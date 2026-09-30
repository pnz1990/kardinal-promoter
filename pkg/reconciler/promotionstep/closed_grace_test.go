// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// TestWaitingForMerge_ClosedGraceWindow covers #1306: a PR closed without
// merging fails the step only once PRStatus marks it final (status.closedFinal,
// or a closed PRStatus without status.closedAt from an older release). While
// the PR is closed inside the grace window the step keeps waiting and says so,
// and a reopen puts the waiting message back.
func TestWaitingForMerge_ClosedGraceWindow(t *testing.T) {
	const closedMsg = "PR #5 is closed; the step fails 5m0s after closing unless it is reopened"
	closedAt := metav1.NewTime(time.Now().Add(-time.Minute))
	tests := []struct {
		name        string
		status      func(s *v1alpha1.PRStatusStatus)
		stepMessage string
		wantState   string
		wantMessage string
		wantExact   bool
	}{
		{
			name:        "closed inside the grace window keeps waiting",
			status:      func(s *v1alpha1.PRStatusStatus) { s.Open, s.ClosedAt = false, &closedAt },
			wantState:   "WaitingForMerge",
			wantMessage: closedMsg,
			wantExact:   true,
		},
		{
			name: "closed after the grace window fails",
			status: func(s *v1alpha1.PRStatusStatus) {
				s.Open, s.ClosedAt, s.ClosedFinal = false, &closedAt, true
			},
			wantState:   "Failed",
			wantMessage: "PR #5 was closed without merging and not reopened within 5m0s",
			wantExact:   true,
		},
		{
			name:        "closed without closedAt from an older release fails",
			status:      func(s *v1alpha1.PRStatusStatus) { s.Open = false },
			wantState:   "Failed",
			wantMessage: "PR #5 was closed without merging",
			wantExact:   true,
		},
		{
			name:        "reopened inside the window restores the waiting message",
			status:      func(s *v1alpha1.PRStatusStatus) { s.Open = true },
			stepMessage: closedMsg,
			wantState:   "WaitingForMerge",
			wantMessage: "PR #5 is open, waiting for merge",
			wantExact:   true,
		},
		{
			name: "a permanent poll error inside the window fails",
			status: func(s *v1alpha1.PRStatusStatus) {
				s.Open, s.ClosedAt, s.PollError = false, &closedAt, "HTTP 401"
			},
			wantState:   "Failed",
			wantMessage: "cannot be polled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prs := openPRStatus("prstatus-step-wfm", "test/repo", 5)
			tt.status(&prs.Status)
			step := makeStep("step-wfm", "nginx-demo", "bundle-1", "prod")
			step.Spec.PRStatusRef = prs.Name
			step.Status.State = "WaitingForMerge"
			step.Status.Message = tt.stepMessage
			step.Status.PRURL = "https://github.com/test/repo/pull/5"
			step.Status.Outputs = map[string]string{"prURL": step.Status.PRURL, "prNumber": "5"}
			c := newClient(t, step, prs, makePipeline("nginx-demo"), makeBundle("bundle-1", "nginx-demo"))
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{}, GitClient: &mockGit{},
				WorkDirFn: func(_, _ string) string { return t.TempDir() }}

			res, err := r.Reconcile(context.Background(), reqFor("step-wfm"))
			require.NoError(t, err)
			got := getStep(t, c, "step-wfm")
			assert.Equal(t, tt.wantState, got.Status.State)
			if tt.wantExact {
				assert.Equal(t, tt.wantMessage, got.Status.Message)
			} else {
				assert.Contains(t, got.Status.Message, tt.wantMessage)
			}
			if tt.wantState != "WaitingForMerge" {
				return
			}
			assert.Greater(t, res.RequeueAfter, time.Duration(0), "a waiting step is requeued")

			// A second reconcile with nothing new does not patch the step:
			// every patch is a watch event.
			_, err = r.Reconcile(context.Background(), reqFor("step-wfm"))
			require.NoError(t, err)
			again := getStep(t, c, "step-wfm")
			assert.Equal(t, got.ResourceVersion, again.ResourceVersion, "no patch when nothing changed")
		})
	}
}
