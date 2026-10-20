// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
)

// AuditActionGateChanged is the AuditEvent action of a PolicyGate template
// change that reached a version in flight (#2144).
const AuditActionGateChanged = "GateChanged"

// gateChangeEntry returns the one outbox entry recording the template
// changes that a Promoting Bundle b's re-translated Graph applies, and false
// when there are none: before is what its gate instances carried
// (graph.TemplateStamps of the Graph before), gates the templates it was
// built with now. A template whose digest or generation differs is an edit
// (a reverted edit too: same digest, new generation), one only in gates is
// new, one only in before is deleted. A copy without a stamp (built before
// #1360) is a baseline, not a change: it records nothing, and the next
// change is measured from the stamp this rebuild writes.
//
// One rebuild is one entry, however many templates it changes (a policy
// repository sync may change dozens): it lists the first
// MaxAuditGateChanges in template order and counts the rest, so a bulk
// change cannot push other records out of the Bundle's bounded outbox. The
// entry is named after the Bundle and every change, so it is written once.
//
// beforeEnvs and afterEnvs are graph.TemplateEnvironments of the Graph before
// and after (afterEnvs nil when it could not be read): a stamped copy whose
// environments changed with no other change (a kardinal.io/applies-to edit)
// is recorded as moved, and an edit that also moves a gate says where (#2338).
func gateChangeEntry(b *kardinalv1alpha1.Bundle, pipeline *kardinalv1alpha1.Pipeline,
	before map[string]graph.TemplateStamp, beforeEnvs, afterEnvs map[string][]string,
	gates []kardinalv1alpha1.PolicyGate, now time.Time) (kardinalv1alpha1.PendingAuditEvent, bool) {
	type change struct {
		key  string
		gate kardinalv1alpha1.AuditGateChange
		env  string
	}
	var changes []change
	seen := map[string]bool{}
	for _, g := range translator.ApplicableGates(pipeline, gates) {
		key := g.Namespace + "/" + g.Name
		seen[key] = true
		digest := graph.TemplateDigest(g)
		old, had := before[key]
		var moved environmentMove
		if had && afterEnvs != nil {
			moved = environmentDiff(beforeEnvs[key], afterEnvs[key])
		}
		if had && (old.Digest == "" || old.Digest == digest && old.Generation == g.Generation && moved.none()) {
			continue
		}
		gc := kardinalv1alpha1.AuditGateChange{
			Template: g.Name, Namespace: g.Namespace, OldDigest: old.Digest, NewDigest: digest,
			OldGeneration: old.Generation, NewGeneration: g.Generation, Manager: lastManager(g.ManagedFields),
		}
		moved.set(&gc)
		changes = append(changes, change{key: key, env: gateEnvironment(g), gate: gc})
	}
	for key, old := range before {
		if seen[key] || old.Digest == "" {
			continue
		}
		ns, name, _ := strings.Cut(key, "/")
		changes = append(changes, change{key: key, env: rejectionEnvironment, gate: kardinalv1alpha1.AuditGateChange{
			Template: name, Namespace: ns, OldDigest: old.Digest, OldGeneration: old.Generation,
		}})
	}
	if len(changes) == 0 {
		return kardinalv1alpha1.PendingAuditEvent{}, false
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].key < changes[j].key })
	env := changes[0].env
	all := make([]kardinalv1alpha1.AuditGateChange, 0, len(changes))
	for _, c := range changes {
		if c.env != env {
			env = rejectionEnvironment
		}
		all = append(all, c.gate)
	}
	return gateChangeRecord(b.Name, b.Spec.Pipeline, env, all, 0, 0, metav1.NewTime(now)), true
}

