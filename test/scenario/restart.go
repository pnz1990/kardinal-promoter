// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"fmt"
	"sync"

	ctrl "sigs.k8s.io/controller-runtime"
)

// crashState is the crash injection of CrashAfter: the controller dies right
// after one of its side effects, and Restart starts it again.
type crashState struct {
	mu sync.Mutex
	// armed: a kardinal reconcile is running, so its effects count.
	armed bool
	// plan are the effect numbers (counted from CrashAfter) after which the
	// controller dies, ascending; effects counts them.
	plan    []int64
	effects int64
	// when are the effects to crash after, in order, for CrashWhen.
	when []func(what string) bool
	// dead: an effect at a crash point happened. Every write, SCM request
	// and push of the dying reconcile panics from then on (a dead process
	// writes nothing more, deferred writes included), and the reconcile
	// ends there.
	dead bool
	// down: the controller crashed this round; the round's other
	// reconciles do not run (it is restarting).
	down bool
	// at says, per crash, after what effect it happened; log what each
	// effect was.
	at  []string
	log []string
}

// crashSignal is the panic that ends a dying reconcile.
type crashSignal struct{}

// CrashAfter makes the controller crash right after its points-th side
// effects, counted from now: the writes of kardinal's reconcilers to the
// API server, their requests that change something on the SCM, and their
// git pushes. The effect is done (stored, opened, pushed) and the
// reconcile dies before the next one, or at its end; the controller then
// starts again with nothing in memory (Restart), and the round's remaining
// reconciles wait for the next round. points must ascend.
func (w *World) CrashAfter(points ...int64) {
	w.crash.mu.Lock()
	defer w.crash.mu.Unlock()
	w.crash.effects = 0
	w.crash.log = nil
	w.crash.when = nil
	w.crash.plan = append([]int64(nil), points...)
}

// CrashWhen makes the controller crash right after the first side effect
// (as EffectLog names them) for which when[0] holds, then after the next for
// which when[1] holds, and so on, as CrashAfter does.
func (w *World) CrashWhen(when ...func(what string) bool) {
	w.crash.mu.Lock()
	defer w.crash.mu.Unlock()
	w.crash.effects = 0
	w.crash.log = nil
	w.crash.plan = nil
	w.crash.when = append([]func(string) bool(nil), when...)
}

// Crashes says after which effect each crash happened.
func (w *World) Crashes() []string {
	w.crash.mu.Lock()
	defer w.crash.mu.Unlock()
	return append([]string(nil), w.crash.at...)
}

// Effects is the number of controller side effects since CrashAfter.
func (w *World) Effects() int64 {
	w.crash.mu.Lock()
	defer w.crash.mu.Unlock()
	return w.crash.effects
}

// EffectLog says what each side effect since CrashAfter was, in order.
func (w *World) EffectLog() []string {
	w.crash.mu.Lock()
	defer w.crash.mu.Unlock()
	return append([]string(nil), w.crash.log...)
}

// effect records a side effect of a kardinal reconcile, done.
func (w *World) effect(what string) {
	w.crash.mu.Lock()
	defer w.crash.mu.Unlock()
	if !w.crash.armed || w.crash.dead {
		return
	}
	w.crash.effects++
	w.crash.log = append(w.crash.log, what)
	switch {
	case len(w.crash.plan) > 0 && w.crash.effects >= w.crash.plan[0]:
		w.crash.plan = w.crash.plan[1:]
	case len(w.crash.when) > 0 && w.crash.when[0](what):
		w.crash.when = w.crash.when[1:]
	default:
		return
	}
	w.crash.dead = true
	w.crash.at = append(w.crash.at, fmt.Sprintf("after effect %d: %s", w.crash.effects, what))
}

// dying reports whether the running reconcile's controller is dead.
func (w *World) dying() bool {
	w.crash.mu.Lock()
	defer w.crash.mu.Unlock()
	return w.crash.armed && w.crash.dead
}

// crashCheck ends a dying reconcile: called before each of its writes.
func (w *World) crashCheck() {
	if w.dying() {
		panic(crashSignal{})
	}
}

// reconcileOnce runs one reconcile of key with k. crashed: the controller
// died in it, and was restarted.
func (w *World) reconcileOnce(k reconcileKind, req ctrl.Request) (res ctrl.Result, err error, crashed bool) {
	w.crash.mu.Lock()
	w.crash.armed = true
	w.crash.mu.Unlock()
	defer func() {
		r := recover()
		w.crash.mu.Lock()
		w.crash.armed = false
		dead := w.crash.dead
		w.crash.dead = false
		w.crash.mu.Unlock()
		if r != nil {
			if _, ok := r.(crashSignal); !ok {
				panic(r)
			}
		}
		if dead {
			crashed = true
			w.Restart()
		}
	}()
	res, err = k.rec(w.Ctx, req)
	return res, err, false
}

// Restart starts the controller again, as after its Pod died: new
// reconcilers with nothing in memory, an informer cache that lists
// everything again (every object is reconciled once), and an empty
// working directory. The API server, kro, the git host, the SCM and the
// GitOps engine run on.
func (w *World) Restart() {
	w.T.Helper()
	w.cache.reset()
	w.mu.Lock()
	w.seen = map[string]reconcileState{}
	w.mu.Unlock()
	w.crash.mu.Lock()
	w.crash.down = true
	w.crash.mu.Unlock()
	w.startManager()
}

// down reports whether the controller crashed this round.
func (w *World) down() bool {
	w.crash.mu.Lock()
	defer w.crash.mu.Unlock()
	return w.crash.down
}

// takeDown ends the round of a crash: the restarted controller runs from
// the next round.
func (w *World) takeDown() {
	w.crash.mu.Lock()
	defer w.crash.mu.Unlock()
	w.crash.down = false
}
