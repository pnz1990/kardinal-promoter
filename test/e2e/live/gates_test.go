//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// Most gate tests open a gate by labelling the Bundle: openExpr is false until
// the Bundle has the openLabel label. Gates read labels at evaluation time,
// and the PolicyGate reconciler does not watch Bundles, so a label change is
// seen at the next recheck.
const (
	openLabel = "e2e-open"
	openExpr  = `"e2e-open" in bundle.labels`
	// recheck is the documented minimum recheckInterval (docs/policy-gates.md).
	recheck = "10s"
	// gateTimeout bounds a gate re-evaluation: one recheck plus reconcile
	// latency, with room for a loaded runner.
	gateTimeout = time.Minute
	// holdFor is how long a closed gate must keep an environment back. It is
	// longer than two rechecks, so the gate was re-evaluated while it held.
	holdFor = 25 * time.Second
)

// assertEnvAt checks what users see for env: the version in git and the
// running image.
func assertEnvAt(t *testing.T, a *app, env, version string) {
	t.Helper()
	assert.Contains(t, a.e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path(env)+"/kustomization.yaml"),
		"newTag: "+version, "%s: git has %s", env, version)
	assert.Equal(t, fixtures.Image+":"+version, a.e.DeploymentImage(t, a.ns, fixtures.Workload(env)),
		"%s: %s is running", env, version)
}

// TestGate_ExpressionHoldsEnvironment checks the basic gate contract: a team
// gate on prod whose CEL expression is false keeps prod from starting (no
// PromotionStep, git and the Deployment unchanged, explain shows Block). Once
// the expression is true, prod promotes. The gate's reason names the Bundle
// version and the result.
//
// Covers GATE-CEL-01.
func TestGate_ExpressionHoldsEnvironment(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	e.CreateGate(t, framework.Gate(a.ns, "needs-open-label", "prod", openExpr, recheck))
	a.apply(t, a.pipeline(nil))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	assertEnvAt(t, a, "test", fixtures.V2)

	e.WaitGateReady(t, a.ns, bundle, "prod", "needs-open-label", false,
		"bundle.version="+fixtures.V2+": "+openExpr+" = false", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)
	row := e.WaitExplainGate(t, a.ns, pipelineName, "prod", "needs-open-label", "Block", 10*time.Second)
	assert.Contains(t, row, "= false", "explain shows the gate's reason")

	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", "needs-open-label", true,
		"bundle.version="+fixtures.V2+": "+openExpr+" = true", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	e.WaitExplainGate(t, a.ns, pipelineName, "prod", "needs-open-label", "Pass", 10*time.Second)
}

