// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Command report reads `go test -json` from stdin, prints the test output as
// it arrives, and ends with a summary. It exits 1 when a test failed, when a
// test skipped (a skipped live test proves nothing), or when no test ran.
// With GITHUB_STEP_SUMMARY set it also writes the summary there as Markdown.
//
//	go test -tags e2e -json ./test/e2e/live ... | go run ./test/e2e/report -suite core
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// event is one line of `go test -json` (cmd/test2json).
type event struct {
	Action  string
	Package string
	Test    string
	Output  string
	Elapsed float64
}

// result is a test's last pass, fail or skip. A test run with -count=N has N
// results.
type result struct {
	Test    string
	Action  string
	Elapsed float64
}

type summary struct {
	results []result
	// pkgFailed is set when a package failed outside any test (a build error,
	// a panic in TestMain, a timeout).
	pkgFailed bool
}

func (s *summary) count(action string) int {
	n := 0
	for _, r := range s.results {
		if r.Action == action {
			n++
		}
	}
	return n
}

// ok reports whether the run proves anything: tests ran and all passed.
func (s *summary) ok() bool {
	return !s.pkgFailed && s.count("pass") > 0 && s.count("fail") == 0 && s.count("skip") == 0
}

// read copies test output to out and collects results. Lines that are not
// JSON (build errors go test prints before any event) are copied as-is.
func read(in io.Reader, out io.Writer) (*summary, error) {
	s := &summary{}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		var ev event
		if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &ev) != nil {
			_, _ = fmt.Fprintln(out, string(line))
			continue
		}
		switch ev.Action {
		case "output":
			_, _ = fmt.Fprint(out, ev.Output)
		case "pass", "fail", "skip":
			if ev.Test == "" {
				if ev.Action == "fail" {
					s.pkgFailed = true
				}
				continue
			}
			s.results = append(s.results, result{Test: ev.Test, Action: ev.Action, Elapsed: ev.Elapsed})
		}
	}
	return s, sc.Err()
}

// markdown renders the summary for GITHUB_STEP_SUMMARY.
func (s *summary) markdown(suite string) string {
	var b strings.Builder
	verdict := "passed"
	if !s.ok() {
		verdict = "FAILED"
	}
	fmt.Fprintf(&b, "### Live e2e suite `%s`: %s\n\n", suite, verdict)
	fmt.Fprintf(&b, "%d passed, %d failed, %d skipped", s.count("pass"), s.count("fail"), s.count("skip"))
	if s.pkgFailed {
		b.WriteString(", and the test binary failed outside a test")
	}
	b.WriteString("\n\n| Test | Result | Time |\n|---|---|---|\n")
	rs := append([]result(nil), s.results...)
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Test < rs[j].Test })
	for _, r := range rs {
		fmt.Fprintf(&b, "| `%s` | %s | %.0fs |\n", r.Test, r.Action, r.Elapsed)
	}
	return b.String()
}

func main() {
	suite := flag.String("suite", "", "suite name, for the summary")
	flag.Parse()

	s, err := read(os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: read go test output: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n=== suite %s: %d passed, %d failed, %d skipped\n", *suite, s.count("pass"), s.count("fail"), s.count("skip"))
	for _, r := range s.results {
		if r.Action != "pass" {
			fmt.Printf("    %s %s\n", strings.ToUpper(r.Action), r.Test)
		}
	}
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, err = f.WriteString(s.markdown(*suite) + "\n")
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "report: write step summary: %v\n", err)
		}
	}
	switch {
	case s.ok():
		return
	case s.count("skip") > 0:
		fmt.Println("FAIL: a live test skipped; a skipped live test proves nothing (test/e2e/README.md)")
	case s.count("pass") == 0 && s.count("fail") == 0:
		fmt.Println("FAIL: no test ran; check the suite's -run pattern")
	}
	os.Exit(1)
}
