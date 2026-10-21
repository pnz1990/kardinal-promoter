// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// doctorSummary is kardinal doctor's last line: the checks passed (1),
// warned (2) and failed (3).
var doctorSummary = regexp.MustCompile(`(?m)^(\d+) check\(s\) passed(?:, (\d+) warning\(s\))?(?:, (\d+) failed)?$`)

// doctorFailed is the stderr line of a doctor run with failed checks.
var doctorFailed = regexp.MustCompile(`(?m)^\d+ pre-flight check\(s\) failed\n`)

// doctorRow matches kardinal doctor's row of the check labelled exactly
// label (doctor pads labels to 32 characters and adds two spaces), with any
// hint under it: its result (1: PASS, WARN or FAIL) and detail (2).
func doctorRow(label string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^(PASS|WARN|FAIL)  ` + regexp.QuoteMeta(label) + ` {2,}(.*)\n(?: {40}.*\n)*`)
}

// DoctorCheck is the result (PASS, WARN or FAIL) and detail of the check
// labelled label in kardinal doctor's plain output stdout; ok is false
// when it has no such row.
func DoctorCheck(stdout, label string) (result, detail string, ok bool) {
	m := doctorRow(label).FindStringSubmatch(stdout)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// WithoutDoctorCheck is kardinal doctor's run r as if its check labelled
// label had not run: stdout without that check's row and hint, the summary
// counting the other checks only, and, when that check failed, the exit
// code and stderr of the other checks' failures alone. A check that reads
// the whole cluster reports what other tests running beside this one made
// (#2390), so a test comparing doctor's output exactly leaves it out.
// Exactly one such row is required.
func WithoutDoctorCheck(t *testing.T, r CLIResult, label string) CLIResult {
	t.Helper()
	out, result, detail, err := withoutDoctorCheck(r, label)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("left out of the comparison: %s  %s  %s", result, label, detail)
	return out
}

// withoutDoctorCheck is WithoutDoctorCheck, with the left-out row's result
// and detail, or an error when r does not print exactly one row of label
// and a summary.
func withoutDoctorCheck(r CLIResult, label string) (CLIResult, string, string, error) {
	rows := doctorRow(label).FindAllStringSubmatch(r.Stdout, -1)
	if len(rows) != 1 {
		return r, "", "", fmt.Errorf("kardinal doctor printed %d %q rows, want 1:\n%s", len(rows), label, r.Stdout)
	}
	out := strings.Replace(r.Stdout, rows[0][0], "", 1)
	m := doctorSummary.FindStringSubmatchIndex(out)
	if m == nil {
		return r, "", "", fmt.Errorf("kardinal doctor printed no summary:\n%s", r.Stdout)
	}
	count := func(i int) int {
		if m[2*i] < 0 {
			return 0
		}
		n, _ := strconv.Atoi(out[m[2*i]:m[2*i+1]]) // \d+
		return n
	}
	passed, warned, failed := count(1), count(2), count(3)
	switch rows[0][1] {
	case "PASS":
		passed--
	case "WARN":
		warned--
	default:
		failed--
		repl := ""
		if failed > 0 {
			repl = fmt.Sprintf("%d pre-flight check(s) failed\n", failed)
		} else {
			r.Code = 0
		}
		r.Stderr = doctorFailed.ReplaceAllLiteralString(r.Stderr, repl)
	}
	summary := fmt.Sprintf("%d check(s) passed", passed)
	if warned > 0 {
		summary += fmt.Sprintf(", %d warning(s)", warned)
	}
	if failed > 0 {
		summary += fmt.Sprintf(", %d failed", failed)
	}
	r.Stdout = out[:m[0]] + summary + out[m[1]:]
	return r, rows[0][1], rows[0][2], nil
}
