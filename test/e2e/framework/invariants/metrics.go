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
	// PushesLanded and PushesRefused are the git pushes over the run that
	// landed and that the server refused as non-fast-forward
	// (kardinal_git_operations_total).
	PushesLanded  float64 `json:"pushesLanded"`
	PushesRefused float64 `json:"pushesRefused"`
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
	// WarmAt is the warm baseline (Options.WarmAt, or the series' start):
	// the *Warm fields are the first sample at or after it.
	WarmAt     time.Time `json:"warmAt"`
	RSSWarmMiB float64   `json:"rssWarmMiB"`
	// SysWarmMiB and SysEndMiB are the memory the Go runtime holds from the
	// OS (go_memstats_sys_bytes); HeapWarmMiB is the lowest heap in use
	// (go_memstats_heap_inuse_bytes) in the warmHeapWindow after the warm
	// baseline, and HeapAfterGCMiB the heap in use
	// after the first garbage collection once the load is over, read from
	// the Pod (0 when not measured).
	SysWarmMiB     float64 `json:"sysWarmMiB"`
	SysEndMiB      float64 `json:"sysEndMiB"`
	HeapWarmMiB    float64 `json:"heapWarmMiB"`
	HeapAfterGCMiB float64 `json:"heapAfterGcMiB"`
	// LeaderStart and LeaderEnd say whether the Pod led at the start and
	// at the end of its series (leader_election_master_status): a standby
	// that took over starts every controller, so its growth is no leak.
	LeaderStart bool `json:"leaderStart"`
	LeaderEnd   bool `json:"leaderEnd"`
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
	// The Pods' memory and goroutines end once the work the load left has
	// drained, plus two scrapes: the last Bundles settling spawn a short
	// burst of goroutines (the RC soak: 520 -> 1671 for under a minute),
	// which the window's last sample can land on.
	memEnd := time.Now().Add(drainScrapes)
	time.Sleep(drainScrapes)
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

	warm := start
	if o.WarmAt.After(start) {
		warm = o.WarmAt
	}
	m.Pods = podSeries(ctx, e, start, memEnd, warm)
	if o.RaceBuild && !o.SharedController && warm != start {
		heapAfterGC(ctx, e, m.Pods, end)
	}
	leak.Violations = leaks(m.Pods, start, memEnd, memoryLimitMiB(ctx, e), o.SharedController, o.RaceBuild, warm != start)

	push := Result{Name: "metrics-push-efficiency"}
	pushSel := `kardinal_git_operations_total{` + ctrlSel + `,operation="push"}`
	pushes := countsFromZero(
		samples(ctx, e, `max_over_time(`+pushSel+`[`+window+`])`),
		samples(ctx, e, fmt.Sprintf("%s @ %d", pushSel, start.Unix())), "result")
	m.PushesLanded, m.PushesRefused = round(pushes["ok"]), round(pushes["non_fast_forward"])
	push.Violations = pushEfficiency(m.PushesLanded, m.PushesRefused, o.MaxRefusedPushRatio, o.SharedController)
	push.Note = fmt.Sprintf("%.0f pushes landed, %.0f refused as non-fast-forward", m.PushesLanded, m.PushesRefused)
	if o.SharedController {
		push.Note += " (shared with parallel tests: reported only)"
	}
	return m, []Result{errs, queue, leak, push}
}

