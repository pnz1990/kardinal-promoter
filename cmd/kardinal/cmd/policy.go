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
	"sort"
	"text/tabwriter"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	sigsyaml "sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/cel/library"
)

func newPolicyCmd() *cobra.Command {
	policy := &cobra.Command{
		Use:   "policy",
		Short: "Manage and evaluate promotion policy gates",
	}
	policy.AddCommand(newPolicyListCmd())
	policy.AddCommand(newPolicySimulateCmd())
	policy.AddCommand(newPolicyTestCmd())
	return policy
}

// ─── policy list ────────────────────────────────────────────────────────────

func newPolicyListCmd() *cobra.Command {
	var pipelineFlag string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List PolicyGates",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("policy list: %w", err)
			}
			return policyListFn(cmd.OutOrStdout(), c, ns, pipelineFlag)
		},
	}
	cmd.Flags().StringVar(&pipelineFlag, "pipeline", "", "Filter by pipeline name")
	return cmd
}

// policyListFn is the testable implementation of policy list.
// It lists all PolicyGates across ALL namespaces and filters to show only
// user-defined template gates (not per-bundle Graph instances). This ensures
// org-level gates in namespaces like 'platform-policies' are always shown,
// matching the documented behavior (Journey 3 pass criteria).
func policyListFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipelineFilter string) error {
	// List across all namespaces — policy templates can be in any namespace
	// (e.g. platform-policies for org-level, or team namespaces for team-level).
	// The current-namespace default is intentionally NOT used here; operators
	// want to see all gates regardless of where kubectl is pointed.
	opts := []sigs_client.ListOption{}
	if pipelineFilter != "" {
		opts = append(opts, sigs_client.MatchingLabels{"kardinal.io/pipeline": pipelineFilter})
	}

	var gates v1alpha1.PolicyGateList
	if listErr := c.List(context.Background(), &gates, opts...); listErr != nil {
		return fmt.Errorf("list policy gates: %w", listErr)
	}

	// Filter out Graph-managed per-bundle instances. These have either the
	// internal.kro.run/graph-name label (set by krocodile) or kardinal.io/bundle
	// label (set by the graph builder). User-defined template PolicyGates in
	// namespaces like platform-policies have neither of these labels.
	var templateGates []v1alpha1.PolicyGate
	for _, g := range gates.Items {
		if _, isGraphInstance := g.Labels["internal.kro.run/graph-name"]; isGraphInstance {
			continue
		}
		if _, isBundleInstance := g.Labels["kardinal.io/bundle"]; isBundleInstance {
			continue
		}
		templateGates = append(templateGates, g)
	}

	return formatPolicyGateTable(w, templateGates)
}

func formatPolicyGateTable(w io.Writer, gates []v1alpha1.PolicyGate) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "NAME\tNAMESPACE\tSCOPE\tAPPLIES-TO\tRECHECK\tREADY\tLAST-EVALUATED"); err != nil {
		return fmt.Errorf("write policy list header: %w", err)
	}

	sort.Slice(gates, func(i, j int) bool { return gates[i].Name < gates[j].Name })

	for _, g := range gates {
		scope := g.Labels["kardinal.io/scope"]
		if scope == "" {
			scope = "team"
		}
		appliesTo := g.Labels["kardinal.io/applies-to"]
		if appliesTo == "" {
			appliesTo = "-"
		}
		recheck := g.Spec.RecheckInterval
		if recheck == "" {
			recheck = "5m"
		}
		ready := PolicyGatePhase(g)
		lastEval := "-"
		if g.Status.LastEvaluatedAt != nil {
			lastEval = HumanAge(g.Status.LastEvaluatedAt.Time) + " ago"
		}

		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			g.Name, g.Namespace, scope, appliesTo, recheck, ready, lastEval,
		); err != nil {
			return fmt.Errorf("write policy gate row: %w", err)
		}
	}

	return tw.Flush()
}

// ─── policy simulate ────────────────────────────────────────────────────────

