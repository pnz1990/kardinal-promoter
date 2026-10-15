// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"regexp"
	"strings"
)

// The docs walkthrough compares a command's output with the output the page
// shows for it (PageSample). Generated names, durations and ages differ from
// run to run, so SampleMismatches compares each sample line as a pattern.

var (
	// sampleDuration is a duration or an age: 812ms, 35s, 2m, 1m5s, 3m48s, 1.2s,
	// and the elapsed time ship --wait prints (+1m5s).
	sampleDuration = regexp.MustCompile(`^[+(]?(\d+(\.\d+)?(ms|s|m|h))+\)?[,;.:]?$`)
	// sampleSkip are lines a non-interactive run does not print: the prompt
	// on a terminal, and the browser the dashboard opens.
	sampleSkip = regexp.MustCompile(`\[y/N\]|Opening in browser`)
)

// samplePattern is the regular expression for one sample line: whitespace
// runs match any whitespace, a duration any word, "<name>-xxxxx" (a name of
// names with the five-character suffix Kubernetes generates) that name with
// any suffix, and "…" anything.
func samplePattern(line string, names []string) *regexp.Regexp {
	var parts []string
	for _, tok := range strings.Fields(line) {
		if sampleDuration.MatchString(tok) {
			parts = append(parts, `\S+`)
			continue
		}
		q := regexp.QuoteMeta(tok)
		for _, n := range names {
			q = regexp.MustCompile(regexp.QuoteMeta(regexp.QuoteMeta(n))+`-[a-z0-9]{5}\b`).
				ReplaceAllString(q, regexp.QuoteMeta(n)+`-[a-z0-9]+`)
		}
		q = strings.ReplaceAll(q, "…", ".*")
		parts = append(parts, q)
	}
	return regexp.MustCompile(`^\s*` + strings.Join(parts, `\s+`) + `\s*$`)
}

// SampleMismatches returns the lines of sample that output does not have, in
// order: each sample line must match an output line after the one the line
// before it matched. A sample line that optional matches may be missing (a
// state the command can skip on a fast cluster). names are the generated
// name prefixes in the sample (Pipeline names: demo-zhxk2 matches demo-q8m4t).
func SampleMismatches(sample, output string, optional *regexp.Regexp, names ...string) []string {
	out := strings.Split(output, "\n")
	var missing []string
	at := 0
	for _, line := range strings.Split(sample, "\n") {
		if strings.TrimSpace(line) == "" || sampleSkip.MatchString(line) {
			continue
		}
		re := samplePattern(line, names)
		found := -1
		for i := at; i < len(out); i++ {
			if re.MatchString(out[i]) {
				found = i
				break
			}
		}
		switch {
		case found >= 0:
			at = found + 1
		case optional != nil && optional.MatchString(line):
		default:
			missing = append(missing, line)
		}
	}
	return missing
}
