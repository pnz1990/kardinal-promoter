// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package invariants

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Report is one Check run: written as <Dir>/report.json and report.md.
type Report struct {
	Test      string    `json:"test"`
	Namespace string    `json:"namespace"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Duration  string    `json:"duration"`
	Pass      bool      `json:"pass"`
	Checks    []Result  `json:"checks"`

	Bundles map[string]int         `json:"bundles"`
	Steps   map[string]int         `json:"steps"`
	Latency LatencyReport          `json:"latency"`
	Graphs  GraphInfo              `json:"graphs"`
	Logs    LogSummary             `json:"logs"`
	Metrics *Metrics               `json:"metrics,omitempty"`
	Extra   map[string]interface{} `json:"extra,omitempty"`

	skip map[string]string
}

// Result is one check.
type Result struct {
	Name       string   `json:"name"`
	Pass       bool     `json:"pass"`
	Violations []string `json:"violations,omitempty"`
	Note       string   `json:"note,omitempty"`
}

func (r *Report) add(res Result) {
	if why, ok := r.skip[res.Name]; ok {
		res = Result{Name: res.Name, Pass: true, Note: "skipped: " + why}
	}
	res.Pass = len(res.Violations) == 0
	r.Checks = append(r.Checks, res)
}

// LatencyReport is promotion latency from the objects: per environment, the
// time from a PromotionStep's creation to its Verified condition, and per
// Bundle, from its creation to its last environment Verified.
type LatencyReport struct {
	// Stage holds per-environment quantiles; Overall pools every step.
	Stage   map[string]Quantiles `json:"perStage"`
	Overall Quantiles            `json:"overall"`
	Bundle  Quantiles            `json:"bundleEndToEnd"`
}

func latency(st *state) LatencyReport {
	per := map[string][]float64{}
	var all []float64
	ends := map[string]time.Time{}
	for i := range st.steps {
		s := &st.steps[i]
		at := verifiedAt(s)
		if at.IsZero() {
			continue
		}
		d := at.Sub(s.CreationTimestamp.Time).Seconds()
		per[s.Spec.Environment] = append(per[s.Spec.Environment], d)
		all = append(all, d)
		if at.After(ends[s.Spec.BundleName]) {
			ends[s.Spec.BundleName] = at
		}
	}
	var e2e []float64
	for _, b := range st.bundles {
		if b.Status.Phase == "Verified" && !ends[b.Name].IsZero() {
			e2e = append(e2e, ends[b.Name].Sub(b.CreationTimestamp.Time).Seconds())
		}
	}
	out := LatencyReport{Stage: map[string]Quantiles{}, Overall: quantiles(all), Bundle: quantiles(e2e)}
	for env, v := range per {
		out.Stage[env] = quantiles(v)
	}
	return out
}

func quantiles(v []float64) Quantiles {
	if len(v) == 0 {
		return Quantiles{}
	}
	sort.Float64s(v)
	q := func(p float64) float64 {
		i := int(math.Ceil(p*float64(len(v)))) - 1
		if i < 0 {
			i = 0
		}
		return round(v[i])
	}
	return Quantiles{N: len(v), P50: q(0.5), P99: q(0.99), Max: round(v[len(v)-1])}
}

// Write writes the report as JSON and markdown to Dir(t) and logs the
// markdown.
func (r *Report) Write(t *testing.T) {
	t.Helper()
	dir := Dir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Errorf("report dir: %v", err)
		return
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0o644)
	}
	md := r.Markdown()
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "report.md"), []byte(md), 0o644)
	}
	if err != nil {
		t.Errorf("write report: %v", err)
	}
	t.Logf("invariants report %s/report.json\n%s", dir, md)
}

// Markdown is the short summary: verdict, the checks, the numbers.
func (r *Report) Markdown() string {
	var b strings.Builder
	verdict := "PASS"
	if !r.Pass {
		verdict = "FAIL"
	}
	fmt.Fprintf(&b, "### %s: %s (%s)\n\n", r.Test, verdict, r.Duration)
	b.WriteString("| Check | Result | Detail |\n|---|---|---|\n")
	for _, c := range r.Checks {
		res, detail := "pass", c.Note
		if !c.Pass {
			res = fmt.Sprintf("**FAIL** (%d)", len(c.Violations))
			detail = strings.ReplaceAll(trim(c.Violations[0], 160), "\n", " ")
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", c.Name, res, strings.ReplaceAll(detail, "|", `\|`))
	}
	fmt.Fprintf(&b, "\nBundles %v; steps %v\n", sorted(r.Bundles), sorted(r.Steps))
	fmt.Fprintf(&b, "\nPromotion latency (step created to Verified): p50 %.1fs, p99 %.1fs, max %.1fs over %d steps; Bundle end to end p50 %.1fs, p99 %.1fs over %d Bundles\n",
		r.Latency.Overall.P50, r.Latency.Overall.P99, r.Latency.Overall.Max, r.Latency.Overall.N,
		r.Latency.Bundle.P50, r.Latency.Bundle.P99, r.Latency.Bundle.N)
	fmt.Fprintf(&b, "\nGraphs: %d, at most %d nodes, %d bytes; %d GraphRevisions\n", r.Graphs.Count, r.Graphs.MaxNodes, r.Graphs.MaxBytes, r.Graphs.Revisions)
	if m := r.Metrics; m != nil {
		fmt.Fprintf(&b, "\nReconcile error ratio %.2f%%; step p50/p99 by type %v\n", 100*m.ErrorRatio, m.StepSeconds)
		for _, p := range m.Pods {
			fmt.Fprintf(&b, "- %s: RSS %.0f -> %.0f MiB (peak %.0f), goroutines %.0f -> %.0f (peak %.0f)\n",
				p.Pod, p.RSSStartMiB, p.RSSEndMiB, p.RSSMaxMiB, p.GoroutinesStart, p.GoroutinesEnd, p.GoroutinesMax)
		}
	}
	if len(r.Extra) > 0 {
		keys := make([]string, 0, len(r.Extra))
		for k := range r.Extra {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "- %s: %v\n", k, r.Extra[k])
		}
	}
	return b.String()
}

func sorted(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}
