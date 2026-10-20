// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// NotRunItem is a line of hack/release-gate/not-run.txt: something a
// release ships that the gate does not run, why, and the evidence that
// stands in for it. The report lists them, so a GO verdict does not read as
// proof of what the gate never ran.
type NotRunItem struct {
	Item, Reason string
	Evidence     []Evidence
}

// Evidence is a test/e2e/coverage.tsv row, with its tier, or a job of
// .github/workflows/release.yml.
type Evidence struct {
	Row, Tier string
	Job       string
}

func (e Evidence) String() string {
	if e.Job != "" {
		return fmt.Sprintf("release.yml job `%s`", e.Job)
	}
	return fmt.Sprintf("%s (%s)", e.Row, e.Tier)
}

const releaseJobPrefix = "release.yml:"

// readNotRun reads "item | reason | evidence" lines; # starts a comment.
// The evidence is comma-separated: coverage.tsv row IDs, whose status must
// be covered, or release.yml:<job>. A line without a reason or evidence, a
// row that is not in coverage.tsv or not covered, and a job release.yml
// does not have are errors, so the list cannot cite evidence that is gone.
// An empty list is an error too: every release ships things the gate does
// not run, so an empty list would read as proof of them.
func readNotRun(root string) ([]NotRunItem, error) {
	path := filepath.Join(root, "hack", "release-gate", "not-run.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the gate's not-run list: %w", err)
	}
	rows, err := coverageRows(root)
	if err != nil {
		return nil, err
	}
	var jobs map[string]bool
	var out []NotRunItem
	for n, l := range strings.Split(string(data), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		at := fmt.Sprintf("%s:%d", path, n+1)
		fields := strings.Split(l, "|")
		if len(fields) != 3 {
			return nil, fmt.Errorf("%s: want item | reason | evidence, got %d fields", at, len(fields))
		}
		it := NotRunItem{Item: strings.TrimSpace(fields[0]), Reason: strings.TrimSpace(fields[1])}
		switch {
		case it.Item == "":
			return nil, fmt.Errorf("%s: no item", at)
		case it.Reason == "":
			return nil, fmt.Errorf("%s: %s has no reason", at, it.Item)
		}
		for _, ref := range strings.Split(fields[2], ",") {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			if job, ok := strings.CutPrefix(ref, releaseJobPrefix); ok {
				if jobs == nil {
					if jobs, err = releaseJobs(root); err != nil {
						return nil, err
					}
				}
				if !jobs[job] {
					return nil, fmt.Errorf("%s: %s cites release.yml job %s, which .github/workflows/release.yml does not have", at, it.Item, job)
				}
				it.Evidence = append(it.Evidence, Evidence{Job: job})
				continue
			}
			row, ok := rows[ref]
			if !ok {
				return nil, fmt.Errorf("%s: %s cites %s, which is not a row of test/e2e/coverage.tsv", at, it.Item, ref)
			}
			if row.status != "covered" {
				return nil, fmt.Errorf("%s: %s cites %s, whose status is %s, not covered", at, it.Item, ref, row.status)
			}
			it.Evidence = append(it.Evidence, Evidence{Row: ref, Tier: row.tier})
		}
		if len(it.Evidence) == 0 {
			return nil, fmt.Errorf("%s: %s has no evidence (coverage.tsv rows or %s<job>)", at, it.Item, releaseJobPrefix)
		}
		out = append(out, it)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s lists nothing: every release ships things the gate does not run (cloud KMS keys, "+
			"EKS, the public-good sigstore, ticketing vendors, publishing, other platforms), and an empty list reads as proof of them", path)
	}
	return out, nil
}

type coverageRow struct{ tier, status string }

// coverageRows are test/e2e/coverage.tsv's rows by ID.
func coverageRows(root string) (map[string]coverageRow, error) {
	f, err := os.Open(filepath.Join(root, "test", "e2e", "coverage.tsv"))
	if err != nil {
		return nil, fmt.Errorf("open coverage.tsv: %w", err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]coverageRow{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for first := true; sc.Scan(); first = false {
		cols := strings.Split(sc.Text(), "\t")
		if first || len(cols) < 4 {
			continue
		}
		out[cols[0]] = coverageRow{tier: cols[1], status: cols[3]}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read coverage.tsv: %w", err)
	}
	return out, nil
}

// releaseJobs are the job IDs of .github/workflows/release.yml.
func releaseJobs(root string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		return nil, fmt.Errorf("read release.yml: %w", err)
	}
	var wf struct {
		Jobs map[string]any `json:"jobs"`
	}
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parse release.yml: %w", err)
	}
	out := map[string]bool{}
	for j := range wf.Jobs {
		out[j] = true
	}
	return out, nil
}

// renderNotRun is the report's "Not run by this gate" section.
func renderNotRun(b *strings.Builder, items []NotRunItem) {
	b.WriteString("\n## Not run by this gate\n\n")
	if len(items) == 0 {
		b.WriteString("**The not-run list was not loaded**: this report says nothing about what the gate does not run.\n")
		return
	}
	b.WriteString("The verdict covers only what the steps above ran. The gate does not run these " +
		"(hack/release-gate/not-run.txt); the last column is the evidence that stands in for each. " +
		"A contract row is a unit or contract test, not a live run; a live row is a live e2e test.\n\n" +
		"| Not run | Why | Evidence instead |\n|---|---|---|\n")
	for _, it := range items {
		ev := make([]string, len(it.Evidence))
		for i, e := range it.Evidence {
			ev[i] = e.String()
		}
		fmt.Fprintf(b, "| %s | %s | %s |\n", it.Item, it.Reason, strings.Join(ev, ", "))
	}
}
