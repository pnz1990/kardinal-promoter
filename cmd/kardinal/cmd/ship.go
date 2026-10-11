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
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
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
		opts    createBundleOptions
		dryRun  bool
		wait    bool
		timeout = 30 * time.Minute
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
			return shipFn(cmd.Context(), cmd.OutOrStdout(), c, ns, args[0], opts, wait, timeout)
		},
	}
	addBundleFlags(cmd, &opts, &dryRun)
	cmd.Flags().BoolVar(&wait, "wait", false, "Follow the Bundle until it is live, fails, stops at a gate, a hold or a PR, or is replaced")
	cmd.Flags().Var(durationValue{&timeout}, "timeout", "With --wait: give up after this long (exit 6)")
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
	wait bool, timeout time.Duration) error {
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
	if !structured() {
		_, _ = fmt.Fprintf(errOut, "Shipped %s as %s; following it (Ctrl-C stops following, the promotion goes on)\n",
			versionOr(res.Version, b.Name), b.Name)
	}
	res = waitShip(ctx, c, ns, pipeline, b.Name, res, timeout)
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

// waitShip follows Bundle name until it ends (shipOutcome) or timeout
// passes, printing each new state of its environments on stderr.
func waitShip(ctx context.Context, c sigs_client.Client, ns, pipeline, name string, res shipResult, timeout time.Duration) shipResult {
	deadline := time.Now().Add(timeout)
	seen := map[string]string{}
	var pending *shipOutcomeResult
	for {
		out, envs := shipOutcome(ctx, c, ns, pipeline, name)
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
			// A stop (a gate, a hold, a PR) counts once two reads agree,
			// so a state the controller is about to leave does not end it.
			res.Result, res.ExitCode, res.Message = out.result, out.code, out.message
			return res
		default:
			pending = out
		}
		if !time.Now().Before(deadline) {
			res.Result, res.ExitCode = "Timeout", ExitTimeout
			res.Message = fmt.Sprintf("Bundle %s is still promoting after %s: kardinal status %s -w follows it", name, durationText(timeout), pipeline)
			return res
		}
		select {
		case <-ctx.Done():
			res.Result, res.ExitCode = "Timeout", ExitTimeout
			res.Message = fmt.Sprintf("stopped following Bundle %s; it goes on: kardinal status %s -w", name, pipeline)
			return res
		case <-time.After(shipPollInterval):
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

// shipOutcome reads Bundle name: its outcome (nil while it promotes) and its
// environments of the Pipeline's status.environments (computed with the
// controller's code when the controller predates it). A read error is
// treated as "still promoting": the next read tries again.
func shipOutcome(ctx context.Context, c sigs_client.Client, ns, pipeline, name string) (*shipOutcomeResult, []v1alpha1.PipelineEnvironmentStatus) {
	var b v1alpha1.Bundle
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
		return nil, nil
	}
	envs := shipEnvironments(ctx, c, ns, pipeline, name)
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
		return &shipOutcomeResult{result: "Verified", code: ExitVerified, final: true,
			message: fmt.Sprintf("%s is %s in %s", versionOr(scmVersion(&b), name),
				strings.ToLower(display.Step("Verified").Label), strings.Join(where(func(e v1alpha1.PipelineEnvironmentStatus) bool { return true }), ", "))}, envs
	case "Failed":
		msg := fmt.Sprintf("Bundle %s failed", name)
		if failed := where(func(e v1alpha1.PipelineEnvironmentStatus) bool {
			return display.Step(e.State).Tone == display.ToneFailed
		}); len(failed) > 0 {
			msg += " in " + strings.Join(failed, ", ")
		}
		return &shipOutcomeResult{result: "Failed", code: ExitFailed, final: true,
			message: msg + fmt.Sprintf(": kardinal why %s explains it", pipeline)}, envs
	case "Superseded":
		return &shipOutcomeResult{result: "Superseded", code: ExitReplaced, final: true,
			message: fmt.Sprintf("Bundle %s was replaced by a newer version: kardinal status %s", name, pipeline)}, envs
	case "Rejected":
		return &shipOutcomeResult{result: "Rejected", code: ExitReplaced, final: true,
			message: fmt.Sprintf("Bundle %s was rejected: kardinal history %s", name, pipeline)}, envs
	}
	// Still promoting: it stops only when nothing of it moves and something
	// holds it.
	moving := where(func(e v1alpha1.PipelineEnvironmentStatus) bool {
		switch e.State {
		case "Promoting", "HealthChecking", "Verifying":
			return true
		case "Pending":
			return !e.Held && len(e.BlockedBy) == 0
		}
		return false
	})
	if len(moving) > 0 {
		return nil, envs
	}
	if held := heldByEvaluatedGates(ctx, c, ns, name, envs); len(held) > 0 {
		return &shipOutcomeResult{result: "Held", code: ExitHeld,
			message: fmt.Sprintf("Bundle %s stopped: %s. Why: kardinal why %s %s", name, strings.Join(held, "; "),
				pipeline, firstWord(held[0]))}, envs
	}
	if merge := where(func(e v1alpha1.PipelineEnvironmentStatus) bool { return e.State == "WaitingForMerge" }); len(merge) > 0 {
		var prs []string
		for _, e := range envs {
			if e.State == "WaitingForMerge" && e.PRURL != "" {
				prs = append(prs, e.PRURL)
			}
		}
		msg := fmt.Sprintf("Bundle %s waits for its PR to be merged in %s", name, strings.Join(merge, ", "))
		if len(prs) > 0 {
			msg += ": " + strings.Join(prs, " ")
		}
		return &shipOutcomeResult{result: "WaitingForMerge", code: ExitWaitingForMerge, message: msg}, envs
	}
	return nil, envs
}

// firstWord is s up to its first space: the environment a held line names.
func firstWord(s string) string { w, _, _ := strings.Cut(s, " "); return w }

// scmVersion is the version Bundle b ships.
func scmVersion(b *v1alpha1.Bundle) string { return createdOf(b).Version }

// shipEnvironments is status.environments of pipeline restricted to the
// environments Bundle name reached or waits to enter.
func shipEnvironments(ctx context.Context, c sigs_client.Client, ns, pipeline, name string) []v1alpha1.PipelineEnvironmentStatus {
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: pipeline}, &p); err != nil {
		return nil
	}
	envs := p.Status.Environments
	if len(envs) == 0 {
		// A controller that predates status.environments.
		var bundles v1alpha1.BundleList
		var steps v1alpha1.PromotionStepList
		var gates v1alpha1.PolicyGateList
		sel := sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline}
		if c.List(ctx, &bundles, sigs_client.InNamespace(ns)) != nil ||
			c.List(ctx, &steps, sigs_client.InNamespace(ns), sel) != nil ||
			c.List(ctx, &gates, sigs_client.InNamespace(ns), sel) != nil {
			return nil
		}
		envs = pipelinerec.ProjectEnvironments(&p, bundles.Items, steps.Items, gates.Items, nil, time.Time{})
	}
	var mine []v1alpha1.PipelineEnvironmentStatus
	for _, e := range envs {
		if e.IncomingBundle == name || (e.IncomingBundle == "" && e.Bundle == name) {
			mine = append(mine, e)
		}
	}
	return mine
}

