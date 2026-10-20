// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"

	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
)

func newExplainCmd() *cobra.Command {
	var (
		envFlag   string
		watchFlag bool
		colorFlag bool
	)

	cmd := &cobra.Command{
		Use:   "explain <pipeline> [<env>]",
		Short: "Deprecated: kardinal why <pipeline> [<env>] -o wide prints the same table",
		// Deprecated (#1758): kardinal why -o wide prints the same. It keeps
		// working, with a notice, and is left out of kardinal --help.
		Hidden: true,
		Long: `For each environment of <pipeline> (or just <env>), show the current
Bundle's step and the gates it waits on, gates that are not ready first, with
each gate's expression, state and reason, and what is deployed there now when
the change has not landed yet. -w refreshes every 3 seconds.`,
		Example: `  kardinal explain my-app
  kardinal explain my-app prod -w`,
		Annotations: map[string]string{ReferenceAnnotation: explainReference, outputAnnotation: "true", wideAnnotation: "true"},
		Args:        pipelineArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pipeline, env, err := envArgs(cmd, args, envFlag)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(errOut, explainDeprecation(pipeline, env, watchFlag))
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("explain: %w", err)
			}
			w := cmd.OutOrStdout()
			if !watchFlag {
				return explainHinted(contextOf(cmd), w, c, ns, pipeline, env, colorFlag)
			}
			return explainWatch(cmd.Context(), w, c, ns, pipeline, env, colorFlag, 3*time.Second)
		},
	}

	addEnvFlag(cmd, &envFlag, "Only this environment (or give it after the pipeline)")
	cmd.Flags().BoolVarP(&watchFlag, "watch", "w", false, "Refresh every 3 seconds")
	cmd.Flags().BoolVar(&colorFlag, "color", false, "Force ANSI color output (auto-detected when TTY)")

	return cmd
}

// explainDeprecation is the notice kardinal explain prints on stderr: the
// kardinal why command that prints the same (-o wide, or the -o format
// given, and -w).
func explainDeprecation(pipeline, env string, watch bool) string {
	use := "kardinal why " + pipeline
	if env != "" {
		use += " " + env
	}
	if structured() {
		use += " -o " + OutputFormat()
	} else {
		use += " -o wide"
	}
	if watch {
		use += " -w"
	}
	return "warning: kardinal explain is deprecated and will be removed in a later release: use " + use + " (the same output)"
}

