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
// build's, often days older than the rollback (the lead time metrics leave
// rollbacks out: IsRollback). A commitTime after the Bundle's creation, or
// more than CommitTimeMaxAge before it, cannot be the commit's (a Bundle
// created with kubectl is not validated by ValidateNewBundle), and is
// ignored.
func LeadTimeStart(b *v1alpha1.Bundle) (start time.Time, fromCommit bool) {
	created := b.CreationTimestamp.UTC()
	if p := b.Spec.Provenance; p != nil && p.CommitTime != nil && !p.CommitTime.IsZero() && p.RollbackOf == "" &&
		!p.CommitTime.After(created) && !p.CommitTime.Time.Before(created.Add(-CommitTimeMaxAge)) {
		return p.CommitTime.UTC(), true
	}
	return created, false
}

// CommitTimeMaxAge is the oldest a provenance.commitTime may be, before the
// Bundle's creation. An older one is almost always a wrong value (the Unix
// epoch, a default, seconds read as milliseconds) rather than a commit that
// waited a year to ship, and one such value would set the p90 lead time on
// its own. DORA's slowest lead time band is "more than six months", so a
// year keeps every real lead time a band can tell apart. A fixed date (git's
// first release, 2005) would still accept any wrong value after it.
const CommitTimeMaxAge = 365 * 24 * time.Hour

// IsRollback reports whether b is a rollback Bundle (spec.provenance.rollbackOf).
// The lead time metrics leave rollbacks out, as DORA's lead time for changes
// does: a rollback restores a change, it does not ship one, and many fast
// rollbacks would pull the p50 down.
func IsRollback(b *v1alpha1.Bundle) bool {
	return b.Spec.Provenance != nil && b.Spec.Provenance.RollbackOf != ""
}

// AutoRollback reports whether b is a rollback Bundle the controller started
// on its own (onHealthFailure: rollback, a RollbackPolicy): it carries
// kardinal.io/rollback=true and kardinal.io/reason=AutoRollback. A rollback
// a person started (kardinal rollback, the UI) has no such reason.
func AutoRollback(b *v1alpha1.Bundle) bool {
	return b.Labels[LabelRollback] == "true" && b.Labels[LabelReason] == ReasonAutoRollback
}
