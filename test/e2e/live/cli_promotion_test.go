//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/user"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The tests in this file drive promotions with the CLI (create bundle,
// promote, rollback, pause, resume, delete bundle) and read them back with
// the get commands and the reports (logs, history, diff, metrics, audit).

// cliCellGap separates table columns (tabwriter pads with at least two
// spaces; a cell holds at most single spaces).
var cliCellGap = regexp.MustCompile(`\s{2,}`)

// watchFooter ends each table frame of a --watch command whose stdout is not
// a terminal; the screen is not cleared between frames.
const watchFooter = "\n(watching — press Ctrl-C to quit)\n"

// frames splits a --watch table stream into its complete frames.
func frames(out string) []string {
	parts := strings.Split(out, watchFooter)
	return parts[:len(parts)-1]
}

// lastFrame is the newest complete frame of a --watch table stream.
func lastFrame(out string) string {
	f := frames(out)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// sameTable asserts that two table outputs have the same header and rows,
// except AGE, which can tick between the two runs.
func sameTable(t *testing.T, want, got string, msg string) {
	t.Helper()
	header := func(out string) []string { return cliCellGap.Split(strings.SplitN(out, "\n", 2)[0], -1) }
	rows := func(out string) []map[string]string {
		rs := framework.ParseTable(out)
		for _, r := range rs {
			delete(r, "AGE")
		}
		return rs
	}
	assert.Equal(t, header(want), header(got), msg)
	assert.Equal(t, rows(want), rows(got), msg)
}

// subStep is one row of get steps' sub-step table.
type subStep struct{ env, name, state, duration, message string }

// cliSubSteps parses get steps' sub-step table: ENVIRONMENT STEP STATE DURATION
// MESSAGE, where the environment is printed on its first row only and the
// rows after it start with spaces.
func cliSubSteps(t *testing.T, out string) []subStep {
	t.Helper()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Equal(t, []string{"ENVIRONMENT", "STEP", "STATE", "DURATION", "MESSAGE"},
		cliCellGap.Split(strings.TrimSpace(lines[0]), -1), "sub-step table header in:\n%s", out)
	var rows []subStep
	env := ""
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			break
		}
		n := 4
		if !strings.HasPrefix(line, " ") {
			n = 5
		}
		cells := cliCellGap.Split(strings.TrimSpace(line), n)
		require.Len(t, cells, n, "sub-step row %q", line)
		if n == 5 {
			env, cells = cells[0], cells[1:]
		}
		rows = append(rows, subStep{env: env, name: cells[0], state: cells[1], duration: cells[2], message: cells[3]})
	}
	return rows
}

// envSubSteps is the name → state of env's rows in rows, in order.
func envSubSteps(rows []subStep, env string) (names []string, states map[string]string) {
	states = map[string]string{}
	for _, r := range rows {
		if r.env == env {
			names = append(names, r.name)
			states[r.name] = r.state
		}
	}
	return names, states
}

