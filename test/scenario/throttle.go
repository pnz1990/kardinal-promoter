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
