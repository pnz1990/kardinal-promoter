//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	evals, _ := evaluationsAndTicks(t, e, ns, bundle, env, template, d)
	return evals
}

// evaluationsAndTicks is evaluations, and also collects the distinct
// status.tick values of every ScheduleClock in the cluster seen during d (the
// first of each clock is its tick when d starts).
func evaluationsAndTicks(t *testing.T, e *framework.Env, ns, bundle, env, template string,
	d time.Duration) (evals, ticks []time.Time) {
	t.Helper()
	lastTick := map[string]string{}
	framework.Consistently(t, d, "gate "+template+" and ScheduleClocks readable", func(ctx context.Context) (bool, string) {
		g, ok, err := e.GateInstance(ctx, ns, bundle, env, template)
		if err != nil || !ok {
			return false, fmt.Sprintf("gate lookup: ok=%v err=%v", ok, err)
		}
		if at := g.Status.LastEvaluatedAt; at != nil && (len(evals) == 0 || !evals[len(evals)-1].Equal(at.Time)) {
			evals = append(evals, at.Time)
		}
		var clocks v1alpha1.ScheduleClockList
		if err := e.Client.List(ctx, &clocks); err != nil {
			return ctx.Err() != nil, "list ScheduleClocks: " + err.Error() // d ending mid-list is fine
		}
		for _, c := range clocks.Items {
			key := c.Namespace + "/" + c.Name
			if c.Status.Tick == "" || lastTick[key] == c.Status.Tick {
				continue
			}
			lastTick[key] = c.Status.Tick
			at, err := time.Parse(time.RFC3339, c.Status.Tick)
			if err != nil {
				return false, fmt.Sprintf("ScheduleClock %s tick %q: %v", key, c.Status.Tick, err)
			}
			ticks = append(ticks, at)
		}
		return true, ""
	})
	return evals, ticks
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

// orgEnv is an environment name no other test uses. An org gate in the shared
// PolicyNamespace applies to every Pipeline in the cluster that has its
// environment, so each test's org gates name their own environment.
func orgEnv(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return "org-" + hex.EncodeToString(b)
}

// TestGate_OrgGatesMandatory checks that an org gate in platform-policies is
// injected into a Pipeline with its environment, and only that environment,
// and that the team cannot remove or weaken it: not with a passing team gate
// of the same name, not with a spec.policyNamespaces list that leaves
// platform-policies out, and not by deleting the instance (the Graph
// recreates it). The org gate alone decides when the environment promotes.
//
// Covers GATE-ORG-01.
func TestGate_OrgGatesMandatory(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	env := orgEnv(t)
	e.EnsureNamespace(t, framework.PolicyNamespace)
	org := framework.Gate(framework.PolicyNamespace, env+"-open", env, openExpr, recheck)
	org.Labels["kardinal.io/scope"] = "org"
	e.CreateGate(t, org)

	a := newArgoApp(t, e, "test", env)
	e.CreateGate(t, framework.Gate(a.ns, org.Name, env, "true", recheck))
	p := a.pipeline(nil)
	p.Spec.PolicyNamespaces = []string{e.Namespace(t)}
	a.apply(t, p)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	orgName := framework.GateInstanceName(framework.PolicyNamespace, org.Name, env, bundle)
	g := e.WaitGateNamed(t, a.ns, orgName, gateTimeout, "blocking", framework.Evaluated(false, "= false"))
	assert.Equal(t, "org", g.Labels["kardinal.io/scope"])
	e.WaitGateNamed(t, a.ns, framework.GateInstanceName(a.ns, org.Name, env, bundle), gateTimeout,
		"passing", framework.Evaluated(true, "= true"))
	onTest, err := e.GateInstances(ctx, a.ns, bundle, "test", org.Name)
	require.NoError(t, err)
	assert.Empty(t, onTest, "the org gate applies only to %s", env)
	e.NoStep(t, a.ns, pipelineName, bundle, env, holdFor)

	require.NoError(t, e.Client.Delete(ctx, g))
	e.WaitGateNamed(t, a.ns, orgName, 2*time.Minute, "recreated and blocking", func(n *v1alpha1.PolicyGate) bool {
		return n.UID != g.UID && framework.Evaluated(false, "= false")(n)
	})
	e.NoStep(t, a.ns, pipelineName, bundle, env, holdFor)
	assertEnvAt(t, a, env, fixtures.V1)

	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateNamed(t, a.ns, orgName, gateTimeout, "open", framework.Evaluated(true, "= true"))
	e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
	assertEnvAt(t, a, env, fixtures.V2)
}

// TestGate_TeamGatesScoped checks that a team gate applies only to its own
// team's Pipelines and only to the environment it names: team A's prod gate
// holds team A's prod, but not A's test and not team B's prod.
//
// Covers GATE-TEAM-01.
func TestGate_TeamGatesScoped(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	b := newArgoApp(t, e, "prod")
	e.CreateGate(t, framework.Gate(a.ns, "team-a-prod", "prod", openExpr, recheck))
	a.apply(t, a.pipeline(nil))
	b.apply(t, b.pipeline(nil))

	bundleA := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	bundleB := e.CreateBundle(t, b.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, b.ns, pipelineName, bundleB, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, b, "prod", fixtures.V2)
	var inB v1alpha1.PolicyGateList
	require.NoError(t, e.Client.List(ctx, &inB, client.InNamespace(b.ns)))
	assert.Empty(t, inB.Items, "team A's gate has no instance in team B's namespace")

	e.WaitStepState(t, a.ns, pipelineName, bundleA, "test", "Verified", promoteTimeout)
	onTest, err := e.GateInstances(ctx, a.ns, bundleA, "test", "team-a-prod")
	require.NoError(t, err)
	assert.Empty(t, onTest, "the gate applies only to prod")
	e.WaitGateReady(t, a.ns, bundleA, "prod", "team-a-prod", false, "= false", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, bundleA, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	e.SetBundleLabel(t, a.ns, bundleA, openLabel, "true")
	e.WaitStepState(t, a.ns, pipelineName, bundleA, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGate_PolicyNamespacesAddGates checks spec.policyNamespaces: a gate in a
// listed namespace holds the Pipeline's prod until it passes, and a gate in a
// namespace the Pipeline does not list is never applied, although it would
// block forever.
//
// Covers GATE-POLNS-01.
func TestGate_PolicyNamespacesAddGates(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	listed, unlisted := e.Namespace(t), e.Namespace(t)
	e.CreateGate(t, framework.Gate(listed, "shared-open", "prod", openExpr, recheck))
	e.CreateGate(t, framework.Gate(unlisted, "unlisted-never", "prod", "false", recheck))
	p := a.pipeline(nil)
	p.Spec.PolicyNamespaces = []string{listed}
	a.apply(t, p)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	shared := framework.GateInstanceName(listed, "shared-open", "prod", bundle)
	e.WaitGateNamed(t, a.ns, shared, gateTimeout, "blocking", framework.Evaluated(false, "= false"))
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)

	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateNamed(t, a.ns, shared, gateTimeout, "open", framework.Evaluated(true, "= true"))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
	never, err := e.GateInstances(context.Background(), a.ns, bundle, "prod", "unlisted-never")
	require.NoError(t, err)
	assert.Empty(t, never, "a namespace the Pipeline does not list adds no gate")
}

// TestGate_HoldsStepBeforeStart checks the pre-start gate check. A paused
// Pipeline lets the Graph create prod's step (its gate passes) but holds it
// Pending. The gate then turns false; after resume the step still does not
// start, and says which gate it waits for. Nothing is pushed and no PR opens.
// When the gate passes again the step starts on an evaluation no older than
// itself.
//
// Covers GATE-PREDEPLOY-01.
func TestGate_HoldsStepBeforeStart(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	e.CreateGate(t, framework.Gate(a.ns, "not-closed", "prod", `!("e2e-closed" in bundle.labels)`, recheck))
	a.apply(t, a.pipeline(nil))
	assert.Contains(t, e.MustKardinal(t, a.ns, "pause", pipelineName), "Pipeline "+pipelineName+" paused.")

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	gate := e.WaitGateReady(t, a.ns, bundle, "prod", "not-closed", true, "= true", gateTimeout)
	e.WaitStepMessage(t, a.ns, pipelineName, bundle, "prod", "Pending", "pipeline "+pipelineName+" is paused", time.Minute)
	e.SetBundleLabel(t, a.ns, bundle, "e2e-closed", "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", "not-closed", false, "= false", gateTimeout)

	assert.Contains(t, e.MustKardinal(t, a.ns, "resume", pipelineName), "Pipeline "+pipelineName+" resumed.")
	resumed := time.Now()
	waiting := "waiting for gate " + gate.Name
	e.WaitStepMessage(t, a.ns, pipelineName, bundle, "prod", "Pending", waiting, 90*time.Second)
	t.Logf("the held step saw the resume after %s", time.Since(resumed).Round(time.Second))
	e.StepHeld(t, a.ns, pipelineName, bundle, "prod", waiting, holdFor)
	assertEnvAt(t, a, "prod", fixtures.V1)
	prs, err := e.Git.PullRequests(ctx, a.repo)
	require.NoError(t, err)
	assert.Empty(t, prs)

	e.SetBundleLabel(t, a.ns, bundle, "e2e-closed", "")
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	g, ok, err := e.GateInstance(ctx, a.ns, bundle, "prod", "not-closed")
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, g.Status.LastEvaluatedAt.Before(&ps.CreationTimestamp),
		"the step started on an evaluation at or after its creation (%s)", ps.CreationTimestamp)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGate_RecheckInterval checks spec.recheckInterval: a gate asking for 1s
// is re-evaluated every 10s, the minimum, with no other event; a gate that
// sets none gets the 5m default. The periodic re-evaluation sees the label
// that opens the gate.
//
// Covers GATE-RECHECK-01.
func TestGate_RecheckInterval(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")
	e.CreateGate(t, framework.Gate(a.ns, "fast-recheck", "prod", openExpr, "1s"))
	defaulted := framework.Gate(a.ns, "default-recheck", "prod", "true", "")
	e.CreateGate(t, defaulted)
	assert.Equal(t, "5m", defaulted.Spec.RecheckInterval, "the API server defaults recheckInterval")
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitGateReady(t, a.ns, bundle, "prod", "fast-recheck", false, "= false", gateTimeout)
	d, ok, err := e.GateInstance(ctx, a.ns, bundle, "prod", "default-recheck")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "5m", d.Spec.RecheckInterval)

	// The window starts at an evaluation 5s after clockMu is ours: a fast
	// clock's last tick, just before another test released clockMu, may still
	// re-evaluate the gate first.
	clockMu.Lock()
	settled := time.Now().Add(5 * time.Second)
	e.WaitGate(t, a.ns, bundle, "prod", "fast-recheck", 30*time.Second, "evaluated after the clocks settled",
		func(g *v1alpha1.PolicyGate) bool {
			return g.Status.LastEvaluatedAt != nil && !g.Status.LastEvaluatedAt.Time.Before(settled)
		})
	evals, ticks := evaluationsAndTicks(t, e, a.ns, bundle, "prod", "fast-recheck", 35*time.Second)
	end := time.Now()
	clockMu.Unlock()
	// Each evaluation after the first comes at least 10s after the one before
	// (whole seconds, so also as written), or from a clock tick (only the
	// chart's once a minute: clockMu). A tick up to 2s before an evaluation can
	// re-run it: its watch event can arrive while that evaluation runs. Every
	// 1s would give over thirty.
	require.NotEmpty(t, evals, "the gate was evaluated after the clocks settled")
	var windowTicks int
	for _, tick := range ticks {
		if !tick.Before(evals[0].Add(-2*time.Second)) && !tick.After(end) {
			windowTicks++
		}
	}
	most := 1 + int(end.Sub(evals[0])/(10*time.Second)) + windowTicks
	t.Logf("evaluations %v to %s; clock ticks %v; at most %d", evals, end.Format(time.RFC3339), ticks, most)
	assert.GreaterOrEqual(t, len(evals), 3, "the gate is re-evaluated periodically")
	assert.LessOrEqual(t, len(evals), most, "1s is raised to the 10s minimum")
	var tenSecondGaps int
	for i := 1; i < len(evals); i++ {
		if gap := evals[i].Sub(evals[i-1]); gap >= 9*time.Second && gap <= 12*time.Second {
			tenSecondGaps++
		}
	}
	assert.GreaterOrEqual(t, tenSecondGaps, 2, "evaluations %v", evals)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", time.Second)

	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", "fast-recheck", true, "= true", 15*time.Second)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGate_FinishedBundlesNotEvaluated checks that the gates of finished
// Bundles stop being evaluated: a Verified Bundle whose Graph is ready, and a
// Superseded one. The newest Bundle's gate, with the same recheckInterval,
// keeps being evaluated over the same time, and promotes once opened.
//
// Covers GATE-FINISHED-01.
func TestGate_FinishedBundlesNotEvaluated(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	e.CreateGate(t, framework.Gate(a.ns, "open", "prod", openExpr, recheck))
	a.apply(t, a.pipeline(nil))

	verified := e.CreateBundleObject(t, &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Namespace: a.ns, Labels: map[string]string{openLabel: "true"}},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipelineName,
			Images: []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}},
	})
	e.WaitStepState(t, a.ns, pipelineName, verified, "prod", "Verified", promoteTimeout)
	framework.Eventually(t, 2*time.Minute, "Bundle "+verified+" Verified with GraphReady", func(ctx context.Context) (bool, string) {
		var b v1alpha1.Bundle
		if err := e.Client.Get(ctx, client.ObjectKey{Namespace: a.ns, Name: verified}, &b); err != nil {
			return false, err.Error()
		}
		return b.Status.Phase == "Verified" && meta.IsStatusConditionTrue(b.Status.Conditions, "GraphReady"),
			fmt.Sprintf("phase=%s conditions=%v", b.Status.Phase, b.Status.Conditions)
	})

	superseded := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V1)
	e.WaitGateReady(t, a.ns, superseded, "prod", "open", false, "= false", gateTimeout)
	latest := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitBundlePhase(t, a.ns, superseded, "Superseded", time.Minute)
	e.WaitGateReady(t, a.ns, latest, "prod", "open", false, "= false", gateTimeout)

	evaluated := func(ctx context.Context, bundle string) (time.Time, error) {
		g, ok, err := e.GateInstance(ctx, a.ns, bundle, "prod", "open")
		switch {
		case err != nil:
			return time.Time{}, err
		case !ok || g.Status.LastEvaluatedAt == nil:
			return time.Time{}, fmt.Errorf("gate of %s not evaluated", bundle)
		}
		return g.Status.LastEvaluatedAt.Time, nil
	}
	ctx := context.Background()
	frozen := map[string]time.Time{}
	for _, b := range []string{verified, superseded} {
		at, err := evaluated(ctx, b)
		require.NoError(t, err)
		frozen[b] = at
	}
	latestEvals := map[time.Time]bool{}
	framework.Consistently(t, 30*time.Second, "finished Bundles' gates not re-evaluated", func(ctx context.Context) (bool, string) {
		for b, want := range frozen {
			at, err := evaluated(ctx, b)
			if err != nil {
				return false, err.Error()
			}
			if !at.Equal(want) {
				return false, fmt.Sprintf("gate of %s evaluated again at %s (was %s)", b, at, want)
			}
		}
		at, err := evaluated(ctx, latest)
		if err != nil {
			return false, err.Error()
		}
		latestEvals[at] = true
		return true, ""
	})
	assert.GreaterOrEqual(t, len(latestEvals), 3, "the in-flight Bundle's gate kept being evaluated")

	e.SetBundleLabel(t, a.ns, latest, openLabel, "true")
	e.WaitStepState(t, a.ns, pipelineName, latest, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V3)
}

