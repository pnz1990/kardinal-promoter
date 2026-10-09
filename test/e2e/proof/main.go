// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Command proof checks a CI run against test/e2e/coverage.tsv: every row a
// live test covers must have passed in the suites that ran. It reads the
// summary.json test/e2e/report wrote for each suite run and prints one line
// per row: passed, failed, missing (the test should have run and did not),
// not run (none of its suites ran here), known bug (its test skipped with
// "KNOWN BUG #n", scale.KnownBug: it reproduces an open bug), unit (a
// contract row; ci.yml runs its unit tests) or todo. It exits 1 when a row
// failed or is missing, and with -complete also when a row is todo, not run
// or a known bug.
//
//	go run ./test/e2e/proof [-complete] [-out proof.json] results/*/summary.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/coverage"
)

// summary is test/e2e/report's -out file.
type summary struct {
	Suite   string `json:"suite"`
	Results []struct {
		Test     string `json:"test"`
		Action   string `json:"action"`
		KnownBug int    `json:"knownBug,omitempty"`
	} `json:"results"`
}

// tally is one test's results across every suite run.
type tally struct {
	Name   string `json:"name"`
	Passed int    `json:"passed"`
	Failed int    `json:"failed"`
	// KnownBugs counts skips as known bugs.
	KnownBugs int `json:"knownBugs,omitempty"`
	// Expected is set when a suite that ran selects the test.
	Expected bool `json:"expected"`
}

// Row results.
const (
	passed  = "passed"
	failed  = "failed"
	missing = "missing"
	notRun  = "not run"
	known   = "known bug"
	unit    = "unit"
	todo    = "todo"
)

type rowResult struct {
	coverage.Row
	Result string  `json:"result"`
	Tests  []tally `json:"tests"`
}

// prove gives each row its result. runs are the suites' -run patterns;
// summaries are the suite runs of this CI run.
func prove(rows []coverage.Row, tests []coverage.Test, runs map[string]*regexp.Regexp, summaries []summary) []rowResult {
	ran := map[string]bool{}
	passes, fails, bugs := map[string]int{}, map[string]int{}, map[string]int{}
	for _, s := range summaries {
		ran[s.Suite] = true
		for _, r := range s.Results {
			switch r.Action {
			case "pass":
				passes[r.Test]++
			case "skip":
				if r.KnownBug > 0 {
					bugs[r.Test]++
				} else {
					fails[r.Test]++
				}
			default: // fail, skip: a skipped live test proves nothing
				fails[r.Test]++
			}
		}
	}
	byRow := map[string][]coverage.Test{}
	for _, t := range tests {
		for _, id := range t.IDs {
			byRow[id] = append(byRow[id], t)
		}
	}
	out := make([]rowResult, 0, len(rows))
	for _, row := range rows {
		rr := rowResult{Row: row}
		for _, t := range byRow[row.ID] {
			ta := tally{Name: t.Name, Passed: passes[t.Name], Failed: fails[t.Name], KnownBugs: bugs[t.Name]}
			for suite, re := range runs {
				ta.Expected = ta.Expected || (t.Live && ran[suite] && re.MatchString(t.Name))
			}
			rr.Tests = append(rr.Tests, ta)
		}
		rr.Result = result(row, rr.Tests)
		out = append(out, rr)
	}
	return out
}

func result(row coverage.Row, tests []tally) string {
	switch {
	case len(tests) == 0:
		return todo
	case row.Tier == "contract":
		return unit
	}
	res := notRun
	for _, t := range tests {
		switch {
		case !t.Expected:
		case t.Failed > 0:
			return failed
		case t.Passed == 0 && t.KnownBugs > 0:
			if res == notRun {
				res = known
			}
		case t.Passed == 0:
			res = missing
		case res == notRun:
			res = passed
		}
	}
	return res
}

// ok reports whether the run proves every row it should.
func ok(results []rowResult, complete bool) bool {
	for _, r := range results {
		switch r.Result {
		case failed, missing:
			return false
		case todo, notRun, known:
			if complete && r.Tier != "contract" {
				return false
			}
		}
	}
	return true
}

