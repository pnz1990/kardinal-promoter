//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	auditpkg "github.com/kardinal-promoter/kardinal-promoter/pkg/audit"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// TestAudit_PromotionRecords promotes a Bundle through test and a prod held
// by a gate until an operator overrides it, then reads the AuditEvents
// (docs/guides/security.md, Audit Logging). They are in the Pipeline's
// namespace, none in kardinal-system. Each step has PromotionStarted (Pending)
// and PromotionSucceeded (Success), named <step>-started and <step>-succeeded;
// the gate instance has GateEvaluated Failure with the gate's message, then
// GateEvaluated Success with the override, and the override its
// GateOverridden record (#1450). Every record carries the
// documented spec fields and the kardinal.io/pipeline, bundle, environment and
// action labels, the action label filters them, `kubectl get auditevents`
// shows them, and the API server refuses to change a record's spec.
//
// Covers STEP-AUDIT-01.
func TestAudit_PromotionRecords(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	require.NoError(t, e.Client.Create(ctx, operatorHold(a.ns, "prod")))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	testStep := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	blocked := a.waitAudit(t, bundle, "GateEvaluated", "Failure")
	a.noStep(t, bundle, "prod", 5*time.Second)
	a.overrideHold(t, bundle, "prod")
	prodStep := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	allowed := a.waitAudit(t, bundle, "GateEvaluated", "Success")

	events, err := e.AuditEvents(ctx, a.ns, bundle)
	require.NoError(t, err)
	got := map[string]v1alpha1.AuditEvent{}
	var keys []string
	for _, ae := range events {
		k := auditKey(ae)
		keys = append(keys, k)
		got[k] = ae
		assert.Equal(t, pipelineName, ae.Spec.PipelineName, "%s: spec.pipelineName", ae.Name)
		assert.Equal(t, bundle, ae.Spec.BundleName, "%s: spec.bundleName", ae.Name)
		assert.False(t, ae.Spec.Timestamp.IsZero(), "%s: spec.timestamp", ae.Name)
		assert.Equal(t, map[string]string{"pipeline": pipelineName, "bundle": bundle, "environment": ae.Spec.Environment,
			"action": ae.Spec.Action}, kardinalLabels(ae.Labels, "pipeline", "bundle", "environment", "action"),
			"%s: labels", ae.Name)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{
		"prod GateEvaluated Failure", "prod GateEvaluated Success", "prod GateOverridden Success",
		"prod PromotionStarted Pending", "prod PromotionSucceeded Success",
		"test PromotionStarted Pending", "test PromotionSucceeded Success",
	}, keys, "the Bundle's AuditEvents (environment, action, outcome)")

	for _, ps := range []*v1alpha1.PromotionStep{testStep, prodStep} {
		env := ps.Spec.Environment
		started, succeeded := got[env+" PromotionStarted Pending"], got[env+" PromotionSucceeded Success"]
		assert.Equal(t, ps.Name+"-started", started.Name, "%s: started record name", env)
		assert.Equal(t, ps.Name+"-succeeded", succeeded.Name, "%s: succeeded record name", env)
		assert.False(t, succeeded.Spec.Timestamp.Before(&started.Spec.Timestamp), "%s: succeeded after started", env)
	}
	assert.False(t, got["prod PromotionStarted Pending"].Spec.Timestamp.Time.Before(got["test PromotionSucceeded Success"].Spec.Timestamp.Time),
		"prod starts after test succeeds")
	assert.True(t, strings.HasPrefix(blocked.Spec.Message, "held for an operator"), "blocked record message: %q", blocked.Spec.Message)
	assert.Equal(t, "hold", blocked.Labels["kardinal.io/gate"], "the gate label")
	assert.Contains(t, allowed.Spec.Message, "OVERRIDDEN by", "allowed record message")
	assert.Contains(t, allowed.Spec.Message, "e2e operator override", "allowed record message")
	assert.False(t, allowed.Spec.Timestamp.Before(&blocked.Spec.Timestamp), "allowed after blocked")

	var succeeded v1alpha1.AuditEventList
	require.NoError(t, e.Client.List(ctx, &succeeded, client.InNamespace(a.ns),
		client.MatchingLabels{"kardinal.io/action": "PromotionSucceeded"}))
	var names []string
	for _, ae := range succeeded.Items {
		names = append(names, ae.Name)
	}
	assert.ElementsMatch(t, []string{testStep.Name + "-succeeded", prodStep.Name + "-succeeded"}, names,
		"-l kardinal.io/action=PromotionSucceeded")
	var elsewhere v1alpha1.AuditEventList
	require.NoError(t, e.Client.List(ctx, &elsewhere, client.InNamespace(framework.ControllerNamespace),
		client.MatchingLabels{"kardinal.io/bundle": bundle}))
	assert.Empty(t, elsewhere.Items, "no AuditEvents in %s", framework.ControllerNamespace)

	table := e.GetTable(t, "kardinal.io", "v1alpha1", "auditevents", a.ns)
	row := got["prod PromotionSucceeded Success"].Name
	for col, want := range map[string]string{"Pipeline": pipelineName, "Bundle": bundle, "Environment": "prod",
		"Action": "PromotionSucceeded", "Outcome": "Success"} {
		assert.Equal(t, want, table.Cell(row, col), "kubectl get auditevents: %s column", col)
	}

	record := got["test PromotionSucceeded Success"]
	record.Spec.Message = "rewritten"
	err = e.Client.Update(ctx, &record)
	require.Error(t, err, "an AuditEvent spec cannot change")
	assert.Contains(t, err.Error(), "AuditEvent spec is immutable")
}

