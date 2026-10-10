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
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/translator"
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

// defaultPolicyNamespaces matches the controller's --policy-namespaces default.
var defaultPolicyNamespaces = []string{"platform-policies"}

func addPolicyNamespacesFlag(cmd *cobra.Command, target *[]string) {
	cmd.Flags().StringSliceVar(target, "policy-namespaces", defaultPolicyNamespaces,
		"Namespaces the controller reads org PolicyGates from (its --policy-namespaces flag); "+
			"a Pipeline's spec.policyNamespaces and its own namespace are read as well")
}

// ─── policy list ────────────────────────────────────────────────────────────

func newPolicyListCmd() *cobra.Command {
	var (
		pipelineFlag string
		policyNS     []string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List PolicyGates",
		Long: `List PolicyGate templates.

Without --pipeline, lists every template in every namespace. With --pipeline,
lists the templates the controller attaches to that pipeline's environments.

The CEL column is the controller's syntax check of the expression: valid,
invalid (see kubectl describe), or - when not checked yet.

The controller evaluates the per-Bundle instances the Graph creates from a
template, never the template, so LAST-EVALUATED is the newest evaluation of
the template's instances (with --pipeline, of that pipeline's instances), or
- when none has been evaluated. An instance records its template's name and
namespace (kardinal.io/gate-template and kardinal.io/gate-template-namespace),
so a template in an org policy namespace or in spec.policyNamespaces counts the
instances in every Pipeline's namespace.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("policy list: %w", err)
			}
			return policyListFn(cmd.OutOrStdout(), c, ns, pipelineFlag, policyNS)
		},
	}
	cmd.Flags().StringVar(&pipelineFlag, "pipeline", "", "Show only the gates attached to this pipeline")
	addPolicyNamespacesFlag(cmd, &policyNS)
	return cmd
}

// policyListFn is the testable implementation of policy list. Graph-stamped
// gate instances are never listed; see `kardinal explain` for those.
func policyListFn(w io.Writer, c sigs_client.Client, ns, pipelineFilter string, policyNS []string) error {
	ctx := context.Background()
	if pipelineFilter == "" {
		var gates v1alpha1.PolicyGateList
		if err := c.List(ctx, &gates); err != nil {
			return fmt.Errorf("list policy gates: %w", err)
		}
		var templates, instances []v1alpha1.PolicyGate
		for _, g := range gates.Items {
			if isGateInstance(g) {
				instances = append(instances, g)
				continue
			}
			templates = append(templates, g)
		}
		return formatPolicyGateTable(w, templates, instances)
	}

	pipe, err := getPipeline(ctx, c, ns, pipelineFilter)
	if err != nil {
		return err
	}
	templates, err := translator.CollectGates(ctx, c, policyNS, pipe)
	if err != nil {
		return fmt.Errorf("collect policy gates: %w", err)
	}
	attached := map[string]bool{}
	for _, env := range pipelineEnvNames(pipe) { // fleet targets included: their gates are instances of the target
		instances, _, err := gatesForEnv(pipe, simulatedBundle(pipe, time.Time{}), templates, policyNS, env)
		if err != nil {
			return err
		}
		for _, g := range instances {
			attached[g.Labels["kardinal.io/gate-template"]] = true
		}
	}
	var shown []v1alpha1.PolicyGate
	for _, g := range templates {
		if attached[g.Name] {
			shown = append(shown, g)
		}
	}
	// The Graph creates a pipeline's instances in the pipeline's namespace.
	var instances v1alpha1.PolicyGateList
	if err := c.List(ctx, &instances, sigs_client.InNamespace(ns),
		sigs_client.MatchingLabels{"kardinal.io/pipeline": pipelineFilter},
		sigs_client.HasLabels{"kardinal.io/gate-template"}); err != nil {
		return fmt.Errorf("list policy gate instances: %w", err)
	}
	return formatPolicyGateTable(w, shown, instances.Items)
}

// gateScope is g's kardinal.io/scope label, "team" when it has none, as the
// Graph builder copies it from a template to its instances
// (pkg/graph buildPolicyGateNode).
func gateScope(g v1alpha1.PolicyGate) string {
	if s := g.Labels["kardinal.io/scope"]; s != "" {
		return s
	}
	return "team"
}

// instanceOf reports whether inst was created from template tmpl. The Graph
// builder labels an instance with its template's name, scope and namespace,
// and creates it in the Pipeline's namespace. An instance from an older
// controller has no namespace label: then an org template matches the org
// instances of that name in every namespace, and any other template the
// instances of that name and scope in its own namespace.
func instanceOf(inst, tmpl v1alpha1.PolicyGate) bool {
	if inst.Labels["kardinal.io/gate-template"] != tmpl.Name || gateScope(inst) != gateScope(tmpl) {
		return false
	}
	if ns := inst.Labels["kardinal.io/gate-template-namespace"]; ns != "" {
		return ns == tmpl.Namespace
	}
	return gateScope(tmpl) == "org" || inst.Namespace == tmpl.Namespace
}

// lastEvaluated is the newest status.lastEvaluatedAt of g and of its
// instances among instances, or nil when none was evaluated. The controller
// never evaluates a template, only its instances.
func lastEvaluated(g v1alpha1.PolicyGate, instances []v1alpha1.PolicyGate) *metav1.Time {
	newest := g.Status.LastEvaluatedAt
	for i := range instances {
		at := instances[i].Status.LastEvaluatedAt
		if at != nil && instanceOf(instances[i], g) && (newest == nil || at.After(newest.Time)) {
			newest = at
		}
	}
	return newest
}

// isGateInstance reports whether g was stamped by a Graph from a template.
func isGateInstance(g v1alpha1.PolicyGate) bool {
	if _, ok := g.Labels["kardinal.io/gate-template"]; ok {
		return true
	}
	_, ok := g.Labels["kardinal.io/bundle"]
	return ok
}

// formatPolicyGateTable writes the policy list table of the templates gates;
// instances are the gate instances their LAST-EVALUATED is read from.
func formatPolicyGateTable(w io.Writer, gates, instances []v1alpha1.PolicyGate) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if _, err := fmt.Fprintln(tw, "NAME\tNAMESPACE\tSCOPE\tAPPLIES-TO\tRECHECK\tCEL\tLAST-EVALUATED"); err != nil {
		return fmt.Errorf("write policy list header: %w", err)
	}

	sort.Slice(gates, func(i, j int) bool {
		if gates[i].Name != gates[j].Name {
			return gates[i].Name < gates[j].Name
		}
		return gates[i].Namespace < gates[j].Namespace
	})

	for _, g := range gates {
		scope := gateScope(g)
		appliesTo := g.Labels["kardinal.io/applies-to"]
		if appliesTo == "" {
			appliesTo = "-"
		}
		recheck := g.Spec.RecheckInterval
		if recheck == "" {
			recheck = "5m"
		}
		lastEval := "-"
		if at := lastEvaluated(g, instances); at != nil {
			lastEval = HumanAge(at.Time) + " ago"
		}

		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			g.Name, g.Namespace, scope, appliesTo, recheck, templateCELState(g), lastEval,
		); err != nil {
			return fmt.Errorf("write policy gate row: %w", err)
		}
	}

	return tw.Flush()
}

// templateCELState reads the controller's syntax check of a template from
// status.reason (pkg/reconciler/policygate reconcileTemplate).
func templateCELState(g v1alpha1.PolicyGate) string {
	switch {
	case strings.HasPrefix(g.Status.Reason, celSyntaxErrorPrefix):
		return "invalid"
	case strings.HasPrefix(g.Status.Reason, celSyntaxValidPrefix):
		return "valid"
	default:
		return "-"
	}
}

func getPipeline(ctx context.Context, c sigs_client.Reader, ns, name string) (*v1alpha1.Pipeline, error) {
	var pipe v1alpha1.Pipeline
	if err := c.Get(ctx, sigs_client.ObjectKey{Namespace: ns, Name: name}, &pipe); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("pipeline %q not found in namespace %q", name, ns)
		}
		return nil, fmt.Errorf("get pipeline %q: %w", name, err)
	}
	return &pipe, nil
}

// pipelineEnvNames lists pipe's environments, each fleet followed by its
// targets' environments.
func pipelineEnvNames(pipe *v1alpha1.Pipeline) []string {
	return graph.EnvironmentNames(pipe)
}

// simulatedBundle is the Bundle simulate evaluates gates for: an image Bundle
// of pipe with no images, provenance or intent.
func simulatedBundle(pipe *v1alpha1.Pipeline, created time.Time) *v1alpha1.Bundle {
	b := &v1alpha1.Bundle{}
	b.Name = pipe.Name + "-simulated"
	b.Namespace = pipe.Namespace
	b.CreationTimestamp = metav1.NewTime(created)
	b.Labels = map[string]string{"kardinal.io/pipeline": pipe.Name}
	b.Spec.Type = "image"
	b.Spec.Pipeline = pipe.Name
	return b
}

// ─── policy simulate ────────────────────────────────────────────────────────

func newPolicySimulateCmd() *cobra.Command {
	var opts simulateOptions

	cmd := &cobra.Command{
		Use:   "simulate",
		Short: "Simulate PolicyGate evaluation for a hypothetical promotion context",
		Long: `Simulate PolicyGate evaluation.

Selects the PolicyGates the controller attaches to the environment (the
pipeline's namespace plus the policy namespaces, matched by the
kardinal.io/applies-to label) and evaluates each one with the controller's
PolicyGate reconciler, against a Bundle that has promoted through every
upstream environment. Metrics, change windows and promotion history are read
from the cluster; nothing is written to it. Metric results are used as they
are now at every simulated time: a MetricCheck result is "Stale" only if it is
stale now.

--time is UTC: a weekday and an hour ("Saturday 3pm", "tue 10:00",
"15 Friday") or an RFC 3339 timestamp. The weekday is its next occurrence
(today counts). Without --time the current time is used.

--soak-minutes is the soak time of every upstream environment
(upstream.<env>.soakMinutes and bundle.upstreamSoakMinutes).

A blocked gate shows the next hour, within 7 days, at which it would pass with
the same inputs. Gates that do not depend on time show no window.

The first line is RESULT: PASS or RESULT: BLOCKED. A blocked result then lists
each blocking gate with its message and next window. Last comes one row per
gate: its name, PASS or BLOCK, and the reason. The command exits 0 whether the
result is PASS or BLOCKED, so a script reads the RESULT line; it exits non-zero
only when it cannot simulate (a bad flag, a pipeline or environment that does
not exist, or no cluster to read).

Example:
  kardinal policy simulate --pipeline nginx-demo --env prod --time "Saturday 3pm"
  # RESULT: BLOCKED
  # Blocked by: no-weekend-deploys`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := buildClient()
			if err != nil {
				return fmt.Errorf("policy simulate: %w", err)
			}
			opts.Now = time.Now().UTC()
			return policySimulateFn(cmd.OutOrStdout(), c, ns, opts)
		},
	}

	cmd.Flags().StringVar(&opts.Pipeline, "pipeline", "", "Pipeline name (required)")
	cmd.Flags().StringVar(&opts.Env, "env", "", "Environment name (required)")
	cmd.Flags().StringVar(&opts.Time, "time", "", `Simulated UTC time (e.g. "Saturday 3pm", "Tuesday 10:00", RFC 3339)`)
	cmd.Flags().Int64Var(&opts.SoakMinutes, "soak-minutes", 0, "Simulated soak time of each upstream environment, in minutes")
	addPolicyNamespacesFlag(cmd, &opts.PolicyNamespaces)
	_ = cmd.MarkFlagRequired("pipeline")
	_ = cmd.MarkFlagRequired("env")

	return cmd
}

