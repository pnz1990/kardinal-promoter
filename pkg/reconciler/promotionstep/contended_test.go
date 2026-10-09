// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/events"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// contendedGit clones a checkout with the environment's kustomization and
// refuses every push as non-fast-forward: another writer always wins.
type contendedGit struct {
	mockGit
	pushes int
}

func (m *contendedGit) Clone(_ context.Context, _, _, dir, _ string) error {
	p := filepath.Join(dir, "environments", "test", "kustomization.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte("images:\n- name: ghcr.io/nginx/nginx\n  newTag: \"1.0\"\n"), 0o600)
}

func (m *contendedGit) Push(context.Context, string, string, string, string, bool) error {
	m.pushes++
	return fmt.Errorf("git push: %w", scm.ErrNonFastForward)
}

// TestStepRetry_ContendedBranchIsTransient (#1504 QA): an auto environment
// whose base branch never stops moving runs out of sequence restarts in every
// reconcile. That is not a failure: across 8 reconciles the step stays
// Promoting, status.contendedRetries counts them and status.retryCount stays
// at 0 (the first reconcile reaches git-push, which resets it, also when
// earlier errors had used up 5 of the 5 retries; after that the index never
// moves, so before #1504's fix the count ran out on the sixth), and
// each requeue is a jittered backoff that never exceeds the 2 minute
// maximum, without sleeping in the reconcile.
func TestStepRetry_ContendedBranchIsTransient(t *testing.T) {
	for _, initial := range []int{0, 5} {
		t.Run(fmt.Sprintf("retryCount %d", initial), func(t *testing.T) {
			ps := asPromoting(labelled(makeStep("step", "p", "b1", "test")), makePipeline("p"))
			ps.Status.RetryCount = initial
			c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
			git := &contendedGit{}
			now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
			r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{open: true}, GitClient: git,
				Recorder:  events.NewFakeRecorder(100),
				NowFn:     func() time.Time { return now },
				WorkDirFn: func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}
			for i := 1; i <= 8; i++ {
				start := time.Now()
				res, err := r.Reconcile(context.Background(), reqFor("step"))
				require.NoError(t, err)
				assert.Less(t, time.Since(start), 5*time.Second, "no sleep in the reconcile")
				got := getStep(t, c, "step")
				require.Equal(t, "Promoting", got.Status.State, "reconcile %d: %s", i, got.Status.Message)
				assert.Contains(t, got.Status.Message, "gave up after 3 restarts in this reconcile")
				assert.Contains(t, got.Status.Message, "no limit while other writers keep moving the branch")
				assert.Equal(t, i, got.Status.ContendedRetries)
				assert.Zero(t, got.Status.RetryCount, "contention does not use up retries")
				assert.Equal(t, 4*i, git.pushes, "one push per run of the sequence")
				assert.Positive(t, res.RequeueAfter)
				assert.LessOrEqual(t, res.RequeueAfter, 2*time.Minute, "capped at the maximum backoff")
				require.NotNil(t, got.Status.NextRetryAt)
				now = got.Status.NextRetryAt.Add(time.Second) // the backoff has passed
			}
		})
	}
}
