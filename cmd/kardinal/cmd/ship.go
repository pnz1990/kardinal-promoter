// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	pipelinerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

// The exit codes of kardinal ship --wait. They are part of the CLI's
// contract (docs/cli-reference: kardinal ship), so a CI job can branch on
// them; a new outcome gets a new code, an existing code keeps its meaning.
const (
	// ExitVerified: the Bundle is Verified in every environment it promotes.
	ExitVerified = 0
	// ExitFailed: the Bundle failed, or the command failed.
	ExitFailed = 1
	// ExitHeld: the Bundle stopped where a gate that was evaluated and is
	// not passing holds it, or a rollback hold keeps it out.
	ExitHeld = 3
	// ExitWaitingForMerge: the Bundle waits for a person to merge its PR.
	ExitWaitingForMerge = 4
	// ExitReplaced: a newer Bundle superseded it, or it was rejected.
	ExitReplaced = 5
	// ExitTimeout: --timeout passed first.
	ExitTimeout = 6
)

// exitError ends the command with an exit code other than 1. Its message,
// when not empty, is printed on stderr like any error; the outcome itself
// has been written on stdout.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// ExitCode is the process exit code for the error a command returned: 0
// for nil, an exitError's code, else 1.
func ExitCode(err error) int {
	var ee *exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.code
	}
	return 1
}

// shipPollInterval is how often ship --wait reads the Bundle; a test
// shortens it.
var shipPollInterval = 2 * time.Second

func newShipCmd() *cobra.Command {
	var (
		opts       createBundleOptions
		dryRun     bool
		wait       bool
		stopAtGate bool
		quiet      bool
		timeout    = 30 * time.Minute
	)
	cmd := &cobra.Command{
		Use:   "ship <pipeline> --image <ref>",
		Short: "Ship a version: create a Bundle, and with --wait follow it",
		Long: `Ship a version through <pipeline>: the same Bundle as kardinal create bundle,
with the same flags. --wait follows it until it is live everywhere it goes
(exit 0), fails (1), stops at a gate or a hold (3), waits for its PR to be
merged (4), is replaced or rejected (5), or --timeout passes (6).`,
		Example: `  kardinal ship my-app --image ghcr.io/org/my-app:1.5.0
  kardinal ship my-app --image ghcr.io/org/my-app:1.5.0 --wait --timeout 1h

  # in CI: the Bundle's name and the outcome as JSON
  kardinal ship my-app --image "$IMAGE" --wait -o json | jq -r .result`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if quiet && !wait {
				return fmt.Errorf("ship: --quiet quiets --wait's progress; add --wait")
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("ship: %w", err)
			}
			if dryRun {
				if wait {
					return fmt.Errorf("ship: --dry-run creates nothing to --wait for; drop one of them")
				}
				return createBundleDryRun(cmd.OutOrStdout(), c, ns, args[0], opts)
			}
			return shipFn(cmd.Context(), cmd.OutOrStdout(), c, ns, args[0], opts, wait, timeout, stopAtGate, quiet)
		},
	}
	addBundleFlags(cmd, &opts, &dryRun)
	cmd.Flags().BoolVar(&wait, "wait", false, "Follow the version until it is live, fails, stops at a gate, a hold or a PR, or is replaced")
	cmd.Flags().Var(durationValue{&timeout}, "timeout", "With --wait: give up after this long (exit 6)")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "With --wait: print only the last line (or -o's document); no progress, PR links or summary on stderr")
	cmd.Flags().BoolVar(&stopAtGate, "stop-at-gate", false, "With --wait: stop (exit 3) at any gate that does not pass, also one that opens by itself (a soak, a schedule)")
	withReference(cmd, shipReference)
	return withStructuredOutput(cmd)
}