// gateChangeRecord is the GateChanged outbox entry for changes (in template
// order) under version bundle, past which omitted more are known only by
// count. It lists the first MaxAuditGateChanges and counts the rest, and is
// named after the version and every change, so the same changes are
// written once.
func gateChangeRecord(bundle, pipeline, env string, changes []kardinalv1alpha1.AuditGateChange, omitted int,
	merged int32, at metav1.Time) kardinalv1alpha1.PendingAuditEvent {
	h := sha256.New()
	listed := make([]kardinalv1alpha1.AuditGateChange, 0, min(len(changes), kardinalv1alpha1.MaxAuditGateChanges))
	for i, gc := range changes {
		_, _ = fmt.Fprintf(h, "%s/%s|%s|%d|%s|%d\n", gc.Namespace, gc.Template, gc.OldDigest, gc.OldGeneration, gc.NewDigest, gc.NewGeneration)
		if !moveOf(gc).none() {
			_, _ = fmt.Fprintf(h, "+%s|-%s|%d\n", strings.Join(gc.EnvironmentsAdded, ","), strings.Join(gc.EnvironmentsRemoved, ","), gc.EnvironmentsOmitted)
		}
		if i < kardinalv1alpha1.MaxAuditGateChanges {
			listed = append(listed, gc)
		}
	}
	omitted += len(changes) - len(listed)
	_, _ = fmt.Fprintf(h, "+%d", omitted)
	return audit.Entry(gateChangeName(bundle, h.Sum(nil)), map[string]string{
		lifecycle.LabelPipeline: pipeline,
		lifecycle.LabelBundle:   bundle,
		"kardinal.io/action":    AuditActionGateChanged,
	}, kardinalv1alpha1.AuditEventSpec{
		BundleName:    bundle,
		PipelineName:  pipeline,
		Environment:   env,
		Action:        AuditActionGateChanged,
		Outcome:       "Success",
		Message:       gateChangeMessage(bundle, listed, omitted, merged),
		Gates:         listed,
		GatesOmitted:  int32(omitted),
		MergedRecords: merged,
	}, at)
}

func init() {
	audit.RegisterCoalescer(AuditActionGateChanged, coalesceGateChanges)
}

// coalesceGateChanges merges two unwritten GateChanged records of the same
// version, older first, when the audit outbox is full: one record then
// says how each template went from its stamp in the older record to its
// stamp in the newer, so a long write outage under repeated gate edits
// takes one outbox slot and never pushes out the version's other records.
// A template edited and edited back to the same stamp is no change. The
// counts of changes neither record listed add up. Past MaxAuditGateChanges
// a template can be omitted from the older record and listed in the newer:
// it then also stays in the older record's omitted count, and its first
// stamp is the newer record's old one. The bound makes this rare; the
// omitted count is then an upper bound.
func coalesceGateChanges(older, newer kardinalv1alpha1.PendingAuditEvent) (kardinalv1alpha1.PendingAuditEvent, bool) {
	o, n := older.Spec, newer.Spec
	if o.BundleName != n.BundleName || o.PipelineName != n.PipelineName {
		return kardinalv1alpha1.PendingAuditEvent{}, false
	}
	byKey := map[string]kardinalv1alpha1.AuditGateChange{}
	for _, gc := range o.Gates {
		byKey[gc.Namespace+"/"+gc.Template] = gc
	}
	for _, gc := range n.Gates {
		key := gc.Namespace + "/" + gc.Template
		if was, ok := byKey[key]; ok {
			gc.OldDigest, gc.OldGeneration = was.OldDigest, was.OldGeneration
			if gc.Manager == "" {
				gc.Manager = was.Manager
			}
			moveOf(was).then(moveOf(gc)).set(&gc)
		}
		byKey[key] = gc
	}
	changes := make([]kardinalv1alpha1.AuditGateChange, 0, len(byKey))
	for _, gc := range byKey {
		if gc.OldDigest == gc.NewDigest && gc.OldGeneration == gc.NewGeneration && moveOf(gc).none() {
			continue
		}
		changes = append(changes, gc)
	}
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].Namespace+"/"+changes[i].Template < changes[j].Namespace+"/"+changes[j].Template
	})
	if len(changes) == 0 && o.GatesOmitted+n.GatesOmitted == 0 {
		return kardinalv1alpha1.PendingAuditEvent{}, false
	}
	env := o.Environment
	if n.Environment != env {
		env = rejectionEnvironment
	}
	// audit.Enqueue sets the same count in spec.mergedRecords; the message
	// says it too, for a reader of the record alone.
	merged := max(o.MergedRecords, 1) + max(n.MergedRecords, 1)
	return gateChangeRecord(n.BundleName, n.PipelineName, env, changes, int(o.GatesOmitted+n.GatesOmitted), merged, n.Timestamp), true
}

