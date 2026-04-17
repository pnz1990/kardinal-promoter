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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// writeGateAuditEvent creates an AuditEvent recording a PolicyGate readiness
// change. Called by patchStatus only on state transitions (ready flip).
// Fire-and-forget: errors are dropped — audit must never block gate evaluation.
func writeGateAuditEvent(
	ctx context.Context,
	c client.Client,
	gate *kardinalv1alpha1.PolicyGate,
	outcome, reason string,
) {
	if c == nil || gate == nil {
		return
	}

	labels := gate.GetLabels()
	pipelineName := labels["kardinal.io/pipeline"]
	bundleName := labels["kardinal.io/bundle"]
	envName := labels["kardinal.io/environment"]
	if pipelineName == "" || bundleName == "" {
		return
	}

	action := "GateEvaluated"
	now := metav1.Now()

	// Name: {gate.Name}-gate-evaluated (truncated)
	rawName := fmt.Sprintf("%s-gate-evaluated", gate.Name)
	name := sanitizeGateName(rawName)

	ae := &kardinalv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: gate.Namespace,
			Labels: map[string]string{
				"kardinal.io/pipeline":    pipelineName,
				"kardinal.io/bundle":      bundleName,
				"kardinal.io/environment": envName,
				"kardinal.io/action":      action,
				"kardinal.io/gate":        gate.Labels["kardinal.io/gate-template"],
			},
		},
		Spec: kardinalv1alpha1.AuditEventSpec{
			Timestamp:    now,
			BundleName:   bundleName,
			PipelineName: pipelineName,
			Environment:  envName,
			Action:       action,
			Outcome:      outcome,
			Message:      reason,
		},
	}

	if err := c.Create(ctx, ae); err != nil {
		// AlreadyExists = idempotent re-reconcile; non-conflict errors are silently ignored.
		_ = client.IgnoreAlreadyExists(err)
	}
}

// sanitizeGateName produces a valid Kubernetes name from a gate event name.
func sanitizeGateName(s string) string {
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
