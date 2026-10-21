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
	e, ok := gateChangeEntry(b, p, before, nil, nil, gates, now)
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

	again, ok := gateChangeEntry(b, p, before, nil, nil, gates, now.Add(time.Hour))
	require.True(t, ok)
	assert.Equal(t, e.Name, again.Name, "the same changes have the same record name")
	other, _ := gateChangeEntry(b, p, before, nil, nil, []kardinalv1alpha1.PolicyGate{edited, unchanged, added, elsewhere}, now)
	assert.NotEqual(t, e.Name, other.Name, "other changes, another record")

	// One environment: the record names it.
	one, ok := gateChangeEntry(b, p, map[string]graph.TemplateStamp{}, nil, nil, []kardinalv1alpha1.PolicyGate{added}, now)
	require.True(t, ok)
	assert.Equal(t, "uat", one.Spec.Environment)
	assert.Contains(t, one.Spec.Message, "1 PolicyGate template changed under version app-v1: ")

	_, ok = gateChangeEntry(b, p, map[string]graph.TemplateStamp{
		"platform-policies/unchanged": {Digest: graph.TemplateDigest(unchanged), Generation: 2},
	}, nil, nil, []kardinalv1alpha1.PolicyGate{unchanged, elsewhere}, now)
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
	e, ok := gateChangeEntry(b, p, before, nil, nil, gates, time.Unix(300, 0))
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
	_, ok := gateChangeEntry(b, p, before, nil, nil, []kardinalv1alpha1.PolicyGate{edited, other}, time.Unix(300, 0))
	assert.False(t, ok, "unstamped copies record nothing, edited, unchanged or deleted")

	added := gateAuditGate("added", "prod", "true", 1)
	e, ok := gateChangeEntry(b, p, before, nil, nil, []kardinalv1alpha1.PolicyGate{edited, other, added}, time.Unix(300, 0))
	require.True(t, ok)
	require.Len(t, e.Spec.Gates, 1)
	assert.Equal(t, "added", e.Spec.Gates[0].Template)

	// After the rebuild the copies carry stamps: the next edit is recorded.
	stamped := map[string]graph.TemplateStamp{
		"platform-policies/edited": {Digest: graph.TemplateDigest(edited), Generation: 4},
		"platform-policies/other":  {Digest: graph.TemplateDigest(other), Generation: 2},
	}
	again := gateAuditGate("edited", "prod", "false", 5)
	e, ok = gateChangeEntry(b, p, stamped, nil, nil, []kardinalv1alpha1.PolicyGate{again, other}, time.Unix(400, 0))
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
		e, ok := gateChangeEntry(b, p, before, nil, nil, []kardinalv1alpha1.PolicyGate{hold, other}, time.Unix(300+gen, 0))
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
	e1, ok := gateChangeEntry(b, p, map[string]graph.TemplateStamp{}, nil, nil, []kardinalv1alpha1.PolicyGate{added}, time.Unix(300, 0))
	require.True(t, ok)
	other := *b
	other.Name = "app-v2"
	e2, ok := gateChangeEntry(&other, p, map[string]graph.TemplateStamp{}, nil, nil, []kardinalv1alpha1.PolicyGate{added}, time.Unix(301, 0))
	require.True(t, ok)
	_, ok = coalesceGateChanges(e1, e2)
	assert.False(t, ok)

	// The same version: deleted, then added again, is one change from the
	// deleted stamp to the new one.
	gone := map[string]graph.TemplateStamp{"platform-policies/added": {Digest: "sha256:old", Generation: 3}}
	d, ok := gateChangeEntry(b, p, gone, nil, nil, nil, time.Unix(302, 0))
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

