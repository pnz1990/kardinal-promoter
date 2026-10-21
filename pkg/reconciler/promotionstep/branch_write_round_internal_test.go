// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// commitOf is env's commit on the branch: the newest whose message promotes
// the Bundle to env.
func (h *writeHost) commitOf(env string) string {
	for _, cm := range h.remoteLog() {
		if strings.HasPrefix(cm.Message, "[kardinal] Promote "+writeBundle+" to "+env+"\n") {
			return cm.Hash.String()
		}
	}
	return ""
}

// pending is how many of bw's requests have no answer.
func (h *writeHost) pending(c client.Client) int {
	return len(pendingRequests(h.get(c)))
}

// TestBranchWrite_HeldMidRound (#2405 review): a pause, a freeze or a hold
// of an environment that comes after the steps asked, while the round is
// writing, stops the write as it stops a step before its next git step: the
// round checks again right before its push, and a held member is not pushed
// and not answered (it stays pending, and the BranchWrite looks again at
// requeuePaused). While the hold lasts, no round starts (no clone, no new
// round number); once it ends, the held members are written.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_HeldMidRound(t *testing.T) {
	ctx := context.Background()
	pauseGate := func() client.Object {
		return &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: lifecycle.FreezeGateName("app"), Namespace: writeNS,
			Labels: map[string]string{lifecycle.LabelFreeze: "true"}}}
	}
	freeze := func() client.Object {
		return &v1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: "inc-1"},
			Spec: v1alpha1.ChangeWindowSpec{Reason: "INC-1", Freeze: &v1alpha1.ChangeWindowFreeze{Actors: v1alpha1.FreezeEveryone, CreatedBy: "alice"}}}
	}
	for _, tc := range []struct {
		name string
		// hold holds in c, and lift ends the hold.
		hold, lift func(t *testing.T, c client.Client)
		// written are the environments written while the hold lasts.
		written []string
	}{
		{name: "a pause",
			hold: func(t *testing.T, c client.Client) { require.NoError(t, c.Create(ctx, pauseGate())) },
			lift: func(t *testing.T, c client.Client) { require.NoError(t, c.Delete(ctx, pauseGate())) }},
		{name: "a freeze",
			hold: func(t *testing.T, c client.Client) { require.NoError(t, c.Create(ctx, freeze())) },
			lift: func(t *testing.T, c client.Client) { require.NoError(t, c.Delete(ctx, freeze())) }},
		{name: "a hold of uat on another bundle",
			hold: func(t *testing.T, c client.Client) {
				var p v1alpha1.Pipeline
				require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: writeNS, Name: "app"}, &p))
				p.Spec.Holds = []v1alpha1.EnvironmentHold{{Environment: "uat", Bundle: "app-v1", Reason: "rollback"}}
				require.NoError(t, c.Update(ctx, &p))
			},
			lift: func(t *testing.T, c client.Client) {
				var p v1alpha1.Pipeline
				require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: writeNS, Name: "app"}, &p))
				p.Spec.Holds = nil
				require.NoError(t, c.Update(ctx, &p))
			},
			written: []string{"test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWriteHost(t, "test", "uat")
			c := h.client(interceptor.Funcs{})
			git := &countingGit{GitClient: scm.NewGoGitClient(), afterFirstClone: func() { tc.hold(t, c) }}
			w := h.reconciler(c, git)
			res, err := h.reconcile(w)
			require.NoError(t, err)
			assert.Equal(t, requeuePaused, res.RequeueAfter, "it looks again when a step would")
			bw := h.get(c)
			assert.Nil(t, bw.Status.InFlight)
			assert.EqualValues(t, len(tc.written), git.pushes.Load(), "pushed only if a member is not held")
			assert.Len(t, h.remoteLog(), 1+len(tc.written))
			for _, e := range tc.written {
				assert.Equal(t, h.commitOf(e), bw.Status.Environments[e].CommitSHA, "%s: written", e)
			}
			for _, e := range []string{"test", "uat"} {
				if !slices.Contains(tc.written, e) {
					assert.NotContains(t, bw.Status.Environments, e, "%s: held, not answered", e)
					assert.Equal(t, "v1", h.tagOf(e), "%s: not pushed", e)
				}
			}
			assert.Equal(t, 2-len(tc.written), h.pending(c), "the held members stay pending")

			clones, round := git.clones.Load(), bw.Status.Round
			res, err = h.reconcile(w)
			require.NoError(t, err)
			assert.Equal(t, requeuePaused, res.RequeueAfter)
			assert.Equal(t, clones, git.clones.Load(), "no round while the hold lasts")
			assert.Equal(t, round, h.get(c).Status.Round, "no new round number")

			tc.lift(t, c)
			_, err = h.reconcile(w)
			require.NoError(t, err)
			bw = h.get(c)
			assert.Zero(t, h.pending(c), bw.Status.Message)
			for _, e := range []string{"test", "uat"} {
				assert.Equal(t, "v2", h.tagOf(e))
				assert.Equal(t, h.commitOf(e), bw.Status.Environments[e].CommitSHA, "%s: its own commit", e)
			}
		})
	}
}

