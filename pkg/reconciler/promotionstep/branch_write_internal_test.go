// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// writeHost is a BranchWrite test's world: a bare repository with one
// kustomization per environment (newTag v1), a batched Pipeline, a Bundle of
// v2, one Promoting step per environment that asked once, and their
// BranchWrite.
type writeHost struct {
	t      *testing.T
	remote string
	envs   []string
	objs   []client.Object
	bw     *v1alpha1.BranchWrite
}

const (
	writeNS     = "team"
	writeBundle = "app-v2"
	writeBW     = "app-v2-write"
)

func newWriteHost(t *testing.T, envs ...string) *writeHost {
	t.Helper()
	h := &writeHost{t: t, envs: envs, remote: filepath.Join(t.TempDir(), "remote.git")}
	_, err := gogit.PlainInit(h.remote, true)
	require.NoError(t, err)
	seed := filepath.Join(t.TempDir(), "seed")
	repo, err := gogit.PlainInit(seed, false)
	require.NoError(t, err)
	for _, e := range envs {
		dir := filepath.Join(seed, "environments", e)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(
			"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nimages:\n  - name: ghcr.io/org/app\n    newTag: v1\n"), 0o644))
	}
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.AddWithOptions(&gogit.AddOptions{All: true}))
	sig := &object.Signature{Name: "seed", Email: "s@example.com", When: time.Now()}
	_, err = wt.Commit("seed", &gogit.CommitOptions{Author: sig, Committer: sig})
	require.NoError(t, err)
	_, err = repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{h.remote}})
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	require.NoError(t, repo.Push(&gogit.PushOptions{RemoteName: "origin",
		RefSpecs: []gitconfig.RefSpec{gitconfig.RefSpec(head.Name().String() + ":refs/heads/main")}}))

	url := "file://" + h.remote
	pipeline := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: writeNS},
		Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: url, Branch: "main", Writes: v1alpha1.WritesBatched}}}
	bundle := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: writeBundle, Namespace: writeNS},
		Spec:   v1alpha1.BundleSpec{Type: "image", Pipeline: "app", Images: []v1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: "v2"}}},
		Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
	h.bw = &v1alpha1.BranchWrite{ObjectMeta: metav1.ObjectMeta{Name: writeBW, Namespace: writeNS},
		Spec: v1alpha1.BranchWriteSpec{PipelineName: "app", BundleName: writeBundle, URL: url, Branch: "main"}}
	h.objs = []client.Object{pipeline, bundle}
	for _, e := range envs {
		pipeline.Spec.Environments = append(pipeline.Spec.Environments, v1alpha1.EnvironmentSpec{Name: e, Approval: "auto"})
		step := &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{Name: "app-" + writeBundle + "-" + e, Namespace: writeNS,
			UID: types.UID("uid-" + e), Labels: map[string]string{"kardinal.io/pipeline": "app", "kardinal.io/environment": e}},
			Spec:   v1alpha1.PromotionStepSpec{PipelineName: "app", BundleName: writeBundle, Environment: e, Write: writeBW},
			Status: v1alpha1.PromotionStepStatus{State: "Promoting", WriteRequest: 1}}
		h.objs = append(h.objs, step)
		h.bw.Spec.Environments = append(h.bw.Spec.Environments, v1alpha1.BranchWriteEnvironment{Name: e, Step: step.Name})
		h.bw.Spec.Requested = append(h.bw.Spec.Requested, v1alpha1.BranchWriteRequest{Step: step.Name, UID: "uid-" + e, Request: 1})
	}
	return h
}

// client builds the fake API server, with funcs intercepting it.
func (h *writeHost) client(funcs interceptor.Funcs) client.Client {
	s := runtime.NewScheme()
	require.NoError(h.t, v1alpha1.AddToScheme(s))
	require.NoError(h.t, appsv1.AddToScheme(s))
	require.NoError(h.t, corev1.AddToScheme(s))
	objs := append(append([]client.Object{}, h.objs...), h.bw)
	return fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&v1alpha1.BranchWrite{}, &v1alpha1.PromotionStep{}, &v1alpha1.Bundle{}, &v1alpha1.Pipeline{}).
		WithObjects(objs...).WithInterceptorFuncs(funcs).Build()
}

// reconciler is a fresh BranchWrite reconciler on c with git: nothing in
// memory from an earlier one.
func (h *writeHost) reconciler(c client.Client, git scm.GitClient) *BranchWriteReconciler {
	steps := &Reconciler{Client: c, GitClient: git, WorkDirRoot: h.t.TempDir()}
	return &BranchWriteReconciler{Client: c, Steps: steps}
}

func (h *writeHost) reconcile(w *BranchWriteReconciler) (ctrl.Result, error) {
	return w.reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: writeNS, Name: writeBW}})
}

