// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
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
		err := confirm(cmd, "Delete Bundle b?")
		return errb.String(), err
	}
	asked, err := run(true, "y\n")
	require.NoError(t, err)
	assert.Equal(t, "Delete Bundle b? [y/N] ", asked)
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

// TestShipOutcome (C5): how ship --wait reads a Bundle: Verified, Failed,
// replaced, held by a gate that was evaluated (not one that was not), held
// by a rollback, waiting for its PR, or still moving.
//
// Covers CLI-VERBS-01.
func TestShipOutcome(t *testing.T) {
	at := time.Now().Add(-time.Hour)
	withEnvs := func(envs ...v1alpha1.PipelineEnvironmentStatus) sigs_client.Object {
		p := policyPipeline("demo", "test", "prod")
		p.Status.Environments = envs
		return p
	}
	evaluated := explainGateInstance("demo", "b1", "prod", "cab", "false", false, true, "false = false")
	fresh := explainGateInstance("demo", "b1", "prod", "cab", "false", false, false, "")
	cases := []struct {
		name   string
		phase  string
		objs   []sigs_client.Object
		result string
		code   int
	}{
		{"verified", "Verified", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "Verified"), pesEnv("prod", "b1", "", "Verified"))}, "Verified", ExitVerified},
		{"failed", "Failed", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "Failed"))}, "Failed", ExitFailed},
		{"superseded", "Superseded", []sigs_client.Object{withEnvs()}, "Superseded", ExitReplaced},
		{"rejected", "Rejected", []sigs_client.Object{withEnvs()}, "Rejected", ExitReplaced},
		{"held by an evaluated gate", "Promoting", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "Verified"),
			pesEnv("prod", "b0", "b1", "Pending", "cab")), evaluated}, "Held", ExitHeld},
		{"a gate not evaluated yet does not stop it", "Promoting", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "Verified"),
			pesEnv("prod", "b0", "b1", "Pending", "cab")), fresh}, "", 0},
		{"held by a rollback", "Promoting", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "Verified"),
			pesHeld("prod", "b0", "b1"))}, "Held", ExitHeld},
		{"waiting for its PR", "Promoting", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "Verified"),
			pesPR(pesEnv("prod", "b0", "b1", "WaitingForMerge"), "https://git/pr/7"))}, "WaitingForMerge", ExitWaitingForMerge},
		{"moving in one environment, held in another", "Promoting", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "HealthChecking"),
			pesEnv("prod", "b0", "b1", "Pending", "cab")), evaluated}, "", 0},
		{"between steps", "Promoting", []sigs_client.Object{withEnvs(pesEnv("test", "b1", "", "Verified"))}, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]sigs_client.Object{explainBundle("b1", tc.phase, at)}, tc.objs...)
			out, _ := shipOutcome(context.Background(), policyClient(t, objs...), "default", "demo", "b1")
			if tc.result == "" {
				assert.Nil(t, out)
				return
			}
			require.NotNil(t, out)
			assert.Equal(t, tc.result, out.result, out.message)
			assert.Equal(t, tc.code, out.code)
		})
	}
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
				b.Status.Phase = "Verified"
				_ = c.Update(ctx, &b)
				var pl = p.DeepCopy()
				_ = c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo"}, pl)
				pl.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "test", Bundle: b.Name, State: "Verified"},
					{Name: "prod", Bundle: b.Name, State: "Verified"}}
				_ = c.Update(ctx, pl)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	var out bytes.Buffer
	require.NoError(t, shipFn(context.Background(), &out, c, "default", "demo", opts, true, time.Minute))
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
	res := waitShip(context.Background(), c, "default", "demo", "demo-held", shipResult{createdBundle: createdOf(b)}, time.Minute)
	assert.Equal(t, "Held", res.Result)
	assert.Equal(t, ExitHeld, res.ExitCode)
	assert.Equal(t, "Bundle demo-held stopped: prod is held by gate cab. Why: kardinal why demo prod", res.Message)
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	for _, field := range []string{`"bundle":"demo-held"`, `"result":"Held"`, `"exitCode":3`, `"version":"1.6.0"`, `"environments":[`} {
		assert.Contains(t, string(raw), field)
	}

	// --timeout.
	moving := policyPipeline("demo", "test", "prod")
	moving.Status.Environments = []v1alpha1.PipelineEnvironmentStatus{{Name: "test", Bundle: "demo-held", State: "HealthChecking"}}
	c = policyClient(t, moving, b)
	res = waitShip(context.Background(), c, "default", "demo", "demo-held", shipResult{}, 5*time.Millisecond)
	assert.Equal(t, []any{"Timeout", ExitTimeout}, []any{res.Result, res.ExitCode})
}

// TestHoldAndWhy (C5): kardinal hold holds an environment on what runs
// there, unhold releases it, and why is explain.
//
// Covers CLI-VERBS-01.
func TestHoldAndWhy(t *testing.T) {
	c := lcClient(t, lcPipeline(), lcBundle("app-v1", "app", "1", 0), lcStep("app-v1", "prod", "Verified", 5))
	var buf bytes.Buffer
	require.NoError(t, holdFn(&buf, c, "default", "app", "prod", "audit", "alice", 0))
	assert.Equal(t, "Held prod of app on app-v1: no other Bundle promotes there until kardinal unhold app prod\n", buf.String())
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
