// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package invariants

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// Metrics is what Prometheus recorded of the controller during the run.
type Metrics struct {
	// Reconciles and ReconcileErrors are increases over the run, per
	// controller-runtime controller.
	Reconciles      map[string]float64 `json:"reconciles"`
	ReconcileErrors map[string]float64 `json:"reconcileErrors"`
	ErrorRatio      float64            `json:"reconcileErrorRatio"`
	// QueueDepthMax is the deepest each work queue got; QueueDepthEnd its
	// depth at the end.
	QueueDepthMax map[string]float64 `json:"workqueueDepthMax"`
	QueueDepthEnd map[string]float64 `json:"workqueueDepthEnd"`
	// StepSeconds is kardinal_step_duration_seconds p50/p99 per step type
	// over the run.
	StepSeconds map[string]Quantiles `json:"stepDurationSeconds"`
	// Pods holds each controller Pod's memory and goroutines over the run.
	Pods []PodSeries `json:"pods"`
}

// Quantiles are p50 and p99 in seconds.
type Quantiles struct {
	N   int     `json:"n,omitempty"`
	P50 float64 `json:"p50"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max,omitempty"`
}

// PodSeries is one controller Pod's resident memory (MiB) and goroutines at
// the start, peak and end of the window it was scraped in.
type PodSeries struct {
	Pod             string    `json:"pod"`
	From            time.Time `json:"from"`
	To              time.Time `json:"to"`
	RSSStartMiB     float64   `json:"rssStartMiB"`
	RSSMaxMiB       float64   `json:"rssMaxMiB"`
	RSSEndMiB       float64   `json:"rssEndMiB"`
	GoroutinesStart float64   `json:"goroutinesStart"`
	GoroutinesMax   float64   `json:"goroutinesMax"`
	GoroutinesEnd   float64   `json:"goroutinesEnd"`
}

const ctrlSel = `namespace="` + framework.ControllerNamespace + `",job="` + framework.ControllerName + `"`

// checkMetrics reads the run's window from Prometheus: the reconcile error
// ratio must stay under MaxReconcileErrorRatio, every work queue must drain
// (depth 0 at the end, checked for up to two minutes), and a controller Pod
// that ran the whole window must end with no more than 1.5x (plus 100) the
// goroutines and 2x (plus 200 MiB) the resident memory it started with, and
// stay under 90% of its memory limit.
func checkMetrics(ctx context.Context, e *framework.Env, o Options) (*Metrics, []Result) {
	m := &Metrics{}
	start, end := o.Start, time.Now()
	window := fmt.Sprintf("%ds", int(math.Max(60, end.Sub(start).Seconds())))
	errs := Result{Name: "metrics-reconcile-errors"}
	queue := Result{Name: "metrics-workqueue-drains"}
	leak := Result{Name: "metrics-no-leak"}

	m.Reconciles = byLabel(ctx, e, `sum by (controller) (increase(controller_runtime_reconcile_total{`+ctrlSel+`}[`+window+`]))`, "controller")
	m.ReconcileErrors = byLabel(ctx, e, `sum by (controller) (increase(controller_runtime_reconcile_errors_total{`+ctrlSel+`}[`+window+`]))`, "controller")
	var tot, bad float64
	for k, v := range m.Reconciles {
		tot += v
		bad += m.ReconcileErrors[k]
	}
	if tot > 0 {
		m.ErrorRatio = bad / tot
	}
	switch {
	case tot == 0:
		errs.Violations = append(errs.Violations, "Prometheus has no controller_runtime_reconcile_total for the controller in the run's window (is its ServiceMonitor scraped?)")
	case m.ErrorRatio > o.MaxReconcileErrorRatio:
		var worst []string
		for k, v := range m.ReconcileErrors {
			if v > 0 {
				worst = append(worst, fmt.Sprintf("%s %.0f/%.0f", k, v, m.Reconciles[k]))
			}
		}
		sort.Strings(worst)
		errs.Violations = append(errs.Violations, fmt.Sprintf("%.1f%% of %.0f reconciles failed (limit %.1f%%): %s",
			100*m.ErrorRatio, tot, 100*o.MaxReconcileErrorRatio, strings.Join(worst, ", ")))
	}
	errs.Note = fmt.Sprintf("%.0f reconciles, %.0f errors (%.2f%%)", tot, bad, 100*m.ErrorRatio)

	m.QueueDepthMax = byLabel(ctx, e, `max by (name) (max_over_time(workqueue_depth{`+ctrlSel+`}[`+window+`]))`, "name")
	deadline := time.Now().Add(2 * time.Minute)
	for {
		m.QueueDepthEnd = byLabel(ctx, e, `max by (name) (workqueue_depth{`+ctrlSel+`})`, "name")
		var deep []string
		for k, v := range m.QueueDepthEnd {
			if v > 0 {
				deep = append(deep, fmt.Sprintf("%s=%.0f", k, v))
			}
		}
		if len(deep) == 0 || o.SharedController || time.Now().After(deadline) {
			if len(deep) > 0 && o.SharedController {
				sort.Strings(deep)
				queue.Note = "shared with parallel tests; at the end: " + strings.Join(deep, ", ") + "; "
			} else if len(deep) > 0 {
				sort.Strings(deep)
				queue.Violations = append(queue.Violations, "work queues still not empty two minutes after the load: "+strings.Join(deep, ", "))
			}
			break
		}
		time.Sleep(10 * time.Second)
	}
	var maxQ []string
	for k, v := range m.QueueDepthMax {
		if v >= 10 {
			maxQ = append(maxQ, fmt.Sprintf("%s=%.0f", k, v))
		}
	}
	sort.Strings(maxQ)
	queue.Note += "peak depth >= 10: " + strings.Join(maxQ, ", ")

	m.StepSeconds = map[string]Quantiles{}
	p50 := byLabel(ctx, e, `histogram_quantile(0.5, sum by (le, step) (increase(kardinal_step_duration_seconds_bucket{`+ctrlSel+`}[`+window+`])))`, "step")
	p99 := byLabel(ctx, e, `histogram_quantile(0.99, sum by (le, step) (increase(kardinal_step_duration_seconds_bucket{`+ctrlSel+`}[`+window+`])))`, "step")
	for k, v := range p50 {
		m.StepSeconds[k] = Quantiles{P50: round(v), P99: round(p99[k])}
	}

	m.Pods = podSeries(ctx, e, start, end)
	leak.Violations = leaks(m.Pods, start, end, memoryLimitMiB(ctx, e), o.SharedController)
	return m, []Result{errs, queue, leak}
}

