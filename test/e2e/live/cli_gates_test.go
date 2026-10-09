//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// The tests in this file read and steer gates with the CLI: explain, status,
// override, policy list and policy simulate.

// explainHeader is the header of kardinal explain's table.
var explainHeader = []string{"ENVIRONMENT", "BUNDLE", "TYPE", "NAME", "STATE", "EXPRESSION", "REASON"}

// ansi matches an ANSI color code.
var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// header is the cells of out's first line.
func header(out string) []string {
	return cliCellGap.Split(strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]), -1)
}

// explainTable asserts kardinal explain's header and returns its rows.
func explainTable(t *testing.T, out string) []map[string]string {
	t.Helper()
	require.Equal(t, explainHeader, header(out), "explain header in:\n%s", out)
	return framework.ParseTable(out)
}

// explainDeployed is what explain prints after its table: the "deployed:"
// lines, or "".
func explainDeployed(out string) string {
	_, after, _ := strings.Cut(out, "\n\n")
	return after
}

// gateRow is one gate row of kardinal explain.
func gateRow(env, bundle, name, state, expr, reason string) map[string]string {
	return map[string]string{"ENVIRONMENT": env, "BUNDLE": bundle, "TYPE": "PolicyGate", "NAME": name,
		"STATE": state, "EXPRESSION": expr, "REASON": reason}
}

// stepRow is one step row of kardinal explain: a Step's reason is its
// status.message, "-" when it has none.
func stepRow(ps *v1alpha1.PromotionStep) map[string]string {
	reason := ps.Status.Message
	if reason == "" {
		reason = "-"
	}
	return map[string]string{"ENVIRONMENT": ps.Spec.Environment, "BUNDLE": ps.Spec.BundleName, "TYPE": "Step",
		"NAME": ps.Spec.StepType, "STATE": ps.Status.State, "EXPRESSION": "-", "REASON": reason}
}

// statusSections splits kardinal status <pipeline> into the paragraphs
// before its tables and its titled tables (title → the table under the
// rule).
func statusSections(out string) (paras []string, tables map[string]string) {
	rule := strings.Repeat("─", 72)
	tables = map[string]string{}
	for _, part := range strings.Split(strings.TrimSuffix(out, "\n"), "\n\n") {
		lines := strings.SplitN(part, "\n", 3)
		if len(lines) >= 2 && lines[1] == rule {
			body := ""
			if len(lines) == 3 {
				body = lines[2] + "\n"
			}
			tables[lines[0]] = body
			continue
		}
		paras = append(paras, part)
	}
	return paras, tables
}

// stripAges deletes column col from rows after checking it matches re.
func stripAges(t *testing.T, rows []map[string]string, col string, re *regexp.Regexp) []map[string]string {
	t.Helper()
	for _, r := range rows {
		assert.Regexp(t, re, r[col], "%s of %v", col, r)
		delete(r, col)
	}
	return rows
}

var (
	ageRE = regexp.MustCompile(`^\d+[smhd]$`)
	agoRE = regexp.MustCompile(`^\d+[smhd] ago$`)
)

// truncated is s cut to 35 runes with "...", as status shows a gate's
// reason.
func truncated(s string) string {
	r := []rune(s)
	if len(r) <= 35 {
		return s
	}
	return string(r[:32]) + "..."
}

// policyRow is one row of kardinal policy list.
func policyRow(name, ns, scope, appliesTo, recheck, cel string) map[string]string {
	return map[string]string{"NAME": name, "NAMESPACE": ns, "SCOPE": scope, "APPLIES-TO": appliesTo,
		"RECHECK": recheck, "CEL": cel, "LAST-EVALUATED": "-"}
}

// policyRows is the rows of kardinal policy list in the namespaces nss, in
// output order.
func policyRows(t *testing.T, out string, nss ...string) []map[string]string {
	t.Helper()
	require.Equal(t, []string{"NAME", "NAMESPACE", "SCOPE", "APPLIES-TO", "RECHECK", "CEL", "LAST-EVALUATED"},
		header(out), "policy list header in:\n%s", out)
	var rows []map[string]string
	for _, r := range framework.ParseTable(out) {
		for _, ns := range nss {
			if r["NAMESPACE"] == ns {
				rows = append(rows, r)
			}
		}
	}
	return rows
}