// createdByRE reads the user kardinal override records the override under.
var createdByRE = regexp.MustCompile(`(?m)^Created by: (\S+)$`)

// TestGate_OverridePassesStage checks an emergency override through the CLI:
// kardinal override on the blocked prod gate, with a reason and an expiry,
// makes the gate pass with a reason naming who overrode it, why and until
// when. prod then promotes, and its PR's Policy Gate Compliance table shows
// the override.
//
// Covers GATE-OVERRIDE-01.
func TestGate_OverridePassesStage(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	e.CreateGate(t, framework.Gate(a.ns, "hold", "prod", openExpr, recheck))
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	gate := e.WaitGateReady(t, a.ns, bundle, "prod", "hold", false, "= false", gateTimeout)
	e.NoStep(t, a.ns, pipelineName, bundle, "prod", holdFor)

	out := e.MustKardinal(t, a.ns, "override", pipelineName, "--stage", "prod", "--gate", "hold",
		"--reason", "INC-4521 hotfix", "--expires-in", "30m")
	assert.Contains(t, out, fmt.Sprintf("Override applied: gate=%s pipeline=%s stage=prod", gate.Name, pipelineName))
	m := createdByRE.FindStringSubmatch(out)
	require.Len(t, m, 2, "override output names its author:\n%s", out)
	overridden := e.WaitGateReady(t, a.ns, bundle, "prod", "hold", true, "OVERRIDDEN by "+m[1]+": INC-4521 hotfix", gateTimeout)
	require.Len(t, overridden.Spec.Overrides, 1)
	o := overridden.Spec.Overrides[0]
	assert.Equal(t, "prod", o.Stage)
	assert.WithinDuration(t, time.Now().Add(30*time.Minute), o.ExpiresAt.Time, 2*time.Minute)
	assert.Contains(t, overridden.Status.Reason, "(expires "+o.ExpiresAt.UTC().Format("2006-01-02T15:04Z")+")")

	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	assert.Regexp(t, `\| hold \| `+a.ns+` \| Pass \| OVERRIDDEN by `+regexp.QuoteMeta(m[1])+`: INC-4521 hotfix \(expires `, pr.Body)
	require.NoError(t, e.Git.MergePR(context.Background(), a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGate_OverrideLimits checks what an override does not do: an expired
// override and an override for another stage leave prod's gate blocked. A
// short override passes the gate only until it expires; the controller
// re-evaluates the gate right at expiry, although its recheckInterval is an
// hour. An override for every stage (no stage) releases prod.
//
// Covers GATE-OVERRIDE-02.
func TestGate_OverrideLimits(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	e.CreateGate(t, framework.Gate(a.ns, "hold", "prod", openExpr, "1h"))
	// test waits for its PR merge, so the Bundle stays in flight and prod is
	// not reached while the overrides come and go.
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	gate := e.WaitGateReady(t, a.ns, bundle, "prod", "hold", false, "= false", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "WaitingForMerge", promoteTimeout)

	patched := metav1.NewTime(time.Now().Truncate(time.Second))
	e.Override(t, gate, v1alpha1.PolicyGateOverride{Stage: "prod", Reason: "expired",
		CreatedBy: "e2e-expired", ExpiresAt: metav1.NewTime(time.Now().Add(-time.Minute))})
	e.Override(t, gate, v1alpha1.PolicyGateOverride{Stage: "staging", Reason: "other stage",
		CreatedBy: "e2e-other-stage", ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour))})
	e.WaitGate(t, a.ns, bundle, "prod", "hold", gateTimeout, "re-evaluated after the overrides", func(g *v1alpha1.PolicyGate) bool {
		return len(g.Spec.Overrides) == 2 && !g.Status.LastEvaluatedAt.Before(&patched)
	})
	framework.Consistently(t, 15*time.Second, "gate hold still blocked", func(ctx context.Context) (bool, string) {
		g, ok, err := e.GateInstance(ctx, a.ns, bundle, "prod", "hold")
		if err != nil || !ok {
			return false, fmt.Sprintf("gate lookup: ok=%v err=%v", ok, err)
		}
		return !g.Status.Ready && strings.Contains(g.Status.Reason, "= false"), framework.DescribeGate(g)
	})

	// No fast ScheduleClock may re-evaluate the gate for us around the expiry.
	clockMu.Lock()
	expires := time.Now().Add(20 * time.Second)
	e.Override(t, gate, v1alpha1.PolicyGateOverride{Stage: "prod", Reason: "short",
		CreatedBy: "e2e-short", ExpiresAt: metav1.NewTime(expires)})
	e.WaitGateReady(t, a.ns, bundle, "prod", "hold", true, "OVERRIDDEN by e2e-short: short", gateTimeout)
	after := e.WaitGateReady(t, a.ns, bundle, "prod", "hold", false, "= false", time.Minute)
	clockMu.Unlock()
	assert.WithinDuration(t, expires, after.Status.LastEvaluatedAt.Time, 5*time.Second,
		"re-evaluated at expiry, not at the next recheck or clock tick")

	e.Override(t, gate, v1alpha1.PolicyGateOverride{Reason: "every stage",
		CreatedBy: "e2e-all-stages", ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour))})
	e.WaitGateReady(t, a.ns, bundle, "prod", "hold", true, "OVERRIDDEN by e2e-all-stages: every stage", gateTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "test PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// gateAudit lists the GateEvaluated AuditEvents of the Bundle's gate for env,
// oldest first.
func gateAudit(ctx context.Context, e *framework.Env, ns, bundle, env, gate string) ([]v1alpha1.AuditEvent, error) {
	var list v1alpha1.AuditEventList
	if err := e.Client.List(ctx, &list, client.InNamespace(ns), client.MatchingLabels{
		"kardinal.io/bundle": bundle, "kardinal.io/environment": env,
		"kardinal.io/action": "GateEvaluated", "kardinal.io/gate": gate,
	}); err != nil {
		return nil, err
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Spec.Timestamp.Before(&list.Items[j].Spec.Timestamp)
	})
	return list.Items, nil
}

