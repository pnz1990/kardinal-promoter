// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
)

var updateOutput = flag.Bool("update-output", false, "rewrite testdata/output.golden")

// outputFixture is a pipeline in the middle of a promotion: 1.5.0 live in
// test, waiting for its PR in uat, held by a gate before prod (where 1.4.0
// runs), with an older Bundle superseded and a failed one, so every kind of
// state shows.
func outputFixture() []sigs_client.Object {
	at := func(m int) time.Time { return time.Now().Add(-time.Duration(m) * time.Minute).Truncate(time.Second) }
	since := metav1.NewTime(at(30))
	p := policyPipeline("demo", "test", "uat", "prod")
	p.CreationTimestamp = metav1.NewTime(at(600))
	p.Status.Phase = "Promoting"
	p.Status.Summary = "1.5.0 live in test; waiting for PR merge in uat (#42); held by gate no-weekend in prod"
	// The cluster's text has non-ASCII characters (the controller writes
	// dashes and arrows): plain output folds them.
	p.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{
		{Name: "test", Bundle: "demo-b3", Version: "1.5.0", State: "Verified", Since: &since},
		{Name: "uat", Bundle: "demo-b1", Version: "1.4.0", IncomingBundle: "demo-b3", IncomingVersion: "1.5.0",
			State: "WaitingForMerge", Since: &since, PRURL: "https://github.com/o/r/pull/42"},
		{Name: "prod", Bundle: "demo-b1", Version: "1.4.0", IncomingBundle: "demo-b3", IncomingVersion: "1.5.0",
			State: "Pending", BlockedBy: []string{"no-weekend"}, Since: &since},
	}
	b1 := explainBundle("demo-b1", "Verified", at(300))
	b2 := explainBundle("demo-b2", "Failed", at(200))
	b3 := explainBundle("demo-b3", "Promoting", at(60))
	for b, tag := range map[*v1alpha1.Bundle]string{b1: "1.4.0", b2: "1.4.1", b3: "1.5.0"} {
		b.Spec.Images = []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: tag}}
	}
	old := explainBundle("demo-b0", "Superseded", at(400))
	uat := explainStep("demo", "demo-b3", "uat", "WaitingForMerge", "waiting for the PR", at(20))
	uat.Status.PRURL = "https://github.com/o/r/pull/42"
	uat.Status.Steps = []v1alpha1.StepStatus{{Name: "git-clone", State: "Completed"}, {Name: "open-pr", State: "InProgress"}}
	gate := explainGateInstance("demo", "demo-b3", "prod", "no-weekend", "!schedule.isWeekend", false, true,
		"No deploys on weekends (!schedule.isWeekend = false)")
	gate.Spec.Message = "No deploys on weekends"
	pass := explainGateInstance("demo", "demo-b3", "uat", "soak", "true", true, true, "true = true")
	return []sigs_client.Object{p, b1, b2, b3, old, uat, gate, pass,
		explainStep("demo", "demo-b1", "test", "Verified", "", at(300)),
		explainStep("demo", "demo-b1", "uat", "Verified", "", at(290)),
		explainStep("demo", "demo-b1", "prod", "Verified", "", at(280)),
		explainStep("demo", "demo-b2", "test", "Failed", "health alarm — rollback Bundle demo-b2 → demo-b1 created", at(200)),
		explainStep("demo", "demo-b0", "test", "Superseded", "", at(400)),
		explainStep("demo", "demo-b3", "test", "Verified", "", at(50)),
	}
}

// outputCommands are the commands whose output the tests check: every
// command that prints states, tables or notices.
var outputCommands = [][]string{
	{"get", "pipelines"},
	{"get", "pipelines", "-A"},
	{"get", "pipelines", "demo"},
	{"get", "bundles", "demo"},
	{"get", "bundles", "-A"},
	{"get", "steps", "demo"},
	{"status", "demo"},
	{"explain", "demo"},
	{"explain", "demo", "prod"},
	{"history", "demo"},
	{"logs", "demo"},
	{"policy", "list"},
	{"policy", "simulate", "demo", "prod", "--time", "Saturday 3pm"},
}