// TestCLI_GatesExplainOverride follows one Bundle through two gates with
// explain, status, override and policy list.
//
// Before any Bundle, explain says there is no promotion yet (for the
// pipeline and for one environment) and rejects an unknown environment or
// Pipeline, status shows the controller's version and "No active
// promotions.", and policy list shows the gate templates: every one in every
// namespace, or with --pipeline the ones attached to the pipeline (with
// --policy-namespaces, org gates from those namespaces too), with their
// scope, environment, recheck interval and CEL check, never a gate instance;
// once the Bundle's gates are evaluated, it shows when for this namespace's
// gates and not for the org gate of the same name.
//
// The Bundle's test gate holds it (Block; status lists it as blocking, its
// reason cut to 35 characters) while prod's gate, not reached yet, is
// Waiting with the gate's message and the expression's result. explain
// --watch redraws until Ctrl-C, and shows the test gate pass once kardinal
// override records an override on that Bundle's instance (not the
// template), with who, why and the expiry; override rejects missing or empty
// flags, a bad duration, a pipeline that does not exist, an unknown gate and
// an instance of another stage or pipeline. With test Verified, prod's gate is Block and status shows its
// message whole; --color colors only the STATE cell and NO_COLOR turns it
// off. Once prod's step waits for its PR, status marks it active with the
// PR, and prod's gate, closed again, is Waiting. A newer Bundle supersedes
// it; once that one is deleted explain shows the gate Superseded and the
// step Failed with why, and override refuses a gate whose only instances
// belong to finished Bundles.
//
// Covers CLI-EXPLAIN-01, CLI-EXPLAIN-02, CLI-STATUS-01, CLI-OVERRIDE-01, CLI-POLICY-LIST-01.
func TestCLI_GatesExplainOverride(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	user := whoAmI(t, e) // override records the authenticated user (#1450)
	a := newArgoApp(t, e, "test", "prod")
	ns := a.ns
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	const holdMsg = "prod needs the e2e-open label"
	e.CreateGate(t, framework.Gate(ns, "entry", "test", openExpr, recheck))
	hold := framework.Gate(ns, "hold", "prod", openExpr, recheck)
	hold.Spec.Message = holdMsg
	e.CreateGate(t, hold)
	// A second namespace: an org gate of the same name for test, and a
	// template with no labels and a broken expression.
	ns2 := e.Namespace(t)
	org := framework.Gate(ns2, "entry", "test", openExpr, "")
	org.Labels["kardinal.io/scope"] = "org"
	e.CreateGate(t, org)
	e.CreateGate(t, &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: ns2, Name: "stray"},
		Spec: v1alpha1.PolicyGateSpec{Expression: "bundle.labels["}})
	waitPipelineValid(t, e, ns, pipelineName)

	// No Bundle yet.
	assert.Equal(t, "No promotion for pipeline \"podinfo\" yet\n", c.Must(ns, "explain", pipelineName))
	assert.Equal(t, "No promotion for \"prod\" in pipeline \"podinfo\" yet\n", c.Must(ns, "explain", pipelineName, "--env", "prod"))
	refuses(t, c, ns, `environment "staging" not found in pipeline "podinfo" (environments: test, prod)`,
		"explain", pipelineName, "--env", "staging")
	refuses(t, c, ns, fmt.Sprintf("pipeline %q not found in namespace %q", "nope", ns), "explain", "nope")

	summary := regexp.MustCompile(`^Controller:  (\S+)\nPipelines:   (\d+)(?: \((\d+) degraded: [^)\n]+\))?\n` +
		`Bundles:     (\d+) \((\d+) active\)\n(?:\nWarning: (\d+) pipeline\(s\) Degraded — run 'kardinal get pipelines' for details\n)?$`)
	out := c.Must(ns, "status")
	m := summary.FindStringSubmatch(out)
	require.NotNil(t, m, "status output:\n%s", out)
	assert.Equal(t, controllerVersion(t, e), m[1])
	n, _ := strconv.Atoi(m[2])
	assert.GreaterOrEqual(t, n, 1, "the cluster has this test's Pipeline")
	assert.Equal(t, m[3], m[6], "the warning counts the degraded pipelines")
	assert.Regexp(t, `^Controller:  unknown\n`, c.Must(ns, "status", "--controller-namespace", ns), "no kardinal-version ConfigMap there")
	assert.Equal(t, "Pipeline: podinfo   Namespace: "+ns+"\n\nNo active promotions.\n", c.Must(ns, "status", pipelineName))
	refuses(t, c, ns, fmt.Sprintf("pipeline %q not found in namespace %q", "nope", ns), "status", "nope")

	// policy list: wait for the controller's CEL check of every template.
	var list string
	framework.Eventually(t, time.Minute, "policy list shows the CEL check of the four templates", func(context.Context) (bool, string) {
		list = c.Must(ns, "policy", "list")
		rows := policyRows(t, list, ns, ns2)
		for _, r := range rows {
			if r["CEL"] == "-" {
				return false, list
			}
		}
		return len(rows) == 4, list
	})
	want := []map[string]string{
		policyRow("entry", ns, "team", "test", recheck, "valid"),
		policyRow("entry", ns2, "org", "test", "5m", "valid"),
		policyRow("hold", ns, "team", "prod", recheck, "valid"),
		policyRow("stray", ns2, "team", "-", "5m", "invalid"),
	}
	if ns2 < ns {
		want[0], want[1] = want[1], want[0]
	}
	assert.Equal(t, want, policyRows(t, list, ns, ns2), "every template in every namespace, by name then namespace")
	out = c.Must(ns, "policy", "list", "--pipeline", pipelineName)
	assert.Equal(t, []map[string]string{policyRow("entry", ns, "team", "test", recheck, "valid"), policyRow("hold", ns, "team", "prod", recheck, "valid")},
		framework.ParseTable(out), "the gates attached to the pipeline:\n%s", out)
	out = c.Must(ns, "policy", "list", "--pipeline", pipelineName, "--policy-namespaces", ns2)
	assert.Equal(t, want[:3], framework.ParseTable(out), "with the org gates of %s:\n%s", ns2, out)
	refuses(t, c, ns, fmt.Sprintf("pipeline %q not found in namespace %q", "nope", ns), "policy", "list", "--pipeline", "nope")

	// B1: test's gate holds it; prod's gate waits.
	b1 := e.CreateBundle(t, ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	blocked := "bundle.version=" + fixtures.V2 + ": " + openExpr + " = false"
	entry := e.WaitGateReady(t, ns, b1, "test", "entry", false, blocked, gateTimeout)
	e.WaitGateReady(t, ns, b1, "prod", "hold", false, holdMsg+" ("+blocked+")", gateTimeout)
	out = c.Must(ns, "explain", pipelineName)
	assert.Equal(t, []map[string]string{
		gateRow("prod", b1, "hold", "Waiting", openExpr, holdMsg+" ("+blocked+")"),
		gateRow("test", b1, "entry", "Block", openExpr, blocked),
	}, explainTable(t, out), out)
	assert.Equal(t, "prod   deployed: none\ntest   deployed: none\n", explainDeployed(out))
	out = c.Must(ns, "explain", pipelineName, "--env", "test")
	assert.Equal(t, []map[string]string{gateRow("test", b1, "entry", "Block", openExpr, blocked)}, explainTable(t, out), out)
	assert.Equal(t, "test   deployed: none\n", explainDeployed(out))

	out = c.Must(ns, "status", pipelineName)
	paras, tables := statusSections(out)
	assert.Equal(t, []string{"Pipeline: podinfo   Namespace: " + ns, "Active bundle(s): " + b1}, paras, out)
	assert.Equal(t, "ENVIRONMENT  REGION  BUNDLE  STATE  ACTIVE STEP  PR  AGE\n", tables["Promotion Steps"], "no step yet:\n%s", out)
	assert.Equal(t, []map[string]string{{"ENVIRONMENT": "prod", "BUNDLE": "none"}, {"ENVIRONMENT": "test", "BUNDLE": "none"}},
		framework.ParseTable(tables["Deployed"]), out)
	assert.Equal(t, []string{"GATE", "ENV", "EXPRESSION", "REASON", "LAST CHECKED"}, header(tables["Blocking Policy Gates"]), out)
	assert.Equal(t, []map[string]string{{"GATE": "entry", "ENV": "test", "EXPRESSION": openExpr, "REASON": truncated(blocked)}},
		stripAges(t, framework.ParseTable(tables["Blocking Policy Gates"]), "LAST CHECKED", agoRE), "only the gate holding the Bundle:\n%s", out)

	// policy list never lists gate instances, but shows when they were last
	// evaluated: for this namespace's team gates, not for ns2's org entry (the
	// entry instances here are the team gate's).
	list = c.Must(ns, "policy", "list")
	rows := policyRows(t, list, ns, ns2)
	for _, r := range rows {
		if r["NAMESPACE"] == ns {
			assert.Regexp(t, agoRE, r["LAST-EVALUATED"], "%s in:\n%s", r["NAME"], list)
			r["LAST-EVALUATED"] = "-"
		}
	}
	assert.Equal(t, want, rows, "no gate instance:\n%s", list)

	// explain --watch redraws until Ctrl-C.
	watch := c.Start(framework.CLIOptions{}, c.Args(ns, "explain", pipelineName, "--env", "test", "--watch")...)
	watch.WaitStdout(30*time.Second, "a first frame", func(s string) bool { return len(frames(s)) >= 1 })

	// override refuses bad input and changes nothing.
	refuses(t, c, ns, `required flag(s) "gate", "reason" not set`, "override", pipelineName)
	refuses(t, c, ns, `required flag(s) "reason" not set`, "override", pipelineName, "--gate", "entry")
	for _, d := range []string{"soon", "-1h", "0s"} {
		refuses(t, c, ns, fmt.Sprintf("invalid --expires-in %q: must be a positive Go duration (e.g. 1h, 30m)", d),
			"override", pipelineName, "--gate", "entry", "--reason", "r", "--expires-in", d)
	}
	refuses(t, c, ns, fmt.Sprintf("get policygate nope: no gate instance or template of that name for pipeline podinfo in namespace %s: "+
		`policygates.kardinal.io "nope" not found`, ns), "override", pipelineName, "--gate", "nope", "--reason", "r")
	refuses(t, c, ns, fmt.Sprintf("policygate %s/%s is the instance for stage test, not prod", ns, entry.Name),
		"override", pipelineName, "--stage", "prod", "--gate", entry.Name, "--reason", "r")
	refuses(t, c, ns, `--reason is required for override (audit record)`, "override", pipelineName, "--gate", "entry", "--reason", "")
	refuses(t, c, ns, `--gate is required`, "override", pipelineName, "--gate", "", "--reason", "r")
	refuses(t, c, ns, fmt.Sprintf("pipeline \"other\" not found in namespace %q", ns),
		"override", "other", "--gate", entry.Name, "--reason", "r")
	other := barePipeline(t, e, ns, "other")
	refuses(t, c, ns, fmt.Sprintf("policygate %s/%s is an instance of pipeline podinfo, not other", ns, entry.Name),
		"override", "other", "--gate", entry.Name, "--reason", "r")
	require.NoError(t, e.Client.Delete(ctx, other))
	refuses(t, c, ns, fmt.Sprintf("no in-progress Bundle of pipeline podinfo has an instance of gate entry in namespace %s "+
		`(stage "prod"; 0 instance(s) of finished Bundles); an override applies to the instances a promoting Bundle creates, `+
		"so run it while the Bundle waits on the gate", ns), "override", pipelineName, "--stage", "prod", "--gate", "entry", "--reason", "r")
	g, ok, err := e.GateInstance(ctx, ns, b1, "test", "entry")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, g.Spec.Overrides, "a refused override records nothing")

	// override passes test's gate for B1.
	before := time.Now().UTC().Truncate(time.Second)
	out = c.Must(ns, "override", pipelineName, "--stage", "test", "--gate", "entry", "--reason", "e2e: hotfix", "--expires-in", "30m")
	after := time.Now().UTC()
	m = regexp.MustCompile(`^Override applied: gate=` + regexp.QuoteMeta(entry.Name) + ` pipeline=podinfo stage=test\n` +
		`Reason: e2e: hotfix\nExpires: (\S+) \(in 30m0s\)\nCreated by: ` + regexp.QuoteMeta(user) + `\n\n` +
		`The gate will pass immediately until the override expires\.\n$`).FindStringSubmatch(out)
	require.NotNil(t, m, "override output:\n%s", out)
	expires, err := time.Parse(time.RFC3339, m[1])
	require.NoError(t, err)
	assert.False(t, expires.Before(before.Add(30*time.Minute)) || expires.After(after.Add(30*time.Minute)), "expires %s", expires)
	g, ok, err = e.GateInstance(ctx, ns, b1, "test", "entry")
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, g.Spec.Overrides, 1)
	o := g.Spec.Overrides[0]
	assert.Equal(t, "e2e: hotfix", o.Reason)
	assert.Equal(t, "test", o.Stage)
	assert.Equal(t, user, o.CreatedBy)
	assert.True(t, o.ExpiresAt.Time.Equal(expires), "spec expiresAt %s, printed %s", o.ExpiresAt, expires)
	require.NotNil(t, o.CreatedAt)
	assert.False(t, o.CreatedAt.Time.Before(before) || o.CreatedAt.After(after), "createdAt %s", o.CreatedAt)
	var tmpl v1alpha1.PolicyGate
	require.NoError(t, e.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: "entry"}, &tmpl))
	assert.Empty(t, tmpl.Spec.Overrides, "the template is not overridden")
	overridden := fmt.Sprintf("OVERRIDDEN by %s: e2e: hotfix (expires %s)", user, expires.Format("2006-01-02T15:04Z"))
	e.WaitGateReady(t, ns, b1, "test", "entry", true, overridden, gateTimeout)

	// The watch shows the gate pass; Ctrl-C ends it with exit 0.
	stream := watch.WaitStdout(30*time.Second, "a frame with the gate passed", func(s string) bool {
		r := framework.TableRow(lastFrame(s), "NAME", "entry")
		return r != nil && r["STATE"] == "Pass"
	})
	fs := frames(stream)
	assert.Equal(t, "Block", framework.TableRow(fs[0], "NAME", "entry")["STATE"], "the first frame:\n%s", fs[0])
	assert.Equal(t, overridden, framework.TableRow(lastFrame(stream), "NAME", "entry")["REASON"])
	watch.Interrupt()
	r := watch.Wait(30 * time.Second)
	assert.Equal(t, 0, r.Code, "Ctrl-C ends --watch with exit 0")
	assert.Empty(t, r.Stderr)
	assert.True(t, strings.HasSuffix(r.Stdout, watchFooter), "every frame ends with the footer:\n%s", r.Stdout)
	for _, f := range frames(r.Stdout) {
		explainTable(t, f)
	}

	// Test is Verified; prod's gate now holds the Bundle.
	testStep := e.WaitStepState(t, ns, pipelineName, b1, "test", "Verified", promoteTimeout)
	assert.NotEmpty(t, testStep.Status.Message, "a Verified step says how its health check passed")
	assertEnvAt(t, a, "test", fixtures.V2)
	e.WaitExplainGate(t, ns, pipelineName, "prod", "hold", "Block", gateTimeout)
	out = c.Must(ns, "explain", pipelineName)
	assert.Equal(t, []map[string]string{
		gateRow("prod", b1, "hold", "Block", openExpr, holdMsg+" ("+blocked+")"),
		gateRow("test", b1, "entry", "Pass", openExpr, overridden),
		stepRow(testStep),
	}, explainTable(t, out), out)
	assert.Equal(t, "prod   deployed: none\n", explainDeployed(out), "test runs B1, so only prod gets a deployed line")

	out = c.Must(ns, "status", pipelineName)
	paras, tables = statusSections(out)
	assert.Equal(t, []string{"Pipeline: podinfo   Namespace: " + ns, "Active bundle(s): " + b1}, paras, out)
	assert.Equal(t, []string{"ENVIRONMENT", "REGION", "BUNDLE", "STATE", "ACTIVE STEP", "PR", "AGE"}, header(tables["Promotion Steps"]), out)
	assert.Equal(t, []map[string]string{{"ENVIRONMENT": "test", "REGION": "-", "BUNDLE": b1, "STATE": "Verified", "ACTIVE STEP": "-", "PR": "-"}},
		stripAges(t, framework.ParseTable(tables["Promotion Steps"]), "AGE", ageRE), out)
	assert.Equal(t, []map[string]string{{"ENVIRONMENT": "prod", "BUNDLE": "none"}, {"ENVIRONMENT": "test", "BUNDLE": b1 + " (" + fixtures.V2 + ")"}},
		framework.ParseTable(tables["Deployed"]), out)
	assert.Equal(t, []map[string]string{{"GATE": "hold", "ENV": "prod", "EXPRESSION": openExpr, "REASON": holdMsg}},
		stripAges(t, framework.ParseTable(tables["Blocking Policy Gates"]), "LAST CHECKED", agoRE), "the gate's message, whole:\n%s", out)

	// --color colors the STATE cell only; NO_COLOR wins.
	plain := c.Must(ns, "explain", pipelineName, "--env", "prod")
	assert.NotContains(t, plain, "\x1b[", "no color when stdout is not a terminal")
	colored := c.Must(ns, "explain", pipelineName, "--env", "prod", "--color")
	lines := strings.Split(colored, "\n")
	assert.Equal(t, strings.Index(lines[0], "STATE"), strings.Index(lines[1], "\x1b[31mBlock\x1b[0m"), "Block, red, in the STATE column:\n%q", colored)
	assert.Len(t, ansi.FindAllString(colored, -1), 2, "one colored cell:\n%q", colored)
	assert.Equal(t, plain, ansi.ReplaceAllString(colored, ""))
	colored = c.Must(ns, "explain", pipelineName, "--env", "test", "--color")
	assert.Contains(t, colored, "\x1b[32mPass\x1b[0m")
	assert.Contains(t, colored, "\x1b[32mVerified\x1b[0m")
	nc := c.Exec(framework.CLIOptions{Env: []string{"NO_COLOR=1"}}, c.Args(ns, "explain", pipelineName, "--env", "prod", "--color")...)
	require.Equal(t, 0, nc.Code, nc.Output())
	assert.Equal(t, plain, nc.Stdout, "NO_COLOR overrides --color")

	// Prod's step waits for its PR: status marks it active. Closing the gate
	// again leaves it Waiting: it no longer holds the Bundle.
	e.SetBundleLabel(t, ns, b1, openLabel, "true")
	prodStep := e.WaitStepState(t, ns, pipelineName, b1, "prod", "WaitingForMerge", promoteTimeout)
	pr := openPR(t, e, a.repo, fixtures.V2)
	assert.Equal(t, pr.URL, prodStep.Status.PRURL)
	active := "-"
	for _, s := range prodStep.Status.Steps {
		if s.State != "Completed" && s.State != "Failed" && s.State != "" {
			active = s.Name
			break
		}
	}
	prCell := pr.URL
	if len(prCell) > 40 {
		prCell = prCell[len(prCell)-40:]
	}
	out = c.Must(ns, "status", pipelineName)
	_, tables = statusSections(out)
	assert.Equal(t, []map[string]string{
		{"ENVIRONMENT": "▶ prod", "REGION": "-", "BUNDLE": b1, "STATE": "WaitingForMerge", "ACTIVE STEP": active, "PR": prCell},
		{"ENVIRONMENT": "test", "REGION": "-", "BUNDLE": b1, "STATE": "Verified", "ACTIVE STEP": "-", "PR": "-"},
	}, stripAges(t, framework.ParseTable(tables["Promotion Steps"]), "AGE", ageRE), out)
	assert.NotContains(t, tables, "Blocking Policy Gates", out)
	e.SetBundleLabel(t, ns, b1, openLabel, "")
	e.WaitGateReady(t, ns, b1, "prod", "hold", false, holdMsg+" ("+blocked+")", gateTimeout)
	e.WaitExplainGate(t, ns, pipelineName, "prod", "hold", "Waiting", gateTimeout)

	// B2 supersedes B1; with B2 deleted, explain shows B1's gate Superseded.
	b2 := e.CreateBundle(t, ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitBundlePhase(t, ns, b1, "Superseded", time.Minute)
	e.WaitGateReady(t, ns, b2, "prod", "hold", false, "", gateTimeout)
	out = c.Must(ns, "explain", pipelineName, "--env", "prod")
	assert.Equal(t, []map[string]string{gateRow("prod", b2, "hold", "Waiting", openExpr,
		holdMsg+" (bundle.version="+fixtures.V3+": "+openExpr+" = false)")}, explainTable(t, out), out)
	assert.Equal(t, "Bundle "+b2+" deleted\n", c.Must(ns, "delete", "bundle", b2))
	waitBundleGone(t, e, ns, b2)
	framework.Eventually(t, time.Minute, "B2's gate instances deleted with it", func(ctx context.Context) (bool, string) {
		_, ok, err := e.GateInstance(ctx, ns, b2, "prod", "hold")
		return err == nil && !ok, fmt.Sprintf("found=%v err=%v", ok, err)
	})
	row := e.WaitExplainGate(t, ns, pipelineName, "prod", "hold", "Superseded", gateTimeout)
	t.Logf("explain: %s", row)
	// B1's prod step is cancelled after its PR is closed on the git server,
	// which can take seconds after B1 turned Superseded (#1550).
	ps := e.WaitStepState(t, ns, pipelineName, b1, "prod", "Failed", time.Minute)
	assert.Equal(t, "bundle "+b1+" was superseded — promotion cancelled", ps.Status.Message)
	out = c.Must(ns, "explain", pipelineName, "--env", "prod")
	assert.Equal(t, []map[string]string{
		gateRow("prod", b1, "hold", "Superseded", openExpr, holdMsg+" ("+blocked+")"),
		stepRow(ps),
	}, explainTable(t, out), out)

	// override applies only to in-progress Bundles.
	refuses(t, c, ns, fmt.Sprintf("no in-progress Bundle of pipeline podinfo has an instance of gate hold in namespace %s "+
		`(stage "prod"; 1 instance(s) of finished Bundles); an override applies to the instances a promoting Bundle creates, `+
		"so run it while the Bundle waits on the gate", ns), "override", pipelineName, "--stage", "prod", "--gate", "hold", "--reason", "r")
	g, ok, err = e.GateInstance(ctx, ns, b1, "prod", "hold")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, g.Spec.Overrides)
}