// cliUser is the name the CLI records as the requester: the OS user without
// a DOMAIN\ prefix or an @domain suffix (cmd/kardinal/cmd currentUser).
func cliUser(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	require.NoError(t, err)
	name := u.Username
	if i := strings.LastIndex(name, `\`); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.Index(name, "@"); i >= 0 {
		name = name[:i]
	}
	return name
}

// openPR waits for the one open PR on repo whose body names tag.
func openPR(t *testing.T, e *framework.Env, repo gitserver.Repo, tag string) gitserver.PR {
	t.Helper()
	return e.WaitPR(t, repo, time.Minute, "the open PR for "+tag, func(pr gitserver.PR) bool {
		return pr.State == "open" && strings.Contains(pr.Body, tag)
	})
}

// kustomizeTag is env's newTag in the app's repo.
func kustomizeTag(t *testing.T, a *app, env string) string {
	t.Helper()
	m := regexp.MustCompile(`newTag: (\S+)`).FindStringSubmatch(
		a.e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path(env)+"/kustomization.yaml"))
	require.NotNil(t, m, "no newTag in %s", fixtures.Path(env))
	return m[1]
}

// TestCLI_CreateBundleAndGet creates image Bundles with kardinal create bundle
// and follows them with the get commands. create bundle prints the Bundle's
// name and records the flags as the Bundle's spec; bad flags or a missing
// Pipeline create nothing. get pipelines, get bundles and get steps show each
// stage, as tables and as -o json/yaml, and their --watch streams follow the
// promotion until Ctrl-C. A newer Bundle supersedes the older one: --active
// and get steps leave it out. -o is checked per command.
// Covers CLI-CREATE-BUNDLE-01, CLI-GET-PIPELINES-01, CLI-GET-BUNDLES-01,
// CLI-GET-STEPS-01, CLI-OUTPUT-01.
func TestCLI_CreateBundleAndGet(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	waitPipelineValid(t, e, a.ns, pipelineName)

	// Before the first Bundle.
	row := framework.TableRow(c.Must(a.ns, "get", "pipelines"), "PIPELINE", pipelineName)
	require.NotNil(t, row)
	assert.Equal(t, map[string]string{"PIPELINE": pipelineName, "BUNDLE": "-", "TEST": "-", "PROD": "-", "SUB": "0", "AGE": row["AGE"]}, row)
	assert.True(t, strings.HasPrefix(c.Must(a.ns, "get", "pipelines"), "PIPELINE   BUNDLE   TEST   PROD   SUB   AGE\n"))
	assert.Equal(t, "BUNDLE   TYPE   PHASE   AGE\n", c.Must(a.ns, "get", "bundles"))
	assert.Equal(t, "No active bundles for pipeline \"podinfo\".\n", c.Must(a.ns, "get", "steps", pipelineName))
	for _, args := range [][]string{{"get", "bundles"}, {"get", "steps", pipelineName}, {"get", "subscriptions"}} {
		assert.Equal(t, "[]\n", c.Must(a.ns, append(args, "-o", "json")...), "%v -o json with nothing to list", args)
	}

	// Bad flags and a missing Pipeline create nothing.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{pipelineName}, `create bundle: type "image" requires at least one --image`},
		{[]string{pipelineName, "--image", "ghcr.io/stefanprodan/PodInfo:" + fixtures.V2},
			`invalid image repository "ghcr.io/stefanprodan/PodInfo": want [host[:port]/]path (e.g. ghcr.io/org/image)`},
		{[]string{pipelineName, "--image", fixtures.Image + ":" + fixtures.V2, "--ci-run-url", "ci.example/run/1"},
			"create bundle: --ci-run-url must be an absolute http or https URL"},
		{[]string{pipelineName, "--image", fixtures.Image + ":" + fixtures.V2, "--type", "helm"},
			`create bundle: type must be one of image, config, mixed, chart (got "helm")`},
		{[]string{"nope", "--image", fixtures.Image + ":" + fixtures.V2},
			fmt.Sprintf("pipeline %q not found in namespace %q", "nope", a.ns)},
	} {
		r := c.Fail(a.ns, append([]string{"create", "bundle"}, tc.args...)...)
		assert.Equal(t, 1, r.Code)
		assert.Equal(t, tc.want+"\n", r.Stderr, "create bundle %v", tc.args)
		assert.Empty(t, r.Stdout)
	}
	assert.Empty(t, bundles(t, e, a.ns), "a rejected create bundle creates nothing")

	// The first Bundle.
	out := c.Must(a.ns, "create", "bundle", pipelineName, "--image", fixtures.Image+":"+fixtures.V2,
		"--commit", "0123abc", "--author", "e2e-bot", "--ci-run-url", "https://ci.example/run/1")
	m := regexp.MustCompile(`^Bundle (podinfo-[a-z0-9]{5}) created for pipeline podinfo\nTrack with: kardinal get bundles podinfo\n$`).FindStringSubmatch(out)
	require.NotNil(t, m, "create bundle output:\n%s", out)
	b1 := m[1]
	got := getBundle(t, e, a.ns, b1)
	assert.Equal(t, "image", got.Spec.Type)
	assert.Equal(t, pipelineName, got.Spec.Pipeline)
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}, got.Spec.Images)
	require.NotNil(t, got.Spec.Provenance)
	assert.Equal(t, v1alpha1.BundleProvenance{CommitSHA: "0123abc", Author: "e2e-bot", CIRunURL: "https://ci.example/run/1"}, *got.Spec.Provenance)
	assert.Nil(t, got.Spec.ConfigRef)
	assert.NotEmpty(t, got.Annotations["kardinal.io/created-at"], "create bundle stamps the creation time")

	e.WaitStepState(t, a.ns, pipelineName, b1, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, pipelineName, b1, "prod", "WaitingForMerge", promoteTimeout)
	pr1 := openPR(t, e, a.repo, fixtures.V2)

	tables := c.Must(a.ns, "get", "pipelines")
	row = framework.TableRow(tables, "PIPELINE", pipelineName)
	require.NotNil(t, row, tables)
	assert.Equal(t, b1, row["BUNDLE"])
	assert.Equal(t, "Verified", row["TEST"])
	assert.Equal(t, "WaitingForMerge", row["PROD"])
	assert.Equal(t, "0", row["SUB"])
	sameTable(t, tables, c.Must(a.ns, "get", "pipelines", pipelineName), "the name filter keeps the only pipeline")
	assert.Equal(t, "No pipelines found.\n  To get started, apply a Pipeline CRD:\n    kubectl apply -f examples/quickstart/pipeline.yaml\n  Or check CRD installation with: kardinal doctor\n",
		c.Must(a.ns, "get", "pipelines", "nope"))
	all := c.Must("", "get", "pipelines", "-A")
	// The environment columns are the union over every namespace's Pipelines.
	header := cliCellGap.Split(strings.SplitN(all, "\n", 2)[0], -1)
	require.GreaterOrEqual(t, len(header), 7, all)
	assert.Equal(t, []string{"NAMESPACE", "PIPELINE", "BUNDLE"}, header[:3])
	assert.Equal(t, []string{"SUB", "AGE"}, header[len(header)-2:])
	assert.Subset(t, header, []string{"TEST", "PROD"})
	row = framework.TableRow(all, "NAMESPACE", a.ns)
	require.NotNil(t, row, "-A lists the test's namespace:\n%s", all)
	assert.Equal(t, pipelineName, row["PIPELINE"])
	assert.Equal(t, b1, row["BUNDLE"])

	var pipelines []v1alpha1.Pipeline
	require.NoError(t, json.Unmarshal([]byte(c.Must(a.ns, "get", "pipelines", "-o", "json")), &pipelines))
	require.Len(t, pipelines, 1)
	assert.Equal(t, pipelineName, pipelines[0].Name)
	assert.Equal(t, []string{"test", "prod"}, []string{pipelines[0].Spec.Environments[0].Name, pipelines[0].Spec.Environments[1].Name})
	pipelines = nil
	require.NoError(t, yaml.Unmarshal([]byte(c.Must(a.ns, "get", "pipelines", "-o", "yaml")), &pipelines))
	require.Len(t, pipelines, 1)
	assert.Equal(t, a.repo.CloneURL, pipelines[0].Spec.Git.URL)

	bt := c.Must(a.ns, "get", "bundles")
	row = framework.TableRow(bt, "BUNDLE", b1)
	require.NotNil(t, row, bt)
	assert.Equal(t, "image", row["TYPE"])
	assert.Equal(t, "Promoting", row["PHASE"])
	sameTable(t, bt, c.Must(a.ns, "get", "bundles", pipelineName), "the pipeline filter keeps the pipeline's Bundles")
	var bl []v1alpha1.Bundle
	require.NoError(t, json.Unmarshal([]byte(c.Must(a.ns, "get", "bundles", "-o", "json")), &bl))
	require.Len(t, bl, 1)
	assert.Equal(t, b1, bl[0].Name)
	assert.Equal(t, "Promoting", bl[0].Status.Phase)
	bl = nil
	require.NoError(t, yaml.Unmarshal([]byte(c.Must(a.ns, "get", "bundles", "-o", "yaml")), &bl))
	require.Len(t, bl, 1)
	assert.Equal(t, fixtures.V2, bl[0].Spec.Images[0].Tag)

	rows := cliSubSteps(t, c.Must(a.ns, "get", "steps", pipelineName))
	names, states := envSubSteps(rows, "test")
	assert.Equal(t, []string{"git-clone", "kustomize-set-image", "git-commit", "git-push", "health-check"}, names)
	for _, n := range names {
		assert.Equal(t, "Completed", states[n], "test %s", n)
	}
	names, states = envSubSteps(rows, "prod")
	assert.Equal(t, []string{"git-clone", "kustomize-set-image", "git-commit", "git-push", "open-pr", "wait-for-merge", "health-check"}, names)
	assert.Equal(t, "Completed", states["open-pr"])
	assert.NotEqual(t, "Completed", states["wait-for-merge"], "the PR is not merged")
	assert.Equal(t, "prod", rows[0].env, "environments are sorted by name")
	var sl []v1alpha1.PromotionStep
	require.NoError(t, json.Unmarshal([]byte(c.Must(a.ns, "get", "steps", pipelineName, "-o", "json")), &sl))
	require.Len(t, sl, 2)
	for _, s := range sl {
		assert.Equal(t, b1, s.Spec.BundleName)
	}
	sl = nil
	require.NoError(t, yaml.Unmarshal([]byte(c.Must(a.ns, "get", "steps", pipelineName, "-o", "yaml")), &sl))
	assert.Len(t, sl, 2)

	// -o: json and yaml where a command supports them, else an error.
	r := c.Fail(a.ns, "explain", pipelineName, "-o", "json")
	assert.Equal(t, "-o json is not supported by \"kardinal explain\"; it prints a table\n", r.Stderr)
	r = c.Fail(a.ns, "get", "bundles", "-o", "xml")
	assert.Equal(t, "invalid -o \"xml\": must be table, json or yaml\n", r.Stderr)
	sameTable(t, bt, c.Must(a.ns, "get", "bundles", "-o", "table"), "-o table is the default")

	// A newer Bundle supersedes the first.
	b2 := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3)
	e.WaitBundlePhase(t, a.ns, b1, "Superseded", promoteTimeout)
	e.WaitPRState(t, a.repo, pr1.Number, "closed", time.Minute)
	bt = c.Must(a.ns, "get", "bundles")
	assert.Equal(t, "Superseded", framework.TableRow(bt, "BUNDLE", b1)["PHASE"], bt)
	require.NotNil(t, framework.TableRow(bt, "BUNDLE", b2), bt)
	active := c.Must(a.ns, "get", "bundles", "--active")
	assert.Nil(t, framework.TableRow(active, "BUNDLE", b1), "--active hides the Superseded Bundle:\n%s", active)
	assert.NotNil(t, framework.TableRow(active, "BUNDLE", b2), active)
	// b2's prod step exists only once its test step is Verified: Argo CD's
	// sync and the health check come first, which can take minutes on a
	// loaded cluster (#1547), so this waits as long as a promotion.
	framework.Eventually(t, promoteTimeout, "get steps to show only "+b2, func(context.Context) (bool, string) {
		var sl []v1alpha1.PromotionStep
		raw := c.Must(a.ns, "get", "steps", pipelineName, "-o", "json")
		if err := json.Unmarshal([]byte(raw), &sl); err != nil {
			return false, err.Error()
		}
		seen := make([]string, 0, len(sl))
		only := true
		for _, s := range sl {
			seen = append(seen, s.Name+"="+s.Status.State)
			only = only && s.Spec.BundleName == b2
		}
		return only && len(sl) == 2, strings.Join(seen, ", ")
	})

	// The --watch streams follow b2 to prod.
	e.WaitStepState(t, a.ns, pipelineName, b2, "prod", "WaitingForMerge", promoteTimeout)
	pr2 := openPR(t, e, a.repo, fixtures.V3)
	pw := c.Start(framework.CLIOptions{}, c.Args(a.ns, "get", "pipelines", "-w")...)
	sw := c.Start(framework.CLIOptions{}, c.Args(a.ns, "get", "steps", pipelineName, "--watch")...)
	jw := c.Start(framework.CLIOptions{}, c.Args(a.ns, "get", "pipelines", "-w", "-o", "json")...)
	pw.WaitStdout(time.Minute, "a pipelines frame with prod waiting", func(out string) bool {
		r := framework.TableRow(lastFrame(out), "PIPELINE", pipelineName)
		return r != nil && r["BUNDLE"] == b2 && r["PROD"] == "WaitingForMerge"
	})
	sw.WaitStdout(time.Minute, "a steps frame", func(out string) bool { return lastFrame(out) != "" })
	require.NoError(t, e.Git.MergePR(ctx, a.repo, pr2.Number))
	e.WaitStepState(t, a.ns, pipelineName, b2, "prod", "Verified", promoteTimeout)
	out = pw.WaitStdout(time.Minute, "a pipelines frame with prod Verified", func(out string) bool {
		r := framework.TableRow(lastFrame(out), "PIPELINE", pipelineName)
		return r != nil && r["BUNDLE"] == b2 && r["TEST"] == "Verified" && r["PROD"] == "Verified"
	})
	assert.GreaterOrEqual(t, len(frames(out)), 2, "the watch redraws")
	sw.WaitStdout(time.Minute, "a steps frame with wait-for-merge Completed", func(out string) bool {
		f := lastFrame(out)
		if !strings.HasPrefix(f, "ENVIRONMENT   STEP") {
			return false
		}
		_, states := envSubSteps(cliSubSteps(t, f), "prod")
		return states["wait-for-merge"] == "Completed" && states["health-check"] == "Completed"
	})
	jw.WaitStdout(time.Minute, "a JSON frame with the Pipeline Ready", func(out string) bool {
		dec := json.NewDecoder(strings.NewReader(out))
		var last []v1alpha1.Pipeline
		for {
			var frame []v1alpha1.Pipeline
			if err := dec.Decode(&frame); err != nil {
				break
			}
			last = frame
		}
		return len(last) == 1 && last[0].Status.Phase == "Ready"
	})
	for _, p := range []*framework.CLIProcess{pw, sw, jw} {
		p.Interrupt()
		res := p.Wait(30 * time.Second)
		assert.Equal(t, 0, res.Code, "Ctrl-C ends kardinal %v with exit 0", res.Args)
		assert.NotContains(t, res.Stdout, "\033[", "no screen clearing when stdout is not a terminal")
	}
	assert.NotContains(t, jw.Stdout(), "watching", "-o json frames have no footer")
	dec := json.NewDecoder(strings.NewReader(jw.Stdout()))
	n := 0
	for {
		var frame []v1alpha1.Pipeline
		err := dec.Decode(&frame)
		if err == io.EOF {
			break
		}
		require.NoError(t, err, "every -o json frame is a JSON array")
		n++
	}
	assert.GreaterOrEqual(t, n, 1)
	assert.Equal(t, fixtures.Image+":"+fixtures.V3, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
}

// logBlock is one PromotionStep block of kardinal logs.
type logBlock struct {
	env, bundle, state string
	prURL              string
	// steps is the status.steps table: name → state, in order.
	steps []subStep
}

var (
	logHeader = regexp.MustCompile(`^=== podinfo/(\S+) \((\S+)\) \[(\S+)\] ===$`)
	logPRURL  = regexp.MustCompile(`^  pr_url:\s+(\S+)$`)
	duration  = regexp.MustCompile(`^(-|\d+\.\ds)$`)
)

// logBlocks parses kardinal logs (without --follow) for pipeline podinfo.
func logBlocks(t *testing.T, out string) []logBlock {
	t.Helper()
	var blocks []logBlock
	inSteps := false
	for _, line := range strings.Split(out, "\n") {
		if m := logHeader.FindStringSubmatch(line); m != nil {
			blocks = append(blocks, logBlock{env: m[1], bundle: m[2], state: m[3]})
			inSteps = false
			continue
		}
		require.NotEmpty(t, blocks, "logs output starts with a === header ===:\n%s", out)
		b := &blocks[len(blocks)-1]
		switch {
		case line == "":
			inSteps = false
		case logPRURL.MatchString(line):
			b.prURL = logPRURL.FindStringSubmatch(line)[1]
		case line == "  steps:":
			inSteps = true
		case inSteps:
			cells := cliCellGap.Split(strings.TrimSpace(line), 4)
			if cells[0] == "STEP" || cells[0] == "----" {
				continue
			}
			require.GreaterOrEqual(t, len(cells), 3, "steps row %q", line)
			s := subStep{env: b.env, name: cells[0], state: cells[1], duration: cells[2]}
			if len(cells) == 4 {
				s.message = cells[3]
			}
			b.steps = append(b.steps, s)
		}
	}
	return blocks
}

// TestCLI_Reports reads two finished promotions back with the report
// commands. Before the first Bundle each one says there is nothing to show.
// logs prints a block per PromotionStep with its PR and status.steps, and
// --follow streams a promotion until the Bundle is Verified. history lists
// each step newest first with its PR, diff compares the two Bundles' images
// and provenance, metrics shows the controller's deployment metrics or
// computes them for another window or environment, and get auditevents (as a
// table, JSON or YAML) and audit summary list and count the AuditEvents the
// controller wrote. Filters, limits and bad flags are checked for each.
// Covers CLI-LOGS-01, CLI-HISTORY-01, CLI-DIFF-01, CLI-METRICS-01,
// CLI-GET-AUDIT-01, CLI-AUDIT-01.
func TestCLI_Reports(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	waitPipelineValid(t, e, a.ns, pipelineName)

	// Nothing to report yet.
	assert.Equal(t, "No promotion history found for pipeline \"podinfo\"\n", c.Must(a.ns, "history", pipelineName))
	assert.Equal(t, "No promotion steps found for pipeline podinfo\n", c.Must(a.ns, "logs", pipelineName))
	assert.Equal(t, "No promotion steps found for pipeline podinfo (env=prod)\n", c.Must(a.ns, "logs", pipelineName, "--env", "prod"))
	assert.Equal(t, "No audit events found.\n", c.Must(a.ns, "get", "auditevents"))
	assert.Equal(t, "No audit events found in the last 24h.\nPipeline filter: podinfo\n",
		c.Must(a.ns, "audit", "summary", "--pipeline", pipelineName))
	assert.Equal(t, [][]string{
		{"METRIC", "VALUE", "NOTES"},
		{"pipeline", "podinfo", "(last 30 days)"},
		{"target_env", "prod"},
		{"bundles_total", "0"},
		{"deployment_frequency", "0.00/day", "(0 verified in target env)"},
		{"lead_time_avg", "-", "(no completed promotions to prod in window)"},
		{"change_fail_rate", "0.0%", "(0 failed / 0 total)"},
		{"rollback_count", "0"},
	}, cells(c.Must(a.ns, "metrics", "--pipeline", pipelineName)))

	// Bundle A, promoted to prod.
	bA := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2,
		"--commit", "aaaa1111cccc", "--author", "alice")
	e.WaitStepState(t, a.ns, pipelineName, bA, "prod", "WaitingForMerge", promoteTimeout)
	prA := openPR(t, e, a.repo, fixtures.V2)
	require.NoError(t, e.Git.MergePR(ctx, a.repo, prA.Number))
	e.WaitStepState(t, a.ns, pipelineName, bA, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, bA, "Verified", promoteTimeout)

	// Bundle B, followed with logs --follow until it is Verified.
	bB := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V3,
		"--commit", "bbbb2222dddd", "--author", "bob")
	lf := c.Start(framework.CLIOptions{}, c.Args(a.ns, "logs", pipelineName, "--follow", "--bundle", bB)...)
	e.WaitStepState(t, a.ns, pipelineName, bB, "prod", "WaitingForMerge", promoteTimeout)
	lf.WaitStdout(time.Minute, "logs --follow to show prod waiting", func(out string) bool {
		return strings.Contains(out, "[podinfo/prod] → WaitingForMerge\n")
	})
	assert.False(t, lf.Exited(), "logs --follow runs while prod waits for its PR")
	prB := openPR(t, e, a.repo, fixtures.V3)
	require.NoError(t, e.Git.MergePR(ctx, a.repo, prB.Number))
	e.WaitStepState(t, a.ns, pipelineName, bB, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, bB, "Verified", promoteTimeout)
	res := lf.Wait(time.Minute)
	assert.Equal(t, 0, res.Code)
	assert.True(t, strings.HasPrefix(res.Stdout, "Following logs for pipeline podinfo (Ctrl+C to stop)...\n"), res.Stdout)
	assert.True(t, strings.HasSuffix(res.Stdout, "[podinfo/prod] → Verified\nAll steps reached terminal state.\n"), res.Stdout)
	for _, env := range []string{"test", "prod"} {
		assert.Equal(t, 1, strings.Count(res.Stdout, "[podinfo/"+env+"] → Verified\n"), "each state change once:\n%s", res.Stdout)
	}
	assert.Regexp(t, `(?m)^\[podinfo/prod\] wait-for-merge\s+\S+`, res.Stdout, "--follow prints the steps it runs")
	assert.NotContains(t, res.Stdout, bA, "--bundle follows only B")

	// logs: one block per PromotionStep, environments by name, then by age.
	blocks := logBlocks(t, c.Must(a.ns, "logs", pipelineName))
	var got []string
	for _, b := range blocks {
		got = append(got, b.env+" "+b.bundle+" "+b.state)
	}
	assert.Equal(t, []string{"prod " + bA + " Verified", "prod " + bB + " Verified", "test " + bA + " Verified", "test " + bB + " Verified"}, got)
	require.Len(t, blocks, 4)
	assert.True(t, strings.HasSuffix(blocks[0].prURL, fmt.Sprintf("/%d", prA.Number)), "pr_url of A's prod step: %q", blocks[0].prURL)
	assert.True(t, strings.HasSuffix(blocks[1].prURL, fmt.Sprintf("/%d", prB.Number)), "pr_url of B's prod step: %q", blocks[1].prURL)
	assert.Empty(t, blocks[2].prURL, "test merges directly")
	for _, b := range blocks {
		var names []string
		for _, s := range b.steps {
			names = append(names, s.name)
			assert.Equal(t, "Completed", s.state, "%s %s %s", b.env, b.bundle, s.name)
			assert.Regexp(t, duration, s.duration)
		}
		want := []string{"git-clone", "kustomize-set-image", "git-commit", "git-push", "health-check"}
		if b.env == "prod" {
			want = []string{"git-clone", "kustomize-set-image", "git-commit", "git-push", "open-pr", "wait-for-merge", "health-check"}
		}
		assert.Equal(t, want, names, "steps of %s %s", b.env, b.bundle)
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"--env", "test"}, []string{"test " + bA, "test " + bB}},
		{[]string{"--bundle", bA}, []string{"prod " + bA, "test " + bA}},
		{[]string{"--env", "prod", "--bundle", bB}, []string{"prod " + bB}},
	} {
		var got []string
		for _, b := range logBlocks(t, c.Must(a.ns, append([]string{"logs", pipelineName}, tc.args...)...)) {
			got = append(got, b.env+" "+b.bundle)
		}
		assert.Equal(t, tc.want, got, "logs %v", tc.args)
	}

	// history: one row per step, newest first.
	hist := framework.ParseTable(c.Must(a.ns, "history", pipelineName))
	require.Len(t, hist, 4)
	type histRow struct{ bundle, action, env, pr string }
	var rows []histRow
	for _, r := range hist {
		rows = append(rows, histRow{r["BUNDLE"], r["ACTION"], r["ENV"], r["PR"]})
		assert.Regexp(t, `^\d+(s|m)$`, r["DURATION"], "a finished step's duration")
		assert.Regexp(t, `^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$`, r["TIMESTAMP"])
	}
	assert.Equal(t, []histRow{
		{bB, "promote", "prod", fmt.Sprintf("#%d", prB.Number)},
		{bB, "promote", "test", "--"},
		{bA, "promote", "prod", fmt.Sprintf("#%d", prA.Number)},
		{bA, "promote", "test", "--"},
	}, rows)
	assert.Equal(t, hist[:1], framework.ParseTable(c.Must(a.ns, "history", pipelineName, "--limit", "1")))
	assert.Equal(t, []map[string]string{hist[0], hist[2]}, framework.ParseTable(c.Must(a.ns, "history", pipelineName, "--env", "prod")))
	assert.Equal(t, "No promotion history found for pipeline \"podinfo\" env \"staging\"\n",
		c.Must(a.ns, "history", pipelineName, "--env", "staging"))

	// diff: images by repository, then provenance; * marks a change.
	assert.Equal(t, [][]string{
		{"ARTIFACT", "BUNDLE-A (" + bA + ")", "BUNDLE-B (" + bB + ")", "CHANGED"},
		{fixtures.Image, fixtures.V2, fixtures.V3, "*"},
		{"PROVENANCE"},
		{"commit", "aaaa1111", "bbbb2222", "*"},
		{"author", "alice", "bob", "*"},
	}, cells(c.Must(a.ns, "diff", bA, bB)))
	assert.Equal(t, [][]string{
		{"ARTIFACT", "BUNDLE-A (" + bA + ")", "BUNDLE-B (" + bA + ")", "CHANGED"},
		{fixtures.Image, fixtures.V2, fixtures.V2},
		{"PROVENANCE"},
		{"commit", "aaaa1111", "aaaa1111"},
		{"author", "alice", "alice"},
	}, cells(c.Must(a.ns, "diff", bA, bA)), "a Bundle does not differ from itself")
	r := c.Fail(a.ns, "diff", bA, "nope")
	assert.Equal(t, 1, r.Code)
	assert.True(t, strings.HasPrefix(r.Stderr, `get bundle "nope": `), r.Stderr)
	assert.Empty(t, r.Stdout)
	r = c.Fail(a.ns, "diff", bA)
	assert.Contains(t, r.Stderr, "accepts 2 arg(s), received 1")

	// get auditevents: what the controller wrote, newest first.
	framework.Eventually(t, time.Minute, "8 AuditEvents", func(ctx context.Context) (bool, string) {
		var list v1alpha1.AuditEventList
		if err := e.Client.List(ctx, &list, client.InNamespace(a.ns)); err != nil {
			return false, err.Error()
		}
		return len(list.Items) == 8, fmt.Sprintf("%d AuditEvents", len(list.Items))
	})
	aeOut := c.Must(a.ns, "get", "auditevents")
	events := framework.ParseTable(aeOut)
	type aeRow struct{ pipeline, bundle, env, action, outcome string }
	var aes []aeRow
	for i, ev := range events {
		aes = append(aes, aeRow{ev["PIPELINE"], ev["BUNDLE"], ev["ENV"], ev["ACTION"], ev["OUTCOME"]})
		assert.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}Z$`, ev["TIMESTAMP"])
		if i > 0 {
			assert.LessOrEqual(t, ev["TIMESTAMP"], events[i-1]["TIMESTAMP"], "newest first")
		}
	}
	var want []aeRow
	for _, b := range []string{bA, bB} {
		for _, env := range []string{"test", "prod"} {
			want = append(want,
				aeRow{pipelineName, b, env, "PromotionStarted", "Pending"},
				aeRow{pipelineName, b, env, "PromotionSucceeded", "Success"})
		}
	}
	assert.ElementsMatch(t, want, aes, aeOut)
	assert.Equal(t, aeOut, c.Must(a.ns, "get", "ae"), "ae is an alias")
	assert.Equal(t, aeOut, c.Must(a.ns, "get", "auditevents", "--pipeline", pipelineName))
	assert.Equal(t, events[:1], framework.ParseTable(c.Must(a.ns, "get", "auditevents", "--limit", "1")))
	for _, tc := range []struct {
		flag, value string
		keep        func(aeRow) bool
	}{
		{"--bundle", bA, func(r aeRow) bool { return r.bundle == bA }},
		{"--env", "prod", func(r aeRow) bool { return r.env == "prod" }},
	} {
		var want, got []aeRow
		for _, r := range aes {
			if tc.keep(r) {
				want = append(want, r)
			}
		}
		for _, ev := range framework.ParseTable(c.Must(a.ns, "get", "auditevents", tc.flag, tc.value)) {
			got = append(got, aeRow{ev["PIPELINE"], ev["BUNDLE"], ev["ENV"], ev["ACTION"], ev["OUTCOME"]})
		}
		assert.Equal(t, want, got, "get auditevents %s %s", tc.flag, tc.value)
	}
	assert.Equal(t, "No audit events found.\n", c.Must(a.ns, "get", "auditevents", "--pipeline", "nope"))
	// -o json and -o yaml print the same events, newest first, as AuditEvents.
	for _, format := range []string{"json", "yaml"} {
		var list []v1alpha1.AuditEvent
		out := c.Must(a.ns, "get", "auditevents", "-o", format)
		if format == "json" {
			require.NoError(t, json.Unmarshal([]byte(out), &list), out)
		} else {
			require.NoError(t, yaml.Unmarshal([]byte(out), &list), out)
		}
		var got []aeRow
		for _, ev := range list {
			got = append(got, aeRow{ev.Spec.PipelineName, ev.Spec.BundleName, ev.Spec.Environment, ev.Spec.Action, ev.Spec.Outcome})
		}
		assert.Equal(t, aes, got, "get auditevents -o %s", format)
	}
	assert.Equal(t, "[]\n", c.Must(a.ns, "get", "auditevents", "--pipeline", "nope", "-o", "json"))

	// audit summary counts the same events.
	summary := regexp.MustCompile(`^Pipeline: podinfo  \(last (24h|7d)\)\n\n` +
		`Promotions:   4 started, 4 succeeded, 0 failed, 0 superseded\n` +
		`Success rate: 100\.0%\n` +
		`Avg duration: (\d+s|\d+m|\d+m \d+s)\n\n` +
		`Gates:        0 evaluations, 0 blocked \(0\.0% block rate\)\n` +
		`Rollbacks:    0 triggered, 0 succeeded\n$`)
	assert.Regexp(t, summary, c.Must(a.ns, "audit", "summary", "--pipeline", pipelineName))
	assert.Regexp(t, summary, c.Must(a.ns, "audit", "summary", "--since", "7d"), "one pipeline in the namespace")
	for _, tc := range []struct{ since, want string }{
		{"0", `invalid --since "0": must be positive`},
		{"-1h", `invalid --since "-1h": must be positive`},
		{"xd", `invalid --since "xd": invalid days "x": want a whole number such as 7d`},
		{"soon", `invalid --since "soon": parse duration: time: invalid duration "soon"`},
	} {
		r := c.Fail(a.ns, "audit", "summary", "--since", tc.since)
		assert.Equal(t, tc.want+"\n", r.Stderr, "--since %s", tc.since)
	}

	// metrics: the controller's metrics for the last environment over 30
	// days, else computed from the Bundles and steps.
	var dm *v1alpha1.PipelineDeploymentMetrics
	framework.Eventually(t, 2*time.Minute, "the Pipeline's deploymentMetrics to count both Bundles", func(ctx context.Context) (bool, string) {
		var p v1alpha1.Pipeline
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &p); err != nil {
			return false, err.Error()
		}
		dm = p.Status.DeploymentMetrics
		return dm != nil && dm.SampleSize == 2, fmt.Sprintf("%+v", dm)
	})
	mc := cells(c.Must(a.ns, "metrics", "--pipeline", pipelineName))
	require.Len(t, mc, 12, "%v", mc)
	assert.Equal(t, [][]string{
		{"METRIC", "VALUE", "NOTES"},
		{"pipeline", "podinfo", "(controller: last 2 bundles verified in prod)"},
		{"target_env", "prod"},
		{"rollouts_last_30d", "2"},
		{"p50_commit_to_prod", fmt.Sprintf("%dm", dm.P50CommitToProdMinutes)},
		{"p90_commit_to_prod", fmt.Sprintf("%dm", dm.P90CommitToProdMinutes)},
		{"auto_rollback_rate", fmt.Sprintf("%.1f%%", float64(dm.AutoRollbackRateMillis)/10), fmt.Sprintf("(%d per thousand)", dm.AutoRollbackRateMillis)},
		{"operator_intervention_rate", fmt.Sprintf("%.1f%%", float64(dm.OperatorInterventionRateMillis)/10), fmt.Sprintf("(%d per thousand)", dm.OperatorInterventionRateMillis)},
		{"stale_prod_days", fmt.Sprintf("%d", dm.StaleProdDays)},
		{"change_failure_rate", "0.0%", "(0 of 2 deployments failed)"},
		{"time_to_restore", "-", "(no restored failure)"},
	}, mc[:11])
	assert.Equal(t, "metrics_age", mc[11][0])
	assert.Regexp(t, `^\d+(s|m\d+s)$`, mc[11][1])
	assert.Equal(t, "(last computed by controller)", mc[11][2])
	assert.Equal(t, "0.0%", mc[6][1], "no automatic rollback")
	// metrics_age is the only row that changes between the two calls.
	explicit := cells(c.Must(a.ns, "metrics", "--pipeline", pipelineName, "--env", "prod", "--days", "30"))
	require.Len(t, explicit, 12, "%v", explicit)
	assert.Equal(t, mc[:11], explicit[:11], "the defaults are the last environment and 30 days")
	assert.Equal(t, "metrics_age", explicit[11][0])

	lead := regexp.MustCompile(`^(\d+s|\d+m\d+s)$`)
	mc = cells(c.Must(a.ns, "metrics", "--pipeline", pipelineName, "--days", "7"))
	require.Len(t, mc, 8, "%v", mc)
	assert.Regexp(t, lead, mc[5][1])
	mc[5][1] = "<lead>"
	assert.Equal(t, [][]string{
		{"METRIC", "VALUE", "NOTES"},
		{"pipeline", "podinfo", "(last 7 days)"},
		{"target_env", "prod"},
		{"bundles_total", "2"},
		{"deployment_frequency", "0.29/day", "(2 verified in target env)"},
		{"lead_time_avg", "<lead>", "(creation → prod verified, 2 samples)"},
		{"change_fail_rate", "0.0%", "(0 failed / 2 total)"},
		{"rollback_count", "0"},
	}, mc)
	mc = cells(c.Must(a.ns, "metrics", "--pipeline", pipelineName, "--env", "test"))
	require.Len(t, mc, 8, "%v", mc)
	assert.Equal(t, []string{"pipeline", "podinfo", "(last 30 days)"}, mc[1])
	assert.Equal(t, []string{"deployment_frequency", "0.07/day", "(2 verified in target env)"}, mc[4])
	assert.Equal(t, "(creation → test verified, 2 samples)", mc[5][2])
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--pipeline", pipelineName, "--days", "0"}, "--days must be positive, got 0"},
		{[]string{"--pipeline", "nope"}, fmt.Sprintf("pipeline %q not found in namespace %q", "nope", a.ns)},
		{nil, `required flag(s) "pipeline" not set`},
	} {
		r := c.Fail(a.ns, append([]string{"metrics"}, tc.args...)...)
		assert.Equal(t, tc.want+"\n", r.Stderr, "metrics %v", tc.args)
	}
}

// cells splits each non-empty line of a table into its cells.
func cells(out string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			rows = append(rows, cliCellGap.Split(line, -1))
		}
	}
	return rows
}
