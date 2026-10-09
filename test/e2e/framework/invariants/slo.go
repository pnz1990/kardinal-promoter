// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package invariants

import (
	"fmt"
	"time"
)

// SLO is a promotion latency objective. Step latency is from a PromotionStep's
// creation (its upstream environments are Verified and its gates passed) to
// its Verified condition, for automatic environments only: a pr-review
// step's latency is the reviewer's. Bundle latency is from the Bundle's
// creation to its last environment Verified, for Bundles that ended
// Verified. A zero field is not checked.
type SLO struct {
	StepP50, StepP99 time.Duration
	BundleP99        time.Duration
}

// checkSLO fails when a quantile is over its objective, or when there is
// nothing to measure.
func checkSLO(st *state, o Options) Result {
	res := Result{Name: "latency-slo"}
	auto := map[string]bool{}
	for _, t := range o.Targets {
		for _, env := range t.Pipeline.Spec.Environments {
			if env.Approval != "pr-review" {
				auto[t.Pipeline.Name+"/"+env.Name] = true
			}
		}
	}
	var steps []float64
	for i := range st.steps {
		s := &st.steps[i]
		if !auto[s.Spec.PipelineName+"/"+s.Spec.Environment] {
			continue
		}
		if at := verifiedAt(s); !at.IsZero() {
			steps = append(steps, at.Sub(s.CreationTimestamp.Time).Seconds())
		}
	}
	q := quantiles(steps)
	b := latency(st).Bundle
	if q.N == 0 {
		res.Violations = append(res.Violations, "no automatic step was Verified: nothing to measure")
		return res
	}
	over := func(what string, got float64, want time.Duration) {
		if want > 0 && got > want.Seconds() {
			res.Violations = append(res.Violations, fmt.Sprintf("%s is %.0fs, over the objective of %s", what, got, want))
		}
	}
	over("auto step p50", q.P50, o.SLO.StepP50)
	over("auto step p99", q.P99, o.SLO.StepP99)
	if o.SLO.BundleP99 > 0 {
		if b.N == 0 {
			res.Violations = append(res.Violations, "no Bundle ended Verified: Bundle latency not measured")
		}
		over("Bundle end to end p99", b.P99, o.SLO.BundleP99)
	}
	res.Note = fmt.Sprintf("auto step p50 %.0fs / p99 %.0fs over %d steps (objective %s / %s); Bundle p99 %.0fs over %d (objective %s)",
		q.P50, q.P99, q.N, o.SLO.StepP50, o.SLO.StepP99, b.P99, b.N, o.SLO.BundleP99)
	return res
}