// simulateOptions are the inputs of policy simulate.
type simulateOptions struct {
	Pipeline         string
	Env              string
	Time             string
	SoakMinutes      int64
	PolicyNamespaces []string
	// Now is the reference time: the default simulated time, and the day
	// --time weekdays are resolved from.
	Now time.Time
}

// nextWindowSearchHours bounds the next-window search to one week.
const nextWindowSearchHours = 7 * 24

// policySimulateFn is the testable implementation of policy simulate.
func policySimulateFn(w io.Writer, c sigs_client.Client, ns string, opts simulateOptions) error {
	ctx := context.Background()

	simTime, err := parseSimulatedTime(opts.Time, opts.Now)
	if err != nil {
		return err
	}
	if opts.SoakMinutes < 0 {
		return fmt.Errorf("--soak-minutes must not be negative")
	}

	pipe, err := getPipeline(ctx, c, ns, opts.Pipeline)
	if err != nil {
		return err
	}
	if !containsString(pipelineEnvNames(pipe), opts.Env) {
		return fmt.Errorf("environment %q not found in pipeline %q (environments: %s)",
			opts.Env, opts.Pipeline, strings.Join(pipelineEnvNames(pipe), ", "))
	}

	templates, err := translator.CollectGates(ctx, c, opts.PolicyNamespaces, pipe)
	if err != nil {
		return fmt.Errorf("collect policy gates: %w", err)
	}
	bundle := simulatedBundle(pipe, simTime)
	gates, upstreams, err := gatesForEnv(pipe, bundle, templates, opts.PolicyNamespaces, opts.Env)
	if err != nil {
		return err
	}
	healthy := metav1.NewTime(simTime.Add(-time.Duration(opts.SoakMinutes) * time.Minute))
	for _, env := range upstreams {
		bundle.Status.Environments = append(bundle.Status.Environments, v1alpha1.EnvironmentStatus{
			Name: env, Phase: "Verified", SoakMinutes: opts.SoakMinutes, HealthCheckedAt: &healthy,
		})
	}

	objs := []sigs_client.Object{bundle}
	for i := range gates {
		gates[i].Namespace = pipe.Namespace
		objs = append(objs, &gates[i])
	}
	ev, err := newGateEvaluator(newSimulationClient(c.Scheme(), c, objs...))
	if err != nil {
		return err
	}
	// An org gate reads metrics.* from its org policy namespace, as in the
	// controller.
	ev.r.PolicyNamespaces = opts.PolicyNamespaces
	// Metric results are the cluster's current ones at every simulated time:
	// a result is stale only if it is stale now.
	ev.metricsAt = opts.Now.UTC()

	type gateResult struct {
		name, reason, message string
		pass                  bool
		nextWindow            *time.Time
	}
	var results []gateResult
	for i := range gates {
		g := &gates[i]
		key := sigs_client.ObjectKeyFromObject(g)
		pass, reason, err := ev.evaluate(ctx, key, simTime)
		if err != nil {
			return err
		}
		r := gateResult{name: gateDisplayName(*g), reason: reason, message: g.Spec.Message, pass: pass}
		if r.message == "" {
			r.message = reason
		}
		if !pass {
			// Hour by hour from the next whole hour, same inputs.
			start := simTime.Truncate(time.Hour)
			for h := 1; h <= nextWindowSearchHours; h++ {
				t := start.Add(time.Duration(h) * time.Hour)
				ok, _, err := ev.evaluate(ctx, key, t)
				if err != nil {
					return err
				}
				if ok {
					r.nextWindow = &t
					break
				}
			}
		}
		results = append(results, r)
	}
	sort.SliceStable(results, func(i, j int) bool { return results[i].name < results[j].name })

	var out strings.Builder
	blocked := false
	for _, r := range results {
		if !r.pass {
			blocked = true
		}
	}
	if blocked {
		out.WriteString("RESULT: BLOCKED\n")
		for _, r := range results {
			if r.pass {
				continue
			}
			fmt.Fprintf(&out, "Blocked by: %s\nMessage: %q\n", r.name, r.message)
			if r.nextWindow != nil {
				fmt.Fprintf(&out, "Next window: %s\n", r.nextWindow.Format("Monday 15:04 UTC"))
			}
			out.WriteString("\n")
		}
	} else {
		out.WriteString("RESULT: PASS\n")
	}
	if _, err := io.WriteString(w, out.String()); err != nil {
		return fmt.Errorf("write result: %w", err)
	}

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
		if _, err := fmt.Fprintf(tw, "No PolicyGates found for pipeline %q environment %q\n", opts.Pipeline, opts.Env); err != nil {
			return fmt.Errorf("write empty: %w", err)
		}
	}
	return tw.Flush()
}

