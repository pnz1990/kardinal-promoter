// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/hack/changelog/fragments"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/coverage"
)

// TestE2ECoverage keeps test/e2e/coverage.tsv honest: a row is "covered" if
// and only if a test's doc comment names it, live and deprecated rows are
// covered only by live tests, every live test names its rows and runs in
// some suite, and a row's suite runs one of its tests.
func TestE2ECoverage(t *testing.T) {
	t.Parallel()
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

// sourceRef is a source ref: a path in the repo (a file or a directory),
// optionally with an anchor, path#anchor.
var sourceRef = regexp.MustCompile(`^([^#\s]+)(?:#(.+))?$`)

// lineRef is the old form, path:N or path:N-M, now refused.
var lineRef = regexp.MustCompile(`^[^#\s]+:[0-9]+(-[0-9]+)?$`)

// TestE2ECoverage_SourceRefs: every source ref of every row names a path in
// the repo, and an anchor, when it has one, names one place in that file: a
// Go declaration (Func, Type.Method, Type, a var or const) of a .go file, a
// heading of a .md file, or else text on exactly one line of the file. A
// covered rollback row names an anchor in every ref. docs/changelog.md is
// cited by entry title or heading, its unreleased entries included
// (docs/changelog.d; changelogRefProblem).
//
// Refs never name line numbers (#1894): any edit above a line moved it, so
// every pull request that touched a cited file rewrote other rows, and two
// unrelated pull requests conflicted on coverage.tsv. An anchor moves with
// its text.
func TestE2ECoverage_SourceRefs(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	rows, err := coverage.Rows(root)
	require.NoError(t, err)
	for _, p := range sourceRefProblems(root, rows) {
		t.Error(p)
	}
}

// TestE2ECoverage_RowsSorted: coverage.tsv lists its rows by id, each id
// once, so pull requests that add rows of different features add them in
// different places and do not conflict (#1894).
func TestE2ECoverage_RowsSorted(t *testing.T) {
	rows, err := coverage.Rows(repoRoot(t))
	require.NoError(t, err)
	for i := 1; i < len(rows); i++ {
		if rows[i-1].ID >= rows[i].ID {
			t.Errorf("row %s is after %s: keep coverage.tsv sorted by id (LC_ALL=C sort), each id once", rows[i].ID, rows[i-1].ID)
		}
	}
}

// TestSourceRefProblems: each rule of TestE2ECoverage_SourceRefs refuses a ref
// that breaks it, and a ref that keeps every rule passes.
func TestSourceRefProblems(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "pkg"), 0o755))
	doc := "# Title\n\n## Install\n\ntext once\ntwice\ntwice\n\n## Twice\n\n## Twice\n\n```\n## Not a heading\n```\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "doc.md"), []byte(doc), 0o600))
	src := "package pkg\n\ntype Reconciler struct{}\n\nfunc (r *Reconciler) Reconcile() {}\n\nfunc (r Other) Reconcile() {}\n\n" +
		"func New() {}\n\nconst (\n\tLimit = 3\n)\n\nvar a, b = 1, 2\n\nfunc dup() {}\n\n// once: a comment\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "r.go"), []byte(src), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "values.yaml"), []byte("a:\n  b: 1\nc:\n  b: 1\n"), 0o600))
	changelog := "# Changelog\n\n## [Unreleased]\n\n" + fragments.Marker + "\n\n" +
		"## [v1.0.0] — 2026-10-01\n\n### Before you upgrade\n\nprose\n\n- **Twice** — one\n- **Twice** — two\n\n" +
		"## [v0.9.0] — 2026-09-01\n\n### Before you upgrade\n\n### Fixed\n\n- **Shared title** — first\n  - **Not top level** — nested\n- plain bullet\n"
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs", "changelog.d"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "docs", "changelog.md"), []byte(changelog), 0o600))
	for name, body := range map[string]string{
		"10-new-thing.md":    "---\nsection: Added\n---\n\n- **New thing** — text\n",
		"11-fix.md":          "---\nsection: Fixed\n---\n\n- **Fix with `code`: a colon** (#11) — text\n",
		"12-shared-title.md": "---\nsection: Changed\n---\n\n- **Shared title** — again\n",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(root, "docs", "changelog.d", name), []byte(body), 0o600))
	}
	for _, tc := range []struct {
		source, area, status string
		// problem is part of the one problem expected, or "" for none.
		problem string
	}{
		{source: "doc.md"},
		{source: "pkg"},
		{source: "doc.md#Install"},
		{source: "doc.md#Title"},
		{source: "doc.md#text once"},
		{source: "doc.md#Install; pkg/r.go#New"},
		{source: "pkg/r.go#Reconciler"},
		{source: "pkg/r.go#Reconciler.Reconcile"},
		{source: "pkg/r.go#Other.Reconcile"},
		{source: "pkg/r.go#Limit"},
		{source: "pkg/r.go#b"},
		{source: "pkg/r.go#once: a comment"},
		{source: "values.yaml#a:"},
		{source: "doc.md#Install", area: "rollback", status: "covered"},
		{source: "doc.md", area: "rollback", status: "todo"},
		{source: "doc.md:1", problem: "names line numbers"},
		{source: "doc.md:3-6", problem: "names line numbers"},
		{source: "doc.md handleBake", problem: "is not path or path#anchor"},
		{source: "doc.md#Install;doc.md", problem: "names no Go declaration, heading or text"},
		{source: "missing.md", problem: "names no path in the repo"},
		{source: "missing.md#Install", problem: "names an anchor in no file"},
		{source: "pkg#New", problem: "names an anchor in no file"},
		{source: "../doc.md", problem: "names a path outside the repo"},
		{source: "/doc.md", problem: "names a path outside the repo"},
		{source: "doc.md#Nowhere", problem: "names no Go declaration, heading or text"},
		// Not a heading (it is in a code block), but text on one line.
		{source: "doc.md#Not a heading"},
		{source: "doc.md#twice", problem: "matches 2 lines of doc.md"},
		{source: "doc.md#Twice", problem: "names 2 headings of doc.md"},
		// Two methods and the type name hold the text: name the method.
		{source: "pkg/r.go#Reconcile", problem: "matches 3 lines of pkg/r.go"},
		{source: "values.yaml#b: 1", problem: "matches 2 lines of values.yaml"},
		{source: "doc.md", area: "rollback", status: "covered", problem: "names no anchor"},
		// docs/changelog.md is cited by entry title or heading; the
		// unreleased entries are the files in docs/changelog.d.
		{source: "docs/changelog.md#New thing"},
		{source: "docs/changelog.md#Fix with `code`: a colon"},
		{source: "docs/changelog.md#Before you upgrade@v1.0.0"},
		{source: "docs/changelog.md#Shared title@v0.9.0"},
		{source: "docs/changelog.md#Shared title@Unreleased"},
		{source: "docs/changelog.md#Fixed@v0.9.0", area: "rollback", status: "covered"},
		{source: "docs/changelog.md#New thing; pkg"},
		{source: "docs/changelog.md#New", problem: "names no `- **Title**` entry or heading"},
		{source: "docs/changelog.md#new thing", problem: "names no `- **Title**` entry or heading"},
		{source: "docs/changelog.md#New thing@v0.9.0", problem: "names no `- **Title**` entry or heading"},
		{source: "docs/changelog.md#Twice", problem: "appears 2 times in release v1.0.0"},
		{source: "docs/changelog.md#Shared title", problem: "several releases (Unreleased, v0.9.0): add @<release>"},
		{source: "docs/changelog.md#Before you upgrade", problem: "several releases (v0.9.0, v1.0.0): add @<release>"},
		{source: "docs/changelog.md#Not top level", problem: "names no `- **Title**` entry or heading"},
		{source: "docs/changelog.md"},
		{source: "docs/changelog.md:7", problem: "names line numbers"},
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
	if lineRef.MatchString(ref) {
		return "names line numbers, which move with every edit above them: write path#<Go declaration, heading or text on one line>"
	}
	m := sourceRef.FindStringSubmatch(ref)
	if m == nil {
		return "is not path or path#anchor"
	}
	path, anchor := m[1], m[2]
	if !filepath.IsLocal(path) {
		return "names a path outside the repo"
	}
	if filepath.Clean(path) == changelogPath && anchor != "" {
		return changelogRefProblem(root, files, anchor)
	}
	if anchor == "" {
		if r.Area == "rollback" && r.Status == "covered" {
			return "names no anchor; a covered rollback row writes path#anchor"
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
			return fmt.Sprintf("names an anchor in no file: %v", err)
		}
		lines = strings.Split(string(b), "\n")
		files[path] = lines
	}
	return anchorProblem(path, lines, anchor)
}

