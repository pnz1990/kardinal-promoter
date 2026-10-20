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
func gateChangeEntry(b *kardinalv1alpha1.Bundle, pipeline *kardinalv1alpha1.Pipeline,
	before map[string]graph.TemplateStamp, gates []kardinalv1alpha1.PolicyGate, now time.Time) (kardinalv1alpha1.PendingAuditEvent, bool) {
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
		if had && (old.Digest == "" || old.Digest == digest && old.Generation == g.Generation) {
			continue
		}
		changes = append(changes, change{key: key, env: gateEnvironment(g), gate: kardinalv1alpha1.AuditGateChange{
			Template: g.Name, Namespace: g.Namespace, OldDigest: old.Digest, NewDigest: digest,
			OldGeneration: old.Generation, NewGeneration: g.Generation, Manager: lastManager(g.ManagedFields),
		}})
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
		}
		byKey[key] = gc
	}
	changes := make([]kardinalv1alpha1.AuditGateChange, 0, len(byKey))
	for _, gc := range byKey {
		if gc.OldDigest == gc.NewDigest && gc.OldGeneration == gc.NewGeneration {
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
	case gc.OldDigest == gc.NewDigest:
		what = fmt.Sprintf("was edited back to the same rule (generation %d to %d)", gc.OldGeneration, gc.NewGeneration)
	default:
		what = fmt.Sprintf("changed (generation %d to %d)", gc.OldGeneration, gc.NewGeneration)
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
	stamps, newStamps map[string]graph.TemplateStamp, gates []kardinalv1alpha1.PolicyGate, now time.Time) (kardinalv1alpha1.PendingAuditEvent, bool) {
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
		if had && has && old == cur {
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
			if had {
				clauses = append(clauses, fmt.Sprintf("%s takes its current rule (generation %d)", key, gc.NewGeneration))
			} else {
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
