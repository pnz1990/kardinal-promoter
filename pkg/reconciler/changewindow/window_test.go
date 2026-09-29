// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changewindow_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func recurring(tz string, days []string, hours string) kardinalv1alpha1.ChangeWindowSpec {
	return kardinalv1alpha1.ChangeWindowSpec{
		Type:     changewindow.TypeRecurring,
		Schedule: &kardinalv1alpha1.ChangeWindowSchedule{Timezone: tz, AllowedDays: days, AllowedHours: hours},
	}
}

func blackout(start, end string) kardinalv1alpha1.ChangeWindowSpec {
	spec := kardinalv1alpha1.ChangeWindowSpec{Type: changewindow.TypeBlackout}
	if start != "" {
		spec.Start = metav1.NewTime(at(start))
	}
	if end != "" {
		spec.End = metav1.NewTime(at(end))
	}
	return spec
}

// TestEvaluate covers blackout and recurring windows (C04-gates-03, C08-api-config-05,
// C13b-design-02). Friday 2026-10-02, Saturday 2026-10-03.
func TestEvaluate(t *testing.T) {
	weekdays := []string{"Mon", "Tue", "Wed", "Thu", "Fri"}
	tests := []struct {
		name       string
		spec       kardinalv1alpha1.ChangeWindowSpec
		now        string
		wantActive bool
		wantNext   string // "" = no next boundary
		wantErr    bool
	}{
		{name: "blackout before start", spec: blackout("2026-12-20T00:00:00Z", "2027-01-02T00:00:00Z"),
			now: "2026-12-19T12:00:00Z", wantActive: false, wantNext: "2026-12-20T00:00:00Z"},
		{name: "blackout at start is active", spec: blackout("2026-12-20T00:00:00Z", "2027-01-02T00:00:00Z"),
			now: "2026-12-20T00:00:00Z", wantActive: true, wantNext: "2027-01-02T00:00:00Z"},
		{name: "blackout after end", spec: blackout("2026-12-20T00:00:00Z", "2027-01-02T00:00:00Z"),
			now: "2027-01-02T00:00:00Z", wantActive: false},
		{name: "blackout without end is invalid", spec: blackout("2026-12-20T00:00:00Z", ""),
			now: "2026-12-19T12:00:00Z", wantActive: true, wantErr: true},
		{name: "blackout end before start is invalid", spec: blackout("2026-12-20T00:00:00Z", "2026-12-01T00:00:00Z"),
			now: "2026-12-19T12:00:00Z", wantActive: true, wantErr: true},

		{name: "recurring inside allowed hours", spec: recurring("", weekdays, "09:00-17:00"),
			now: "2026-10-02T10:00:00Z", wantActive: false, wantNext: "2026-10-02T17:00:00Z"},
		{name: "recurring at end is blocked", spec: recurring("", weekdays, "09:00-17:00"),
			now: "2026-10-02T17:00:00Z", wantActive: true, wantNext: "2026-10-03T00:00:00Z"},
		{name: "recurring on Saturday is blocked", spec: recurring("", weekdays, "09:00-17:00"),
			now: "2026-10-03T22:00:00Z", wantActive: true, wantNext: "2026-10-04T00:00:00Z"},
		{name: "recurring before start is blocked", spec: recurring("", weekdays, "09:00-17:00"),
			now: "2026-10-02T08:59:00Z", wantActive: true, wantNext: "2026-10-02T09:00:00Z"},
		{name: "timezone is applied", spec: recurring("America/Los_Angeles", weekdays, "09:00-17:00"),
			// 16:30 UTC is 09:30 PDT (UTC-7).
			now: "2026-10-02T16:30:00Z", wantActive: false, wantNext: "2026-10-03T00:00:00Z"},
		{name: "timezone blocks outside local hours", spec: recurring("America/Los_Angeles", weekdays, "09:00-17:00"),
			// 15:30 UTC is 08:30 PDT.
			now: "2026-10-02T15:30:00Z", wantActive: true, wantNext: "2026-10-02T16:00:00Z"},
		{name: "full day names and no hours", spec: recurring("", []string{"saturday", "Sunday"}, ""),
			now: "2026-10-03T03:00:00Z", wantActive: false, wantNext: "2026-10-04T00:00:00Z"},
		{name: "no days means every day", spec: recurring("", nil, "09:00-10:00"),
			now: "2026-10-03T09:30:00Z", wantActive: false, wantNext: "2026-10-03T10:00:00Z"},
		{name: "end 24:00", spec: recurring("", nil, "20:00-24:00"),
			now: "2026-10-03T23:59:00Z", wantActive: false, wantNext: "2026-10-04T00:00:00Z"},
		{name: "overnight range after midnight belongs to the start day", spec: recurring("", []string{"Fri"}, "22:00-06:00"),
			now: "2026-10-03T05:00:00Z", wantActive: false, wantNext: "2026-10-03T06:00:00Z"},
		{name: "overnight range does not open the day before", spec: recurring("", []string{"Fri"}, "22:00-06:00"),
			now: "2026-10-02T05:00:00Z", wantActive: true, wantNext: "2026-10-02T06:00:00Z"},

		{name: "recurring without schedule is invalid", spec: kardinalv1alpha1.ChangeWindowSpec{Type: "recurring"},
			now: "2026-10-02T10:00:00Z", wantActive: true, wantErr: true},
		{name: "bad timezone is invalid", spec: recurring("Mars/Olympus", weekdays, "09:00-17:00"),
			now: "2026-10-02T10:00:00Z", wantActive: true, wantErr: true},
		{name: "bad day is invalid", spec: recurring("", []string{"Funday"}, "09:00-17:00"),
			now: "2026-10-02T10:00:00Z", wantActive: true, wantErr: true},
		{name: "bad hours are invalid", spec: recurring("", weekdays, "9-17"),
			now: "2026-10-02T10:00:00Z", wantActive: true, wantErr: true},
		{name: "equal hours are invalid", spec: recurring("", weekdays, "09:00-09:00"),
			now: "2026-10-02T10:00:00Z", wantActive: true, wantErr: true},
		{name: "unknown type is invalid", spec: kardinalv1alpha1.ChangeWindowSpec{Type: "freeze"},
			now: "2026-10-02T10:00:00Z", wantActive: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := changewindow.Evaluate(tt.spec, at(tt.now))
			assert.Equal(t, tt.wantActive, res.Active, res.Reason)
			assert.NotEmpty(t, res.Reason)
			if tt.wantErr {
				require.Error(t, res.Err)
				assert.Contains(t, res.Reason, "invalid ChangeWindow")
				assert.True(t, res.Next.IsZero())
				return
			}
			require.NoError(t, res.Err)
			if tt.wantNext == "" {
				assert.True(t, res.Next.IsZero(), "next = %s", res.Next)
			} else {
				assert.True(t, at(tt.wantNext).Equal(res.Next), "next = %s, want %s", res.Next.UTC(), tt.wantNext)
			}
		})
	}
}

// TestEvaluate_ReasonIsStableBetweenBoundaries keeps status writes from churning.
func TestEvaluate_ReasonIsStableBetweenBoundaries(t *testing.T) {
	spec := recurring("Europe/Berlin", []string{"Mon", "Tue"}, "08:00-18:00")
	a := changewindow.Evaluate(spec, at("2026-10-03T01:00:00Z"))
	b := changewindow.Evaluate(spec, at("2026-10-03T20:00:00Z"))
	assert.Equal(t, a.Reason, b.Reason)
	assert.Contains(t, a.Reason, "Europe/Berlin")
}