// gateDisplayName is the user-facing name of a gate instance: its template's
// name, not the Graph-generated resource name.
func gateDisplayName(g v1alpha1.PolicyGate) string {
	if n := g.Labels["kardinal.io/gate-name"]; n != "" {
		return n
	}
	if n := g.Labels["kardinal.io/gate-template"]; n != "" {
		return n
	}
	return g.Name
}

var weekdayNames = []time.Weekday{
	time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday,
}

// parseSimulatedTime parses the --time flag relative to now (UTC).
//
// Accepted: an RFC 3339 timestamp, or a weekday and an hour in any order,
// separated by spaces, with an optional trailing "UTC". The weekday is a full
// name or its three-letter form; the hour is "15", "3pm", "12am" or "15:30".
// The result is the next occurrence of the weekday (today counts) at that time.
// An empty string means now. Anything else is an error.
func parseSimulatedTime(s string, now time.Time) (time.Time, error) {
	now = now.UTC()
	s = strings.TrimSpace(s)
	if s == "" {
		return now, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}

	fields := strings.Fields(strings.ToLower(s))
	if n := len(fields); n > 0 && fields[n-1] == "utc" {
		fields = fields[:n-1]
	}
	day, hour, minute := -1, -1, 0
	for _, f := range fields {
		if d, ok := parseWeekday(f); ok && day < 0 {
			day = int(d)
			continue
		}
		if h, m, err := parseClock(f); err == nil && hour < 0 {
			hour, minute = h, m
			continue
		}
		return time.Time{}, invalidTimeError(s)
	}
	if day < 0 || hour < 0 {
		return time.Time{}, invalidTimeError(s)
	}
	daysAhead := (day - int(now.Weekday()) + 7) % 7
	d := now.AddDate(0, 0, daysAhead)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, time.UTC), nil
}