// shipResult is ship -o json|yaml: the Bundle created, and with --wait how
// it ended.
type shipResult struct {
	createdBundle
	// Result is what --wait saw last: Verified, Failed, Held,
	// WaitingForMerge, Superseded, Rejected or Timeout; empty without --wait.
	Result string `json:"result,omitempty"`
	// ExitCode is the process's exit code (ExitVerified ... ExitTimeout).
	ExitCode int `json:"exitCode"`
	// Message says where it stopped and why.
	Message string `json:"message,omitempty"`
	// Environments are the Bundle's environments of status.environments:
	// the ones it reached or waits to enter.
	Environments []v1alpha1.PipelineEnvironmentStatus `json:"environments,omitempty"`
}

// shipFn creates the Bundle and, with wait, follows it.
func shipFn(ctx context.Context, w io.Writer, c sigs_client.Client, ns, pipeline string, opts createBundleOptions,
	wait bool, timeout time.Duration, stopAtGate, quiet bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	b, err := newBundle(ctx, c, ns, pipeline, opts)
	if err != nil {
		return err
	}
	res := shipResult{createdBundle: createdOf(b)}
	if !wait {
		if structured() {
			return writeStructured(w, res)
		}
		_, _ = fmt.Fprintf(w, "Shipped %s as %s\n", versionOr(res.Version, b.Name), b.Name)
		_, _ = fmt.Fprintf(errOut, "%s kardinal status %s -w\n", sty.Dim("Watch:"), pipeline)
		return nil
	}
	// Ctrl-C or SIGTERM ends the wait with its result (exit 6), not the
	// promotion.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	progress := &shipProgress{w: errOut, timed: true, start: shipNow(), created: b.CreationTimestamp.Time}
	if quiet {
		progress = &shipProgress{}
	} else {
		_, _ = fmt.Fprintf(errOut, "Shipped %s as %s; following it (Ctrl-C stops following, the promotion goes on)\n",
			versionOr(res.Version, b.Name), b.Name)
	}
	res = followShip(ctx, c, ns, pipeline, b.Name, res, timeout, stopAtGate, progress)
	progress.timing(ctx, c, b)
	progress.summary(res.Environments)
	if structured() {
		if err := writeStructured(w, res); err != nil {
			return err
		}
	} else {
		_, _ = fmt.Fprintln(w, res.Message)
	}
	if res.ExitCode == ExitVerified {
		return nil
	}
	return &exitError{code: res.ExitCode}
}

// versionOr is version, or name without one.
func versionOr(version, name string) string {
	if version == "" {
		return name
	}
	return version
}

// versionRef names a shipped version in a sentence: "version 1.5.0
// (app-x7k2p)", its tag and its Bundle, or "version app-x7k2p" when it has no
// other name. upperFirst starts a sentence with it.
func versionRef(version, name string) string {
	if version == "" || version == name {
		return "version " + name
	}
	return "version " + version + " (" + name + ")"
}

// waitShip follows Bundle name until it ends (shipOutcome), timeout passes
// or ctx is done (Ctrl-C, SIGTERM), printing each new state of its
// environments on stderr. A read that fails is tried again, after a longer
// pause each time; three in a row end the wait with the error. stopAtGate
// stops at any evaluated gate that does not pass (--stop-at-gate).
func waitShip(ctx context.Context, c sigs_client.Client, ns, pipeline, name string, res shipResult, timeout time.Duration, stopAtGate bool) shipResult {
	return followShip(ctx, c, ns, pipeline, name, res, timeout, stopAtGate, &shipProgress{w: errOut, start: shipNow()})
}

// shipNow is the clock of ship --wait's progress; a test replaces it.
var shipNow = time.Now

// shipProgress is what ship --wait prints on stderr while it follows a
// Bundle: each environment's new state (with the time since the start when
// timed), the URL of each PR it opens, and with timed a summary at the end.
// A nil w prints nothing (--quiet).
type shipProgress struct {
	w     io.Writer
	timed bool
	start time.Time
	// created is when the Bundle was created (the summary's "took" is
	// measured from it), start when it is not known.
	created time.Time
	// seen is each environment's last state shown; changed when this
	// follow saw it change; pr its PR shown.
	seen    map[string]string
	changed map[string]time.Time
	pr      map[string]string
	// started is when the Bundle's promotion began in each environment;
	// upstreams each environment's direct upstreams (timing fills both).
	started   map[string]time.Time
	upstreams map[string][]string
}

