// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"regexp"
	"strings"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// WithVersion is a gate instance's status.reason when no message leads it:
// reason, then the version of the Bundle it was evaluated for in
// parentheses:
//
//	waiting for approvals: 0 of 1 (version 1.5.0)
//
// With no version it is reason unchanged.
func WithVersion(reason, version string) string {
	if version == "" {
		return reason
	}
	return reason + " (version " + version + ")"
}

// BlockedReason is the status.reason the PolicyGate reconciler writes on a
// gate instance whose expression evaluated to false. detail is the result
// ("!schedule.isWeekend = false", plus any stale metric note) and version the
// Bundle's version. When the gate has a spec.message, the explanation its
// author wrote for users, the reason leads with it and detail and version
// follow in parentheses:
//
//	Production deployments are blocked on weekends (!schedule.isWeekend = false; version 1.2.0)
//
// With no message it is WithVersion(detail, version). Evaluation errors,
// context errors and overrides keep their own reasons, whose prefixes the
// CLI relies on.
func BlockedReason(message, detail, version string) string {
	if message == "" {
		return WithVersion(detail, version)
	}
	if version != "" {
		detail += "; version " + version
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

// GateDetail is what gate's expression evaluated to, read from its
// status.reason without the message (BlockedDetail) and the version
// (status.bundleVersion): "!schedule.isWeekend = false". A controller older
// than status.bundleVersion led the result with "bundle.version=<v>: ";
// with no status.bundleVersion that prefix is dropped too.
func GateDetail(gate *kardinalv1alpha1.PolicyGate) string {
	d := BlockedDetail(gate.Spec.Message, gate.Status.Reason)
	v := gate.Status.BundleVersion
	if v == "" {
		if m := legacyVersionPrefix.FindString(d); m != "" {
			return d[len(m):]
		}
		return d
	}
	if t, ok := strings.CutSuffix(d, "; version "+v); ok {
		return t
	}
	return strings.TrimSuffix(d, " (version "+v+")")
}

// legacyVersionPrefix is how a controller older than status.bundleVersion
// began the result in a gate's reason.
var legacyVersionPrefix = regexp.MustCompile(`^bundle\.version=[^\s:]+: `)