func (h *writeHost) get(c client.Client) *v1alpha1.BranchWrite {
	var bw v1alpha1.BranchWrite
	require.NoError(h.t, c.Get(context.Background(), client.ObjectKey{Namespace: writeNS, Name: writeBW}, &bw))
	return &bw
}

// remoteLog is the branch's commits, newest first: sha and message.
func (h *writeHost) remoteLog() []object.Commit {
	repo, err := gogit.PlainOpen(h.remote)
	require.NoError(h.t, err)
	ref, err := repo.Reference("refs/heads/main", true)
	require.NoError(h.t, err)
	iter, err := repo.Log(&gogit.LogOptions{From: ref.Hash()})
	require.NoError(h.t, err)
	var out []object.Commit
	require.NoError(h.t, iter.ForEach(func(c *object.Commit) error { out = append(out, *c); return nil }))
	return out
}

// tagOf is env's newTag on the branch.
func (h *writeHost) tagOf(env string) string {
	repo, err := gogit.PlainOpen(h.remote)
	require.NoError(h.t, err)
	ref, err := repo.Reference("refs/heads/main", true)
	require.NoError(h.t, err)
	c, err := repo.CommitObject(ref.Hash())
	require.NoError(h.t, err)
	f, err := c.File("environments/" + env + "/kustomization.yaml")
	require.NoError(h.t, err)
	text, err := f.Contents()
	require.NoError(h.t, err)
	for _, l := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "newTag: "); ok {
			return v
		}
	}
	return ""
}

// TestBranchWrite_OneRound (#2366): one round writes every environment that
// asked: one clone and one push, one commit per environment with its own
// promotion message and the round's trailer, and each environment's result
// names its own commit. An answered request is not written again; a newer
// request of an environment already at the version answers noChanges.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_OneRound(t *testing.T) {
	h := newWriteHost(t, "test", "uat")
	c := h.client(interceptor.Funcs{})
	git := &countingGit{GitClient: scm.NewGoGitClient()}
	w := h.reconciler(c, git)
	_, err := h.reconcile(w)
	require.NoError(t, err)
	bw := h.get(c)
	assert.Nil(t, bw.Status.InFlight)
	assert.EqualValues(t, 1, bw.Status.Round)
	require.Len(t, bw.Status.Environments, 2, bw.Status.Message)
	assert.EqualValues(t, 1, git.clones.Load(), "one clone for the round")
	assert.EqualValues(t, 1, git.pushes.Load(), "one push for the round")
	log := h.remoteLog()
	require.Len(t, log, 3, "the seed and one commit per environment")
	byEnv := map[string]string{}
	for _, cm := range log[:2] {
		first, _, _ := strings.Cut(cm.Message, "\n")
		env, ok := strings.CutPrefix(first, "[kardinal] Promote app-v2 to ")
		require.True(t, ok, cm.Message)
		assert.True(t, strings.HasSuffix(strings.TrimSpace(cm.Message), "Kardinal-Write: app-v2-write/1"), cm.Message)
		assert.Contains(t, cm.Message, "Pipeline: app\nNamespace: team", "the promotion message, as git-commit writes it")
		byEnv[env] = cm.Hash.String()
	}
	for _, e := range h.envs {
		res := bw.Status.Environments[e]
		assert.Equal(t, byEnv[e], res.CommitSHA, "%s: its own commit", e)
		assert.EqualValues(t, 1, res.Request)
		assert.EqualValues(t, 1, res.Round)
		assert.Equal(t, "uid-"+e, res.UID)
		assert.Empty(t, res.Error)
		assert.Equal(t, "v2", h.tagOf(e))
	}

	_, err = h.reconcile(w)
	require.NoError(t, err)
	assert.EqualValues(t, 1, h.get(c).Status.Round, "answered requests are not written again")
	assert.EqualValues(t, 1, git.pushes.Load())

	bw = h.get(c)
	base := bw.DeepCopy()
	bw.Spec.Requested[1].Request = 2 // uat retries
	require.NoError(t, c.Patch(context.Background(), bw, client.MergeFrom(base)))
	_, err = h.reconcile(w)
	require.NoError(t, err)
	bw = h.get(c)
	res := bw.Status.Environments["uat"]
	assert.EqualValues(t, 2, res.Round)
	assert.EqualValues(t, 2, res.Request)
	assert.True(t, res.NoChanges, "uat already has v2")
	assert.Equal(t, log[0].Hash.String(), res.CommitSHA, "the head the round started from")
	assert.EqualValues(t, 1, git.pushes.Load(), "nothing to push")
	assert.Len(t, h.remoteLog(), 3)
}