func invalidTimeError(s string) error {
	return fmt.Errorf("invalid --time %q: want a weekday and an hour in UTC "+
		"(\"Saturday 3pm\", \"tue 10:00\") or an RFC 3339 timestamp", s)
}

func parseWeekday(f string) (time.Weekday, bool) {
	for _, d := range weekdayNames {
		name := strings.ToLower(d.String())
		if f == name || f == name[:3] {
			return d, true
		}
	}
	return 0, false
}

// parseClock parses "15", "15:30", "3pm", "3:30pm" and "12am" (midnight).
func parseClock(f string) (int, int, error) {
	hourText, minuteText, hasMinutes := strings.Cut(f, ":")
	suffix := ""
	last := hourText
	if hasMinutes {
		last = minuteText
	}
	if strings.HasSuffix(last, "am") || strings.HasSuffix(last, "pm") {
		suffix = last[len(last)-2:]
		last = last[:len(last)-2]
	}
	if hasMinutes {
		minuteText = last
	} else {
		hourText = last
	}
	h, err := parseHour(hourText + suffix)
	if err != nil {
		return 0, 0, err
	}
	m := 0
	if hasMinutes {
		if len(minuteText) != 2 {
			return 0, 0, fmt.Errorf("invalid minutes %q", minuteText)
		}
		if m, err = strconv.Atoi(minuteText); err != nil || m < 0 || m > 59 {
			return 0, 0, fmt.Errorf("invalid minutes %q", minuteText)
		}
	}
	return h, m, nil
}