// TestAudit_FailedPromotion promotes a tag that never becomes healthy to an
// environment with a 20s health timeout. The failed step writes
// PromotionFailed (Failure), named <step>-failed, with the step's message, and
// no PromotionSucceeded; its Failed Event is a Warning with action Promote and
// the note "env test: step failed: <message>".
//
// Covers STEP-AUDIT-01.
func TestAudit_FailedPromotion(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	p := a.pipeline(nil)
	envSpec(t, p, "test").Health.Timeout = "20s"
	a.apply(t, p)
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.BrokenTag)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Failed", promoteTimeout)

	failed := a.waitAudit(t, bundle, "PromotionFailed", "Failure")
	assert.Equal(t, ps.Name+"-failed", failed.Name)
	assert.Equal(t, "test", failed.Spec.Environment)
	assert.Equal(t, ps.Status.Message, failed.Spec.Message, "the record keeps the step's message")
	events, err := e.AuditEvents(context.Background(), a.ns, bundle)
	require.NoError(t, err)
	var keys []string
	for _, ae := range events {
		keys = append(keys, auditKey(ae))
	}
	assert.ElementsMatch(t, []string{"test PromotionStarted Pending", "test PromotionFailed Failure"}, keys)

	var ev eventsv1.Event
	framework.Eventually(t, time.Minute, "the step's Failed Event", func(ctx context.Context) (bool, string) {
		evs, err := e.Events(ctx, a.ns, "PromotionStep", ps.Name)
		if err != nil {
			return false, err.Error()
		}
		var ok bool
		ev, ok = eventsByReason(evs)["Failed"]
		return ok, fmt.Sprintf("reasons %v", eventReasons(evs))
	})
	assert.Equal(t, "Warning", ev.Type)
	assert.Equal(t, "Promote", ev.Action)
	assert.Equal(t, kubeNote("env test: step failed: "+ps.Status.Message), ev.Note)
}

// kubeNote is note as an Event carries it: cut to 1024 bytes, ending in "...".
func kubeNote(note string) string {
	if len(note) <= 1024 {
		return note
	}
	return note[:1021] + "..."
}

