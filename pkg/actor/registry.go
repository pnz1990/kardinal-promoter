// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// MaxIndexBytes bounds the index entries of ready agents: an AgentIndex is
// one etcd object (1.5 MiB at most), and the rest is headroom for blocked
// entries and metadata.
const MaxIndexBytes = 1 << 20

// Reasons of an AgentIdentity's Ready condition.
const (
	ReasonValid           = "Valid"
	ReasonSubjectConflict = "SubjectConflict"
	ReasonInvalidSponsor  = "InvalidSponsor"
	ReasonIndexFull       = "IndexFull"
	// ReasonInvalidSigningIdentity: a spec.signingIdentities subjectRegExp
	// does not compile.
	ReasonInvalidSigningIdentity = "InvalidSigningIdentity"
)

// Verdict is the registry's decision on one AgentIdentity.
type Verdict struct {
	Ready   bool
	Reason  string
	Message string
}

// Registry is the outcome of Evaluate: a verdict per AgentIdentity and the
// index admission reads.
type Registry struct {
	Verdicts map[string]Verdict
	Index    v1alpha1.AgentIndexStatus
	// Full is true when an AgentIdentity was left out to stay under
	// MaxIndexBytes.
	Full bool
	// Bytes is the size of the entries as the bound counts them (each
	// entry and its key as stored in JSON, a colon and a comma), blocked
	// ones included, also when Full; MaxBytes is the bound.
	Bytes, MaxBytes int
}

// Evaluate decides which AgentIdentities take effect and builds the index,
// within maxBytes of entries.
// It is deterministic: AgentIdentities are taken oldest first (creation
// time, then name), and the older one keeps a subject two of them list.
// A not-ready or suspended AgentIdentity's subjects stay in the index as
// blocked, so an admin's mistake denies the agent rather than turning it into
// a person.
func Evaluate(ids []v1alpha1.AgentIdentity, maxBytes int) Registry {
	return evaluate(ids, maxBytes, false)
}

// EvaluateCompact is Evaluate in the compact format: the entries are in
// Index.CompactUsers and CompactGroups, counted by their encoded size
// against maxBytes, and the legacy maps hold only LegacyReaderGuard. An
// entry a separator would corrupt is written blocked, so it denies its agent.
func EvaluateCompact(ids []v1alpha1.AgentIdentity, maxBytes int) Registry {
	return evaluate(ids, maxBytes, true)
}