// TestBranchWrite_RestartMidRound (#2366, the coordinator's decision 1): a
// controller that stops after the round pushed, before it recorded the
// results, leaves the round in flight in status with its push intent. A new
// reconciler, with no memory of the round (a fresh branch queue), finds the
// round's commits on the branch by their trailer and records them; nothing is
// pushed twice. One that stops before its push intent writes the round again.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_RestartMidRound(t *testing.T) {
	for _, tc := range []struct {
		name           string
		stopAfterPush  bool
		wantPushesThen int64
	}{
		{name: "after the push", stopAfterPush: true, wantPushesThen: 0},
		{name: "before the push intent", stopAfterPush: false, wantPushesThen: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newWriteHost(t, "test", "uat")
			var stop atomic.Bool
			stop.Store(true)
			c := h.client(interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object,
				patch client.Patch, opts ...client.SubResourcePatchOption) error {
				if bw, ok := obj.(*v1alpha1.BranchWrite); ok && stop.Load() {
					if tc.stopAfterPush && bw.Status.InFlight == nil && len(bw.Status.Environments) > 0 {
						return errors.New("the controller stopped") // the results write, after the push
					}
					if !tc.stopAfterPush && ((bw.Status.InFlight != nil && bw.Status.InFlight.PushIntent != nil) ||
						bw.Status.InFlight == nil) {
						return errors.New("the controller stopped") // at the push intent write, and so every one after it
					}
				}
				return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
			}})
			first := &countingGit{GitClient: scm.NewGoGitClient()}
			_, err := h.reconcile(h.reconciler(c, first))
			require.Error(t, err, "the reconcile that stopped")
			in := h.get(c).Status.InFlight
			require.NotNil(t, in, "the round is left in flight")
			assert.EqualValues(t, 1, in.Round)
			assert.Len(t, in.Members, 2)
			pushedBefore := len(h.remoteLog())

			stop.Store(false)
			again := &countingGit{GitClient: scm.NewGoGitClient()}
			_, err = h.reconcile(h.reconciler(c, again)) // a new process: a fresh branch queue
			require.NoError(t, err)
			bw := h.get(c)
			assert.Nil(t, bw.Status.InFlight)
			require.Len(t, bw.Status.Environments, 2, bw.Status.Message)
			assert.EqualValues(t, tc.wantPushesThen, again.pushes.Load())
			log := h.remoteLog()
			if tc.stopAfterPush {
				assert.Equal(t, pushedBefore, len(log), "nothing pushed twice")
				assert.Contains(t, bw.Status.Message, "2 of its 2 commit(s) found")
				assert.EqualValues(t, 1, bw.Status.Round, "the found round, not a new one")
			} else {
				assert.Equal(t, 1, pushedBefore, "the stopped round never pushed")
				assert.EqualValues(t, 2, bw.Status.Round, "written again in a new round")
			}
			require.Len(t, log, 3, "the seed and one commit per environment")
			for _, cm := range log[:2] {
				first, _, _ := strings.Cut(cm.Message, "\n")
				env := strings.TrimPrefix(first, "[kardinal] Promote app-v2 to ")
				assert.Equal(t, cm.Hash.String(), bw.Status.Environments[env].CommitSHA, "%s: its own commit", env)
			}
		})
	}
}

// TestBranchWrite_BranchMoved: another writer pushes between the round's
// clone and its push; the round writes its environments again on the new
// head, and both writers' commits are on the branch.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_BranchMoved(t *testing.T) {
	h := newWriteHost(t, "test", "uat")
	c := h.client(interceptor.Funcs{})
	git := &countingGit{GitClient: scm.NewGoGitClient(), beforeFirstPush: func() {
		// Another writer's commit to a file of neither environment.
		other := filepath.Join(t.TempDir(), "other")
		g := scm.NewGoGitClient()
		require.NoError(t, g.Clone(context.Background(), "file://"+h.remote, "main", other, scm.GitAuth{}))
		require.NoError(t, os.WriteFile(filepath.Join(other, "README.md"), []byte("other writer\n"), 0o644))
		require.NoError(t, g.CommitAll(context.Background(), other, "another writer", "o", "o@example.com"))
		require.NoError(t, g.Push(context.Background(), other, "origin", "main", scm.GitAuth{}, false))
	}}
	_, err := h.reconcile(h.reconciler(c, git))
	require.NoError(t, err)
	bw := h.get(c)
	require.Len(t, bw.Status.Environments, 2, bw.Status.Message)
	assert.EqualValues(t, 2, git.pushes.Load(), "refused once, then landed")
	assert.EqualValues(t, 2, git.clones.Load(), "the round written again on the new head")
	msgs := ""
	for _, cm := range h.remoteLog() {
		msgs += cm.Message + "\n"
	}
	assert.Contains(t, msgs, "another writer")
	for _, e := range h.envs {
		assert.Empty(t, bw.Status.Environments[e].Error)
		assert.Equal(t, "v2", h.tagOf(e))
	}
}