// pushEfficiency fails a run whose git pushes were refused more than
// maxRatio times per push that landed (#1578): the promotions of one
// controller that write one branch take turns, so a refusal means another
// writer moved the branch. Each environment of a wave racing the others
// for the branch made 13 refusals per landed push at 150 environments.
func pushEfficiency(landed, refused, maxRatio float64, shared bool) []string {
	if shared || refused == 0 {
		return nil
	}
	if refused > maxRatio*math.Max(landed, 1) {
		return []string{fmt.Sprintf("%.0f pushes refused as non-fast-forward for %.0f that landed (%.2f per landed push; limit %.2f)",
			refused, landed, refused/math.Max(landed, 1), maxRatio)}
	}
	return nil
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

// samples runs an instant query, nil on error.
func samples(ctx context.Context, e *framework.Env, q string) []framework.PromSample {
	s, err := e.PromQuery(ctx, q)
	if err != nil {
		return nil
	}
	return s
}

// countsFromZero sums, by label, how much each counter series grew over a
// window: its highest value in the window (end) minus its value at the
// window's start (start), or minus nothing when the series did not exist
// then. increase() measures from a series' first sample instead, so a
// series that appeared in the window (a fresh controller, a new result, a
// new leader) lost the count it already had when first scraped: TwoTenants
// reported 121 pushes for 151 environments. A series lower at the end than
// at the start was reset (its container restarted) and counts its end
// value.
func countsFromZero(end, start []framework.PromSample, label string) map[string]float64 {
	key := func(m map[string]string) string {
		keys := make([]string, 0, len(m))
		for k := range m {
			if k != "__name__" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(k + "=" + m[k] + ",")
		}
		return b.String()
	}
	base := map[string]float64{}
	for _, x := range start {
		if v, err := strconv.ParseFloat(x.Value, 64); err == nil && !math.IsNaN(v) {
			base[key(x.Metric)] = v
		}
	}
	out := map[string]float64{}
	for _, x := range end {
		v, err := strconv.ParseFloat(x.Value, 64)
		if err != nil || math.IsNaN(v) {
			continue
		}
		if b := base[key(x.Metric)]; v >= b {
			v -= b
		}
		out[x.Metric[label]] += v
	}
	return out
}

// podSeries reads each controller Pod's RSS, goroutines and Go memory over
// the window; the *Warm fields are taken at warm.
func podSeries(ctx context.Context, e *framework.Env, start, end, warm time.Time) []PodSeries {
	step := 15 * time.Second
	if d := end.Sub(start); d > 2*time.Hour {
		step = d / 400
	}
	q := func(metric string) []framework.PromSeries {
		s, _ := e.PromQueryRange(ctx, metric+`{`+ctrlSel+`}`, start, end, step)
		return s
	}
	byPod := map[string]*PodSeries{}
	get := func(pod string) *PodSeries {
		if byPod[pod] == nil {
			byPod[pod] = &PodSeries{Pod: pod}
		}
		return byPod[pod]
	}
	for _, s := range q("process_resident_memory_bytes") {
		if len(s.Points) == 0 {
			continue
		}
		p := get(s.Metric["pod"])
		p.From, p.To = s.Points[0].Time, s.Points[len(s.Points)-1].Time
		p.RSSStartMiB, p.RSSEndMiB = mib(s.Points[0].Value), mib(s.Points[len(s.Points)-1].Value)
		w := pointAt(s.Points, warm)
		p.WarmAt, p.RSSWarmMiB = w.Time, mib(w.Value)
		for _, pt := range s.Points {
			p.RSSMaxMiB = math.Max(p.RSSMaxMiB, mib(pt.Value))
		}
	}
	for _, s := range q("go_memstats_sys_bytes") {
		if len(s.Points) > 0 {
			p := get(s.Metric["pod"])
			p.SysWarmMiB, p.SysEndMiB = mib(pointAt(s.Points, warm).Value), mib(s.Points[len(s.Points)-1].Value)
		}
	}
	for _, s := range q("go_memstats_heap_inuse_bytes") {
		if len(s.Points) > 0 {
			get(s.Metric["pod"]).HeapWarmMiB = mib(minFrom(s.Points, warm, warmHeapWindow))
		}
	}
	for _, s := range q("go_goroutines") {
		if len(s.Points) == 0 {
			continue
		}
		p := get(s.Metric["pod"])
		p.GoroutinesStart, p.GoroutinesEnd = s.Points[0].Value, s.Points[len(s.Points)-1].Value
		for _, pt := range s.Points {
			p.GoroutinesMax = math.Max(p.GoroutinesMax, pt.Value)
		}
	}
	lead, _ := e.PromQueryRange(ctx, `max by (pod) (leader_election_master_status{`+ctrlSel+`})`, start, end, step)
	for _, s := range lead {
		if p := byPod[s.Metric["pod"]]; p != nil && len(s.Points) > 0 {
			p.LeaderStart, p.LeaderEnd = s.Points[0].Value == 1, s.Points[len(s.Points)-1].Value == 1
		}
	}
	out := make([]PodSeries, 0, len(byPod))
	for _, p := range byPod {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From.Before(out[j].From) })
	return out
}

