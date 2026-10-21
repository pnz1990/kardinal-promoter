// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// The compact index format (#2071, docs/design/29-agent-index-scale.md),
// which a later release's controller writes and this release reads, so that
// its CLI keeps stamping agents, and its controller keeps answering the UI
// API and the Bundle API, against a cluster that runs the later release.
//
// In the compact format each entry is one string (CompactUsers,
// CompactGroups) instead of an object (Users, Groups), and Ready is False
// with reason CompactFormat; CompactReady says whether it is built. A
// controller of this release writes the legacy format over it, rewriting
// Ready, and leaves the compact maps stale, so the compact maps are read only
// while Ready's reason is CompactFormat. This release's actor policies read
// only the legacy format: admission decides either way.
const (
	// ReasonCompactFormat is Ready's reason while the index is in the
	// compact format.
	ReasonCompactFormat = "CompactFormat"
	// ConditionCompactReady is the readiness of the compact format.
	ConditionCompactReady = "CompactReady"

	// An entry is "agent F sponsor F autonomy F namespaces F pipelines F
	// environments F blocked", F compactField (DEL), each list's items
	// joined with compactList; blocked is the last of compactFields fields,
	// split off whole, so it may hold anything.
	compactField  = "\x7f"
	compactList   = "\x1f"
	compactFields = 7
)

// DecodeEntry is the entry s encodes; an entry that does not have every
// field decodes blocked.
func DecodeEntry(s string) v1alpha1.AgentIndexEntry {
	f := strings.SplitN(s, compactField, compactFields)
	if len(f) != compactFields {
		return v1alpha1.AgentIndexEntry{Blocked: "the agent index entry is malformed"}
	}
	list := func(v string) []string {
		if v == "" {
			return nil
		}
		return strings.Split(v, compactList)
	}
	return v1alpha1.AgentIndexEntry{Agent: f[0], Sponsor: f[1], Autonomy: f[2],
		Namespaces: list(f[3]), Pipelines: list(f[4]), Environments: list(f[5]), Blocked: f[6]}
}

// IsCompact reports whether st's current format is the compact one: Ready's
// reason is CompactFormat. A writer of the legacy format overwrites it.
func IsCompact(st *v1alpha1.AgentIndexStatus) bool {
	c := meta.FindStatusCondition(st.Conditions, "Ready")
	return c != nil && c.Reason == ReasonCompactFormat
}

// Readable returns the entries of st in the legacy shape (Users, Groups),
// whatever format is current, and false when the index is not built: in the
// compact format CompactReady is not True, in the legacy one Ready is not.
// Source reads the index through it, so Derive, Scope and VerifyStamp work
// on either format.
func Readable(st *v1alpha1.AgentIndexStatus) (*v1alpha1.AgentIndexStatus, bool) {
	if st == nil {
		return nil, false
	}
	if !IsCompact(st) {
		// Compact maps a writer of the legacy format left behind are
		// stale: drop them.
		out := *st
		out.CompactUsers, out.CompactGroups = nil, nil
		return &out, meta.IsStatusConditionTrue(st.Conditions, "Ready")
	}
	if !meta.IsStatusConditionTrue(st.Conditions, ConditionCompactReady) {
		return nil, false
	}
	out := *st
	out.Users = make(map[string]v1alpha1.AgentIndexEntry, len(st.CompactUsers))
	for k, v := range st.CompactUsers {
		out.Users[k] = DecodeEntry(v)
	}
	out.Groups = make(map[string]v1alpha1.AgentIndexEntry, len(st.CompactGroups))
	for k, v := range st.CompactGroups {
		out.Groups[k] = DecodeEntry(v)
	}
	out.CompactUsers, out.CompactGroups = nil, nil
	return &out, true
}
