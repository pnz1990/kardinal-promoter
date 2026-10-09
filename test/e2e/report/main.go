// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Command report reads `go test -json` from stdin, prints the test output as
// it arrives, and ends with a summary. It exits 1 when a test failed, when a
// test skipped (a skipped live test proves nothing), or when no test ran.
// A scale test that reproduces an open bug (scale.KnownBug) is an expected
// failure: when it fails it is listed as a known bug (action "xfail") and
// does not fail the run; when it passes, scale.KnownBug fails it with "KNOWN
// BUG #n FIXED", which fails the run like any failure.
// With GITHUB_STEP_SUMMARY set it also writes the summary there as Markdown;
// with -out it writes the results as JSON for test/e2e/proof.
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
	"regexp"
	"sort"
	"strconv"
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
	Test    string  `json:"test"`
	Action  string  `json:"action"`
	Elapsed float64 `json:"elapsed"`
	// KnownBug is the open bug an expected failure ("xfail") reproduces.
	KnownBug int `json:"knownBug,omitempty"`
}

// The lines scale.KnownBug logs, as testing prints a t.Log line (indented
// "file.go:N: "): the bug the test reproduces, and the line its cleanup
// adds when the test passed anyway. Only TestScale_ tests use KnownBug.
var (
	knownBug      = regexp.MustCompile(`^\s+[\w./-]+\.go:[0-9]+: KNOWN BUG #([0-9]+) https://github\.com/pnz1990/kardinal-promoter/issues/[0-9]+: `)
	knownBugFixed = regexp.MustCompile(`^\s+[\w./-]+\.go:[0-9]+: KNOWN BUG #[0-9]+ FIXED`)
)

type summary struct {
	results []result
	// pkgFailed is set when a package failed outside any test (a build error,
	// a panic in TestMain, a timeout).
	pkgFailed bool
}

// file is the -out JSON: test/e2e/proof reads one per suite run.
type file struct {
	Suite     string   `json:"suite"`
	Results   []result `json:"results"`
	PkgFailed bool     `json:"pkgFailed"`
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

// knownBugs counts the expected failures.
func (s *summary) knownBugs() int { return s.count("xfail") }

// ok reports whether the run proves anything: tests ran and all passed.
func (s *summary) ok() bool {
	// go test fails the package when a test fails, an expected failure too.
	pkgOK := !s.pkgFailed || (s.count("xfail") > 0 && s.count("fail") == 0)
	return pkgOK && s.count("pass") > 0 && s.count("fail") == 0 && s.count("skip") == 0
}

// read copies test output to out and collects results. Lines that are not
// JSON (build errors go test prints before any event) are copied as-is.
func read(in io.Reader, out io.Writer) (*summary, error) {
	s := &summary{}
	bugs, fixed := map[string]int{}, map[string]bool{}
	pkgFail := false
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
			if !strings.HasPrefix(ev.Test, "TestScale_") {
				break
			}
			if m := knownBug.FindStringSubmatch(ev.Output); m != nil {
				bugs[ev.Test], _ = strconv.Atoi(m[1])
			}
			if knownBugFixed.MatchString(ev.Output) {
				fixed[ev.Test] = true
			}
		case "pass", "fail", "skip":
			if ev.Test == "" {
				if ev.Action == "fail" {
					pkgFail = true
				}
				continue
			}
			r := result{Test: ev.Test, Action: ev.Action, Elapsed: ev.Elapsed}
			if ev.Action == "fail" && bugs[ev.Test] > 0 && !fixed[ev.Test] {
				r.Action, r.KnownBug = "xfail", bugs[ev.Test]
			}
			delete(bugs, ev.Test)
			delete(fixed, ev.Test)
			s.results = append(s.results, r)
		}
	}
	s.pkgFailed = pkgFail
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
	fmt.Fprintf(&b, "%d passed, %d failed, %d skipped, %d known bugs", s.count("pass"), s.count("fail"), s.count("skip"), s.knownBugs())
	if s.pkgFailed {
		b.WriteString(", and the test binary failed outside a test")
	}
	b.WriteString("\n\n| Test | Result | Time |\n|---|---|---|\n")
	rs := append([]result(nil), s.results...)
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Test < rs[j].Test })
	for _, r := range rs {
		action := r.Action
		if r.KnownBug > 0 {
			action = fmt.Sprintf("known bug [#%d](https://github.com/pnz1990/kardinal-promoter/issues/%d)", r.KnownBug, r.KnownBug)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %.0fs |\n", r.Test, action, r.Elapsed)
	}
	return b.String()
}

func main() {
	suite := flag.String("suite", "", "suite name, for the summary")
	outFile := flag.String("out", "", "write the results as JSON to this file")
	flag.Parse()

	s, err := read(os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "report: read go test output: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n=== suite %s: %d passed, %d failed, %d skipped, %d known bugs\n", *suite, s.count("pass"), s.count("fail"), s.count("skip"), s.knownBugs())
	for _, r := range s.results {
		switch {
		case r.KnownBug > 0:
			fmt.Printf("    KNOWN BUG #%d %s\n", r.KnownBug, r.Test)
		case r.Action != "pass":
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
	if *outFile != "" {
		data, err := json.MarshalIndent(file{Suite: *suite, Results: s.results, PkgFailed: s.pkgFailed}, "", "  ")
		if err == nil {
			err = os.WriteFile(*outFile, append(data, '\n'), 0o644)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "report: write %s: %v\n", *outFile, err)
			os.Exit(1)
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
