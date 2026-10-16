// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package lifecycle

import (
	"time"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// LeadTimeStart returns when b's lead time starts: its source commit
// (spec.provenance.commitTime, which CI sets) or, without one, the Bundle's
// creation, which leaves out the build (#1942). fromCommit reports which. A
// rollback Bundle starts at its creation: its commit is the restored
// build's, often days older than the rollback. A commitTime after the
// Bundle's creation cannot be the commit's (a Bundle created with kubectl is
// not validated by ValidateNewBundle), and is ignored.
func LeadTimeStart(b *v1alpha1.Bundle) (start time.Time, fromCommit bool) {
	created := b.CreationTimestamp.UTC()
	if p := b.Spec.Provenance; p != nil && p.CommitTime != nil && !p.CommitTime.IsZero() && p.RollbackOf == "" &&
		!p.CommitTime.After(created) {
		return p.CommitTime.UTC(), true
	}
	return created, false
}

// AutoRollback reports whether b is a rollback Bundle the controller started
// on its own (onHealthFailure: rollback, a RollbackPolicy): it carries
// kardinal.io/rollback=true and kardinal.io/reason=AutoRollback. A rollback
// a person started (kardinal rollback, the UI) has no such reason.
func AutoRollback(b *v1alpha1.Bundle) bool {
	return b.Labels[LabelRollback] == "true" && b.Labels[LabelReason] == ReasonAutoRollback
}
