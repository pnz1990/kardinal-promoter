// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// TestConfirm (C5): a destructive command asks on a terminal, --yes answers
// for it, and without a terminal it acts as before.
//
// Covers CLI-VERBS-01.
func TestConfirm(t *testing.T) {
	prev := promptable
	t.Cleanup(func() { promptable = prev })
	run := func(tty bool, answer string, args ...string) (string, error) {
		promptable = func() bool { return tty }
		cmd := newDeleteBundleCmd()
		cmd.SetIn(strings.NewReader(answer))
		var errb bytes.Buffer
		cmd.SetErr(&errb)
		require.NoError(t, cmd.ParseFlags(args))
		err := confirm(cmd, "Delete b?")
		return errb.String(), err
	}
	asked, err := run(true, "y\n")
	require.NoError(t, err)
	assert.Equal(t, "Delete b? [y/N] ", asked)
	_, err = run(true, "\n")
	assert.ErrorIs(t, err, errCancelled, "the default is no")
	_, err = run(true, "nope\n")
	assert.ErrorIs(t, err, errCancelled)
	asked, err = run(true, "", "--yes")
	require.NoError(t, err)
	assert.Empty(t, asked, "--yes does not ask")
	asked, err = run(false, "")
	require.NoError(t, err)
	assert.Empty(t, asked, "no terminal: no question, as before")

	for _, c := range []string{"delete", "reject", "override", "rollback"} {
		cmd, _, err := NewRootCmd().Find([]string{c, "bundle"})
		if c != "delete" {
			cmd, _, err = NewRootCmd().Find([]string{c})
		}
		require.NoError(t, err)
		assert.NotNil(t, cmd.Flags().Lookup("yes"), "%s has --yes", c)
	}

	// Only the end of the pipeline asks for a rollback.
	c := policyClient(t, policyPipeline("demo", "test", "uat", "prod"))
	assert.True(t, lastEnvironment(context.Background(), c, "default", "demo", "prod"))
	assert.False(t, lastEnvironment(context.Background(), c, "default", "demo", "uat"))
	assert.True(t, lastEnvironment(context.Background(), c, "default", "nope", "uat"), "unknown: ask")
}

// TestExitCode (C5): the exit code of an error.
//
// Covers CLI-VERBS-01.
func TestExitCode(t *testing.T) {
	assert.Equal(t, 0, ExitCode(nil))
	assert.Equal(t, 1, ExitCode(errors.New("x")))
	assert.Equal(t, ExitHeld, ExitCode(&exitError{code: ExitHeld}))
	assert.Equal(t, ExitTimeout, ExitCode(errors.Join(errors.New("a"), &exitError{code: ExitTimeout})))
	assert.Equal(t, []int{0, 1, 3, 4, 5, 6}, []int{ExitVerified, ExitFailed, ExitHeld, ExitWaitingForMerge, ExitReplaced, ExitTimeout},
		"the documented codes do not change")
}

var updateShipOutcomes = flag.Bool("update-ship-outcomes", false, "rewrite testdata/ship-outcomes.golden")