// observe prints what changed in envs since the last read.
func (p *shipProgress) observe(envs []v1alpha1.PipelineEnvironmentStatus) {
	if p.seen == nil {
		p.seen, p.changed, p.pr = map[string]string{}, map[string]time.Time{}, map[string]string{}
	}
	now := shipNow()
	for _, e := range envs {
		text := sty.State(envState(e))
		if p.seen[e.Name] != text {
			p.seen[e.Name] = text
			p.changed[e.Name] = now
			p.printf("%s: %s", e.Name, text)
		}
		if e.PRURL != "" && p.pr[e.Name] != e.PRURL {
			p.pr[e.Name] = e.PRURL
			p.printf("%s: PR %s", e.Name, e.PRURL)
		}
	}
}

// printf prints one progress line, after the time since the start when
// timed.
func (p *shipProgress) printf(format string, args ...any) {
	if p.w == nil {
		return
	}
	line := fmt.Sprintf(format, args...)
	if p.timed {
		line = fmt.Sprintf("%7s  %s", "+"+shortDuration(shipNow().Sub(p.start)), line)
	}
	_, _ = fmt.Fprintln(p.w, "  "+line)
}

// summary prints, when timed, how long the follow took and each
// environment's last state: how long its own promotion took ("took"), or
// how long it has been in its state ("for"). An environment's state began at
// the controller's status.environments[].since; at what this follow saw when
// the controller does not write it (nil or zero), and is unknown when this
// follow did not see it either. Its own promotion began when its
// PromotionStep's first step started (started), else when its last upstream
// went live (upstreams; the environment before it when not known), else when
// the Bundle was created. Every time is clamped to [created, now], so a
// clock skew or a stale since never prints a negative or enormous duration.
func (p *shipProgress) summary(envs []v1alpha1.PipelineEnvironmentStatus) {
	if p.w == nil || !p.timed || len(envs) == 0 {
		return
	}
	now := shipNow()
	created := p.created
	if created.IsZero() || created.After(now) {
		created = p.start
	}
	clamp := func(t time.Time) time.Time {
		switch {
		case t.Before(created):
			return created
		case t.After(now):
			return now
		}
		return t
	}
	since := map[string]time.Time{}
	for _, e := range envs {
		if e.Since != nil && !e.Since.IsZero() {
			since[e.Name] = clamp(e.Since.Time)
		} else if t, ok := p.changed[e.Name]; ok && !t.IsZero() {
			since[e.Name] = clamp(t)
		}
	}
	live := func(name string) (time.Time, bool) {
		for _, e := range envs {
			if e.Name == name && e.State == "Verified" {
				t, ok := since[name]
				return t, ok
			}
		}
		return time.Time{}, false
	}
	began := func(i int) time.Time {
		e := envs[i]
		if t, ok := p.started[e.Name]; ok && !t.IsZero() {
			return clamp(t)
		}
		ups, known := p.upstreams[e.Name]
		if !known && i > 0 {
			ups = []string{envs[i-1].Name}
		}
		start := created
		for _, up := range ups {
			if t, ok := live(up); ok && t.After(start) {
				start = t
			}
		}
		return start
	}
	_, _ = fmt.Fprintf(p.w, "%s after %s:\n", sty.Dim("Summary"), shortDuration(now.Sub(p.start)))
	tw := style.NewTable(p.w, 3)
	for i, e := range envs {
		t, known := since[e.Name]
		when := ""
		switch {
		case !known:
		case e.State == "Verified" && e.Since != nil && !e.Since.IsZero() && e.Since.Time.Before(created):
			when = "already live"
		case e.State == "Verified":
			when = "took " + shortDuration(t.Sub(began(i)))
		default:
			when = "for " + shortDuration(now.Sub(t))
		}
		if when == "" {
			_, _ = fmt.Fprintf(tw, "  %s\t%s\n", e.Name, sty.State(envState(e)))
			continue
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", e.Name, sty.State(envState(e)), when)
	}
	_ = tw.Flush()
}

// timing records, for the summary, when Bundle b's promotion began in each
// environment (the earliest startedAt in its PromotionStep's status.steps)
// and each environment's direct upstreams in the Pipeline's DAG. It is best
// effort: what it cannot read, the summary estimates.
func (p *shipProgress) timing(ctx context.Context, c sigs_client.Client, b *v1alpha1.Bundle) {
	if p.w == nil || !p.timed || b == nil {
		return
	}
	var pl v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: b.Spec.Pipeline}, &pl); err == nil {
		p.upstreams = map[string][]string{}
		for _, e := range pl.Spec.Environments {
			if ups, err := graph.DirectUpstreams(&pl, b, e.Name); err == nil {
				p.upstreams[e.Name] = ups
			}
		}
	}
	var steps v1alpha1.PromotionStepList
	if err := c.List(ctx, &steps, sigs_client.InNamespace(b.Namespace), sigs_client.MatchingLabels{"kardinal.io/bundle": b.Name}); err != nil {
		return
	}
	p.started = map[string]time.Time{}
	for i := range steps.Items {
		s := &steps.Items[i]
		if s.Spec.BundleName != b.Name {
			continue
		}
		for _, st := range s.Status.Steps {
			if st.StartedAt == nil || st.StartedAt.IsZero() {
				continue
			}
			if t, ok := p.started[s.Spec.Environment]; !ok || st.StartedAt.Time.Before(t) {
				p.started[s.Spec.Environment] = st.StartedAt.Time
			}
		}
	}
}

