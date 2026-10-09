// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// branchQueue gives the auto promotions of this controller that push to one
// branch of one repository their turn one at a time, oldest first (#1578).
// Each of N environments writing one branch used to clone, commit and race
// the others for the push: the losers rebased up to 6 times and then started
// over from a fresh clone, so a wave of N made O(N²) pushes (4,023 refused
// for 318 landed at 150 environments). It also held a worker for minutes, so
// other namespaces' steps waited for one (#1577).
//
// A step takes its branch's turn before git-clone and gives it back as soon
// as git-push has landed (or the reconcile ends): it is never held across
// reconciles. A step whose branch is busy, or that is not the oldest live
// waiter, does no git work: it is requeued at low priority after about its
// place in line times the time a turn takes. When a turn ends, the oldest
// live waiter is woken at once (wake), so the branch does not sit idle until
// its requeue. Inside one controller the push then lands at the first
// attempt. Other writers (people, CI, another shard) still move the branch,
// which git-push rebases onto as before.
//
// The turn is per repository branch, not per namespace: two teams whose
// Pipelines write one branch take turns too, as their pushes would collide.
//
// This is in-process state that only times requeues and orders pushes, like
// the Graph identity lock and the SCM circuit breaker; no promotion state
// lives in it, and a restarted controller starts with an empty queue
// (docs/design/16-graph-capability-ledger.md, G8). The zero value is ready
// to use.
type branchQueue struct {
	mu       sync.Mutex
	branches map[string]*branchTurn
	// wake, when set, reconciles the waiting step key (namespace/name) now.
	wake func(step string)
}

// branchTurn is one branch's holder and waiters.
type branchTurn struct {
	holder  string
	since   time.Time
	avgHold time.Duration
	// waiters are the steps refused a turn: when first refused, and the
	// latest time they were told to ask again by. A waiter that has not asked
	// again by then (plus waiterGrace) has gone: finished elsewhere,
	// deleted or superseded.
	waiters map[string]waiter
}

type waiter struct{ first, due time.Time }

const (
	// initialTurn is the assumed length of a turn before one was measured.
	initialTurn = 2 * time.Second
	// minTurnWait and maxTurnWait bound how long a waiting step is requeued for.
	minTurnWait = 300 * time.Millisecond
	maxTurnWait = 15 * time.Second
	// waiterGrace is how late a waiter may ask again and keep its place.
	waiterGrace = 5 * time.Second
)

// turnJitter spreads the waiters' requeues: up to a fifth of the wait either way.
var turnJitter = func(d time.Duration) time.Duration {
	span := int64(d) / 5
	return d + time.Duration(rand.Int64N(2*span+1)-span) //nolint:gosec // jitter, not security
}

// tryTurn gives step the turn of branch key when nobody holds it and no
// live waiter is older, and returns the release func (idempotent).
// Otherwise it returns how long step should wait before it asks again: its
// place among the waiters, oldest first, times the average turn, at least
// minTurnWait and at most maxTurnWait.
func (q *branchQueue) tryTurn(key, step string, now time.Time) (release func(time.Time), wait time.Duration, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.branches == nil {
		q.branches = map[string]*branchTurn{}
	}
	q.sweepLocked(now)
	b := q.branches[key]
	if b == nil {
		b = &branchTurn{avgHold: initialTurn, waiters: map[string]waiter{}}
		q.branches[key] = b
	}
	w, seen := b.waiters[step]
	if !seen {
		w.first = now
	}
	if b.holder == "" || b.holder == step {
		// Free: the oldest live waiter's, or anyone's when nobody waits.
		if head, _ := b.headLocked(); head == "" || head == step {
			b.holder, b.since = step, now
			delete(b.waiters, step)
			var once sync.Once
			return func(at time.Time) { once.Do(func() { q.release(key, step, at) }) }, 0, true
		}
	}
	place := 1
	for k, o := range b.waiters {
		if k != step && (o.first.Before(w.first) || (o.first.Equal(w.first) && k < step)) {
			place++
		}
	}
	wait = time.Duration(place) * b.avgHold
	wait = turnJitter(min(max(wait, minTurnWait), maxTurnWait))
	w.due = now.Add(wait)
	b.waiters[step] = w
	return nil, wait, false
}