// lastEvaluatedIn maps name/namespace to LAST-EVALUATED for the policy list
// rows in the namespaces nss.
func lastEvaluatedIn(t *testing.T, out string, nss ...string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, r := range policyRows(t, out, nss...) {
		got[r["NAME"]+"/"+r["NAMESPACE"]] = r["LAST-EVALUATED"]
	}
	return got
}

// TestCLI_PolicyListLastEvaluated checks policy list's LAST-EVALUATED. The
// controller evaluates a template's instances, never the template, so the
// column is the newest evaluation of the instances, "-" before there is one.
// A team gate counts the instances in its own namespace, not those of a team
// gate of the same name elsewhere; an org gate a Pipeline reads through
// spec.policyNamespaces counts its instances in the Pipeline's namespace.
//
// Covers CLI-POLICY-LIST-01.
func TestCLI_PolicyListLastEvaluated(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test")
	ns, orgNS, otherNS := a.ns, e.Namespace(t), e.Namespace(t)
	e.CreateGate(t, framework.Gate(ns, "entry", "test", openExpr, recheck))
	audit := framework.Gate(orgNS, "audit", "test", "true", recheck)
	audit.Labels["kardinal.io/scope"] = "org"
	e.CreateGate(t, audit)
	e.CreateGate(t, framework.Gate(otherNS, "entry", "test", openExpr, recheck))
	p := a.pipeline(nil)
	p.Spec.PolicyNamespaces = []string{orgNS}
	a.apply(t, p)
	waitPipelineValid(t, e, ns, pipelineName)

	none := map[string]string{"entry/" + ns: "-", "audit/" + orgNS: "-", "entry/" + otherNS: "-"}
	out := c.Must(ns, "policy", "list")
	assert.Equal(t, none, lastEvaluatedIn(t, out, ns, orgNS, otherNS), "no instance yet:\n%s", out)

	b := e.CreateBundle(t, ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitGateReady(t, ns, b, "test", "entry", false, "= false", gateTimeout)
	e.WaitGateNamed(t, ns, framework.GateInstanceName(orgNS, "audit", "test", b), gateTimeout, "evaluated",
		framework.Evaluated(true, "= true"))

	out = c.Must(ns, "policy", "list")
	got := lastEvaluatedIn(t, out, ns, orgNS, otherNS)
	assert.Regexp(t, agoRE, got["entry/"+ns], "the team gate's instance:\n%s", out)
	assert.Regexp(t, agoRE, got["audit/"+orgNS], "the org gate's instance, in %s:\n%s", ns, out)
	assert.Equal(t, "-", got["entry/"+otherNS], "a team gate of the same name in another namespace:\n%s", out)

	out = c.Must(ns, "policy", "list", "--pipeline", pipelineName)
	got = lastEvaluatedIn(t, out, ns, orgNS, otherNS)
	assert.Len(t, got, 2, "the gates attached to the pipeline:\n%s", out)
	assert.Regexp(t, agoRE, got["entry/"+ns], out)
	assert.Regexp(t, agoRE, got["audit/"+orgNS], out)
}

// simRows renders policy simulate's per-gate rows (name, PASS or BLOCK,
// reason), aligned as the CLI aligns them.
func simRows(rows ...[3]string) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 3, ' ', 0)
	for _, r := range rows {
		fmt.Fprintf(tw, "%s:\t%s\t(%s)\n", r[0], r[1], r[2])
	}
	_ = tw.Flush()
	return b.String()
}

