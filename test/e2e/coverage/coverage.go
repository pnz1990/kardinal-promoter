// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package coverage reads test/e2e/coverage.tsv, the documented behaviors of
// kardinal-promoter, and the tests that claim them: a test claims rows with
// one sentence in its doc comment, "Covers ID, ID." (it may wrap).
//
// test/hack's TestE2ECoverage checks the two agree; test/e2e/proof checks
// that every claimed row's tests passed in a CI run.
package coverage

import (
	"encoding/csv"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// File is coverage.tsv's path from the repository root.
const File = "test/e2e/coverage.tsv"

// LiveDir holds the live e2e tests; only they can cover a live row.
const LiveDir = "test/e2e/live"

// Row is one documented behavior.
type Row struct {
	ID string
	// Tier is live (a live e2e test must cover it), contract (a test against
	// a recorded API: Bitbucket, Azure DevOps) or deprecated (a live test of
	// the deprecation behavior).
	Tier string
	// Suite is the hack/e2e/up.sh suite that runs the row's tests, or unit
	// for a contract row.
	Suite string
	// Status is covered when a test claims the row, known-bug when the test
	// that claims it reproduces an open bug (scale.KnownBug: an expected
	// failure until the bug is fixed), else todo.
	Status  string
	Area    string
	Feature string
	// Source is where the behavior is documented and implemented: refs
	// separated by "; ", each a repo path, path:N or path:N-M, or docs/changelog.md#<entry title or heading>[@<release>].
	Source string
}

// Header is coverage.tsv's first line.
var Header = []string{"id", "tier", "suite", "status", "area", "feature", "source"}

// Rows reads coverage.tsv.
func Rows(root string) ([]Row, error) {
	f, err := os.Open(filepath.Join(root, File))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	r := csv.NewReader(f)
	r.Comma = '\t'
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", File, err)
	}
	if len(records) == 0 || strings.Join(records[0], "\t") != strings.Join(Header, "\t") {
		return nil, fmt.Errorf("%s: header must be %q", File, strings.Join(Header, "\t"))
	}
	rows := make([]Row, 0, len(records)-1)
	for _, rec := range records[1:] {
		rows = append(rows, Row{rec[0], rec[1], rec[2], rec[3], rec[4], rec[5], rec[6]})
	}
	return rows, nil
}

// Test is a top-level Test function and the rows its doc comment claims.
type Test struct {
	Name string
	// File is the path from the repository root.
	File string
	Live bool
	IDs  []string
}

var (
	// ID is the form of a row id.
	ID             = regexp.MustCompile(`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)+$`)
	coversSentence = regexp.MustCompile(`\bCovers ([A-Z][A-Z0-9]*-[A-Z0-9-]+(?:,?\s+(?:and\s+)?[A-Z][A-Z0-9]*-[A-Z0-9-]+)*)\.`)
)

// Tests parses every _test.go file under root, ignoring build tags, and
// returns the Test functions that claim rows, plus every test in LiveDir.
func Tests(root string) ([]Test, error) {
	live := filepath.Join(root, LiveDir)
	var out []Test
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
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		isLive := filepath.Dir(path) == live
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			t := Test{Name: fn.Name.Name, File: filepath.ToSlash(rel), Live: isLive, IDs: claims(fn.Doc.Text())}
			if isLive || len(t.IDs) > 0 {
				out = append(out, t)
			}
		}
		return nil
	})
	return out, err
}

// claims returns the row ids of a doc comment's "Covers ..." sentences.
func claims(doc string) []string {
	var ids []string
	for _, m := range coversSentence.FindAllStringSubmatch(doc, -1) {
		for _, id := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
			if id != "and" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// suiteRun finds a suite's RUN on the line of its case label or the next one.
// The first [^\n]*? is lazy so a RUN on the label's line wins over the next
// suite's RUN on the line below.
var suiteRun = regexp.MustCompile(`(?m)^\s+([a-z0-9-]+)\)[^\n]*?(?:\n[^\n]*?)?RUN='([^']+)'`)

// SuiteRuns returns each suite's go test -run pattern from hack/e2e/up.sh.
func SuiteRuns(root string) (map[string]*regexp.Regexp, error) {
	data, err := os.ReadFile(filepath.Join(root, "hack/e2e/up.sh"))
	if err != nil {
		return nil, err
	}
	runs := map[string]*regexp.Regexp{}
	for _, m := range suiteRun.FindAllStringSubmatch(string(data), -1) {
		re, err := regexp.Compile(m[2])
		if err != nil {
			return nil, fmt.Errorf("hack/e2e/up.sh: suite %s: %w", m[1], err)
		}
		runs[m[1]] = re
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("hack/e2e/up.sh: no suite sets RUN=")
	}
	return runs, nil
}