// TestBranchWrite_FailedReadHolds (#2405 review): a read that fails holds,
// as the step's freeze check does (a kill switch fails closed): a pause gate
// or ChangeWindow list that cannot be read right before the push holds every
// member, and a transient error reading one member's step holds that member,
// not cancels it. Nothing held is pushed or answered; it stays pending.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_FailedReadHolds(t *testing.T) {
	down := errors.New("etcdserver: request timed out")
	for _, tc := range []struct {
		name string
		// fails reports whether a read of key (kind/name) fails.
		fails   func(key string) bool
		written []string
	}{
		{name: "the pause gate", fails: func(k string) bool { return k == "PolicyGate/"+lifecycle.FreezeGateName("app") }},
		{name: "the freeze list", fails: func(k string) bool { return k == "ChangeWindowList" }},
		{name: "a member's step", fails: func(k string) bool { return k == "PromotionStep/app-"+writeBundle+"-uat" },
			written: []string{"test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWriteHost(t, "test", "uat")
			var failing atomic.Bool
			c := h.client(interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if failing.Load() && tc.fails(kindOf(c, obj)+"/"+key.Name) {
						return down
					}
					return c.Get(ctx, key, obj, opts...)
				},
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if failing.Load() && tc.fails(kindOf(c, list)) {
						return down
					}
					return c.List(ctx, list, opts...)
				},
			})
			// The reads fail from after the round's clone: at the check
			// right before the push.
			git := &countingGit{GitClient: scm.NewGoGitClient(), afterFirstClone: func() { failing.Store(true) }}
			res, err := h.reconcile(h.reconciler(c, git))
			require.NoError(t, err)
			assert.Equal(t, requeuePaused, res.RequeueAfter)
			failing.Store(false)
			bw := h.get(c)
			assert.Nil(t, bw.Status.InFlight)
			assert.EqualValues(t, len(tc.written), git.pushes.Load())
			for _, e := range []string{"test", "uat"} {
				if slices.Contains(tc.written, e) {
					assert.Equal(t, h.commitOf(e), bw.Status.Environments[e].CommitSHA, "%s: written", e)
					continue
				}
				assert.NotContains(t, bw.Status.Environments, e, "%s: held, not answered (not cancelled)", e)
				assert.Equal(t, "v1", h.tagOf(e), "%s: not pushed", e)
			}
			assert.Equal(t, 2-len(tc.written), h.pending(c))
		})
	}
}

// kindOf is obj's kind in c's scheme.
func kindOf(c client.Client, obj runtime.Object) string {
	gvk, err := c.GroupVersionKindFor(obj)
	if err != nil {
		return ""
	}
	return gvk.Kind
}

