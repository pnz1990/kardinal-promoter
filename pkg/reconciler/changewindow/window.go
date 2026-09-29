// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package changewindow

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Window types.
const (
	TypeBlackout  = "blackout"
	TypeRecurring = "recurring"
)

// Result is the state of a ChangeWindow at one instant.
type Result struct {
	// Active is true when the window blocks promotions. An invalid spec is
	// active: a freeze that cannot be evaluated must not let promotions through.
	Active bool
	// Reason explains Active. It only changes at a boundary, so writing it to
	// status does not churn.
	Reason string
	// Next is the next instant the result can change. Zero when it never will
	// (a blackout that has ended, an invalid spec).
	Next time.Time
	// Err is set when the spec is invalid.
	Err error
}

// Evaluate returns whether the window described by spec is active at now.
//
//   - blackout: active from spec.start (inclusive) to spec.end (exclusive).
//   - recurring: spec.schedule describes when promotions are ALLOWED; the
//     window is active (blocking) at every other time. allowedDays empty means
//     every day, allowedHours empty means the whole day, timezone empty means
//     UTC. An overnight range ("22:00-06:00") belongs to the day it starts on.
//
// Any invalid spec returns Active=true with Err set.
func Evaluate(spec kardinalv1alpha1.ChangeWindowSpec, now time.Time) Result {
	switch spec.Type {
	case TypeBlackout:
		return evaluateBlackout(spec, now)
	case TypeRecurring:
		return evaluateRecurring(spec.Schedule, now)
	default:
		return invalid(fmt.Errorf("unknown type %q (want blackout or recurring)", spec.Type))
	}
}

func invalid(err error) Result {
	return Result{Active: true, Reason: fmt.Sprintf("invalid ChangeWindow, blocking: %s", err), Err: err}
}

func evaluateBlackout(spec kardinalv1alpha1.ChangeWindowSpec, now time.Time) Result {
	start, end := spec.Start.Time, spec.End.Time
	if start.IsZero() || end.IsZero() {
		return invalid(fmt.Errorf("type blackout requires spec.start and spec.end"))
	}
	if !end.After(start) {
		return invalid(fmt.Errorf("spec.end %s is not after spec.start %s",
			end.UTC().Format(time.RFC3339), start.UTC().Format(time.RFC3339)))
	}
	switch {
	case now.Before(start):
		return Result{Reason: "blackout starts at " + start.UTC().Format(time.RFC3339), Next: start}
	case now.Before(end):
		return Result{Active: true, Reason: "blackout until " + end.UTC().Format(time.RFC3339), Next: end}
	default:
		return Result{Reason: "blackout ended at " + end.UTC().Format(time.RFC3339)}
	}
}

// schedule is a parsed ChangeWindowSchedule. Times are minutes after local midnight.
type schedule struct {
	loc        *time.Location
	days       map[time.Weekday]bool
	start, end int
	desc       string
}

func parseSchedule(s *kardinalv1alpha1.ChangeWindowSchedule) (*schedule, error) {
	if s == nil {
		return nil, fmt.Errorf("type recurring requires spec.schedule")
	}
	out := &schedule{loc: time.UTC, days: map[time.Weekday]bool{}, start: 0, end: 24 * 60}
	if s.Timezone != "" {
		loc, err := time.LoadLocation(s.Timezone)
		if err != nil {
			return nil, fmt.Errorf("spec.schedule.timezone %q: %w", s.Timezone, err)
		}
		out.loc = loc
	}
	if len(s.AllowedDays) == 0 {
		for d := time.Sunday; d <= time.Saturday; d++ {
			out.days[d] = true
		}
	}
	for _, name := range s.AllowedDays {
		d, ok := parseDay(name)
		if !ok {
			return nil, fmt.Errorf("spec.schedule.allowedDays: %q is not a day (want Mon, Tue, Wed, Thu, Fri, Sat or Sun)", name)
		}
		out.days[d] = true
	}
	if s.AllowedHours != "" {
		from, to, ok := strings.Cut(s.AllowedHours, "-")
		if !ok {
			return nil, fmt.Errorf("spec.schedule.allowedHours %q: want HH:MM-HH:MM", s.AllowedHours)
		}
		var err error
		if out.start, err = parseClock(strings.TrimSpace(from), false); err != nil {
			return nil, fmt.Errorf("spec.schedule.allowedHours %q: %w", s.AllowedHours, err)
		}
		if out.end, err = parseClock(strings.TrimSpace(to), true); err != nil {
			return nil, fmt.Errorf("spec.schedule.allowedHours %q: %w", s.AllowedHours, err)
		}
		if out.start == out.end {
			return nil, fmt.Errorf("spec.schedule.allowedHours %q: start and end are equal", s.AllowedHours)
		}
	}
	days := "every day"
	if len(s.AllowedDays) > 0 {
		days = strings.Join(s.AllowedDays, ",")
	}
	hours := "all day"
	if s.AllowedHours != "" {
		hours = s.AllowedHours
	}
	out.desc = fmt.Sprintf("%s %s %s", days, hours, out.loc.String())
	return out, nil
}

// parseDay accepts "Mon" or "Monday", case-insensitively.
func parseDay(name string) (time.Weekday, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	for d := time.Sunday; d <= time.Saturday; d++ {
		full := strings.ToLower(d.String())
		if key == full || key == full[:3] {
			return d, true
		}
	}
	return 0, false
}

// parseClock parses "HH:MM" into minutes after midnight. "24:00" is accepted
// only as an end time.
func parseClock(s string, isEnd bool) (int, error) {
	hh, mm, ok := strings.Cut(s, ":")
	if !ok || len(hh) != 2 || len(mm) != 2 {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	h, errH := strconv.Atoi(hh)
	m, errM := strconv.Atoi(mm)
	if errH != nil || errM != nil || h < 0 || m < 0 || m > 59 {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	if h == 24 && m == 0 && isEnd {
		return 24 * 60, nil
	}
	if h > 23 {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	return h*60 + m, nil
}

// allowed reports whether local time t is inside the allowed window.
func (s *schedule) allowed(t time.Time) bool {
	minute := t.Hour()*60 + t.Minute()
	if s.start < s.end {
		return s.days[t.Weekday()] && minute >= s.start && minute < s.end
	}
	// Overnight: [start, 24:00) on an allowed day, or [00:00, end) the day after one.
	if minute >= s.start {
		return s.days[t.Weekday()]
	}
	if minute < s.end {
		return s.days[(t.Weekday()+6)%7]
	}
	return false
}

func evaluateRecurring(spec *kardinalv1alpha1.ChangeWindowSchedule, now time.Time) Result {
	s, err := parseSchedule(spec)
	if err != nil {
		return invalid(err)
	}
	local := now.In(s.loc)
	res := Result{Next: s.next(local)}
	if s.allowed(local) {
		res.Reason = "inside the allowed window " + s.desc
	} else {
		res.Active = true
		res.Reason = "outside the allowed window " + s.desc
	}
	return res
}

// next returns the first candidate boundary (local midnight, start or end of
// the allowed hours) after t. The state can only change at one of them.
func (s *schedule) next(t time.Time) time.Time {
	y, m, d := t.Date()
	var best time.Time
	for day := 0; day <= 8; day++ {
		for _, minute := range []int{0, s.start, s.end} {
			c := time.Date(y, m, d+day, minute/60, minute%60, 0, 0, s.loc)
			if c.After(t) && (best.IsZero() || c.Before(best)) {
				best = c
			}
		}
	}
	return best
}
