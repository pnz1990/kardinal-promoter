// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/coverage"
)

// TestE2ECoverage keeps test/e2e/coverage.tsv honest: a row is "covered" if
// and only if a test's doc comment names it, live and deprecated rows are
// covered only by live tests, every live test names its rows and runs in
// some suite, and a row's suite runs one of its tests.
func TestE2ECoverage(t *testing.T) {
	root := repoRoot(t)
	rows, err := coverage.Rows(root)
	require.NoError(t, err)
	byID := map[string]coverage.Row{}
	for _, r := range rows {
		assert.Regexp(t, coverage.ID, r.ID)
		assert.NotContains(t, byID, r.ID, "duplicate row")
		byID[r.ID] = r
		assert.Contains(t, []string{"live", "contract", "deprecated"}, r.Tier, r.ID)
		assert.Contains(t, []string{"covered", "todo", "known-bug"}, r.Status, r.ID)
		assert.NotEmpty(t, r.Suite, r.ID)
		assert.NotEmpty(t, r.Feature, r.ID)
		assert.NotEmpty(t, r.Source, r.ID)
	}

	tests, err := coverage.Tests(root)
	require.NoError(t, err)
	runs, err := coverage.SuiteRuns(root)
	require.NoError(t, err)
	coveredBy := map[string][]string{}
	testsOf := map[string][]string{}
	for _, ct := range tests {
		name := ct.File + ":" + ct.Name
		if ct.Live {
			assert.NotEmpty(t, ct.IDs, "%s: a live test's doc comment says which rows it covers: \"Covers ID, ID.\"", name)
			inSuite := false
			for _, re := range runs {
				inSuite = inSuite || re.MatchString(ct.Name)
			}
			assert.True(t, inSuite, "%s: no suite in hack/e2e/up.sh runs it", name)
		}
		for _, id := range ct.IDs {
			row, ok := byID[id]
			if !assert.True(t, ok, "%s covers %s, which is not in %s", name, id, coverage.File) {
				continue
			}
			if row.Tier != "contract" {
				assert.True(t, ct.Live, "%s covers %s row %s; only a test in %s can", name, row.Tier, id, coverage.LiveDir)
			}
			coveredBy[id] = append(coveredBy[id], name)
			testsOf[id] = append(testsOf[id], ct.Name)
		}
	}

	// A live or deprecated row's suite is the hack/e2e/up.sh suite that runs
	// one of its tests; a contract row's tests are unit tests.
	for _, r := range rows {
		if r.Tier == "contract" {
			assert.Equal(t, "unit", r.Suite, "%s: a contract row's suite is unit", r.ID)
			continue
		}
		re, ok := runs[r.Suite]
		if !assert.True(t, ok, "%s: suite %q is not in hack/e2e/up.sh", r.ID, r.Suite) || len(testsOf[r.ID]) == 0 {
			continue
		}
		inSuite := false
		for _, name := range testsOf[r.ID] {
			inSuite = inSuite || re.MatchString(name)
		}
		assert.True(t, inSuite, "%s: suite %s runs none of its tests %v", r.ID, r.Suite, testsOf[r.ID])
	}

	counts := map[string][2]int{}
	for _, r := range rows {
		_, has := coveredBy[r.ID]
		if r.Status == "covered" || r.Status == "known-bug" {
			// A known-bug row's test reproduces an open bug (scale.KnownBug).
			assert.True(t, has, "%s is marked %s but no test covers it", r.ID, r.Status)
		} else {
			assert.False(t, has, "%s is marked todo but %v covers it; mark it covered", r.ID, coveredBy[r.ID])
		}
		c := counts[r.Tier]
		c[1]++
		if has {
			c[0]++
		}
		counts[r.Tier] = c
	}
	tiers := make([]string, 0, len(counts))
	for tier := range counts {
		tiers = append(tiers, tier)
	}
	sort.Strings(tiers)
	for _, tier := range tiers {
		t.Logf("%s: %d of %d rows covered", tier, counts[tier][0], counts[tier][1])
	}
}

// sourceRef is a source ref: a path in the repo (a file or a directory), or
// lines of a file: path:N or path:N-M.
var sourceRef = regexp.MustCompile(`^([^:\s]+)(?::([0-9]+)(?:-([0-9]+))?)?$`)

// tableSeparator is the line under the header of a markdown table, such as
// |---|---| or | :--- | ---: |. Each cell needs three dashes, so a YAML "- |"
// line is not one.
var tableSeparator = regexp.MustCompile(`^\|?(\s*:?-{3,}:?\s*\|)+\s*(:?-{3,}:?)?$`)

// TestE2ECoverage_SourceRefs: every source ref of every row names a path in
// the repo; the changelog is cited by entry (changelogRefProblem), not line. A ref with lines names lines that exist, and its first line has
// content, so a reader of the row lands on the behavior. A covered rollback row
// names lines in every ref.
func TestE2ECoverage_SourceRefs(t *testing.T) {
	root := repoRoot(t)
	rows, err := coverage.Rows(root)
	require.NoError(t, err)
	for _, p := range sourceRefProblems(root, rows) {
		t.Error(p)
	}
}

