// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package promotionstep

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
)

// AuditAction describes what happened at a promotion lifecycle transition.
const (
	AuditActionPromotionStarted    = "PromotionStarted"
	AuditActionPromotionSucceeded  = "PromotionSucceeded"
	AuditActionPromotionFailed     = "PromotionFailed"
	AuditActionPromotionSuperseded = "PromotionSuperseded"
	// AuditActionPromotionRejected is written when a started step is
	// cancelled because its Bundle was rejected (kardinal reject, #1451).
	AuditActionPromotionRejected = "PromotionRejected"
	AuditActionRollbackStarted   = "RollbackStarted"
	// AuditActionRollbackSucceeded is written, besides PromotionSucceeded,
	// when a step of a rollback Bundle reaches Verified (B50).
	AuditActionRollbackSucceeded = "RollbackSucceeded"
)

// AuditOutcome describes the result of the action.
const (
	AuditOutcomeSuccess = "Success"
	AuditOutcomeFailure = "Failure"
	AuditOutcomePending = "Pending"
)

// auditKind labels the audit outbox metrics of PromotionStep records.
const auditKind = "PromotionStep"

// auditEntry is the outbox entry recording action on ps at at, and false
// when ps lacks the pipeline or bundle label, so no useful record can be
// written.
//
// The AuditEvent name is {ps.Name}-{action}, with no timestamp: one event per
// step and action is intended, so a re-run reconcile that repeats the
// transition hits AlreadyExists and writes nothing new.
func auditEntry(ps *v1alpha1.PromotionStep, action, outcome, message string, at time.Time) (v1alpha1.PendingAuditEvent, bool) {
	if ps == nil {
		return v1alpha1.PendingAuditEvent{}, false
	}
	labels := ps.GetLabels()
	pipelineName := labels["kardinal.io/pipeline"]
	bundleName := labels["kardinal.io/bundle"]
	envName := labels["kardinal.io/environment"]
	if pipelineName == "" || bundleName == "" {
		return v1alpha1.PendingAuditEvent{}, false
	}
	name := sanitizeK8sName(fmt.Sprintf("%s-%s", ps.Name, slugifyAction(action)))
	return audit.Entry(name, map[string]string{
		"kardinal.io/pipeline":    pipelineName,
		"kardinal.io/bundle":      bundleName,
		"kardinal.io/environment": envName,
		"kardinal.io/action":      action,
	}, v1alpha1.AuditEventSpec{
		BundleName:   bundleName,
		PipelineName: pipelineName,
		Environment:  envName,
		Action:       action,
		Outcome:      outcome,
		Message:      message,
	}, metav1.NewTime(at)), true
}

// flushAudit creates the AuditEvents in ps's outbox
// (status.pendingAuditEvents) and removes the written entries from its
// status. It returns an error when an entry is still unwritten, so the
// reconcile is retried; the entry stays in status until a create succeeds
// (#1552). The status patch carries ps's resourceVersion, so it cannot drop
// an entry a newer reconcile stored.
func (r *Reconciler) flushAudit(ctx context.Context, ps *v1alpha1.PromotionStep) error {
	if len(ps.Status.PendingAuditEvents) == 0 {
		return nil
	}
	remaining, ferr := audit.Flush(ctx, r.Client, auditKind, ps.Namespace, ps.Status.PendingAuditEvents)
	if len(remaining) != len(ps.Status.PendingAuditEvents) {
		prev := ps.Status.PendingAuditEvents
		patch := client.MergeFromWithOptions(ps.DeepCopy(), client.MergeFromWithOptimisticLock{})
		ps.Status.PendingAuditEvents = remaining
		if err := r.Status().Patch(ctx, ps, patch); err != nil && !apierrors.IsNotFound(err) {
			// The entries are still in status: the next flush finds their
			// AuditEvents by name (AlreadyExists).
			ps.Status.PendingAuditEvents = prev
			return errors.Join(ferr, fmt.Errorf("remove written AuditEvents from the outbox: %w", err))
		}
	}
	if ferr != nil {
		zerolog.Ctx(ctx).Error().Err(ferr).Int("pending", len(remaining)).
			Msg("failed to write AuditEvent; kept in status.pendingAuditEvents to retry")
		return ferr
	}
	return nil
}

// slugifyAction converts an action string to a DNS-label-safe slug.
func slugifyAction(action string) string {
	switch action {
	case AuditActionPromotionStarted:
		return "started"
	case AuditActionPromotionSucceeded:
		return "succeeded"
	case AuditActionPromotionFailed:
		return "failed"
	case AuditActionPromotionSuperseded:
		return "superseded"
	case AuditActionPromotionRejected:
		return "rejected"
	case AuditActionRollbackStarted:
		return "rollback-started"
	case AuditActionRollbackSucceeded:
		return "rollback-succeeded"
	default:
		return "event"
	}
}

// sanitizeK8sName truncates and lowercases a string to a valid Kubernetes name.
func sanitizeK8sName(s string) string {
	if len(s) > 253 {
		s = s[:253]
	}
	// Kubernetes names must be lowercase DNS subdomains.
	result := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z':
			result = append(result, c+32) // to lowercase
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.':
			result = append(result, c)
		default:
			result = append(result, '-')
		}
	}
	// Trim leading/trailing hyphens
	for len(result) > 0 && result[0] == '-' {
		result = result[1:]
	}
	for len(result) > 0 && result[len(result)-1] == '-' {
		result = result[:len(result)-1]
	}
	return string(result)
}