// outcomes is the Outcome of each event.
func outcomes(events []v1alpha1.AuditEvent) []string {
	out := make([]string, len(events))
	for i, ae := range events {
		out[i] = ae.Spec.Outcome
	}
	return out
}

// TestGate_AuditsEvaluations checks the gate audit trail: one GateEvaluated
// AuditEvent for the first evaluation and one per readiness flip, none for
// rechecks with the same result, each carrying the reason; Blocked and
// Allowed Kubernetes Events on the gate; and kardinal get auditevents lists
// them.
//
// Covers GATE-AUDIT-01.
func TestGate_AuditsEvaluations(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	e.CreateGate(t, framework.Gate(a.ns, "audited", "prod", openExpr, recheck))
	// test waits for its PR merge so the gate can flip without prod starting.
	a.apply(t, a.pipeline(map[string]string{"test": "pr-review"}))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)

	waitAudit := func(want ...string) []v1alpha1.AuditEvent {
		t.Helper()
		var got []v1alpha1.AuditEvent
		framework.Eventually(t, gateTimeout, fmt.Sprintf("audit outcomes %v", want), func(ctx context.Context) (bool, string) {
			evs, err := gateAudit(ctx, e, a.ns, bundle, "prod", "audited")
			if err != nil {
				return false, err.Error()
			}
			got = evs
			return assert.ObjectsAreEqual(want, outcomes(evs)), fmt.Sprintf("%v", outcomes(evs))
		})
		return got
	}
	blocked := e.WaitGateReady(t, a.ns, bundle, "prod", "audited", false, "= false", gateTimeout)
	evs := waitAudit("Failure")
	assert.Equal(t, blocked.Status.Reason, evs[0].Spec.Message)
	assert.Equal(t, pipelineName, evs[0].Spec.PipelineName)
	framework.Consistently(t, holdFor, "no audit record for rechecks", func(ctx context.Context) (bool, string) {
		evs, err := gateAudit(ctx, e, a.ns, bundle, "prod", "audited")
		if err != nil {
			return false, err.Error()
		}
		return len(evs) == 1, fmt.Sprintf("%v", outcomes(evs))
	})

	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", "audited", true, "= true", gateTimeout)
	evs = waitAudit("Failure", "Success")
	assert.Contains(t, evs[1].Spec.Message, "= true")
	e.SetBundleLabel(t, a.ns, bundle, openLabel, "")
	e.WaitGateReady(t, a.ns, bundle, "prod", "audited", false, "= false", gateTimeout)
	waitAudit("Failure", "Success", "Failure")
	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", "audited", true, "= true", gateTimeout)
	waitAudit("Failure", "Success", "Failure", "Success")

	var events corev1.EventList
	require.NoError(t, e.Client.List(ctx, &events, client.InNamespace(a.ns)))
	reasons := map[string]string{}
	for _, ev := range events.Items {
		if ev.InvolvedObject.Kind == "PolicyGate" && ev.InvolvedObject.Name == blocked.Name {
			reasons[ev.Reason] = ev.Type
		}
	}
	assert.Equal(t, map[string]string{"Blocked": corev1.EventTypeWarning, "Allowed": corev1.EventTypeNormal}, reasons)

	out := e.MustKardinal(t, a.ns, "get", "auditevents", "--pipeline", pipelineName, "--bundle", bundle, "--env", "prod")
	var rows []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "GateEvaluated") {
			rows = append(rows, strings.Fields(line)[5])
		}
	}
	assert.Equal(t, []string{"Success", "Failure", "Success", "Failure"}, rows, "newest first")

	pr := e.WaitPR(t, a.repo, time.Minute, "test PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)
}