// TestSourceRefProblems: each rule of TestE2ECoverage_SourceRefs refuses a ref
// that breaks it, and a ref that keeps every rule passes.
func TestSourceRefProblems(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "pkg"), 0o755))
	doc := "# Title\n\n| a | b |\n|---|---|\n---\ntext\n- |\n| :--- | ---: |\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "doc.md"), []byte(doc), 0o600))
	changelog := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- **New thing** — text\n- **Fix with `code`: a colon** (#1) — text\n" +
		"- **Twice** — one\n- **Twice** — two\n- **Shared title** — again\n  - **Not top level** — nested\n- plain bullet\n\n" +
		"## [v1.0.0] — 2026-10-01\n\n### Before you upgrade\n\nprose\n\n" +
		"## [v0.9.0] — 2026-09-01\n\n### Before you upgrade\n\n### Fixed\n\n- **Shared title** — first\n"
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "docs", "changelog.md"), []byte(changelog), 0o600))
	for _, tc := range []struct {
		source, area, status string
		// problem is part of the one problem expected, or "" for none.
		problem string
	}{
		{source: "doc.md"},
		{source: "pkg"},
		{source: "doc.md:1"},
		{source: "doc.md:3-6"},
		{source: "doc.md:6; pkg"},
		{source: "doc.md:7"},
		{source: "doc.md:1", area: "rollback", status: "covered"},
		{source: "doc.md", area: "rollback", status: "todo"},
		{source: "doc.md handleBake", problem: "is not path, path:N or path:N-M"},
		{source: "doc.md:handleBake", problem: "is not path, path:N or path:N-M"},
		{source: "doc.md:1;doc.md:6", problem: "is not path, path:N or path:N-M"},
		{source: "missing.md", problem: "names no path in the repo"},
		{source: "../doc.md", problem: "names a path outside the repo"},
		{source: "/doc.md:1", problem: "names a path outside the repo"},
		{source: "missing.md:1", problem: "names lines of no file"},
		{source: "pkg:1", problem: "names lines of no file"},
		{source: "doc.md:0", problem: "is outside doc.md, which has 8 lines"},
		{source: "doc.md:9", problem: "is outside doc.md, which has 8 lines"},
		{source: "doc.md:6-9", problem: "is outside doc.md, which has 8 lines"},
		{source: "doc.md:3-2", problem: "is outside doc.md, which has 8 lines"},
		{source: "doc.md:1; doc.md:2-3", problem: `"doc.md:2-3" starts on a blank line`},
		{source: "doc.md:4", problem: "starts on a markdown table separator"},
		{source: "doc.md:8", problem: "starts on a markdown table separator"},
		{source: "doc.md:5-6", problem: "starts on a bare ---"},
		{source: "doc.md", area: "rollback", status: "covered", problem: "names no lines"},
		// docs/changelog.md is cited by entry title or heading, not line.
		{source: "docs/changelog.md#New thing"},
		{source: "docs/changelog.md#Fix with `code`: a colon"},
		{source: "docs/changelog.md#Before you upgrade@v1.0.0"},
		{source: "docs/changelog.md#Shared title@v0.9.0"},
		{source: "docs/changelog.md#Fixed@v0.9.0", area: "rollback", status: "covered"},
		{source: "docs/changelog.md#New thing; pkg"},
		{source: "docs/changelog.md#New", problem: "names no `- **Title**` entry or heading"},
		{source: "docs/changelog.md#new thing", problem: "names no `- **Title**` entry or heading"},
		{source: "docs/changelog.md#New thing@v0.9.0", problem: "names no `- **Title**` entry or heading"},
		{source: "docs/changelog.md#Twice", problem: "appears 2 times in release Unreleased"},
		{source: "docs/changelog.md#Shared title", problem: "several releases (Unreleased, v0.9.0): add @<release>"},
		{source: "docs/changelog.md#Before you upgrade", problem: "several releases (v0.9.0, v1.0.0): add @<release>"},
		{source: "docs/changelog.md#Release title", problem: "names no `- **Title**` entry or heading"},
		{source: "docs/changelog.md"},
		{source: "docs/changelog.md:7", problem: "cites the changelog by line"},
	} {
		row := coverage.Row{ID: "TEST-01", Area: tc.area, Status: tc.status, Source: tc.source}
		got := sourceRefProblems(root, []coverage.Row{row})
		if tc.problem == "" {
			assert.Empty(t, got, "source %q", tc.source)
			continue
		}
		if assert.Len(t, got, 1, "source %q", tc.source) {
			assert.Contains(t, got[0], tc.problem, "source %q", tc.source)
		}
	}
}

// sourceRefProblems returns one message for each source ref of rows that
// breaks a rule of TestE2ECoverage_SourceRefs. The paths are relative to root.
func sourceRefProblems(root string, rows []coverage.Row) []string {
	var problems []string
	files := map[string][]string{}
	for _, r := range rows {
		for _, ref := range strings.Split(r.Source, "; ") {
			if p := sourceRefProblem(root, files, r, ref); p != "" {
				problems = append(problems, fmt.Sprintf("%s: ref %q %s", r.ID, ref, p))
			}
		}
	}
	return problems
}

