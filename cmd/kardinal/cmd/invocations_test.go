// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// usageError matches the errors of an invocation the CLI does not accept:
// an unknown command or flag, a wrong number of arguments, a missing or
// conflicting pipeline or environment. Any other error (no cluster to reach,
// a file that does not exist) means the invocation was accepted.
var usageError = regexp.MustCompile(`unknown (command|flag|shorthand)|accepts .* arg|requires at least|required flag|given twice|which (environment|pipeline)|invalid argument|flag needs an argument|is required`)

// skipInvocation lists commands the test does not run: they act on the host
// (open a browser, read a terminal) before they need a cluster.
var skipInvocation = map[string]bool{"dashboard": true, "init": true, "completion": true}

// TestInvocations_Compatible (C2): every invocation the old and the new forms
// allow keeps working. legacy lists the forms the CLI took before the
// <pipeline> [<env>] positionals (flags --env, --stage, --pipeline); new
// lists the positional forms; documented adds every kardinal command line
// in the docs' code blocks.
//
// Covers CLI-MODEL-01.
func TestInvocations_Compatible(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	legacy := []string{
		"promote my-app --env prod",
		"promote my-app -e prod",
		"rollback my-app --env prod",
		"rollback my-app --env prod --to my-app-v1 --hold --reason incident --hold-expires-in 24h",
		"release-hold my-app --env prod",
		"override my-app --stage prod --gate no-weekend --reason hotfix --expires-in 4h",
		"override my-app --gate no-weekend --reason hotfix",
		"history my-app --env prod --limit 5",
		"logs my-app --env prod --bundle b -f",
		"explain my-app --env prod --watch",
		"metrics --pipeline my-app --env prod --days 7",
		"policy simulate --pipeline my-app --env prod --time \"Saturday 3pm\"",
		"policy list --pipeline my-app",
		"audit summary --pipeline my-app --since 7d",
		"approve my-app-v1 --env prod",
		"create bundle my-app --image app:1.4",
		"get steps my-app -w",
		"get bundles my-app --active",
		"get auditevents --pipeline my-app --env prod",
		"status my-app",
		"pause my-app",
		"resume my-app",
	}
	newForms := []string{
		"promote my-app prod",
		"rollback my-app prod",
		"rollback my-app prod --hold --reason incident --hold-expires-in 2d",
		"release-hold my-app prod",
		"unhold my-app prod",
		"override my-app prod --gate no-weekend --reason hotfix --expires-in 1d",
		"override my-app --env prod --gate no-weekend --reason hotfix",
		"history my-app prod",
		"logs my-app prod -f",
		"explain my-app prod -w",
		"metrics my-app",
		"metrics my-app prod --days 7",
		"policy simulate my-app prod --time \"Saturday 3pm\"",
		"policy list my-app",
		"audit summary my-app --since 7d",
		"get steps my-app -A",
		"get bundles -A",
		"get auditevents -A -e prod",
		"status my-app -w",
		"promote my-app prod --env prod",
	}
	run := func(t *testing.T, line string) {
		t.Helper()
		args := splitWords(t, line)
		if len(args) == 0 || skipInvocation[args[0]] {
			return
		}
		_, err := executeRoot(t, args...)
		if err != nil {
			assert.NotRegexp(t, usageError, err.Error(), "kardinal %s", line)
		}
	}
	for _, line := range append(legacy, newForms...) {
		t.Run(line, func(t *testing.T) { run(t, line) })
	}
	for _, line := range documentedInvocations(t) {
		t.Run("docs: "+line, func(t *testing.T) { run(t, line) })
	}
}

