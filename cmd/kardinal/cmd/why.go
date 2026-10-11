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
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	pipelinerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"
)

func newWhyCmd() *cobra.Command {
	var (
		envFlag   string
		watchFlag bool
		colorFlag bool
	)
	cmd := &cobra.Command{
		Use:   "why <pipeline> [<env>]",
		Short: "Say why an environment waits, and what moves it on",
		Long: `Say, for <env> of <pipeline> (or each environment), in a sentence what it
waits for, then the version, the gate or PR, and the command that moves it
on. -o wide prints kardinal explain's table instead; -o json and yaml print
explain's entries. -w refreshes every 3 seconds.`,
		Example: `  kardinal why my-app prod
  kardinal why my-app
  kardinal why my-app prod -o wide`,
		Annotations: map[string]string{ReferenceAnnotation: whyReference, outputAnnotation: "true", wideAnnotation: "true"},
		Args:        cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pipeline, env, err := envArgs(cmd, args, envFlag)
			if err != nil {
				return err
			}
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("why: %w", err)
			}
			w := cmd.OutOrStdout()
			if structured() || OutputFormat() == "wide" {
				if watchFlag {
					return explainWatch(cmd.Context(), w, c, ns, pipeline, env, colorFlag, 3*time.Second)
				}
				return explainHinted(w, c, ns, pipeline, env, colorFlag)
			}
			if !watchFlag {
				return whyFn(cmd.Context(), w, c, ns, pipeline, env)
			}
			return whyWatch(cmd.Context(), w, c, ns, pipeline, env, 3*time.Second)
		},
	}
	addEnvFlag(cmd, &envFlag, "Only this environment (or give it after the pipeline)")
	cmd.Flags().BoolVarP(&watchFlag, "watch", "w", false, "Refresh every 3 seconds")
	cmd.Flags().BoolVar(&colorFlag, "color", false, "With -o wide: force ANSI color output (auto-detected when TTY)")
	return cmd
}

// whyWatch re-renders why every interval until ctx is done, SIGINT or
// SIGTERM, as explain -w does.
func whyWatch(ctx context.Context, w io.Writer, c sigs_client.Client, ns, pipeline, env string, interval time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	clearScreen := style.IsTerminal(w)
	for {
		if clearScreen {
			_, _ = fmt.Fprint(w, "\033[H\033[2J")
		}
		if err := whyFn(ctx, w, c, ns, pipeline, env); err != nil {
			_, _ = fmt.Fprintf(w, "error: %v\n", HumanError(err))
		}
		_, _ = fmt.Fprintln(w, watchFooter)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// whyNow is the time why measures "since" from; a test fixes it.
var whyNow = time.Now

// shortVersion is a version by its tag alone, or its Bundle when it has no
// tag.
func shortVersion(version, bundle string) string {
	if version != "" {
		return version
	}
	return bundle
}

// whyLine is one detail line of a why answer: a label and its value.
type whyLine struct{ label, value string }

// whyAnswer is why's answer for one environment: a sentence, then details.
type whyAnswer struct {
	sentence string
	lines    []whyLine
}

// whyFn writes, for env of pipeline (or each environment, in promotion
// order), what it waits for and what moves it on.
func whyFn(ctx context.Context, w io.Writer, c sigs_client.Client, ns, pipeline, envFilter string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p, err := getPipeline(ctx, c, ns, pipeline)
	if err != nil {
		return err
	}
	names := pipelineEnvNames(p)
	if envFilter != "" && !slices.Contains(names, envFilter) {
		return fmt.Errorf("environment %q not found in pipeline %q (environments: %s)", envFilter, pipeline, strings.Join(names, ", "))
	}
	sel := sigs_client.MatchingLabels{"kardinal.io/pipeline": p.Name}
	var bundles v1alpha1.BundleList
	var steps v1alpha1.PromotionStepList
	var gates v1alpha1.PolicyGateList
	if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}
	if err := c.List(ctx, &steps, sigs_client.InNamespace(ns), sel); err != nil {
		return fmt.Errorf("list promotion steps: %w", err)
	}
	if err := c.List(ctx, &gates, sigs_client.InNamespace(ns), sel); err != nil {
		return fmt.Errorf("list policy gates: %w", err)
	}
	envs := p.Status.Environments
	if len(envs) == 0 {
		// A controller that predates status.environments.
		envs = pipelinerec.ProjectEnvironments(p, bundles.Items, steps.Items, gates.Items, nil, time.Time{})
	}
	byName := map[string]*v1alpha1.Bundle{}
	for i := range bundles.Items {
		byName[bundles.Items[i].Name] = &bundles.Items[i]
	}
	// Who asks, to tell "your approval" from someone else's; unknown (nil)
	// when the API server does not say.
	var me *Identity
	if id, err := identityOf(ctx, c); err == nil {
		me = &id
	}
	var answers []whyAnswer
	for _, e := range envs {
		if envFilter != "" && e.Name != envFilter {
			continue
		}
		answers = append(answers, whyEnvironment(p, e, envs, gates.Items, steps.Items, byName, me, whyNow()))
	}
	for i, a := range answers {
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}
		if err := writeWhy(w, a); err != nil {
			return err
		}
	}
	return nil
}

