//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// The shard suite runs two controllers: the main release as shard
// "default" (namespaces without kardinal.io/shard, and cluster-scoped kinds)
// and components/shard.sh's release as shard "b".

// labelShard sets the namespace's kardinal.io/shard label ("" removes it).
func labelShard(t *testing.T, e *framework.Env, ns, shard string) {
	t.Helper()
	require.NoError(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var n corev1.Namespace
		if err := e.Client.Get(context.Background(), types.NamespacedName{Name: ns}, &n); err != nil {
			return err
		}
		if shard == "" {
			delete(n.Labels, "kardinal.io/shard")
		} else {
			n.Labels["kardinal.io/shard"] = shard
		}
		return e.Client.Update(context.Background(), &n)
	}))
}

// waitShardHolder waits until the namespace's kardinal-shard Lease is held
// by shard.
func waitShardHolder(t *testing.T, e *framework.Env, ns, shard string, timeout time.Duration) {
	t.Helper()
	want := "kardinal-shard/" + shard
	framework.Eventually(t, timeout, ns+" held by shard "+shard, func(ctx context.Context) (bool, string) {
		var l coordinationv1.Lease
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: "kardinal-shard"}, &l); err != nil {
			return false, err.Error()
		}
		got := ""
		if l.Spec.HolderIdentity != nil {
			got = *l.Spec.HolderIdentity
		}
		return got == want, "holder " + got
	})
}

// shardBLog is the shard b controller's log.
func shardBLog(t *testing.T, e *framework.Env) string {
	t.Helper()
	ns := os.Getenv("KARDINAL_E2E_SHARD_B_NS")
	require.NotEmpty(t, ns, "KARDINAL_E2E_SHARD_B_NS is not set; the shard suite runs components/shard.sh")
	pods, err := e.Kube.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=kardinal-promoter"})
	require.NoError(t, err)
	var b strings.Builder
	for _, p := range pods.Items {
		raw, err := e.Kube.CoreV1().Pods(ns).GetLogs(p.Name, &corev1.PodLogOptions{}).DoRaw(context.Background())
		require.NoError(t, err)
		b.Write(raw)
	}
	return b.String()
}

// TestShard_SplitsNamespaces promotes one app in a namespace without the
// label and one in a namespace labelled kardinal.io/shard=b. Each is held by
// its shard (the kardinal-shard Lease) and reconciled only by it: both reach
// Verified, and only shard b's controller logs work in the labelled
// namespace.
//
// Covers SHARD-SPLIT-01.
func TestShard_SplitsNamespaces(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	def := newArgoApp(t, e, "test")
	b := newArgoApp(t, e, "test")
	labelShard(t, e, b.ns, "b")
	def.apply(t, def.pipeline(nil))
	b.apply(t, b.pipeline(nil))
	waitShardHolder(t, e, def.ns, "default", time.Minute)
	waitShardHolder(t, e, b.ns, "b", time.Minute)

	defBundle := e.CreateBundle(t, def.ns, pipelineName, "--image", imageV2)
	bBundle := e.CreateBundle(t, b.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, def.ns, pipelineName, defBundle, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, b.ns, pipelineName, bBundle, "test", "Verified", promoteTimeout)
	def.fileHas(t, "test", fixtures.V2, "default shard")
	b.fileHas(t, "test", fixtures.V2, "shard b")

	log := shardBLog(t, e)
	assert.Contains(t, log, b.ns, "shard b reconciled its namespace")
	assert.NotContains(t, log, `"namespace":"`+def.ns+`"`, "shard b never reconciled the default shard's namespace")
}

// TestShard_Handoff moves a namespace from shard default to shard b while a
// pr-review promotion waits for its PR. Shard default releases the Lease,
// shard b takes it and adopts the in-flight step: merging the PR finishes the
// promotion in shard b with no second PR, and a new Bundle is promoted by
// shard b. Moving the namespace back hands it to default the same way.
//
// Covers SHARD-HANDOFF-01.
func TestShard_Handoff(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))
	waitShardHolder(t, e, a.ns, "default", time.Minute)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "WaitingForMerge", promoteTimeout)
	pr := a.openPR(t, bundle, "test")

	labelShard(t, e, a.ns, "b")
	waitShardHolder(t, e, a.ns, "b", 2*time.Minute)
	a.merge(t, pr)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	a.fileHas(t, "test", fixtures.V2, "after the handoff")
	prs, err := e.Git.PullRequests(context.Background(), a.repo)
	require.NoError(t, err)
	n := 0
	for _, p := range prs {
		if p.Head == prHead(bundle, "test") {
			n++
		}
	}
	assert.Equal(t, 1, n, "one PR for the promotion across the handoff")

	next := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV3)
	e.WaitStepState(t, a.ns, pipelineName, next, "test", "WaitingForMerge", promoteTimeout)
	assert.Contains(t, shardBLog(t, e), a.ns, "shard b promotes the namespace")

	labelShard(t, e, a.ns, "")
	waitShardHolder(t, e, a.ns, "default", 2*time.Minute)
	a.merge(t, a.openPR(t, next, "test"))
	e.WaitStepState(t, a.ns, pipelineName, next, "test", "Verified", promoteTimeout)
	a.fileHas(t, "test", fixtures.V3, fmt.Sprintf("back in shard default (%s)", a.ns))
}