func evaluate(ids []v1alpha1.AgentIdentity, maxBytes int, compact bool) Registry {
	sorted := make([]v1alpha1.AgentIdentity, len(ids))
	copy(sorted, ids)
	sort.SliceStable(sorted, func(i, j int) bool {
		ti, tj := sorted[i].CreationTimestamp, sorted[j].CreationTimestamp
		if !ti.Equal(&tj) {
			return ti.Before(&tj)
		}
		return sorted[i].Name < sorted[j].Name
	})
	reg := Registry{
		Verdicts: map[string]Verdict{},
		Index: v1alpha1.AgentIndexStatus{
			ObservedIdentities: len(ids),
			Users:              map[string]v1alpha1.AgentIndexEntry{},
			Groups:             map[string]v1alpha1.AgentIndexEntry{},
		},
	}
	if compact {
		reg.Index.CompactUsers, reg.Index.CompactGroups = map[string]string{}, map[string]string{}
	}
	// Subjects of every AgentIdentity, ready or not: a sponsor cannot be one.
	userSubjects := map[string]string{}
	for _, id := range sorted {
		for _, s := range id.Spec.Subjects {
			if s.Kind != "Group" {
				if _, ok := userSubjects[SubjectKey(s)]; !ok {
					userSubjects[SubjectKey(s)] = id.Name
				}
			}
		}
	}
	owner := map[string]string{} // subject key (with kind) -> AgentIdentity name
	size := 0
	for _, id := range sorted {
		v := Verdict{Ready: true, Reason: ReasonValid, Message: "the agent is registered"}
		for _, s := range id.Spec.Subjects {
			if o, ok := owner[ownerKey(s)]; ok && o != id.Name {
				v = Verdict{Reason: ReasonSubjectConflict,
					Message: fmt.Sprintf("subject %s is already agent %s (the older AgentIdentity keeps it)", SubjectKey(s), o)}
				break
			}
		}
		if o, ok := userSubjects[id.Spec.Sponsor.User]; v.Ready && ok {
			v = Verdict{Reason: ReasonInvalidSponsor,
				Message: fmt.Sprintf("sponsor %s is agent %s: an agent cannot sponsor an agent", id.Spec.Sponsor.User, o)}
		}
		for i, si := range id.Spec.SigningIdentities {
			if si.SubjectRegExp == "" || !v.Ready {
				continue
			}
			if _, err := regexp.Compile("^(?:" + si.SubjectRegExp + ")$"); err != nil {
				v = Verdict{Reason: ReasonInvalidSigningIdentity,
					Message: fmt.Sprintf("signingIdentities[%d].subjectRegExp does not compile: %v", i, err)}
			}
		}
		entry := v1alpha1.AgentIndexEntry{
			Agent: id.Name, Sponsor: id.Spec.Sponsor.User,
			Namespaces: id.Spec.Scope.Namespaces, Pipelines: orAll(id.Spec.Scope.Pipelines),
			Environments: orAll(id.Spec.Scope.Environments), Autonomy: orDefault(id.Spec.Autonomy),
		}
		switch {
		case !v.Ready:
			// Compact: every subject of the AgentIdentity carries it.
			entry = v1alpha1.AgentIndexEntry{Agent: id.Name, Sponsor: id.Spec.Sponsor.User, Blocked: "not ready: " + v.Reason}
		case id.Spec.Suspended:
			entry.Blocked = "suspended"
		}
		reg.Verdicts[id.Name] = v
		for _, s := range id.Spec.Subjects {
			k := ownerKey(s)
			if _, ok := owner[k]; ok {
				continue // an older AgentIdentity's subject keeps its entry
			}
			owner[k] = id.Name
			key := SubjectKey(s)
			if s.Kind == "Group" {
				key = s.Name
			}
			if compact {
				enc, ok := EncodeEntry(entry)
				if !ok {
					enc, _ = EncodeEntry(v1alpha1.AgentIndexEntry{Agent: id.Name,
						Blocked: "its sponsor or scope holds a character the agent index cannot encode"})
				}
				size += compactSize(key, enc)
				if s.Kind == "Group" {
					reg.Index.CompactGroups[key] = enc
				} else {
					reg.Index.CompactUsers[key] = enc
				}
				continue
			}
			size += entrySize(key, entry)
			if s.Kind == "Group" {
				reg.Index.Groups[key] = entry
			} else {
				reg.Index.Users[key] = entry
			}
		}
	}
	if compact {
		// Older readers of the legacy maps fail closed (compact.go).
		reg.Index.Groups[LegacyReaderGroup] = LegacyReaderGuard
	}
	// Every entry counts, blocked ones too. An index past the bound is
	// written empty and not Ready, and admission then refuses every write it
	// judges (fail closed) until AgentIdentities are removed.
	reg.Bytes, reg.MaxBytes = size, maxBytes
	if size > maxBytes {
		reg.Full = true
		reg.Index.Users, reg.Index.Groups = map[string]v1alpha1.AgentIndexEntry{}, map[string]v1alpha1.AgentIndexEntry{}
		if compact {
			reg.Index.CompactUsers, reg.Index.CompactGroups = map[string]string{}, map[string]string{}
			reg.Index.Groups[LegacyReaderGroup] = LegacyReaderGuard
		}
		for name := range reg.Verdicts {
			reg.Verdicts[name] = Verdict{Reason: ReasonIndexFull, Message: fmt.Sprintf(
				"the agent index is full (%d bytes of %d): remove AgentIdentities or subjects; until then kardinal refuses "+
					"the writes the actor policy checks", size, maxBytes)}
		}
	}
	return reg
}

func ownerKey(s v1alpha1.AgentSubject) string {
	if s.Kind == "Group" {
		return "group:" + s.Name
	}
	return "user:" + SubjectKey(s)
}

// entrySize is an entry as the API server stores it: its key and its value
// in JSON (escapes included), a colon and a comma.
func entrySize(key string, e v1alpha1.AgentIndexEntry) int {
	raw, _ := json.Marshal(e)
	return len(raw) + jsonLen(key) + 2
}

// compactSize is entrySize for a compact entry.
func compactSize(key, enc string) int {
	return jsonLen(enc) + jsonLen(key) + 2
}

// jsonLen is the length of s as a JSON string, its quotes and escapes
// included.
func jsonLen(s string) int {
	raw, _ := json.Marshal(s)
	return len(raw)
}

func orAll(v []string) []string {
	if len(v) == 0 {
		return []string{"*"}
	}
	return v
}

func orDefault(a string) string {
	if strings.TrimSpace(a) == "" {
		return v1alpha1.AutonomyApprove
	}
	return a
}
