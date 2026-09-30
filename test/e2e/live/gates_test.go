//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
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
