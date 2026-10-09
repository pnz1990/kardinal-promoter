// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noTurnJitter(t *testing.T) {
	t.Helper()
	old := turnJitter
	turnJitter = func(d time.Duration) time.Duration { return d }
	t.Cleanup(func() { turnJitter = old })
}

// TestBranchQueue_FIFO (#1578): one step holds a branch's turn; the others
// wait, the longer the further back they are. A released turn goes to the
// oldest live waiter, who is woken at once: a newcomer, or a younger waiter
// that asks first, is refused while it waits. Another branch, or the same
// branch of another repository, is independent.
//
// Covers PERF-PUSH-QUEUE-01.
func TestBranchQueue_FIFO(t *testing.T) {
	noTurnJitter(t)
	var woken []string
	q := branchQueue{wake: func(step string) { woken = append(woken, step) }}
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

	at := now.Add(time.Second)
	release(at)
	release(at) // idempotent
	assert.Equal(t, []string{"ns/b"}, woken, "the oldest waiter is woken once")
	_, _, ok = q.tryTurn("repo#main", "ns/c", at)
	assert.False(t, ok, "a younger waiter asking first is refused")
	_, _, ok = q.tryTurn("repo#main", "ns/new", at)
	assert.False(t, ok, "so is a newcomer")
	_, _, ok = q.tryTurn("repo#main", "ns/b", at)
	require.True(t, ok, "the oldest waiter takes it")
	assert.Equal(t, []string{"ns/c", "ns/new"}, q.waiting("repo#main"))
	_, waitC, _ = q.tryTurn("repo#main", "ns/c", at)
	assert.Equal(t, (4*initialTurn+time.Second)/5, waitC, "the average turn follows the measured one")
}

// TestBranchQueue_GoneWaitersAndBounds: waits stay within minTurnWait and
// maxTurnWait; a waiter that does not ask again by its due time (plus
// waiterGrace) is dropped and the turn goes to the next one, so a deleted or
// finished step cannot block the branch; a branch nobody holds or waits for
// is dropped.
func TestBranchQueue_GoneWaitersAndBounds(t *testing.T) {
	noTurnJitter(t)
	var q branchQueue
	now := time.Now()
	release, _, _ := q.tryTurn("k", "holder", now)
	var wait time.Duration
	for i := 0; i < 50; i++ {
		_, wait, _ = q.tryTurn("k", fmt.Sprintf("ns/w%02d", i), now.Add(time.Duration(i)))
	}
	assert.Equal(t, maxTurnWait, wait, "fifty in line: capped")
	release(now.Add(time.Second))

	// w00 never asks again; w01 does, after w00's due time and grace.
	later := now.Add(initialTurn + waiterGrace + time.Second)
	_, _, ok := q.tryTurn("k", "ns/w01", later)
	assert.True(t, ok, "the gone head is dropped; the next live waiter takes the turn")

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
