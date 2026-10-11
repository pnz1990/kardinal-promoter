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
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/display"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	pipelinerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"

	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
)

func newStatusCmd() *cobra.Command {
	var (
		controllerNS string
		watchFlag    bool
	)
	cmd := &cobra.Command{
		Use:   "status [<pipeline>]",
		Short: "Show controller health or per-pipeline in-flight promotion details",
		Long: `Without a pipeline: the controller version and how many pipelines and
bundles there are. With one: per environment, the version running there, a
newer one on its way in, its state in words and since when, its PR, and the
gates holding it back. Run it first when a promotion is stuck; -w refreshes
it and -o json prints it for scripts.`,
		Example: `  kardinal status
  kardinal status my-app -w
  kardinal status my-app -o json | jq -r '.environments[] | .name + "=" + .version'`,
		Annotations: map[string]string{ReferenceAnnotation: statusReference, outputAnnotation: "true", wideAnnotation: "true"},
		Args:        cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return err // buildClient already provides actionable message
			}
			w := cmd.OutOrStdout()
			render := func() error {
				if len(args) == 1 {
					return statusPipelineWriter(w, c, ns, args[0])
				}
				return statusSummaryFn(w, c, controllerNS)
			}
			if !watchFlag {
				if err := render(); err != nil {
					return err
				}
				if len(args) == 1 && !structured() {
					printUILink(ns, args[0], "")
				}
				return nil
			}
			return watchLoop(cmd.Context(), w, getPipelinesWatchInterval, render)
		},
	}
	cmd.Flags().BoolVarP(&watchFlag, "watch", "w", false, "Refresh every 2 seconds (Ctrl-C to quit)")
	cmd.Flags().StringVar(&controllerNS, "controller-namespace", defaultControllerNamespace,
		"Namespace kardinal-promoter is installed in")
	return cmd
}

// statusSummary is kardinal status (no pipeline) -o json|yaml.
type statusSummary struct {
	// Controller is the controller version, "unknown" without its
	// kardinal-version ConfigMap.
	Controller string `json:"controller"`
	Pipelines  int    `json:"pipelines"`
	// Degraded is the namespace/name of every Degraded pipeline.
	Degraded      []string `json:"degraded,omitempty"`
	Bundles       int      `json:"bundles"`
	ActiveBundles int      `json:"activeBundles"`
}

// statusSummaryFn writes the cluster-level controller summary.
func statusSummaryFn(out io.Writer, c sigs_client.Reader, controllerNS string) error {
	ctx := context.Background()
	ctrlVersion := controllerVersion(ctx, c, controllerNS)

	var pipelines v1alpha1.PipelineList
	if err := c.List(ctx, &pipelines); err != nil {
		return fmt.Errorf("list pipelines: %w", err)
	}
	var bundles v1alpha1.BundleList
	if err := c.List(ctx, &bundles); err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}

	activeBundles := 0
	for _, b := range bundles.Items {
		if b.Status.Phase == "Available" || b.Status.Phase == "Promoting" {
			activeBundles++
		}
	}
	var degraded []string
	for _, p := range pipelines.Items {
		if p.Status.Phase == "Degraded" {
			degraded = append(degraded, p.Namespace+"/"+p.Name)
		}
	}
	sort.Strings(degraded)

	if structured() {
		return writeStructured(out, statusSummary{Controller: ctrlVersion, Pipelines: len(pipelines.Items),
			Degraded: degraded, Bundles: len(bundles.Items), ActiveBundles: activeBundles})
	}
	_, _ = fmt.Fprintf(out, "Controller:  %s\n", ctrlVersion)
	_, _ = fmt.Fprintf(out, "Pipelines:   %d", len(pipelines.Items))
	if len(degraded) > 0 {
		_, _ = fmt.Fprintf(out, " (%d degraded: %s)", len(degraded), strings.Join(degraded, ", "))
	}
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintf(out, "Bundles:     %d (%d active)\n", len(bundles.Items), activeBundles)
	if len(degraded) > 0 {
		_, _ = fmt.Fprintf(out,
			"\n%s %d pipeline(s) Degraded: run 'kardinal get pipelines' for details\n", sty.Warning("Warning:"), len(degraded))
	}
	return nil
}