// TestShipOutcome (C5): how ship --wait reads a Bundle. It stops (3) only
// where a person is needed: an approval gate, a hold, a pause, a gate that
// does not pass and does not open by itself (one on a Bundle label, whatever
// its recheckInterval: the CRD gives every gate one). A gate that opens by
// itself (a soak, schedule, upstream or metric expression) and a PR the SCM
// merges by itself are waited for; --stop-at-gate stops at any gate. A PR
// that needs a person is 4, a deleted Bundle 5. Each case's line is golden
// (testdata/ship-outcomes.golden).
//
// Covers CLI-VERBS-01.
func TestShipOutcome(t *testing.T) {
	at := time.Now().Add(-time.Hour)
	withEnvs := func(envs ...v1alpha1.PipelineEnvironmentStatus) *v1alpha1.Pipeline {
		p := policyPipeline("demo", "test", "prod")
		p.Status.Environments = envs
		return p
	}
	gate := func(mut func(*v1alpha1.PolicyGate)) *v1alpha1.PolicyGate {
		g := explainGateInstance("demo", "b1", "prod", "cab", "bundle.labels.ok == 'yes'", false, true, "false")
		if mut != nil {
			mut(g)
		}
		return g
	}
	blocked := withEnvs(pesEnv("test", "b1", "", "Verified"), pesEnv("prod", "b0", "b1", "Pending", "cab"))
	paused := blocked.DeepCopy()
	paused.Spec.Paused = true
	autoPR := withEnvs(pesEnv("test", "b1", "", "Verified"), pesPR(pesEnv("prod", "b0", "b1", "WaitingForMerge"), "https://git/pr/7"))
	autoPR.Spec.Environments[1].PR = &v1alpha1.PRConfig{Merge: &v1alpha1.PRMergeConfig{Auto: true}}
	prStep := func(autoMerge string) *v1alpha1.PromotionStep {
		s := explainStep("demo", "b1", "prod", "WaitingForMerge", "", at)
		s.Status.Outputs = map[string]string{"prAutoMerge": autoMerge}
		return s
	}
	cases := []struct {
		name       string
		phase      string
		objs       []sigs_client.Object
		stopAtGate bool
		result     string
		code       int
	}{
		{"verified", "Verified", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "Verified"), pesEnv("prod", "b1", "", "Verified"))}, false, "Verified", ExitVerified},
		{"failed", "Failed", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "Failed"))}, false, "Failed", ExitFailed},
		{"superseded", "Superseded", []sigs_client.Object{withEnvs()}, false, "Superseded", ExitReplaced},
		{"rejected", "Rejected", []sigs_client.Object{withEnvs()}, false, "Rejected", ExitReplaced},
		{"approval gate", "Promoting", []sigs_client.Object{blocked, gate(func(g *v1alpha1.PolicyGate) {
			g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
			g.Spec.RecheckInterval = "1m"
		})}, false, "Held", ExitHeld},
		{"label gate: a person", "Promoting", []sigs_client.Object{blocked, gate(nil)}, false, "Held", ExitHeld},
		{"label gate with a recheckInterval (every gate has one): a person", "Promoting", []sigs_client.Object{blocked,
			gate(func(g *v1alpha1.PolicyGate) { g.Spec.RecheckInterval = "5m" })}, false, "Held", ExitHeld},
		{"upstream gate: waited for", "Promoting", []sigs_client.Object{blocked, gate(func(g *v1alpha1.PolicyGate) { g.Spec.Expression = "upstream.uat.verified" })}, false, "", 0},
		{"metric gate: waited for", "Promoting", []sigs_client.Object{blocked, gate(func(g *v1alpha1.PolicyGate) { g.Spec.Expression = `metrics["errors"].result == "Pass"` })}, false, "", 0},
		{"soak gate: waited for", "Promoting", []sigs_client.Object{blocked, gate(func(g *v1alpha1.PolicyGate) { g.Spec.Expression = "upstream.uat.soakMinutes >= 30" })}, false, "", 0},
		{"schedule gate: waited for", "Promoting", []sigs_client.Object{blocked, gate(func(g *v1alpha1.PolicyGate) { g.Spec.Expression = "!schedule.isWeekend" })}, false, "", 0},
		{"soak gate with --stop-at-gate", "Promoting", []sigs_client.Object{blocked, gate(func(g *v1alpha1.PolicyGate) { g.Spec.Expression = "upstream.uat.soakMinutes >= 30" })}, true, "Held", ExitHeld},
		{"a gate not evaluated yet does not stop it", "Promoting", []sigs_client.Object{blocked, gate(func(g *v1alpha1.PolicyGate) { g.Status.LastEvaluatedAt = nil })}, true, "", 0},
		{"held", "Promoting", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "Verified"), pesHeld("prod", "b0", "b1"))}, false, "Held", ExitHeld},
		{"paused", "Promoting", []sigs_client.Object{paused}, false, "Held", ExitHeld},
		{"its PR needs a person", "Promoting", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "Verified"),
			pesPR(pesEnv("prod", "b0", "b1", "WaitingForMerge"), "https://git/pr/7"))}, false, "WaitingForMerge", ExitWaitingForMerge},
		{"its PR merges by itself: waited for", "Promoting", []sigs_client.Object{autoPR, prStep("enabled")}, false, "", 0},
		{"its auto-merge failed", "Promoting", []sigs_client.Object{autoPR, prStep("failed")}, false, "WaitingForMerge", ExitWaitingForMerge},
		{"moving in one environment, held in another", "Promoting", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "HealthChecking"),
			pesEnv("prod", "b0", "b1", "Pending", "cab")), gate(nil)}, false, "", 0},
		{"between steps", "Promoting", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "Verified"))}, false, "", 0},
	}
	var golden strings.Builder
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]sigs_client.Object{explainBundle("b1", tc.phase, at)}, tc.objs...)
			out, _, err := shipOutcome(context.Background(), policyClient(t, objs...), "default", "demo", "b1", tc.stopAtGate)
			require.NoError(t, err)
			if tc.result == "" {
				assert.Nil(t, out)
				fmt.Fprintf(&golden, "%s: waits\n", tc.name)
				return
			}
			require.NotNil(t, out)
			assert.Equal(t, tc.result, out.result, out.message)
			assert.Equal(t, tc.code, out.code)
			fmt.Fprintf(&golden, "%s: %d %s: %s\n", tc.name, out.code, out.result, out.message)
		})
	}

	// A Bundle deleted while it is followed.
	out, _, err := shipOutcome(context.Background(), policyClient(t, blocked), "default", "demo", "b1", false)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, []any{"Deleted", ExitReplaced, true}, []any{out.result, out.code, out.final})
	fmt.Fprintf(&golden, "deleted: %d %s: %s\n", out.code, out.result, out.message)

	path := filepath.Join("testdata", "ship-outcomes.golden")
	if *updateShipOutcomes {
		require.NoError(t, os.WriteFile(path, []byte(golden.String()), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), golden.String())
}