// TestGate_InvalidGatesRejected checks that the API server refuses the
// PolicyGates the controller cannot honour and says what to do instead: a
// spec.selector, and a name over 63 characters. A 63-character name is
// accepted.
//
// Covers GATE-REJECT-01.
func TestGate_InvalidGatesRejected(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)

	for name, sel := range map[string]*metav1.LabelSelector{
		"with-selector":  {MatchLabels: map[string]string{"app": "podinfo"}},
		"empty-selector": {},
	} {
		g := framework.Gate(ns, name, "prod", "true", "")
		g.Spec.Selector = sel //nolint:staticcheck // the test checks the CRD refuses the deprecated field
		err := e.Client.Create(ctx, g)
		require.Error(t, err, name)
		assert.True(t, apierrors.IsInvalid(err), "%s: %v", name, err)
		assert.Contains(t, err.Error(), "spec.selector is not implemented; use the kardinal.io/applies-to label", name)
	}

	long := framework.Gate(ns, strings.Repeat("g", 64), "prod", "true", "")
	err := e.Client.Create(ctx, long)
	require.Error(t, err)
	assert.True(t, apierrors.IsInvalid(err), "%v", err)
	assert.Contains(t, err.Error(), "PolicyGate names are at most 63 characters")

	require.NoError(t, e.Client.Create(ctx, framework.Gate(ns, strings.Repeat("g", 63), "prod", "true", "")))
}