// statusPipelineWriter renders the per-pipeline status to w using client c.
// It is separate from the command so tests can pass a fake client.
func statusPipelineWriter(w io.Writer, c sigs_client.Client, ns, pipeline string) error {
	ctx := context.Background()
	view, err := pipelineStatusOf(ctx, c, ns, pipeline)
	if err != nil {
		return err
	}
	if structured() {
		return writeStructured(w, view)
	}
	return writePipelineStatus(w, view)
}

// pipelineStatus is kardinal status <pipeline>: per environment what runs
// there and where the newest version stands (the Pipeline's
// status.environments), the summary sentence, and the gates holding a
// version back. -o json|yaml prints it with the API's state values.
type pipelineStatus struct {
	Pipeline  string `json:"pipeline"`
	Namespace string `json:"namespace"`
	Phase     string `json:"phase,omitempty"`
	Paused    bool   `json:"paused,omitempty"`
	Summary   string `json:"summary,omitempty"`
	// Source is "controller" when Environments and Summary are the
	// Pipeline's status (written by the controller), "cli" when the CLI
	// computed them because the controller predates them.
	Source        string                               `json:"source"`
	Environments  []v1alpha1.PipelineEnvironmentStatus `json:"environments"`
	BlockingGates []statusGate                         `json:"blockingGates"`
	// Notices are the Pipeline's own problems, before everything else: an
	// ERROR: line when it is not Ready, a WARNING: line when its git Secret
	// is not referenceable.
	Notices []string `json:"notices,omitempty"`
	// Warnings are the WARNING: lines after the table (a Rejected Bundle
	// whose change is live).
	Warnings []string `json:"warnings,omitempty"`
	// Regions is, per environment that has one, its region (spec regions)
	// or the cluster its health check reads (health.kubeconfigSecretRef,
	// or the deprecated health.cluster).
	Regions map[string]string `json:"regions,omitempty"`
	// activeSteps is, with -o wide, the sub-step each environment's newest
	// step is on.
	activeSteps map[string]string
}

// statusGate is a gate instance that holds the newest Bundle of an
// environment back.
type statusGate struct {
	Name        string `json:"name"`
	Instance    string `json:"instance"`
	Environment string `json:"environment"`
	Bundle      string `json:"bundle"`
	Expression  string `json:"expression"`
	// Message is the gate's spec.message when it explains the block
	// (graph.BlockedMessage).
	Message string `json:"message,omitempty"`
	// Reason is the gate's status.reason: its evaluation.
	Reason          string       `json:"reason,omitempty"`
	LastEvaluatedAt *metav1.Time `json:"lastEvaluatedAt,omitempty"`
}