// pesEnv is a status.environments entry for the tests.
func pesEnv(name, bundle, incoming, state string, blockedBy ...string) v1alpha1.PipelineEnvironmentStatus {
	return v1alpha1.PipelineEnvironmentStatus{Name: name, Bundle: bundle, IncomingBundle: incoming, State: state, BlockedBy: blockedBy}
}

func pesHeld(name, bundle, incoming string) v1alpha1.PipelineEnvironmentStatus {
	e := pesEnv(name, bundle, incoming, "Pending")
	e.Held = true
	return e
}

func pesPR(e v1alpha1.PipelineEnvironmentStatus, url string) v1alpha1.PipelineEnvironmentStatus {
	e.PRURL = url
	return e
}

// TestShipWait (C5): ship --wait follows the Bundle it created and ends with
// the outcome on stdout (JSON with -o json) and its exit code; a stop needs
// two reads that agree.
//
// Covers CLI-VERBS-01.
func TestShipWait(t *testing.T) {
	prevPoll, prevErr := shipPollInterval, errOut
	shipPollInterval = time.Millisecond
	var progress bytes.Buffer
	errOut = &progress
	t.Cleanup(func() { shipPollInterval, errOut, globalOutput = prevPoll, prevErr, "" })
	stubIdentity(t, Identity{Username: "ci"})

	p := policyPipeline("demo", "test", "prod")
	c := policyClient(t, p)
	opts := createBundleOptions{Images: []string{"ghcr.io/o/app:1.5.0"}, Type: "image"}

	// The controller's part: once the Bundle exists, it is Verified.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			var bl v1alpha1.BundleList
			_ = c.List(ctx, &bl)
			for i := range bl.Items {
				b := bl.Items[i]
				var pl = p.DeepCopy()
				_ = c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo"}, pl)
				pl.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "test", Bundle: b.Name, State: "Verified"},
					{Name: "prod", Bundle: b.Name, State: "Verified"}}
				_ = c.Update(ctx, pl)
				b.Status.Phase = "Verified"
				_ = c.Update(ctx, &b)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	var out bytes.Buffer
	require.NoError(t, shipFn(context.Background(), &out, c, "default", "demo", opts, true, time.Minute, false))
	assert.Equal(t, "1.5.0 is live in test, prod\n", out.String())
	assert.Contains(t, progress.String(), "following it")

	// Held at an evaluated gate: exit 3, JSON.
	cancel()
	globalOutput = "json"
	b := explainBundle("demo-held", "Promoting", time.Now())
	b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "1.6.0"}}
	held := policyPipeline("demo", "test", "prod")
	held.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "test", Bundle: "demo-held", State: "Verified"},
		{Name: "prod", Bundle: "x", IncomingBundle: "demo-held", State: "Pending", BlockedBy: []string{"cab"}}}
	c = policyClient(t, held, b, explainGateInstance("demo", "demo-held", "prod", "cab", "false", false, true, "no"))
	res := waitShip(context.Background(), c, "default", "demo", "demo-held", shipResult{createdBundle: createdOf(b)}, time.Minute, false)
	assert.Equal(t, "Held", res.Result)
	assert.Equal(t, ExitHeld, res.ExitCode)
	assert.Equal(t, "Version 1.6.0 (demo-held) stopped: prod is held by gate cab, which does not open by itself (kardinal why demo prod)", res.Message)
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	for _, field := range []string{`"bundle":"demo-held"`, `"result":"Held"`, `"exitCode":3`, `"version":"1.6.0"`, `"environments":[`} {
		assert.Contains(t, string(raw), field)
	}

	// --timeout.
	moving := policyPipeline("demo", "test", "prod")
	moving.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "test", Bundle: "demo-held", State: "HealthChecking"}}
	c = policyClient(t, moving, b)
	res = waitShip(context.Background(), c, "default", "demo", "demo-held", shipResult{}, 5*time.Millisecond, false)
	assert.Equal(t, []any{"Timeout", ExitTimeout}, []any{res.Result, res.ExitCode})

	// Interrupted (Ctrl-C, SIGTERM): the result, exit 6.
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	res = waitShip(ctx2, c, "default", "demo", "demo-held", shipResult{}, time.Minute, false)
	assert.Equal(t, []any{"Interrupted", ExitTimeout}, []any{res.Result, res.ExitCode})
	assert.Contains(t, res.Message, "(interrupted); the promotion goes on")

	// Reads that fail are retried; three in a row end the wait, exit 1.
	failing := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, sigs_client.WithWatch, sigs_client.ObjectKey, sigs_client.Object, ...sigs_client.GetOption) error {
			return errors.New("etcd is down")
		},
	}).Build()
	res = waitShip(context.Background(), failing, "default", "demo", "demo-held", shipResult{}, time.Minute, false)
	assert.Equal(t, []any{"Error", ExitFailed}, []any{res.Result, res.ExitCode})
	assert.Equal(t, "cannot follow version demo-held: get bundle demo-held: etcd is down", res.Message)
}