// shortDuration is d to the second, as Go writes it (1m5s), 0s under one.
// A negative d (the controller's clock ahead of this one) is 0s.
func shortDuration(d time.Duration) string {
	return max(d, 0).Round(time.Second).String()
}

// followShip is waitShip, printing its progress with p.
func followShip(ctx context.Context, c sigs_client.Client, ns, pipeline, name string, res shipResult, timeout time.Duration,
	stopAtGate bool, p *shipProgress) shipResult {
	deadline := time.Now().Add(timeout)
	var pending *shipOutcomeResult
	failures := 0
	pause := shipPollInterval
	for {
		out, envs, err := shipOutcome(ctx, c, ns, pipeline, name, stopAtGate)
		if err != nil {
			failures++
			if failures >= 3 {
				res.Result, res.ExitCode = "Error", ExitFailed
				res.Message = fmt.Sprintf("cannot follow %s: %v", versionRef(res.Version, name), HumanError(err))
				return res
			}
			pause = min(pause*2, 16*shipPollInterval)
		} else {
			failures, pause = 0, shipPollInterval
			p.observe(envs)
			res.Environments = envs
			switch {
			case out == nil:
				pending = nil
			case out.final || (pending != nil && pending.result == out.result):
				// A stop (a gate, a hold, a PR) counts once two reads
				// agree, so a state the controller is about to leave does
				// not end it.
				res.Result, res.ExitCode, res.Message = out.result, out.code, out.message
				return res
			default:
				pending = out
			}
		}
		if !time.Now().Before(deadline) {
			res.Result, res.ExitCode = "Timeout", ExitTimeout
			res.Message = fmt.Sprintf("%s is still promoting after %s: kardinal status %s -w follows it", upperFirst(versionRef(res.Version, name)), durationText(timeout), pipeline)
			return res
		}
		select {
		case <-ctx.Done():
			res.Result, res.ExitCode = "Interrupted", ExitTimeout
			res.Message = fmt.Sprintf("Stopped following %s (interrupted); the promotion goes on: kardinal status %s -w", versionRef(res.Version, name), pipeline)
			return res
		case <-time.After(pause):
		}
	}
}

// shipOutcomeResult is how a shipped Bundle ended, or where it stopped.
type shipOutcomeResult struct {
	result, message string
	code            int
	// final is a Bundle phase that does not change again; a stop at a gate,
	// a hold or a PR is not final.
	final bool
}