// drainScrapes is how long after the work queues drained the memory and
// goroutine series end: two steps of podSeries' 15s range query (the suite's
// Prometheus scrapes every 5s).
const drainScrapes = 30 * time.Second

// warmHeapWindow is how long after the warm baseline the warm heap is the
// lowest sample of: one sample would land anywhere on the GC sawtooth.
const warmHeapWindow = 5 * time.Minute

// minFrom is the lowest value of the points in [t, t+d], or pointAt(t)'s
// when none is in it.
func minFrom(pts []framework.PromPoint, t time.Time, d time.Duration) float64 {
	low := math.Inf(1)
	for _, p := range pts {
		if !p.Time.Before(t) && !p.Time.After(t.Add(d)) {
			low = math.Min(low, p.Value)
		}
	}
	if math.IsInf(low, 1) {
		return pointAt(pts, t).Value
	}
	return low
}

// pointAt is the first point at or after t, or the last point.
func pointAt(pts []framework.PromPoint, t time.Time) framework.PromPoint {
	for _, p := range pts {
		if !p.Time.Before(t) {
			return p
		}
	}
	return pts[len(pts)-1]
}

// heapAfterGC sets each Pod's HeapAfterGCMiB: the heap in use read from the
// Pod's metrics endpoint just after its first garbage collection that ended
// after loadEnd. The Go runtime forces a collection at least every two
// minutes, so this waits up to three. A Pod with none stays at 0, which
// leaks reports.
func heapAfterGC(ctx context.Context, e *framework.Env, pods []PodSeries, loadEnd time.Time) {
	deadline := time.Now().Add(3 * time.Minute)
	for {
		left := 0
		for i := range pods {
			if pods[i].HeapAfterGCMiB > 0 {
				continue
			}
			v, err := podMetrics(ctx, e, pods[i].Pod, "go_memstats_last_gc_time_seconds", "go_memstats_heap_inuse_bytes")
			if err == nil && v[0] > float64(loadEnd.Unix()) && v[1] > 0 {
				pods[i].HeapAfterGCMiB = mib(v[1])
				continue
			}
			left++
		}
		if left == 0 || time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Second)
	}
}

// podMetrics reads the named unlabelled metrics from a controller Pod's
// metrics endpoint through the API server's Pod proxy.
func podMetrics(ctx context.Context, e *framework.Env, pod string, names ...string) ([]float64, error) {
	raw, err := e.Kube.CoreV1().RESTClient().Get().
		AbsPath("/api/v1/namespaces", framework.ControllerNamespace, "pods", pod+":8080", "proxy", "metrics").DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("metrics of %s: %w", pod, err)
	}
	return parseMetrics(string(raw), names...), nil
}

// parseMetrics returns the values of the named unlabelled samples in a
// Prometheus text exposition (NaN for one not found).
func parseMetrics(text string, names ...string) []float64 {
	out := make([]float64, len(names))
	for i := range out {
		out[i] = math.NaN()
	}
	for _, line := range strings.Split(text, "\n") {
		name, val, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		for i, n := range names {
			if name == n {
				if f, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
					out[i] = f
				}
			}
		}
	}
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

// leaks checks each controller Pod's series. A Pod that ran the whole
// window in one role (it started within a minute of start, was scraped
// within a minute of end, and led at both ends or at neither), unless other
// tests shared the controller, must not end with more than 1.5x (plus 100)
// its goroutines, and its memory is measured from the warm baseline
// (PodSeries.WarmAt):
//
//   - built without -race: resident memory at most 2x (plus 200 MiB) the
//     warm sample;
//   - built with -race, with a warm baseline (Options.WarmAt): the race detector's shadow memory is not the
//     controller's (the Go runtime does not account for it, and it is never
//     returned), so RSS is not bounded. Instead the memory the Go runtime
//     holds from the OS (go_memstats_sys_bytes) may grow at most 25% from
//     the warm sample to the end, and the heap in use after the first
//     garbage collection once the load is over must be below the lowest
//     heap in use of the five minutes after the warm baseline: what the
//     load left reachable is gone. A missing Go memory series fails;
//   - built with -race, with no warm baseline (a test whose load has no
//     steady state measures from its start): resident memory at most 2.5x
//     (plus 500 MiB) the start, as before the warm baseline existed. Steady
//     leaders measured up to 2.3x and +261 MiB in the full profile.
//
// No Pod may pass 90% of the memory limit, -race or not.
var (
	rssGrowth     = growth{factor: 2, slackMiB: 200}
	rssGrowthRace = growth{factor: 2.5, slackMiB: 500}
)

