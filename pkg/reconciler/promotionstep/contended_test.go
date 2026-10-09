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
// whose base branch keeps moving runs out of sequence restarts in one
// reconcile. That is not a failure: the step stays Promoting, its retry
// count goes up and it is requeued after a jittered backoff (between half
// and one and a half of the usual delay), without sleeping in the
// reconcile. Each attempt gets as far as git-push, which is progress, so the
// count starts over: contention alone never fails the step, also when
// earlier errors had used up the retries.
func TestStepRetry_ContendedBranchIsTransient(t *testing.T) {
	for _, tt := range []struct {
		retryCount int
		wantState  string
	}{{0, "Promoting"}, {5, "Promoting"}} {
		ps := asPromoting(labelled(makeStep("step", "p", "b1", "test")), makePipeline("p"))
		ps.Status.RetryCount = tt.retryCount
		c := newClient(t, ps, makePipeline("p"), makeBundle("b1", "p"))
		git := &contendedGit{}
		r := &promotionstep.Reconciler{Client: c, SCM: &mockSCM{open: true}, GitClient: git,
			Recorder:  events.NewFakeRecorder(20),
			WorkDirFn: func(_, _ string) string { return filepath.Join(t.TempDir(), "w") }}
		start := time.Now()
		res, err := r.Reconcile(context.Background(), reqFor("step"))
		require.NoError(t, err)
		assert.Less(t, time.Since(start), 5*time.Second, "no sleep in the reconcile")
		got := getStep(t, c, "step")
		assert.Equal(t, tt.wantState, got.Status.State, got.Status.Message)
		assert.Contains(t, got.Status.Message, "gave up after 3 restarts in this reconcile")
		assert.Equal(t, 4, git.pushes, "one push per run of the sequence")
		if tt.wantState == "Promoting" {
			assert.Equal(t, 1, got.Status.RetryCount, "progress to git-push restarts the count")
			assert.Contains(t, got.Status.Message, "retrying in")
			assert.GreaterOrEqual(t, res.RequeueAfter, 5*time.Second)
			assert.Less(t, res.RequeueAfter, 15*time.Second)
		}
	}
}
