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
type Totals struct{ Run, Passed, Failed, Skipped int }

func (t *Totals) add(o Totals) {
	t.Run += o.Run
	t.Passed += o.Passed
	t.Failed += o.Failed
	t.Skipped += o.Skipped
}

// countTests reads a go test -json stream. A test run N times (-count)
// counts N times.
func countTests(r io.Reader) (Totals, error) {
	var t Totals
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var ev struct{ Action, Test string }
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Test == "" {
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
}

func readCoverage(root, proofFile string) (Coverage, error) {
	c := Coverage{ByStatus: map[string]int{}, ByResult: map[string]int{}}
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
			Result string `json:"result"`
		}
		if json.Unmarshal(data, &rows) == nil {
			for _, r := range rows {
				c.ByResult[r.Result]++
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
	Images                     []string    // images.txt lines: ref, image ID, build
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

// Verdict of the whole gate.
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
// durations and provenance. Links are relative to the report's directory.
func Render(w io.Writer, steps []Step, results []Result, out string, cov Coverage, p Provenance, wall time.Duration) error {
	var b strings.Builder
	short := p.SHA
	if len(short) > 12 {
		short = short[:12]
	}
	fmt.Fprintf(&b, "# Release gate: %s\n\n", short)
	fmt.Fprintf(&b, "**Verdict: %s.** `%s` (%s)", verdict(results), p.SHA, p.Describe)
	if p.Dirty {
		b.WriteString(", **with uncommitted changes**")
	}
	fmt.Fprintf(&b, ". Generated %s by `make release-gate`.\n\n", time.Now().UTC().Format(time.RFC3339))

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
		passed, secs := 0, 0.0
		status := statusPass
		for _, i := range idx {
			if tt, ok := stepTotals(out, steps[i]); ok {
				t.add(tt)
			}
			secs += results[i].Seconds
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
		fmt.Fprintf(&b, "| %s | %s | %d of %d | %d | %d | %d | %d | %s |\n", a, strings.ToUpper(status), passed, len(idx),
			t.Run, t.Passed, t.Failed, t.Skipped, duration(secs))
	}
	fmt.Fprintf(&b, "| **Total** | **%s** | | **%d** | **%d** | **%d** | **%d** | %s wall |\n\n",
		verdict(results), all.Run, all.Passed, all.Failed, all.Skipped, wall.Round(time.Second))
	b.WriteString("Tests count every test and subtest once per run (`-count`), from each step's `go test -json` output.\n\n")

	b.WriteString("## Steps\n\n| Step | Area | Result | Tests (pass/fail/skip) | Time | Log |\n|---|---|---|---|---|---|\n")
	for i, st := range steps {
		r := results[i]
		res := strings.ToUpper(r.Status)
		if r.Cached {
			res += " (cached)"
		}
		if r.Reason != "" && r.Status != statusPass {
			res += ": " + strings.ReplaceAll(r.Reason, "|", "/")
		}
		tests := ""
		if t, ok := stepTotals(out, st); ok {
			tests = fmt.Sprintf("%d/%d/%d", t.Passed, t.Failed, t.Skipped)
		}
		logLink := ""
		if _, err := os.Stat(filepath.Join(out, st.ID, "log.txt")); err == nil {
			logLink = fmt.Sprintf("[log](%s/log.txt)", st.ID)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", st.Title, st.Area, res, tests, duration(r.Seconds), logLink)
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
	} else {
		b.WriteString("No live e2e results yet, so no coverage proof.\n")
	}

	b.WriteString("\n## Provenance\n\n")
	fmt.Fprintf(&b, "- Commit: `%s` (%s)\n", p.SHA, p.Describe)
	if p.PrevRelease != "" {
		fmt.Fprintf(&b, "- Previous release: %s\n", p.PrevRelease)
	}
	fmt.Fprintf(&b, "- Go: %s\n", p.GoVersion)
	for _, t := range p.Tools {
		fmt.Fprintf(&b, "- %s: `%s`\n", t[0], t[1])
	}
	if len(p.Images) == 0 {
		b.WriteString("- Images: not built (the images step did not pass)\n")
	}
	for _, img := range p.Images {
		fmt.Fprintf(&b, "- Image: `%s`\n", img)
	}
	_, err := io.WriteString(w, b.String())
	if err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}