// raceSysGrowth is how much go_memstats_sys_bytes may grow from the warm
// sample to the end in a -race build.
const raceSysGrowth = 1.25

type growth struct{ factor, slackMiB float64 }

func (g growth) exceeded(start, end float64) bool { return end > g.factor*start+g.slackMiB }

func leaks(pods []PodSeries, start, end time.Time, limitMiB float64, shared, race, warm bool) []string {
	var v []string
	if len(pods) == 0 {
		return []string{"Prometheus has no process_resident_memory_bytes for the controller in the run's window"}
	}
	for _, p := range pods {
		// A Pod that ran the whole window in one role: a standby that took
		// over the lead (a leader was killed) starts every controller.
		whole := p.From.Sub(start) < time.Minute && end.Sub(p.To) < time.Minute && p.LeaderStart == p.LeaderEnd
		if whole && !shared && p.GoroutinesEnd > math.Max(1.5*p.GoroutinesStart, p.GoroutinesStart+100) {
			v = append(v, fmt.Sprintf("%s: goroutines %.0f at the start, %.0f at the end (peak %.0f)",
				p.Pod, p.GoroutinesStart, p.GoroutinesEnd, p.GoroutinesMax))
		}
		at := p.WarmAt.UTC().Format(time.RFC3339)
		switch {
		case !whole || shared:
		case race && !warm:
			if rssGrowthRace.exceeded(p.RSSWarmMiB, p.RSSEndMiB) {
				v = append(v, fmt.Sprintf("%s: resident memory %.0f MiB at the start, %.0f MiB at the end (over %gx + %g MiB; peak %.0f)",
					p.Pod, p.RSSWarmMiB, p.RSSEndMiB, rssGrowthRace.factor, rssGrowthRace.slackMiB, p.RSSMaxMiB))
			}
		case race && (p.SysWarmMiB == 0 || p.HeapWarmMiB == 0):
			v = append(v, fmt.Sprintf("%s: no go_memstats_sys_bytes or go_memstats_heap_inuse_bytes at the warm baseline (%s): Go memory not measured", p.Pod, at))
		case race && p.SysEndMiB > raceSysGrowth*p.SysWarmMiB:
			v = append(v, fmt.Sprintf("%s: Go runtime memory (go_memstats_sys_bytes) %.0f MiB warm (%s), %.0f MiB at the end (over %gx)",
				p.Pod, p.SysWarmMiB, at, p.SysEndMiB, raceSysGrowth))
		case race && p.HeapAfterGCMiB == 0:
			v = append(v, fmt.Sprintf("%s: no garbage collection seen within 3 minutes of the load's end: heap after GC not measured", p.Pod))
		case race && p.HeapAfterGCMiB >= p.HeapWarmMiB:
			v = append(v, fmt.Sprintf("%s: heap in use %.0f MiB after a GC at the end, not below the warm %.0f MiB (%s)",
				p.Pod, p.HeapAfterGCMiB, p.HeapWarmMiB, at))
		case !race && rssGrowth.exceeded(p.RSSWarmMiB, p.RSSEndMiB):
			v = append(v, fmt.Sprintf("%s: resident memory %.0f MiB warm (%s), %.0f MiB at the end (over %gx + %g MiB; peak %.0f)",
				p.Pod, p.RSSWarmMiB, at, p.RSSEndMiB, rssGrowth.factor, rssGrowth.slackMiB, p.RSSMaxMiB))
		}
		if limitMiB > 0 && p.RSSMaxMiB > 0.9*limitMiB {
			v = append(v, fmt.Sprintf("%s: resident memory peaked at %.0f MiB, over 90%% of its %.0f MiB limit",
				p.Pod, p.RSSMaxMiB, limitMiB))
		}
	}
	return v
}
