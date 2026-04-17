// Copyright 2026 The kardinal-promoter Authors.
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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

const (
	// AuditActionGateEvaluated is written on every PolicyGate reconcile that
	// produces a definitive pass/fail result (not on context-build errors).
	AuditActionGateEvaluated = "GateEvaluated"
)

// writeGateAuditEvent creates an immutable AuditEvent CRD recording a
// PolicyGate evaluation result. It is fire-and-forget — errors are ignored
// because audit logging must never block gate evaluation.
//
// The AuditEvent name encodes gate name + outcome + last-evaluated time
// to ensure uniqueness on repeated reconciles.
func writeGateAuditEvent(
	ctx context.Context,
	c client.Client,
	gate *v1alpha1.PolicyGate,
	outcome, message string,
) {
	if c == nil || gate == nil {
		return
	}

	labels := gate.GetLabels()
	pipelineName := labels["kardinal.io/pipeline"]
	bundleName := labels["kardinal.io/bundle"]
	envName := labels[labelEnvironment] // "kardinal.io/environment"
	gateName := gate.Name

	if pipelineName == "" || bundleName == "" {
		// Missing labels — omit audit event rather than write an incomplete one.
		return
	}

	now := metav1.Now()

	// Name: {gate}-{outcome}-{unix-ts} (truncated to 253 chars for K8s)
	outcomeSlug := "pass"
	if outcome == "Failure" {
		outcomeSlug = "fail"
	}
	rawName := fmt.Sprintf("%s-%s-%d", gateName, outcomeSlug, now.UnixNano()%100000)
	name := sanitizeK8sName(rawName)

	ae := &v1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: gate.Namespace,
			Labels: map[string]string{
				"kardinal.io/pipeline":    pipelineName,
				"kardinal.io/bundle":      bundleName,
				"kardinal.io/environment": envName,
				"kardinal.io/action":      AuditActionGateEvaluated,
			},
		},
		Spec: v1alpha1.AuditEventSpec{
			Timestamp:    now,
			BundleName:   bundleName,
			PipelineName: pipelineName,
			Environment:  envName,
			Action:       AuditActionGateEvaluated,
			Outcome:      outcome,
			Message:      fmt.Sprintf("gate=%s expr=%q %s", gate.Name, gate.Spec.Expression, message),
		},
	}

	if err := c.Create(ctx, ae); err != nil {
		if client.IgnoreAlreadyExists(err) != nil {
			// Non-conflict error — ignore; audit must not block gate evaluation.
			_ = err
		}
	}
}

// sanitizeK8sName truncates and lowercases a string to a valid Kubernetes name.
// Mirrors the same function in pkg/reconciler/promotionstep/audit.go.
func sanitizeK8sName(s string) string {
	if len(s) > 253 {
		s = s[:253]
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
