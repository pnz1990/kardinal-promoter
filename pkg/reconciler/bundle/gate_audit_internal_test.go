// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package bundle

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

func gateAuditGate(name, env, expr string, gen int64) kardinalv1alpha1.PolicyGate {
	return kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "platform-policies",
		Generation: gen, Labels: map[string]string{"kardinal.io/applies-to": env}},
		Spec: kardinalv1alpha1.PolicyGateSpec{Expression: expr}}
}

func gateAuditFixture() (*kardinalv1alpha1.Pipeline, *kardinalv1alpha1.Bundle) {
	p := &kardinalv1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app"},
		Spec: kardinalv1alpha1.PipelineSpec{Environments: []kardinalv1alpha1.EnvironmentSpec{{Name: "uat"}, {Name: "prod"}}}}
	b := &kardinalv1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v1"}, Spec: kardinalv1alpha1.BundleSpec{Pipeline: "app"}}
	return p, b
}

// #2144: the record of a re-translate under a Promoting version. An edit
// names both stamps and the declared field manager of the last write; a
// reverted edit says the rule is the same; a new template has no old stamp
// and a deleted one no new stamp; an unchanged template and one of another
// environment are not recorded. One rebuild is one record, named after its
// changes.
//
// Covers GATE-EDIT-02.
func TestGateChangeEntry(t *testing.T) {
	t1, t2 := metav1.NewTime(time.Unix(100, 0)), metav1.NewTime(time.Unix(200, 0))
	edited := gateAuditGate("edited", "prod", "true", 4)
	edited.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "argocd-controller", Time: &t1}, {Manager: "kubectl-edit", Time: &t2}}
	old := gateAuditGate("edited", "prod", "false", 3)
	reverted := gateAuditGate("reverted", "prod", "true", 5)
	unchanged := gateAuditGate("unchanged", "uat,prod", "true", 2)
	added := gateAuditGate("added", "uat", "true", 1)
	elsewhere := gateAuditGate("elsewhere", "staging", "false", 9)
	before := map[string]graph.TemplateStamp{
		"platform-policies/edited":    {Digest: graph.TemplateDigest(old), Generation: 3},
		"platform-policies/reverted":  {Digest: graph.TemplateDigest(reverted), Generation: 2},
		"platform-policies/unchanged": {Digest: graph.TemplateDigest(unchanged), Generation: 2},
		"platform-policies/deleted":   {Digest: "sha256:old", Generation: 7},
	}
	p, b := gateAuditFixture()
	now := time.Unix(300, 0)
	gates := []kardinalv1alpha1.PolicyGate{edited, reverted, unchanged, added, elsewhere}
	e, ok := gateChangeEntry(b, p, before, gates, now)
	require.True(t, ok)
	assert.Equal(t, "GateChanged", e.Spec.Action)
	assert.Equal(t, "app-v1", e.Spec.BundleName)
	assert.Equal(t, "app", e.Spec.PipelineName)
	assert.Equal(t, "*", e.Spec.Environment, "changes in several environments")
	assert.LessOrEqual(t, len(e.Name), 253)
	assert.Zero(t, e.Spec.GatesOmitted)
	require.Len(t, e.Spec.Gates, 4)
	byTemplate := map[string]kardinalv1alpha1.AuditGateChange{}
	var order []string
	for _, gc := range e.Spec.Gates {
		byTemplate[gc.Template] = gc
		order = append(order, gc.Template)
	}
	assert.Equal(t, []string{"added", "deleted", "edited", "reverted"}, order, "template order")
	assert.Equal(t, kardinalv1alpha1.AuditGateChange{Template: "edited", Namespace: "platform-policies",
		OldDigest: graph.TemplateDigest(old), OldGeneration: 3, NewDigest: graph.TemplateDigest(edited), NewGeneration: 4,
		Manager: "kubectl-edit"}, byTemplate["edited"])
	assert.Empty(t, byTemplate["added"].OldDigest)
	assert.Empty(t, byTemplate["deleted"].NewDigest)
	assert.Equal(t, int64(2), byTemplate["reverted"].OldGeneration)
	msg := e.Spec.Message
	assert.Contains(t, msg, "4 PolicyGate templates changed under version app-v1: ")
	assert.Contains(t, msg, "platform-policies/edited changed (generation 3 to 4), declared field manager kubectl-edit")
	assert.Contains(t, msg, "platform-policies/reverted was edited back to the same rule (generation 2 to 5)")
	assert.Contains(t, msg, "platform-policies/added now applies (generation 1)")
	assert.Contains(t, msg, "platform-policies/deleted was deleted (generation 7)")
	assert.NotContains(t, msg, "and ", "four changes, all named")

	again, ok := gateChangeEntry(b, p, before, gates, now.Add(time.Hour))
	require.True(t, ok)
	assert.Equal(t, e.Name, again.Name, "the same changes have the same record name")
	other, _ := gateChangeEntry(b, p, before, []kardinalv1alpha1.PolicyGate{edited, unchanged, added, elsewhere}, now)
	assert.NotEqual(t, e.Name, other.Name, "other changes, another record")

	// One environment: the record names it.
	one, ok := gateChangeEntry(b, p, map[string]graph.TemplateStamp{}, []kardinalv1alpha1.PolicyGate{added}, now)
	require.True(t, ok)
	assert.Equal(t, "uat", one.Spec.Environment)
	assert.Contains(t, one.Spec.Message, "1 PolicyGate template changed under version app-v1: ")

	_, ok = gateChangeEntry(b, p, map[string]graph.TemplateStamp{
		"platform-policies/unchanged": {Digest: graph.TemplateDigest(unchanged), Generation: 2},
	}, []kardinalv1alpha1.PolicyGate{unchanged, elsewhere}, now)
	assert.False(t, ok, "nothing changed, nothing recorded")
}

