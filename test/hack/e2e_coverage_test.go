// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"encoding/csv"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coverageRow is one row of test/e2e/coverage.tsv: a documented behavior and
// whether a test covers it.
type coverageRow struct {
	ID, Tier, Suite, Status, Area, Feature, Source string
}

var coverageHeader = []string{"id", "tier", "suite", "status", "area", "feature", "source"}

func coverageRows(t *testing.T) []coverageRow {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRoot(t), "test/e2e/coverage.tsv"))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	r := csv.NewReader(f)
	r.Comma = '\t'
	r.LazyQuotes = true
	records, err := r.ReadAll()
	require.NoError(t, err)
	require.NotEmpty(t, records)
	require.Equal(t, coverageHeader, records[0], "coverage.tsv header")
	var rows []coverageRow
	for _, rec := range records[1:] {
		rows = append(rows, coverageRow{rec[0], rec[1], rec[2], rec[3], rec[4], rec[5], rec[6]})
	}
	return rows
}

// coversSentence matches the "Covers A-01, B-02." sentence of a test's doc
// comment; it may wrap across lines.
var (
	coverageID     = regexp.MustCompile(`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)+$`)
	coversSentence = regexp.MustCompile(`\bCovers ([A-Z][A-Z0-9]*-[A-Z0-9-]+(?:,?\s+(?:and\s+)?[A-Z][A-Z0-9]*-[A-Z0-9-]+)*)\.`)
)

// coveringTest is a Test function whose doc comment names coverage rows.
type coveringTest struct {
	Name, File string
	Live       bool
	IDs        []string
}

// coveringTests parses every _test.go file in the repository (build tags
// ignored) and returns its top-level Test functions with the ids their doc
// comments cover. Tests in test/e2e/live are returned even without ids.
func coveringTests(t *testing.T) []coveringTest {
	t.Helper()
	root := repoRoot(t)
	live := filepath.Join(root, "test", "e2e", "live")
	var out []coveringTest
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "bin", "site":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		isLive := filepath.Dir(path) == live
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			ct := coveringTest{Name: fn.Name.Name, File: rel, Live: isLive}
			if fn.Doc != nil {
				for _, m := range coversSentence.FindAllStringSubmatch(fn.Doc.Text(), -1) {
					for _, id := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
						if id != "and" {
							ct.IDs = append(ct.IDs, id)
						}
					}
				}
			}
			if isLive || len(ct.IDs) > 0 {
				out = append(out, ct)
			}
		}
		return nil
	})
	require.NoError(t, err)
	return out
}

// suiteRuns returns the go test -run pattern of each suite in hack/e2e/up.sh.
func suiteRuns(t *testing.T) map[string]*regexp.Regexp {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "hack/e2e/up.sh"))
	require.NoError(t, err)
	runs := map[string]*regexp.Regexp{}
	for _, m := range regexp.MustCompile(`(?m)^\s+([a-z0-9-]+)\)[^\n]*\n?[^\n]*?RUN='([^']+)'`).FindAllStringSubmatch(string(data), -1) {
		runs[m[1]] = regexp.MustCompile(m[2])
	}
	require.NotEmpty(t, runs, "no suites with RUN= in hack/e2e/up.sh")
	return runs
}

// TestE2ECoverage keeps test/e2e/coverage.tsv honest: a row is "covered" if
// and only if a test's doc comment names it, live rows are covered only by
// live tests, and every live test names its rows and runs in some suite.
func TestE2ECoverage(t *testing.T) {
	rows := coverageRows(t)
	byID := map[string]coverageRow{}
	for _, r := range rows {
		assert.Regexp(t, coverageID, r.ID)
		assert.NotContains(t, byID, r.ID, "duplicate row")
		byID[r.ID] = r
		assert.Contains(t, []string{"live", "contract", "deprecated"}, r.Tier, r.ID)
		assert.Contains(t, []string{"covered", "todo"}, r.Status, r.ID)
		assert.NotEmpty(t, r.Suite, r.ID)
		assert.NotEmpty(t, r.Feature, r.ID)
		assert.NotEmpty(t, r.Source, r.ID)
	}

	tests := coveringTests(t)
	runs := suiteRuns(t)
	coveredBy := map[string][]string{}
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
			if !assert.True(t, ok, "%s covers %s, which is not in test/e2e/coverage.tsv", name, id) {
				continue
			}
			if row.Tier != "contract" {
				assert.True(t, ct.Live, "%s covers %s row %s; only a test in test/e2e/live can", name, row.Tier, id)
			}
			coveredBy[id] = append(coveredBy[id], name)
		}
	}

	counts := map[string][2]int{}
	for _, r := range rows {
		_, has := coveredBy[r.ID]
		if r.Status == "covered" {
			assert.True(t, has, "%s is marked covered but no test covers it", r.ID)
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
