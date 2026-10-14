// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package policygate

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// The fuzz and property tests of the PolicyGate CEL context (#1837):
// buildContext, over Bundles, MetricChecks, PRStatuses, ChangeWindows and
// times decoded from fuzz bytes, never panics, returns every variable the
// docs list (AGENTS.md, docs/reference/cel-context.md) with its documented
// type, and a fixed set of well-typed expressions evaluates without error
// against it.

type ctxSource struct {
	data []byte
	pos  int
}

func (s *ctxSource) byte() byte {
	if s.pos >= len(s.data) {
		return 0
	}
	b := s.data[s.pos]
	s.pos++
	return b
}

func (s *ctxSource) intn(n int) int {
	if n <= 1 {
		return 0
	}
	return int(s.byte()) % n
}

func (s *ctxSource) bool() bool { return s.byte()&1 == 1 }

func (s *ctxSource) int64() int64 {
	var v int64
	for i := 0; i < 8; i++ {
		v = v<<8 | int64(s.byte())
	}
	return v
}

// str returns a string of up to 24 fuzz bytes, made valid UTF-8 as the API
// server's JSON keeps it.
func (s *ctxSource) str() string {
	n := s.intn(25)
	b := make([]byte, n)
	for i := range b {
		b[i] = s.byte()
	}
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

func (s *ctxSource) pick(xs []string) string { return xs[s.intn(len(xs))] }

var (
	ctxEnvNames    = []string{"test", "uat", "staging", "prod", "prod-eu", "canary"}
	ctxMetricNames = []string{"error-rate", "p99-latency", "success-rate", "a", "x-1"}
	ctxWindowNames = []string{"freeze", "business-hours", "q4"}
	ctxResults     = []string{"Pass", "Fail", "", "Stale", "weird"}
	ctxPhases      = []string{"Verified", "Failed", "Promoting", "", "Superseded"}
	ctxHours       = []string{"09:00-17:00", "22:00-02:00", "00:00-24:00", "bad", ""}
	ctxDays        = []string{"Monday", "Saturday", "Sunday", "Funday"}
)

// fuzzContextFixture decodes data into a reconciler over a fake cluster, a
// gate and the bundle name to evaluate it for.
func fuzzContextFixture(t *testing.T, data []byte) (*Reconciler, *kardinalv1alpha1.PolicyGate, time.Time, *kardinalv1alpha1.Bundle) {
	t.Helper()
	s := &ctxSource{data: data}
	const ns = "team-a"
	// Any time from 1970 to about 2106, sometimes in a zone far from UTC.
	now := time.Unix(int64(uint32(s.int64())), 0).UTC()
	if loc := s.intn(3); loc == 1 {
		now = now.In(time.FixedZone("plus14", 14*3600))
	}

	pipeline := &kardinalv1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: ns}}
	for _, e := range ctxEnvNames {
		pipeline.Spec.Environments = append(pipeline.Spec.Environments, kardinalv1alpha1.EnvironmentSpec{Name: e})
	}
	labels := map[string]string{}
	for i := 0; i < s.intn(4); i++ {
		labels[s.pick([]string{"hotfix", "release-type", "kardinal.io/rollback", "team"})] = s.str()
	}
	bundle := &kardinalv1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: "app-v1", Namespace: ns, Labels: labels,
			CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
		Spec: kardinalv1alpha1.BundleSpec{Type: s.pick([]string{"image", "config", "mixed", ""}), Pipeline: "app"},
	}
	if s.bool() {
		bundle.Annotations = map[string]string{lifecycle.AnnotationCreatedBy: s.str()}
	}
	if s.bool() {
		bundle.Spec.Images = []kardinalv1alpha1.ImageRef{{Repository: "ghcr.io/org/app", Tag: s.str(), Digest: s.str()}}
	}
	if s.bool() {
		bundle.Spec.Provenance = &kardinalv1alpha1.BundleProvenance{Author: s.str(), CommitSHA: s.str(), CIRunURL: s.str()}
	}
	if s.bool() {
		bundle.Spec.Intent = &kardinalv1alpha1.BundleIntent{TargetEnvironment: s.pick(ctxEnvNames)}
	}
	for i := 0; i < s.intn(5); i++ {
		st := kardinalv1alpha1.EnvironmentStatus{Name: s.pick(ctxEnvNames), Phase: s.pick(ctxPhases), SoakMinutes: s.int64()}
		if s.bool() {
			at := metav1.NewTime(now.Add(-time.Duration(s.intn(1000)) * time.Minute))
			st.HealthCheckedAt = &at
		}
		bundle.Status.Environments = append(bundle.Status.Environments, st)
	}
	objs := []runtime.Object{pipeline, bundle}
	// Older Bundles of the Pipeline feed upstream.* history.
	for i := 0; i < s.intn(4); i++ {
		old := &kardinalv1alpha1.Bundle{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("app-old-%d", i), Namespace: ns,
				CreationTimestamp: metav1.NewTime(now.Add(-time.Duration(2+i) * time.Hour))},
			Spec: kardinalv1alpha1.BundleSpec{Type: "image", Pipeline: "app"},
			Status: kardinalv1alpha1.BundleStatus{Environments: []kardinalv1alpha1.EnvironmentStatus{
				{Name: s.pick(ctxEnvNames), Phase: s.pick(ctxPhases), SoakMinutes: s.int64()}}},
		}
		objs = append(objs, old)
	}
	for i := 0; i < s.intn(5); i++ {
		mc := &kardinalv1alpha1.MetricCheck{
			ObjectMeta: metav1.ObjectMeta{Name: s.pick(ctxMetricNames) + fmt.Sprint(i), Namespace: ns},
			Spec:       kardinalv1alpha1.MetricCheckSpec{PerPromotion: s.intn(4) == 0},
			Status:     kardinalv1alpha1.MetricCheckStatus{LastValue: s.str(), Result: s.pick(ctxResults)},
		}
		switch s.intn(3) {
		case 1:
			v := metav1.NewTime(now.Add(time.Minute))
			mc.Status.ValidUntil = &v
		case 2:
			v := metav1.NewTime(now.Add(-time.Minute))
			mc.Status.ValidUntil = &v
		}
		objs = append(objs, mc)
	}
	for i := 0; i < s.intn(3); i++ {
		objs = append(objs, &kardinalv1alpha1.PRStatus{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("app-v1-pr-%d", i), Namespace: ns,
				Labels: map[string]string{labelBundle: "app-v1", labelEnvironment: s.pick(ctxEnvNames)}},
			Status: kardinalv1alpha1.PRStatusStatus{Open: s.bool(), Approved: s.bool(), ApprovalCount: s.intn(5)},
		})
	}
	windows := map[string]bool{}
	for i := 0; i < s.intn(4); i++ {
		name := s.pick(ctxWindowNames)
		if windows[name] {
			continue
		}
		windows[name] = true
		cw := &kardinalv1alpha1.ChangeWindow{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if s.bool() {
			start := now.Add(time.Duration(s.intn(200)-100) * time.Hour)
			cw.Spec = kardinalv1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.NewTime(start),
				End: metav1.NewTime(start.Add(time.Duration(s.intn(200)-50) * time.Hour))}
		} else {
			sched := &kardinalv1alpha1.ChangeWindowSchedule{AllowedHours: s.pick(ctxHours),
				Timezone: s.pick([]string{"", "UTC", "Europe/Paris", "Not/AZone"})}
			if s.bool() {
				sched.AllowedDays = []string{s.pick(ctxDays), s.pick(ctxDays)}
			}
			cw.Spec = kardinalv1alpha1.ChangeWindowSpec{Type: s.pick([]string{"recurring", "bogus"}), Schedule: sched}
		}
		objs = append(objs, cw)
	}

	scheme := runtime.NewScheme()
	require.NoError(t, kardinalv1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).
		WithIndex(&kardinalv1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	r, err := NewReconciler(c)
	require.NoError(t, err)
	r.NowFn = func() time.Time { return now }

	// The Graph creates gates only for the environments the Bundle promotes:
	// with a targetEnvironment, those up to it (the Pipeline is linear).
	promoted := ctxEnvNames
	if in := bundle.Spec.Intent; in != nil {
		for i, e := range ctxEnvNames {
			if e == in.TargetEnvironment {
				promoted = ctxEnvNames[:i+1]
			}
		}
	}
	env := s.pick(promoted)
	// The expression decides which optional parts buildContext computes.
	expr := "true"
	if s.bool() {
		expr += ` && !changewindow.isBlocked("freeze")`
	}
	if s.bool() {
		expr += " && bundle.upstreamSoakMinutes >= 0"
	}
	gate := &kardinalv1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "gate", Namespace: ns,
			Labels: map[string]string{labelPipeline: "app", labelEnvironment: env, labelBundle: "app-v1"}},
		Spec: kardinalv1alpha1.PolicyGateSpec{Expression: expr},
	}
	return r, gate, now, bundle
}