// explainWatch re-renders explain every interval until ctx is done or the
// process gets SIGINT (Ctrl-C) or SIGTERM, then returns nil, as the other
// --watch commands do. Errors are printed (on stderr with -o json or yaml)
// and polling continues. The screen is cleared only for a table on a TTY.
func explainWatch(ctx context.Context, w io.Writer, c sigs_client.Client,
	ns, pipeline, envFilter string, forceColor bool, interval time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Only a table redraws in place with a footer; -o json and yaml print
	// one document per refresh, errors on stderr, like get pipelines -w.
	table := !structured()
	clearScreen := table && style.IsTerminal(w)
	for {
		if clearScreen {
			_, _ = fmt.Fprint(w, "\033[H\033[2J")
		}
		yamlDocument(w)
		if err := explainOnce(ctx, w, c, ns, pipeline, envFilter, forceColor); err != nil {
			if table {
				_, _ = fmt.Fprintf(w, "error: %v\n", HumanError(err))
			} else {
				_, _ = fmt.Fprintf(errOut, "error: %v\n", HumanError(err))
			}
		}
		if table {
			_, _ = fmt.Fprintln(w, watchFooter)
		}
		if ctx.Err() != nil {
			return nil // done during the render: no second one (watchLoop)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// explainHinted is explainOnce followed, on stderr, by the command that gets
// past each gate that blocks: kardinal explain and why without -w (a watch
// would repeat them every frame).
func explainHinted(ctx context.Context, w io.Writer, c sigs_client.Client, ns, pipeline, envFilter string, forceColor bool) error {
	return explainRender(ctx, w, c, ns, pipeline, envFilter, forceColor, true)
}

// explainNow is explain's clock (a soak gate's progress); a test replaces it.
var explainNow = time.Now

func explainOnce(ctx context.Context, w io.Writer, c sigs_client.Client, ns, pipeline, envFilter string, forceColor bool) error {
	return explainRender(ctx, w, c, ns, pipeline, envFilter, forceColor, false)
}

// explainRender renders explain to w, and with hints the unblock commands
// on stderr.
func explainRender(ctx context.Context, w io.Writer, c sigs_client.Client, ns, pipeline, envFilter string, forceColor, hints bool) error {
	env := envOf(ctx)

	pipe, err := getPipeline(ctx, c, ns, pipeline)
	if err != nil {
		return err
	}
	envNames := pipelineEnvNames(pipe)
	if envFilter != "" && !containsString(envNames, envFilter) {
		return fmt.Errorf("environment %q not found in pipeline %q (environments: %s)",
			envFilter, pipeline, strings.Join(envNames, ", "))
	}

	var steps v1alpha1.PromotionStepList
	if err := c.List(ctx, &steps,
		sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
	); err != nil {
		return fmt.Errorf("list promotion steps: %w", err)
	}

	// Gate instances the Graph stamped for this pipeline's Bundles (org and
	// team gates alike; they all carry the pipeline and bundle labels).
	var gates v1alpha1.PolicyGateList
	if err := c.List(ctx, &gates,
		sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline},
	); err != nil {
		return fmt.Errorf("list policy gates: %w", err)
	}

	var bundles v1alpha1.BundleList
	if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}
	// Retired Bundles (#1492) keep their steps in status.retiredSteps.
	steps.Items = lifecycle.AddRetiredSteps(steps.Items, bundles.Items, map[string]string{"kardinal.io/pipeline": pipeline})

	type explainRow struct {
		environment string
		bundle      string
		kind        string // "PolicyGate" or "Step"
		name        string
		state       string
		reason      string
		expression  string // CEL expression for PolicyGate rows (empty for Step rows)
		notReady    bool   // a PolicyGate that is not ready (Block, Waiting, Pending or Superseded)
		progress    string // a soak gate's progress (gateSoak), when it does not pass
	}

	current := currentBundleByEnv(bundles.Items, steps.Items, gates.Items)
	currentBundle := func(env string) string { return current[env] }
	bundleByName := make(map[string]*v1alpha1.Bundle, len(bundles.Items))
	for i := range bundles.Items {
		bundleByName[bundles.Items[i].Name] = &bundles.Items[i]
	}

	var rows []explainRow
	var unblock []string // next-step hints, for stderr
	for _, s := range steps.Items {
		env := s.Spec.Environment
		if envFilter != "" && env != envFilter {
			continue
		}
		if s.Spec.BundleName != currentBundle(env) {
			continue
		}
		reason := s.Status.Message
		if reason == "" {
			reason = "-"
		}
		rows = append(rows, explainRow{
			environment: env,
			bundle:      s.Spec.BundleName,
			kind:        "Step",
			name:        s.Spec.StepType,
			state:       stepState(s),
			reason:      reason,
			expression:  "-",
		})
	}

	windows := changeWindows(ctx, c, gates.Items)
	for _, g := range gates.Items {
		env := g.Labels["kardinal.io/environment"]
		if envFilter != "" && env != envFilter {
			continue
		}
		bundle := g.Labels["kardinal.io/bundle"]
		if bundle == "" || bundle != currentBundle(env) {
			continue
		}
		reason := g.Status.Reason
		if reason == "" {
			reason = "-"
		}
		expr := g.Spec.Expression
		if expr == "" {
			expr = "-"
		}
		progress, opens := "", ""
		if soak, ok := soakOf(pipe, bundleByName[bundle], &g, explainNow()); ok && !g.Status.Ready {
			progress, opens = soak.now(), soak.opens()
			if opens != "" {
				progress += ", opens " + opens
			}
		} else if at := windowOpens(&g, windows, explainNow()); at != "" && !g.Status.Ready {
			// A ChangeWindow gate opens when its window does (#1940).
			progress, opens = "ChangeWindow "+windowOfGate(&g)+" blocks, opens "+at, at
		}
		rows = append(rows, explainRow{
			environment: env,
			bundle:      bundle,
			kind:        "PolicyGate",
			name:        gateDisplayName(g),
			state:       graph.GateState(pipe, bundleByName[bundle], &g, steps.Items),
			reason:      reason,
			expression:  expr,
			notReady:    !g.Status.Ready,
			progress:    progress,
		})
		// How to get past a gate that was evaluated and does not pass. A
		// gate that opens by itself is waited for; its override comes as
		// the alternative.
		if !g.Status.Ready && g.Status.LastEvaluatedAt != nil {
			override := fmt.Sprintf("kardinal override %s %s --gate %s --reason <why>%s", pipeline, env, gateDisplayName(g), nsArg(ns))
			hints := []string{"Override: " + override}
			switch {
			case g.Spec.Approval != nil:
				hints = []string{fmt.Sprintf("Approve: kardinal approve %s %s%s", pipeline, env, nsArg(ns))}
			case opens != "":
				hints = []string{fmt.Sprintf("Wait: %s opens by itself %s (kardinal status %s%s -w)", gateDisplayName(g), opens, pipeline, nsArg(ns)), "Or: " + override}
			case rechecks(&g):
				hints = []string{fmt.Sprintf("Wait: %s opens by itself when its expression passes (kardinal status %s%s -w)", gateDisplayName(g), pipeline, nsArg(ns)), "Or: " + override}
			}
			for _, hint := range hints {
				if !slices.Contains(unblock, hint) {
					unblock = append(unblock, hint)
				}
			}
		}
	}

	// Sort by environment, then kind (PolicyGate before Step), then gates
	// that are not ready before the ones that pass, then name.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].environment != rows[j].environment {
			return rows[i].environment < rows[j].environment
		}
		if rows[i].kind != rows[j].kind {
			return rows[i].kind < rows[j].kind
		}
		if rows[i].notReady != rows[j].notReady {
			return rows[i].notReady
		}
		return rows[i].name < rows[j].name
	})

	if env.structured() {
		out := explainOutput{Pipeline: pipeline, Namespace: ns, Entries: []explainEntry{}, Holds: explainHolds(pipe, envFilter)}
		for _, row := range rows {
			out.Entries = append(out.Entries, explainEntry{
				Environment: row.environment, Bundle: row.bundle, Type: row.kind, Name: row.name,
				State: row.state, Expression: noDash(row.expression), Reason: noDash(row.reason), Progress: row.progress,
			})
		}
		return env.writeStructured(w, out)
	}

	if len(rows) == 0 {
		msg := fmt.Sprintf("No promotion for pipeline %q yet\n", pipeline)
		if envFilter != "" {
			msg = fmt.Sprintf("No promotion for %q in pipeline %q yet\n", envFilter, pipeline)
		}
		if _, err := fmt.Fprint(w, msg); err != nil {
			return fmt.Errorf("write empty message: %w", err)
		}
		return nil
	}

	// --color colours the STATE cells off a terminal too.
	st := env.sty
	if forceColor {
		st = st.ForceColor()
	}
	tw := style.NewTable(w, 3)
	if _, err := fmt.Fprintln(tw, "ENVIRONMENT\tVERSION\tTYPE\tNAME\tSTATE\tEXPRESSION\tREASON"); err != nil {
		return fmt.Errorf("write explain header: %w", err)
	}
	for _, row := range rows {
		state := display.Step(row.state)
		if row.state == "" {
			state = display.Step("Pending")
		}
		if row.kind == "PolicyGate" {
			state = display.Gate(row.state)
		}
		reason := row.reason
		if row.progress != "" {
			reason += "; " + row.progress
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.environment, versionLabel(versionOf(bundleByName, row.bundle), row.bundle), row.kind, row.name, st.State(state), row.expression, reason,
		); err != nil {
			return fmt.Errorf("write explain row: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush explain table: %w", err)
	}
	for _, h := range rejectedLiveHints(pipeline, current, bundles.Items, steps.Items, envFilter) {
		if _, err := fmt.Fprintln(w, notice(ctx, h)); err != nil {
			return fmt.Errorf("write explain hint: %w", err)
		}
	}
	if err := writeExplainDeployed(w, pipeline, envNames, envFilter, current, steps.Items, bundleByName); err != nil {
		return err
	}
	if err := writeExplainHolds(w, pipe, envFilter, bundleByName); err != nil {
		return err
	}
	// Hints go to stderr: stdout stays the table.
	if !hints {
		unblock = nil
	}
	for _, h := range unblock {
		label, cmdline, _ := strings.Cut(h, ": ")
		_, _ = fmt.Fprintf(env.errOut, "%s %s\n", env.sty.Dim(label+":"), cmdline)
	}
	return nil
}