// writeWhy writes one answer: the sentence, then each detail with its
// label dimmed.
func writeWhy(w io.Writer, a whyAnswer) error {
	if _, err := fmt.Fprintln(w, a.sentence); err != nil {
		return fmt.Errorf("write why: %w", err)
	}
	width := 0
	for _, l := range a.lines {
		width = max(width, len(l.label))
	}
	for _, l := range a.lines {
		pad := strings.Repeat(" ", width-len(l.label))
		if _, err := fmt.Fprintf(w, "  %s%s  %s\n", sty.Dim(l.label), pad, l.value); err != nil {
			return fmt.Errorf("write why: %w", err)
		}
	}
	return nil
}

// versionLabel is a version as a person reads it: the tag, then the Bundle
// when it says something more ("1.5.0 (my-app-x7k2p)").
func versionLabel(version, bundle string) string {
	switch {
	case bundle == "":
		return version
	case version == "" || version == bundle:
		return bundle
	}
	return version + " (" + bundle + ")"
}

// whyEnvironment is why's answer for environment e of p: paused, held, at a
// gate (an approval, or another gate), waiting for a PR, failed, promoting,
// waiting for an upstream environment, or nothing to wait for.
func whyEnvironment(p *v1alpha1.Pipeline, e v1alpha1.PipelineEnvironmentStatus, envs []v1alpha1.PipelineEnvironmentStatus,
	gates []v1alpha1.PolicyGate, steps []v1alpha1.PromotionStep, byName map[string]*v1alpha1.Bundle, me *Identity, now time.Time) whyAnswer {
	incoming := e.IncomingBundle
	incomingVersion := e.IncomingVersion
	if incoming == "" && e.State != "" && e.State != "Verified" {
		incoming, incomingVersion = e.Bundle, e.Version
	}
	version := whyLine{"Version", versionLabel(incomingVersion, incoming)}
	running := shortVersion(e.Version, e.Bundle)

	if p.Spec.Paused {
		a := whyAnswer{sentence: fmt.Sprintf("%s is paused with pipeline %s: nothing new promotes until it is resumed.", e.Name, p.Name)}
		if incoming != "" {
			a.lines = append(a.lines, version)
		}
		a.lines = append(a.lines, whyLine{"Next", "kardinal resume " + p.Name})
		return a
	}
	if h := lifecycle.HoldOf(p, e.Name); h != nil && !h.Expired(now) {
		held := shortVersion(versionOf(byName, h.Bundle), h.Bundle)
		sentence := fmt.Sprintf("%s is held on %s", e.Name, held)
		if h.CreatedBy != "" {
			sentence += " by " + h.CreatedBy
		}
		if h.Reason != "" {
			sentence += " (" + h.Reason + ")"
		}
		a := whyAnswer{sentence: sentence + "."}
		if incoming != "" && incoming != h.Bundle {
			a.lines = append(a.lines, whyLine{"Waiting", versionLabel(incomingVersion, incoming)})
		}
		if st := p.HoldState(h); st != nil && st.State == v1alpha1.HoldStateBundleMissing {
			a.lines = append(a.lines, whyLine{"Note", "the version it is held on does not exist; the hold stays until it is released"})
		}
		if h.ExpiresAt != nil {
			a.lines = append(a.lines, whyLine{"Until", h.ExpiresAt.UTC().Format(time.RFC3339)})
		}
		a.lines = append(a.lines, whyLine{"Next", fmt.Sprintf("kardinal unhold %s %s", p.Name, e.Name)})
		return a
	}
	if len(e.BlockedBy) > 0 {
		var mine []v1alpha1.PolicyGate
		for _, g := range gates {
			if g.Labels["kardinal.io/environment"] == e.Name && g.Labels["kardinal.io/bundle"] == incoming &&
				slices.Contains(e.BlockedBy, gateDisplayName(g)) && !g.Status.Ready {
				mine = append(mine, g)
			}
		}
		// An approval first: it is what a person does.
		slices.SortStableFunc(mine, func(a, b v1alpha1.PolicyGate) int {
			if (a.Spec.Approval != nil) != (b.Spec.Approval != nil) {
				if a.Spec.Approval != nil {
					return -1
				}
				return 1
			}
			return strings.Compare(gateDisplayName(a), gateDisplayName(b))
		})
		if len(mine) > 0 {
			creator, known := "", false
			if b := byName[incoming]; b != nil {
				creator, known = b.Annotations[lifecycle.AnnotationCreatedBy], true
			}
			return whyGate(p, e.Name, version, mine, me, creator, known)
		}
		return whyAnswer{sentence: fmt.Sprintf("%s is held by gate %s.", e.Name, strings.Join(e.BlockedBy, ", ")),
			lines: []whyLine{version, {"Next", fmt.Sprintf("kardinal explain %s %s", p.Name, e.Name)}}}
	}
	state := display.Step(e.State)
	switch {
	case e.State == "WaitingForMerge":
		pr := "its PR"
		if n := path.Base(e.PRURL); e.PRURL != "" && n != "" && strings.Trim(n, "0123456789") == "" {
			pr = "PR #" + n
		}
		a := whyAnswer{sentence: fmt.Sprintf("%s waits for %s to merge.", e.Name, pr), lines: []whyLine{version}}
		if e.PRURL != "" {
			a.lines = append(a.lines, whyLine{"PR", e.PRURL})
		}
		if spec := environmentSpec(p, e.Name); spec != nil && spec.PR != nil && spec.PR.Merge != nil && spec.PR.Merge.Auto {
			a.lines = append(a.lines, whyLine{"Merge", "the SCM merges it once its checks pass (pr.merge.auto)"})
		}
		return a
	case state.Tone == display.ToneFailed:
		a := whyAnswer{sentence: fmt.Sprintf("%s did not promote %s: %s.", e.Name, versionLabel(incomingVersion, incoming), strings.ToLower(state.Label))}
		if msg := stepMessage(steps, e.Name, incoming); msg != "" {
			a.lines = append(a.lines, whyLine{"Why", msg})
		}
		a.lines = append(a.lines, whyLine{"Next", fmt.Sprintf("kardinal logs %s %s", p.Name, e.Name)})
		if e.Bundle != "" {
			a.lines = append(a.lines, whyLine{"Or", fmt.Sprintf("kardinal rollback %s %s, back to %s", p.Name, e.Name, running)})
		}
		return a
	case state.Tone == display.TonePromoting:
		a := whyAnswer{sentence: fmt.Sprintf("%s is promoting %s: %s.", e.Name, versionLabel(incomingVersion, incoming), strings.ToLower(state.Label))}
		if e.Since != nil {
			a.lines = append(a.lines, whyLine{"Since", durationText(now.Sub(e.Since.Time).Round(time.Second)) + " ago"})
		}
		return a
	case incoming != "":
		if up := waitingUpstreams(p, e.Name, envs, incoming); len(up) > 0 {
			return whyAnswer{sentence: fmt.Sprintf("%s waits for %s to be live first.", e.Name, strings.Join(up, " and ")),
				lines: []whyLine{version}}
		}
		return whyAnswer{sentence: fmt.Sprintf("%s is about to promote %s.", e.Name, versionLabel(incomingVersion, incoming))}
	case e.Bundle == "":
		return whyAnswer{sentence: fmt.Sprintf("%s has no version yet.", e.Name),
			lines: []whyLine{{"Next", fmt.Sprintf("kardinal ship %s --image <ref>", p.Name)}}}
	}
	return whyAnswer{sentence: fmt.Sprintf("%s runs %s and nothing is waiting.", e.Name, running)}
}

