// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// events records what the cleanups and the fake controller did, in order.
type events struct {
	mu  sync.Mutex
	got []string
}

func (ev *events) add(format string, args ...any) {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	ev.got = append(ev.got, fmt.Sprintf(format, args...))
}

func (ev *events) list() []string {
	ev.mu.Lock()
	defer ev.mu.Unlock()
	return append([]string(nil), ev.got...)
}

// fakeGit is a git server that only creates and deletes repos.
type fakeGit struct {
	gitserver.Server
	ev    *events
	mu    sync.Mutex
	repos map[string]bool
}

func (g *fakeGit) Kind() string { return "fake" }

func (g *fakeGit) CreateRepo(_ context.Context, name string, _ map[string][]byte) (gitserver.Repo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.repos[name] = true
	return gitserver.Repo{Owner: "e2e", Name: name, Branch: "main"}, nil
}

func (g *fakeGit) DeleteRepo(_ context.Context, r gitserver.Repo) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.repos, r.Name)
	g.ev.add("delete repo")
	return nil
}

func (g *fakeGit) exists(name string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.repos[name]
}

// TestRepoCleanupDrainsNamespaceFirst checks the order of a test's cleanups:
// its namespace's PromotionSteps are deleted, and their close-pr finalizer
// closes the PR, while the repo still exists; only then is the repo deleted,
// and the namespace after it.
func TestRepoCleanupDrainsNamespaceFirst(t *testing.T) {
	ev := &events{}
	c := fake.NewClientBuilder().WithScheme(Scheme()).WithInterceptorFuncs(interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*corev1.Namespace); ok {
				ev.add("delete namespace")
			}
			return c.Delete(ctx, obj, opts...)
		},
	}).Build()
	git := &fakeGit{ev: ev, repos: map[string]bool{}}
	e := &Env{Client: c, Git: git}
	t.Setenv(EnvKeep, "")

	stop := make(chan struct{})
	defer close(stop)
	t.Run("test", func(t *testing.T) {
		ctx := context.Background()
		ns := e.Namespace(t)
		repo := e.RepoWithoutWebhook(t, ns, nil)
		require.NoError(t, c.Create(ctx, &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: ns}}))
		step := &v1alpha1.PromotionStep{ObjectMeta: metav1.ObjectMeta{
			Name: "s", Namespace: ns, Finalizers: []string{"kardinal.io/close-pr"},
		}}
		require.NoError(t, c.Create(ctx, step))
		// The controller: closing a deleted step's PR needs the repo.
		go func() {
			key := types.NamespacedName{Namespace: ns, Name: step.Name}
			for {
				select {
				case <-stop:
					return
				case <-time.After(50 * time.Millisecond):
				}
				var s v1alpha1.PromotionStep
				if err := c.Get(ctx, key, &s); apierrors.IsNotFound(err) {
					return
				} else if err != nil || s.DeletionTimestamp == nil {
					continue
				}
				ev.add("close PR (repo exists: %t)", git.exists(repo.Name))
				s.Finalizers = nil
				if err := c.Update(ctx, &s); err != nil {
					ev.add("remove finalizer: %v", err)
				}
			}
		}()
	})
	assert.Equal(t, []string{"close PR (repo exists: true)", "delete repo", "delete namespace"}, ev.list())
	var bundles v1alpha1.BundleList
	require.NoError(t, c.List(context.Background(), &bundles))
	assert.Empty(t, bundles.Items, "the drain deletes the Bundles")
}
