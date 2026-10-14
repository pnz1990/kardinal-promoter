// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Command citool holds the logic of the CI workflows that is worth testing:
// splitting the Go packages into balanced test shards (ci.yml), reading go
// test output (red-main.yml, ci-health.yml) and finding flaky tests
// (ci-health.yml). The workflows fetch data with gh; citool only reads files
// and stdin and writes stdout, so every subcommand runs offline.
//
//	go list ./... | citool shard 2/4 hack/ci/test-durations.txt
//	citool durations LOG...          > hack/ci/test-durations.txt
//	citool skew 4 CURRENT NEW         # how unbalanced CURRENT's split is under NEW
//	citool failures < LOG             # package<TAB>test, one per failure
//	citool flaky -repo O/R RECORDS LOGDIR
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const usage = `usage:
  citool shard I/N DURATIONS       packages on stdin; prints shard I's
  citool durations LOG...          prints a durations file from go test output
  citool skew N CURRENT NEW        prints the slowest shard over the ideal
  citool failures                  go test output on stdin; prints package<TAB>test
  citool flaky -repo O/R RECORDS LOGDIR
`

var errUsage = errors.New(strings.TrimSpace(usage))

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "citool:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "shard":
		return cmdShard(rest, stdin, stdout)
	case "durations":
		return cmdDurations(rest, stdout)
	case "skew":
		return cmdSkew(rest, stdout)
	case "failures":
		for _, f := range parseFailures(stdin) {
			if _, err := fmt.Fprintf(stdout, "%s\t%s\n", f.Package, f.Test); err != nil {
				return fmt.Errorf("write: %w", err)
			}
		}
		return nil
	case "flaky":
		return cmdFlaky(rest, stdout)
	default:
		return fmt.Errorf("unknown command %q: %w", cmd, errUsage)
	}
}

// ── shard ────────────────────────────────────────────────────────────────────

func cmdShard(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) != 2 {
		return errUsage
	}
	i, n, err := parseShard(args[0])
	if err != nil {
		return err
	}
	durations, err := readDurations(args[1])
	if err != nil {
		return err
	}
	pkgs, err := readLines(stdin)
	if err != nil {
		return err
	}
	for _, p := range split(pkgs, durations, n)[i-1] {
		if _, err := fmt.Fprintln(stdout, p); err != nil {
			return fmt.Errorf("write: %w", err)
		}
	}
	return nil
}

func parseShard(s string) (int, int, error) {
	a, b, ok := strings.Cut(s, "/")
	i, err1 := strconv.Atoi(a)
	n, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || n < 1 || i < 1 || i > n {
		return 0, 0, fmt.Errorf("shard %q: want I/N with 1 <= I <= N", s)
	}
	return i, n, nil
}

// split deals the packages into n shards, longest first, each to the shard
// with the least work so far (lowest index on a tie), so the slowest shard is
// close to the ideal sum/n. A package not in durations is assumed to take the
// median. The result is the same for the same input in any order.
func split(pkgs []string, durations map[string]float64, n int) [][]string {
	median := medianOf(durations)
	uniq := map[string]bool{}
	var sorted []string
	for _, p := range pkgs {
		if p = strings.TrimSpace(p); p != "" && !uniq[p] {
			uniq[p] = true
			sorted = append(sorted, p)
		}
	}
	cost := func(p string) float64 {
		if d, ok := durations[p]; ok {
			return d
		}
		return median
	}
	sort.Slice(sorted, func(a, b int) bool {
		if ca, cb := cost(sorted[a]), cost(sorted[b]); ca != cb {
			return ca > cb
		}
		return sorted[a] < sorted[b]
	})
	shards := make([][]string, n)
	load := make([]float64, n)
	for _, p := range sorted {
		best := 0
		for s := 1; s < n; s++ {
			if load[s] < load[best] {
				best = s
			}
		}
		shards[best] = append(shards[best], p)
		load[best] += cost(p)
	}
	for _, s := range shards {
		sort.Strings(s)
	}
	return shards
}

func medianOf(durations map[string]float64) float64 {
	if len(durations) == 0 {
		return 1
	}
	v := make([]float64, 0, len(durations))
	for _, d := range durations {
		v = append(v, d)
	}
	sort.Float64s(v)
	return v[(len(v)-1)/2]
}

// readDurations reads "package seconds" lines; # starts a comment.
func readDurations(path string) (map[string]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open durations: %w", err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]float64{}
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text, _, _ := strings.Cut(sc.Text(), "#")
		fields := strings.Fields(text)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: want \"package seconds\"", path, line)
		}
		d, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || d < 0 {
			return nil, fmt.Errorf("%s:%d: bad seconds %q", path, line, fields[1])
		}
		out[fields[0]] = d
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read durations: %w", err)
	}
	return out, nil
}

// ── durations ────────────────────────────────────────────────────────────────

