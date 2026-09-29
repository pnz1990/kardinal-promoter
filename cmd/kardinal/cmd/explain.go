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
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

func newExplainCmd() *cobra.Command {
	var (
		envFlag   string
		watchFlag bool
		colorFlag bool
	)

	cmd := &cobra.Command{
		Use:   "explain <pipeline>",
		Short: "Explain the current state of a promotion pipeline",
		Long: `Explain displays, per environment, the PromotionStep and the PolicyGates of
the Bundle currently promoting there (or last promoted). Gates include org
gates from the policy namespaces: they are the instances the Graph created for
that Bundle, with the controller's latest evaluation.

Use --env to filter to a specific environment.
Use --watch to refresh every 3 seconds.
Use --color to force ANSI color output (auto-detected when writing to a TTY).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("explain: %w", err)
			}
			w := cmd.OutOrStdout()
			if !watchFlag {
				return explainOnce(w, c, ns, args[0], envFlag, colorFlag)
			}
			return explainWatch(cmd.Context(), w, c, ns, args[0], envFlag, colorFlag, 3*time.Second)
		},
	}

	cmd.Flags().StringVar(&envFlag, "env", "", "Filter to a specific environment")
	cmd.Flags().BoolVar(&watchFlag, "watch", false, "Refresh every 3 seconds")
	cmd.Flags().BoolVar(&colorFlag, "color", false, "Force ANSI color output (auto-detected when TTY)")

	return cmd
}

// explainWatch re-renders explain every interval until ctx is done. Errors
// are printed and polling continues. The screen is cleared only on a TTY.
func explainWatch(ctx context.Context, w io.Writer, c sigs_client.Client,
	ns, pipeline, envFilter string, forceColor bool, interval time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	tty := isTerminal(w)
	for {
		if tty {
			_, _ = fmt.Fprint(w, "\033[H\033[2J")
		}
		if err := explainOnce(w, c, ns, pipeline, envFilter, forceColor); err != nil {
			_, _ = fmt.Fprintf(w, "error: %v\n", err)
		}
		_, _ = fmt.Fprintln(w, "\n(watching — press Ctrl-C to quit)")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func explainOnce(w io.Writer, c sigs_client.Client, ns, pipeline, envFilter string, forceColor bool) error {
	ctx := context.Background()

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

	type explainRow struct {
		environment string
		kind        string // "PolicyGate" or "Step"
		name        string
		state       string
		reason      string
		expression  string // CEL expression for PolicyGate rows (empty for Step rows)
	}

	active := activeBundleByEnv(pipe, steps.Items, gates.Items)
	activeBundle := func(env string) string { return active[env] }

	var rows []explainRow
	for _, s := range steps.Items {
		env := s.Spec.Environment
		if envFilter != "" && env != envFilter {
			continue
		}
		if s.Spec.BundleName != activeBundle(env) {
			continue
		}
		reason := s.Status.Message
		if reason == "" {
			reason = "-"
		}
		rows = append(rows, explainRow{
			environment: env,
			kind:        "Step",
			name:        s.Spec.StepType,
			state:       stepState(s),
			reason:      reason,
			expression:  "-",
		})
	}

	for _, g := range gates.Items {
		env := g.Labels["kardinal.io/environment"]
		if envFilter != "" && env != envFilter {
			continue
		}
		bundle := g.Labels["kardinal.io/bundle"]
		if bundle == "" || bundle != activeBundle(env) {
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
		rows = append(rows, explainRow{
			environment: env,
			kind:        "PolicyGate",
			name:        gateDisplayName(g),
			state:       PolicyGatePhase(g),
			reason:      reason,
			expression:  expr,
		})
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

	// Sort by environment, then kind (PolicyGate before Step), then name.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].environment != rows[j].environment {
			return rows[i].environment < rows[j].environment
		}
		if rows[i].kind != rows[j].kind {
			return rows[i].kind < rows[j].kind
		}
		return rows[i].name < rows[j].name
	})

	// Render plain text first so tabwriter aligns on visible widths, then
	// color only the STATE cell of each row.
	var tableBuf strings.Builder
	tw := tabwriter.NewWriter(&tableBuf, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ENVIRONMENT\tTYPE\tNAME\tSTATE\tEXPRESSION\tREASON"); err != nil {
		return fmt.Errorf("write explain header: %w", err)
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			row.environment, row.kind, row.name, row.state, row.expression, row.reason,
		); err != nil {
			return fmt.Errorf("write explain row: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush explain table: %w", err)
	}

	output := tableBuf.String()
	if cr := newColorizer(w, forceColor); cr.enabled {
		lines := strings.SplitAfter(output, "\n")
		// Environment, type and name are Kubernetes names (ASCII), so the
		// header's byte offset of STATE is the cell's offset on every row.
		col := strings.Index(lines[0], "STATE")
		for i, row := range rows {
			line := lines[i+1]
			if col >= 0 && len(line) > col && strings.HasPrefix(line[col:], row.state) {
				lines[i+1] = line[:col] + cr.colorState(row.state) + line[col+len(row.state):]
			}
		}
		output = strings.Join(lines, "")
	}
	if _, err := fmt.Fprint(w, output); err != nil {
		return fmt.Errorf("write explain output: %w", err)
	}
	return nil
}

// activeBundleByEnv returns, per environment, the Bundle explain and status
// describe. Candidates are the Bundles with a PromotionStep there, ranked by
// stepStatePriority, and the Bundles waiting at its gates: gate instances
// there, no step yet, and a Verified step in every upstream environment (the
// Graph creates the step once the gates pass). A waiting Bundle ranks with a
// Pending step, so a Bundle held by a gate beats an older Verified one. Ties
// go to the newest. A Bundle whose gate instances exist but that has not
// reached the environment is used only when nothing else is there.
func activeBundleByEnv(pipe *v1alpha1.Pipeline, steps []v1alpha1.PromotionStep,
	gates []v1alpha1.PolicyGate) map[string]string {
	// A Pipeline the builder rejects (no environments, a cycle) has no
	// upstreams to check; its existing steps and gates are still shown.
	deps, _ := graph.EnvironmentDependencies(pipe)
	type key struct{ bundle, env string }
	stepStates := make(map[key][]string)
	for _, s := range steps {
		k := key{s.Spec.BundleName, s.Spec.Environment}
		stepStates[k] = append(stepStates[k], stepState(s))
	}
	verifiedIn := func(bundle, env string) bool {
		states := stepStates[key{bundle, env}]
		for _, st := range states {
			if st != "Verified" {
				return false
			}
		}
		return len(states) > 0
	}

	type candidate struct {
		bundle   string
		priority int
		created  time.Time
	}
	best := make(map[string]candidate)
	offer := func(env string, c candidate) {
		b, ok := best[env]
		if !ok || c.priority > b.priority ||
			(c.priority == b.priority && (c.created.After(b.created) ||
				(c.created.Equal(b.created) && c.bundle > b.bundle))) {
			best[env] = c
		}
	}
	for _, s := range steps {
		offer(s.Spec.Environment, candidate{s.Spec.BundleName, stepStatePriority(stepState(s)), s.CreationTimestamp.Time})
	}

	gateCreated := make(map[key]time.Time)
	for _, g := range gates {
		k := key{g.Labels["kardinal.io/bundle"], g.Labels["kardinal.io/environment"]}
		if k.bundle == "" {
			continue // a template, not an instance
		}
		if t, ok := gateCreated[k]; !ok || g.CreationTimestamp.After(t) {
			gateCreated[k] = g.CreationTimestamp.Time
		}
	}
	for k, created := range gateCreated {
		if _, stepped := stepStates[k]; stepped {
			continue
		}
		priority := -1 // not reached yet
		reached := true
		for _, up := range deps[k.env] {
			reached = reached && verifiedIn(k.bundle, up)
		}
		if reached {
			priority = stepStatePriority("Pending")
		}
		offer(k.env, candidate{k.bundle, priority, created})
	}

	out := make(map[string]string, len(best))
	for env, c := range best {
		out[env] = c.bundle
	}
	return out
}

func stepState(s v1alpha1.PromotionStep) string {
	if s.Status.State == "" {
		return "Pending"
	}
	return s.Status.State
}
