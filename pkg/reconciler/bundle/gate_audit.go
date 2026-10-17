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
	h := sha256.New()
	listed := make([]kardinalv1alpha1.AuditGateChange, 0, min(len(changes), kardinalv1alpha1.MaxAuditGateChanges))
	for i, c := range changes {
		gc := c.gate
		_, _ = fmt.Fprintf(h, "%s|%s|%d|%s|%d\n", c.key, gc.OldDigest, gc.OldGeneration, gc.NewDigest, gc.NewGeneration)
		if c.env != env {
			env = rejectionEnvironment
		}
		if i < kardinalv1alpha1.MaxAuditGateChanges {
			listed = append(listed, gc)
		}
	}
	omitted := len(changes) - len(listed)
	return audit.Entry(gateChangeName(b.Name, h.Sum(nil)), map[string]string{
		lifecycle.LabelPipeline: b.Spec.Pipeline,
		lifecycle.LabelBundle:   b.Name,
		"kardinal.io/action":    AuditActionGateChanged,
	}, kardinalv1alpha1.AuditEventSpec{
		BundleName:   b.Name,
		PipelineName: b.Spec.Pipeline,
		Environment:  env,
		Action:       AuditActionGateChanged,
		Outcome:      "Success",
		Message:      gateChangeMessage(b.Name, listed, omitted),
		Gates:        listed,
		GatesOmitted: int32(omitted),
	}, metav1.NewTime(now)), true
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
func gateChangeMessage(bundle string, listed []kardinalv1alpha1.AuditGateChange, omitted int) string {
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
	return fmt.Sprintf("%d PolicyGate %s changed under version %s: %s%s", n, noun, bundle, strings.Join(parts, "; "), more)
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