// timestamp is the prefix GitHub puts on every line of a job log.
var timestamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z `)

// okLine is go test's line for a package that passed: ok, the import path,
// the time. A cached result ("(cached)") has no time and is not matched.
var okLine = regexp.MustCompile(`^ok\s+(\S+)\s+([0-9]+(?:\.[0-9]+)?)s(?:\s|$)`)

const durationsHeader = `# hack/ci/test-durations.txt
#
# How long each Go package's tests take under go test -race in CI, in
# seconds: hack/citool shard splits the packages into the ci.yml test shards
# by these, so the shards finish together. A package not listed counts as the
# median. ci-health.yml measures them from the last green main runs every
# week and proposes a refresh when the split is more than 15% off; to refresh
# by hand: go run ./hack/citool durations LOG... > hack/ci/test-durations.txt
`

func cmdDurations(logs []string, stdout io.Writer) error {
	if len(logs) == 0 {
		return errUsage
	}
	var readers []io.Reader
	for _, l := range logs {
		f, err := os.Open(l)
		if err != nil {
			return fmt.Errorf("open log: %w", err)
		}
		defer func() { _ = f.Close() }()
		readers = append(readers, f)
	}
	d, err := measure(readers...)
	if err != nil {
		return err
	}
	return writeDurations(stdout, d)
}

// measure returns each package's median time over the logs it passed in.
func measure(logs ...io.Reader) (map[string]float64, error) {
	seen := map[string][]float64{}
	for _, r := range logs {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			line := timestamp.ReplaceAllString(sc.Text(), "")
			if m := okLine.FindStringSubmatch(line); m != nil {
				d, _ := strconv.ParseFloat(m[2], 64)
				seen[m[1]] = append(seen[m[1]], d)
			}
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read log: %w", err)
		}
	}
	out := map[string]float64{}
	for p, v := range seen {
		sort.Float64s(v)
		m := v[len(v)/2]
		if len(v)%2 == 0 {
			m = (v[len(v)/2-1] + v[len(v)/2]) / 2
		}
		out[p] = m
	}
	return out, nil
}

func writeDurations(w io.Writer, d map[string]float64) error {
	pkgs := make([]string, 0, len(d))
	for p := range d {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	var b strings.Builder
	b.WriteString(durationsHeader)
	for _, p := range pkgs {
		fmt.Fprintf(&b, "%s %.1f\n", p, d[p])
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// ── skew ─────────────────────────────────────────────────────────────────────

func cmdSkew(args []string, stdout io.Writer) error {
	if len(args) != 3 {
		return errUsage
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 {
		return fmt.Errorf("skew: bad shard count %q", args[0])
	}
	current, err := readDurations(args[1])
	if err != nil {
		return err
	}
	measured, err := readDurations(args[2])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "%.2f\n", skew(n, current, measured))
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// skew is the slowest shard's measured time over the ideal (the measured
// total / n) when the measured packages are split by the current durations:
// 1.00 is a perfect split, 1.20 means the critical shard runs 20% long.
func skew(n int, current, measured map[string]float64) float64 {
	pkgs := make([]string, 0, len(measured))
	total := 0.0
	for p, d := range measured {
		pkgs = append(pkgs, p)
		total += d
	}
	if total == 0 {
		return 1
	}
	worst := 0.0
	for _, s := range split(pkgs, current, n) {
		sum := 0.0
		for _, p := range s {
			sum += measured[p]
		}
		worst = max(worst, sum)
	}
	return worst / (total / float64(n))
}

// ── failures ─────────────────────────────────────────────────────────────────

// Failure is one failing test, or a package that failed with no failing test
// (a build failure, a panic outside a test, a timeout): Test is "-" then,
// or the test that was running when a timeout panic named it.
type Failure struct {
	Package, Test string
}

var (
	failTest    = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	failPackage = regexp.MustCompile(`^FAIL\s+(\S+)(?:\s+[0-9.]+s|\s+\[[^\]]*\])\s*$`)
	okPackage   = regexp.MustCompile(`^ok\s+(\S+)\s`)
	timedOut    = regexp.MustCompile(`^panic: test timed out after`)
	runningTest = regexp.MustCompile(`^\s+(Test\S*) \([0-9.hms]+\)\s*$`)
)

// parseFailures reads go test output (several packages, -v or not, with or
// without GitHub's timestamps). go test prints each package's output in one
// block that ends with its ok or FAIL line, so a failing test belongs to the
// package whose FAIL line follows it. Subtests are reported, their parents
// only when no subtest failed.
func parseFailures(r io.Reader) []Failure {
	var out []Failure
	var pending []string
	inTimeout := false
	timeouts := map[string]bool{}
	seen := map[Failure]bool{}
	add := func(f Failure) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := timestamp.ReplaceAllString(sc.Text(), "")
		switch {
		case timedOut.MatchString(line):
			inTimeout = true
		case inTimeout && runningTest.MatchString(line):
			name := runningTest.FindStringSubmatch(line)[1]
			pending = append(pending, name)
			timeouts[name] = true
		case failTest.MatchString(line):
			pending = append(pending, failTest.FindStringSubmatch(line)[1])
		case failPackage.MatchString(line):
			pkg := failPackage.FindStringSubmatch(line)[1]
			tests := leaves(pending)
			if len(tests) == 0 {
				tests = []string{"-"}
			}
			for _, t := range tests {
				if timeouts[t] {
					t += " (timeout)"
				}
				add(Failure{Package: pkg, Test: t})
			}
			pending, inTimeout, timeouts = nil, false, map[string]bool{}
		case okPackage.MatchString(line):
			pending, inTimeout, timeouts = nil, false, map[string]bool{}
		}
	}
	return out
}

// leaves drops a test whose subtest is also in the list.
func leaves(tests []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tests {
		parent := false
		for _, u := range tests {
			if strings.HasPrefix(u, t+"/") {
				parent = true
				break
			}
		}
		if !parent && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// ── flaky ────────────────────────────────────────────────────────────────────

// Record is one job of one run attempt, a line of the RECORDS file:
// sha, run id, attempt, job name, job id, conclusion, tab-separated.
type Record struct {
	SHA, RunID, Attempt, Job, JobID, Conclusion string
}

// Flake is a test that failed in one job and passed in a run of the same job
// on the same commit.
type Flake struct {
	Failure
	Job  string
	Runs []string // "<run id>/<attempt>" of each failing job
}

func cmdFlaky(args []string, stdout io.Writer) error {
	repo := ""
	if len(args) >= 2 && args[0] == "-repo" {
		repo, args = args[1], args[2:]
	}
	if len(args) != 2 || repo == "" {
		return errUsage
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return fmt.Errorf("read records: %w", err)
	}
	var recs []Record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 {
			continue
		}
		recs = append(recs, Record{f[0], f[1], f[2], f[3], f[4], f[5]})
	}
	logDir := args[1]
	flakes, err := findFlakes(recs, func(jobID string) (io.ReadCloser, error) {
		return os.Open(filepath.Join(logDir, jobID+".log"))
	})
	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, renderFlakes(repo, flakes))
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// findFlakes returns the failures of every job that also succeeded on the
// same commit, most frequent first. A job log that is gone (expired) is
// skipped.
func findFlakes(recs []Record, openLog func(jobID string) (io.ReadCloser, error)) ([]Flake, error) {
	type key struct{ sha, job string }
	passed := map[key]bool{}
	for _, r := range recs {
		if r.Conclusion == "success" {
			passed[key{r.SHA, r.Job}] = true
		}
	}
	byFailure := map[string]*Flake{}
	var order []string
	for _, r := range recs {
		if r.Conclusion != "failure" || !passed[key{r.SHA, r.Job}] {
			continue
		}
		rc, err := openLog(r.JobID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("open log of job %s: %w", r.JobID, err)
		}
		fails := parseFailures(rc)
		_ = rc.Close()
		if len(fails) == 0 {
			fails = []Failure{{Package: "-", Test: "-"}}
		}
		for _, f := range fails {
			id := r.Job + "\x00" + f.Package + "\x00" + f.Test
			fl, ok := byFailure[id]
			if !ok {
				fl = &Flake{Failure: f, Job: r.Job}
				byFailure[id] = fl
				order = append(order, id)
			}
			fl.Runs = append(fl.Runs, r.RunID+"/"+r.Attempt)
		}
	}
	out := make([]Flake, 0, len(order))
	for _, id := range order {
		out = append(out, *byFailure[id])
	}
	sort.SliceStable(out, func(a, b int) bool {
		if len(out[a].Runs) != len(out[b].Runs) {
			return len(out[a].Runs) > len(out[b].Runs)
		}
		return out[a].Package+out[a].Test < out[b].Package+out[b].Test
	})
	return out, nil
}

func renderFlakes(repo string, flakes []Flake) string {
	if len(flakes) == 0 {
		return "No flaky tests: no job both failed and passed on the same commit.\n"
	}
	var b strings.Builder
	b.WriteString("| test | package | job | failures | runs |\n|---|---|---|---|---|\n")
	for _, f := range flakes {
		var links []string
		for _, r := range f.Runs {
			id, attempt, _ := strings.Cut(r, "/")
			links = append(links, fmt.Sprintf("[%s#%s](https://github.com/%s/actions/runs/%s/attempts/%s)", id, attempt, repo, id, attempt))
		}
		test, pkg := f.Test, strings.TrimPrefix(f.Package, "github.com/kardinal-promoter/kardinal-promoter/")
		if test == "-" {
			test = "(no failing test in the log)"
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s | %d | %s |\n", test, pkg, f.Job, len(f.Runs), strings.Join(links, " "))
	}
	return b.String()
}

// ── io ───────────────────────────────────────────────────────────────────────

func readLines(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			out = append(out, s)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return out, nil
}