func round(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*1000) / 1000
}

// byLabel runs an instant query and maps each sample's label to its value.
func byLabel(ctx context.Context, e *framework.Env, q, label string) map[string]float64 {
	out := map[string]float64{}
	s, err := e.PromQuery(ctx, q)
	if err != nil {
		return out
	}
	for _, x := range s {
		v, err := strconv.ParseFloat(x.Value, 64)
		if err != nil || math.IsNaN(v) {
			continue
		}
		out[x.Metric[label]] = v
	}
	return out
}

// podSeries reads each controller Pod's RSS and goroutines over the window.
func podSeries(ctx context.Context, e *framework.Env, start, end time.Time) []PodSeries {
	step := 15 * time.Second
	if d := end.Sub(start); d > 2*time.Hour {
		step = d / 400
	}
	rss, _ := e.PromQueryRange(ctx, `process_resident_memory_bytes{`+ctrlSel+`}`, start, end, step)
	gor, _ := e.PromQueryRange(ctx, `go_goroutines{`+ctrlSel+`}`, start, end, step)
	byPod := map[string]*PodSeries{}
	get := func(pod string) *PodSeries {
		if byPod[pod] == nil {
			byPod[pod] = &PodSeries{Pod: pod}
		}
		return byPod[pod]
	}
	for _, s := range rss {
		if len(s.Points) == 0 {
			continue
		}
		p := get(s.Metric["pod"])
		p.From, p.To = s.Points[0].Time, s.Points[len(s.Points)-1].Time
		p.RSSStartMiB, p.RSSEndMiB = mib(s.Points[0].Value), mib(s.Points[len(s.Points)-1].Value)
		for _, pt := range s.Points {
			p.RSSMaxMiB = math.Max(p.RSSMaxMiB, mib(pt.Value))
		}
	}
	for _, s := range gor {
		if len(s.Points) == 0 {
			continue
		}
		p := get(s.Metric["pod"])
		p.GoroutinesStart, p.GoroutinesEnd = s.Points[0].Value, s.Points[len(s.Points)-1].Value
		for _, pt := range s.Points {
			p.GoroutinesMax = math.Max(p.GoroutinesMax, pt.Value)
		}
	}
	out := make([]PodSeries, 0, len(byPod))
	for _, p := range byPod {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From.Before(out[j].From) })
	return out
}