// gateEnvironment is the environment a GateChanged record names: the
// template's kardinal.io/applies-to, or "*" when it names several.
func gateEnvironment(g kardinalv1alpha1.PolicyGate) string {
	env := strings.TrimSpace(g.Labels["kardinal.io/applies-to"])
	if env == "" || strings.Contains(env, ",") {
		return rejectionEnvironment
	}
	return env
}

// lastManager is the field manager of the newest of fields, or "".
func lastManager(fields []metav1.ManagedFieldsEntry) string {
	var name string
	var at time.Time
	for _, f := range fields {
		if f.Time != nil && !f.Time.Time.Before(at) {
			name, at = f.Manager, f.Time.Time
		}
	}
	return name
}

// gateChangeName is the AuditEvent name of the changes hashed into sum
// under Bundle bundle: fixed per set of changes, at most 253 characters.
func gateChangeName(bundle string, sum []byte) string {
	suffix := "-gate-" + hex.EncodeToString(sum)[:12]
	if len(bundle)+len(suffix) > 253 {
		bundle = bundle[:253-len(suffix)]
	}
	return bundle + suffix
}

// gateMessageChanges is how many changes a GateChanged message names; the
// record's spec.gates has the rest and every digest.
const gateMessageChanges = 5

// gateChangeMessage is the sentence of a GateChanged record: the first
// gateMessageChanges changes, then how many more there were. The digests
// are in spec.gates, not here, so the sentence stays short.
func gateChangeMessage(bundle string, listed []kardinalv1alpha1.AuditGateChange, omitted int, merged int32) string {
	n := len(listed) + omitted
	parts := make([]string, 0, gateMessageChanges)
	for i, gc := range listed {
		if i == gateMessageChanges {
			break
		}
		parts = append(parts, gateChangeClause(gc))
	}
	more := ""
	if rest := n - len(parts); rest > 0 {
		more = fmt.Sprintf("; and %d more", rest)
	}
	noun := "template"
	if n != 1 {
		noun = "templates"
	}
	head := fmt.Sprintf("%d PolicyGate %s changed under version %s", n, noun, bundle)
	if merged > 1 {
		// Before the changes, so a cut message keeps it.
		head += fmt.Sprintf(" (%d records merged, left unwritten during an audit write outage)", merged)
	}
	if len(parts) == 0 {
		return head
	}
	return head + ": " + strings.Join(parts, "; ") + more
}

// gateChangeClause says what change gc did to its template.
func gateChangeClause(gc kardinalv1alpha1.AuditGateChange) string {
	key := gc.Namespace + "/" + gc.Template
	var what string
	switch {
	case gc.NewDigest == "":
		what = fmt.Sprintf("was deleted (generation %d)", gc.OldGeneration)
	case gc.OldDigest == "":
		what = fmt.Sprintf("now applies (generation %d)", gc.NewGeneration)
	case gc.OldDigest == gc.NewDigest && gc.OldGeneration == gc.NewGeneration:
		what = fmt.Sprintf("moved (generation %d)", gc.NewGeneration)
	case gc.OldDigest == gc.NewDigest:
		what = fmt.Sprintf("was edited back to the same rule (generation %d to %d)", gc.OldGeneration, gc.NewGeneration)
	default:
		what = fmt.Sprintf("changed (generation %d to %d)", gc.OldGeneration, gc.NewGeneration)
	}
	if m := moveOf(gc); !m.none() {
		what += ": " + m.String()
	}
	if gc.Manager != "" {
		what += ", declared field manager " + gc.Manager
	}
	return key + " " + what
}

