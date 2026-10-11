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
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
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
			return shipFn(cmd.Context(), cmd.OutOrStdout(), c, ns, args[0], opts, wait, timeout, stopAtGate)
		},
	}
	addBundleFlags(cmd, &opts, &dryRun)
	cmd.Flags().BoolVar(&wait, "wait", false, "Follow the Bundle until it is live, fails, stops at a gate, a hold or a PR, or is replaced")
	cmd.Flags().Var(durationValue{&timeout}, "timeout", "With --wait: give up after this long (exit 6)")
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
	wait bool, timeout time.Duration, stopAtGate bool) error {
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
	_, _ = fmt.Fprintf(errOut, "Shipped %s as %s; following it (Ctrl-C stops following, the promotion goes on)\n",
		versionOr(res.Version, b.Name), b.Name)
	res = waitShip(ctx, c, ns, pipeline, b.Name, res, timeout, stopAtGate)
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
	deadline := time.Now().Add(timeout)
	seen := map[string]string{}
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
			for _, e := range envs {
				if text := sty.State(envState(e)); seen[e.Name] != text {
					seen[e.Name] = text
					_, _ = fmt.Fprintf(errOut, "  %s: %s\n", e.Name, text)
				}
			}
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
// time, by the text that names them in an expression: a gate whose
// expression contains one opens by itself, so ship --wait waits for it.
// The ship reference and docs/ci-integration.md list them from here.
var timeVariables = []struct{ match, vars string }{
	{"schedule.", "schedule.isWeekend, schedule.hour, schedule.dayOfWeek"},
	{"soakMinutes", "upstream.<env>.soakMinutes, bundle.upstreamSoakMinutes"},
	{"upstream.", "anything else of upstream.<env>"},
	{"metrics", "metrics.<name>.value, metrics.<name>.result"},
	{"changewindow", "changewindow.<name>"},
}

// timeExpressions are timeVariables' match texts.
var timeExpressions = func() []string {
	out := make([]string, 0, len(timeVariables))
	for _, v := range timeVariables {
		out = append(out, v.match)
	}
	return out
}()

// timeVariableList is timeVariables for the reference page, one per line.
func timeVariableList() string {
	var b strings.Builder
	for _, v := range timeVariables {
		fmt.Fprintf(&b, "  %-14s %s\n", v.match, v.vars)
	}
	return b.String()
}

// rechecks reports whether gate g opens by itself: its expression reads what
// changes with time (a schedule, a soak, an upstream environment, a metric,
// a change window). Its recheckInterval says nothing: the CRD defaults it to
// 5m, so every gate has one. A gate on anything else (a Bundle label, its
// provenance) waits for a person.
func rechecks(g *v1alpha1.PolicyGate) bool {
	for _, s := range timeExpressions {
		if strings.Contains(g.Spec.Expression, s) {
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

--wait follows the Bundle, printing each new state of its environments on
stderr, and ends with one line on stdout (or, with -o json, an object with
bundle, pipeline, namespace, type, version, url, result, exitCode, message
and the Bundle's environments from status.environments). Exit codes:

` + shipExitCodeTable() + `
A stop at a gate, a hold or a PR counts once two reads agree, and a gate not
evaluated yet never stops it. A gate that does not pass is waited for, until
--timeout, when it opens by itself: its expression contains one of these
texts, which name the variables that change with time:

` + timeVariableList() + `
So CI can wait through a soak or a schedule. A PR the SCM merges by itself
(pr.merge.auto) is waited for too. Any other gate that does not pass (on a
Bundle label, its provenance, its version) needs a person: exit 3, as do an
approval gate, a hold and a paused pipeline. --stop-at-gate stops at any
gate that does not pass. Stopping to follow does not stop the promotion:
kardinal status <pipeline> -w goes on following it. The codes
are stable: a new outcome gets a new code.`