// TestGate_BadExpressionsFailClosed checks that a gate whose expression does
// not compile, returns a non-bool, or fails to evaluate blocks, with the error
// in status.reason. The template shows a syntax error too. The two runtime
// failures are data-dependent: once the Bundle is labelled they evaluate to
// true. The one that does not compile keeps blocking until it is overridden.
//
// Covers GATE-CEL-02.
func TestGate_BadExpressionsFailClosed(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	gates := map[string]struct{ expr, blocked string }{
		"bad-syntax": {`bundle.version ==`, "CEL compile error: "},
		"non-bool": {openExpr + ` ? true : bundle.version`,
			fmt.Sprintf(`returned non-boolean: string(%s)`, fixtures.V2)},
		"eval-error": {openExpr + ` || bundle.labels["missing"] == "x"`, "CEL evaluation error: no such key: missing"},
	}
	for name, g := range gates {
		e.CreateGate(t, framework.Gate(a.ns, name, "prod", g.expr, recheck))
	}
	framework.Eventually(t, gateTimeout, "the template to report its syntax error", func(ctx context.Context) (bool, string) {
		var g v1alpha1.PolicyGate
		if err := e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: "bad-syntax"}, &g); err != nil {
			return false, err.Error()
		}
		return strings.HasPrefix(g.Status.Reason, "CEL syntax error: ") && !g.Status.Ready, framework.DescribeGate(&g)
	})
	a.apply(t, a.pipeline(nil))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	for name, g := range gates {
		e.WaitGateReady(t, a.ns, bundle, "prod", name, false, g.blocked, gateTimeout)
	}
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	// The label makes the two runtime failures true; the compile error stays.
	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", "non-bool", true, "= true", gateTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "eval-error", true, "= true", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	bad := e.WaitGateReady(t, a.ns, bundle, "prod", "bad-syntax", false, "CEL compile error: ", gateTimeout)

	e.Override(t, bad, v1alpha1.PolicyGateOverride{
		Reason: "e2e: release the bad expression", Stage: "prod", CreatedBy: "e2e-oncall",
		ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour)),
	})
	e.WaitGateReady(t, a.ns, bundle, "prod", "bad-syntax", true, "OVERRIDDEN by e2e-oncall", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// runawayExpr is a gate expression that exceeds the CEL cost limit (six
// nested comprehensions, a million iterations) unless the Bundle has
// openLabel, in which case || short-circuits it.
var runawayExpr = func() string {
	l := "[0, 1, 2, 3, 4, 5, 6, 7, 8, 9]"
	return fmt.Sprintf(`%s || %s.all(a, %s.all(b, %s.all(c, %s.all(d, %s.all(e, %s.all(f, true))))))`,
		openExpr, l, l, l, l, l, l)
}()

// TestGate_RunawayExpressionFailsClosed checks the documented evaluation
// limits: an expression over the cost limit fails to evaluate and the gate
// blocks with a CEL evaluation error, instead of tying up the controller.
// The same gate passes once the Bundle's label short-circuits the expensive
// part.
//
// Covers GATE-CEL-03.
func TestGate_RunawayExpressionFailsClosed(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	e.CreateGate(t, framework.Gate(a.ns, "runaway", "prod", runawayExpr, recheck))
	a.apply(t, a.pipeline(nil))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitGateReady(t, a.ns, bundle, "prod", "runaway", false,
		"CEL evaluation error: operation cancelled: actual cost limit exceeded", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", "runaway", true, "= true", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGate_LibraryFunctions checks that the documented kro library functions
// evaluate in the controller: JSON, map merge, the list functions and the
// seeded random functions (whose values for a seed are fixed), plus the
// string extensions. Each gate is false until the Bundle is labelled, and then
// true only if its functions return what docs/reference/cel-context.md says.
//
// Covers GATE-LIB-01.
func TestGate_LibraryFunctions(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	gates := map[string]string{
		"lib-json": `json.unmarshal('{"tier": "gold", "n": 2}').tier == "gold" && json.marshal({"a": 1}) == '{"a":1}'`,
		"lib-maps": `{"a": "1"}.merge({"b": "2"}) == {"a": "1", "b": "2"} && bundle.labels.merge({"region": "eu"}).region == "eu"`,
		"lib-lists": `lists.setAtIndex([1, 2, 3], 0, 9) == [9, 2, 3] && lists.insertAtIndex([1, 2], 1, 5) == [1, 5, 2] && ` +
			`lists.removeAtIndex([1, 2, 3], 0) == [2, 3]`,
		// The values for seed V2 are the ones the evaluator returns offline:
		// the same seed gives the same value in every process.
		"lib-random": `random.seededInt(0, 100, bundle.version) == 58 && random.seededString(8, bundle.version) == "bif273tv" && ` +
			`random.seededString(8, "other") != "bif273tv"`,
		"lib-strings": fmt.Sprintf(`bundle.version.split(".").size() == 3 && "v%%s".format([bundle.version]) == "v%s" && `+
			`"E2E".lowerAscii() == "e2e"`, fixtures.V2),
	}
	for name, expr := range gates {
		e.CreateGate(t, framework.Gate(a.ns, name, "prod", openExpr+" && "+expr, recheck))
	}
	a.apply(t, a.pipeline(nil))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	for name := range gates {
		e.WaitGateReady(t, a.ns, bundle, "prod", name, false, "= false", gateTimeout)
	}
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)

	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	for name := range gates {
		e.WaitGateReady(t, a.ns, bundle, "prod", name, true, "= true", gateTimeout)
	}
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGate_BundleAttributes checks that the documented bundle and
// environment attributes carry the Bundle's values: type, version, labels,
// provenance, intent and the environment name. A gate that expects other
// values blocks; the gate with the Bundle's values passes, and prod promotes
// once the mismatching gate is released.
//
// Covers GATE-ATTR-01.
func TestGate_BundleAttributes(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	match := fmt.Sprintf(`bundle.type == "image" && bundle.version == %q && bundle.labels.team == "payments" && `+
		`bundle.provenance.author == "e2e-author" && bundle.provenance.commitSHA == "0123abc" && `+
		`bundle.provenance.ciRunURL == "https://ci.example/run/7" && bundle.intent.targetEnvironment == "prod" && `+
		`environment.name == "prod"`, fixtures.V2)
	e.CreateGate(t, framework.Gate(a.ns, "attrs-match", "prod", match, recheck))
	// attrs-mismatch expects another author until the Bundle says it is open.
	e.CreateGate(t, framework.Gate(a.ns, "attrs-mismatch", "prod",
		`bundle.provenance.author == "someone-else" || `+openExpr, recheck))
	a.apply(t, a.pipeline(nil))

	bundle := e.CreateBundleObject(t, &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Labels: map[string]string{"team": "payments"}},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: pipelineName,
			Images:   []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}},
			Provenance: &v1alpha1.BundleProvenance{
				Author: "e2e-author", CommitSHA: "0123abc", CIRunURL: "https://ci.example/run/7",
			},
			Intent: &v1alpha1.BundleIntent{TargetEnvironment: "prod"},
		},
	})
	e.WaitGateReady(t, a.ns, bundle, "prod", "attrs-match", true, "= true", gateTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "attrs-mismatch", false, "= false", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)

	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", "attrs-mismatch", true, "= true", gateTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "attrs-match", true, "= true", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// scheduleMatch is a CEL expression that is true at t and at every time up to