// TestGate_MessageShownWhenBlocked checks that a blocking gate's
// spec.message, its "human-readable explanation shown when gate blocks"
// (docs/policy-gates.md), reaches the user: the gate's status and Ready
// condition, kardinal explain, kardinal status and the held step's message.
// The CEL result stays in the reason after the message. A step exists only
// once its gates pass, so a paused Pipeline holds prod's step while the gate
// closes, as in TestGate_HoldsStepBeforeStart.
//
// Covers GATE-MESSAGE-01.
func TestGate_MessageShownWhenBlocked(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "prod")
	const (
		msg  = "Production is closed while the bundle has the e2e-closed label"
		expr = `!("e2e-closed" in bundle.labels)`
	)
	g := framework.Gate(a.ns, "explained", "prod", expr, recheck)
	g.Spec.Message = msg
	e.CreateGate(t, g)
	a.apply(t, a.pipeline(nil))
	assert.Contains(t, e.MustKardinal(t, a.ns, "pause", pipelineName), "Pipeline "+pipelineName+" paused.")
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	open := e.WaitGateReady(t, a.ns, bundle, "prod", "explained", true, expr+" = true", gateTimeout)
	assert.NotContains(t, open.Status.Reason, msg, "a passing gate's reason has no message")
	e.WaitStepMessage(t, a.ns, pipelineName, bundle, "prod", "Pending", "pipeline "+pipelineName+" is paused", time.Minute)

	e.SetBundleLabel(t, a.ns, bundle, "e2e-closed", "true")
	gate := e.WaitGateReady(t, a.ns, bundle, "prod", "explained", false, expr+" = false", gateTimeout)
	assert.Equal(t, msg, gate.Spec.Message, "the instance carries the template's message")
	assert.Equal(t, msg+" (bundle.version="+fixtures.V2+": "+expr+" = false)", gate.Status.Reason, "status.reason")
	if c := meta.FindStatusCondition(gate.Status.Conditions, "Ready"); assert.NotNil(t, c) {
		assert.Equal(t, gate.Status.Reason, c.Message, "Ready condition")
	}
	assert.Contains(t, e.MustKardinal(t, a.ns, "resume", pipelineName), "Pipeline "+pipelineName+" resumed.")
	e.WaitStepMessage(t, a.ns, pipelineName, bundle, "prod", "Pending", "waiting for gate "+gate.Name+": "+msg, 90*time.Second)
	state, row, ok := e.ExplainGate(t, a.ns, pipelineName, "prod", "explained")
	assert.True(t, ok && state == "Block", "kardinal explain shows the gate blocking: %s", row)
	assert.Contains(t, row, gate.Status.Reason, "kardinal explain")
	assert.Contains(t, e.MustKardinal(t, a.ns, "status", pipelineName), msg, "kardinal status")
}