// runOutput runs kardinal args against c and returns stdout and stderr.
func runOutput(t *testing.T, c sigs_client.Client, args ...string) (string, string, error) {
	t.Helper()
	prevClient, prevOutput := buildClient, globalOutput
	buildClient = func() (sigs_client.Client, string, error) { return c, "default", nil }
	t.Cleanup(func() { buildClient, globalOutput, sty = prevClient, prevOutput, style.Plain() })
	root := NewRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errb.String(), err
}

// asTerminal makes the commands render as for a terminal whose environment
// is env (key, value pairs; the rest of the environment is unset) until the
// returned func or the end of the test.
func asTerminal(t *testing.T, env ...string) (restore func()) {
	t.Helper()
	m := map[string]string{}
	for i := 0; i+1 < len(env); i += 2 {
		m[env[i]] = env[i+1]
	}
	prev := detectStyle
	detectStyle = func(io.Writer) style.Styler { return style.Terminal(func(k string) string { return m[k] }) }
	restore = func() { detectStyle = prev }
	t.Cleanup(restore)
	return restore
}

// volatile matches what the clock changes in the golden output: history
// timestamps and the age of a fixed time.
var volatile = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}|\b\d+d ago`)

// assertPlain fails when out has an ANSI escape or a non-ASCII character.
func assertPlain(t *testing.T, out, what string) {
	t.Helper()
	assert.NotContains(t, out, "\x1b", "%s: an ANSI escape:\n%q", what, out)
	for i, r := range out {
		if r >= 0x80 {
			assert.Failf(t, "non-ASCII output", "%s: %q at %d in:\n%s", what, r, i, out)
			return
		}
	}
}

// TestOutput_PlainWhenNotATerminal (C4): piped output, and a terminal with
// NO_COLOR or TERM=dumb, has no ANSI escape and no non-ASCII character in
// any command's stdout. The piped form is golden (testdata/output.golden).
//
// Covers CLI-STYLE-01.
func TestOutput_PlainWhenNotATerminal(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	// No UTF-8 locale: the stream takes ASCII only, the cluster's text is
	// folded.
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "C")
	c := policyClient(t, outputFixture()...)
	var golden strings.Builder
	for _, args := range outputCommands {
		line := strings.Join(args, " ")
		out, _, err := runOutput(t, c, args...)
		require.NoError(t, err, line)
		assertPlain(t, out, "piped: kardinal "+line)
		golden.WriteString("$ kardinal " + line + "\n" + volatile.ReplaceAllString(out, "<time>") + "\n")

		for _, env := range [][]string{{"NO_COLOR", "1", "LANG", "C"}, {"TERM", "dumb", "LANG", "en_US.UTF-8"}} {
			restore := asTerminal(t, env...)
			out, _, err := runOutput(t, c, args...)
			restore()
			require.NoError(t, err, line)
			assertPlain(t, out, env[0]+": kardinal "+line)
			assert.NotContains(t, out, "\x1b", "no colour")
		}
	}

	path := filepath.Join("testdata", "output.golden")
	if *updateOutput {
		require.NoError(t, os.WriteFile(path, []byte(golden.String()), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run go test -run TestOutput_PlainWhenNotATerminal -update-output")
	assert.Equal(t, string(want), golden.String(), "piped output changed; rerun with -update-output if intended")
}

// TestOutput_UTF8Pipe (C4): with a UTF-8 locale, a pipe keeps the cluster's
// text as it is; the CLI's own words stay plain: no colour, no glyphs.
//
// Covers CLI-STYLE-01.
func TestOutput_UTF8Pipe(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	t.Setenv("LC_ALL", "")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("TERM", "xterm")
	c := policyClient(t, outputFixture()...)
	out, _, err := runOutput(t, c, "logs", "demo")
	require.NoError(t, err)
	assert.Contains(t, out, "health alarm — rollback Bundle demo-b2 → demo-b1 created", "the cluster's text, unchanged")
	assert.Contains(t, out, "[Failed]", "a label, no glyph")
	assert.NotContains(t, out, "\x1b")

	t.Setenv("TERM", "dumb")
	out, _, err = runOutput(t, c, "logs", "demo")
	require.NoError(t, err)
	assert.Contains(t, out, "health alarm - rollback Bundle demo-b2 -> demo-b1 created", "TERM=dumb folds")
}

// placeholders are the table's display placeholders: JSON never carries
// them.
var placeholders = map[string]bool{"-": true, "--": true, "...": true, "—": true, "none": true}

// walkJSON calls f on every string in v.
func walkJSON(v any, path string, f func(path, s string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			walkJSON(e, path+"."+k, f)
		}
	case []any:
		for i, e := range x {
			walkJSON(e, fmt.Sprintf("%s[%d]", path, i), f)
		}
	case string:
		f(path, x)
	}
}

// TestOutput_JSONHasNoPlaceholders (C4): no -o json output carries a table
// placeholder ("-", "--", "...", "—", "none") as a value.
//
// Covers CLI-STYLE-01.
func TestOutput_JSONHasNoPlaceholders(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	c := policyClient(t, outputFixture()...)
	for _, args := range [][]string{
		{"status", "-o", "json"}, {"status", "demo", "-o", "json"}, {"explain", "demo", "-o", "json"},
		{"history", "demo", "-o", "json"}, {"get", "pipelines", "-o", "json"}, {"get", "bundles", "-o", "json"},
		{"get", "steps", "demo", "-o", "json"},
		{"create", "bundle", "demo", "--image", "ghcr.io/o/app:1.6.0", "--dry-run", "-o", "json"},
		{"create", "bundle", "demo", "--image", "ghcr.io/o/app:1.6.0", "-o", "json"},
		{"ship", "demo", "--image", "ghcr.io/o/app:1.7.0", "-o", "json"},
	} {
		out, _, err := runOutput(t, c, args...)
		require.NoError(t, err, args)
		var v any
		require.NoError(t, json.Unmarshal([]byte(out), &v), "%v: %s", args, out)
		walkJSON(v, "", func(path, s string) {
			assert.False(t, placeholders[strings.TrimSpace(s)], "%v: %s is the placeholder %q", args, path, s)
		})
	}
}

// TestOutput_Terminal (C4): on a terminal the states have their glyph and
// tone colour, and the columns still line up.
//
// Covers CLI-STYLE-01.
func TestOutput_Terminal(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	c := policyClient(t, outputFixture()...)
	asTerminal(t, "LANG", "en_US.UTF-8")
	out, _, err := runOutput(t, c, "status", "demo")
	require.NoError(t, err)
	assert.Contains(t, out, "\x1b[32m● Live\x1b[0m")
	assert.Contains(t, out, "\x1b[34m◷ Waiting for PR merge\x1b[0m")
	assert.Contains(t, out, "\x1b[33m⏸ Held by gate no-weekend\x1b[0m")

	plain, _, err := runOutput(t, c, "status", "demo")
	require.NoError(t, err)
	lines := strings.Split(style.Strip(plain), "\n")
	header := lines[3]
	require.True(t, strings.HasPrefix(header, "ENVIRONMENT"), plain)
	col := strings.Index(header, "SINCE")
	for _, l := range lines[4:7] {
		assert.Equal(t, "30m", strings.Fields(string([]rune(l)[col:]))[0], "SINCE lines up under its header:\n%s", plain)
	}

	asTerminal(t, "LANG", "C")
	out, _, err = runOutput(t, c, "get", "bundles", "demo")
	require.NoError(t, err)
	assert.Regexp(t, "\x1b\\[1;97;4[0-7]mx Failed\x1b\\[0m", out, "ASCII glyph without a UTF-8 locale, failed filled")
}

// TestOutput_JSON (C4): status, explain, history, create bundle, promote and
// rollback print JSON with -o json; the created Bundle's name is .bundle,
// and warnings go to stderr, so stdout stays valid JSON.
//
// Covers CLI-STYLE-01.
func TestOutput_JSON(t *testing.T) {
	t.Setenv("KUBECONFIG", "/dev/null")
	t.Setenv("KARDINAL_UI_URL", "")
	c := policyClient(t, outputFixture()...)
	for _, args := range [][]string{
		{"status", "-o", "json"},
		{"status", "demo", "-o", "json"},
		{"explain", "demo", "-o", "json"},
		{"history", "demo", "-o", "json"},
		{"create", "bundle", "demo", "--image", "ghcr.io/o/app:1.6.0", "--dry-run", "-o", "json"},
	} {
		out, _, err := runOutput(t, c, args...)
		require.NoError(t, err, args)
		assert.True(t, json.Valid([]byte(out)), "%v: %s", args, out)
	}

	out, stderr, err := runOutput(t, c, "create", "bundle", "demo", "--image", "ghcr.io/o/app:1.6.0", "-o", "json")
	require.NoError(t, err)
	var created createdBundle
	require.NoError(t, json.Unmarshal([]byte(out), &created), out)
	assert.True(t, strings.HasPrefix(created.Bundle, "demo-"), out)
	assert.Equal(t, createdBundle{Bundle: created.Bundle, Pipeline: "demo", Namespace: "default", Type: "image", Version: "1.6.0",
		URL: "http://localhost:8082/ui/#pipeline=demo&ns=default"}, created)
	assert.Contains(t, stderr, "warning: the version records no creator", "the fake API server says nobody: a warning, on stderr")

	var view pipelineStatus
	out, _, err = runOutput(t, c, "status", "demo", "-o", "yaml")
	require.NoError(t, err)
	assert.Contains(t, out, "source: controller\n")

	out, _, err = runOutput(t, c, "explain", "demo", "prod", "-o", "json")
	require.NoError(t, err)
	var ex explainOutput
	require.NoError(t, json.Unmarshal([]byte(out), &ex), out)
	require.NotEmpty(t, ex.Entries)
	assert.Equal(t, explainEntry{Environment: "prod", Bundle: "demo-b3", Type: "PolicyGate", Name: "no-weekend", State: "Waiting",
		Expression: "!schedule.isWeekend", Reason: "No deploys on weekends (!schedule.isWeekend = false)"}, ex.Entries[0])

	out, _, err = runOutput(t, c, "history", "demo", "uat", "-o", "json")
	require.NoError(t, err)
	var hist []historyEntry
	require.NoError(t, json.Unmarshal([]byte(out), &hist), out)
	require.Len(t, hist, 2)
	assert.Equal(t, "WaitingForMerge", hist[0].State)
	assert.Equal(t, "https://github.com/o/r/pull/42", hist[0].PRURL)

	out, _, err = runOutput(t, c, "status", "demo", "-o", "json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(out), &view), out)
	assert.Equal(t, "Pending", view.Environments[2].State)
}

// TestExplainWatch_JSON (C4): explain -w -o json prints JSON documents only:
// no footer, no screen clear, and its errors on stderr.
//
// Covers CLI-STYLE-01.
func TestExplainWatch_JSON(t *testing.T) {
	globalOutput = "json"
	var errb bytes.Buffer
	prevErr := errOut
	errOut = &errb
	t.Cleanup(func() { globalOutput, errOut = "", prevErr })
	c := policyClient(t, outputFixture()...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	require.NoError(t, explainWatch(ctx, &out, c, "default", "demo", "", false, time.Millisecond))
	assert.True(t, json.Valid(out.Bytes()), out.String())
	assert.NotContains(t, out.String(), "watching")

	out.Reset()
	require.NoError(t, explainWatch(ctx, &out, c, "default", "nope", "", false, time.Millisecond))
	assert.Empty(t, out.String(), "the error is not on stdout")
	assert.Contains(t, errb.String(), `error: pipeline "nope" not found`)
}

// TestWatch_YAMLDocuments (C4): -o yaml -w separates its documents with
// "---", so the stream parses as YAML documents.
//
// Covers CLI-STYLE-01.
func TestWatch_YAMLDocuments(t *testing.T) {
	globalOutput = "yaml"
	t.Cleanup(func() { globalOutput = "" })
	c := policyClient(t, outputFixture()...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	require.NoError(t, explainWatch(ctx, &out, c, "default", "demo", "", false, time.Millisecond))
	assert.True(t, strings.HasPrefix(out.String(), "---\n"), out.String())
	assert.Contains(t, out.String(), "\npipeline: demo\n")

	out.Reset()
	n := 0
	require.NoError(t, watchLoop(ctx, &out, time.Millisecond, func() error {
		n++
		return statusPipelineWriter(&out, c, "default", "demo")
	}))
	assert.Equal(t, 1, n)
	assert.True(t, strings.HasPrefix(out.String(), "---\n"), out.String())
	assert.NotContains(t, out.String(), "watching")
}