// checkContextProperty is the property of FuzzCELContext and
// TestCELContext_Property.
func checkContextProperty(t *testing.T, data []byte) {
	t.Helper()
	r, gate, now, bundle := fuzzContextFixture(t, data)
	var (
		celCtx map[string]interface{}
		err    error
	)
	require.NotPanics(t, func() { celCtx, _, err = r.buildContext(t.Context(), gate, "app-v1") })
	require.NoError(t, err, "with the Pipeline and Bundle present, building the context does not fail")

	// The documented variables and their types.
	b := mapOf(t, celCtx, "bundle")
	for _, k := range []string{"type", "version", "createdBy"} {
		assert.IsType(t, "", b[k], "bundle.%s", k)
	}
	assert.IsType(t, map[string]interface{}{}, b["labels"], "bundle.labels")
	prov := mapOf(t, b, "provenance")
	for _, k := range []string{"author", "commitSHA", "ciRunURL"} {
		assert.IsType(t, "", prov[k], "bundle.provenance.%s", k)
	}
	if p := bundle.Spec.Provenance; p != nil {
		assert.Equal(t, p.Author, prov["author"])
		assert.Equal(t, p.CommitSHA, prov["commitSHA"])
	}
	assert.IsType(t, "", mapOf(t, b, "intent")["targetEnvironment"])
	for env, v := range mapOf(t, b, "pr") {
		pr, ok := v.(map[string]interface{})
		require.True(t, ok, "bundle.pr[%q]", env)
		assert.IsType(t, true, pr["isApproved"])
	}
	if strings.Contains(gate.Spec.Expression, "upstreamSoakMinutes") {
		assert.IsType(t, int64(0), b["upstreamSoakMinutes"])
	}

	sched := mapOf(t, celCtx, "schedule")
	require.IsType(t, true, sched["isWeekend"])
	require.IsType(t, 0, sched["hour"])
	require.IsType(t, "", sched["dayOfWeek"])
	hour := sched["hour"].(int)
	assert.True(t, hour >= 0 && hour < 24, "schedule.hour %d", hour)
	// schedule.* is UTC (docs/reference/cel-context.md), whatever zone the
	// clock's time is in.
	assert.Equal(t, now.UTC().Weekday().String(), sched["dayOfWeek"])
	assert.Equal(t, now.UTC().Hour(), hour)
	weekend := sched["dayOfWeek"] == "Saturday" || sched["dayOfWeek"] == "Sunday"
	assert.Equal(t, weekend, sched["isWeekend"], "schedule.isWeekend agrees with schedule.dayOfWeek")

	assert.Equal(t, gate.Labels[labelEnvironment], mapOf(t, celCtx, "environment")["name"])

	metrics := mapOf(t, celCtx, "metrics")
	for name, v := range metrics {
		m, ok := v.(map[string]interface{})
		require.True(t, ok, "metrics[%q]", name)
		assert.IsType(t, "", m["value"], "metrics[%q].value", name)
		assert.IsType(t, "", m["result"], "metrics[%q].result", name)
		assert.IsType(t, true, m["stale"], "metrics[%q].stale", name)
	}
	upstream := mapOf(t, celCtx, "upstream")
	for name, v := range upstream {
		u, ok := v.(map[string]interface{})
		require.True(t, ok, "upstream[%q]", name)
		assert.IsType(t, int64(0), u["soakMinutes"], "upstream[%q].soakMinutes", name)
		for _, k := range []string{"recentSuccessCount", "recentFailureCount"} {
			if x, has := u[k]; has {
				assert.IsType(t, int64(0), x, "upstream[%q].%s", name, k)
			}
		}
		if x, has := u["lastPromotedAt"]; has {
			assert.IsType(t, "", x, "upstream[%q].lastPromotedAt", name)
		}
	}
	for name, v := range mapOf(t, celCtx, "changewindow") {
		assert.IsType(t, true, v, "changewindow[%q]", name)
	}
	approvals := mapOf(t, celCtx, "approvals")
	assert.IsType(t, int64(0), approvals["count"])
	assert.IsType(t, int64(0), approvals["required"])
	assert.IsType(t, true, approvals["rejected"])

	// Well-typed expressions evaluate without error. Each is true by
	// construction, so a wrong value fails too.
	exprs := []string{
		`type(bundle.labels) == map`,
		`schedule.hour >= 0 && schedule.hour < 24`,
		`schedule.isWeekend == (schedule.dayOfWeek in ["Saturday", "Sunday"])`,
		`bundle.version == bundle.version && size(bundle.provenance.author) >= 0`,
		`bundle.intent.targetEnvironment == "" || size(bundle.intent.targetEnvironment) > 0`,
		`environment.name == "` + gate.Labels[labelEnvironment] + `"`,
		`json.marshal(bundle) != "" && json.marshal(upstream) != "" && json.marshal(metrics) != ""`,
		`json.marshal(schedule) != "" && json.marshal(changewindow) != "" && json.marshal(approvals) != ""`,
		`approvals.count >= 0 && approvals.required >= 0`,
		`random.seededInt(0, 100, bundle.version) >= 0`,
	}
	for name := range metrics {
		exprs = append(exprs, fmt.Sprintf(`metrics[%q].result == metrics[%q].result && type(metrics[%q].stale) == bool`, name, name, name))
	}
	for name := range upstream {
		exprs = append(exprs, fmt.Sprintf(`type(upstream[%q].soakMinutes) == int`, name))
	}
	for name := range mapOf(t, celCtx, "changewindow") {
		exprs = append(exprs, fmt.Sprintf(`changewindow.isBlocked(%q) || !changewindow.isBlocked(%q)`, name, name))
	}
	for _, expr := range exprs {
		var pass bool
		require.NotPanics(t, func() { pass, _, err = r.eval.evaluate(t.Context(), expr, celCtx) }, expr)
		require.NoError(t, err, expr)
		assert.True(t, pass, expr)
	}
}