// pipelineStatusOf reads the status of pipeline in ns: status.environments
// and status.summary as the controller wrote them, or, from a controller
// that predates them, the same projection computed here by the
// controller's own functions (pipelinerec.ProjectEnvironments, Summarize).
// The gates listed are the instances status.environments names in
// blockedBy.
func pipelineStatusOf(ctx context.Context, c sigs_client.Client, ns, pipeline string) (*pipelineStatus, error) {
	var pl v1alpha1.Pipeline
	if err := c.Get(ctx, types.NamespacedName{Name: pipeline, Namespace: ns}, &pl); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, pipelineNotFound(ctx, c, ns, pipeline)
		}
		return nil, fmt.Errorf("get pipeline: %w", err)
	}
	view := &pipelineStatus{Pipeline: pipeline, Namespace: ns, Phase: pl.Status.Phase, Paused: pl.Spec.Paused,
		Source: "controller", Environments: pl.Status.Environments, Summary: pl.Status.Summary, BlockingGates: []statusGate{},
		Notices: pipelineNotices(&pl)}

	var bundles v1alpha1.BundleList
	if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list bundles: %w", err)
	}
	var gates v1alpha1.PolicyGateList
	listGates := func() error {
		if err := c.List(ctx, &gates, sigs_client.InNamespace(ns),
			sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline}); err != nil {
			return fmt.Errorf("list policy gates: %w", err)
		}
		return nil
	}
	if len(view.Environments) == 0 {
		// A controller that predates status.environments (#1707).
		var steps v1alpha1.PromotionStepList
		if err := c.List(ctx, &steps, sigs_client.InNamespace(ns),
			sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline}); err != nil {
			return nil, fmt.Errorf("list promotion steps: %w", err)
		}
		if err := listGates(); err != nil {
			return nil, err
		}
		// Retired Bundles (#1492) keep their steps in status.retiredSteps.
		items := lifecycle.AddRetiredSteps(steps.Items, bundles.Items, map[string]string{"kardinal.io/pipeline": pipeline})
		view.Source = "cli"
		view.Environments = pipelinerec.ProjectEnvironments(&pl, bundles.Items, items, gates.Items, nil, time.Time{})
		for i := range view.Environments {
			// Since is known only for a Verified state without the
			// controller's record of when a state began.
			if e := &view.Environments[i]; e.State != "Verified" {
				e.Since = nil
			}
		}
		ready := metav1.Condition{}
		if cond := meta.FindStatusCondition(pl.Status.Conditions, "Ready"); cond != nil {
			ready = *cond
		}
		view.Summary = pipelinerec.Summarize(&pl, ready, view.Environments)
	} else if slices.ContainsFunc(view.Environments, func(e v1alpha1.PipelineEnvironmentStatus) bool { return len(e.BlockedBy) > 0 }) {
		if err := listGates(); err != nil {
			return nil, err
		}
	}
	if view.Environments == nil {
		view.Environments = []v1alpha1.PipelineEnvironmentStatus{}
	}
	view.Regions = environmentRegions(&pl)
	if OutputFormat() == "wide" {
		var steps v1alpha1.PromotionStepList
		if err := c.List(ctx, &steps, sigs_client.InNamespace(ns),
			sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline}); err != nil {
			return nil, fmt.Errorf("list promotion steps: %w", err)
		}
		view.activeSteps = activeSteps(view.Environments, steps.Items)
	}

	byName := make(map[string]*v1alpha1.Bundle, len(bundles.Items))
	for i := range bundles.Items {
		byName[bundles.Items[i].Name] = &bundles.Items[i]
	}
	for _, e := range view.Environments {
		newest := e.Bundle
		if e.IncomingBundle != "" {
			newest = e.IncomingBundle
		}
		for i := range gates.Items {
			g := &gates.Items[i]
			if g.Labels["kardinal.io/environment"] != e.Name || g.Labels["kardinal.io/bundle"] != newest ||
				!slices.Contains(e.BlockedBy, gateDisplayName(*g)) {
				continue
			}
			view.BlockingGates = append(view.BlockingGates, statusGate{
				Name: gateDisplayName(*g), Instance: g.Name, Environment: e.Name, Bundle: newest,
				Expression: g.Spec.Expression, Message: graph.BlockedMessage(g), Reason: g.Status.Reason,
				LastEvaluatedAt: g.Status.LastEvaluatedAt,
			})
		}
		// A Rejected Bundle whose change runs here (lifecycle.RejectedLiveStep).
		if b := byName[e.Bundle]; b != nil && lifecycle.Rejected(b) {
			view.Warnings = append(view.Warnings, fmt.Sprintf("WARNING: bundle %s is Rejected in %s: %s (kardinal rollback %s --env %s)",
				b.Name, e.Name, lifecycle.RejectedLiveHint, pipeline, e.Name))
		}
	}
	return view, nil
}

