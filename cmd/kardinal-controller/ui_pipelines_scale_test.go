// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// fleet returns n Pipelines of envs environments each (a chain in list
// order), one Promoting Bundle per Pipeline, a Verified step of it in every
// environment and one not-ready gate per Pipeline, on the last environment.
func fleet(n, envs int) ([]v1alpha1.Pipeline, []v1alpha1.Bundle, []v1alpha1.PromotionStep, []v1alpha1.PolicyGate) {
	at := metav1.NewTime(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	var ps []v1alpha1.Pipeline
	var bs []v1alpha1.Bundle
	var ss []v1alpha1.PromotionStep
	var gs []v1alpha1.PolicyGate
	for i := range n {
		name := fmt.Sprintf("app-%03d", i)
		p := v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team"}}
		for e := range envs {
			p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{Name: fmt.Sprintf("env-%03d", e)})
		}
		ps = append(ps, p)
		b := v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: name + "-b1", Namespace: "team", CreationTimestamp: at},
			Spec:   v1alpha1.BundleSpec{Type: "image", Pipeline: name, Images: []v1alpha1.ImageRef{{Repository: "r/app", Tag: "1.0.0"}}},
			Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
		bs = append(bs, b)
		for _, env := range p.Spec.Environments {
			ss = append(ss, v1alpha1.PromotionStep{
				ObjectMeta: metav1.ObjectMeta{Name: name + "-" + env.Name, Namespace: "team", CreationTimestamp: at},
				Spec:       v1alpha1.PromotionStepSpec{PipelineName: name, BundleName: b.Name, Environment: env.Name},
				Status: v1alpha1.PromotionStepStatus{State: "Verified", Conditions: []metav1.Condition{
					{Type: "Verified", Status: metav1.ConditionTrue, LastTransitionTime: at}}},
			})
		}
		gs = append(gs, v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: name + "-gate", Namespace: "team",
			Labels: map[string]string{"kardinal.io/bundle": b.Name, "kardinal.io/environment": fmt.Sprintf("env-%03d", envs-1)}}})
	}
	return ps, bs, ss, gs
}

func timeList(n, envs int) time.Duration {
	ps, bs, ss, gs := fleet(n, envs)
	now := time.Now()
	start := time.Now()
	_ = pipelineListResponse(ps, bs, ss, gs, now, nil)
	return time.Since(start)
}

// TestPipelineListResponse_Linear (#1519 QA): the pipeline list scales
// linearly with the environments. Resolving the ordering per environment made
// it cubic (55 ms at 100 environments, about 10 s at 500). Doubling the
// environments must not more than about double the time; the bound is loose
// (4x, against 8x for cubic) so -race and a busy host do not flake it.
func TestPipelineListResponse_Linear(t *testing.T) {
	ps, bs, ss, gs := fleet(1, 3)
	got := pipelineListResponse(ps, bs, ss, gs, time.Now(), nil)
	require.Len(t, got, 1)
	assert.Equal(t, []string{"env-001"}, got[0].EnvironmentTopology[2].Upstreams)
	assert.Equal(t, "1.0.0", got[0].Deployed["env-002"].Version)

	best := func(envs int) time.Duration {
		d := time.Duration(1<<63 - 1)
		for range 3 {
			d = min(d, timeList(20, envs))
		}
		return d
	}
	small, large := best(250), best(500)
	t.Logf("20 pipelines: 250 envs %s, 500 envs %s", small, large)
	assert.Less(t, large, 4*small+5*time.Millisecond, "doubling the environments must not grow the time much more than 2x")
}

// BenchmarkPipelineListResponse is the pipeline list at 200 Pipelines of 500
// environments each (100 000 steps). #1519 QA: it must stay under 100 ms.
// "cold" resolves every Pipeline's ordering (the first poll, or every
// Pipeline edited since the last); "warm" is the steady state of the UI's
// poll, with the orderings cached by generation.
func BenchmarkPipelineListResponse(b *testing.B) {
	ps, bs, ss, gs := fleet(200, 500)
	for i := range ps {
		ps[i].UID = types.UID(fmt.Sprintf("uid-%d", i))
		ps[i].Generation = 1
	}
	now := time.Now()
	b.Run("cold", func(b *testing.B) {
		for b.Loop() {
			_ = pipelineListResponse(ps, bs, ss, gs, now, nil)
		}
	})
	b.Run("warm", func(b *testing.B) {
		cache := &upstreamCache{}
		_ = pipelineListResponse(ps, bs, ss, gs, now, cache)
		for b.Loop() {
			_ = pipelineListResponse(ps, bs, ss, gs, now, cache)
		}
	})
}

// TestUpstreamCache: the cache answers by UID and generation, recomputes on
// a new generation, and prune drops Pipelines that are gone.
func TestUpstreamCache(t *testing.T) {
	ps, _, _, _ := fleet(2, 3)
	ps[0].UID, ps[1].UID = "a", "b"
	c := &upstreamCache{}
	ups, err := c.get(&ps[0])
	require.NoError(t, err)
	assert.Equal(t, []string{"env-000"}, ups["env-001"])
	ps[0].Spec.Environments[2].DependsOn = []string{"env-000"}
	ups, _ = c.get(&ps[0])
	assert.Equal(t, []string{"env-001"}, ups["env-002"], "same generation: cached")
	ps[0].Generation = 2
	ups, _ = c.get(&ps[0])
	assert.Equal(t, []string{"env-000"}, ups["env-002"], "new generation: recomputed")
	_, _ = c.get(&ps[1])
	c.prune(ps[:1])
	assert.Len(t, c.entries, 1)
}
