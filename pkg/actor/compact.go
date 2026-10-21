// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// The compact index format (#2071, docs/design/29-agent-index-scale.md).
//
// The API server converts an actor policy's whole param, the AgentIndex, for
// every request it matches, and a map of objects costs about 2.8 times a map
// of strings of the same entries. So once the chart's actor policies read it,
// the controller writes each entry as one string (CompactUsers,
// CompactGroups) instead of an object (Users, Groups).
//
// Which format is current is told by a field every writer rewrites: in the
// compact format Ready is False with reason CompactFormat. A controller of an
// older release decodes the index into its own type, so it drops the compact
// fields and leaves them stale on the server, but it overwrites Ready (Built
// or IndexFull) and the legacy maps. The readers of this release therefore
// read the compact maps only when Ready's reason is CompactFormat. The
// readers of older releases see Ready not True, or, for the one policy that
// does not check Ready (<release>-actor-records), every requester matches
// LegacyReaderGuard: they fail closed, never open.
const (
	// ReasonCompactFormat is Ready's reason while the index is in the
	// compact format.
	ReasonCompactFormat = "CompactFormat"
	// ConditionCompactReady is the readiness of the compact format.
	ConditionCompactReady = "CompactReady"
	// LabelIndexFormat on all of the chart's actor policies, with the value
	// FormatCompact, says they read the compact format: only then does the
	// controller write it.
	LabelIndexFormat = "kardinal.io/agent-index-format"
	// FormatCompact is LabelIndexFormat's value for the compact format.
	FormatCompact = "compact"

	// compactField separates an entry's fields, compactList a list's items:
	// control characters no username, name or scope pattern holds (an OIDC
	// username may hold "|", an LDAP DN ","). An entry whose fields hold
	// either is written blocked. DEL separates the fields, which every entry
	// has six of, because JSON, in which the API server stores the index
	// (and the bound counts it), keeps it as one byte; a C0 control
	// character takes six (\u001f), so it only separates list items, which
	// a list of one has none of.
	compactField = "\x7f"
	compactList  = "\x1f"
	// compactFields is how many fields an entry has; blocked is the last,
	// split off whole, so it may hold anything.
	compactFields = 7
)

// LegacyReaderGroup is the group LegacyReaderGuard is filed under in the
// legacy Groups map while the index is in the compact format: every
// authenticated requester has it.
const LegacyReaderGroup = "system:authenticated"

// LegacyReaderGuard is the legacy Groups entry of a compact index. A reader
// of an older release that judges a stamp without checking Ready (v0.12's
// <release>-actor-records; v0.13's refuses a compact index outright, #2384)
// finds it for every requester, so it admits only a stamp naming this
// agent and sponsor. Both are longer than an actor stamp's agent and sponsor
// may be (MaxActorNameLength), so the API server's schema refuses any write
// that names them before admission runs, whatever the client, and such a
// reader refuses every stamp instead of reading the empty legacy maps as "no
// agents" (QA-3 on #2372). A helm rollback leaves the CRDs, and their
// limits, in place.
var LegacyReaderGuard = v1alpha1.AgentIndexEntry{
	Agent: "kardinal's agent index is in the compact format of a newer release, which this release cannot read: " +
		"finish the upgrade (or the rollback) and retry. This name is longer than any actor stamp may name, so no " +
		"write can claim it and every stamp this release's records policy checks is refused",
	Sponsor: "nobody: this entry stands for every requester while the agent index is in the compact format of a newer " +
		"release; it is longer than any actor stamp may name, so no write can claim it, and the stamps this release " +
		"checks without reading the index's Ready condition are refused",
	Blocked: "kardinal's agent index is in the compact format of a newer release, which this release cannot read: " +
		"finish the upgrade (or the rollback) and retry",
}

// MaxActorNameLength is the longest agent and sponsor an actor stamp may
// name (v1alpha1.Actor's schema); LegacyReaderGuard's are longer.
const MaxActorNameLength = 253

// EncodeEntry is e in the compact format, and false when one of its fields
// (but Blocked) holds a separator.
func EncodeEntry(e v1alpha1.AgentIndexEntry) (string, bool) {
	list := func(v []string) (string, bool) {
		for _, s := range v {
			if s == "" || strings.ContainsAny(s, compactField+compactList) {
				return "", false
			}
		}
		return strings.Join(v, compactList), true
	}
	ns, ok1 := list(e.Namespaces)
	pl, ok2 := list(e.Pipelines)
	env, ok3 := list(e.Environments)
	for _, s := range []string{e.Agent, e.Sponsor, e.Autonomy} {
		if strings.ContainsAny(s, compactField+compactList) {
			return "", false
		}
	}
	if !ok1 || !ok2 || !ok3 {
		return "", false
	}
	return strings.Join([]string{e.Agent, e.Sponsor, e.Autonomy, ns, pl, env, e.Blocked}, compactField), true
}

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
// reason is CompactFormat. An older writer overwrites it.
func IsCompact(st *v1alpha1.AgentIndexStatus) bool {
	c := meta.FindStatusCondition(st.Conditions, "Ready")
	return c != nil && c.Reason == ReasonCompactFormat
}

// Readable returns the entries of st in the legacy shape (Users, Groups),
// whatever format is current, and false when the index is not built: in the
// compact format CompactReady is not True, in the legacy one Ready is not.
// Every reader of this release decides with it, so Derive, Scope and Verify
// work on either format.
func Readable(st *v1alpha1.AgentIndexStatus) (*v1alpha1.AgentIndexStatus, bool) {
	if st == nil {
		return nil, false
	}
	if !IsCompact(st) {
		// Compact maps an older writer left behind are stale: drop them.
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