// whyGate is why's answer for gates that hold an environment: the first
// says what it waits for, the others follow as more lines. creator is the
// version's kardinal.io/created-by; known is false when the version was not
// read, and then nothing is said about its creator.
func whyGate(p *v1alpha1.Pipeline, env string, version whyLine, gates []v1alpha1.PolicyGate, me *Identity, creator string, known bool) whyAnswer {
	g := gates[0]
	name := gateDisplayName(g)
	var a whyAnswer
	switch {
	case g.Spec.Approval != nil && known && unapprovable(g.Spec.Approval, creator) != "":
		// The gate holds whatever approvals it gets (as tallyApprovals
		// in pkg/reconciler/policygate/approvals.go decides).
		a.sentence = fmt.Sprintf("%s is held by gate %s: nobody can approve this version.", env, name)
		a.lines = []whyLine{version,
			{"Why", unapprovable(g.Spec.Approval, creator)},
			{"Next", fmt.Sprintf("ship it again as yourself: kardinal ship %s --image <ref>", p.Name)},
			{"Or", fmt.Sprintf("kardinal override %s %s --gate %s --reason <why>", p.Name, env, name)}}
	case g.Spec.Approval != nil:
		approved, rejectedBy := approvalCount(g)
		required := max(g.Spec.Approval.Required, 1)
		by, mine := approvalBy(g.Spec.Approval, me, creator)
		a.sentence = fmt.Sprintf("%s is waiting for %s.", env, by)
		if rejectedBy != "" {
			a.sentence = fmt.Sprintf("%s was rejected by %s.", env, rejectedBy)
		}
		next := fmt.Sprintf("kardinal approve %s %s", p.Name, env)
		switch {
		case mine || me == nil:
		case g.Spec.Approval.ExcludeAuthor && creator == me.Username:
			next = "someone else runs: " + next
		default:
			next = "one of them runs: " + next
		}
		a.lines = []whyLine{version,
			{"Gate", fmt.Sprintf("%s · %d of %d approvals", name, approved, required)},
			{"Next", next}}
	default:
		a.sentence = fmt.Sprintf("%s is held by gate %s.", env, name)
		a.lines = []whyLine{version}
		if g.Spec.Message != "" {
			a.lines = append(a.lines, whyLine{"Why", g.Spec.Message})
		}
		a.lines = append(a.lines, whyLine{"Rule", g.Spec.Expression})
		if now := gateEvaluation(g.Status.Reason); now != "" {
			a.lines = append(a.lines, whyLine{"Now", now})
		}
		every := g.Spec.RecheckInterval
		if every == "" {
			every = "5m"
		}
		opens := "when the expression passes; rechecked every " + every
		if !rechecks(&g) {
			opens = "when what it reads changes (nothing in it changes with time); rechecked every " + every
		}
		a.lines = append(a.lines, whyLine{"Opens", opens},
			whyLine{"Next", fmt.Sprintf("kardinal override %s %s --gate %s --reason <why>", p.Name, env, name)})
	}
	for _, other := range gates[1:] {
		a.lines = append(a.lines, whyLine{"Also", "gate " + gateDisplayName(other)})
	}
	return a
}

