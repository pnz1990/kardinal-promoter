// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWithoutDoctorCheck checks WithoutDoctorCheck on each result of the
// left-out check (#2390): its row and hint go, the summary counts the other
// checks only, and a failure of its own no longer fails the run.
func TestWithoutDoctorCheck(t *testing.T) {
	const label = "Policy namespaces shared"
	row := func(icon, label, detail string) string { return fmt.Sprintf("%s  %-32s  %s\n", icon, label, detail) }
	hint := strings.Repeat(" ", 40) + "Label each namespace kardinal.io/policy-shareable=true\n"
	head := "\nkardinal-promoter pre-flight check\n" + row("PASS", "CRDs installed", "all served")
	for name, tc := range map[string]struct{ in, want CLIResult }{
		"pass": {
			CLIResult{Stdout: head + row("PASS", label, "every spec.policyNamespaces entry agreed") +
				row("PASS", `Pipeline "podinfo"`, "ok") + "\n3 check(s) passed\n"},
			CLIResult{Stdout: head + row("PASS", `Pipeline "podinfo"`, "ok") + "\n2 check(s) passed\n"},
		},
		"warn, another test's Pipeline": {
			CLIResult{Stdout: head + row("WARN", label, "v0.14.0 refuses them: a/p (b) (all namespaces)") + hint +
				"\n1 check(s) passed, 1 warning(s)\n"},
			CLIResult{Stdout: head + "\n1 check(s) passed\n"},
		},
		"warn beside another warning": {
			CLIResult{Stdout: head + row("WARN", "SCM token", "no Deployment") + row("WARN", label, "x") + hint +
				"\n1 check(s) passed, 2 warning(s)\n"},
			CLIResult{Stdout: head + row("WARN", "SCM token", "no Deployment") + "\n1 check(s) passed, 1 warning(s)\n"},
		},
		"fail beside another failure": {
			CLIResult{Code: 1, Stdout: head + row("FAIL", label, "refused, their new versions wait: a/p (b)") + hint +
				row("FAIL", `Pipeline "missing"`, "not found") + "\n1 check(s) passed, 1 warning(s), 2 failed\n",
				Stderr: "2 pre-flight check(s) failed\n"},
			CLIResult{Code: 1, Stdout: head + row("FAIL", `Pipeline "missing"`, "not found") +
				"\n1 check(s) passed, 1 warning(s), 1 failed\n", Stderr: "1 pre-flight check(s) failed\n"},
		},
		"the only failure": {
			CLIResult{Code: 1, Stdout: head + row("FAIL", label, "refused, their new versions wait: a/p (b)") + hint +
				"\n1 check(s) passed, 1 failed\n", Stderr: "1 pre-flight check(s) failed\n"},
			CLIResult{Stdout: head + "\n1 check(s) passed\n"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, WithoutDoctorCheck(t, tc.in, label))
		})
	}
	// Exactly one row of the label, and a summary (QA-5 on #2393): two rows,
	// none, a longer label that starts with it, or no summary is an error.
	for name, stdout := range map[string]string{
		"two rows":            head + row("WARN", label, "a") + row("PASS", label, "b") + "\n2 check(s) passed, 1 warning(s)\n",
		"none":                head + "\n1 check(s) passed\n",
		"a longer label only": head + row("FAIL", label+" (prod)", "refused") + "\n1 check(s) passed, 1 failed\n",
		"no summary":          head + row("WARN", label, "a"),
	} {
		_, _, _, err := withoutDoctorCheck(CLIResult{Stdout: stdout}, label)
		assert.Error(t, err, name)
	}
	_, _, ok := DoctorCheck(head+row("FAIL", label+" (prod)", "refused"), label)
	assert.False(t, ok, "a longer label is another check")

	result, detail, ok := DoctorCheck(head+row("WARN", label, "v0.14.0 refuses them: a/p (b) (all namespaces)")+hint, label)
	assert.True(t, ok)
	assert.Equal(t, "WARN", result)
	assert.Equal(t, "v0.14.0 refuses them: a/p (b) (all namespaces)", detail)
	_, _, ok = DoctorCheck(head, label)
	assert.False(t, ok)
}
