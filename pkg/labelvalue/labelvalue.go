// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package labelvalue fits a user-controlled name into a Kubernetes label
// value. A name can be 253 characters (a ChangeWindow, a Subscription, a
// MetricCheck or AnalysisTemplate), a label value only 63, and a label the
// API server refuses fails the whole write, often far from where the name
// was chosen (#2034).
package labelvalue

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// prefix is how much of a name that does not fit is kept, so a shortened
// value still reads as the name; the rest is a hash of the whole name.
const prefix = 52

// Fit is name when it is a valid label value, else its first 52 characters
// (without trailing '-', '_' or '.') and the first 10 hex characters of the
// SHA-256 of name, which is a valid label value of at most 63 characters.
// It is deterministic, so a value written with it is selected with it too,
// and names that differ give different values. A name that is already valid
// is unchanged, so labels written before Fit stay matched.
func Fit(name string) string {
	if len(validation.IsValidLabelValue(name)) == 0 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	head := strings.TrimRight(name[:min(len(name), prefix)], "-_.")
	return head + "-" + hex.EncodeToString(sum[:])[:10]
}
