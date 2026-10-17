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
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
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
				if err := whyFn(cmd.Context(), w, c, ns, pipeline, env); err != nil {
					return err
				}
				printUILink(ns, pipeline, env)
				return nil
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
// quoted names the lines (by label) whose value relays text kardinal did not
// write, a step's message from the SCM or the GitOps tool or a PR's URL, and
// quotedSentence a sentence that quotes such text: kardinal mcp labels them
// as data, not instructions.
type whyAnswer struct {
	sentence       string
	lines          []whyLine
	quoted         []string
	quotedSentence bool
}

// whyFn writes, for env of pipeline (or each environment, in promotion
// order), what it waits for and what moves it on.
func whyFn(ctx context.Context, w io.Writer, c sigs_client.Client, ns, pipeline, envFilter string) error {
	answers, err := whyAnswers(ctx, c, ns, pipeline, envFilter)
	if err != nil {
		return err
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

// whyAnswers is why's answers for env of pipeline (or each environment, in
// promotion order): the newest version's failure first, when it failed.
func whyAnswers(ctx context.Context, c sigs_client.Client, ns, pipeline, envFilter string) ([]whyAnswer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	p, err := getPipeline(ctx, c, ns, pipeline)
	if err != nil {
		return nil, err
	}
	names := pipelineEnvNames(p)
	if envFilter != "" && !slices.Contains(names, envFilter) {
		return nil, fmt.Errorf("environment %q not found in pipeline %q (environments: %s)", envFilter, pipeline, strings.Join(names, ", "))
	}
	sel := sigs_client.MatchingLabels{"kardinal.io/pipeline": p.Name}
	var bundles v1alpha1.BundleList
	var steps v1alpha1.PromotionStepList
	var gates v1alpha1.PolicyGateList
	if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	if err := c.List(ctx, &steps, sigs_client.InNamespace(ns), sel); err != nil {
		return nil, fmt.Errorf("list promotion steps: %w", err)
	}
	if err := c.List(ctx, &gates, sigs_client.InNamespace(ns), sel); err != nil {
		return nil, fmt.Errorf("list policy gates: %w", err)
	}
	envs := currentEnvironments(p, bundles.Items, steps.Items, gates.Items)
	failed := p.Status.FailedVersion
	if len(p.Status.Environments) == 0 {
		// A controller that predates status.environments (and failedVersion).
		failed = pipelinerec.FailedVersion(p.Name, bundles.Items)
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
	// The ChangeWindows a window gate reads, to say when it opens.
	windows := changeWindows(ctx, c, gates.Items)
	var answers []whyAnswer
	// The newest version failed: that comes first, for every environment
	// when it failed before any step, else for the one whose step failed.
	if failed != nil && (envFilter == "" || failed.Environment == "" || failed.Environment == envFilter) {
		answers = append(answers, whyFailed(p, failed))
	}
	receipts := pipelineReceipts(ctx, c, ns, p.Name)
	for _, e := range envs {
		if envFilter != "" && e.Name != envFilter {
			continue
		}
		a := whyEnvironment(p, e, envs, gates.Items, steps.Items, byName, me, windows, whyNow())
		// The signed receipt of the version running there (docs/receipts.md).
		if rc, ok := receipts[receiptKey{e.Bundle, e.Name}]; ok && e.Bundle != "" {
			a.lines = append(a.lines, whyLine{"Receipt", fmt.Sprintf("%s, %s: kardinal verify-receipt %s %s --bundle %s",
				rc.Name, receiptMark(rc), p.Name, e.Name, e.Bundle)})
		}
		answers = append(answers, a)
	}
	return answers, nil
}

// currentEnvironments is p's environments as the objects just read show
// them. The controller writes a step's move between running states into
// status.environments up to 2 seconds late (#1719), so an environment
// whose step just started could read "has no version yet" (#2022). why
// projects each environment that has a step again, the way the controller
// does (retired Bundles' steps included), keeping status.environments'
// since times. An environment without a step here (with its Bundle) keeps
// its status entry: a fleet's targets are resolved by the controller only.
func currentEnvironments(p *v1alpha1.Pipeline, bundles []v1alpha1.Bundle, steps []v1alpha1.PromotionStep,
	gates []v1alpha1.PolicyGate) []v1alpha1.PipelineEnvironmentStatus {
	all := lifecycle.AddRetiredSteps(steps, bundles, map[string]string{lifecycle.LabelPipeline: p.Name})
	fresh := pipelinerec.ProjectEnvironments(p, bundles, all, gates, p.Status.Environments, time.Time{})
	if len(p.Status.Environments) == 0 {
		return fresh
	}
	// The projection counts a step only with its Bundle.
	known := map[string]bool{}
	for i := range bundles {
		known[bundles[i].Name] = true
	}
	stepped := map[string]bool{}
	for _, s := range all {
		if s.Spec.PipelineName == p.Name && known[s.Spec.BundleName] {
			stepped[s.Spec.Environment] = true
		}
	}
	byName := map[string]v1alpha1.PipelineEnvironmentStatus{}
	for _, e := range fresh {
		byName[e.Name] = e
	}
	out := make([]v1alpha1.PipelineEnvironmentStatus, len(p.Status.Environments))
	for i, e := range p.Status.Environments {
		if f, ok := byName[e.Name]; ok && stepped[e.Name] {
			e = f
		}
		out[i] = e
	}
	return out
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
	gates []v1alpha1.PolicyGate, steps []v1alpha1.PromotionStep, byName map[string]*v1alpha1.Bundle, me *Identity,
	windows []v1alpha1.ChangeWindow, now time.Time) whyAnswer {
	incoming := e.IncomingBundle
	incomingVersion := e.IncomingVersion
	if incoming == "" && e.State != "" && e.State != "Verified" {
		incoming, incomingVersion = e.Bundle, e.Version
	}
	version := whyLine{"Version", versionLabel(incomingVersion, incoming)}
	// Who made the version (docs/actors.md), when it is known.
	if b := byName[incoming]; b != nil {
		if a := bundleActor(b); a.Kind != v1alpha1.ActorUnknown || a.Principal != "" {
			version.value += ", by " + actor.Describe(a)
		}
	}
	running := shortVersion(e.Version, e.Bundle)

	if p.Spec.Paused {
		a := whyAnswer{sentence: fmt.Sprintf("%s is paused with pipeline %s: nothing new promotes until it is resumed.", e.Name, p.Name)}
		if incoming != "" {
			a.lines = append(a.lines, version)
		}
		a.lines = append(a.lines, whyLine{"Next", "kardinal resume " + p.Name + nsArg(p.Namespace)})
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
		a.lines = append(a.lines, whyLine{"Next", fmt.Sprintf("kardinal unhold %s %s%s", p.Name, e.Name, nsArg(p.Namespace))})
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
			author, known := lifecycle.BundleAuthor(nil), false
			if b := byName[incoming]; b != nil {
				author, known = lifecycle.BundleAuthor(b), true
			}
			return whyGate(p, e.Name, version, mine, me, author, known, byName[incoming], windows, now)
		}
		return whyAnswer{sentence: fmt.Sprintf("%s is held by gate %s.", e.Name, strings.Join(e.BlockedBy, ", ")),
			lines: []whyLine{version, {"Next", fmt.Sprintf("kardinal explain %s %s%s", p.Name, e.Name, nsArg(p.Namespace))}}}
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
			a.quoted = append(a.quoted, "PR")
		}
		if spec := environmentSpec(p, e.Name); spec != nil && spec.PR != nil && spec.PR.Merge != nil && spec.PR.Merge.Auto {
			a.lines = append(a.lines, whyLine{"Merge", "the SCM merges it once its checks pass (pr.merge.auto)"})
		}
		return a
	case state.Tone == display.ToneFailed:
		a := whyAnswer{sentence: fmt.Sprintf("%s did not promote %s: %s.", e.Name, versionLabel(incomingVersion, incoming), strings.ToLower(state.Label))}
		if msg := stepMessage(steps, e.Name, incoming); msg != "" {
			a.lines = append(a.lines, whyLine{"Why", msg})
			a.quoted = append(a.quoted, "Why")
		}
		a.lines = append(a.lines, whyLine{"Next", fmt.Sprintf("kardinal logs %s %s%s", p.Name, e.Name, nsArg(p.Namespace))})
		if e.Bundle != "" {
			a.lines = append(a.lines, whyLine{"Or", fmt.Sprintf("kardinal rollback %s %s%s, back to %s", p.Name, e.Name, nsArg(p.Namespace), running)})
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
			lines: []whyLine{{"Next", fmt.Sprintf("kardinal ship %s --image <ref>%s", p.Name, nsArg(p.Namespace))}}}
	}
	if b, where := onItsWay(p, e, envs, byName); b != nil {
		// A newer version is on its way, still upstream (#1940).
		return whyAnswer{sentence: fmt.Sprintf("%s runs %s; %s is on its way (%s).", e.Name, running,
			versionLabel(versionOf(byName, b.Name), b.Name), where),
			lines: []whyLine{{"Follow", fmt.Sprintf("kardinal status %s%s -w", p.Name, nsArg(p.Namespace))}}}
	}
	return whyAnswer{sentence: fmt.Sprintf("%s runs %s and nothing is waiting.", e.Name, running)}
}

// onItsWay is the newest version of p in flight that is newer than what e
// runs and has not reached e yet, and where it is in words ("live in dev",
// "promoting in uat"); nil when there is none.
func onItsWay(p *v1alpha1.Pipeline, e v1alpha1.PipelineEnvironmentStatus, envs []v1alpha1.PipelineEnvironmentStatus,
	byName map[string]*v1alpha1.Bundle) (*v1alpha1.Bundle, string) {
	running := byName[e.Bundle]
	var newest *v1alpha1.Bundle
	for _, b := range byName {
		inFlight := b.Status.Phase == "" || b.Status.Phase == "Available" || b.Status.Phase == "Promoting"
		if b.Spec.Pipeline != p.Name || !inFlight || lifecycle.Halted(b) || b.Name == e.Bundle ||
			(running != nil && lifecycle.CompareCreation(b, running) <= 0) {
			continue
		}
		if newest == nil || lifecycle.CompareCreation(b, newest) > 0 {
			newest = b
		}
	}
	if newest == nil {
		return nil, ""
	}
	var live, promoting []string
	for _, o := range envs {
		switch newest.Name {
		case o.IncomingBundle:
			promoting = append(promoting, o.Name)
		case o.Bundle:
			live = append(live, o.Name)
		}
	}
	switch {
	case len(promoting) > 0:
		return newest, "promoting in " + strings.Join(promoting, ", ")
	case len(live) > 0:
		return newest, "live in " + strings.Join(live, ", ")
	}
	return newest, "not in any environment yet"
}

// whyGate is why's answer for gates that hold an environment: the first
// says what it waits for, the others follow as more lines. creator is the
// version's kardinal.io/created-by; known is false when the version was not
// read, and then nothing is said about its creator. b is the version's
// Bundle (nil when not read): a soak gate's progress is read from it at now.
func whyGate(p *v1alpha1.Pipeline, env string, version whyLine, gates []v1alpha1.PolicyGate, me *Identity, author v1alpha1.Actor, known bool,
	b *v1alpha1.Bundle, windows []v1alpha1.ChangeWindow, now time.Time) whyAnswer {
	g := gates[0]
	name := gateDisplayName(g)
	var a whyAnswer
	switch {
	case g.Spec.Approval != nil && !lifecycle.ApprovalWaived(&g):
		var id *lifecycle.ApproverIdentity
		if me != nil {
			id = &lifecycle.ApproverIdentity{User: me.Username, Groups: me.Groups}
		}
		st := lifecycle.ApprovalStandingOf(&g, author, id)
		// A version not read has an unknown creator, not none: nothing is
		// said about excludeAuthor then.
		creatorHeld := st.Block == lifecycle.ApprovalNoCreator || st.Block == lifecycle.ApprovalComponentCreator
		if st.Block.Held() && (known || !creatorHeld) {
			// The gate holds whatever approvals it gets: the server's own
			// decision, said in the CLI's words.
			a.sentence = fmt.Sprintf("%s is held by gate %s: nobody can approve this version.", env, name)
			a.lines = []whyLine{version,
				{"Why", unapprovable(st.Block, author.Principal)},
				{"Next", fmt.Sprintf("ship it again as yourself: kardinal ship %s --image <ref>%s", p.Name, nsArg(p.Namespace))},
				{"Or", fmt.Sprintf("kardinal override %s %s --gate %s --reason <why>%s", p.Name, env, name, nsArg(p.Namespace))}}
			if st.Block == lifecycle.ApprovalTooMany {
				a.lines[2].value = fmt.Sprintf("delete the stale Approvals: kubectl get approvals -n %s", p.Namespace)
			}
			break
		}
		by, next := approvalBy(st, me != nil && !creatorHeld, fmt.Sprintf("kardinal approve %s %s%s", p.Name, env, nsArg(p.Namespace)))
		a.sentence = fmt.Sprintf("%s is waiting for %s.", env, by)
		a.lines = []whyLine{version,
			{"Gate", fmt.Sprintf("%s · %d of %d approvals", name, st.Approved, st.Required)},
			{"Next", next}}
		// Rejected; or, the version not read (so whether excludeAuthor holds
		// it is unknown), a known rejection is what to say.
		if st.Block == lifecycle.ApprovalRejected || (creatorHeld && !known && len(st.RejectedBy) > 0) {
			// A counted reject blocks it: no approval opens it.
			by := st.RejectedBy
			who := by[len(by)-1]
			if len(by) > 1 {
				who = strings.Join(by[:len(by)-1], ", ") + " and " + who
			}
			a.sentence = fmt.Sprintf("%s was rejected by %s.", env, who)
			a.lines[2].value = fmt.Sprintf("ship a fixed version: kardinal ship %s --image <ref>%s", p.Name, nsArg(p.Namespace))
			a.lines = append(a.lines, whyLine{"Or", fmt.Sprintf("kardinal override %s %s --gate %s --reason <why>%s", p.Name, env, name, nsArg(p.Namespace))})
		}
	default:
		a.sentence = fmt.Sprintf("%s is held by gate %s.", env, name)
		a.lines = []whyLine{version}
		if g.Spec.Message != "" {
			a.lines = append(a.lines, whyLine{"Why", g.Spec.Message})
		}
		a.lines = append(a.lines, whyLine{"Rule", g.Spec.Expression})
		soak, isSoak := soakOf(p, b, &g, now)
		if isSoak {
			a.lines = append(a.lines, whyLine{"Now", soak.now()})
		} else if detail := graph.GateDetail(&g); detail != "" {
			a.lines = append(a.lines, whyLine{"Now", detail})
		}
		every := g.Spec.RecheckInterval
		if every == "" {
			every = "5m"
		}
		override := fmt.Sprintf("kardinal override %s %s --gate %s --reason <why>%s", p.Name, env, name, nsArg(p.Namespace))
		follow := fmt.Sprintf("kardinal status %s%s -w follows it", p.Name, nsArg(p.Namespace))
		// A gate that opens by itself is waited for; the override (a
		// recorded bypass) is only the alternative.
		switch {
		case isSoak && soak.opens() != "":
			a.lines = append(a.lines, whyLine{"Opens", "by itself " + soak.opens() + "; rechecked every " + every},
				whyLine{"Next", fmt.Sprintf("wait: it opens by itself %s (%s)", soak.opens(), follow)},
				whyLine{"Or", override})
		case windowOpens(&g, windows, now) != "":
			// A ChangeWindow gate opens when its window does (#1940).
			opens := windowOpens(&g, windows, now)
			a.lines = append(a.lines, whyLine{"Opens", "by itself " + opens + ", when ChangeWindow " + windowOfGate(&g) + " allows it; rechecked every " + every},
				whyLine{"Next", fmt.Sprintf("wait: it opens by itself %s (%s)", opens, follow)},
				whyLine{"Or", override})
		case rechecks(&g):
			a.lines = append(a.lines, whyLine{"Opens", "by itself when the expression passes; rechecked every " + every},
				whyLine{"Next", fmt.Sprintf("wait: it opens by itself when the expression passes (%s)", follow)},
				whyLine{"Or", override})
		default:
			a.lines = append(a.lines,
				whyLine{"Opens", "when what it reads changes (nothing in it changes with time); rechecked every " + every},
				whyLine{"Next", override})
		}
	}
	for _, other := range gates[1:] {
		also := "gate " + gateDisplayName(other)
		if soak, ok := soakOf(p, b, &other, now); ok {
			also += ": " + soak.now()
			if _, live := soak.opensIn(); live && soak.opens() != "" {
				also += ", opens by itself " + soak.opens()
			}
		}
		a.lines = append(a.lines, whyLine{"Also", also})
	}
	return a
}

// unapprovable says, in the CLI's words, why no approval can open an
// approval gate held by block (lifecycle.ApprovalBlock.Held) for a version
// created by creator. With excludeAuthor, a version with no verified creator
// (kardinal.io/created-by), or one a kardinal component created (the UI, the
// Bundle API, a Subscription), has no author to exclude.
func unapprovable(block lifecycle.ApprovalBlock, creator string) string {
	switch block {
	case lifecycle.ApprovalTooMany:
		return fmt.Sprintf("more than %d Approvals for this version; delete the stale ones (kubectl get approvals)", lifecycle.MaxGateApprovals)
	case lifecycle.ApprovalNoCreator:
		return "the gate excludes the version's author, and the version has no verified creator (kardinal.io/created-by)"
	case lifecycle.ApprovalComponentCreator:
		return fmt.Sprintf("the gate excludes the version's author, and the version was created by the kardinal component %s, not a person", creator)
	}
	return ""
}

// approvalBy is who an approval gate waits for, from the caller's standing
// with it (lifecycle.ApprovalStandingOf, the server's tally), and the Next
// line: "your approval" when the caller's approval would count; "approval by
// someone else" once it is counted; for the version's author, "someone other
// than you"; else who may ("alice, bob or group release-managers"). An
// unknown caller gets who may, or "approval".
func approvalBy(st lifecycle.ApprovalStanding, known bool, approve string) (by, next string) {
	list := ""
	switch n := len(st.Who); n {
	case 0:
	case 1:
		list = st.Who[0]
	default:
		list = strings.Join(st.Who[:n-1], ", ") + " or " + st.Who[n-1]
	}
	switch {
	case !known:
		if list != "" {
			return "approval by " + list, approve
		}
		return "approval", approve
	case st.CanApprove:
		return "your approval", approve
	case st.Block == lifecycle.ApprovalCounted:
		return "approval by someone else (you approved)", "someone else runs: " + approve
	case st.Block == lifecycle.ApprovalAuthor:
		if list != "" {
			return "approval by " + list + ", someone other than you (you created this version)", "someone else runs: " + approve
		}
		return "approval by someone other than you (you created this version)", "someone else runs: " + approve
	case list != "":
		return "approval by " + list, "one of them runs: " + approve
	}
	return "approval", approve
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
"your approval" only when your approval would count: you are in
allowedUsers or allowedGroups, or both are empty, with excludeAuthor you did
not create the version, and yours is not counted yet ("approval by someone
else (you approved)" once it is); else who may, "approval by alice or group
release-managers"; who rejected it, when someone did; nobody, when the
policy cannot be met. These come from the server's own tally of the gate's
Approvals, so why and the controller agree; any other gate: its message, Rule its expression, Now its last evaluation
(for a soak gate, how far the version soaked: "soak 0.4 of 3 min in uat"),
Opens when it opens and how often it is rechecked ("by itself in about
2m36s" for a soak gate; "by itself at Thursday 04:25 UTC, in about 18m" for
a gate that only reads a ChangeWindow); Next: kardinal approve; for a gate that opens by
itself, wait, with kardinal override only as Or; else kardinal override); its PR waits to be merged (PR and its URL); the version
failed (Why the step's message; Next: kardinal logs, or roll back); it is
promoting; it waits for an upstream environment; it has no version yet; a
newer version is on its way, still upstream ("prod runs 1.4.0; 1.5.0 is on
its way (live in test)."); or nothing is waiting ("prod runs 1.5.0 and
nothing is waiting."). Every command names -n <namespace> when the
pipeline's namespace is not the kubeconfig context's.

why shows the gates the version started with. A version gets its gates when
it starts promoting, so a gate created or edited later applies to the
versions shipped after it, not to one already waiting (Policy Gates, "When a
new or edited gate takes effect").

-o wide prints kardinal explain's table (every step and gate of the current
version, with kinds and expressions), with the commands that get past a
gate on stderr; -o json and yaml print explain's entries.`

// whyFailed is why's answer when the Pipeline's newest version failed
// (status.failedVersion): what failed, the reason, what fixes it where
// known, and where the whole record is.
func whyFailed(p *v1alpha1.Pipeline, f *v1alpha1.PipelineFailedVersion) whyAnswer {
	where := "failed before it started"
	if f.Environment != "" {
		where = "failed in " + f.Environment
	}
	detail, fix, _ := strings.Cut(f.Message, " — ")
	if own := failureFix(p, f); own != "" {
		fix = own
	}
	// The message can quote a step's (the SCM's, the GitOps tool's) text.
	a := whyAnswer{sentence: fmt.Sprintf("The newest version %s %s: %s", versionLabel(f.Version, f.Bundle), where, detail),
		quotedSentence: true}
	a.lines = append(a.lines, whyLine{"Reason", f.Reason})
	if fix != "" {
		a.lines = append(a.lines, whyLine{"Fix", fix})
	}
	a.lines = append(a.lines, whyLine{"See", fmt.Sprintf("kubectl get bundle %s -n %s -o yaml", f.Bundle, p.Namespace)})
	return a
}

// failureFix says what to do about a failure, in the CLI's words (the
// controller's message names the kinds); "" leaves the message's own hint.
func failureFix(p *v1alpha1.Pipeline, f *v1alpha1.PipelineFailedVersion) string {
	switch f.Reason {
	case "GraphBuildFailed":
		return "fix the pipeline, version or gate the message names; changing the pipeline or its gates retries this version"
	case "InvalidIntent", "InvalidPipeline", "CircularDependency":
		return "fix the pipeline (kubectl edit pipeline " + p.Name + " -n " + p.Namespace + "); the change retries this version"
	case "GraphRejected":
		return "kro could not run the Graph kardinal built for this version: check that the installed kro is at least the " +
			"version the chart's kro.version annotation names (helm show chart kardinal-promoter; " +
			"https://pnz1990.github.io/kardinal-promoter/installation/#install-kro), upgrade kardinal if a release fixes it, " +
			"or report it with this message; then ship the version again (kardinal ship " + p.Name + nsArg(p.Namespace) + " ...)"
	case "StepFailed":
		if f.Environment != "" {
			return fmt.Sprintf("fix what %s reports, then ship the version again, or roll it back: kardinal rollback %s --env %s%s",
				f.Environment, p.Name, f.Environment, nsArg(p.Namespace))
		}
	}
	return ""
}

// nsArg is " -n <ns>" when a command for ns must name it, as kardinal inbox
// does: ns is not the kubeconfig context's namespace, so the printed command
// runs as is when copied (#1940).
func nsArg(ns string) string {
	if ns == "" || ns == contextNamespace() {
		return ""
	}
	return " -n " + ns
}