// headLocked returns the oldest waiter of b.
func (b *branchTurn) headLocked() (string, waiter) {
	var head string
	var hw waiter
	for k, w := range b.waiters {
		if head == "" || w.first.Before(hw.first) || (w.first.Equal(hw.first) && k < head) {
			head, hw = k, w
		}
	}
	return head, hw
}

// sweepLocked forgets the waiters that did not ask again in time, and the
// branches nobody holds or waits for.
func (q *branchQueue) sweepLocked(now time.Time) {
	for key, b := range q.branches {
		for k, w := range b.waiters {
			if now.After(w.due.Add(waiterGrace)) {
				delete(b.waiters, k)
			}
		}
		if b.holder == "" && len(b.waiters) == 0 {
			delete(q.branches, key)
		}
	}
}

// release ends step's turn of branch key at at, folds its length into the
// branch's average turn, and wakes the oldest waiter.
func (q *branchQueue) release(key, step string, at time.Time) {
	q.mu.Lock()
	b := q.branches[key]
	if b == nil || b.holder != step {
		q.mu.Unlock()
		return
	}
	if d := at.Sub(b.since); d > 0 {
		b.avgHold = (4*b.avgHold + d) / 5
	}
	b.holder = ""
	head, _ := b.headLocked()
	if head == "" {
		delete(q.branches, key)
	}
	wake := q.wake
	q.mu.Unlock()
	if head != "" && wake != nil {
		wake(head)
	}
}

// waiting returns the keys of the steps waiting for branch key, oldest
// first. For tests.
func (q *branchQueue) waiting(key string) []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	b := q.branches[key]
	if b == nil {
		return nil
	}
	keys := make([]string, 0, len(b.waiters))
	for k := range b.waiters {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		wi, wj := b.waiters[keys[i]], b.waiters[keys[j]]
		return wi.first.Before(wj.first) || (wi.first.Equal(wj.first) && keys[i] < keys[j])
	})
	return keys
}

// basePushBranch returns the branch queue key of a sequence that pushes to
// its base branch at or after index from: an auto promotion (no open-pr)
// whose git-push has not run yet. A pr-review promotion pushes to its own
// kardinal/ branch and takes no turn.
func basePushBranch(seq []string, from int, repoURL, branch string) (string, bool) {
	push := -1
	for i, s := range seq {
		switch s {
		case steps.OpenPRStepName:
			return "", false
		case "git-push":
			push = i
		}
	}
	if push < 0 || from > push || repoURL == "" || branch == "" {
		return "", false
	}
	return normalizeRepoURL(repoURL) + "#" + branch, true
}

// normalizeRepoURL folds the spellings of one repository's URL together:
// case, a trailing slash and a .git suffix.
func normalizeRepoURL(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.TrimSuffix(u, "/")
	return strings.TrimSuffix(u, ".git")
}

// waitingForTurnMessage is status.message of a step waiting for its branch.
const waitingForTurnMessage = "waiting for its turn to push to %s: other promotions of this controller are writing it"

// lowPriority is the work-queue priority of a step waiting for its branch:
// every other step goes first (#1577).
var lowPriority = handler.LowPriority

// normalPriority is the work-queue priority a step gets back with its turn.
var normalPriority = 0

// waitForBranchTurn requeues ps, which waits for the turn of branch, after
// wait at low priority. It writes status.message only when it changes, so
// the waiting costs no status write per try, and spends no retry.
func (r *Reconciler) waitForBranchTurn(ctx context.Context, base, ps *v1alpha1.PromotionStep,
	branch string, wait time.Duration) (ctrl.Result, error) {
	res := ctrl.Result{RequeueAfter: wait, Priority: &lowPriority}
	msg := fmt.Sprintf(waitingForTurnMessage, branch)
	if base.Status.Message == msg {
		return res, nil
	}
	ps.Status.Message = msg
	if err := r.Status().Patch(ctx, ps, client.MergeFrom(base)); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("patch branch turn wait: %w", err)
	}
	return res, nil
}