// TestInvocations_Conflicts (C2): a positional and a flag that disagree are
// refused before the command does anything, and a missing environment says
// how to give it.
//
// Covers CLI-MODEL-01.
func TestInvocations_Conflicts(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	cases := map[string]string{
		"promote my-app prod --env uat":                            `the environment is given twice: "prod" as an argument and "uat" with --env`,
		"rollback my-app prod --stage uat":                         `the environment is given twice: "prod" as an argument and "uat" with --stage`,
		"rollback my-app prod -e uat":                              `"uat" with --env`,
		"metrics my-app --pipeline other":                          `the pipeline is given twice: "my-app" as an argument and "other" with --pipeline`,
		"policy simulate my-app prod --pipeline other":             `the pipeline is given twice`,
		"promote my-app":                                           `which environment? Give it after the pipeline: kardinal promote my-app <env>`,
		"release-hold my-app":                                      `which environment?`,
		"metrics":                                                  `which pipeline?`,
		"override my-app prod --gate g --reason r --expires-in 2x": `invalid argument "2x" for "--expires-in" flag: want 30m, 4h or 2d`,
	}
	for line, want := range cases {
		t.Run(line, func(t *testing.T) {
			_, err := executeRoot(t, splitWords(t, line)...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
		})
	}
}

// documentedInvocations returns every kardinal command line in the fenced
// code blocks of README.md and docs/, without "kardinal", up to the first
// shell operator or comment.
func documentedInvocations(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..", "..")
	files := []string{filepath.Join(root, "README.md")}
	require.NoError(t, filepath.WalkDir(filepath.Join(root, "docs"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// The generated reference repeats the commands' own examples, and
		// design notes and the changelog quote forms of their time.
		if d.IsDir() && (d.Name() == "reference" || d.Name() == "design") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".md") && d.Name() != "changelog.md" {
			files = append(files, p)
		}
		return nil
	}))
	commands := map[string]bool{}
	for _, c := range NewRootCmd().Commands() {
		commands[c.Name()] = true
		for _, a := range c.Aliases {
			commands[a] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		in := false
		var cont string
		for _, raw := range strings.Split(string(b), "\n") {
			line := strings.TrimSpace(raw)
			if strings.HasPrefix(line, "```") {
				in, cont = !in, ""
				continue
			}
			if !in {
				continue
			}
			if cont != "" {
				line = cont + " " + line
				cont = ""
			}
			if strings.HasSuffix(line, "\\") {
				cont = strings.TrimSuffix(line, "\\")
				continue
			}
			line = strings.TrimPrefix(line, "$ ")
			rest, ok := strings.CutPrefix(line, "kardinal ")
			if !ok {
				continue
			}
			for _, op := range []string{" | ", " && ", " ; ", " # ", " > ", " 2>", " &"} {
				if i := strings.Index(rest, op); i >= 0 {
					rest = rest[:i]
				}
			}
			rest = strings.TrimSpace(rest)
			first := strings.Fields(rest)
			if len(first) == 0 || !commands[first[0]] || strings.ContainsAny(rest, "$`[") || seen[rest] {
				continue
			}
			// A syntax line, not an invocation: a bare command path
			// ("policy simulate") or one ending in a flag with no value.
			if last := first[len(first)-1]; strings.HasPrefix(last, "-") && !strings.Contains(last, "=") ||
				len(first) <= 2 && !strings.ContainsAny(rest, "-<") && isCommandPath(first) {
				continue
			}
			seen[rest] = true
			out = append(out, rest)
		}
	}
	require.NotEmpty(t, out)
	return out
}

// isCommandPath reports whether words name a command and nothing else.
func isCommandPath(words []string) bool {
	c, rest, err := NewRootCmd().Find(words)
	return err == nil && len(rest) == 0 && c.HasParent() && (len(words) == 1 || c.Name() == words[len(words)-1])
}

// splitWords splits a command line on spaces, keeping "double quoted" words.
func splitWords(t *testing.T, s string) []string {
	t.Helper()
	var out []string
	var cur strings.Builder
	quoted, any := false, false
	for _, r := range s {
		switch {
		case r == '"':
			quoted, any = !quoted, true
		case r == ' ' && !quoted:
			if any || cur.Len() > 0 {
				out = append(out, cur.String())
			}
			cur.Reset()
			any = false
		default:
			cur.WriteRune(r)
		}
	}
	if any || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// TestDurationFlag (C2): a duration flag prints the way it is typed, and no
// default when it is unset.
//
// Covers CLI-MODEL-01.
func TestDurationFlag(t *testing.T) {
	for in, want := range map[string]string{"1h": "1h", "90m": "1h30m", "2d": "2d", "45s": "45s", "30m": "30m", "36h": "36h"} {
		var d time.Duration
		require.NoError(t, durationValue{&d}.Set(in), in)
		assert.Equal(t, want, durationValue{&d}.String(), in)
	}
	var zero time.Duration
	assert.Equal(t, "", durationValue{&zero}.String())

	out, err := executeRoot(t, "rollback", "--help")
	require.NoError(t, err)
	assert.NotContains(t, out, "(default 0s)")
	out, err = executeRoot(t, "override", "--help")
	require.NoError(t, err)
	assert.Contains(t, out, "(default 1h)")
}