// ten minutes later: the schedule attributes (UTC day and hour) of both ends.
func scheduleMatch(t time.Time) string {
	var clauses []string
	for _, at := range []time.Time{t.UTC(), t.UTC().Add(10 * time.Minute)} {
		weekend := at.Weekday() == time.Saturday || at.Weekday() == time.Sunday
		c := fmt.Sprintf(`(schedule.dayOfWeek == %q && schedule.hour == %d && schedule.isWeekend == %v)`,
			at.Weekday().String(), at.Hour(), weekend)
		if len(clauses) == 0 || clauses[0] != c {
			clauses = append(clauses, c)
		}
	}
	return strings.Join(clauses, " || ")
}

// TestGate_ScheduleAttributes checks that schedule.dayOfWeek, schedule.hour
// and schedule.isWeekend are the controller's current UTC day and hour: a
// gate that allows only the current hour passes, and a gate that forbids it
// blocks prod until the Bundle is labelled.
//
// Covers GATE-SCHED-01.
func TestGate_ScheduleAttributes(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	now := scheduleMatch(time.Now())
	e.CreateGate(t, framework.Gate(a.ns, "sched-now", "prod", now, recheck))
	e.CreateGate(t, framework.Gate(a.ns, "sched-not-now", "prod", "!("+now+") || "+openExpr, recheck))
	a.apply(t, a.pipeline(nil))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitGateReady(t, a.ns, bundle, "prod", "sched-now", true, "= true", gateTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "sched-not-now", false, "= false", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", "sched-not-now", true, "= true", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// clockMu serializes the tests that run a fast ScheduleClock or count gate
// evaluations. Every ScheduleClock tick re-evaluates every gate instance in
// the cluster, so a fast clock would inflate another test's count.
var clockMu sync.Mutex

// tickTimes collects the distinct status.tick values of a ScheduleClock seen
// during d.
func tickTimes(t *testing.T, e *framework.Env, ns, name string, d time.Duration) []time.Time {
	t.Helper()
	var ticks []time.Time
	framework.Consistently(t, d, "ScheduleClock "+name+" readable", func(ctx context.Context) (bool, string) {
		var c v1alpha1.ScheduleClock
		if err := e.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &c); err != nil {
			return false, err.Error()
		}
		if c.Status.Tick == "" {
			return true, ""
		}
		at, err := time.Parse(time.RFC3339, c.Status.Tick)
		if err != nil {
			return false, fmt.Sprintf("tick %q: %v", c.Status.Tick, err)
		}
		if len(ticks) == 0 || !ticks[len(ticks)-1].Equal(at) {
			ticks = append(ticks, at)
		}
		return true, ""
	})
	return ticks
}

// evaluations collects the distinct status.lastEvaluatedAt values of a gate
// instance seen during d.
func evaluations(t *testing.T, e *framework.Env, ns, bundle, env, template string, d time.Duration) []time.Time {
	t.Helper()
	var seen []time.Time
	framework.Consistently(t, d, "gate "+template+" readable", func(ctx context.Context) (bool, string) {
		g, ok, err := e.GateInstance(ctx, ns, bundle, env, template)
		if err != nil || !ok {
			return false, fmt.Sprintf("gate lookup: ok=%v err=%v", ok, err)
		}
		if at := g.Status.LastEvaluatedAt; at != nil && (len(seen) == 0 || !seen[len(seen)-1].Equal(at.Time)) {
			seen = append(seen, at.Time)
		}
		return true, ""
	})
	return seen
}

// TestGate_ScheduleClockTicks checks the ScheduleClock contract: the chart's
// kardinal-clock ticks every minute; a clock asking for 1s ticks no faster
// than every 5s; and each tick re-evaluates gate instances with no other
// event. The gate here has a one-hour recheckInterval and the PolicyGate
// reconciler does not watch Bundles, so only ticks can see the label that
// opens it.
//
// Covers GATE-CLOCK-01.
func TestGate_ScheduleClockTicks(t *testing.T) {
	t.Parallel()
	e := framework.New(t)

	var chartClock v1alpha1.ScheduleClock
	require.NoError(t, e.Client.Get(context.Background(),
		client.ObjectKey{Namespace: framework.ControllerNamespace, Name: "kardinal-clock"}, &chartClock))
	assert.Equal(t, "1m", chartClock.Spec.Interval, "the chart's clock ticks every minute")
	framework.Eventually(t, 2*time.Minute, "kardinal-clock to tick", func(ctx context.Context) (bool, string) {
		var c v1alpha1.ScheduleClock
		if err := e.Client.Get(ctx, client.ObjectKeyFromObject(&chartClock), &c); err != nil {
			return false, err.Error()
		}
		return c.Status.Tick != chartClock.Status.Tick, "tick " + c.Status.Tick
	})

	a := newArgoApp(t, e, "prod")
	e.CreateGate(t, framework.Gate(a.ns, "on-tick", "prod", openExpr+" && schedule.hour >= 0", "1h"))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitGateReady(t, a.ns, bundle, "prod", "on-tick", false, "= false", gateTimeout)

	clockMu.Lock()
	defer clockMu.Unlock()
	fast := &v1alpha1.ScheduleClock{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Name: "fast"},
		Spec:       v1alpha1.ScheduleClockSpec{Interval: "1s"},
	}
	require.NoError(t, e.Client.Create(context.Background(), fast))
	// Deleted before clockMu is released (defers run last in, first out).
	defer func() { assert.NoError(t, e.Client.Delete(context.Background(), fast)) }()

	ticks := tickTimes(t, e, a.ns, "fast", 32*time.Second)
	evals := evaluations(t, e, a.ns, bundle, "prod", "on-tick", 20*time.Second)
	t.Logf("fast clock ticks %v; gate evaluations %v", ticks, evals)
	require.GreaterOrEqual(t, len(ticks), 4, "a 1s clock still ticks: %v", ticks)
	assert.LessOrEqual(t, len(ticks), 8, "a 1s clock ticks at most every 5s: %v", ticks)
	for i := 1; i < len(ticks); i++ {
		assert.GreaterOrEqual(t, ticks[i].Sub(ticks[i-1]), 5*time.Second, "ticks %v", ticks)
	}
	// A 1h recheckInterval would give no evaluation in 20s; the 5s ticks give
	// about four.
	assert.GreaterOrEqual(t, len(evals), 2, "ticks re-evaluate the gate: %v", evals)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", 5*time.Second)

	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", "on-tick", true, "= true", 20*time.Second)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// upstreamStatus is the Bundle's status for env.
func upstreamStatus(t *testing.T, e *framework.Env, ns, bundle, env string) v1alpha1.EnvironmentStatus {
	t.Helper()
	var b v1alpha1.Bundle
	require.NoError(t, e.Client.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: bundle}, &b))
	for _, s := range b.Status.Environments {
		if s.Name == env {
			return s
		}
	}
	t.Fatalf("Bundle %s has no status for %s: %+v", bundle, env, b.Status.Environments)
	return v1alpha1.EnvironmentStatus{}
}