// TestHoldAndWhy (C5): kardinal hold holds an environment on what runs
// there, unhold releases it, and why is explain.
//
// Covers CLI-VERBS-01.
func TestHoldAndWhy(t *testing.T) {
	c := lcClient(t, lcPipeline(), lcBundle("app-v1", "app", "1", 0), lcStep("app-v1", "prod", "Verified", 5))
	var buf bytes.Buffer
	require.NoError(t, holdFn(&buf, c, "default", "app", "prod", "audit", "alice", 0))
	assert.Equal(t, "Held prod of app on app-v1: no other version promotes there until kardinal unhold app prod\n", buf.String())
	buf.Reset()
	require.NoError(t, holdFn(&buf, c, "default", "app", "prod", "audit", "alice", 0), "the same hold again is a no-op")
	assert.Equal(t, "Held prod of app is held on app-v1 already, for that reason: nothing changed\n", buf.String())
	assert.ErrorIs(t, holdFn(&buf, c, "default", "app", "prod", "another reason", "alice", 0), lifecycle.ErrConflict)
	var p v1alpha1.Pipeline
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "app"}, &p))
	require.Len(t, p.Spec.Holds, 1)
	assert.Equal(t, "app-v1", p.Spec.Holds[0].Bundle)
	assert.Empty(t, newBundles(t, c, "app-v1"), "nothing is rolled back")

	buf.Reset()
	require.NoError(t, releaseHoldFn(&buf, c, "default", "app", "prod"))

	globalOutput = "json"
	t.Cleanup(func() { globalOutput = "" })
	buf.Reset()
	require.NoError(t, holdFn(&buf, c, "default", "app", "prod", "audit", "alice", 2*time.Hour))
	var h heldEnvironment
	require.NoError(t, json.Unmarshal(buf.Bytes(), &h), buf.String())
	assert.Equal(t, []string{"app", "default", "prod", "app-v1", "audit", "alice"},
		[]string{h.Pipeline, h.Namespace, h.Environment, h.Bundle, h.Reason, h.CreatedBy})
	require.NotNil(t, h.ExpiresAt)

	why, _, err := NewRootCmd().Find([]string{"why"})
	require.NoError(t, err)
	assert.Equal(t, "explain", why.Name())
	for _, verb := range []string{"ship", "hold", "unhold"} {
		cmd, _, err := NewRootCmd().Find([]string{verb})
		require.NoError(t, err, verb)
		assert.NotEmpty(t, cmd.Example, verb)
	}
}

