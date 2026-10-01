//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the test computes Asia/Tokyo hours on any host

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// blackout is a blackout ChangeWindow from start to end. ChangeWindows are
// cluster-scoped: name them after the test's namespace.
func blackout(name string, start, end time.Time) *v1alpha1.ChangeWindow {
	return &v1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Reason: "e2e " + name,
			Start: metav1.NewTime(start), End: metav1.NewTime(end)},
	}
}

// recurring is a recurring ChangeWindow allowing promotions on schedule s.
func recurring(name string, s v1alpha1.ChangeWindowSchedule) *v1alpha1.ChangeWindow {
	return &v1alpha1.ChangeWindow{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.ChangeWindowSpec{Type: "recurring", Schedule: &s},
	}
}

// editWindow applies edit to the ChangeWindow's spec with a merge patch.
func editWindow(t *testing.T, e *framework.Env, name string, edit func(*v1alpha1.ChangeWindowSpec)) {
	t.Helper()
	ctx := context.Background()
	var cw v1alpha1.ChangeWindow
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Name: name}, &cw))
	patch := client.MergeFrom(cw.DeepCopy())
	edit(&cw.Spec)
	require.NoError(t, e.Client.Patch(ctx, &cw, patch), "edit ChangeWindow %s", name)
}

// windowState matches a window with status.active and a reason containing
// reason, whose Valid condition has status valid.
func windowState(active bool, reason string, valid metav1.ConditionStatus) func(*v1alpha1.ChangeWindow) bool {
	return func(cw *v1alpha1.ChangeWindow) bool {
		c := meta.FindStatusCondition(cw.Status.Conditions, "Valid")
		return cw.Status.Active == active && strings.Contains(cw.Status.Reason, reason) &&
			c != nil && c.Status == valid && c.ObservedGeneration == cw.Generation
	}
}

