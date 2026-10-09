// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBranchQueue_OneTurnAtATime (#1578): one step holds a branch's turn;
// the others wait, the longer the further back they are, and the turn goes
// to the next step once it is released. Another branch, or the same branch
// of another repository, is independent.
//
// Covers PERF-PUSH-QUEUE-01.
func TestBranchQueue_OneTurnAtATime(t *testing.T) {
	defer func(old func(time.Duration) time.Duration) { turnJitter = old }(turnJitter)
	turnJitter = func(d time.Duration) time.Duration { return d }
	var q branchQueue
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

	release, _, ok := q.tryTurn("repo#main", "ns/a", now)
	require.True(t, ok)
	_, waitB, ok := q.tryTurn("repo#main", "ns/b", now)
	require.False(t, ok)
	_, waitC, ok := q.tryTurn("repo#main", "ns/c", now.Add(time.Millisecond))
	require.False(t, ok)
	assert.Equal(t, initialTurn, waitB, "first in line: one turn")
	assert.Equal(t, 2*initialTurn, waitC, "second in line: two turns")
	assert.Equal(t, []string{"ns/b", "ns/c"}, q.waiting("repo#main"))

	_, _, ok = q.tryTurn("repo#other", "ns/b", now)
	assert.True(t, ok, "another branch has its own turn")

	release(now.Add(7 * time.Second))
	_, _, ok = q.tryTurn("repo#main", "ns/b", now.Add(7*time.Second))
	require.True(t, ok, "released: the next step takes the turn")
	assert.Equal(t, []string{"ns/c"}, q.waiting("repo#main"))
	_, waitC, _ = q.tryTurn("repo#main", "ns/c", now.Add(7*time.Second))
	assert.Equal(t, (4*initialTurn+7*time.Second)/5, waitC, "the average turn follows the measured one")
}

// TestBranchQueue_Bounds: waits stay within minTurnWait and maxTurnWait, a
// waiter that stopped asking is forgotten, and a branch with no holder and
// no waiters is dropped.
func TestBranchQueue_Bounds(t *testing.T) {
	defer func(old func(time.Duration) time.Duration) { turnJitter = old }(turnJitter)
	turnJitter = func(d time.Duration) time.Duration { return d }
	var q branchQueue
	now := time.Now()
	_, _, _ = q.tryTurn("k", "holder", now)
	var wait time.Duration
	for i := 0; i < 50; i++ {
		_, wait, _ = q.tryTurn("k", string(rune('a'+i%26))+string(rune('a'+i/26)), now.Add(time.Duration(i)))
	}
	assert.Equal(t, maxTurnWait, wait, "fifty in line: capped")
	_, _, _ = q.tryTurn("k", "late", now.Add(waiterTTL+time.Second))
	assert.Equal(t, []string{"late"}, q.waiting("k"), "waiters that stopped asking are forgotten")

	var q2 branchQueue
	rel, _, _ := q2.tryTurn("k", "a", now)
	rel(now)
	assert.Empty(t, q2.branches, "nothing kept for an idle branch")
}

func TestBasePushBranch(t *testing.T) {
	auto := []string{"git-clone", "kustomize-set-image", "git-commit", "git-push", "health-check"}
	key, ok := basePushBranch(auto, 0, "https://Git.example/org/Repo.git/", "main")
	require.True(t, ok)
	assert.Equal(t, "https://git.example/org/repo#main", key)
	_, ok = basePushBranch(auto, 4, "https://git.example/org/repo", "main")
	assert.False(t, ok, "past git-push: no turn")
	_, ok = basePushBranch([]string{"git-clone", "git-commit", "git-push", "open-pr", "wait-for-merge"}, 0, "u", "main")
	assert.False(t, ok, "a pr-review promotion pushes its own branch")
	_, ok = basePushBranch([]string{"health-check"}, 0, "u", "main")
	assert.False(t, ok)
}
