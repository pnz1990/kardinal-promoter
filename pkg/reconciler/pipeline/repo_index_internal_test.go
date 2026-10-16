// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// pipelineListCounter records how many Pipelines each PipelineList read
// returns: what an informer cache deep-copies for the read.
type pipelineListCounter struct {
	client.Client
	listed []int
}

func (c *pipelineListCounter) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	err := c.Client.List(ctx, list, opts...)
	if pl, ok := list.(*kardinalv1alpha1.PipelineList); ok && err == nil {
		c.listed = append(c.listed, len(pl.Items))
	}
	return err
}

func (c *pipelineListCounter) total() int {
	n := 0
	for _, v := range c.listed {
		n += v
	}
	return n
}

// TestPipelineRepoIndex_ReadsOnlyPeers (#1684): a Pipeline reconcile, and
// the Pipeline watches that re-check PathConflict and the rendered branch
// conflict, read only the Pipelines of the same repository through the
// IndexPipelineRepo index, not every Pipeline in the cluster. In the
// 100-Pipeline soak the cluster-wide list on every Pipeline reconcile
// allocated 68.8 GB of the leader's 685 GB. The conditions are unchanged:
// a peer on the same repository and branch in the same namespace is named,
// one in another namespace is counted, and another branch or repository is
// no conflict.
func TestPipelineRepoIndex_ReadsOnlyPeers(t *testing.T) {
	const others = 200
	const repo = "https://github.com/org/gitops"
	s := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))

	withSecret := func(p kardinalv1alpha1.Pipeline) *kardinalv1alpha1.Pipeline {
		p.Spec.Git.SecretRef = &kardinalv1alpha1.SecretRef{Name: "git"}
		p.UID = types.UID(p.Namespace + "/" + p.Name)
		return &p
	}
	mine := withSecret(pl("team-a", "mine", repo, ev("prod", "env/prod")))
	sameNS := withSecret(pl("team-a", "peer", "git@github.com:org/gitops.git", ev("prod", "env/prod/app")))
	otherNS := withSecret(pl("team-b", "peer", "https://github.com/ORG/gitops.git", ev("prod", "env/prod")))
	otherBranch := withSecret(pl("team-a", "branch", repo, ev("prod", "env/prod")))
	otherBranch.Spec.Git.Branch = "release"
	objs := []client.Object{mine, sameNS, otherNS, otherBranch}
	for _, ns := range []string{"team-a", "team-b"} {
		objs = append(objs, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "git"},
			Data: map[string][]byte{"token": []byte("x")}})
	}
	for i := range others {
		objs = append(objs, withSecret(pl(fmt.Sprintf("tenant-%d", i%10), fmt.Sprintf("app-%d", i),
			fmt.Sprintf("https://github.com/org/app-%d", i), ev("prod", "env/prod"))))
	}
	c := &pipelineListCounter{Client: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&kardinalv1alpha1.Pipeline{}).
		WithIndex(&kardinalv1alpha1.Pipeline{}, IndexPipelineRepo, PipelineRepoValues).
		WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).
		WithIndex(&kardinalv1alpha1.PromotionStep{}, "spec.pipelineName", func(o client.Object) []string {
			return []string{o.(*kardinalv1alpha1.PromotionStep).Spec.PipelineName}
		}).Build()}
	r := &Reconciler{Client: c}
	ctx := context.Background()

	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(mine)}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.NotEmpty(t, c.listed, "the reconcile reads Pipelines")
	// The reconcile lists mine and its two peers on repository and branch,
	// never the 200 Pipelines of other repositories (it listed 204 before).
	assert.Equal(t, []int{3}, c.listed, "Pipelines read by one reconcile")

	var got kardinalv1alpha1.Pipeline
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionPathConflict)
	require.NotNil(t, cond, "the same-repository peers still conflict")
	assert.Contains(t, cond.Message, "Pipeline peer environment prod (env/prod/app)", "the same-namespace peer is named")
	assert.Contains(t, cond.Message, "1 other Pipeline(s) in other namespaces", "the other namespace's peer is counted")
	assert.NotContains(t, cond.Message, "Pipeline branch ", "another branch is no conflict")

	// The watches: a Pipeline event enqueues its peers from the index.
	c.listed = nil
	reqs := r.pipelinesSharingRepo(ctx, mine)
	assert.ElementsMatch(t, []string{"team-a/peer", "team-b/peer"}, names(reqs))
	assert.Equal(t, 3, c.total(), "the PathConflict watch reads only the repository and branch")

	c.listed = nil
	rendering := withSecret(pl("team-c", "render", repo, kardinalv1alpha1.EnvironmentSpec{Name: "prod", Layout: "branch",
		Render: &kardinalv1alpha1.RenderConfig{Branch: "env/prod-rendered"}}))
	require.NoError(t, c.Create(ctx, rendering))
	reqs = r.renderingPipelinesSharingRepo(ctx, mine)
	assert.Equal(t, []string{"team-c/render"}, names(reqs), "the rendering Pipeline of the repository")
	// mine, otherBranch, otherNS and render: the scp spelling of sameNS is
	// never scm.SameRepoURL, so it is not on the URL key.
	assert.Equal(t, 4, c.total(), "the rendered branch watch reads only the repository's Pipelines, every branch")
}

func names(reqs []ctrl.Request) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Namespace+"/"+r.Name)
	}
	return out
}

// TestPipelineRepoValues: the index values are the repository and branch
// PathConflict compares (main when the branch is empty, any URL spelling)
// and the scm.SameRepoURL key; a URL SameRepoURL never matches (scp form)
// gets only the first, and an object of another kind none.
func TestPipelineRepoValues(t *testing.T) {
	tests := []struct {
		name, url, branch string
		want              []string
	}{
		{"https", "https://github.com/Org/GitOps.git", "", []string{"branch:github.com/org/gitops#main", "url:https://github.com:443/org/gitops"}},
		{"branch", "https://github.com/org/gitops", "release", []string{"branch:github.com/org/gitops#release", "url:https://github.com:443/org/gitops"}},
		{"scp", "git@github.com:org/gitops.git", "main", []string{"branch:github.com/org/gitops#main"}},
		{"empty", "", "", []string{"branch:#main"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := pl("ns", "p", tc.url)
			p.Spec.Git.Branch = tc.branch
			assert.Equal(t, tc.want, PipelineRepoValues(&p))
		})
	}
	assert.Nil(t, PipelineRepoValues(&kardinalv1alpha1.Bundle{}))
}