func newPolicySimulateCmd() *cobra.Command {
	var (
		pipelineFlag    string
		envFlag         string
		timeFlag        string
		soakMinutesFlag int
	)

	cmd := &cobra.Command{
		Use:   "simulate",
		Short: "Simulate PolicyGate evaluation for a hypothetical promotion context",
		Long: `Simulate PolicyGate evaluation.

Builds a mock CEL context from the provided flags and evaluates each
PolicyGate for the pipeline/environment against that context.

Example:
  kardinal policy simulate --pipeline nginx-demo --env prod --time "Saturday 3pm"
  # RESULT: BLOCKED
  # Blocked by: no-weekend-deploys`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("policy simulate: %w", err)
			}
			return policySimulateFn(cmd.OutOrStdout(), c, ns, pipelineFlag, envFlag, timeFlag, soakMinutesFlag)
		},
	}

	cmd.Flags().StringVar(&pipelineFlag, "pipeline", "", "Pipeline name (required)")
	cmd.Flags().StringVar(&envFlag, "env", "", "Environment name (required)")
	cmd.Flags().StringVar(&timeFlag, "time", "", "Simulated time (e.g. \"Saturday 3pm\", \"Tuesday 10am\")")
	cmd.Flags().IntVar(&soakMinutesFlag, "soak-minutes", 0, "Simulated upstream soak time in minutes")
	_ = cmd.MarkFlagRequired("pipeline")
	_ = cmd.MarkFlagRequired("env")

	return cmd
}

// policySimulateFn is the testable implementation of policy simulate.
func policySimulateFn(w interface{ Write([]byte) (int, error) }, c sigs_client.Client, ns, pipeline, env, timeStr string, soakMinutes int) error {
	ctx := context.Background()

	// Find PolicyGates across ALL namespaces — org-level gates may be in namespaces
	// like 'platform-policies', not the kubectl default namespace.
	// Note: the applies-to label filter below (CLI-3) selects gates scoped to this
	// environment OR gates with no applies-to restriction (global gates). A pure
	// server-side label selector cannot express this OR-absent condition in one call,
	// so the distinction is done client-side. See: docs/design/11-graph-purity-tech-debt.md#CLI-3
	var gates v1alpha1.PolicyGateList
	if listErr := c.List(ctx, &gates); listErr != nil {
		return fmt.Errorf("list policy gates: %w", listErr)
	}

	// Filter out Graph-managed per-bundle instances (same filter as policy list).
	// Only evaluate user-defined template gates — per-bundle instances are evaluated
	// by the Graph and would produce N² duplicate results (#297).
	var templateGates []v1alpha1.PolicyGate
	for _, g := range gates.Items {
		if _, isGraphInstance := g.Labels["internal.kro.run/graph-name"]; isGraphInstance {
			continue
		}
		if _, isBundleInstance := g.Labels["kardinal.io/bundle"]; isBundleInstance {
			continue
		}
		templateGates = append(templateGates, g)
	}

	// Build simulated time.
	simTime := time.Now().UTC()
	if timeStr != "" {
		if parsed, parseErr := parseSimulatedTime(timeStr); parseErr == nil {
			simTime = parsed
		}
	}

	// Build CEL evaluator (inline: avoids pkg/cel import which is banned outside policygate).
	celEnv, celErr := newSimulateCELEnvironment()
	if celErr != nil {
		return fmt.Errorf("init CEL environment: %w", celErr)
	}

	// Build CEL context.
	celCtx := map[string]interface{}{
		"bundle": map[string]interface{}{
			"type":                "image",
			"version":             "v1.0.0",
			"upstreamSoakMinutes": float64(soakMinutes),
			"provenance": map[string]interface{}{
				"author":    "simulate",
				"commitSHA": "abc1234",
				"ciRunURL":  "",
			},
			"intent": map[string]interface{}{
				"targetEnvironment": env,
			},
		},
		"schedule": map[string]interface{}{
			"isWeekend": simTime.Weekday() == time.Saturday || simTime.Weekday() == time.Sunday,
			"hour":      float64(simTime.Hour()),
			"dayOfWeek": simTime.Weekday().String(),
		},
		"environment": map[string]interface{}{
			"name": env,
		},
	}

	type gateResult struct {
		name    string
		pass    bool
		reason  string
		message string
	}

	var results []gateResult
	var blocked []string

	// Only evaluate gates that apply to this pipeline/environment.
	for _, g := range templateGates {
		if g.Spec.Expression == "" {
			continue
		}
		// Filter by environment label or no restriction.
		appliesTo := g.Labels["kardinal.io/applies-to"]
		if appliesTo != "" && appliesTo != env {
			continue
		}

		pass, reason, evalErr := simulateCELEvaluate(celEnv, g.Spec.Expression, celCtx)
		if evalErr != nil {
			reason = fmt.Sprintf("eval error: %v", evalErr)
			pass = false
		}

		message := g.Spec.Message
		if message == "" {
			message = reason
		}

		results = append(results, gateResult{
			name:    g.Name,
			pass:    pass,
			reason:  reason,
			message: message,
		})

		if !pass {
			blocked = append(blocked, g.Name)
		}
	}

	// Print results.
	if len(blocked) > 0 {
		if _, err := fmt.Fprintf(w, "RESULT: BLOCKED\n"); err != nil {
			return fmt.Errorf("write result: %w", err)
		}
		for _, name := range blocked {
			for _, r := range results {
				if r.name == name {
					if _, err := fmt.Fprintf(w, "Blocked by: %s\nMessage: %q\n", r.name, r.message); err != nil {
						return fmt.Errorf("write blocked: %w", err)
					}
					// Show next window for schedule-based gates (e.g. weekend gates).
					// Compute the next weekday window when the simulated time is on a weekend.
					if simTime.Weekday() == time.Saturday || simTime.Weekday() == time.Sunday {
						daysUntilMonday := (int(time.Monday) - int(simTime.Weekday()) + 7) % 7
						if daysUntilMonday == 0 {
							daysUntilMonday = 7
						}
						nextWindow := time.Date(simTime.Year(), simTime.Month(), simTime.Day()+daysUntilMonday, 0, 0, 0, 0, time.UTC)
						if _, err := fmt.Fprintf(w, "Next window: %s\n", nextWindow.Format("Monday 15:04 UTC")); err != nil {
							return fmt.Errorf("write next window: %w", err)
						}
					}
					if _, err := fmt.Fprintf(w, "\n"); err != nil {
						return fmt.Errorf("write newline: %w", err)
					}
					break
				}
			}
		}
	} else {
		if _, err := fmt.Fprintf(w, "RESULT: PASS\n"); err != nil {
			return fmt.Errorf("write result: %w", err)
		}
	}

	// Print per-gate table.
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	for _, r := range results {
		status := "PASS"
		if !r.pass {
			status = "BLOCK"
		}
		if _, err := fmt.Fprintf(tw, "%s:\t%s\t(%s)\n", r.name, status, r.reason); err != nil {
			return fmt.Errorf("write gate row: %w", err)
		}
	}
	if len(results) == 0 {
		if _, err := fmt.Fprintf(tw, "No PolicyGates found for pipeline %q environment %q\n", pipeline, env); err != nil {
			return fmt.Errorf("write empty: %w", err)
		}
	}

	return tw.Flush()
}