// TestGate_UpstreamSoak checks bundle.upstreamSoakMinutes: prod waits until
// test has been Verified for a minute, then promotes. The soak is the time
// since test's health check passed.
//
// Covers GATE-SOAK-01.
func TestGate_UpstreamSoak(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	e.CreateGate(t, framework.Gate(a.ns, "soak", "prod", "bundle.upstreamSoakMinutes >= 1", recheck))
	a.apply(t, a.pipeline(nil))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "soak", false, "bundle.upstreamSoakMinutes >= 1 = false", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	// The Bundle reconciler updates soakMinutes about once a minute, so the
	// gate opens one to two minutes after test's health check.
	g := e.WaitGateReady(t, a.ns, bundle, "prod", "soak", true, "= true", 3*time.Minute)
	test := upstreamStatus(t, e, a.ns, bundle, "test")
	require.NotNil(t, test.HealthCheckedAt)
	assert.GreaterOrEqual(t, g.Status.LastEvaluatedAt.Sub(test.HealthCheckedAt.Time), time.Minute,
		"the gate opened a minute or more after test was Verified")
	assert.GreaterOrEqual(t, test.SoakMinutes, int64(1))

	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.GreaterOrEqual(t, ps.CreationTimestamp.Sub(test.HealthCheckedAt.Time), time.Minute,
		"prod started after the soak")
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGate_UpstreamHistory checks the cross-stage history attributes: prod
// requires two recent test successes, so the first Bundle waits at prod; the
// second, whose test promotion is the second success, goes through.
//
// Covers GATE-UPSTREAM-01.
func TestGate_UpstreamHistory(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	expr := `upstream.test.recentSuccessCount >= 2 && upstream.test.recentFailureCount == 0 && ` +
		`timestamp(upstream.test.lastPromotedAt) > timestamp("2026-01-01T00:00:00Z")`
	e.CreateGate(t, framework.Gate(a.ns, "test-history", "prod", expr, recheck))
	a.apply(t, a.pipeline(nil))

	first := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, first, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, first, "prod", "test-history", false, "= false", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, first, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	second := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitStepState(t, a.ns, pipelineName, second, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, second, "prod", "test-history", true, "= true", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, second, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V3)
	e.WaitBundlePhase(t, a.ns, first, "Superseded", time.Minute)
}

// waitPRApproved waits until the PRStatus of the Bundle's env PR records
// approvals approving reviews.
func waitPRApproved(t *testing.T, e *framework.Env, ns, bundle, env string, approvals int) {
	t.Helper()
	framework.Eventually(t, 2*time.Minute, "PRStatus of "+bundle+"/"+env+" approved", func(ctx context.Context) (bool, string) {
		var list v1alpha1.PRStatusList
		if err := e.Client.List(ctx, &list, client.InNamespace(ns),
			client.MatchingLabels{"kardinal.io/bundle": bundle, "kardinal.io/environment": env}); err != nil {
			return false, err.Error()
		}
		if len(list.Items) != 1 {
			return false, fmt.Sprintf("%d PRStatus objects", len(list.Items))
		}
		st := list.Items[0].Status
		return st.Approved && st.ApprovalCount == approvals, fmt.Sprintf("approved=%v count=%d", st.Approved, st.ApprovalCount)
	})
}

// TestGate_PRReviewAttributes checks bundle.pr: prod requires an approved
// staging PR. A staging PR merged without a review promotes staging but not
// prod; once a later Bundle's staging PR is approved by another user before
// the merge, prod promotes.
//
// Covers GATE-PR-01.
func TestGate_PRReviewAttributes(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	reviewer, ok := e.Git.(gitserver.Reviewer)
	require.True(t, ok, "the suite's git server %T cannot review PRs", e.Git)
	a := newArgoApp(t, e, "staging", "prod")
	e.CreateGate(t, framework.Gate(a.ns, "staging-approved", "prod",
		`"staging" in bundle.pr && bundle.pr["staging"].isApproved && bundle.pr["staging"].approvalCount >= 1`, recheck))
	a.apply(t, a.pipeline(map[string]string{"staging": "pr-review"}))
	ctx := context.Background()

	first := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, first, "staging", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "first staging PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, first, "staging", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, first, "prod", "staging-approved", false, "= false", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, first, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	second := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitStepState(t, a.ns, pipelineName, second, "staging", "WaitingForMerge", promoteTimeout)
	pr = e.WaitPR(t, a.repo, time.Minute, "second staging PR", func(p gitserver.PR) bool {
		return p.State == "open" && p.Number != pr.Number
	})
	require.NoError(t, reviewer.ApprovePR(ctx, a.repo, pr.Number, "e2e: approved"))
	waitPRApproved(t, e, a.ns, second, "staging", 1)
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, second, "staging", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, second, "prod", "staging-approved", true, "= true", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, second, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V3)
}