// TestAudit_Events reads the events.k8s.io/v1 Events kardinal records while a
// Bundle promotes test and is blocked at prod by a gate whose message is
// longer than an Event note may be. The test step's Events are Promoting
// (action Promote), HealthChecking (CheckHealth) and Verified (Verify), with
// notes naming the environment. The gate instance's Blocked Event (Warning,
// action Evaluate) carries the message cut to 1024 bytes ending in "...",
// where the API server would reject a longer note and lose the Event; the
// gate's status.reason keeps the whole message, and its GateEvaluated
// AuditEvent the first audit.MaxMessageBytes of it ending in "…" (the
// AuditEvent goes through the writer's status outbox, #1552). Every kardinal Event in the namespace has an action and a note of
// at most 1024 bytes.
//
// Covers STEP-EVENTS-01.
func TestAudit_Events(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	long := strings.Repeat("prod is frozen while the change board reviews this release. ", 20)
	require.Greater(t, len(long), 1024)
	gate := operatorHold(a.ns, "prod")
	gate.Spec.Message = long
	require.NoError(t, e.Client.Create(ctx, gate))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", imageV2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	audit := a.waitAudit(t, bundle, "GateEvaluated", "Failure")

	var steps []eventsv1.Event
	framework.Eventually(t, time.Minute, "the test step's Events", func(ctx context.Context) (bool, string) {
		var err error
		steps, err = e.Events(ctx, a.ns, "PromotionStep", ps.Name)
		if err != nil {
			return false, err.Error()
		}
		return len(eventsByReason(steps)) >= 3, fmt.Sprintf("reasons %v", eventReasons(steps))
	})
	byReason := eventsByReason(steps)
	for _, want := range []struct{ reason, action, note string }{
		{"Promoting", "Promote", "env test: promotion started"},
		{"HealthChecking", "CheckHealth", "env test: change delivered, running health check"},
		{"Verified", "Verify", "env test: step completed successfully"},
	} {
		ev, ok := byReason[want.reason]
		if !assert.True(t, ok, "a %s Event; got %v", want.reason, eventReasons(steps)) {
			continue
		}
		assert.Equal(t, "Normal", ev.Type, "%s type", want.reason)
		assert.Equal(t, want.action, ev.Action, "%s action", want.reason)
		assert.True(t, strings.HasPrefix(ev.Note, want.note), "%s note: %q", want.reason, ev.Note)
	}

	var instances v1alpha1.PolicyGateList
	require.NoError(t, e.Client.List(ctx, &instances, client.InNamespace(a.ns),
		client.MatchingLabels{"kardinal.io/bundle": bundle, "kardinal.io/gate-template": gate.Name}))
	require.Len(t, instances.Items, 1, "one hold instance for the Bundle")
	inst := instances.Items[0]
	var blocked eventsv1.Event
	framework.Eventually(t, time.Minute, "the gate's Blocked Event", func(ctx context.Context) (bool, string) {
		evs, err := e.Events(ctx, a.ns, "PolicyGate", inst.Name)
		if err != nil {
			return false, err.Error()
		}
		var ok bool
		blocked, ok = eventsByReason(evs)["Blocked"]
		return ok, fmt.Sprintf("reasons %v", eventReasons(evs))
	})
	assert.Equal(t, "Warning", blocked.Type)
	assert.Equal(t, "Evaluate", blocked.Action)
	assert.LessOrEqual(t, len(blocked.Note), 1024, "the note fits the API limit")
	assert.True(t, strings.HasSuffix(blocked.Note, "..."), "a cut note ends in ...: %q", tail(blocked.Note))
	prefix := fmt.Sprintf("env prod pipeline %s: gate %s blocking promotion: %s", pipelineName, inst.Name, long[:100])
	assert.True(t, strings.HasPrefix(blocked.Note, prefix), "note: %q", blocked.Note)
	require.NoError(t, e.Client.Get(ctx, client.ObjectKeyFromObject(&inst), &inst))
	assert.Equal(t, kubeNote(fmt.Sprintf("env prod pipeline %s: gate %s blocking promotion: %s",
		pipelineName, inst.Name, inst.Status.Reason)), blocked.Note, "the note is the reason cut to 1024 bytes")
	assert.True(t, strings.HasPrefix(inst.Status.Reason, long), "status.reason keeps the whole message")
	// The AuditEvent's message is cut to auditpkg.MaxMessageBytes on purpose:
	// it travels through the writer's status outbox (#1552). Exactly that:
	// at most the bound, ending in the marker, and the message's own start
	// up to the bound (the gate message is ASCII, so the cut is not moved
	// back to a rune boundary), not some shorter or other text.
	const marker = "…"
	assert.LessOrEqual(t, len(audit.Spec.Message), auditpkg.MaxMessageBytes, "the AuditEvent's message fits the outbox bound")
	assert.True(t, strings.HasSuffix(audit.Spec.Message, marker), "a cut message ends in %q: %q", marker, tail(audit.Spec.Message))
	body := strings.TrimSuffix(audit.Spec.Message, marker)
	assert.Len(t, body, auditpkg.MaxMessageBytes-len(marker), "cut at the bound")
	assert.True(t, strings.HasPrefix(long, body), "the AuditEvent keeps the start of the gate message")

	all, err := e.Kube.EventsV1().Events(a.ns).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	kinds := map[string]int{}
	for _, ev := range all.Items {
		if ev.Regarding.APIVersion != v1alpha1.GroupVersion.String() {
			continue
		}
		kinds[ev.Regarding.Kind]++
		assert.NotEmpty(t, ev.Action, "%s %s %s: action", ev.Regarding.Kind, ev.Regarding.Name, ev.Reason)
		assert.LessOrEqual(t, len(ev.Note), 1024, "%s %s %s: note", ev.Regarding.Kind, ev.Regarding.Name, ev.Reason)
	}
	for _, k := range []string{"Bundle", "PromotionStep", "PolicyGate"} {
		assert.NotZero(t, kinds[k], "%s Events in the namespace; got %v", k, kinds)
	}
}