// QA on #2146: a bulk change (a policy repository sync editing 40
// templates) is one record listing MaxAuditGateChanges changes and counting
// the rest, so the PromotionStarted and GateOverridden entries already in
// the Bundle's outbox stay.
func TestGateChangeEntry_BulkChangeIsOneRecord(t *testing.T) {
	p, b := gateAuditFixture()
	before := map[string]graph.TemplateStamp{}
	var gates []kardinalv1alpha1.PolicyGate
	for i := range 40 {
		g := gateAuditGate(fmt.Sprintf("org-gate-%02d", i), "prod", "true", 2)
		was := gateAuditGate(g.Name, "prod", "false", 1)
		before["platform-policies/"+g.Name] = graph.TemplateStamp{Digest: graph.TemplateDigest(was), Generation: 1}
		gates = append(gates, g)
	}
	e, ok := gateChangeEntry(b, p, before, gates, time.Unix(300, 0))
	require.True(t, ok)
	assert.Len(t, e.Spec.Gates, kardinalv1alpha1.MaxAuditGateChanges)
	assert.Equal(t, int32(40-kardinalv1alpha1.MaxAuditGateChanges), e.Spec.GatesOmitted)
	assert.Equal(t, "org-gate-00", e.Spec.Gates[0].Template)
	assert.Equal(t, "org-gate-19", e.Spec.Gates[19].Template)
	assert.Contains(t, e.Spec.Message, "40 PolicyGate templates changed")
	assert.Contains(t, e.Spec.Message, "platform-policies/org-gate-04 changed (generation 1 to 2); and 35 more")
	assert.NotContains(t, e.Spec.Message, "org-gate-05", "the message names five; spec.gates lists twenty")
	assert.Less(t, len(e.Spec.Message), audit.MaxMessageBytes, "not truncated")

	ctx := context.Background()
	pending := []kardinalv1alpha1.PendingAuditEvent{
		audit.Entry("app-v1-started", nil, kardinalv1alpha1.AuditEventSpec{Action: "PromotionStarted", Outcome: "Success"}, metav1.Now()),
		audit.Entry("app-v1-override", nil, kardinalv1alpha1.AuditEventSpec{Action: "GateOverridden", Outcome: "Success"}, metav1.Now()),
	}
	pending = audit.Enqueue(ctx, auditKind, pending, e)
	actions := map[string]int{}
	for _, pe := range pending {
		actions[pe.Spec.Action]++
	}
	assert.Equal(t, map[string]int{"PromotionStarted": 1, "GateOverridden": 1, "GateChanged": 1}, actions)
}

