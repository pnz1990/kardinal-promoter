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
	"strings"

	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// maxObjectNameLength is the longest valid Kubernetes object name.
const maxObjectNameLength = 253

// writeGateAuditEvent creates an AuditEvent recording a PolicyGate readiness
// change at time at. patchStatus calls it when the gate first evaluates and
// on every ready flip.
//
// The name carries the outcome and the transition time, so every transition
// gets its own record (C04-gates-22); a fixed name per gate recorded only the
// first one. A failed write is logged, not returned: audit must never block
// gate evaluation.
func writeGateAuditEvent(
	ctx context.Context,
	c client.Client,
	gate *kardinalv1alpha1.PolicyGate,
	outcome, reason string,
	at metav1.Time,
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
	suffix := fmt.Sprintf("-gate-%s-%d", strings.ToLower(outcome), at.Unix())
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

	ae := &kardinalv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: gate.Namespace,
			Labels:    aeLabels,
		},
		Spec: kardinalv1alpha1.AuditEventSpec{
			Timestamp:    at,
			BundleName:   bundleName,
			PipelineName: pipelineName,
			Environment:  envName,
			Action:       action,
			Outcome:      outcome,
			Message:      reason,
		},
	}

	// AlreadyExists is the same transition written by an earlier attempt.
	err := c.Create(ctx, ae)
	switch {
	case client.IgnoreAlreadyExists(err) == nil:
	case apierrors.HasStatusCause(err, corev1.NamespaceTerminatingCause):
		// The namespace is being deleted: it refuses new objects, and its
		// deletion removes the gate and its records next.
		zerolog.Ctx(ctx).Debug().Err(err).
			Str("gate", gate.Name).Str("auditEvent", name).
			Msg("namespace is being deleted — PolicyGate AuditEvent not written")
	default:
		zerolog.Ctx(ctx).Warn().Err(err).
			Str("gate", gate.Name).Str("auditEvent", name).
			Msg("failed to write PolicyGate AuditEvent")
	}
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
