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

package policygate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
)

// maxObjectNameLength is the longest valid Kubernetes object name.
const maxObjectNameLength = 253

// auditKind labels the audit outbox metrics of PolicyGate records.
const auditKind = "PolicyGate"

// gateAuditEntry is the outbox entry recording a PolicyGate readiness
// change at time at, and false for a gate without the pipeline or bundle
// label. patchStatus stores it on the first evaluation and on every ready
// flip, in the same status patch as the flip (#1552).
//
// The name carries the outcome and the transition time, so every transition
// gets its own record (C04-gates-22); a fixed name per gate recorded only the
// first one. The name is fixed when the entry is stored, so a retried create
// finds the record by name.
func gateAuditEntry(gate *kardinalv1alpha1.PolicyGate, outcome, reason string,
	at metav1.Time) (kardinalv1alpha1.PendingAuditEvent, bool) {
	if gate == nil {
		return kardinalv1alpha1.PendingAuditEvent{}, false
	}
	labels := gate.GetLabels()
	pipelineName := labels["kardinal.io/pipeline"]
	bundleName := labels["kardinal.io/bundle"]
	envName := labels["kardinal.io/environment"]
	if pipelineName == "" || bundleName == "" {
		return kardinalv1alpha1.PendingAuditEvent{}, false
	}

	action := "GateEvaluated"
	// Milliseconds: a gate can flip more than once in a second, and with
	// seconds the second flip to the same outcome had the first one's name
	// and was dropped as AlreadyExists (#1484).
	suffix := fmt.Sprintf("-gate-%s-%d", strings.ToLower(outcome), at.UnixMilli())
	base := gate.Name
	if limit := maxObjectNameLength - len(suffix); len(base) > limit {
		base = base[:limit]
	}
	name := sanitizeGateName(base + suffix)

	aeLabels := map[string]string{
		"kardinal.io/pipeline":    pipelineName,
		"kardinal.io/bundle":      bundleName,
		"kardinal.io/environment": envName,
		"kardinal.io/action":      action,
	}
	// kardinal.io/gate is the user-facing gate name. Instances carry it in
	// kardinal.io/gate-name; gate-template is the fallback for instances created
	// before that label existed.
	if g := labels["kardinal.io/gate-name"]; g != "" {
		aeLabels["kardinal.io/gate"] = g
	} else if g := labels["kardinal.io/gate-template"]; g != "" {
		aeLabels["kardinal.io/gate"] = g
	}

	return audit.Entry(name, aeLabels, kardinalv1alpha1.AuditEventSpec{
		BundleName:   bundleName,
		PipelineName: pipelineName,
		Environment:  envName,
		Action:       action,
		Outcome:      outcome,
		Message:      reason,
	}, at), true
}

// flushAudit creates the AuditEvents in gate's outbox
// (status.pendingAuditEvents) and removes the written entries from its
// status. It returns an error when an entry is still unwritten; the entry
// stays in status until a create succeeds (#1552). The status patch carries
// the gate's resourceVersion, so it cannot drop an entry a newer reconcile
// stored.
func (r *Reconciler) flushAudit(ctx context.Context, gate *kardinalv1alpha1.PolicyGate) error {
	if len(gate.Status.PendingAuditEvents) == 0 {
		return nil
	}
	remaining, ferr := audit.Flush(ctx, r.Client, auditKind, gate.Namespace, gate.Status.PendingAuditEvents)
	if len(remaining) != len(gate.Status.PendingAuditEvents) {
		prev := gate.Status.PendingAuditEvents
		patch := client.MergeFromWithOptions(gate.DeepCopy(), client.MergeFromWithOptimisticLock{})
		gate.Status.PendingAuditEvents = remaining
		if err := r.Status().Patch(ctx, gate, patch); err != nil && !apierrors.IsNotFound(err) {
			// The entries are still in status: the next flush finds their
			// AuditEvents by name (AlreadyExists).
			gate.Status.PendingAuditEvents = prev
			return errors.Join(ferr, fmt.Errorf("remove written AuditEvents from the outbox: %w", err))
		}
	}
	if ferr != nil {
		zerolog.Ctx(ctx).Warn().Err(ferr).Str("gate", gate.Name).Int("pending", len(remaining)).
			Msg("failed to write PolicyGate AuditEvent; kept in status.pendingAuditEvents to retry")
		return ferr
	}
	return nil
}

// sanitizeGateName produces a valid Kubernetes name from a gate event name.
func sanitizeGateName(s string) string {
	if len(s) > maxObjectNameLength {
		s = s[:maxObjectNameLength]
	}
	result := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z':
			result = append(result, c+32)
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.':
			result = append(result, c)
		default:
			result = append(result, '-')
		}
	}
	for len(result) > 0 && result[0] == '-' {
		result = result[1:]
	}
	for len(result) > 0 && result[len(result)-1] == '-' {
		result = result[:len(result)-1]
	}
	return string(result)
}