// upgradeGateChangeEntry returns the outbox entry recording the rule changes
// that re-rendering a Promoting Bundle b's Graph for the current
// graph.RenderVersion applied (#2271), and false when there are none. A
// Graph an earlier controller rendered kept the rule each template had when
// the version started (v0.12 did not carry template edits to versions in
// flight), so a template edited, created or deleted since reaches the
// version only now, at the upgrade: before and after are graph.TemplateRules
// of the Graph before and after the re-render, stamps and newStamps their
// graph.TemplateStamps, gates the templates it was built with. A change of
// stamp alone (a copy made before #1360 gets one) is not a rule change.
func upgradeGateChangeEntry(b *kardinalv1alpha1.Bundle, before, after map[string]string,
	stamps, newStamps map[string]graph.TemplateStamp, beforeEnvs, afterEnvs map[string][]string,
	gates []kardinalv1alpha1.PolicyGate, now time.Time) (kardinalv1alpha1.PendingAuditEvent, bool) {
	byKey := map[string]kardinalv1alpha1.PolicyGate{}
	for _, g := range gates {
		byKey[g.Namespace+"/"+g.Name] = g
	}
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var changes []kardinalv1alpha1.AuditGateChange
	var clauses []string
	env := ""
	for _, key := range sorted {
		old, had := before[key]
		cur, has := after[key]
		var moved environmentMove
		if had && has {
			moved = environmentDiff(beforeEnvs[key], afterEnvs[key])
		}
		if had && has && old == cur && moved.none() {
			continue
		}
		ns, name, _ := strings.Cut(key, "/")
		gc := kardinalv1alpha1.AuditGateChange{Template: name, Namespace: ns,
			OldDigest: stamps[key].Digest, OldGeneration: stamps[key].Generation}
		e := rejectionEnvironment
		switch {
		case has:
			gc.NewDigest, gc.NewGeneration = newStamps[key].Digest, newStamps[key].Generation
			if g, ok := byKey[key]; ok {
				gc.Manager, e = lastManager(g.ManagedFields), gateEnvironment(g)
			}
			moved.set(&gc)
			switch {
			case had && old == cur:
				clauses = append(clauses, fmt.Sprintf("%s moves (generation %d): %s", key, gc.NewGeneration, moved))
			case had && !moved.none():
				clauses = append(clauses, fmt.Sprintf("%s takes its current rule (generation %d): %s", key, gc.NewGeneration, moved))
			case had:
				clauses = append(clauses, fmt.Sprintf("%s takes its current rule (generation %d)", key, gc.NewGeneration))
			default:
				clauses = append(clauses, fmt.Sprintf("%s now applies (generation %d)", key, gc.NewGeneration))
			}
		default:
			clauses = append(clauses, key+" no longer applies")
		}
		switch {
		case env == "":
			env = e
		case env != e:
			env = rejectionEnvironment
		}
		changes = append(changes, gc)
	}
	if len(changes) == 0 {
		return kardinalv1alpha1.PendingAuditEvent{}, false
	}
	entry := gateChangeRecord(b.Name, b.Spec.Pipeline, env, changes, 0, 0, metav1.NewTime(now))
	noun := "template"
	if len(changes) != 1 {
		noun = "templates"
	}
	if len(clauses) > gateMessageChanges {
		clauses = append(clauses[:gateMessageChanges], fmt.Sprintf("and %d more", len(clauses)-gateMessageChanges))
	}
	entry.Spec.Message = fmt.Sprintf("%d PolicyGate %s changed under version %s on upgrade: its Graph, rendered by an earlier controller, "+
		"was rendered again (render version %s) and its gates took the current rules: %s",
		len(changes), noun, b.Name, graph.RenderLabel, strings.Join(clauses, "; "))
	return entry, true
}

