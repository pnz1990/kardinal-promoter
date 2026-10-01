// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"testing"

	"github.com/stretchr/testify/assert"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestBlockedReason: a gate's message leads its blocked reason and the CEL
// result follows in parentheses; with no message the reason is the result,
// as before GATE-MESSAGE-01.
func TestBlockedReason(t *testing.T) {
	const detail = "bundle.version=1.2.0: !schedule.isWeekend = false"
	assert.Equal(t, detail, BlockedReason("", detail))
	assert.Equal(t, "Production deployments are blocked on weekends ("+detail+")",
		BlockedReason("Production deployments are blocked on weekends", detail))
}

// TestBlockedDetail: BlockedDetail undoes BlockedReason and leaves any other
// reason as it is.
func TestBlockedDetail(t *testing.T) {
	const msg, detail = "Production deployments are blocked on weekends", "bundle.version=1.2.0: x (y) = false"
	assert.Equal(t, detail, BlockedDetail(msg, BlockedReason(msg, detail)))
	assert.Equal(t, detail, BlockedDetail("", detail))
	assert.Equal(t, "CEL evaluation error: no such key: x", BlockedDetail(msg, "CEL evaluation error: no such key: x"))
	assert.Equal(t, msg+" (unclosed", BlockedDetail(msg, msg+" (unclosed"))
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
	blocked := BlockedReason(msg, "bundle.version=1.2.0: !schedule.isWeekend = false")
	assert.Equal(t, msg, BlockedMessage(gate(msg, false, blocked)))
	assert.Empty(t, BlockedMessage(gate("", false, "bundle.version=1.2.0: !schedule.isWeekend = false")), "no message")
	assert.Empty(t, BlockedMessage(gate(msg, true, "bundle.version=1.2.0: !schedule.isWeekend = true")), "ready")
	assert.Empty(t, BlockedMessage(gate(msg, false, "CEL evaluation error: no such key: x")), "evaluation error")
	assert.Empty(t, BlockedMessage(gate(msg, false, "context error: get bundle: not found")), "context error")
	assert.Empty(t, BlockedMessage(gate(msg, false, "")), "not evaluated yet")
}
