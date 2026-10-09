// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps

import "errors"

// ErrPermanent marks a step error that retrying cannot fix, such as a
// configuration the step refuses. The PromotionStep reconciler retries an
// error returned by a step with backoff (network, API and git failures), and
// fails the step at once when errors.Is(err, ErrPermanent).
//
// A step that fails without returning an error (StepFailed, nil) is always
// permanent; ErrPermanent is only needed when the step also returns an error.
var ErrPermanent = errors.New("permanent step failure")

// Permanent marks err as permanent. The message is unchanged, and err stays
// in the chain for errors.Is and errors.As. Permanent(nil) is nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// ErrContended marks a step that lost a race for a shared resource too
// often in one reconcile: the base branch kept moving while git-push
// rebased and the sequence restarted. It is transient: the PromotionStep
// reconciler requeues the step with backoff and jitter (RequeueAfter),
// it does not wait inside the reconcile.
var ErrContended = errors.New("contended")

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }

func (e *permanentError) Unwrap() []error { return []error{e.err, ErrPermanent} }