// TestBranchWrite_MemberGoneMidRound (#2405 review): a member's step that
// is deleted, or deleted and made again, while the round is writing is
// dropped from the round right before its push, answered cancelled; the
// others are written again without it, from a fresh clone, and pushed.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_MemberGoneMidRound(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, want string
		again      bool
	}{
		{name: "deleted", want: "is gone"},
		{name: "made again", want: "was made again", again: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWriteHost(t, "test", "uat")
			c := h.client(interceptor.Funcs{})
			git := &countingGit{GitClient: scm.NewGoGitClient(), afterFirstClone: func() {
				var ps v1alpha1.PromotionStep
				key := client.ObjectKey{Namespace: writeNS, Name: "app-" + writeBundle + "-uat"}
				require.NoError(t, c.Get(ctx, key, &ps))
				require.NoError(t, c.Delete(ctx, &ps))
				if tc.again {
					ps.ResourceVersion, ps.UID = "", types.UID("uid-uat-2")
					require.NoError(t, c.Create(ctx, &ps))
				}
			}}
			_, err := h.reconcile(h.reconciler(c, git))
			require.NoError(t, err)
			bw := h.get(c)
			assert.Equal(t, h.commitOf("test"), bw.Status.Environments["test"].CommitSHA, "test is written")
			assert.Empty(t, bw.Status.Environments["test"].Error)
			uat := bw.Status.Environments["uat"]
			assert.True(t, uat.Cancelled, "uat: %+v", uat)
			assert.Contains(t, uat.Error, tc.want)
			assert.Empty(t, uat.CommitSHA)
			assert.Equal(t, "v1", h.tagOf("uat"), "uat's change is not pushed")
			assert.Len(t, h.remoteLog(), 2, "the seed and test's commit")
			assert.EqualValues(t, 2, git.clones.Load(), "written again without uat")
			assert.EqualValues(t, 1, git.pushes.Load())
		})
	}
}

// TestBranchWrite_PushErrorAfterLanding (#2405 review): a push that landed
// while git returned an error records each member's own commit, found on the
// branch among the commits the round recorded; a push that did not land
// answers the error. When the branch cannot be read either, the round stays
// in flight, with its commits, and the next reconcile records them from the
// branch.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_PushErrorAfterLanding(t *testing.T) {
	reset := errors.New("connection reset by peer")
	t.Run("landed", func(t *testing.T) {
		h := newWriteHost(t, "test", "uat")
		c := h.client(interceptor.Funcs{})
		git := &countingGit{GitClient: scm.NewGoGitClient(), afterPush: func(err error) error {
			require.NoError(t, err)
			return reset
		}}
		_, err := h.reconcile(h.reconciler(c, git))
		require.NoError(t, err)
		bw := h.get(c)
		assert.Nil(t, bw.Status.InFlight)
		for _, e := range h.envs {
			res := bw.Status.Environments[e]
			assert.Empty(t, res.Error, e)
			assert.Equal(t, h.commitOf(e), res.CommitSHA, "%s: its own commit, not the head the round started from", e)
		}
		assert.EqualValues(t, 1, git.pushes.Load())
	})
	t.Run("not landed", func(t *testing.T) {
		h := newWriteHost(t, "test", "uat")
		c := h.client(interceptor.Funcs{})
		git := &countingGit{GitClient: failingPush{scm.NewGoGitClient(), reset}}
		_, err := h.reconcile(h.reconciler(c, git))
		require.NoError(t, err)
		bw := h.get(c)
		for _, e := range h.envs {
			assert.Contains(t, bw.Status.Environments[e].Error, "connection reset", e)
			assert.Empty(t, bw.Status.Environments[e].CommitSHA, e)
		}
		assert.Len(t, h.remoteLog(), 1)
	})
	t.Run("the branch cannot be read", func(t *testing.T) {
		h := newWriteHost(t, "test", "uat")
		c := h.client(interceptor.Funcs{})
		git := &countingGit{GitClient: scm.NewGoGitClient(), historyErr: errors.New("the host is down"),
			afterPush: func(err error) error {
				require.NoError(t, err)
				return reset
			}}
		_, err := h.reconcile(h.reconciler(c, git))
		require.Error(t, err)
		in := h.get(c).Status.InFlight
		require.NotNil(t, in, "left in flight")
		require.NotNil(t, in.PushIntent)
		assert.Len(t, in.Commits, 2)
		assert.Empty(t, h.get(c).Status.Environments)

		again := &countingGit{GitClient: scm.NewGoGitClient()}
		_, err = h.reconcile(h.reconciler(c, again))
		require.NoError(t, err)
		bw := h.get(c)
		assert.Contains(t, bw.Status.Message, "2 of its 2 commit(s) found")
		assert.Zero(t, again.pushes.Load(), "nothing pushed twice")
		for _, e := range h.envs {
			assert.Equal(t, h.commitOf(e), bw.Status.Environments[e].CommitSHA, e)
		}
	})
}

// failingPush is a git client whose push fails before it reaches the remote.
type failingPush struct {
	scm.GitClient
	err error
}

func (g failingPush) Push(context.Context, string, string, string, scm.GitAuth, bool) error {
	return g.err
}