var updateShipCodes = flag.Bool("update-ship-codes", false, "rewrite testdata/ship-exit-codes.golden")

// TestShipExitCodes_Golden (C5): ship --wait's exit codes, as the reference
// page lists them, are pinned: a change to them shows here.
//
// Covers CLI-VERBS-01.
func TestShipExitCodes_Golden(t *testing.T) {
	got := shipExitCodeTable()
	path := filepath.Join("testdata", "ship-exit-codes.golden")
	if *updateShipCodes {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), got, "the exit codes of ship --wait are a contract")
	ship, _, err := NewRootCmd().Find([]string{"ship"})
	require.NoError(t, err)
	assert.Contains(t, ship.Annotations[ReferenceAnnotation], got, "the reference page shows them")
}

// TestShipTimeVariables (C5): the texts that make a gate one ship --wait
// waits for are listed, each with its variables, in the ship reference and in
// docs/ci-integration.md, in the same order.
//
// Covers CLI-VERBS-01.
func TestShipTimeVariables(t *testing.T) {
	ship, _, err := NewRootCmd().Find([]string{"ship"})
	require.NoError(t, err)
	assert.Contains(t, ship.Annotations[ReferenceAnnotation], timeVariableList())
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "ci-integration.md"))
	require.NoError(t, err)
	// The docs' table, without its code quotes, is the reference's list.
	plain := strings.ReplaceAll(string(doc), "`", "")
	for _, v := range timeVariables {
		assert.Contains(t, plain, "| "+v.match+" | "+v.vars+" |", "docs/ci-integration.md lists %s", v.match)
	}
}

var updateShipRechecks = flag.Bool("update-ship-rechecks", false, "rewrite testdata/ship-rechecks.golden")

// TestRechecks_Golden (C5 QA): which gate expressions ship --wait waits for
// (it opens by itself) and which need a person: QA's false positives (a
// string literal or a label that names a variable) need a person, QA's false
// negatives (an index, spaces) wait.
//
// Covers CLI-VERBS-02.
func TestRechecks_Golden(t *testing.T) {
	exprs := []string{
		// Read a variable that changes with time: waits.
		`!schedule.isWeekend && schedule.hour >= 9 && schedule.hour < 17`,
		`schedule["hour"] >= 9`,
		`schedule . hour >= 9`,
		`upstream.uat.soakMinutes >= 30`,
		`upstream["uat"]["verified"]`,
		`bundle.upstreamSoakMinutes >= 30`,
		`metrics["error-rate"].result == "Pass"`,
		`metrics.latency.value < "200"`,
		`!changewindow.freeze`,
		`!schedule.isWeekend && bundle.labels.approved == "yes"`,
		`(schedule.dayOfWeek != "Friday")`,
		// Do not: a person is needed.
		`bundle.labels.soakMinutes == "30"`,
		`bundle.labels.team == "schedule.ops"`,
		`bundle.provenance.author != "metrics-bot"`,
		`bundle.provenance.author != 'upstream.bot'`,
		`bundle.labels.schedule.x == "y"`,
		`bundle.labels["metrics"] == "on"`,
		`bundle.version.startsWith("changewindow.")`,
		`"""schedule.hour""" != ""`,
		`bundle.provenance.author != "dependabot[bot]"`,
		`true`,
	}
	var b strings.Builder
	for _, e := range exprs {
		what := "a person"
		if rechecks(&v1alpha1.PolicyGate{Spec: v1alpha1.PolicyGateSpec{Expression: e}}) {
			what = "waits"
		}
		fmt.Fprintf(&b, "%-9s %s\n", what, e)
	}
	path := filepath.Join("testdata", "ship-rechecks.golden")
	if *updateShipRechecks {
		require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), b.String())
}