// TestGate_LongTemplateNamesRejected checks that the 63-character limit on
// PolicyGate names has no loophole. A name over 63 characters is refused
// whether or not it looks like the names kardinal gives the gates it creates
// (instances contain "--", freeze gates start with "freeze-"). A 63-character
// template still works, although the instance the Graph makes from it has a
// longer name. A user gate that claims to be kardinal's (spec.generated) may
// have a long name, but it is never used as a template, so it holds nothing.
//
// Covers GATE-REJECT-02.
func TestGate_LongTemplateNamesRejected(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "prod")

	for _, name := range []string{
		"team--" + strings.Repeat("g", 60),
		"freeze-" + strings.Repeat("g", 60),
		"team-" + strings.Repeat("g", 60),
	} {
		err := e.Client.Create(ctx, framework.Gate(a.ns, name, "prod", "true", ""), client.DryRunAll)
		if assert.Error(t, err, "%s is accepted", name) {
			assert.True(t, apierrors.IsInvalid(err), "%s: %v", name, err)
			assert.Contains(t, err.Error(), "PolicyGate names are at most 63 characters", name)
		}
	}

	long := strings.Repeat("g", 63)
	e.CreateGate(t, framework.Gate(a.ns, long, "prod", openExpr, recheck))
	claimed := framework.Gate(a.ns, "team--"+strings.Repeat("c", 60), "prod", "false", recheck)
	claimed.Spec.Generated = true
	e.CreateGate(t, claimed)
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)

	gate := e.WaitGateReady(t, a.ns, bundle, "prod", long, false, openExpr+" = false", gateTimeout)
	assert.Greater(t, len(gate.Name), 63, "the instance name %s is over 63 characters", gate.Name)
	e.SetBundleLabel(t, a.ns, bundle, openLabel, "true")
	e.WaitGateReady(t, a.ns, bundle, "prod", long, true, openExpr+" = true", gateTimeout)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, a, "prod", fixtures.V2)

	var instances v1alpha1.PolicyGateList
	require.NoError(t, e.Client.List(ctx, &instances, client.InNamespace(a.ns), client.MatchingLabels{
		"kardinal.io/bundle": bundle, "kardinal.io/environment": "prod"}))
	names := make([]string, 0, len(instances.Items))
	for _, g := range instances.Items {
		names = append(names, g.Name)
	}
	assert.Equal(t, []string{gate.Name}, names, "prod has only the instance of the 63-character template")
}