// QA on #2146: a Graph built before #1360 has gate copies without a stamp.
// They are the baseline: neither the template edited nor the others are
// recorded (no "changed from unknown digest" for every template), a
// template that was not there before still is, and the next change is
// measured from the stamp this rebuild writes.
func TestGateChangeEntry_UnstampedCopiesAreTheBaseline(t *testing.T) {
	p, b := gateAuditFixture()
	edited := gateAuditGate("edited", "prod", "true", 4)
	other := gateAuditGate("other", "prod", "true", 2)
	before := map[string]graph.TemplateStamp{
		"platform-policies/edited":  {},
		"platform-policies/other":   {},
		"platform-policies/deleted": {},
	}
	_, ok := gateChangeEntry(b, p, before, []kardinalv1alpha1.PolicyGate{edited, other}, time.Unix(300, 0))
	assert.False(t, ok, "unstamped copies record nothing, edited, unchanged or deleted")

	added := gateAuditGate("added", "prod", "true", 1)
	e, ok := gateChangeEntry(b, p, before, []kardinalv1alpha1.PolicyGate{edited, other, added}, time.Unix(300, 0))
	require.True(t, ok)
	require.Len(t, e.Spec.Gates, 1)
	assert.Equal(t, "added", e.Spec.Gates[0].Template)

	// After the rebuild the copies carry stamps: the next edit is recorded.
	stamped := map[string]graph.TemplateStamp{
		"platform-policies/edited": {Digest: graph.TemplateDigest(edited), Generation: 4},
		"platform-policies/other":  {Digest: graph.TemplateDigest(other), Generation: 2},
	}
	again := gateAuditGate("edited", "prod", "false", 5)
	e, ok = gateChangeEntry(b, p, stamped, []kardinalv1alpha1.PolicyGate{again, other}, time.Unix(400, 0))
	require.True(t, ok)
	require.Len(t, e.Spec.Gates, 1)
	assert.Equal(t, graph.TemplateDigest(edited), e.Spec.Gates[0].OldDigest)
}

// QA's residual risk on #2146: during a long write outage, repeated gate
// edits fill a version's outbox with GateChanged records. A full outbox
// merges them, oldest pair first, before it drops anything: the
// PromotionStarted record stays, and the merged record says how each
// template went from its first recorded stamp to its last.
//
// Covers AUDIT-OUTBOX-02.
func TestGateChanged_FullOutboxMergesBeforeDropping(t *testing.T) {
	ctx := context.Background()
	p, b := gateAuditFixture()
	at := metav1.NewTime(time.Unix(300, 0))
	started := audit.Entry("app-v1-started", map[string]string{"kardinal.io/bundle": "app-v1"}, kardinalv1alpha1.AuditEventSpec{
		BundleName: "app-v1", PipelineName: "app", Environment: "test", Action: "PromotionStarted", Outcome: "Success"}, at)
	pending := []kardinalv1alpha1.PendingAuditEvent{started}

	// Edit "hold" again and again: generation g to g+1 each time, and one
	// other template per edit.
	digest := func(gen int64) string {
		return graph.TemplateDigest(gateAuditGate("hold", "prod", fmt.Sprintf("x == %d", gen), gen))
	}
	edit := func(gen int64) kardinalv1alpha1.PendingAuditEvent {
		hold := gateAuditGate("hold", "prod", fmt.Sprintf("x == %d", gen+1), gen+1)
		other := gateAuditGate(fmt.Sprintf("other-%02d", gen), "prod", "true", 1)
		before := map[string]graph.TemplateStamp{"platform-policies/hold": {Digest: digest(gen), Generation: gen}}
		e, ok := gateChangeEntry(b, p, before, []kardinalv1alpha1.PolicyGate{hold, other}, time.Unix(300+gen, 0))
		require.True(t, ok)
		return e
	}
	for gen := int64(1); gen <= int64(kardinalv1alpha1.MaxPendingAuditEvents)+5; gen++ {
		pending = audit.Enqueue(ctx, auditKind, pending, edit(gen))
	}
	require.Len(t, pending, kardinalv1alpha1.MaxPendingAuditEvents)
	assert.Equal(t, "app-v1-started", pending[0].Name, "PromotionStarted is not pushed out")
	var gateChanged int
	for _, e := range pending {
		if e.Spec.Action == AuditActionGateChanged {
			gateChanged++
		}
	}
	assert.Equal(t, kardinalv1alpha1.MaxPendingAuditEvents-1, gateChanged)

	// The oldest GateChanged is the merge of the first edits: hold from its
	// first stamp to the stamp of the last edit merged into it.
	merged := pending[1].Spec
	var hold *kardinalv1alpha1.AuditGateChange
	for i, gc := range merged.Gates {
		if gc.Template == "hold" {
			hold = &merged.Gates[i]
		}
	}
	require.NotNil(t, hold)
	assert.Equal(t, digest(1), hold.OldDigest)
	assert.Equal(t, int64(1), hold.OldGeneration)
	assert.Greater(t, hold.NewGeneration, int64(2), "later edits merged in")
	assert.Greater(t, len(merged.Gates), 2, "the other templates of the merged edits are listed")
	assert.Greater(t, merged.MergedRecords, int32(2), "the merged record counts the records it replaces")
	assert.Contains(t, merged.Message, "records merged, left unwritten during an audit write outage")
}