// environmentMove is how a template's gate copies moved between the
// environments of a version: the environments they are in now and were not,
// and those they were in and are not (#2338). Lists hold every name; set
// writes the first kardinalv1alpha1.MaxAuditGateEnvironments of each to a
// record and counts the rest.
type environmentMove struct {
	added, removed []string
	omitted        int32
}

// environmentDiff is the move from the environments before to after (each
// sorted, graph.TemplateEnvironments).
func environmentDiff(before, after []string) environmentMove {
	in := map[string]bool{}
	for _, e := range before {
		in[e] = true
	}
	var m environmentMove
	for _, e := range after {
		if !in[e] {
			m.added = append(m.added, e)
		}
		delete(in, e)
	}
	for _, e := range before {
		if in[e] {
			m.removed = append(m.removed, e)
		}
	}
	return m
}

// moveOf is the move a record carries.
func moveOf(gc kardinalv1alpha1.AuditGateChange) environmentMove {
	return environmentMove{added: gc.EnvironmentsAdded, removed: gc.EnvironmentsRemoved, omitted: gc.EnvironmentsOmitted}
}

func (m environmentMove) none() bool {
	return len(m.added) == 0 && len(m.removed) == 0 && m.omitted == 0
}

// then is m followed by next, as a merge of two records sees them: every
// environment either added goes into added, and every one either removed
// into removed. An environment in both moved there and back (or back and
// there): kept, so a merge never erases a window in which a gate was off an
// environment of the version, as an edit and an edit back are kept with
// the generations (QA-2 on #2347). Counts of omitted names add up, an upper
// bound.
func (m environmentMove) then(next environmentMove) environmentMove {
	union := func(a, b []string) []string {
		in := map[string]bool{}
		for _, e := range append(append([]string(nil), a...), b...) {
			in[e] = true
		}
		out := make([]string, 0, len(in))
		for e := range in {
			out = append(out, e)
		}
		sort.Strings(out)
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return environmentMove{added: union(m.added, next.added), removed: union(m.removed, next.removed), omitted: m.omitted + next.omitted}
}

// set writes m to gc, the first MaxAuditGateEnvironments names of each list.
func (m environmentMove) set(gc *kardinalv1alpha1.AuditGateChange) {
	n := kardinalv1alpha1.MaxAuditGateEnvironments
	gc.EnvironmentsAdded, gc.EnvironmentsRemoved, gc.EnvironmentsOmitted = nil, nil, m.omitted
	if len(m.added) > 0 {
		gc.EnvironmentsAdded = append([]string(nil), m.added[:min(len(m.added), n)]...)
		gc.EnvironmentsOmitted += int32(len(m.added) - len(gc.EnvironmentsAdded))
	}
	if len(m.removed) > 0 {
		gc.EnvironmentsRemoved = append([]string(nil), m.removed[:min(len(m.removed), n)]...)
		gc.EnvironmentsOmitted += int32(len(m.removed) - len(gc.EnvironmentsRemoved))
	}
}

// String says the move: "now also in test; no longer in uat; moved and
// back in prod" (an environment both added and removed, after a merge), and
// how many more.
func (m environmentMove) String() string {
	removed := map[string]bool{}
	for _, e := range m.removed {
		removed[e] = true
	}
	var added, both []string
	for _, e := range m.added {
		if removed[e] {
			both = append(both, e)
			delete(removed, e)
		} else {
			added = append(added, e)
		}
	}
	var gone []string
	for _, e := range m.removed {
		if removed[e] {
			gone = append(gone, e)
		}
	}
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "now also in "+strings.Join(added, ", "))
	}
	if len(gone) > 0 {
		parts = append(parts, "no longer in "+strings.Join(gone, ", "))
	}
	if len(both) > 0 {
		parts = append(parts, "moved and back in "+strings.Join(both, ", "))
	}
	if m.omitted > 0 {
		parts = append(parts, fmt.Sprintf("and %d more environments", m.omitted))
	}
	return strings.Join(parts, "; ")
}