// waitAudit waits for the AuditEvent of bundle with action and outcome.
func (a *app) waitAudit(t *testing.T, bundle, action, outcome string) v1alpha1.AuditEvent {
	t.Helper()
	var found v1alpha1.AuditEvent
	framework.Eventually(t, time.Minute, fmt.Sprintf("a %s %s AuditEvent", action, outcome), func(ctx context.Context) (bool, string) {
		events, err := a.e.AuditEvents(ctx, a.ns, bundle)
		if err != nil {
			return false, err.Error()
		}
		var seen []string
		for _, ae := range events {
			if ae.Spec.Action == action && ae.Spec.Outcome == outcome {
				found = ae
				return true, ""
			}
			seen = append(seen, auditKey(ae))
		}
		return false, fmt.Sprintf("seen %v", seen)
	})
	return found
}

// auditKey is "<environment> <action> <outcome>".
func auditKey(ae v1alpha1.AuditEvent) string {
	return ae.Spec.Environment + " " + ae.Spec.Action + " " + ae.Spec.Outcome
}

// kardinalLabels returns the kardinal.io/<key> labels of labels, keyed by key.
func kardinalLabels(labels map[string]string, keys ...string) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		if v, ok := labels["kardinal.io/"+k]; ok {
			out[k] = v
		}
	}
	return out
}

// eventsByReason keys events by reason, keeping the latest of each.
func eventsByReason(events []eventsv1.Event) map[string]eventsv1.Event {
	out := map[string]eventsv1.Event{}
	for _, ev := range events {
		if cur, ok := out[ev.Reason]; !ok || cur.EventTime.Before(&ev.EventTime) {
			out[ev.Reason] = ev
		}
	}
	return out
}

// eventReasons lists the reasons of events, for failure messages.
func eventReasons(events []eventsv1.Event) []string {
	var out []string
	for _, ev := range events {
		out = append(out, ev.Reason)
	}
	return out
}

// tail is the last 40 bytes of s.
func tail(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[len(s)-40:]
}