// explainOutput is kardinal explain -o json|yaml: the rows of the table, with
// the API's state values, and the holds of the environments shown.
type explainOutput struct {
	Pipeline  string         `json:"pipeline"`
	Namespace string         `json:"namespace"`
	Entries   []explainEntry `json:"entries"`
	Holds     []explainHold  `json:"holds,omitempty"`
}

// explainEntry is one row of kardinal explain: a step or a gate instance of
// the current Bundle of an environment.
type explainEntry struct {
	Environment string `json:"environment"`
	Bundle      string `json:"bundle"`
	// Type is Step or PolicyGate.
	Type string `json:"type"`
	Name string `json:"name"`
	// State is the step's state or the gate's (Pass, Block, Waiting,
	// Pending, Superseded, Rejected), as the API has it.
	State string `json:"state"`
	// Expression is a gate's CEL expression; omitted for a step.
	Expression string `json:"expression,omitempty"`
	// Reason is a step's message or a gate's status reason; omitted when
	// there is none.
	Reason string `json:"reason,omitempty"`
	// Progress is, for a soak gate that does not pass, how far the version
	// soaked and when the gate opens ("soak 0.4 of 3 min in uat, opens in
	// about 2m36s"), from the Bundle's status.environments[].healthCheckedAt.
	Progress string `json:"progress,omitempty"`
}

