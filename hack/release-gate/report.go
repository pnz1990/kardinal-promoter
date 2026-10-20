// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Totals counts go test results: every test and subtest's last action.
type Totals struct {
	Run, Passed, Failed, Skipped int
	// PackageFailed counts the package-level failures among Failed.
	PackageFailed int
}

func (t *Totals) add(o Totals) {
	t.Run += o.Run
	t.Passed += o.Passed
	t.Failed += o.Failed
	t.Skipped += o.Skipped
	t.PackageFailed += o.PackageFailed
}

// countTests reads a go test -json stream. A test run N times (-count)
// counts N times.
func countTests(r io.Reader) (Totals, error) {
	var t Totals
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var ev struct{ Action, Test string }
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Test == "" {
			// A package that fails outside a test (a TestMain that exits
			// 0 after failures still records it, a build error, a panic)
			// counts as a failure.
			if ev.Action == "fail" {
				t.Failed++
				t.PackageFailed++
			}
			continue
		}
		switch ev.Action {
		case "pass":
			t.Passed++
		case "fail":
			t.Failed++
		case "skip":
			t.Skipped++
		default:
			continue
		}
		t.Run++
	}
	if err := sc.Err(); err != nil {
		return t, fmt.Errorf("read go test output: %w", err)
	}
	return t, nil
}

func stepTotals(dir string, st Step) (Totals, bool) {
	if st.Tests == "" {
		return Totals{}, false
	}
	f, err := os.Open(filepath.Join(dir, st.ID, st.Tests))
	if err != nil {
		return Totals{}, false
	}
	defer func() { _ = f.Close() }()
	t, err := countTests(f)
	return t, err == nil
}

// Coverage is test/e2e/coverage.tsv's rows by status, and the coverage
// proof's rows by result when the e2e steps ran.
type Coverage struct {
	Rows     int
	ByStatus map[string]int
	ByResult map[string]int // test/e2e/proof -out, empty without e2e results
	// Unproven are the rows whose result is failed, missing or not run.
	Unproven map[string]string
	// Allowed are rows that may stay unproven, each with its reason
	// (hack/release-gate/coverage-allow.txt).
	Allowed map[string]string
	// ProofError, when set, is why there is no proof: the gate is NO-GO.
	ProofError string
}

// unprovenResults are the proof results that do not prove a row.
var unprovenResults = map[string]bool{"failed": true, "missing": true, "not run": true}

// Blocking are the unproven rows not in the allowlist, sorted.
func (c Coverage) Blocking() []string {
	var out []string
	for id := range c.Unproven {
		if _, ok := c.Allowed[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// readAllowlist reads "ID reason" lines; # starts a comment. A row without
// a reason is an error.
func readAllowlist(path string) (map[string]string, error) {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the coverage allowlist: %w", err)
	}
	for _, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		id, reason, _ := strings.Cut(l, " ")
		if strings.TrimSpace(reason) == "" {
			return nil, fmt.Errorf("%s: %s has no reason", path, id)
		}
		out[id] = strings.TrimSpace(reason)
	}
	return out, nil
}

func readCoverage(root, proofFile string) (Coverage, error) {
	c := Coverage{ByStatus: map[string]int{}, ByResult: map[string]int{}, Unproven: map[string]string{}}
	f, err := os.Open(filepath.Join(root, "test", "e2e", "coverage.tsv"))
	if err != nil {
		return c, fmt.Errorf("open coverage.tsv: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for first := true; sc.Scan(); first = false {
		cols := strings.Split(sc.Text(), "\t")
		if first || len(cols) < 4 {
			continue
		}
		c.Rows++
		c.ByStatus[cols[3]]++
	}
	if err := sc.Err(); err != nil {
		return c, fmt.Errorf("read coverage.tsv: %w", err)
	}
	if data, err := os.ReadFile(proofFile); err == nil {
		var rows []struct {
			ID     string `json:"ID"`
			Result string `json:"result"`
		}
		if err := json.Unmarshal(data, &rows); err != nil {
			return c, fmt.Errorf("read %s: %w", proofFile, err)
		}
		for _, r := range rows {
			c.ByResult[r.Result]++
			if unprovenResults[r.Result] {
				c.Unproven[r.ID] = r.Result
			}
		}
	}
	return c, nil
}

// Provenance is what the evidence was gathered on.
type Provenance struct {
	SHA, Describe, PrevRelease string
	Dirty                      bool
	GoVersion                  string
	Tools                      [][2]string // name, version or image
	Images                     []string    // the images step's images.txt: ref, image ID, build
	// RanImages are the images the live suites ran: each local suite's
	// images.txt, and the CI run's e2e-images artifact.
	RanImages []string
}

// ranTotals counts the tests of a step that ran on this commit with these
// inputs: output left by another run does not count.
func ranTotals(out string, st Step, r Result) (Totals, bool) {
	if r.Status != statusPass && r.Status != statusFail {
		return Totals{}, false
	}
	return stepTotals(out, st)
}

// skippedTests lists the tests a go test -json file records as skipped.
func skippedTests(file string) []string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var ev struct{ Action, Test, Package string }
		if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Action == "skip" && ev.Test != "" {
			out = append(out, strings.TrimPrefix(ev.Package, "github.com/kardinal-promoter/kardinal-promoter/")+" "+ev.Test)
		}
	}
	return out
}