// parseHour parses "15" (0-23) or "3pm"/"12am" (1-12 with am/pm).
func parseHour(s string) (int, error) {
	s = strings.TrimSpace(s)
	suffix := ""
	if strings.HasSuffix(s, "am") || strings.HasSuffix(s, "pm") {
		suffix, s = s[len(s)-2:], s[:len(s)-2]
	}
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, fmt.Errorf("invalid hour %q", s+suffix)
	}
	h, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid hour %q: %w", s+suffix, err)
	}
	switch suffix {
	case "":
		if h > 23 {
			return 0, fmt.Errorf("hour %d out of range 0-23", h)
		}
	default:
		if h < 1 || h > 12 {
			return 0, fmt.Errorf("hour %d%s out of range 1-12", h, suffix)
		}
		h %= 12
		if suffix == "pm" {
			h += 12
		}
	}
	return h, nil
}

// ─── policy test ─────────────────────────────────────────────────────────────

func newPolicyTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test <file>",
		Short: "Validate PolicyGate YAML syntax and dry-run CEL expressions",
		Long: `Validate every PolicyGate in a YAML file (multiple documents are fine).

Each expression is compiled with the controller's PolicyGate CEL environment,
then evaluated by the controller's reconciler against a local context: the
current time, a Bundle with no images or provenance, no metrics, no upstream
history and no change windows. The environment is the first entry of the
gate's kardinal.io/applies-to label.

Results:
  PASS     the gate would allow promotion in that context
  FAIL     the gate would block promotion in that context
  UNKNOWN  the expression needs cluster data the local context lacks
           (metrics, upstream, bundle.pr); use 'kardinal policy simulate'

No cluster access is required. The command exits non-zero only when an
expression does not compile.

Example:
  kardinal policy test policy-gates.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return policyTestFn(cmd.OutOrStdout(), args[0], time.Now().UTC())
		},
	}
}

// policyTestFn reads a PolicyGate YAML file, compiles each gate's CEL
// expression and dry-runs it with the controller's reconciler at now.
// Returns an error when the file cannot be read or parsed, or when an
// expression does not compile.
func policyTestFn(w io.Writer, filename string, now time.Time) error {
	ctx := context.Background()
	data, err := os.ReadFile(filename) //nolint:gosec
	if err != nil {
		return fmt.Errorf("read %q: %w", filename, err)
	}

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

	var out strings.Builder
	syntaxErrors, failed, unknown := 0, 0, 0
	for _, g := range gates {
		name := g.Name
		if name == "" {
			name = "(unnamed)"
		}
		expr := g.Spec.Expression
		fmt.Fprintf(&out, "PolicyGate %q (%s):\n", name, filename)
		fmt.Fprintf(&out, "  Expression: %s\n", expr)

		if expr == "" {
			out.WriteString("  Syntax: SKIP (no expression)\n\n")
			continue
		}

		if msg, invalid, err := celSyntaxCheck(ctx, expr); err != nil {
			return err
		} else if invalid {
			syntaxErrors++
			fmt.Fprintf(&out, "  Syntax: INVALID — %s\n\n", msg)
			continue
		}
		out.WriteString("  Syntax: valid\n")

		env := "test"
		if a := strings.TrimSpace(strings.Split(g.Labels["kardinal.io/applies-to"], ",")[0]); a != "" {
			env = a
		}
		pass, reason, err := localGateCheck(ctx, g, env, now)
		if err != nil {
			return err
		}
		result := "PASS"
		switch {
		case pass:
		case strings.HasPrefix(reason, celEvalErrorPrefix):
			unknown++
			result = "UNKNOWN"
		default:
			failed++
			result = "FAIL"
		}
		fmt.Fprintf(&out, "  Result: %s (%s)\n\n", result, reason)
	}

	var summary string
	switch {
	case syntaxErrors > 0:
		summary = "CEL syntax errors found"
	case failed > 0:
		summary = "Some gates would BLOCK with current context (see FAIL results above)"
	case unknown > 0:
		summary = "All gates valid; some need cluster context to evaluate (see UNKNOWN results above)"
	default:
		summary = "All gates valid and pass current context"
	}
	fmt.Fprintf(&out, "%s (%d gate(s))\n", summary, len(gates))
	if _, err := io.WriteString(w, out.String()); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if syntaxErrors > 0 {
		return fmt.Errorf("CEL syntax errors in %d of %d gate(s)", syntaxErrors, len(gates))
	}
	return nil
}

// parsePolicyGateYAML decodes every PolicyGate in data: one or more YAML (or
// JSON) documents, each a PolicyGate or a List of PolicyGates. Documents of
// other kinds are ignored.
func parsePolicyGateYAML(data []byte) ([]v1alpha1.PolicyGate, error) {
	type document struct {
		v1alpha1.PolicyGate `json:",inline"`
		Items               []v1alpha1.PolicyGate `json:"items"`
	}
	dec := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096)
	var gates []v1alpha1.PolicyGate
	for {
		var doc document
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode YAML: %w", err)
		}
		switch {
		case len(doc.Items) > 0:
			gates = append(gates, doc.Items...)
		case doc.Kind == "PolicyGate",
			doc.Kind == "" && (doc.Name != "" || doc.Spec.Expression != ""):
			gates = append(gates, doc.PolicyGate)
		}
	}
	if len(gates) == 0 {
		return nil, fmt.Errorf("no PolicyGate resources found in YAML")
	}
	return gates, nil
}