// noDash is s, or "" for the table's "-" placeholder: JSON carries no
// display placeholders.
func noDash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// explainHold is a hold (kardinal rollback --hold) of an environment shown.
type explainHold struct {
	Environment string `json:"environment"`
	Bundle      string `json:"bundle"`
	CreatedBy   string `json:"createdBy,omitempty"`
	Reason      string `json:"reason,omitempty"`
	// BundleMissing is true when the held rollback Bundle does not exist.
	BundleMissing bool `json:"bundleMissing,omitempty"`
}

// explainHolds is the holds of p that are in effect in the environments
// explain shows (envFilter, or all).
func explainHolds(p *v1alpha1.Pipeline, envFilter string) []explainHold {
	var out []explainHold
	for _, h := range p.Spec.Holds {
		if (envFilter != "" && h.Environment != envFilter) || h.Expired(time.Now()) {
			continue
		}
		st := p.HoldState(&h)
		out = append(out, explainHold{Environment: h.Environment, Bundle: h.Bundle, CreatedBy: h.CreatedBy, Reason: h.Reason,
			BundleMissing: st != nil && st.State == v1alpha1.HoldStateBundleMissing})
	}
	return out
}

// writeExplainHolds prints the holds (spec.holds, kardinal rollback --hold)
// of the shown environments: the rollback each is held on, who held it and
// why, and how to release it.
func writeExplainHolds(w io.Writer, p *v1alpha1.Pipeline, envFilter string, byName map[string]*v1alpha1.Bundle) error {
	var buf strings.Builder
	for _, h := range p.Spec.Holds {
		if (envFilter != "" && h.Environment != envFilter) || h.Expired(time.Now()) {
			continue
		}
		by := h.CreatedBy
		if by == "" {
			by = "unknown"
		}
		if st := p.HoldState(&h); st != nil && st.State == v1alpha1.HoldStateBundleMissing {
			since := ""
			if st.BundleMissingSince != nil {
				since = " since " + st.BundleMissingSince.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(&buf, "%s: held on %s by %s (%s), but that version does not exist%s. The hold stays in effect: no other version promotes here. Release with: kardinal unhold %s %s%s, then hold again if wanted\n",
				h.Environment, h.Bundle, by, h.Reason, since, p.Name, h.Environment, nsArg(p.Namespace))
			continue
		}
		b := byName[h.Bundle]
		if b != nil && isRollbackBundle(*b) {
			fmt.Fprintf(&buf, "%s: held on rollback %s by %s (%s); other versions do not promote here, and its gates here pass as EXEMPT while the controller verifies it. Release with: kardinal unhold %s %s%s\n",
				h.Environment, versionLabel(versionOf(byName, h.Bundle), h.Bundle), by, h.Reason, p.Name, h.Environment, nsArg(p.Namespace))
			continue
		}
		fmt.Fprintf(&buf, "%s: held on %s by %s (%s); other versions do not promote here. Release with: kardinal unhold %s %s%s\n",
			h.Environment, versionLabel(versionOf(byName, h.Bundle), h.Bundle), by, h.Reason, p.Name, h.Environment, nsArg(p.Namespace))
	}
	if buf.Len() == 0 {
		return nil
	}
	if _, err := fmt.Fprint(w, "\n"+buf.String()); err != nil {
		return fmt.Errorf("write holds: %w", err)
	}
	return nil
}

// writeExplainDeployed prints, for each environment with a current Bundle
// whose change has not landed there, the Bundle deployed there now.
func writeExplainDeployed(w io.Writer, pipeline string, envNames []string, envFilter string,
	current map[string]string, steps []v1alpha1.PromotionStep, byName map[string]*v1alpha1.Bundle) error {
	envs := slices.Sorted(slices.Values(envNames)) // the table's order
	deployed := deployedBundles(steps, pipeline, envs, byName)
	var buf strings.Builder
	tw := style.NewTable(&buf, 3)
	n := 0
	for _, env := range envs {
		if (envFilter != "" && env != envFilter) || current[env] == "" || deployed[env].bundle == current[env] {
			continue
		}
		if _, err := fmt.Fprintf(tw, "%s\tdeployed: %s\n", env, deployedLabelOf(deployed[env], byName)); err != nil {
			return fmt.Errorf("write deployed bundle: %w", err)
		}
		n++
	}
	if n == 0 {
		return nil
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush deployed bundles: %w", err)
	}
	if _, err := fmt.Fprint(w, "\n"+buf.String()); err != nil {
		return fmt.Errorf("write deployed bundles: %w", err)
	}
	return nil
}

func stepState(s v1alpha1.PromotionStep) string {
	if s.Status.State == "" {
		return "Pending"
	}
	return s.Status.State
}

// explainReference is the detail of explain for its reference page.
const explainReference = `Explain displays, per environment, the PromotionStep and the PolicyGates of
the current Bundle there: the newest Bundle that is not Superseded and has a
PromotionStep in that environment, or a gate instance there and has not
failed. The Graph creates a Bundle's gate instances when the Bundle starts, so
an environment a promoting Bundle has not reached yet shows the gates it will
wait on; one a Failed Bundle never reached keeps the Bundle before it. When
every Bundle there is Superseded, the newest one with a PromotionStep there is
shown. Gates include org
gates from the policy namespaces and skip-permission gates: they are the
instances the Graph created for that Bundle, with the controller's latest
evaluation. Gates that are not ready are listed first. VERSION names the
version each row belongs to: its tag, then its Bundle ("1.5.0 (app-x7k2p)").
kardinal why says the same in a sentence per environment.

An environment where that Bundle's change has not landed yet (it has not
reached the environment, or its PR is not merged) also gets a line after the
table with the version deployed there now (its image tags or config commit,
then its Bundle): the one whose change landed last, as kardinal rollback
judges it.
Image and config Bundles do not supersede each other: when the last one is an
image Bundle and a config commit landed before it, the line adds
"; config <sha> from <bundle>", and under a config Bundle it adds
"; images <tags> from <bundle>". "deployed: none" means no change has landed
there yet.

    prod   deployed: sha-1a2b3c4 (app-v1)
    uat    deployed: sha-5d6e7f8 (app-v2); config 9f8e7d6 from app-cfg1

A gate's STATE is the one the UI shows, the first that applies:

    Pass        ready
    Block       holding the Bundle: every upstream environment is Verified
                and the environment has no step yet, or the gate holds
                the environment's Pending step
    Superseded  the Bundle was superseded; the gate is not evaluated again
    Rejected    the Bundle was rejected (kardinal reject); not evaluated again
    Pending     not evaluated yet
    Waiting     evaluated not ready, not holding the Bundle: the Bundle has
                not reached the environment, or it failed (it can retry)

`
