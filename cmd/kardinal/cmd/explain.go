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
the current Bundle there: the newest Bundle that is not Superseded and has a
PromotionStep in that environment, or a gate instance there and has not
failed. The Graph creates a Bundle's gate instances when the Bundle starts, so
an environment a promoting Bundle has not reached yet shows the gates it will
wait on; one a Failed Bundle never reached keeps the Bundle before it. When
every Bundle there is Superseded, the newest one with a PromotionStep there is
shown. Gates include org
gates from the policy namespaces and skip-permission gates: they are the
instances the Graph created for that Bundle, with the controller's latest
evaluation. Gates that are not ready are listed first.

A gate's STATE is the one the UI shows, the first that applies:

    Pass        ready
    Block       holding the Bundle: every upstream environment is Verified
                and the environment has no step yet, or the gate holds
                the environment's Pending step
    Superseded  the Bundle was superseded; the gate is not evaluated again
    Pending     not evaluated yet
    Waiting     evaluated not ready, not holding the Bundle: the Bundle has
                not reached the environment, or it failed (it can retry)

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

	var bundles v1alpha1.BundleList
	if err := c.List(ctx, &bundles, sigs_client.InNamespace(ns)); err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}

	type explainRow struct {
		environment string
		kind        string // "PolicyGate" or "Step"
		name        string
		state       string
		reason      string
		expression  string // CEL expression for PolicyGate rows (empty for Step rows)
		notReady    bool   // a PolicyGate that is not ready (Block, Waiting, Pending or Superseded)
	}

	current := currentBundleByEnv(bundles.Items, steps.Items, gates.Items)
	currentBundle := func(env string) string { return current[env] }
	bundleByName := make(map[string]*v1alpha1.Bundle, len(bundles.Items))
	for i := range bundles.Items {
		bundleByName[bundles.Items[i].Name] = &bundles.Items[i]
	}

	var rows []explainRow
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
		rows = append(rows, explainRow{
			environment: env,
			kind:        "PolicyGate",
			name:        gateDisplayName(g),
			state:       graph.GateState(pipe, bundleByName[bundle], &g, steps.Items),
			reason:      reason,
			expression:  expr,
			notReady:    !g.Status.Ready,
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

func stepState(s v1alpha1.PromotionStep) string {
	if s.Status.State == "" {
		return "Pending"
	}
	return s.Status.State
}