// readRanImages reads the images.txt of every step that ran and the CI
// run's e2e-images artifact.
func readRanImages(out string, steps []Step, results []Result) []string {
	var lines []string
	add := func(file string) {
		data, err := os.ReadFile(file)
		if err != nil {
			return
		}
		for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if l != "" {
				lines = append(lines, l)
			}
		}
	}
	for i, st := range steps {
		if st.ID != "images" && (results[i].Status == statusPass || results[i].Status == statusFail) {
			add(filepath.Join(out, st.ID, "images.txt"))
		}
	}
	add(filepath.Join(out, "ci-images", "images.txt"))
	sort.Strings(lines)
	return lines
}

func readImages(out string) []string {
	data, err := os.ReadFile(filepath.Join(out, "images", "images.txt"))
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// verdictOf is the gate's verdict: the steps', made NO-GO by a coverage row
// that is not proven (and not allowed) or by a missing proof.
func verdictOf(results []Result, cov Coverage) string {
	v := verdict(results)
	if cov.ProofError != "" || len(cov.Blocking()) > 0 {
		return "NO-GO"
	}
	return v
}

// verdict of the steps alone.
func verdict(results []Result) string {
	incomplete := false
	for _, r := range results {
		switch r.Status {
		case statusFail, statusMissing:
			return "NO-GO"
		case statusNotRun:
			incomplete = true
		}
	}
	if incomplete {
		return "INCOMPLETE"
	}
	return "GO"
}

func duration(s float64) string {
	d := time.Duration(s * float64(time.Second)).Round(time.Second)
	if d == 0 {
		return "-"
	}
	return d.String()
}

// Render writes the report: verdict, per-area table, totals, coverage,
// what the gate does not run, durations and provenance. Links are relative
// to the report's directory.
func Render(w io.Writer, steps []Step, results []Result, out string, cov Coverage, notRun []NotRunItem, p Provenance, wall time.Duration) error {
	var b strings.Builder
	short := p.SHA
	if len(short) > 12 {
		short = short[:12]
	}
	fmt.Fprintf(&b, "# Release gate: %s\n\n", short)
	v := verdictOf(results, cov)
	fmt.Fprintf(&b, "**Verdict: %s.** `%s` (%s)", v, p.SHA, p.Describe)
	if p.Dirty {
		b.WriteString(", **with uncommitted changes**")
	}
	fmt.Fprintf(&b, ". Generated %s by `make release-gate`.", time.Now().UTC().Format(time.RFC3339))
	if len(notRun) > 0 {
		fmt.Fprintf(&b, " It does not prove the %d items [not run by this gate](#not-run-by-this-gate).", len(notRun))
	}
	b.WriteString("\n\n")

	var all Totals
	byArea := map[string][]int{}
	for i, st := range steps {
		byArea[st.Area] = append(byArea[st.Area], i)
	}
	b.WriteString("## Areas\n\n| Area | Result | Steps passed | Tests run | Passed | Failed | Skipped | Time |\n|---|---|---|---|---|---|---|---|\n")
	for _, a := range areas {
		idx := byArea[a]
		if len(idx) == 0 {
			continue
		}
		var t Totals
		passed := 0
		status := statusPass
		var first, last time.Time
		for _, i := range idx {
			if tt, ok := ranTotals(out, steps[i], results[i]); ok {
				t.add(tt)
			}
			if r := results[i]; !r.Started.IsZero() {
				end := r.Started.Add(time.Duration(r.Seconds * float64(time.Second)))
				if first.IsZero() || r.Started.Before(first) {
					first = r.Started
				}
				if end.After(last) {
					last = end
				}
			}
			switch results[i].Status {
			case statusPass:
				passed++
			case statusFail, statusMissing:
				status = statusFail
			case statusNotRun:
				if status == statusPass {
					status = statusNotRun
				}
			}
		}
		all.add(t)
		// The area's span, first start to last end: its steps overlap (the
		// CI jobs run at once), so their sum would overstate it.
		fmt.Fprintf(&b, "| %s | %s | %d of %d | %d | %d | %d | %d | %s |\n", a, strings.ToUpper(status), passed, len(idx),
			t.Run, t.Passed, t.Failed, t.Skipped, duration(last.Sub(first).Seconds()))
	}
	fmt.Fprintf(&b, "| **Total** | **%s** | | **%d** | **%d** | **%d** | **%d** | %s wall |\n\n",
		v, all.Run, all.Passed, all.Failed, all.Skipped, wall.Round(time.Second))
	b.WriteString("Tests count every test and subtest once per run (`-count`), from each step's `go test -json` output. " +
		"An area's time runs from its first step's start to its last step's end.\n\n")

	b.WriteString("## Steps\n\n| Step | Area | Result | Tests (pass/fail/skip) | Time | Log |\n|---|---|---|---|---|---|\n")
	for i, st := range steps {
		r := results[i]
		res := strings.ToUpper(r.Status)
		if r.Cached {
			res += " (cached)"
		}
		if r.Dirty {
			res += " (uncommitted changes)"
		}
		if r.Reason != "" && r.Status != statusPass {
			res += ": " + strings.ReplaceAll(r.Reason, "|", "/")
		}
		tests := ""
		if t, ok := ranTotals(out, st, r); ok {
			tests = fmt.Sprintf("%d/%d/%d", t.Passed, t.Failed, t.Skipped)
		}
		logLink := ""
		if r.Status != statusPass && r.Status != statusFail {
			// No output of this commit and these inputs to link.
		} else if job, err := os.ReadFile(filepath.Join(out, st.ID, "ci-job.txt")); err == nil {
			logLink = fmt.Sprintf("[CI job](%s)", strings.TrimSpace(string(job)))
		} else if _, err := os.Stat(filepath.Join(out, st.ID, "log.txt")); err == nil {
			logLink = fmt.Sprintf("[log](%s/log.txt)", st.ID)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", st.Title, st.Area, res, tests, duration(r.Seconds), logLink)
	}

	var skips []string
	for i, st := range steps {
		if _, ok := ranTotals(out, st, results[i]); !ok {
			continue
		}
		for _, name := range skippedTests(filepath.Join(out, st.ID, st.Tests)) {
			skips = append(skips, fmt.Sprintf("- %s: `%s`", st.ID, name))
		}
	}
	if len(skips) > 0 {
		b.WriteString("\n## Skipped tests\n\n" + strings.Join(skips, "\n") + "\n")
	}

	b.WriteString("\n## Coverage\n\n")
	statuses := make([]string, 0, len(cov.ByStatus))
	for s := range cov.ByStatus {
		statuses = append(statuses, s)
	}
	sort.Strings(statuses)
	var parts []string
	for _, s := range statuses {
		parts = append(parts, fmt.Sprintf("%d %s", cov.ByStatus[s], s))
	}
	fmt.Fprintf(&b, "`test/e2e/coverage.tsv`: %d rows, %d covered and %d not (%s).\n", cov.Rows, cov.ByStatus["covered"],
		cov.Rows-cov.ByStatus["covered"], strings.Join(parts, ", "))
	if len(cov.ByResult) > 0 {
		var res []string
		keys := make([]string, 0, len(cov.ByResult))
		for k := range cov.ByResult {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			res = append(res, fmt.Sprintf("%d %s", cov.ByResult[k], k))
		}
		fmt.Fprintf(&b, "The coverage proof over this gate's live results ([coverage-proof.json](coverage-proof.json)): %s.\n", strings.Join(res, ", "))
	}
	if cov.ProofError != "" {
		fmt.Fprintf(&b, "\n**No coverage proof: %s.** No row is proven, so the gate is NO-GO.\n", cov.ProofError)
	}
	if blocking := cov.Blocking(); len(blocking) > 0 {
		fmt.Fprintf(&b, "\n**%d covered rows are not proven** (NO-GO): ", len(blocking))
		shown := blocking
		if len(shown) > 60 {
			shown = shown[:60]
		}
		var items []string
		for _, id := range shown {
			items = append(items, fmt.Sprintf("%s (%s)", id, cov.Unproven[id]))
		}
		b.WriteString(strings.Join(items, ", "))
		if len(blocking) > len(shown) {
			fmt.Fprintf(&b, " and %d more (coverage-proof.json)", len(blocking)-len(shown))
		}
		b.WriteString(".\n")
	}
	var allowed []string
	for id := range cov.Unproven {
		if reason, ok := cov.Allowed[id]; ok {
			allowed = append(allowed, fmt.Sprintf("- %s (%s): %s", id, cov.Unproven[id], reason))
		}
	}
	if len(allowed) > 0 {
		sort.Strings(allowed)
		b.WriteString("\nNot proven, allowed by hack/release-gate/coverage-allow.txt:\n\n" + strings.Join(allowed, "\n") + "\n")
	}

	renderNotRun(&b, notRun)

	b.WriteString("\n## Provenance\n\n")
	fmt.Fprintf(&b, "- Commit: `%s` (%s)\n", p.SHA, p.Describe)
	if p.PrevRelease != "" {
		fmt.Fprintf(&b, "- Previous release: %s\n", p.PrevRelease)
	}
	fmt.Fprintf(&b, "- Go: %s\n", p.GoVersion)
	for _, t := range p.Tools {
		fmt.Fprintf(&b, "- %s: `%s`\n", t[0], t[1])
	}
	if len(p.RanImages) == 0 {
		b.WriteString("- Images the live suites ran: none recorded\n")
	}
	for _, img := range p.RanImages {
		fmt.Fprintf(&b, "- Image the live suites ran: `%s`\n", img)
	}
	if len(p.Images) == 0 {
		b.WriteString("- Images trivy scanned: none (the images step did not pass)\n")
	}
	for _, img := range p.Images {
		fmt.Fprintf(&b, "- Image trivy scanned: `%s`\n", img)
	}
	_, err := io.WriteString(w, b.String())
	if err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}