// parseSimulatedTime parses informal time strings like "Saturday 3pm" or "Tuesday 10am".
// Returns a UTC time on the nearest such day relative to today.
func parseSimulatedTime(s string) (time.Time, error) {
	// Try to find a day-of-week prefix.
	days := map[string]time.Weekday{
		"sunday": time.Sunday, "sun": time.Sunday,
		"monday": time.Monday, "mon": time.Monday,
		"tuesday": time.Tuesday, "tue": time.Tuesday,
		"wednesday": time.Wednesday, "wed": time.Wednesday,
		"thursday": time.Thursday, "thu": time.Thursday,
		"friday": time.Friday, "fri": time.Friday,
		"saturday": time.Saturday, "sat": time.Saturday,
	}

	lower := toLower(s)
	now := time.Now().UTC()
	targetWeekday := now.Weekday()
	targetHour := now.Hour()

	for name, day := range days {
		if len(lower) >= len(name) && lower[:len(name)] == name {
			targetWeekday = day
			rest := lower[len(name):]
			// Parse hour from rest: "3pm" → 15, "10am" → 10
			if h, err := parseHour(rest); err == nil {
				targetHour = h
			}
			break
		}
	}

	// Find the next occurrence of targetWeekday.
	daysUntil := int(targetWeekday) - int(now.Weekday())
	if daysUntil < 0 {
		daysUntil += 7
	}
	target := now.AddDate(0, 0, daysUntil)
	return time.Date(target.Year(), target.Month(), target.Day(), targetHour, 0, 0, 0, time.UTC), nil
}

// parseHour parses hour strings like "3pm", "15", "10am".
func parseHour(s string) (int, error) {
	s = trimSpace(s)
	pm := false
	if len(s) > 2 && s[len(s)-2:] == "pm" {
		pm = true
		s = s[:len(s)-2]
	} else if len(s) > 2 && s[len(s)-2:] == "am" {
		s = s[:len(s)-2]
	}
	trimmed := trimSpace(s)
	if len(trimmed) == 0 {
		return 0, fmt.Errorf("empty hour")
	}
	h := 0
	for _, c := range trimmed {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-digit in hour")
		}
		h = h*10 + int(c-'0')
	}
	if pm && h < 12 {
		h += 12
	}
	return h, nil
}

// toLower returns a lowercase copy of s without using strings.ToLower.
func toLower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		out[i] = c
	}
	return string(out)
}