// unapprovable says why no approval can open approval gate a, or "": with
// excludeAuthor, a version with no verified creator (kardinal.io/created-by)
// or one a kardinal component created (the UI, the Bundle API, a
// Subscription) has no author to exclude, and the gate holds. It mirrors
// tallyApprovals in pkg/reconciler/policygate/approvals.go.
func unapprovable(a *v1alpha1.GateApprovalPolicy, creator string) string {
	switch {
	case !a.ExcludeAuthor:
		return ""
	case creator == "":
		return "the gate excludes the version's author, and the version has no verified creator (kardinal.io/created-by)"
	case lifecycle.ComponentCreator(creator):
		return fmt.Sprintf("the gate excludes the version's author, and the version was created by the kardinal component %s, not a person", creator)
	}
	return ""
}

// approvalBy is who an approval gate waits for, and whether the caller may
// approve: "your approval" when the caller is in allowedUsers or
// allowedGroups (or both are empty: then RBAC decides) and, with
// excludeAuthor, did not create the version; else "approval by alice, bob or
// group release-managers" (with "someone other than you" for the author). An
// unknown caller gets the list, or "approval".
func approvalBy(a *v1alpha1.GateApprovalPolicy, me *Identity, creator string) (string, bool) {
	var who []string
	who = append(who, a.AllowedUsers...)
	for _, g := range a.AllowedGroups {
		who = append(who, "group "+g)
	}
	list := func() string {
		switch len(who) {
		case 0:
			return ""
		case 1:
			return who[0]
		}
		return strings.Join(who[:len(who)-1], ", ") + " or " + who[len(who)-1]
	}
	if me != nil {
		if a.ExcludeAuthor && creator != "" && creator == me.Username {
			if l := list(); l != "" {
				return "approval by " + l + ", someone other than you (you created this version)", false
			}
			return "approval by someone other than you (you created this version)", false
		}
		allowed := len(a.AllowedUsers) == 0 && len(a.AllowedGroups) == 0
		allowed = allowed || slices.Contains(a.AllowedUsers, me.Username)
		for _, g := range me.Groups {
			allowed = allowed || slices.Contains(a.AllowedGroups, g)
		}
		if allowed {
			return "your approval", true
		}
	}
	if l := list(); l != "" {
		return "approval by " + l, false
	}
	return "approval", false
}

