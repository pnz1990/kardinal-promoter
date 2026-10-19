// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Throttle makes the API server answer 429 Too Many Requests to every write
// (create, update, patch, delete, status) of an object for which refuse
// holds, as an API server under load does to some requests and not others;
// nil stops it.
func (w *World) Throttle(refuse func(o client.Object) bool) {
	w.refuseMu.Lock()
	defer w.refuseMu.Unlock()
	w.refuse = refuse
}

// throttled is the 429 of Throttle for a write of o, or nil.
func (w *World) throttled(o client.Object) error {
	w.refuseMu.Lock()
	refuse := w.refuse
	w.refuseMu.Unlock()
	if refuse == nil || o == nil || !refuse(o) {
		return nil
	}
	return apierrors.NewTooManyRequests("the server has received too many requests and has asked us to try again later", 1)
}

// FailGitReads makes the controller's reads of the git host's branch heads
// and histories fail with err (nil: they work again), as an unreachable or
// rate-limited git host does.
func (w *World) FailGitReads(err error) {
	w.refuseMu.Lock()
	defer w.refuseMu.Unlock()
	w.gitReadFail, w.gitHistoryFail = err, err
}

// FailGitHistoryReads makes only the controller's reads of branch histories
// fail with err (nil: they work again); its reads of the branch heads work.
func (w *World) FailGitHistoryReads(err error) {
	w.refuseMu.Lock()
	defer w.refuseMu.Unlock()
	w.gitHistoryFail = err
}

func (w *World) gitReadErr(history bool) error {
	w.refuseMu.Lock()
	defer w.refuseMu.Unlock()
	if history {
		return w.gitHistoryFail
	}
	return w.gitReadFail
}
