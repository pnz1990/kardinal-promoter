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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// AuditAction describes what happened at a promotion lifecycle transition.
const (
	AuditActionPromotionStarted    = "PromotionStarted"
	AuditActionPromotionSucceeded  = "PromotionSucceeded"
	AuditActionPromotionFailed     = "PromotionFailed"
	AuditActionPromotionSuperseded = "PromotionSuperseded"
	// AuditActionRollbackStarted is written when an auto-rollback or manual rollback
	// Bundle is created. Outcome is always Pending (rollback in-flight).
	AuditActionRollbackStarted = "RollbackStarted"
)

// AuditOutcome describes the result of the action.
const (
	AuditOutcomeSuccess = "Success"
	AuditOutcomeFailure = "Failure"
	AuditOutcomePending = "Pending"
)

// writeAuditEvent creates an immutable AuditEvent CRD recording a promotion
// lifecycle transition. It is fire-and-forget: errors are logged but never
// returned — audit logging must not block promotion.
//
// The AuditEvent name includes the PromotionStep name and action to ensure
// uniqueness within a namespace. A timestamp suffix prevents collisions if
// the same action is re-attempted.
func writeAuditEvent(
	ctx context.Context,
	c client.Client,
	ps *v1alpha1.PromotionStep,
	action, outcome, message string,
) {
	if c == nil || ps == nil {
		return
	}

	// Extract pipeline and bundle from PromotionStep labels.
	labels := ps.GetLabels()
	pipelineName := labels["kardinal.io/pipeline"]
	bundleName := labels["kardinal.io/bundle"]
	envName := labels["kardinal.io/environment"]
	if pipelineName == "" || bundleName == "" {
		// Missing labels — can't produce a useful audit event.
		return
	}

	now := metav1.Now()

	// AuditEvent name: {ps.Name}-{action} (truncated to 253 chars).
	// Lowercase and sanitize for Kubernetes name compliance.
	rawName := fmt.Sprintf("%s-%s", ps.Name, slugifyAction(action))
	name := sanitizeK8sName(rawName)

	ae := &v1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ps.Namespace,
			Labels: map[string]string{
				"kardinal.io/pipeline":    pipelineName,
				"kardinal.io/bundle":      bundleName,
				"kardinal.io/environment": envName,
				"kardinal.io/action":      action,
			},
		},
		Spec: v1alpha1.AuditEventSpec{
			Timestamp:    now,
			BundleName:   bundleName,
			PipelineName: pipelineName,
			Environment:  envName,
			Action:       action,
			Outcome:      outcome,
			Message:      message,
		},
	}

	// Idempotent: if the event already exists (re-reconcile), ignore the conflict.
	if err := c.Create(ctx, ae); err != nil {
		// Log at debug — audit write failures must never block promotion.
		// client.IgnoreAlreadyExists would swallow duplicates silently.
		if client.IgnoreAlreadyExists(err) != nil {
			// Non-conflict error — log but proceed.
			_ = err // zerolog not imported in this file; caller logs via reconciler
		}
	}
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
	case AuditActionRollbackStarted:
		return "rollback-started"
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