// countsMemo holds goDecls and mdHeadings of each file read, by kind, path
// and line count: every row's anchors in one file share them.
var countsMemo sync.Map

func memoCounts(kind, path string, lines []string, f func([]string) map[string]int) map[string]int {
	key := fmt.Sprintf("%s|%s|%d", kind, path, len(lines))
	if v, ok := countsMemo.Load(key); ok {
		return v.(map[string]int)
	}
	m := f(lines)
	countsMemo.Store(key, m)
	return m
}

// anchorProblem checks that anchor names one place in path: a Go
// declaration, a markdown heading, or else text on one line.
func anchorProblem(path string, lines []string, anchor string) string {
	switch {
	case strings.HasSuffix(path, ".go"):
		if n := memoCounts("go", path, lines, goDecls)[anchor]; n == 1 {
			return ""
		} else if n > 1 {
			return fmt.Sprintf("names %d declarations of %s: write Type.Method", n, path)
		}
	case strings.HasSuffix(path, ".md"):
		if n := memoCounts("md", path, lines, mdHeadings)[anchor]; n == 1 {
			return ""
		} else if n > 1 {
			return fmt.Sprintf("names %d headings of %s: name the section above it, or text on one line", n, path)
		}
	}
	n := 0
	for _, l := range lines {
		if strings.Contains(l, anchor) {
			n++
		}
	}
	switch n {
	case 1:
		return ""
	case 0:
		return "names no Go declaration, heading or text of " + path
	}
	return fmt.Sprintf("matches %d lines of %s: quote more of the line, or name its declaration or heading", n, path)
}