// sourceRefProblem returns the rule that ref, a source ref of row r, breaks,
// or "" when it keeps them all. files holds the lines of the files read so far.
func sourceRefProblem(root string, files map[string][]string, r coverage.Row, ref string) string {
	if anchor, ok := strings.CutPrefix(ref, changelogPath+"#"); ok {
		return changelogRefProblem(root, files, anchor)
	}
	m := sourceRef.FindStringSubmatch(ref)
	if m == nil {
		return "is not path, path:N or path:N-M"
	}
	path := m[1]
	if !filepath.IsLocal(path) {
		return "names a path outside the repo"
	}
	if path == changelogPath && m[2] != "" {
		return "cites the changelog by line: write " + changelogPath + "#<entry title or heading>, which an entry added above does not shift"
	}
	if m[2] == "" {
		if r.Area == "rollback" && r.Status == "covered" {
			return "names no lines; a covered rollback row writes path:N or path:N-M"
		}
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			return fmt.Sprintf("names no path in the repo: %v", err)
		}
		return ""
	}
	lines, ok := files[path]
	if !ok {
		b, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return fmt.Sprintf("names lines of no file: %v", err)
		}
		lines = strings.Split(string(b), "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		files[path] = lines
	}
	first, _ := strconv.Atoi(m[2])
	last := first
	if m[3] != "" {
		last, _ = strconv.Atoi(m[3])
	}
	if first < 1 || first > last || last > len(lines) {
		return fmt.Sprintf("is outside %s, which has %d lines", path, len(lines))
	}
	switch line := strings.TrimSpace(lines[first-1]); {
	case line == "":
		return "starts on a blank line"
	case line == "---":
		return "starts on a bare ---"
	case tableSeparator.MatchString(line):
		return "starts on a markdown table separator"
	}
	return ""
}

// changelogPath is the one file cited by entry title rather than by line:
// every new entry shifts the lines of all the older ones, so line refs into
// it made every open PR conflict on coverage.tsv.
const changelogPath = "docs/changelog.md"

// changelogRelease is a "## [<release>]" heading of the changelog.
var changelogRelease = regexp.MustCompile(`^## \[([^\]]+)\]`)

// changelogAnchor is what a changelog ref may name: the bold title of an
// entry ("- **Title** — ...") or the text of a heading below a release
// ("### Fixed", "#### From v0.8.1").
var changelogAnchor = regexp.MustCompile(`^- \*\*(.+?)\*\*|^#{3,6} (.+?)\s*$`)

// changelogAnchors maps each anchor of the changelog to the releases it
// appears in and how often in each.
func changelogAnchors(lines []string) map[string]map[string]int {
	anchors := map[string]map[string]int{}
	release := ""
	for _, line := range lines {
		if m := changelogRelease.FindStringSubmatch(line); m != nil {
			release = m[1]
			continue
		}
		m := changelogAnchor.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text := m[1] + m[2]
		if anchors[text] == nil {
			anchors[text] = map[string]int{}
		}
		anchors[text][release]++
	}
	return anchors
}

// changelogRefProblem checks the anchor of a docs/changelog.md#<anchor> ref:
// it names an entry title or a heading exactly, once in its release. An
// anchor that appears in several releases names one with a suffix
// "@<release>" (the release heading's name, such as v0.9.0). A release's
// "Unreleased" heading is renamed when it ships, so qualify only what needs
// it.
func changelogRefProblem(root string, files map[string][]string, anchor string) string {
	lines, ok := files[changelogPath]
	if !ok {
		b, err := os.ReadFile(filepath.Join(root, changelogPath))
		if err != nil {
			return fmt.Sprintf("names an entry of no file: %v", err)
		}
		lines = strings.Split(string(b), "\n")
		files[changelogPath] = lines
	}
	anchors := changelogAnchors(lines)
	text, release := anchor, ""
	if i := strings.LastIndex(anchor, "@"); i > 0 {
		if _, known := releasesOf(anchors)[anchor[i+1:]]; known {
			text, release = anchor[:i], anchor[i+1:]
		}
	}
	in := anchors[text]
	if release != "" {
		in = map[string]int{release: anchors[text][release]}
		if in[release] == 0 {
			in = nil
		}
	}
	if len(in) == 0 {
		return "names no `- **Title**` entry or heading of " + changelogPath
	}
	var releases []string
	for rel, n := range in {
		if n > 1 {
			return fmt.Sprintf("names an entry that appears %d times in release %s", n, rel)
		}
		releases = append(releases, rel)
	}
	if len(releases) > 1 {
		sort.Strings(releases)
		return fmt.Sprintf("names an entry of several releases (%s): add @<release>", strings.Join(releases, ", "))
	}
	return ""
}

// releasesOf is the set of release names of the changelog's anchors.
func releasesOf(anchors map[string]map[string]int) map[string]struct{} {
	out := map[string]struct{}{}
	for _, in := range anchors {
		for rel := range in {
			out[rel] = struct{}{}
		}
	}
	return out
}