// shipOutcome reads Bundle name: its outcome (nil while it promotes, or
// waits for something that comes by itself) and its environments of the
// Pipeline's status.environments (computed with the controller's code when
// the controller predates it). It stops only where a person is needed, with
// nothing else of the Bundle moving: an approval gate, a gate that does not
// pass and does not open by itself (rechecks), a hold, a paused Pipeline, or
// a PR that no one but a person will merge. A gate that opens by itself (a
// soak, schedule, upstream, metric or change window expression) and a PR the
// SCM merges by itself (pr.merge.auto) are waited for; with stopAtGate, any
// evaluated gate that does not pass stops it. A deleted Bundle is final.
func shipOutcome(ctx context.Context, c sigs_client.Client, ns, pipeline, name string, stopAtGate bool) (*shipOutcomeResult, []v1alpha1.PipelineEnvironmentStatus, error) {
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
		if apierrors.IsNotFound(err) {
			return &shipOutcomeResult{result: "Deleted", code: ExitReplaced, final: true,
				message: fmt.Sprintf("%s was deleted: kardinal status %s", upperFirst(versionRef("", name)), pipeline)}, nil, nil
		}
		return nil, nil, fmt.Errorf("get bundle %s: %w", name, err)
	}
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: pipeline}, &p); err != nil {
		return nil, nil, fmt.Errorf("get pipeline %s: %w", pipeline, err)
	}
	envs, err := shipEnvironments(ctx, c, &p, name)
	if err != nil {
		return nil, nil, err
	}
	where := func(pred func(v1alpha1.PipelineEnvironmentStatus) bool) []string {
		var out []string
		for _, e := range envs {
			if pred(e) {
				out = append(out, e.Name)
			}
		}
		return out
	}
	switch b.Status.Phase {
	case "Verified":
		msg := fmt.Sprintf("%s is %s", versionOr(scmVersion(&b), name), strings.ToLower(display.Step("Verified").Label))
		// status.environments may trail the Bundle's phase by a reconcile.
		if in := where(func(v1alpha1.PipelineEnvironmentStatus) bool { return true }); len(in) > 0 {
			msg += " in " + strings.Join(in, ", ")
		}
		return &shipOutcomeResult{result: "Verified", code: ExitVerified, final: true, message: msg}, envs, nil
	case "Failed":
		msg := fmt.Sprintf("%s failed", upperFirst(versionRef(scmVersion(&b), name)))
		if failed := where(func(e v1alpha1.PipelineEnvironmentStatus) bool {
			return display.Step(e.State).Tone == display.ToneFailed
		}); len(failed) > 0 {
			msg += " in " + strings.Join(failed, ", ")
		}
		return &shipOutcomeResult{result: "Failed", code: ExitFailed, final: true,
			message: msg + fmt.Sprintf(": kardinal why %s explains it", pipeline)}, envs, nil
	case "Superseded":
		return &shipOutcomeResult{result: "Superseded", code: ExitReplaced, final: true,
			message: fmt.Sprintf("%s was replaced by a newer version: kardinal status %s", upperFirst(versionRef(scmVersion(&b), name)), pipeline)}, envs, nil
	case "Rejected":
		return &shipOutcomeResult{result: "Rejected", code: ExitReplaced, final: true,
			message: fmt.Sprintf("%s was rejected: kardinal history %s", upperFirst(versionRef(scmVersion(&b), name)), pipeline)}, envs, nil
	}
	if p.Spec.Paused {
		return &shipOutcomeResult{result: "Held", code: ExitHeld,
			message: fmt.Sprintf("%s stopped: pipeline %s is paused (kardinal resume %s)", upperFirst(versionRef(scmVersion(&b), name)), pipeline, pipeline)}, envs, nil
	}

	var steps v1alpha1.PromotionStepList
	var gates v1alpha1.PolicyGateList
	if err := c.List(ctx, &steps, sigs_client.InNamespace(ns), sigs_client.MatchingLabels{"kardinal.io/bundle": name}); err != nil {
		return nil, nil, fmt.Errorf("list promotion steps: %w", err)
	}
	if err := c.List(ctx, &gates, sigs_client.InNamespace(ns), sigs_client.MatchingLabels{"kardinal.io/bundle": name}); err != nil {
		return nil, nil, fmt.Errorf("list policy gates: %w", err)
	}
	// A PR the SCM merges by itself (pr.merge.auto, its auto-merge not
	// failed) is on its way, like a step that runs.
	autoMerging := func(env string) bool {
		spec := environmentSpec(&p, env)
		if spec == nil || spec.PR == nil || spec.PR.Merge == nil || !spec.PR.Merge.Auto {
			return false
		}
		for i := range steps.Items {
			if s := &steps.Items[i]; s.Spec.Environment == env {
				return s.Status.Outputs[prAutoMergeOutput] != autoMergeFailed
			}
		}
		return true
	}
	moving := where(func(e v1alpha1.PipelineEnvironmentStatus) bool {
		switch e.State {
		case "Promoting", "HealthChecking", "Verifying":
			return true
		case "Pending":
			return !e.Held && len(e.BlockedBy) == 0
		case "WaitingForMerge":
			return autoMerging(e.Name)
		}
		return false
	})
	if len(moving) > 0 {
		return nil, envs, nil
	}
	if held := heldForAPerson(pipeline, name, envs, gates.Items, stopAtGate); len(held) > 0 {
		return &shipOutcomeResult{result: "Held", code: ExitHeld,
			message: fmt.Sprintf("%s stopped: %s", upperFirst(versionRef(scmVersion(&b), name)), strings.Join(held, "; "))}, envs, nil
	}
	if merge := where(func(e v1alpha1.PipelineEnvironmentStatus) bool { return e.State == "WaitingForMerge" }); len(merge) > 0 {
		var prs []string
		for _, e := range envs {
			if e.State == "WaitingForMerge" && e.PRURL != "" {
				prs = append(prs, e.PRURL)
			}
		}
		msg := fmt.Sprintf("%s waits for its PR to be merged in %s", upperFirst(versionRef(scmVersion(&b), name)), strings.Join(merge, ", "))
		if len(prs) > 0 {
			msg += ": " + strings.Join(prs, " ")
		}
		return &shipOutcomeResult{result: "WaitingForMerge", code: ExitWaitingForMerge, message: msg}, envs, nil
	}
	return nil, envs, nil
}