// approvalCount is the counted approvals of approval gate g, and who
// rejected it (a counted reject blocks it).
func approvalCount(g v1alpha1.PolicyGate) (approved int, rejectedBy string) {
	for _, s := range g.Status.Approvals {
		if !s.Counted {
			continue
		}
		switch s.Decision {
		case "approve":
			approved++
		case "reject":
			if rejectedBy == "" {
				rejectedBy = s.User
			}
		}
	}
	return approved, rejectedBy
}

// gateEvaluation is the evaluation in a gate's status reason, which ends
// "(<context>: <expression> = <result>)": the part after the context.
func gateEvaluation(reason string) string {
	open := strings.LastIndex(reason, "(")
	if open < 0 || !strings.HasSuffix(reason, ")") {
		return ""
	}
	inner := reason[open+1 : len(reason)-1]
	if _, after, ok := strings.Cut(inner, ": "); ok {
		inner = after
	}
	return inner
}

// waitingUpstreams are the environments env follows that do not run bundle
// yet.
func waitingUpstreams(p *v1alpha1.Pipeline, env string, envs []v1alpha1.PipelineEnvironmentStatus, bundle string) []string {
	up, err := graph.EnvironmentUpstreams(p, env)
	if err != nil {
		return nil
	}
	var out []string
	for _, u := range up {
		for _, e := range envs {
			if e.Name == u && (e.Bundle != bundle || e.IncomingBundle != "") {
				out = append(out, u)
			}
		}
	}
	return out
}

// stepMessage is the message of bundle's step in env.
func stepMessage(steps []v1alpha1.PromotionStep, env, bundle string) string {
	for _, s := range steps {
		if s.Spec.Environment == env && s.Spec.BundleName == bundle {
			return s.Status.Message
		}
	}
	return ""
}

// versionOf is the version of Bundle name, or "" when it is not known.
func versionOf(byName map[string]*v1alpha1.Bundle, name string) string {
	if b := byName[name]; b != nil {
		return createdOf(b).Version
	}
	return ""
}

// whyReference is the detail of why for its reference page.
const whyReference = `why answers, per environment, in one sentence what it waits for, then the
details and the command that moves it on:

  prod is waiting for your approval.
    Version  1.5.0 (my-app-x7k2p)
    Gate     cab · 0 of 1 approvals
    Next     kardinal approve my-app prod

The cases, in the order it checks them: the pipeline is paused (Next:
kardinal resume); the environment is held (on what, by whom, why; Next:
kardinal unhold); a gate holds it (an approval: how many of how many, and
"your approval" only when you may give it: you are in allowedUsers or
allowedGroups, or both are empty, and, with excludeAuthor, you did not create
the version; else who may, "approval by alice or group release-managers"; any
other gate: its message, Rule its expression, Now its last evaluation,
Opens when it opens and how often it is rechecked; Next: kardinal approve or
kardinal override); its PR waits to be merged (PR and its URL); the version
failed (Why the step's message; Next: kardinal logs, or roll back); it is
promoting; it waits for an upstream environment; it has no version yet; or
nothing is waiting ("prod runs 1.5.0 and nothing is waiting.").

-o wide prints kardinal explain's table (every step and gate of the current
version, with kinds and expressions), with the commands that get past a
gate on stderr; -o json and yaml print explain's entries.`