// TestBranchWrite_MemberFails: an environment whose update fails (its
// kustomization is gone) is answered with the error and written by nobody;
// the others are written, and its half-made change is in no other commit.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_MemberFails(t *testing.T) {
	h := newWriteHost(t, "test", "uat")
	for _, o := range h.objs {
		if p, ok := o.(*v1alpha1.Pipeline); ok {
			p.Spec.Environments[0].Path = "environments/gone"
		}
	}
	c := h.client(interceptor.Funcs{})
	_, err := h.reconcile(h.reconciler(c, scm.NewGoGitClient()))
	require.NoError(t, err)
	bw := h.get(c)
	assert.NotEmpty(t, bw.Status.Environments["test"].Error, "test's update failed")
	assert.Empty(t, bw.Status.Environments["test"].CommitSHA)
	assert.NotEmpty(t, bw.Status.Environments["uat"].CommitSHA, "uat is written")
	log := h.remoteLog()
	require.Len(t, log, 2, "the seed and uat's commit")
	assert.True(t, strings.HasPrefix(log[0].Message, "[kardinal] Promote app-v2 to uat"))
	assert.Equal(t, "v1", h.tagOf("test"))
}

// TestBranchWrite_SupersededBundle: a round of a superseded Bundle writes
// nothing and answers every member cancelled, as the push guard does.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_SupersededBundle(t *testing.T) {
	h := newWriteHost(t, "test")
	for _, o := range h.objs {
		if b, ok := o.(*v1alpha1.Bundle); ok {
			b.Status.Phase = "Superseded"
		}
	}
	c := h.client(interceptor.Funcs{})
	git := &countingGit{GitClient: scm.NewGoGitClient()}
	_, err := h.reconcile(h.reconciler(c, git))
	require.NoError(t, err)
	res := h.get(c).Status.Environments["test"]
	assert.True(t, res.Cancelled)
	assert.Contains(t, res.Error, "superseded")
	assert.Zero(t, git.clones.Load(), "nothing cloned")
	assert.Len(t, h.remoteLog(), 1)
}

// TestBranchWrite_WaitsForTheBranchTurn: a BranchWrite whose branch's turn a
// step holds does no git work and waits; once the turn ends, it is woken
// (wakeWrite) and writes.
//
// Covers GRAPH-COMPACT-10.
func TestBranchWrite_WaitsForTheBranchTurn(t *testing.T) {
	h := newWriteHost(t, "test")
	c := h.client(interceptor.Funcs{})
	git := &countingGit{GitClient: scm.NewGoGitClient()}
	w := h.reconciler(c, git)
	woken := make(chan string, 1)
	w.Steps.turns().wakeWrite = func(key string) { woken <- key }
	key := normalizeRepoURL("file://"+h.remote) + "#main"
	release, _, ok := w.Steps.turns().tryTurn(key, writeNS+"/a-step", time.Now())
	require.True(t, ok)
	res, err := h.reconcile(w)
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter, "it waits for the turn")
	assert.Zero(t, git.clones.Load())
	assert.Contains(t, h.get(c).Status.Message, "waiting for its turn")
	release(time.Now())
	select {
	case k := <-woken:
		assert.Equal(t, writeNS+"/"+writeBW, k)
	case <-time.After(time.Second):
		t.Fatal("not woken when the turn ended")
	}
	_, err = h.reconcile(w)
	require.NoError(t, err)
	assert.NotEmpty(t, h.get(c).Status.Environments["test"].CommitSHA)
}

// countingGit counts clones and pushes, and runs beforeFirstPush once.
type countingGit struct {
	scm.GitClient
	clones, pushes  atomic.Int64
	beforeFirstPush func()
}

func (g *countingGit) Clone(ctx context.Context, url, branch, dir string, auth scm.GitAuth) error {
	g.clones.Add(1)
	return g.GitClient.Clone(ctx, url, branch, dir, auth)
}

func (g *countingGit) Push(ctx context.Context, dir, remote, branch string, auth scm.GitAuth, force bool) error {
	if g.pushes.Add(1) == 1 && g.beforeFirstPush != nil {
		g.beforeFirstPush()
	}
	return g.GitClient.Push(ctx, dir, remote, branch, auth, force)
}

func (g *countingGit) HeadCommit(ctx context.Context, dir string) (string, error) {
	return g.GitClient.(scm.HeadCommitReader).HeadCommit(ctx, dir)
}

func (g *countingGit) CloneOrInit(ctx context.Context, url, branch, dir string, auth scm.GitAuth, depth int) (bool, error) {
	return g.GitClient.(scm.BranchCloner).CloneOrInit(ctx, url, branch, dir, auth, depth)
}

func (g *countingGit) CommitMessages(ctx context.Context, dir string, limit int) ([]scm.CommitMessage, error) {
	return g.GitClient.(scm.HistoryReader).CommitMessages(ctx, dir, limit)
}