// heldByEvaluatedGates is, per environment where Bundle name is held, why:
// a rollback hold, or its gate instances there that were evaluated and do
// not pass. A gate not evaluated yet does not count: it may pass on its
// first evaluation.
func heldByEvaluatedGates(ctx context.Context, c sigs_client.Client, ns, name string, envs []v1alpha1.PipelineEnvironmentStatus) []string {
	var gates v1alpha1.PolicyGateList
	_ = c.List(ctx, &gates, sigs_client.InNamespace(ns), sigs_client.MatchingLabels{"kardinal.io/bundle": name})
	var out []string
	for _, e := range envs {
		if e.Held {
			out = append(out, e.Name+" is held by a rollback (kardinal release-hold)")
			continue
		}
		var held []string
		for i := range gates.Items {
			g := &gates.Items[i]
			if g.Labels["kardinal.io/environment"] == e.Name && slices.Contains(e.BlockedBy, gateDisplayName(*g)) &&
				!g.Status.Ready && g.Status.LastEvaluatedAt != nil {
				held = append(held, gateDisplayName(*g))
			}
		}
		if len(held) > 0 {
			slices.Sort(held)
			out = append(out, e.Name+" is held by gate "+strings.Join(held, ", "))
		}
	}
	return out
}

// shipReference is the detail of ship for its reference page.
const shipReference = `ship takes create bundle's flags and creates the same Bundle; create bundle
stays. Without --wait it prints the Bundle's name and, on stderr, how to
follow it.

--wait follows the Bundle, printing each new state of its environments on
stderr, and ends with one line on stdout (or, with -o json, an object with
bundle, pipeline, namespace, type, version, result, exitCode, message and the
Bundle's environments from status.environments). Exit codes:

  0  Verified: live in every environment it promotes
  1  Failed (or the command itself failed)
  3  Held: a gate that was evaluated and does not pass, or a rollback hold,
     keeps it out of an environment, and nothing else of it is moving
  4  WaitingForMerge: its PR waits for a person to merge it, and nothing
     else of it is moving
  5  Superseded or Rejected: a newer version replaced it, or it was rejected
  6  Timeout: --timeout (default 30m) passed, or following was interrupted

A stop at a gate, a hold or a PR counts once two reads agree, and a gate not
evaluated yet never stops it. Stopping to follow does not stop the
promotion: kardinal status <pipeline> -w goes on following it. The codes
are stable: a new outcome gets a new code.`