// environmentRegions is, per environment of p that has one, its region
// (spec regions, deprecated) or the cluster its health check reads: the
// kubeconfig Secret of health.kubeconfigSecretRef, or the deprecated
// health.cluster. nil when no environment has one: the REGION column is
// then hidden.
func environmentRegions(p *v1alpha1.Pipeline) map[string]string {
	out := map[string]string{}
	for _, e := range p.Spec.Environments {
		switch {
		case len(e.Regions) > 0: //nolint:staticcheck // SA1019: shows the deprecated regions of an old Pipeline
			out[e.Name] = strings.Join(e.Regions, ", ") //nolint:staticcheck // SA1019: as above
		case e.Health.KubeconfigSecretRef != nil:
			out[e.Name] = e.Health.KubeconfigSecretRef.Name
		case e.Health.Cluster != "": //nolint:staticcheck // SA1019: shows the deprecated cluster of an old Pipeline
			out[e.Name] = e.Health.Cluster //nolint:staticcheck // SA1019: as above
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// activeSteps is, per environment, the sub-step the newest Bundle's step
// there is on (the first that is neither Completed nor Failed).
func activeSteps(envs []v1alpha1.PipelineEnvironmentStatus, steps []v1alpha1.PromotionStep) map[string]string {
	out := map[string]string{}
	for _, e := range envs {
		newest := e.Bundle
		if e.IncomingBundle != "" {
			newest = e.IncomingBundle
		}
		for i := range steps {
			s := &steps[i]
			if s.Spec.Environment != e.Name || s.Spec.BundleName != newest {
				continue
			}
			for _, ss := range s.Status.Steps {
				if ss.State != "Completed" && ss.State != "Failed" && ss.State != "" {
					out[e.Name] = ss.Name
					break
				}
			}
		}
	}
	return out
}

// envState is the display state of an environment of status.environments:
// held by a rollback hold or by gates when it says so.
func envState(e v1alpha1.PipelineEnvironmentStatus) display.State {
	switch {
	case e.State == "":
		return display.State{Label: "-"}
	case e.Held:
		return display.Held(e.State)
	case len(e.BlockedBy) > 0:
		return display.HeldByGate(e.State, strings.Join(e.BlockedBy, ", "))
	}
	return display.Step(e.State)
}

// versionCell is "version (bundle)" for a cell of the status table
// (versionLabel), "-" without a bundle.
func versionCell(bundle, version string) string {
	if bundle == "" {
		return "-"
	}
	return versionLabel(version, bundle)
}

// writePipelineStatus writes view as kardinal status <pipeline> prints it.
func writePipelineStatus(w io.Writer, view *pipelineStatus) error {
	_, _ = fmt.Fprintf(w, "Pipeline: %s   Namespace: %s\n", view.Pipeline, view.Namespace)
	// The Pipeline's own problems come first: nothing promotes until they
	// are fixed.
	for _, n := range view.Notices {
		_, _ = fmt.Fprintln(w, notice(n))
	}
	running := slices.ContainsFunc(view.Environments, func(e v1alpha1.PipelineEnvironmentStatus) bool {
		return e.Bundle != "" || e.IncomingBundle != "" || e.State != ""
	})
	if !running {
		_, _ = fmt.Fprintf(w, "\nNo versions yet. Ship one: kardinal create bundle %s --image <ref>\n", view.Pipeline)
		return nil
	}
	if view.Summary != "" {
		_, _ = fmt.Fprintln(w, view.Summary)
	}
	_, _ = fmt.Fprintln(w)
	// REGION shows where an environment runs when one of them says (a
	// multi-cluster pipeline); ACTIVE STEP only with -o wide.
	wide := OutputFormat() == "wide"
	tw := style.NewTable(w, 2)
	header := "ENVIRONMENT\t"
	if view.Regions != nil {
		header += "REGION\t"
	}
	header += "RUNNING\tINCOMING\tSTATE\t"
	if wide {
		header += "ACTIVE STEP\t"
	}
	_, _ = fmt.Fprintln(tw, header+"SINCE\tPR")
	for _, e := range view.Environments {
		since := "-"
		if e.Since != nil && !e.Since.IsZero() {
			since = HumanAge(e.Since.Time)
		}
		row := e.Name + "\t"
		if view.Regions != nil {
			row += orDash(view.Regions[e.Name]) + "\t"
		}
		row += versionCell(e.Bundle, e.Version) + "\t" + versionCell(e.IncomingBundle, e.IncomingVersion) + "\t" + sty.State(envState(e)) + "\t"
		if wide {
			row += orDash(view.activeSteps[e.Name]) + "\t"
		}
		_, _ = fmt.Fprintln(tw, row+since+"\t"+orDash(e.PRURL))
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write status table: %w", err)
	}
	for _, h := range view.Warnings {
		_, _ = fmt.Fprintln(w, notice(h))
	}

	if len(view.BlockingGates) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Blocking gates")
		_, _ = fmt.Fprintln(w, sty.Rule(72))
		gtw := style.NewTable(w, 2)
		_, _ = fmt.Fprintln(gtw, "GATE\tENV\tEXPRESSION\tREASON\tLAST CHECKED")
		for _, g := range view.BlockingGates {
			lastChecked := "-"
			if g.LastEvaluatedAt != nil && !g.LastEvaluatedAt.IsZero() {
				lastChecked = HumanAge(g.LastEvaluatedAt.Time) + " ago"
			}
			// The gate's message is shown whole: it is what its author wrote
			// for this table. The CEL detail is in kardinal explain.
			reason := g.Message
			if reason == "" {
				reason = truncateRunes(orDash(g.Reason), 35)
			}
			_, _ = fmt.Fprintf(gtw, "%s\t%s\t%s\t%s\t%s\n", sty.Paint(display.ToneBlocked, g.Name), g.Environment,
				truncateRunes(orDash(g.Expression), 40), reason, lastChecked)
		}
		if err := gtw.Flush(); err != nil {
			return fmt.Errorf("write blocking gates: %w", err)
		}
	}
	return nil
}

// pipelineNotices are the Pipeline's own problems, shown before its
// promotions: an ERROR when it is not Ready (an invalid spec, or
// GitCredentialMissing with the command that fixes it), and a WARNING for a
// git Secret that works today but is not labelled referenceable, so it is
// fixed before v0.11 refuses it.
func pipelineNotices(p *v1alpha1.Pipeline) []string {
	var out []string
	if c := meta.FindStatusCondition(p.Status.Conditions, "Ready"); c != nil && c.Status == metav1.ConditionFalse {
		out = append(out, fmt.Sprintf("ERROR: pipeline %s is not Ready (%s): %s", p.Name, c.Reason, c.Message))
	}
	if c := meta.FindStatusCondition(p.Status.Conditions, "SecretReferenceable"); c != nil && c.Status == metav1.ConditionFalse {
		out = append(out, fmt.Sprintf("WARNING: pipeline %s: %s", p.Name, c.Message))
	}
	return out
}

// statusReference is the detail of status for its reference page.
const statusReference = `When called without arguments: displays the controller version (the
kardinal-version ConfigMap in --controller-namespace), the pipeline count with
any Degraded pipelines, and the bundle count (active = Available or Promoting).

When called with a pipeline name: first the Pipeline's own problems, an
ERROR when it is not Ready (such as GitCredentialMissing, with the command
that creates the git Secret) and a WARNING when its git Secret is not
labelled kardinal.io/referenceable=true; then the Pipeline's summary sentence
("1.5.0 live in test; waiting for PR merge in prod (#42)"), then one row per environment
(a fleet environment expands to its targets) from the Pipeline's
status.environments, which the controller writes:

  RUNNING   the Bundle whose change runs there, with its version
  INCOMING  a newer Bundle on its way in, with its version
  STATE     where the newest Bundle there stands, in words ("Live",
            "Waiting for PR merge", "Held by gate no-weekend-deploys",
            "Held by rollback")
  SINCE     how long it has been in that state
  PR        the pull request it waits for, while it waits for its merge

REGION, after ENVIRONMENT, shows where each environment runs when one of
them says: its region (spec regions) or the cluster its health check reads
(the health.kubeconfigSecretRef Secret). -o wide adds ACTIVE STEP, the step
the newest Bundle there is on (git-clone, open-pr, health-check, ...).

Then the gates holding a version back, with their CEL expression cut to 40
characters, their message and when each was last checked; and a WARNING: line
for a Rejected Bundle whose change still runs somewhere, with the rollback
that removes it. A pipeline nothing was shipped to yet says so, with the
command that ships the first version.

With a controller older than status.environments, the CLI computes the same
rows itself, with the controller's code; SINCE is then known only for live
environments. -o json and -o yaml print the same data with the API's state
values (Verified, WaitingForMerge) and "source": "controller" or "cli".
kardinal explain shows the steps and every gate of the current Bundle.`