// #2338: a kardinal.io/applies-to edit moves a gate without changing its
// rule: a stamped copy with the same digest and generation whose
// environments changed is recorded as moved, with the environments added
// and removed; one that did not move is not recorded; an unstamped copy
// (v0.12) stays the baseline; the move takes part in the record's name.
//
// Covers GATE-EDIT-04.
func TestGateChangeEntry_EnvironmentMove(t *testing.T) {
	p, b := gateAuditFixture()
	p.Spec.Environments = append(p.Spec.Environments, kardinalv1alpha1.EnvironmentSpec{Name: "test"})
	window := gateAuditGate("window", "test,prod", "true", 2)
	stamp := map[string]graph.TemplateStamp{"platform-policies/window": {Digest: graph.TemplateDigest(window), Generation: 2}}
	before := map[string][]string{"platform-policies/window": {"prod"}}
	after := map[string][]string{"platform-policies/window": {"prod", "test"}}
	gates := []kardinalv1alpha1.PolicyGate{window}

	e, ok := gateChangeEntry(b, p, stamp, before, after, gates, time.Unix(400, 0))
	require.True(t, ok, "a move is a change")
	require.Len(t, e.Spec.Gates, 1)
	gc := e.Spec.Gates[0]
	assert.Equal(t, gc.OldDigest, gc.NewDigest)
	assert.Equal(t, int64(2), gc.NewGeneration)
	assert.Equal(t, []string{"test"}, gc.EnvironmentsAdded)
	assert.Empty(t, gc.EnvironmentsRemoved)
	assert.Contains(t, e.Spec.Message, "platform-policies/window moved (generation 2): now also in test")

	_, ok = gateChangeEntry(b, p, stamp, before, before, gates, time.Unix(400, 0))
	assert.False(t, ok, "no move, no change")
	_, ok = gateChangeEntry(b, p, stamp, before, nil, gates, time.Unix(400, 0))
	assert.False(t, ok, "the Graph after could not be read: the move is unknown")
	_, ok = gateChangeEntry(b, p, map[string]graph.TemplateStamp{"platform-policies/window": {}}, before, after, gates, time.Unix(400, 0))
	assert.False(t, ok, "an unstamped copy is the baseline")

	back, ok := gateChangeEntry(b, p, stamp, after, before, gates, time.Unix(400, 0))
	require.True(t, ok)
	assert.Equal(t, []string{"test"}, back.Spec.Gates[0].EnvironmentsRemoved)
	assert.NotEqual(t, e.Name, back.Name, "moving back is another record")
}

// #2338: environmentDiff, then and set: the names in each list are sorted,
// a record keeps the first MaxAuditGateEnvironments of each and counts the
// rest, and an environment added and then removed (or the reverse) stays in
// both lists, said as moved and back (QA-2 on #2347).
//
// Covers GATE-EDIT-04.
func TestEnvironmentMove(t *testing.T) {
	m := environmentDiff([]string{"a", "b", "c"}, []string{"b", "c", "d", "e"})
	assert.Equal(t, []string{"d", "e"}, m.added)
	assert.Equal(t, []string{"a"}, m.removed)
	assert.Equal(t, "now also in d, e; no longer in a", m.String())
	assert.True(t, environmentDiff([]string{"a"}, []string{"a"}).none())

	var many []string
	for i := range 12 {
		many = append(many, fmt.Sprintf("env-%02d", i))
	}
	var gc kardinalv1alpha1.AuditGateChange
	environmentDiff(nil, many).set(&gc)
	assert.Len(t, gc.EnvironmentsAdded, kardinalv1alpha1.MaxAuditGateEnvironments)
	assert.Equal(t, int32(12-kardinalv1alpha1.MaxAuditGateEnvironments), gc.EnvironmentsOmitted)
	assert.Equal(t, "env-00", gc.EnvironmentsAdded[0])

	there := environmentDiff([]string{"prod"}, []string{"prod", "test"})
	back := environmentDiff([]string{"prod", "test"}, []string{"prod"})
	round := there.then(back)
	assert.Equal(t, []string{"test"}, round.added, "added, then removed: kept")
	assert.Equal(t, []string{"test"}, round.removed)
	assert.Equal(t, "moved and back in test", round.String())
	assert.Equal(t, round, back.then(there), "removed, then added: the same")
	both := there.then(environmentDiff([]string{"prod", "test"}, []string{"prod", "test", "uat"}))
	assert.Equal(t, []string{"test", "uat"}, both.added)
	assert.Empty(t, both.removed)
}