func (g failingPush) HeadCommit(ctx context.Context, dir string) (string, error) {
	return g.GitClient.(scm.HeadCommitReader).HeadCommit(ctx, dir)
}

func (g failingPush) CloneOrInit(ctx context.Context, url, branch, dir string, auth scm.GitAuth, depth int) (bool, error) {
	return g.GitClient.(scm.BranchCloner).CloneOrInit(ctx, url, branch, dir, auth, depth)
}

func (g failingPush) CommitMessages(ctx context.Context, dir string, limit int) ([]scm.CommitMessage, error) {
	return g.GitClient.(scm.HistoryReader).CommitMessages(ctx, dir, limit)
}

// TestBranchWrite_SpecChangesMidRound (#2405 review): the Graph rewrites
// spec.requested when another step asks, while a round is writing; the
// round's status writes (its push intent, its results) are merge patches of
// the status, not locked on the object, so they do not conflict with it.
// The new request is written in the next round.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_SpecChangesMidRound(t *testing.T) {
	ctx := context.Background()
	h := newWriteHost(t, "test", "uat")
	c := h.client(interceptor.Funcs{})
	git := &countingGit{GitClient: scm.NewGoGitClient(), afterFirstClone: func() {
		bw := h.get(c)
		bw.Spec.Requested[1].Request = 2 // uat asks again
		bw.Labels = map[string]string{"kro.run/owned": "true"}
		require.NoError(t, c.Update(ctx, bw))
	}}
	w := h.reconciler(c, git)
	_, err := h.reconcile(w)
	require.NoError(t, err)
	bw := h.get(c)
	assert.Nil(t, bw.Status.InFlight)
	for _, e := range h.envs {
		assert.Empty(t, bw.Status.Environments[e].Error, e)
		assert.Equal(t, h.commitOf(e), bw.Status.Environments[e].CommitSHA, e)
		assert.EqualValues(t, 1, bw.Status.Environments[e].Request, e)
	}
	assert.Equal(t, 1, h.pending(c), "uat's second request")
	_, err = h.reconcile(w)
	require.NoError(t, err)
	uat := h.get(c).Status.Environments["uat"]
	assert.EqualValues(t, 2, uat.Request)
	assert.True(t, uat.NoChanges)
}

// TestBranchWrite_ResumeTrustsOnlyItsCommits (#2405 review): a round found in
// flight after its push intent counts as written only the commits it
// recorded, on the branch. A commit of anyone else with the round's message
// and trailer is not one of them: the member is written again.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_ResumeTrustsOnlyItsCommits(t *testing.T) {
	ctx := context.Background()
	h := newWriteHost(t, "test")
	forger := filepath.Join(t.TempDir(), "forger")
	g := scm.NewGoGitClient()
	require.NoError(t, g.Clone(ctx, "file://"+h.remote, "main", forger, scm.GitAuth{}))
	require.NoError(t, os.WriteFile(filepath.Join(forger, "README.md"), []byte("not a promotion\n"), 0o644))
	require.NoError(t, g.CommitAll(ctx, forger, "[kardinal] Promote app-v2 to test\n\n"+writeTrailer+writeBW+"/1", "x", "x@example.com"))
	require.NoError(t, g.Push(ctx, forger, "origin", "main", scm.GitAuth{}, false))
	forged := h.remoteLog()[0].Hash.String()

	now := metav1.NewTime(time.Now())
	h.bw.Status = v1alpha1.BranchWriteStatus{Round: 1, InFlight: &v1alpha1.BranchWriteRound{Round: 1,
		Members:    []v1alpha1.BranchWriteMember{{Environment: "test", Step: "app-" + writeBundle + "-test", UID: "uid-test", Request: 1}},
		PushIntent: &now, Commits: map[string]string{"test": "0123456789abcdef0123456789abcdef01234567"}}}
	c := h.client(interceptor.Funcs{})
	git := &countingGit{GitClient: scm.NewGoGitClient()}
	_, err := h.reconcile(h.reconciler(c, git))
	require.NoError(t, err)
	bw := h.get(c)
	res := bw.Status.Environments["test"]
	assert.NotEqual(t, forged, res.CommitSHA, "the look-alike is not the round's")
	assert.Equal(t, h.commitOf("test"), res.CommitSHA)
	assert.EqualValues(t, 2, res.Round, "written again")
	assert.Equal(t, "v2", h.tagOf("test"))
	assert.EqualValues(t, 1, git.pushes.Load())
}

