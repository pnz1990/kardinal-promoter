// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"strings"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// BlockedReason is the status.reason the PolicyGate reconciler writes on a
// gate instance whose expression evaluated to false. detail is the result
// ("bundle.version=1.2.0: !schedule.isWeekend = false", plus any stale
// metric note). When the gate has a spec.message, the explanation its author
// wrote for users, the reason leads with it and detail follows in
// parentheses:
//
//	Production deployments are blocked on weekends (bundle.version=1.2.0: !schedule.isWeekend = false)
//
// With no message it is detail unchanged. Evaluation errors, context errors
// and overrides keep their own reasons, whose prefixes the CLI relies on.
func BlockedReason(message, detail string) string {
	if message == "" {
		return detail
	}
	return message + " (" + detail + ")"
}

// BlockedDetail returns the detail BlockedReason was given when reason leads
// with message, and reason itself otherwise. Output that shows the message on
// its own uses it to print what the expression evaluated to without repeating
// the message.
func BlockedDetail(message, reason string) string {
	if message == "" {
		return reason
	}
	if d, ok := strings.CutPrefix(reason, message+" ("); ok {
		if d, ok := strings.CutSuffix(d, ")"); ok {
			return d
		}
	}
	return reason
}

// BlockedMessage returns gate's spec.message when gate blocks because its
// expression is false, that is when status.reason leads with the message
// (BlockedReason). It returns "" otherwise: no message, a ready gate, or one
// blocked by an evaluation or context error, which the message does not
// explain.
func BlockedMessage(gate *kardinalv1alpha1.PolicyGate) string {
	m := gate.Spec.Message
	if m == "" || gate.Status.Ready || !strings.HasPrefix(gate.Status.Reason, m+" (") {
		return ""
	}
	return m
}