// The auto-merge state a PR step records (pkg/steps OutputPRAutoMerge and
// AutoMergeFailed; the CLI does not import the step engine).
const (
	prAutoMergeOutput = "prAutoMerge"
	autoMergeFailed   = "failed"
)

// environmentSpec is env of p, or nil.
func environmentSpec(p *v1alpha1.Pipeline, env string) *v1alpha1.EnvironmentSpec {
	for i := range p.Spec.Environments {
		if p.Spec.Environments[i].Name == env {
			return &p.Spec.Environments[i]
		}
	}
	return nil
}

// scmVersion is the version Bundle b ships.
func scmVersion(b *v1alpha1.Bundle) string { return createdOf(b).Version }

// shipEnvironments is status.environments of p restricted to the
// environments Bundle name reached or waits to enter.
func shipEnvironments(ctx context.Context, c sigs_client.Client, p *v1alpha1.Pipeline, name string) ([]v1alpha1.PipelineEnvironmentStatus, error) {
	envs := p.Status.Environments
	if len(envs) == 0 {
		// A controller that predates status.environments.
		var bundles v1alpha1.BundleList
		var steps v1alpha1.PromotionStepList
		var gates v1alpha1.PolicyGateList
		sel := sigs_client.MatchingLabels{"kardinal.io/pipeline": p.Name}
		if err := c.List(ctx, &bundles, sigs_client.InNamespace(p.Namespace)); err != nil {
			return nil, fmt.Errorf("list bundles: %w", err)
		}
		if err := c.List(ctx, &steps, sigs_client.InNamespace(p.Namespace), sel); err != nil {
			return nil, fmt.Errorf("list promotion steps: %w", err)
		}
		if err := c.List(ctx, &gates, sigs_client.InNamespace(p.Namespace), sel); err != nil {
			return nil, fmt.Errorf("list policy gates: %w", err)
		}
		envs = pipelinerec.ProjectEnvironments(p, bundles.Items, steps.Items, gates.Items, nil, time.Time{})
	}
	var mine []v1alpha1.PipelineEnvironmentStatus
	for _, e := range envs {
		if e.IncomingBundle == name || (e.IncomingBundle == "" && e.Bundle == name) {
			mine = append(mine, e)
		}
	}
	return mine, nil
}