// TestCLI_PolicySimulate evaluates prod's gates with kardinal policy
// simulate at chosen times and soak times. A gate blocked at the simulated
// time is listed with its message (its reason when it has none) and, when
// time can open it, the next hour it passes; every gate gets a PASS or BLOCK
// row with what its expression evaluated to. BLOCKED still exits 0. An
// environment without gates says so. Missing flags, a bad --time, a negative
// --soak-minutes and an unknown environment or Pipeline are errors, and
// nothing is written to the cluster.
// Covers CLI-POLICY-SIMULATE-01.
func TestCLI_PolicySimulate(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	barePipeline(t, e, ns, pipelineName, func(p *v1alpha1.Pipeline) {
		p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{Name: "prod", Path: "environments/prod"})
	})
	weekdays := framework.Gate(ns, "weekdays", "prod", "!schedule.isWeekend", "")
	weekdays.Spec.Message = "No weekend deploys"
	e.CreateGate(t, weekdays)
	e.CreateGate(t, framework.Gate(ns, "soaked", "prod", "bundle.upstreamSoakMinutes >= 30", ""))

	simulate := func(args ...string) string {
		t.Helper()
		r := c.Run(ns, append([]string{"policy", "simulate", "--pipeline", pipelineName}, args...)...)
		require.Equal(t, 0, r.Code, "BLOCKED or PASS, simulate exits 0:\n%s", r.Output())
		assert.Empty(t, r.Stderr)
		return r.Stdout
	}
	const (
		weekend   = "!schedule.isWeekend = false"
		weekday   = "!schedule.isWeekend = true"
		soakedOK  = "bundle.upstreamSoakMinutes >= 30 = true"
		soakShort = "bundle.upstreamSoakMinutes >= 30 = false"
	)
	assert.Equal(t, "RESULT: BLOCKED\n"+
		"Blocked by: weekdays\nMessage: \"No weekend deploys\"\nNext window: Monday 00:00 UTC\n\n"+
		simRows([3]string{"soaked", "PASS", soakedOK}, [3]string{"weekdays", "BLOCK", weekend}),
		simulate("--env", "prod", "--time", "Saturday 3pm", "--soak-minutes", "60"))
	assert.Equal(t, "RESULT: PASS\n"+
		simRows([3]string{"soaked", "PASS", soakedOK}, [3]string{"weekdays", "PASS", weekday}),
		simulate("--env", "prod", "--time", "2026-10-06T10:00:00Z", "--soak-minutes", "60"), "an RFC 3339 time (a Tuesday)")
	assert.Equal(t, "RESULT: BLOCKED\n"+
		"Blocked by: soaked\nMessage: \""+soakShort+"\"\n\n"+
		simRows([3]string{"soaked", "BLOCK", soakShort}, [3]string{"weekdays", "PASS", weekday}),
		simulate("--env", "prod", "--time", "tue 10:00"), "no soak: no time opens the soak gate")
	assert.Equal(t, "RESULT: BLOCKED\n"+
		"Blocked by: soaked\nMessage: \""+soakShort+"\"\n\n"+
		"Blocked by: weekdays\nMessage: \"No weekend deploys\"\nNext window: Monday 00:00 UTC\n\n"+
		simRows([3]string{"soaked", "BLOCK", soakShort}, [3]string{"weekdays", "BLOCK", weekend}),
		simulate("--env", "prod", "--time", "sun 23:30", "--soak-minutes", "10"))
	assert.Equal(t, "RESULT: PASS\nNo PolicyGates found for pipeline \"podinfo\" environment \"test\"\n",
		simulate("--env", "test", "--time", "Saturday 3pm"))

	refuses(t, c, ns, `required flag(s) "env", "pipeline" not set`, "policy", "simulate")
	refuses(t, c, ns, `required flag(s) "env" not set`, "policy", "simulate", "--pipeline", pipelineName)
	refuses(t, c, ns, `invalid --time "someday": want a weekday and an hour in UTC ("Saturday 3pm", "tue 10:00") or an RFC 3339 timestamp`,
		"policy", "simulate", "--pipeline", pipelineName, "--env", "prod", "--time", "someday")
	refuses(t, c, ns, "--soak-minutes must not be negative",
		"policy", "simulate", "--pipeline", pipelineName, "--env", "prod", "--soak-minutes", "-5")
	refuses(t, c, ns, `environment "staging" not found in pipeline "podinfo" (environments: test, prod)`,
		"policy", "simulate", "--pipeline", pipelineName, "--env", "staging")
	refuses(t, c, ns, fmt.Sprintf("pipeline %q not found in namespace %q", "nope", ns),
		"policy", "simulate", "--pipeline", "nope", "--env", "prod")

	// Nothing was written: no Bundle, no gate instance, templates untouched.
	var bl v1alpha1.BundleList
	require.NoError(t, e.Client.List(ctx, &bl, client.InNamespace(ns)))
	assert.Empty(t, bl.Items)
	var gl v1alpha1.PolicyGateList
	require.NoError(t, e.Client.List(ctx, &gl, client.InNamespace(ns)))
	var names []string
	for _, g := range gl.Items {
		names = append(names, g.Name)
		assert.Empty(t, g.Spec.Overrides, g.Name)
		assert.Nil(t, g.Status.LastEvaluatedAt, "%s: a template is never evaluated", g.Name)
	}
	sort.Strings(names)
	assert.Equal(t, []string{"soaked", "weekdays"}, names)
}