func counts(results []rowResult) (tiers []string, byTier map[string]map[string]int) {
	byTier = map[string]map[string]int{}
	for _, r := range results {
		if byTier[r.Tier] == nil {
			byTier[r.Tier] = map[string]int{}
			tiers = append(tiers, r.Tier)
		}
		byTier[r.Tier][r.Result]++
		byTier[r.Tier]["rows"]++
	}
	sort.Strings(tiers)
	return tiers, byTier
}

// markdown renders the results for GITHUB_STEP_SUMMARY.
func markdown(w io.Writer, results []rowResult, verdict bool) {
	v := "every row with a live test passed"
	if !verdict {
		v = "FAILED"
	}
	_, _ = fmt.Fprintf(w, "### Coverage proof: %s\n\nRows of `%s` and what this run proved.\n\n", v, coverage.File)
	cols := []string{passed, failed, missing, notRun, known, unit, todo}
	_, _ = fmt.Fprintf(w, "| Tier | Rows | %s |\n|---|---|%s\n", strings.Join(cols, " | "), strings.Repeat("---|", len(cols)))
	tiers, byTier := counts(results)
	for _, tier := range tiers {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = fmt.Sprint(byTier[tier][c])
		}
		_, _ = fmt.Fprintf(w, "| %s | %d | %s |\n", tier, byTier[tier]["rows"], strings.Join(cells, " | "))
	}
	_, _ = fmt.Fprint(w, "\n<details><summary>Every row</summary>\n\n| Row | Tier | Suite | Result | Tests |\n|---|---|---|---|---|\n")
	for _, r := range results {
		names := make([]string, len(r.Tests))
		for i, t := range r.Tests {
			names[i] = "`" + t.Name + "`"
		}
		_, _ = fmt.Fprintf(w, "| %s | %s | %s | %s | %s |\n", r.ID, r.Tier, r.Suite, r.Result, strings.Join(names, " "))
	}
	_, _ = fmt.Fprint(w, "\n</details>\n")
}

func main() {
	complete := flag.Bool("complete", false, "also fail when a live or deprecated row is todo or did not run")
	outFile := flag.String("out", "", "write every row's result as JSON to this file")
	flag.Parse()
	if err := run(".", flag.Args(), *complete, *outFile); err != nil {
		fmt.Fprintln(os.Stderr, "proof:", err)
		os.Exit(1)
	}
}

func run(root string, files []string, complete bool, outFile string) error {
	if len(files) == 0 {
		return fmt.Errorf("no summary files; usage: proof [-complete] [-out FILE] SUMMARY_JSON")
	}
	rows, err := coverage.Rows(root)
	if err != nil {
		return err
	}
	tests, err := coverage.Tests(root)
	if err != nil {
		return err
	}
	runs, err := coverage.SuiteRuns(root)
	if err != nil {
		return err
	}
	var summaries []summary
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var s summary
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		if _, known := runs[s.Suite]; !known {
			return fmt.Errorf("%s: suite %q is not in hack/e2e/up.sh", f, s.Suite)
		}
		summaries = append(summaries, s)
	}
	results := prove(rows, tests, runs, summaries)
	verdict := ok(results, complete)

	tiers, byTier := counts(results)
	for _, tier := range tiers {
		fmt.Printf("%s: %d rows, %d passed here, %d not run here, %d known bugs, %d unit, %d todo, %d failed, %d missing\n", tier,
			byTier[tier]["rows"], byTier[tier][passed], byTier[tier][notRun], byTier[tier][known], byTier[tier][unit], byTier[tier][todo],
			byTier[tier][failed], byTier[tier][missing])
	}
	for _, r := range results {
		if r.Result == failed || r.Result == missing {
			fmt.Printf("    %s %s %v\n", strings.ToUpper(r.Result), r.ID, r.Tests)
		}
	}
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		markdown(f, results, verdict)
		if err := f.Close(); err != nil {
			return err
		}
	}
	if outFile != "" {
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(outFile, append(data, '\n'), 0o644); err != nil {
			return err
		}
	}
	if !verdict {
		return fmt.Errorf("a row's tests failed or did not run (complete=%v)", complete)
	}
	return nil
}