func mib(b float64) float64 { return math.Round(b/(1<<20)*10) / 10 }

// memoryLimitMiB is the controller container's memory limit, or 0.
func memoryLimitMiB(ctx context.Context, e *framework.Env) float64 {
	var pods corev1.PodList
	if err := e.Client.List(ctx, &pods, client.InNamespace(framework.ControllerNamespace),
		client.MatchingLabels{"app.kubernetes.io/name": framework.ControllerName}); err != nil || len(pods.Items) == 0 {
		return 0
	}
	for _, c := range pods.Items[0].Spec.Containers {
		if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			return float64(q.Value()) / (1 << 20)
		}
	}
	return 0
}

// checkRestarts: no controller container restarted on its own (OOMKilled,
// a crash) during the run. Pods the test deleted are new Pods, not
// restarts, so a test that kills leaders still gets this check.
func checkRestarts(ctx context.Context, e *framework.Env, o Options) Result {
	return restarts(ctx, e, o, "controller-no-restarts", framework.ControllerNamespace, framework.ControllerName)
}

// checkKroRestarts is checkRestarts for the kro controller: an OOMKilled kro
// stops every Graph in the cluster.
func checkKroRestarts(ctx context.Context, e *framework.Env, o Options) Result {
	return restarts(ctx, e, o, "kro-no-restarts", "kro-system", "kro")
}

func restarts(ctx context.Context, e *framework.Env, o Options, check, ns, name string) Result {
	res := Result{Name: check}
	var pods corev1.PodList
	if err := e.Client.List(ctx, &pods, client.InNamespace(ns),
		client.MatchingLabels{"app.kubernetes.io/name": name}); err != nil {
		res.Violations = append(res.Violations, "list "+name+" Pods: "+err.Error())
		return res
	}
	for _, p := range pods.Items {
		for _, cs := range p.Status.ContainerStatuses {
			term := cs.LastTerminationState.Terminated
			if cs.RestartCount > 0 && term != nil && term.FinishedAt.After(o.Start) {
				res.Violations = append(res.Violations, fmt.Sprintf("%s/%s restarted %d times; last exit %s (code %d) at %s",
					p.Name, cs.Name, cs.RestartCount, term.Reason, term.ExitCode, term.FinishedAt.UTC().Format(time.RFC3339)))
			}
		}
	}
	return res
}

// leaks checks each controller Pod's series: one that ran the whole window
// (it started within a minute of start and was scraped within a minute of
// end) must not end with more than 1.5x (plus 100) its goroutines or 2x
// (plus 200 MiB) its resident memory, unless other tests shared the
// controller; and no Pod may pass 90% of the memory limit.
func leaks(pods []PodSeries, start, end time.Time, limitMiB float64, shared bool) []string {
	var v []string
	if len(pods) == 0 {
		return []string{"Prometheus has no process_resident_memory_bytes for the controller in the run's window"}
	}
	for _, p := range pods {
		whole := p.From.Sub(start) < time.Minute && end.Sub(p.To) < time.Minute
		if whole && !shared && p.GoroutinesEnd > math.Max(1.5*p.GoroutinesStart, p.GoroutinesStart+100) {
			v = append(v, fmt.Sprintf("%s: goroutines %.0f at the start, %.0f at the end (peak %.0f)",
				p.Pod, p.GoroutinesStart, p.GoroutinesEnd, p.GoroutinesMax))
		}
		if whole && !shared && p.RSSEndMiB > 2*p.RSSStartMiB+200 {
			v = append(v, fmt.Sprintf("%s: resident memory %.0f MiB at the start, %.0f MiB at the end (over 2x + 200 MiB; peak %.0f)",
				p.Pod, p.RSSStartMiB, p.RSSEndMiB, p.RSSMaxMiB))
		}
		if limitMiB > 0 && p.RSSMaxMiB > 0.9*limitMiB {
			v = append(v, fmt.Sprintf("%s: resident memory peaked at %.0f MiB, over 90%% of its %.0f MiB limit",
				p.Pod, p.RSSMaxMiB, limitMiB))
		}
	}
	return v
}