// Records of different versions are not merged.
func TestCoalesceGateChanges_SameVersionOnly(t *testing.T) {
	p, b := gateAuditFixture()
	added := gateAuditGate("added", "uat", "true", 1)
	e1, ok := gateChangeEntry(b, p, map[string]graph.TemplateStamp{}, []kardinalv1alpha1.PolicyGate{added}, time.Unix(300, 0))
	require.True(t, ok)
	other := *b
	other.Name = "app-v2"
	e2, ok := gateChangeEntry(&other, p, map[string]graph.TemplateStamp{}, []kardinalv1alpha1.PolicyGate{added}, time.Unix(301, 0))
	require.True(t, ok)
	_, ok = coalesceGateChanges(e1, e2)
	assert.False(t, ok)

	// The same version: deleted, then added again, is one change from the
	// deleted stamp to the new one.
	gone := map[string]graph.TemplateStamp{"platform-policies/added": {Digest: "sha256:old", Generation: 3}}
	d, ok := gateChangeEntry(b, p, gone, nil, time.Unix(302, 0))
	require.True(t, ok)
	m, ok := coalesceGateChanges(d, e1)
	require.True(t, ok)
	require.Len(t, m.Spec.Gates, 1)
	assert.Equal(t, "sha256:old", m.Spec.Gates[0].OldDigest)
	assert.Equal(t, graph.TemplateDigest(added), m.Spec.Gates[0].NewDigest)
	assert.Equal(t, "*", m.Spec.Environment, "the deletion names every environment")
}

// QA-2 on #2160: a merge is deterministic (the gates sorted, whatever order
// the newer record lists them in, and the same name), adds both records'
// omitted counts, merges test and prod into "*", and says how many
// records it replaces.
func TestCoalesceGateChanges_Properties(t *testing.T) {
	change := func(name string, gen int64) kardinalv1alpha1.AuditGateChange {
		return kardinalv1alpha1.AuditGateChange{Template: name, Namespace: "platform-policies",
			OldDigest: fmt.Sprintf("sha256:%s-%d", name, gen), OldGeneration: gen,
			NewDigest: fmt.Sprintf("sha256:%s-%d", name, gen+1), NewGeneration: gen + 1}
	}
	record := func(env string, omitted int, gates ...kardinalv1alpha1.AuditGateChange) kardinalv1alpha1.PendingAuditEvent {
		return gateChangeRecord("app-v1", "app", env, gates, omitted, 0, metav1.NewTime(time.Unix(300, 0)))
	}
	older := record("test", 2, change("charlie", 1), change("alpha", 1))
	newer := record("prod", 3, change("delta", 4), change("bravo", 2), change("echo", 7))
	shuffled := newer
	shuffled.Spec.Gates = []kardinalv1alpha1.AuditGateChange{change("echo", 7), change("delta", 4), change("bravo", 2)}

	a, ok := coalesceGateChanges(older, newer)
	require.True(t, ok)
	b, ok := coalesceGateChanges(older, shuffled)
	require.True(t, ok)
	again, ok := coalesceGateChanges(older, newer)
	require.True(t, ok)
	var names []string
	for _, gc := range a.Spec.Gates {
		names = append(names, gc.Template)
	}
	assert.Equal(t, []string{"alpha", "bravo", "charlie", "delta", "echo"}, names, "sorted by template")
	assert.Equal(t, a.Spec.Gates, b.Spec.Gates, "the newer record's order does not matter")
	assert.Equal(t, a.Name, b.Name)
	assert.Equal(t, a.Name, again.Name, "the same merge, the same name")

	assert.Equal(t, int32(5), a.Spec.GatesOmitted, "both records' omitted counts")
	assert.Contains(t, a.Spec.Message, "10 PolicyGate templates changed")
	assert.Contains(t, a.Spec.Message, "; and 5 more")

	assert.Equal(t, "*", a.Spec.Environment, "test and prod merge into every environment")
	same, ok := coalesceGateChanges(record("prod", 0, change("alpha", 1)), record("prod", 0, change("bravo", 1)))
	require.True(t, ok)
	assert.Equal(t, "prod", same.Spec.Environment, "one environment stays")

	assert.Equal(t, int32(2), a.Spec.MergedRecords)
	assert.Contains(t, a.Spec.Message, "(2 records merged, left unwritten during an audit write outage)")
	three, ok := coalesceGateChanges(a, record("prod", 0, change("foxtrot", 1)))
	require.True(t, ok)
	assert.Equal(t, int32(3), three.Spec.MergedRecords, "a merged record counts what it replaced")
	assert.Zero(t, record("prod", 0, change("alpha", 1)).Spec.MergedRecords, "a record as recorded has none")
}