// #2338, QA-2 on #2347: merged records keep every move, so a merge (the
// outbox full during an AuditEvent write outage) cannot erase that a gate
// was off an environment of the version for a while: prod-freeze taken off
// prod by an applies-to edit and put back ten minutes later is one change
// that says it moved and back, beside window's edit.
//
// Covers GATE-EDIT-04.
func TestCoalesceGateChanges_EnvironmentMove(t *testing.T) {
	p, b := gateAuditFixture()
	freeze := gateAuditGate("prod-freeze", "uat,prod", "!schedule.isWeekend", 2)
	window := gateAuditGate("window", "prod", "true", 2)
	stamps := map[string]graph.TemplateStamp{
		"platform-policies/prod-freeze": {Digest: graph.TemplateDigest(freeze), Generation: 2},
		"platform-policies/window":      {Digest: "sha256:old", Generation: 1},
	}
	key := "platform-policies/prod-freeze"
	on, off := []string{"prod", "uat"}, []string{"uat"}
	gates := []kardinalv1alpha1.PolicyGate{freeze, window}
	removed, ok := gateChangeEntry(b, p, stamps, map[string][]string{key: on}, map[string][]string{key: off}, gates, time.Unix(500, 0))
	require.True(t, ok)
	require.Len(t, removed.Spec.Gates, 2, "the move and window's edit")
	stamps["platform-policies/window"] = graph.TemplateStamp{Digest: graph.TemplateDigest(window), Generation: 2}
	back, ok := gateChangeEntry(b, p, stamps, map[string][]string{key: off}, map[string][]string{key: on}, gates, time.Unix(1100, 0))
	require.True(t, ok)
	require.Len(t, back.Spec.Gates, 1)

	m, ok := coalesceGateChanges(removed, back)
	require.True(t, ok)
	byName := map[string]kardinalv1alpha1.AuditGateChange{}
	for _, gc := range m.Spec.Gates {
		byName[gc.Template] = gc
	}
	require.Contains(t, byName, "prod-freeze", "the move and the move back are kept")
	assert.Equal(t, []string{"prod"}, byName["prod-freeze"].EnvironmentsAdded)
	assert.Equal(t, []string{"prod"}, byName["prod-freeze"].EnvironmentsRemoved)
	assert.Contains(t, byName, "window")
	assert.Contains(t, m.Spec.Message, "platform-policies/prod-freeze moved (generation 2): moved and back in prod")
}

// QA-2 on #2347: three or more merged moves keep the net state. Each step
// is folded in order: an environment that ends where it started is moved
// and back (in both lists), one that ends off is no longer in (removed
// only), one that ends on is now also in (added only).
//
// Covers GATE-EDIT-04.
func TestEnvironmentMove_Sequences(t *testing.T) {
	on, off := []string{"prod", "uat"}, []string{"uat"}
	takeOff := environmentDiff(on, off)
	putOn := environmentDiff(off, on)
	for _, tc := range []struct {
		name           string
		steps          []environmentMove
		added, removed []string
		says           string
	}{
		{"off prod, back on", []environmentMove{takeOff, putOn}, []string{"prod"}, []string{"prod"}, "moved and back in prod"},
		{"off prod, back on, off again", []environmentMove{takeOff, putOn, takeOff}, nil, []string{"prod"}, "no longer in prod"},
		{"onto prod, off, onto again", []environmentMove{putOn, takeOff, putOn}, []string{"prod"}, nil, "now also in prod"},
		{"off, on, off, on", []environmentMove{takeOff, putOn, takeOff, putOn}, []string{"prod"}, []string{"prod"}, "moved and back in prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.steps[0]
			for _, s := range tc.steps[1:] {
				got = got.then(s)
			}
			assert.Equal(t, tc.added, got.added)
			assert.Equal(t, tc.removed, got.removed)
			assert.Equal(t, tc.says, got.String())
		})
	}
	// A newer record that is itself a merge, back where it started (on, then
	// off), keeps the older net move: off, then (on, off) ends off (QA-2's
	// right fold on #2347).
	right := takeOff.then(putOn.then(takeOff))
	assert.Nil(t, right.added)
	assert.Equal(t, []string{"prod"}, right.removed)
	assert.Equal(t, "no longer in prod", right.String())
}

// QA-2 on #2347: three unwritten records of one version merged as the
// outbox merges them, oldest first: prod-freeze off prod, back on, off again
// is recorded as no longer in prod, not as moved and back.
//
// Covers GATE-EDIT-04.
func TestCoalesceGateChanges_ThreeMoves(t *testing.T) {
	p, b := gateAuditFixture()
	freeze := gateAuditGate("prod-freeze", "uat", "!schedule.isWeekend", 2)
	stamps := map[string]graph.TemplateStamp{"platform-policies/prod-freeze": {Digest: graph.TemplateDigest(freeze), Generation: 2}}
	key := "platform-policies/prod-freeze"
	on, off := []string{"prod", "uat"}, []string{"uat"}
	gates := []kardinalv1alpha1.PolicyGate{freeze}
	record := func(before, after []string, at int64) kardinalv1alpha1.PendingAuditEvent {
		e, ok := gateChangeEntry(b, p, stamps, map[string][]string{key: before}, map[string][]string{key: after}, gates, time.Unix(at, 0))
		require.True(t, ok)
		return e
	}
	m, ok := coalesceGateChanges(record(on, off, 600), record(off, on, 1200))
	require.True(t, ok)
	m, ok = coalesceGateChanges(m, record(on, off, 1800))
	require.True(t, ok)
	require.Len(t, m.Spec.Gates, 1)
	assert.Empty(t, m.Spec.Gates[0].EnvironmentsAdded, "prod-freeze ends off prod")
	assert.Equal(t, []string{"prod"}, m.Spec.Gates[0].EnvironmentsRemoved)
	assert.Contains(t, m.Spec.Message, "platform-policies/prod-freeze moved (generation 2): no longer in prod")
}

