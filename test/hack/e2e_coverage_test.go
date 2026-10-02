// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"bytes"
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
		assert.Contains(t, []string{"covered", "todo"}, r.Status, r.ID)
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

// rollbackRef is a source ref that names lines: path:N or path:N-M.
var rollbackRef = regexp.MustCompile(`^([^:\s]+):([0-9]+)(?:-([0-9]+))?$`)

// TestE2ECoverage_RollbackRefsNameLines: every source ref of a covered
// rollback row names the lines that document or implement the row, and those
// lines exist, so a reader of the row finds them without reading the file.
func TestE2ECoverage_RollbackRefsNameLines(t *testing.T) {
	root := repoRoot(t)
	rows, err := coverage.Rows(root)
	require.NoError(t, err)
	lineCount := map[string]int{}
	for _, r := range rows {
		if r.Area != "rollback" || r.Status != "covered" {
			continue
		}
		for _, ref := range strings.Split(r.Source, "; ") {
			m := rollbackRef.FindStringSubmatch(ref)
			if !assert.NotNil(t, m, "%s: ref %q names no lines; write path:N or path:N-M", r.ID, ref) {
				continue
			}
			n, ok := lineCount[m[1]]
			if !ok {
				b, err := os.ReadFile(filepath.Join(root, m[1]))
				if !assert.NoError(t, err, "%s: ref %q", r.ID, ref) {
					continue
				}
				n = bytes.Count(b, []byte("\n"))
				lineCount[m[1]] = n
			}
			first, _ := strconv.Atoi(m[2])
			last := first
			if m[3] != "" {
				last, _ = strconv.Atoi(m[3])
			}
			assert.True(t, first >= 1 && first <= last && last <= n,
				"%s: ref %q is outside %s, which has %d lines", r.ID, ref, m[1], n)
		}
	}
}