// TestBranchWrite_SharedPath (#2405 review): of two members that write one
// path (a PathConflict), the second goes to the next round, on the branch
// with the first one's change, so its answer names a commit that has its
// version.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_SharedPath(t *testing.T) {
	h := newWriteHost(t, "test", "uat")
	for _, o := range h.objs {
		if p, ok := o.(*v1alpha1.Pipeline); ok {
			p.Spec.Environments[1].Path = "./environments/test/"
		}
	}
	c := h.client(interceptor.Funcs{})
	git := &countingGit{GitClient: scm.NewGoGitClient()}
	w := h.reconciler(c, git)
	res, err := h.reconcile(w)
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter, "the deferred member is written next")
	assert.Less(t, res.RequeueAfter, time.Second)
	bw := h.get(c)
	assert.Equal(t, h.commitOf("test"), bw.Status.Environments["test"].CommitSHA)
	assert.NotContains(t, bw.Status.Environments, "uat", "uat waits for the next round")
	_, err = h.reconcile(w)
	require.NoError(t, err)
	uat := h.get(c).Status.Environments["uat"]
	assert.True(t, uat.NoChanges, "%+v", uat)
	assert.Equal(t, h.commitOf("test"), uat.CommitSHA, "the commit that has its version")
	assert.EqualValues(t, 2, uat.Round)
	assert.Len(t, h.remoteLog(), 2)
}

// TestBranchWrite_RefusesAnotherRepository (#2405 review): a BranchWrite
// whose repository or branch is not its Pipeline's writes nothing with the
// Pipeline's credentials; every member is answered with why.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_RefusesAnotherRepository(t *testing.T) {
	for name, change := range map[string]func(*v1alpha1.BranchWriteSpec){
		"repository": func(s *v1alpha1.BranchWriteSpec) { s.URL = "https://github.com/someone/else" },
		"branch":     func(s *v1alpha1.BranchWriteSpec) { s.Branch = "release" },
	} {
		t.Run(name, func(t *testing.T) {
			h := newWriteHost(t, "test")
			change(&h.bw.Spec)
			c := h.client(interceptor.Funcs{})
			git := &countingGit{GitClient: scm.NewGoGitClient()}
			_, err := h.reconcile(h.reconciler(c, git))
			require.NoError(t, err)
			res := h.get(c).Status.Environments["test"]
			assert.Contains(t, res.Error, "not Pipeline app's")
			assert.Empty(t, res.CommitSHA)
			assert.Zero(t, git.clones.Load(), "nothing cloned")
		})
	}
}

// TestPendingRequests: a request is answered by a result for the same step
// (name and UID) and a request at least as new; any other is pending, in
// spec.environments order.
func TestPendingRequests(t *testing.T) {
	bw := &v1alpha1.BranchWrite{Spec: v1alpha1.BranchWriteSpec{
		Environments: []v1alpha1.BranchWriteEnvironment{{Name: "a", Step: "s-a"}, {Name: "b", Step: "s-b"},
			{Name: "c", Step: "s-c"}, {Name: "d", Step: "s-d"}, {Name: "e", Step: "s-e"}, {Name: "f", Step: "s-f"}},
		Requested: []v1alpha1.BranchWriteRequest{{Step: "s-f", UID: "u", Request: 1}, {Step: "s-a", UID: "u", Request: 1},
			{Step: "s-b", UID: "u", Request: 2}, {Step: "s-c", UID: "u2", Request: 1}, {Step: "s-d", UID: "u", Request: 0},
			{Step: "s-x", UID: "u", Request: 1}},
	}, Status: v1alpha1.BranchWriteStatus{Environments: map[string]v1alpha1.BranchWriteResult{
		"a": {Step: "s-a", UID: "u", Request: 1},
		"b": {Step: "s-b", UID: "u", Request: 1},
		"c": {Step: "s-c", UID: "u", Request: 1},
		"f": {Step: "s-old", UID: "u", Request: 1},
	}}}
	var got []string
	for _, m := range pendingRequests(bw) {
		got = append(got, m.Environment)
	}
	assert.Equal(t, []string{"b", "c", "f"}, got,
		"a answered; b a newer request; c a step made again; d no request; e never asked; f another step's answer")
}