// heldForAPerson is, per environment where Bundle name waits for a person,
// what holds it: a hold, an approval gate, or a gate that was evaluated, does
// not pass and does not open by itself (rechecks). A gate not evaluated yet,
// or one that opens by itself, comes by itself, unless stopAtGate.
func heldForAPerson(pipeline, name string, envs []v1alpha1.PipelineEnvironmentStatus, gates []v1alpha1.PolicyGate, stopAtGate bool) []string {
	var out []string
	for _, e := range envs {
		if e.Held {
			out = append(out, fmt.Sprintf("%s is held (kardinal unhold %s %s)", e.Name, pipeline, e.Name))
			continue
		}
		var approval, closed []string
		for i := range gates {
			g := &gates[i]
			if g.Labels["kardinal.io/environment"] != e.Name || g.Labels["kardinal.io/bundle"] != name ||
				!slices.Contains(e.BlockedBy, gateDisplayName(*g)) || g.Status.Ready || g.Status.LastEvaluatedAt == nil {
				continue
			}
			switch {
			case g.Spec.Approval != nil:
				approval = append(approval, gateDisplayName(*g))
			case stopAtGate || !rechecks(g):
				closed = append(closed, gateDisplayName(*g))
			}
		}
		if len(approval) > 0 {
			slices.Sort(approval)
			out = append(out, fmt.Sprintf("%s waits for approval at gate %s (kardinal approve %s %s)",
				e.Name, strings.Join(approval, ", "), pipeline, e.Name))
		}
		if len(closed) > 0 {
			slices.Sort(closed)
			why := ", which does not open by itself"
			if stopAtGate {
				why = " (--stop-at-gate)"
			}
			out = append(out, fmt.Sprintf("%s is held by gate %s%s (kardinal why %s %s)",
				e.Name, strings.Join(closed, ", "), why, pipeline, e.Name))
		}
	}
	return out
}

// timeVariables are the PolicyGate context variables that change with
// time: a gate whose expression reads one opens by itself, so ship --wait
// waits for it. match is how the reference names it, vars what it covers,
// and re finds it in an expression whose string literals are removed (so
// author != "metrics-bot" is not one), as a root variable (not a field of
// another, so bundle.labels.schedule is not one), with . or [ after it
// (schedule.hour, schedule["hour"], schedule . hour). The ship reference and
// docs/ci-integration.md list them from here.
var timeVariables = []struct {
	match, vars string
	re          *regexp.Regexp
}{
	{"schedule", "schedule.isWeekend, schedule.hour, schedule.dayOfWeek", rootVariable("schedule")},
	{"upstream", "upstream.<env>.soakMinutes and every other field of upstream.<env>", rootVariable("upstream")},
	{"bundle.upstreamSoakMinutes", "the soak of the environment before this one", regexp.MustCompile(`(?:^|[^\w.])bundle\s*\.\s*upstreamSoakMinutes\b`)},
	{"metrics", "metrics.<name>.value, metrics.<name>.result", rootVariable("metrics")},
	{"changewindow", "changewindow.<name>", rootVariable("changewindow")},
}

