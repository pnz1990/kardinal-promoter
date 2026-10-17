// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apiserver/pkg/cel/environment"
)

// shardNameSets are principal sets an admission router must spread: random
// ServiceAccounts, and adversarial sets that differ only by a numeric suffix
// or by namespace, as fleets of CI bots and per-team agents do.
func shardNameSets() map[string][]string {
	r := rand.New(rand.NewSource(2071))
	word := func(n int) string {
		const a = "abcdefghijklmnopqrstuvwxyz0123456789-"
		b := make([]byte, n)
		for i := range b {
			b[i] = a[r.Intn(len(a)-1)]
		}
		return string(b)
	}
	sets := map[string][]string{}
	for i := 0; i < 10000; i++ {
		sets["random-sa"] = append(sets["random-sa"], fmt.Sprintf("system:serviceaccount:%s:%s", word(4+r.Intn(12)), word(4+r.Intn(20))))
		sets["ci-bot-suffix"] = append(sets["ci-bot-suffix"], fmt.Sprintf("ci-bot-%04d", i))
		sets["sa-ci-bot-suffix"] = append(sets["sa-ci-bot-suffix"], fmt.Sprintf("system:serviceaccount:ci:ci-bot-%04d", i))
		sets["team-ns-same-agent"] = append(sets["team-ns-same-agent"], fmt.Sprintf("system:serviceaccount:team-%04d:promoter-agent", i))
		sets["emails"] = append(sets["emails"], fmt.Sprintf("%s.%s@Example.COM", word(3+r.Intn(8)), word(3+r.Intn(10))))
		sets["prefix-number"] = append(sets["prefix-number"], fmt.Sprintf("%d-agent", i))
	}
	return sets
}

func TestShardOf_Balance(t *testing.T) {
	for name, set := range shardNameSets() {
		for _, n := range []int{2, 3, 5, DefaultIndexShards, 16, 64} {
			counts := make([]int, n)
			for _, p := range set {
				s := ShardOf(p, n)
				require.True(t, s >= 0 && s < n, "%s: shard %d of %d", p, s, n)
				counts[s]++
			}
			mean := float64(len(set)) / float64(n)
			for i, c := range counts {
				assert.LessOrEqual(t, float64(c), 2*mean, "%s, %d shards: shard %d holds %d, mean %.0f", name, n, i, c, mean)
			}
		}
	}
}

func TestShardOf_OneShard(t *testing.T) {
	assert.Equal(t, 0, ShardOf("anyone", 1))
	assert.Equal(t, 0, ShardOf("anyone", 0))
	assert.Equal(t, "0", ShardExpression("u", 1))
}

// TestShardExpression_MatchesShardOf pins the chart's CEL router to ShardOf
// in the API server's own CEL environment at the chart's minimum Kubernetes
// version: a principal the controller indexes in shard i must be routed to
// shard i's policy, or the policy would read it as not an agent.
func TestShardExpression_MatchesShardOf(t *testing.T) {
	base := environment.MustBaseEnvSet(version.MajorMinor(1, 35))
	envs, err := base.Extend(environment.VersionedOptions{
		IntroducedVersion: version.MajorMinor(1, 0),
		EnvOptions:        []cel.EnvOption{cel.Variable("u", cel.StringType)},
	})
	require.NoError(t, err)
	env, err := envs.Env(environment.StoredExpressions)
	require.NoError(t, err)

	edge := []string{"", "a", "ö", "日本語-agent", "UPPER@Example.COM", "system:serviceaccount:kardinal-system:kardinal-controller"}
	long := ""
	for i := 0; i < 300; i++ {
		long += string(rune('a' + i%26))
	}
	edge = append(edge, long, long+"ß")
	for _, n := range []int{2, 3, DefaultIndexShards, MaxIndexShards} {
		ast, iss := env.Compile(ShardExpression("u", n))
		require.NoError(t, iss.Err(), "%d shards", n)
		prg, err := env.Program(ast, cel.CostLimit(1_000_000))
		require.NoError(t, err)
		check := func(p string) {
			out, _, err := prg.Eval(map[string]any{"u": p})
			require.NoError(t, err, "%q", p)
			require.EqualValues(t, ShardOf(p, n), out.Value(), "%q with %d shards", p, n)
		}
		for _, p := range edge {
			check(p)
		}
		total := 0
		for _, set := range shardNameSets() {
			for i, p := range set {
				if n != DefaultIndexShards && i%10 != 0 {
					continue
				}
				check(p)
				total++
			}
		}
		if n == DefaultIndexShards {
			assert.GreaterOrEqual(t, total, 10000)
		}
	}
}