func mapOf(t *testing.T, m map[string]interface{}, key string) map[string]interface{} {
	t.Helper()
	v, ok := m[key].(map[string]interface{})
	require.True(t, ok, "%s is a map, got %T", key, m[key])
	return v
}

// FuzzCELContext fuzzes buildContext with checkContextProperty. Run it with
// go test ./pkg/reconciler/policygate -run '^$' -fuzz '^FuzzCELContext$' -fuzztime 2m.
func FuzzCELContext(f *testing.F) {
	for _, seed := range contextSeeds() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) { checkContextProperty(t, data) })
}

func contextSeeds() [][]byte {
	return [][]byte{
		{},
		{0x65, 0x5c, 0x1f, 0, 0, 0, 0, 0, 1, 3, 1, 2, 1, 1, 1, 1},
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 2, 3, 0, 4, 0xff, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		[]byte("a Saturday in the window, with metrics and history: \x00\x01\x02\x03\x04"),
	}
}

// TestCELContext_Property runs checkContextProperty over 300 seeded random
// inputs and the fuzz seeds. Covers GATE-FUZZ-01.
func TestCELContext_Property(t *testing.T) {
	inputs := contextSeeds()
	r := rand.New(rand.NewSource(1837))
	for i := 0; i < 300; i++ {
		b := make([]byte, 16+r.Intn(200))
		r.Read(b)
		inputs = append(inputs, b)
	}
	for i, in := range inputs {
		in := in
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			checkContextProperty(t, in)
		})
	}
}