var (
	goFuncDecl  = regexp.MustCompile(`^func (?:\((?:\w+ )?\*?(\w+)(?:\[[^\]]*\])?\) )?(\w+)`)
	goNameDecl  = regexp.MustCompile(`^(?:type|var|const) (\w+)`)
	goNames     = regexp.MustCompile(`^(?:var|const) ((?:\w+, )*\w+)\b`)
	goBlock     = regexp.MustCompile(`^(?:var|const|type) \($`)
	goBlockName = regexp.MustCompile(`^\t((?:\w+, )*\w+)\b`)
	mdHeading   = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*#*\s*$`)
)

// goDecls counts the top-level declarations of a Go file by name: Func,
// Type.Method, Type, and each var and const name, also in blocks.
func goDecls(lines []string) map[string]int {
	out := map[string]int{}
	block := false
	for _, l := range lines {
		if block {
			if strings.HasPrefix(l, ")") {
				block = false
			} else if m := goBlockName.FindStringSubmatch(l); m != nil {
				for _, n := range strings.Split(m[1], ", ") {
					out[n]++
				}
			}
			continue
		}
		switch {
		case goBlock.MatchString(l):
			block = true
		case goFuncDecl.MatchString(l):
			m := goFuncDecl.FindStringSubmatch(l)
			if m[1] != "" {
				out[m[1]+"."+m[2]]++
			} else {
				out[m[2]]++
			}
		case goNames.MatchString(l):
			for _, n := range strings.Split(goNames.FindStringSubmatch(l)[1], ", ") {
				out[n]++
			}
		case goNameDecl.MatchString(l):
			out[goNameDecl.FindStringSubmatch(l)[1]]++
		}
	}
	return out
}

// mdHeadings counts a markdown file's headings by text, outside code fences.
func mdHeadings(lines []string) map[string]int {
	out := map[string]int{}
	fence := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
			continue
		}
		if m := mdHeading.FindStringSubmatch(l); m != nil && !fence {
			out[m[1]]++
		}
	}
	return out
}

// changelogPath is cited by entry title or heading: its unreleased entries
// are the files of docs/changelog.d until a release writes them in.
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
		text, err := fragments.Assembled(root)
		if err != nil {
			return fmt.Sprintf("names an entry of no changelog: %v", err)
		}
		lines = strings.Split(text, "\n")
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