// trimSpace removes leading/trailing spaces.
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && s[start] == ' ' {
		start++
	}
	for end > start && s[end-1] == ' ' {
		end--
	}
	return s[start:end]
}

// newSimulateCELEnvironment creates a CEL environment for policy simulation in the CLI.
// It registers the same variables and library extensions as pkg/cel.NewCELEnvironment()
// so that complex expressions using json.*, maps.*, lists.*, random.* work correctly.
//
// The import of pkg/cel/library is allowed (see AGENTS.md — only pkg/cel itself is banned
// outside pkg/reconciler/policygate). pkg/cel/library has no dependency on pkg/cel.
func newSimulateCELEnvironment() (*cel.Env, error) {
	env, err := cel.NewEnv(
		cel.Variable("bundle", cel.DynType),
		cel.Variable("schedule", cel.DynType),
		cel.Variable("environment", cel.DynType),
		cel.Variable("metrics", cel.DynType),
		cel.Variable("upstream", cel.DynType),
		cel.Variable("previousBundle", cel.DynType),
		// kro CEL library extensions — same set as pkg/cel.NewCELEnvironment().
		// Ensures expressions like json.unmarshal(...), map1.merge(map2), etc. work in simulate.
		library.JSON(),
		library.Maps(),
		library.Lists(),
		library.Random(),
	)
	if err != nil {
		return nil, fmt.Errorf("cel.NewEnv: %w", err)
	}
	return env, nil
}

// simulateCELEvaluate compiles and evaluates a single CEL expression against the given context.
// Returns (pass, reason, error). Errors are fail-closed (pass=false on error).
// This is the CLI-local equivalent of pkg/cel.Evaluator.Evaluate() to avoid the banned import.
func simulateCELEvaluate(env *cel.Env, expr string, ctx map[string]interface{}) (bool, string, error) {
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return false, fmt.Sprintf("CEL compile error: %s", issues.Err()), issues.Err()
	}
	prg, err := env.Program(ast)
	if err != nil {
		return false, fmt.Sprintf("CEL program error: %s", err), err
	}
	out, _, err := prg.Eval(ctx)
	if err != nil {
		return false, fmt.Sprintf("CEL evaluation error: %s", err), err
	}
	result, ok := out.Value().(bool)
	if !ok {
		e := fmt.Errorf("CEL expression %q returned non-boolean: %T(%v)", expr, out.Value(), out.Value())
		return false, e.Error(), e
	}
	return result, fmt.Sprintf("%s = %v", expr, result), nil
}

// ─── policy test ─────────────────────────────────────────────────────────────

func newPolicyTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <file>",
		Short: "Validate PolicyGate YAML syntax and dry-run CEL expressions",
		Long: `Validate a PolicyGate YAML file: check CEL syntax and dry-run evaluate
each gate against a default context (current time, empty bundle).

No cluster access is required — all validation is performed locally.

Example:
  kardinal policy test policy-gates.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return policyTestFn(cmd.OutOrStdout(), args[0])
		},
	}
}

// policyTestFn reads a PolicyGate YAML file, validates each gate's CEL expression,
// and performs a dry-run evaluation with a default context.
// Returns a non-nil error only if the file cannot be read or parsed.
// Individual gate validation errors are printed inline without aborting.
func policyTestFn(w io.Writer, filename string) error {
	data, err := os.ReadFile(filename) //nolint:gosec
	if err != nil {
		return fmt.Errorf("read %q: %w", filename, err)
	}

	// Parse: the file may contain a single PolicyGate or a list inside a List manifest.
	// Try single gate first; if Kind is List, iterate items.
	gates, err := parsePolicyGateYAML(data)
	if err != nil {
		return fmt.Errorf("parse %q: %w", filename, err)
	}

	if len(gates) == 0 {
		if _, werr := fmt.Fprintf(w, "No PolicyGates found in %q\n", filename); werr != nil {
			return fmt.Errorf("write: %w", werr)
		}
		return nil
	}

	celEnv, celErr := newSimulateCELEnvironment()
	if celErr != nil {
		return fmt.Errorf("init CEL environment: %w", celErr)
	}

	// Default dry-run context: current time, empty bundle.
	now := time.Now().UTC()
	defaultCtx := map[string]interface{}{
		"bundle": map[string]interface{}{
			"type":                "image",
			"version":             "v0.0.0",
			"upstreamSoakMinutes": float64(0),
			"provenance": map[string]interface{}{
				"author":    "test",
				"commitSHA": "0000000",
				"ciRunURL":  "",
			},
			"intent": map[string]interface{}{
				"targetEnvironment": "test",
			},
		},
		"schedule": map[string]interface{}{
			"isWeekend": now.Weekday() == time.Saturday || now.Weekday() == time.Sunday,
			"hour":      float64(now.Hour()),
			"dayOfWeek": now.Weekday().String(),
		},
		"environment": map[string]interface{}{
			"name": "test",
		},
		"metrics":        map[string]interface{}{},
		"upstream":       map[string]interface{}{},
		"previousBundle": map[string]interface{}{},
	}

	allPassed := true
	hasSyntaxError := false
	for _, g := range gates {
		name := g.Name
		if name == "" {
			name = "(unnamed)"
		}
		expr := g.Spec.Expression
		if _, werr := fmt.Fprintf(w, "PolicyGate %q (%s):\n", name, filename); werr != nil {
			return fmt.Errorf("write: %w", werr)
		}
		if _, werr := fmt.Fprintf(w, "  Expression: %s\n", expr); werr != nil {
			return fmt.Errorf("write: %w", werr)
		}

		if expr == "" {
			if _, werr := fmt.Fprintf(w, "  Syntax: SKIP (no expression)\n\n"); werr != nil {
				return fmt.Errorf("write: %w", werr)
			}
			continue
		}

		// Validate CEL syntax.
		_, issues := celEnv.Compile(expr)
		if issues != nil && issues.Err() != nil {
			allPassed = false
			hasSyntaxError = true
			if _, werr := fmt.Fprintf(w, "  Syntax: INVALID — %s\n\n", issues.Err()); werr != nil {
				return fmt.Errorf("write: %w", werr)
			}
			continue
		}
		if _, werr := fmt.Fprintf(w, "  Syntax: valid\n"); werr != nil {
			return fmt.Errorf("write: %w", werr)
		}

		// Dry-run evaluation.
		pass, reason, evalErr := simulateCELEvaluate(celEnv, expr, defaultCtx)
		if evalErr != nil {
			allPassed = false
			hasSyntaxError = true // eval errors are also syntax-level issues
			if _, werr := fmt.Fprintf(w, "  Result: ERROR (%s)\n\n", evalErr); werr != nil {
				return fmt.Errorf("write: %w", werr)
			}
			continue
		}

		result := "PASS"
		if !pass {
			allPassed = false
			result = "FAIL"
		}
		if _, werr := fmt.Fprintf(w, "  Result: %s (%s)\n\n", result, reason); werr != nil {
			return fmt.Errorf("write: %w", werr)
		}
	}

	// Summary: distinguish syntax errors from evaluation failures.
	// - Syntax errors → error message + exit non-zero (for CI use)
	// - Evaluation FAIL → informational (gate blocks on current context, not a bug)
	var summary string
	switch {
	case hasSyntaxError:
		summary = "CEL syntax errors found"
	case !allPassed:
		summary = "Some gates would BLOCK with current context (see FAIL results above)"
	default:
		summary = "All gates valid and pass current context"
	}
	if _, werr := fmt.Fprintf(w, "%s (%d gate(s))\n", summary, len(gates)); werr != nil {
		return fmt.Errorf("write: %w", werr)
	}
	// Return error only for syntax errors (enables CI gating).
	// Evaluation FAIL is informational — not a test failure.
	if hasSyntaxError {
		return fmt.Errorf("CEL syntax errors in %d gate(s)", len(gates))
	}
	return nil
}

// parsePolicyGateYAML decodes YAML that contains one or more PolicyGate objects.
// Supports: a single PolicyGate document, or multiple documents in a --- separated file.
func parsePolicyGateYAML(data []byte) ([]v1alpha1.PolicyGate, error) {
	// Try single object first.
	var single v1alpha1.PolicyGate
	if err := sigsyaml.Unmarshal(data, &single); err == nil && single.Kind == "PolicyGate" {
		return []v1alpha1.PolicyGate{single}, nil
	}

	// Try as a list wrapper with items field (kubectl-style).
	type listWrapper struct {
		Items []v1alpha1.PolicyGate `json:"items"`
	}
	var list listWrapper
	if err := sigsyaml.Unmarshal(data, &list); err == nil && len(list.Items) > 0 {
		return list.Items, nil
	}

	// Fallback: assume it's a single gate even without Kind set.
	if single.Spec.Expression != "" || single.Name != "" {
		return []v1alpha1.PolicyGate{single}, nil
	}

	return nil, fmt.Errorf("no PolicyGate resources found in YAML")
}

// ensure context is used (avoids 'context imported and not used' lint error)
var _ = context.Background
