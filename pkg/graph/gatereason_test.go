// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestBlockedReason: a gate's message leads its blocked reason and the CEL
// result and version follow in parentheses; with no message the reason is the
// result with the version after it.
func TestBlockedReason(t *testing.T) {
	const detail = "!schedule.isWeekend = false"
	assert.Equal(t, detail+" (version 1.2.0)", BlockedReason("", detail, "1.2.0"))
	assert.Equal(t, detail, BlockedReason("", detail, ""))
	assert.Equal(t, "Production deployments are blocked on weekends ("+detail+"; version 1.2.0)",
		BlockedReason("Production deployments are blocked on weekends", detail, "1.2.0"))
	assert.Equal(t, "Production deployments are blocked on weekends ("+detail+")",
		BlockedReason("Production deployments are blocked on weekends", detail, ""))
	assert.Equal(t, "waiting for approvals: 0 of 1 (version 6.14.1)", WithVersion("waiting for approvals: 0 of 1", "6.14.1"))
}

// TestBlockedDetail: BlockedDetail undoes the message of BlockedReason and
// leaves any other reason as it is.
func TestBlockedDetail(t *testing.T) {
	const msg, detail = "Production deployments are blocked on weekends", "x (y) = false"
	assert.Equal(t, detail, BlockedDetail(msg, BlockedReason(msg, detail, "")))
	assert.Equal(t, detail+"; version 1.2.0", BlockedDetail(msg, BlockedReason(msg, detail, "1.2.0")))
	assert.Equal(t, detail, BlockedDetail("", detail))
	assert.Equal(t, "CEL evaluation error: no such key: x", BlockedDetail(msg, "CEL evaluation error: no such key: x"))
	assert.Equal(t, msg+" (unclosed", BlockedDetail(msg, msg+" (unclosed"))
}

// TestGateDetail: GateDetail is what the expression evaluated to, with
// neither the message nor the version.
func TestGateDetail(t *testing.T) {
	const msg, detail = "Production deployments are blocked on weekends", "x (y) = false"
	gate := func(message, reason, version string) *kardinalv1alpha1.PolicyGate {
		return &kardinalv1alpha1.PolicyGate{
			Spec:   kardinalv1alpha1.PolicyGateSpec{Message: message},
			Status: kardinalv1alpha1.PolicyGateStatus{Reason: reason, BundleVersion: version},
		}
	}
	assert.Equal(t, detail, GateDetail(gate(msg, BlockedReason(msg, detail, "1.2.0"), "1.2.0")))
	assert.Equal(t, detail, GateDetail(gate("", BlockedReason("", detail, "1.2.0"), "1.2.0")))
	assert.Equal(t, detail, GateDetail(gate("", detail, "")))
	assert.Equal(t, "CEL evaluation error: x (version 1.2.0)", GateDetail(gate(msg, "CEL evaluation error: x (version 1.2.0)", "")),
		"a reason the version does not match is left as it is")
}

// TestBlockedMessage: the message is reported only for a gate that blocks
// because its expression is false, the only reason that leads with it.
func TestBlockedMessage(t *testing.T) {
	const msg = "Production deployments are blocked on weekends"
	gate := func(message string, ready bool, reason string) *kardinalv1alpha1.PolicyGate {
		return &kardinalv1alpha1.PolicyGate{
			Spec:   kardinalv1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend", Message: message},
			Status: kardinalv1alpha1.PolicyGateStatus{Ready: ready, Reason: reason},
		}
	}
	blocked := BlockedReason(msg, "!schedule.isWeekend = false", "1.2.0")
	assert.Equal(t, msg, BlockedMessage(gate(msg, false, blocked)))
	assert.Empty(t, BlockedMessage(gate("", false, "!schedule.isWeekend = false (version 1.2.0)")), "no message")
	assert.Empty(t, BlockedMessage(gate(msg, true, "!schedule.isWeekend = true (version 1.2.0)")), "ready")
	assert.Empty(t, BlockedMessage(gate(msg, false, "CEL evaluation error: no such key: x")), "evaluation error")
	assert.Empty(t, BlockedMessage(gate(msg, false, "context error: get bundle: not found")), "context error")
	assert.Empty(t, BlockedMessage(gate(msg, false, "")), "not evaluated yet")
}