// TestGate_BlackoutWindowBlocks checks a blackout ChangeWindow through every
// gate syntax: changewindow.isBlocked, changewindow.isAllowed and the index
// form all hold prod while the window is active, and a gate naming a window
// that does not exist blocks with an evaluation error. A window that has not
// started does not block. Ending the blackout and creating the missing window
// open every gate, and prod promotes.
//
// Covers CW-01.
func TestGate_BlackoutWindowBlocks(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	freeze, later, missing := a.ns+"-freeze", a.ns+"-later", a.ns+"-missing"
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(time.Hour)
	e.CreateChangeWindow(t, blackout(freeze, now.Add(-time.Minute), end))
	e.CreateChangeWindow(t, blackout(later, now.Add(time.Hour), now.Add(2*time.Hour)))
	gates := map[string]struct{ expr, blocked string }{
		"is-blocked":    {fmt.Sprintf(`!changewindow.isBlocked(%q)`, freeze), "= false"},
		"is-allowed":    {fmt.Sprintf(`changewindow.isAllowed(%q)`, freeze), "= false"},
		"index":         {fmt.Sprintf(`!changewindow[%q]`, freeze), "= false"},
		"missing-fn":    {fmt.Sprintf(`changewindow.isAllowed(%q)`, missing), fmt.Sprintf(`CEL evaluation error: changewindow: unknown ChangeWindow %q`, missing)},
		"missing-index": {fmt.Sprintf(`!changewindow[%q]`, missing), "CEL evaluation error: no such key: " + missing},
		"not-started":   {fmt.Sprintf(`changewindow.isAllowed(%q) && !changewindow.isBlocked(%q)`, later, later), "= true"},
	}
	for name, g := range gates {
		e.CreateGate(t, framework.Gate(a.ns, name, "prod", g.expr, recheck))
	}
	e.WaitChangeWindow(t, freeze, gateTimeout, "active", windowState(true, "blackout until "+end.Format(time.RFC3339), metav1.ConditionTrue))
	e.WaitChangeWindow(t, later, gateTimeout, "not active yet", windowState(false, "blackout starts at "+now.Add(time.Hour).Format(time.RFC3339), metav1.ConditionTrue))
	a.apply(t, a.pipeline(nil))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	for name, g := range gates {
		e.WaitGateReady(t, a.ns, bundle, "prod", name, name == "not-started", g.blocked, gateTimeout)
	}
	e.WaitExplainGate(t, a.ns, pipelineName, "prod", "is-blocked", "Block", 10*time.Second)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	ended := time.Now().UTC().Truncate(time.Second).Add(-time.Second)
	editWindow(t, e, freeze, func(s *v1alpha1.ChangeWindowSpec) { s.End = metav1.NewTime(ended) })
	e.CreateChangeWindow(t, blackout(missing, now.Add(-2*time.Hour), now.Add(-time.Hour)))
	e.WaitChangeWindow(t, freeze, gateTimeout, "ended", windowState(false, "blackout ended at "+ended.Format(time.RFC3339), metav1.ConditionTrue))
	for name := range gates {
		e.WaitGateReady(t, a.ns, bundle, "prod", name, true, "= true", gateTimeout)
	}
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// allowedHours is "HH:MM-HH:MM" from 30 minutes before now to 30 minutes
// after, in loc, and the weekday it starts on (an overnight range belongs to
// the day it starts on).
func allowedHours(now time.Time, loc *time.Location) (string, time.Weekday) {
	from, to := now.Add(-30*time.Minute).In(loc), now.Add(30*time.Minute).In(loc)
	return from.Format("15:04") + "-" + to.Format("15:04"), from.Weekday()
}

// TestGate_RecurringWindowSchedule checks recurring ChangeWindows against the
// current time: allowedDays, allowedHours and timezone (Asia/Tokyo, from the
// controller's embedded timezone database; UTC when unset). A window allowing
// this hour today does not block; the same hours on another day, or UTC's
// hours read in Tokyo, block prod until the window is edited to allow now.
//
// Covers CW-02.
func TestGate_RecurringWindowSchedule(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	require.NoError(t, err)
	now := time.Now()
	hours, day := allowedHours(now, tokyo)
	utcHours, _ := allowedHours(now, time.UTC)
	other := (day + 3) % 7
	in, otherDay, wrongTZ, utc := a.ns+"-in", a.ns+"-other-day", a.ns+"-wrong-tz", a.ns+"-utc"
	e.CreateChangeWindow(t, recurring(in, v1alpha1.ChangeWindowSchedule{
		Timezone: "Asia/Tokyo", AllowedDays: []string{day.String()[:3]}, AllowedHours: hours}))
	// Full day names are accepted too.
	e.CreateChangeWindow(t, recurring(otherDay, v1alpha1.ChangeWindowSchedule{
		Timezone: "Asia/Tokyo", AllowedDays: []string{other.String()}, AllowedHours: hours}))
	// UTC's hours in Tokyo (UTC+9, no DST) are nine hours from now.
	e.CreateChangeWindow(t, recurring(wrongTZ, v1alpha1.ChangeWindowSchedule{Timezone: "Asia/Tokyo", AllowedHours: utcHours}))
	e.CreateChangeWindow(t, recurring(utc, v1alpha1.ChangeWindowSchedule{AllowedHours: utcHours}))

	e.WaitChangeWindow(t, in, gateTimeout, "inside", windowState(false,
		fmt.Sprintf("inside the allowed window %s %s Asia/Tokyo", day.String()[:3], hours), metav1.ConditionTrue))
	e.WaitChangeWindow(t, otherDay, gateTimeout, "outside", windowState(true,
		fmt.Sprintf("outside the allowed window %s %s Asia/Tokyo", other, hours), metav1.ConditionTrue))
	e.WaitChangeWindow(t, wrongTZ, gateTimeout, "outside", windowState(true,
		fmt.Sprintf("outside the allowed window every day %s Asia/Tokyo", utcHours), metav1.ConditionTrue))
	e.WaitChangeWindow(t, utc, gateTimeout, "inside", windowState(false,
		fmt.Sprintf("inside the allowed window every day %s UTC", utcHours), metav1.ConditionTrue))

	e.CreateGate(t, framework.Gate(a.ns, "inside", "prod",
		fmt.Sprintf(`changewindow.isAllowed(%q) && changewindow.isAllowed(%q)`, in, utc), recheck))
	e.CreateGate(t, framework.Gate(a.ns, "other-day", "prod", fmt.Sprintf(`changewindow.isAllowed(%q)`, otherDay), recheck))
	e.CreateGate(t, framework.Gate(a.ns, "timezone", "prod", fmt.Sprintf(`changewindow.isAllowed(%q)`, wrongTZ), recheck))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitGateReady(t, a.ns, bundle, "prod", "inside", true, "= true", gateTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "other-day", false, "= false", gateTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "timezone", false, "= false", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	editWindow(t, e, otherDay, func(s *v1alpha1.ChangeWindowSpec) { s.Schedule.AllowedDays = []string{day.String()} })
	editWindow(t, e, wrongTZ, func(s *v1alpha1.ChangeWindowSpec) { s.Schedule.Timezone = "UTC" })
	e.WaitChangeWindow(t, otherDay, gateTimeout, "inside after the edit", windowState(false, "inside the allowed window", metav1.ConditionTrue))
	e.WaitChangeWindow(t, wrongTZ, gateTimeout, "inside after the edit", windowState(false, "inside the allowed window every day "+utcHours+" UTC", metav1.ConditionTrue))
	e.WaitGateReady(t, a.ns, bundle, "prod", "other-day", true, "= true", gateTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "timezone", true, "= true", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGate_InvalidWindowBlocks checks that a ChangeWindow the controller
// cannot evaluate fails closed: its Valid condition is False with reason
// InvalidSpec and a message naming the problem, it is active, and a gate that
// allows promotions only outside it blocks. The controller reports each
// invalid spec once, with one InvalidSpec Warning Event, however often the
// gates are evaluated. Fixing each spec makes it Valid and inactive, and prod
// promotes.
//
// Covers CW-03.
func TestGate_InvalidWindowBlocks(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	now := time.Now().UTC().Truncate(time.Second)
	windows := map[string]struct {
		cw    *v1alpha1.ChangeWindow
		error string
		fix   func(*v1alpha1.ChangeWindowSpec)
	}{
		"end-first": {blackout(a.ns+"-end-first", now, now.Add(-time.Hour)), "is not after spec.start",
			func(s *v1alpha1.ChangeWindowSpec) { s.Start = metav1.NewTime(now.Add(-2 * time.Hour)) }},
		"unknown-tz": {recurring(a.ns+"-unknown-tz", v1alpha1.ChangeWindowSchedule{Timezone: "Mars/Olympus_Mons"}),
			`spec.schedule.timezone "Mars/Olympus_Mons" is not a known IANA timezone name`,
			func(s *v1alpha1.ChangeWindowSpec) { s.Schedule.Timezone = "UTC" }},
		"local-tz": {recurring(a.ns+"-local-tz", v1alpha1.ChangeWindowSchedule{Timezone: "Local"}),
			`spec.schedule.timezone "Local" is the controller's local time`,
			func(s *v1alpha1.ChangeWindowSpec) { s.Schedule.Timezone = "Asia/Tokyo" }},
		"bad-day": {recurring(a.ns+"-bad-day", v1alpha1.ChangeWindowSchedule{AllowedDays: []string{"Funday"}}),
			`spec.schedule.allowedDays: "Funday" is not a day`,
			func(s *v1alpha1.ChangeWindowSpec) { s.Schedule.AllowedDays = nil }},
		"empty-hours": {recurring(a.ns+"-empty-hours", v1alpha1.ChangeWindowSchedule{AllowedHours: "09:00-09:00"}),
			`spec.schedule.allowedHours "09:00-09:00": start and end are equal`,
			func(s *v1alpha1.ChangeWindowSpec) { s.Schedule.AllowedHours = "00:00-24:00" }},
	}
	for gate, w := range windows {
		e.CreateChangeWindow(t, w.cw)
		e.CreateGate(t, framework.Gate(a.ns, gate, "prod", fmt.Sprintf(`changewindow.isAllowed(%q)`, w.cw.Name), recheck))
	}
	for _, w := range windows {
		cw := e.WaitChangeWindow(t, w.cw.Name, gateTimeout, "invalid", windowState(true, "invalid ChangeWindow, blocking: ", metav1.ConditionFalse))
		c := meta.FindStatusCondition(cw.Status.Conditions, "Valid")
		require.NotNil(t, c)
		assert.Equal(t, "InvalidSpec", c.Reason, w.cw.Name)
		assert.Contains(t, c.Message, w.error, w.cw.Name)
		assert.Contains(t, c.Message, "; the window is active (blocking) until the spec is fixed", w.cw.Name)
	}
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	for gate := range windows {
		e.WaitGateReady(t, a.ns, bundle, "prod", gate, false, "= false", gateTimeout)
	}
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)
	// A ChangeWindow is cluster-scoped, so its Events are in default.
	for _, w := range windows {
		var evs []eventsv1.Event
		framework.Eventually(t, time.Minute, w.cw.Name+"'s InvalidSpec Event", func(ctx context.Context) (bool, string) {
			all, err := e.Events(ctx, metav1.NamespaceDefault, "ChangeWindow", w.cw.Name)
			if err != nil {
				return false, err.Error()
			}
			evs = nil
			for _, ev := range all {
				if ev.Reason == "InvalidSpec" {
					evs = append(evs, ev)
				}
			}
			return len(evs) > 0, fmt.Sprintf("reasons %v", eventReasons(all))
		})
		require.Len(t, evs, 1, w.cw.Name)
		assert.Nil(t, evs[0].Series, "%s: reported once, not as a series", w.cw.Name)
		assert.Equal(t, "Warning", evs[0].Type, w.cw.Name)
		assert.Equal(t, "Evaluate", evs[0].Action, w.cw.Name)
		assert.Contains(t, evs[0].Note, w.error, w.cw.Name)
	}

	for _, w := range windows {
		editWindow(t, e, w.cw.Name, w.fix)
	}
	for gate, w := range windows {
		cw := e.WaitChangeWindow(t, w.cw.Name, gateTimeout, "valid after the fix", windowState(false, "", metav1.ConditionTrue))
		assert.Equal(t, "SpecValid", meta.FindStatusCondition(cw.Status.Conditions, "Valid").Reason, w.cw.Name)
		e.WaitGateReady(t, a.ns, bundle, "prod", gate, true, "= true", gateTimeout)
	}
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGate_WindowBoundaryFlipsGates checks that a window boundary alone
// re-evaluates the gates that use the window: the ChangeWindow reconciler
// requeues to just after the boundary and its status write wakes the gates.
// The gate's recheckInterval is an hour, and no fast ScheduleClock runs, so
// nothing else would. A blackout starting about 45s from now flips prod's gate
// to blocked within seconds of its start and back to ready within seconds of
// its end. test waits for its PR merge, so the Bundle stays in flight.
//
// Covers CW-04.
func TestGate_WindowBoundaryFlipsGates(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	name := a.ns + "-boundary"
	e.CreateGate(t, framework.Gate(a.ns, "outside-freeze", "prod", fmt.Sprintf(`!changewindow.isBlocked(%q)`, name), "1h"))
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))

	// No fast ScheduleClock may tick during the window (clockMu). The shared
	// kardinal-clock ticks once a minute, so it can land near one of the two
	// boundaries 20s apart, never near both.
	var bundle string
	var start, end time.Time
	var closed, opened *v1alpha1.PolicyGate
	func() {
		clockMu.Lock()
		defer clockMu.Unlock()
		start = time.Now().UTC().Truncate(time.Second).Add(45 * time.Second)
		end = start.Add(20 * time.Second)
		e.CreateChangeWindow(t, blackout(name, start, end))
		bundle = e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
		e.WaitGateReady(t, a.ns, bundle, "prod", "outside-freeze", true, "= true", gateTimeout)
		e.WaitChangeWindow(t, name, gateTimeout, "not started", windowState(false, "blackout starts at "+start.Format(time.RFC3339), metav1.ConditionTrue))
		require.True(t, time.Now().Before(start), "the setup took longer than the 45s before the window starts")

		closed = e.WaitGateReady(t, a.ns, bundle, "prod", "outside-freeze", false, "= false", time.Minute)
		e.WaitChangeWindow(t, name, 5*time.Second, "active", windowState(true, "blackout until "+end.Format(time.RFC3339), metav1.ConditionTrue))
		opened = e.WaitGateReady(t, a.ns, bundle, "prod", "outside-freeze", true, "= true", time.Minute)
		e.WaitChangeWindow(t, name, 5*time.Second, "ended", windowState(false, "blackout ended at "+end.Format(time.RFC3339), metav1.ConditionTrue))
	}()
	t.Logf("window %s-%s; gate closed at %s, opened at %s", start.Format(time.TimeOnly), end.Format(time.TimeOnly),
		closed.Status.LastEvaluatedAt.UTC().Format(time.TimeOnly), opened.Status.LastEvaluatedAt.UTC().Format(time.TimeOnly))
	for _, c := range []struct {
		boundary time.Time
		gate     *v1alpha1.PolicyGate
	}{{start, closed}, {end, opened}} {
		at := c.gate.Status.LastEvaluatedAt.Time
		assert.False(t, at.Before(c.boundary), "evaluated at %s, before the boundary %s", at, c.boundary)
		assert.LessOrEqual(t, at.Sub(c.boundary), 5*time.Second, "evaluated at %s, long after the boundary %s", at, c.boundary)
	}

	pr := e.WaitPR(t, a.repo, time.Minute, "test PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	require.NoError(t, e.Git.MergePR(context.Background(), a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}