// TestExplainHints (C5): explain and why print, on stderr, the command that
// gets past an evaluated gate that blocks; explain -w does not repeat them.
//
// Covers CLI-VERBS-01.
func TestExplainHints(t *testing.T) {
	var errb bytes.Buffer
	prev := errOut
	errOut = &errb
	t.Cleanup(func() { errOut = prev })
	at := time.Now().Add(-time.Hour)
	approval := explainGateInstance("demo", "b1", "prod", "cab", "true", false, true, "waiting for approvals: 0 of 1")
	approval.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1}
	c := policyClient(t, policyPipeline("demo", "test", "prod"), explainBundle("b1", "Promoting", at),
		explainStep("demo", "b1", "test", "Verified", "", at),
		explainGateInstance("demo", "b1", "prod", "freeze", "false", false, true, "false = false"), approval,
		explainGateInstance("demo", "b1", "prod", "fresh", "false", false, false, ""))
	var out bytes.Buffer
	require.NoError(t, explainHinted(&out, c, "default", "demo", "", false))
	assert.Equal(t, "Approve: kardinal approve demo prod\nOverride: kardinal override demo prod --gate freeze --reason <why>\n", errb.String(),
		"a gate not evaluated yet gets no hint")
	assert.NotContains(t, out.String(), "Override:", "hints are not on stdout")

	errb.Reset()
	require.NoError(t, explainOnce(&out, c, "default", "demo", "", false))
	assert.Empty(t, errb.String())
}

// syncBuffer is a bytes.Buffer safe to read while another goroutine writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestShipWait_Interrupt (C5): SIGINT during ship --wait ends the wait, not
// the process: the JSON result is printed and the exit code is 6.
//
// Covers CLI-VERBS-01.
func TestShipWait_Interrupt(t *testing.T) {
	prevPoll, prevErr := shipPollInterval, errOut
	shipPollInterval = 5 * time.Millisecond
	progress := &syncBuffer{}
	errOut = progress
	globalOutput = "json"
	t.Cleanup(func() { shipPollInterval, errOut, globalOutput = prevPoll, prevErr, "" })
	stubIdentity(t, Identity{Username: "ci"})
	c := policyClient(t, policyPipeline("demo", "test", "prod"))

	go func() {
		for !strings.Contains(progress.String(), "following it") {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()
	var out bytes.Buffer
	err := shipFn(context.Background(), &out, c, "default", "demo",
		createBundleOptions{Images: []string{"ghcr.io/o/app:1.5.0"}, Type: "image"}, true, time.Minute, false)
	require.Error(t, err)
	assert.Equal(t, ExitTimeout, ExitCode(err))
	var res shipResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res), out.String())
	assert.Equal(t, "Interrupted", res.Result)
	assert.Equal(t, ExitTimeout, res.ExitCode)
}

// TestApprove_PinAndConfirm (C5): approve says which version it records a
// decision on, refuses one --version does not name, and asks on a terminal
// (--yes answers).
//
// Covers CLI-VERBS-01.
func TestApprove_PinAndConfirm(t *testing.T) {
	stubIdentity(t, Identity{Username: "alice"})
	b := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "app-v2", Namespace: "default"},
		Spec:   v1alpha1.BundleSpec{Type: "image", Pipeline: "app", Images: []v1alpha1.ImageRef{{Repository: "r/app", Tag: "2.0"}}},
		Status: v1alpha1.BundleStatus{Phase: "Promoting"}}
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}}}}
	p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "prod", Bundle: "app-v1", IncomingBundle: "app-v2", State: "Pending"}}
	c := fake.NewClientBuilder().WithScheme(rejectScheme(t)).WithObjects(b, p).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	prevClient, prevPrompt := buildClient, promptable
	buildClient = func() (sigs_client.Client, string, error) { return c, "default", nil }
	t.Cleanup(func() { buildClient, promptable = prevClient, prevPrompt })
	run := func(stdin string, tty bool, args ...string) (string, string, error) {
		promptable = func() bool { return tty }
		root := NewRootCmd()
		var out, errb bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errb)
		root.SetIn(strings.NewReader(stdin))
		root.SetArgs(append([]string{"approve"}, args...))
		err := root.Execute()
		return out.String(), errb.String(), err
	}

	_, errOut, err := run("", false, "app", "prod", "--version", "1.9")
	assert.EqualError(t, err, "approve: the version waiting for prod is 2.0 (app-v2), not 1.9: nothing recorded")
	assert.Contains(t, errOut, "Version: 2.0 (app-v2)")
	assert.Empty(t, approvals(t, c))

	_, errOut, err = run("n\n", true, "app", "prod")
	assert.ErrorIs(t, err, errCancelled)
	assert.Contains(t, errOut, "Approve 2.0 (app-v2) for prod? [y/N] ")
	assert.Empty(t, approvals(t, c), "declined: nothing recorded")

	out, _, err := run("", true, "app", "prod", "--version", "2.0", "--yes")
	require.NoError(t, err)
	assert.Contains(t, out, "Recorded: alice approves app-v2 for prod")
	assert.Len(t, approvals(t, c), 1)
}