// rootVariable matches name as a root variable with a field or an index
// after it.
func rootVariable(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^\w.])` + name + `\s*[.\[]`)
}

// celStringLiteral is a CEL string or bytes literal: triple-quoted, quoted
// with escapes, with an r (raw) or b prefix.
var celStringLiteral = regexp.MustCompile(`(?s)[rRbB]{0,2}(?:""".*?"""|'''.*?'''|"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*')`)

// timeVariableList is timeVariables for the reference page, one per line.
func timeVariableList() string {
	var b strings.Builder
	for _, v := range timeVariables {
		fmt.Fprintf(&b, "  %-27s %s\n", v.match, v.vars)
	}
	return b.String()
}

// rechecks reports whether gate g opens by itself: its expression, without
// its string literals, reads a variable that changes with time
// (timeVariables). Its recheckInterval says nothing: the CRD defaults it to
// 5m, so every gate has one. A gate on anything else (a Bundle label, its
// provenance) waits for a person.
func rechecks(g *v1alpha1.PolicyGate) bool {
	expr := celStringLiteral.ReplaceAllString(g.Spec.Expression, `""`)
	for _, v := range timeVariables {
		if v.re.MatchString(expr) {
			return true
		}
	}
	return false
}

// shipExitCodes are ship --wait's exit codes, as its reference page lists
// them (testdata/ship-exit-codes.golden pins the text).
var shipExitCodes = []struct {
	code         int
	result, when string
}{
	{ExitVerified, "Verified", "live in every environment it promotes"},
	{ExitFailed, "Failed, Error", "it failed, or it could not be read three times in a row (or the command itself failed)"},
	{ExitHeld, "Held", "a person is needed and nothing else of it is moving: an approval gate, a hold, a paused pipeline, or a gate that does not pass and does not open by itself (with --stop-at-gate, any gate that does not pass)"},
	{ExitWaitingForMerge, "WaitingForMerge", "its PR waits for a person to merge it (not one the SCM merges by itself, pr.merge.auto), and nothing else of it is moving"},
	{ExitReplaced, "Superseded, Rejected, Deleted", "a newer version replaced it, it was rejected, or it was deleted"},
	{ExitTimeout, "Timeout, Interrupted", "--timeout (default 30m) passed, or Ctrl-C or SIGTERM interrupted the wait; the result is printed first"},
}

// shipExitCodeTable is shipExitCodes as the reference page's table.
func shipExitCodeTable() string {
	var b strings.Builder
	b.WriteString("  CODE  RESULT                         WHEN\n")
	for _, x := range shipExitCodes {
		fmt.Fprintf(&b, "  %-4d  %-29s  %s\n", x.code, x.result, x.when)
	}
	return b.String()
}

// shipReference is the detail of ship for its reference page.
var shipReference = `ship takes create bundle's flags and creates the same Bundle; create bundle
stays. Without --wait it prints the Bundle's name and, on stderr, how to
follow it.

--wait follows the Bundle, printing on stderr each new state of its
environments after the time since the start (+1m5s), the URL of each PR it
opens, and at the end a summary: each environment's last state and how long
its own promotion there took ("took": from its first step's start, else from
its upstream going live, else from the Bundle's creation), or how long it has
waited in it ("for"). --quiet (-q) prints none of that.
It ends with one line on stdout (or, with -o json, an object with
bundle, pipeline, namespace, type, version, url, result, exitCode, message
and the Bundle's environments from status.environments). Exit codes:

` + shipExitCodeTable() + `
A stop at a gate, a hold or a PR counts once two reads agree, and a gate not
evaluated yet never stops it. A gate that does not pass is waited for, until
--timeout, when it opens by itself: its expression, outside its string
literals, reads one of these variables that change with time (with a . or
[ after it: schedule.hour, schedule["hour"]):

` + timeVariableList() + `
So CI can wait through a soak or a schedule. A PR the SCM merges by itself
(pr.merge.auto) is waited for too. Any other gate that does not pass (on a
Bundle label, its provenance, its version) needs a person: exit 3, as do an
approval gate, a hold and a paused pipeline. --stop-at-gate stops at any
gate that does not pass. Stopping to follow does not stop the promotion:
kardinal status <pipeline> -w goes on following it. The codes
are stable: a new outcome gets a new code.`