// TestRenderedBefore (#2411): a Graph's render label predates a render
// version when it has none, a lower integer, or a release branch's
// "<n>-v<patch>" with a lower n; the current label and later ones do not.
func TestRenderedBefore(t *testing.T) {
	for label, want := range map[string]bool{"": true, "2": true, "3": true, "3-v0.12.4": true,
		"4": false, "4-v0.13.1": false, "5": false} {
		assert.Equal(t, want, renderedBefore(label, verifyOneAllowlistRender), "label %q", label)
	}
	assert.False(t, renderedBefore(graph.RenderLabel, verifyOneAllowlistRender), "the current label")
}

// TestVerifyRuleChangeEntries (#2411): the re-render records one
// GateChanged per verify gate of agents only that applies to the version,
// naming the rule change with the template's digest and generation
// unchanged; a verify gate that lists people, groups or nobody, a gate
// without verify and one for another environment get none. Rule-change
// records are never merged into others.
func TestVerifyRuleChangeEntries(t *testing.T) {
	p, b := gateAuditFixture()
	agents := gateAuditGate("qa-only", "prod", "true", 3)
	agents.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "qa", AllowedAgents: []string{"qa", "fixer"}}
	lead := gateAuditGate("qa-or-lead", "prod", "true", 1)
	lead.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "qa", AllowedAgents: []string{"qa"}, AllowedUsers: []string{"lead"}}
	group := gateAuditGate("qa-or-group", "prod", "true", 1)
	group.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "qa", AllowedAgents: []string{"qa"}, AllowedGroups: []string{"qa"}}
	anyone := gateAuditGate("anyone", "prod", "true", 1)
	anyone.Spec.Verify = &kardinalv1alpha1.GateVerifyPolicy{Check: "qa"}
	elsewhere := gateAuditGate("qa-only-staging", "staging", "true", 1)
	elsewhere.Spec.Verify = agents.Spec.Verify.DeepCopy()
	plain := gateAuditGate("window", "prod", "true", 1)
	now := time.Date(2026, 10, 21, 5, 0, 0, 0, time.UTC)
	got := verifyRuleChangeEntries(b, p, []kardinalv1alpha1.PolicyGate{agents, lead, group, anyone, elsewhere, plain}, now)
	require.Len(t, got, 1)
	e := got[0]
	assert.Equal(t, AuditActionGateChanged, e.Spec.Action)
	assert.Equal(t, "prod", e.Spec.Environment)
	assert.Equal(t, "app-v1", e.Spec.BundleName)
	assert.Equal(t, "PolicyGate platform-policies/qa-only changed under version app-v1 on upgrade: the controller's verify rule "+
		"changed (v0.13): only the agents listed in verify.allowedAgents count (qa, fixer); a person's or bot's verdict no longer "+
		"counts on it (#2411)", e.Spec.Message)
	d := graph.TemplateDigest(agents)
	assert.Equal(t, []kardinalv1alpha1.AuditGateChange{{Template: "qa-only", Namespace: "platform-policies",
		OldDigest: d, NewDigest: d, OldGeneration: 3, NewGeneration: 3}}, e.Spec.Gates)
	again := verifyRuleChangeEntries(b, p, []kardinalv1alpha1.PolicyGate{agents}, now.Add(time.Minute))
	assert.Equal(t, e.Name, again[0].Name, "named after the version and the template: written once")

	_, ok := coalesceGateChanges(e, gateChangeRecord("app-v1", "app", "prod",
		[]kardinalv1alpha1.AuditGateChange{{Template: "window", Namespace: "platform-policies", NewDigest: "sha256:b", NewGeneration: 2}},
		0, 0, metav1.NewTime(now)))
	assert.False(t, ok, "a rule change is not merged into a template change")
}
