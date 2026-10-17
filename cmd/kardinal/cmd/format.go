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

// Package cmd implements the cobra subcommand tree for the kardinal CLI.
package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigsyaml "sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	pipelinerec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/pipeline"

	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
)

// HumanAge returns a human-readable age string for the given creation time.
func HumanAge(t time.Time) string {
	return humanDuration(time.Since(t))
}

// humanDuration renders d in its largest whole unit: 45s, 4m, 3h, 2d.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// orDash returns s, or "-" when s is empty.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// truncateRunes shortens s to n runes, the last three being "...", without
// splitting a UTF-8 character.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}

// stepStatePriority returns a sort priority for a PromotionStep state.
// Higher priority = displayed first. Used by the pipeline, steps and explain views.
// Active states (Promoting/WaitingForMerge/HealthChecking/Verifying) take precedence,
// then Pending (step queued but not started), then Verified, AbortedByAlarm
// and RollingBack, then Failed. AbortedByAlarm and RollingBack rank with
// Verified because the alarm fires after merge: the environment runs that
// bundle, so the newer of the two is what is deployed. A RollingBack step
// stays RollingBack for good (the rollback Bundle's own step carries the
// promotion on), so ranking it as active would hide that newer step once it
// is Verified. Failed ranks lowest because most failures happen before merge.
// This ensures in-flight promotions and rollbacks are shown over older
// terminal-state bundles (#260).
func stepStatePriority(state string) int {
	switch state {
	case "Promoting", "WaitingForMerge", "HealthChecking", "Verifying":
		return 4
	case "Pending":
		return 3
	case "Verified", "AbortedByAlarm", "RollingBack":
		return 2
	case "Failed", "Superseded":
		// Superseded: the step did not push; a newer Bundle's step runs.
		return 1
	default:
		return 0
	}
}

// maxEnvColumns is the most environment columns the pipeline table shows:
// with more (one wide Pipeline, or the union of several), each row shows a
// summary instead (formatPipelineSummary), unless allColumns asks for every
// environment (kardinal get pipelines <name>).
const maxEnvColumns = 8

// formatPipelines writes a table of pipelines to w. VERSION is each
// pipeline's current Bundle (lifecycle.CurrentBundle, the UI's
// activeBundleName) and every environment column describes that Bundle
// (pipelineEnvStates). When subs is non-nil a SUB column counts the Watching
// Subscriptions per pipeline; showNamespace prepends a NAMESPACE column.
// allColumns: show one
// column per environment however many there are.
func formatPipelines(w io.Writer, pipelines []v1alpha1.Pipeline, bundles []v1alpha1.Bundle,
	steps []v1alpha1.PromotionStep, gates []v1alpha1.PolicyGate, subs []v1alpha1.Subscription, showNamespace, allColumns bool) error {
	var subCount map[string]int
	if subs != nil {
		// namespace/pipeline → count of active Subscriptions (Phase=="Watching").
		subCount = make(map[string]int)
		for _, s := range subs {
			if s.Spec.Pipeline == "" {
				continue
			}
			if s.Status.Phase == "Watching" {
				subCount[s.Namespace+"/"+s.Spec.Pipeline]++
			}
		}
	}
	rows := pipelineEnvStates(pipelines, bundles, steps, gates)
	orders := pipelineOrders(pipelines)
	envOrder := envColumnOrder(pipelines, orders)
	if !allColumns && len(envOrder) > maxEnvColumns {
		return formatPipelineSummary(w, pipelines, orders, rows, subCount, showNamespace)
	}
	return formatPipelineTableInternal(w, pipelines, envOrder, rows, subCount, showNamespace)
}

// pipelineOrder is one Pipeline's ordering, resolved once per table:
// environments in DAG order and the edges between them (graph.Ordering).
// When the ordering is invalid, resolved is false, order is the list order
// and deps is nil.
type pipelineOrder struct {
	order    []string
	deps     map[string][]string
	resolved bool
}

// pipelineOrders resolves the ordering of every pipeline, once each.
func pipelineOrders(pipelines []v1alpha1.Pipeline) []pipelineOrder {
	out := make([]pipelineOrder, len(pipelines))
	for i := range pipelines {
		p := &pipelines[i]
		order, deps, err := graph.Ordering(p)
		if err == nil && len(order) == len(p.Spec.Environments) {
			out[i] = pipelineOrder{order: order, deps: deps, resolved: true}
			continue
		}
		for _, e := range p.Spec.Environments {
			out[i].order = append(out[i].order, e.Name)
		}
	}
	return out
}

// envColumnOrder is the environment columns of pipelines: each Pipeline's
// environments in DAG order (graph.EnvironmentOrder), merged so that an
// environment comes after every environment it depends on in some Pipeline
// (graph.EnvironmentDependencies: dependsOn, waves, or the previous entry),
// e.g. test, uat, prod for a Pipeline test → uat → prod next to one test →
// prod. A Pipeline whose ordering is invalid contributes its list order.
// Ties, and orders that contradict each other, keep the first appearance.
func envColumnOrder(pipelines []v1alpha1.Pipeline, orders []pipelineOrder) []string {
	var first []string
	seen := map[string]bool{}
	after := map[string]map[string]bool{} // env → the envs that must come before it
	before := func(env, dep string) {
		if after[env] == nil {
			after[env] = map[string]bool{}
		}
		after[env][dep] = true
	}
	for i := range pipelines {
		p := &pipelines[i]
		order, deps := orders[i].order, orders[i].deps
		if !orders[i].resolved {
			for j, e := range p.Spec.Environments {
				if j > 0 {
					before(e.Name, p.Spec.Environments[j-1].Name)
				}
			}
		}
		for _, env := range order {
			if !seen[env] {
				seen[env] = true
				first = append(first, env)
			}
			for _, dep := range deps[env] {
				before(env, dep)
			}
		}
	}
	// Kahn's algorithm over first-appearance order; whatever a cycle leaves
	// keeps its first-appearance place at the end.
	placed := map[string]bool{}
	out := make([]string, 0, len(first))
	for progress := true; progress && len(out) < len(first); {
		progress = false
		for _, env := range first {
			if placed[env] {
				continue
			}
			ready := true
			for dep := range after[env] {
				if !placed[dep] {
					ready = false
					break
				}
			}
			if ready {
				placed[env] = true
				out = append(out, env)
				progress = true
			}
		}
	}
	for _, env := range first {
		if !placed[env] {
			out = append(out, env)
		}
	}
	return out
}

// formatPipelineSummary renders the pipeline table with one summary column
// instead of a column per environment: ENVS is the environment count and
// PROGRESS the current Bundle's states, most advanced first ("42 Verified,
// 108 HealthChecking"). A footnote says how to see every environment.
func formatPipelineSummary(w io.Writer, pipelines []v1alpha1.Pipeline, orders []pipelineOrder, rows map[string]pipelineRow,
	subCount map[string]int, showNamespace bool) error {
	tw := style.NewTable(w, 3)
	header := ""
	if showNamespace {
		header = "NAMESPACE\t"
	}
	header += "PIPELINE\tVERSION\tENVS\tPROGRESS\tFURTHEST"
	if subCount != nil {
		header += "\tSUB"
	}
	header += "\tAGE"
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return fmt.Errorf("write pipeline table header: %w", err)
	}
	for i, p := range pipelines {
		key := p.Namespace + "/" + p.Name
		pr := rows[key]
		name := p.Name
		if p.Spec.Paused {
			name += " [PAUSED]"
		}
		row := name + "\t" + orDash(pr.bundle)
		if showNamespace {
			row = p.Namespace + "\t" + row
		}
		row += fmt.Sprintf("\t%d\t%s\t%s", len(p.Spec.Environments), progressSummary(pr.envs), furthestVerified(orders[i].order, pr.envs))
		if subCount != nil {
			row += fmt.Sprintf("\t%d", subCount[key])
		}
		row += "\t" + HumanAge(p.CreationTimestamp.Time)
		if _, err := fmt.Fprintln(tw, row); err != nil {
			return fmt.Errorf("write pipeline row: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush pipeline table: %w", err)
	}
	_, err := fmt.Fprintf(w, "\nMore than %d environments: one row per Pipeline. "+
		"Every environment of one: kardinal get pipelines <name>\n", maxEnvColumns)
	if err != nil {
		return fmt.Errorf("write pipeline table note: %w", err)
	}
	return nil
}

// furthestVerified is the last environment of order (a Pipeline's DAG
// order, pipelineOrder) where the row's Bundle is Verified; "-" when it is
// Verified nowhere. In a wave that is the wave's last environment in spec
// order that is Verified.
func furthestVerified(order []string, envs map[string]string) string {
	for i := len(order) - 1; i >= 0; i-- {
		if envs[order[i]] == "Verified" {
			return order[i]
		}
	}
	return "-"
}

// progressSummary counts the environment states of a row, Verified first,
// then by stepStatePriority (the most advanced first), "-" without any.
func progressSummary(envs map[string]string) string {
	count := map[string]int{}
	for _, st := range envs {
		count[st]++
	}
	if len(count) == 0 {
		return "-"
	}
	states := make([]string, 0, len(count))
	for st := range count {
		states = append(states, st)
	}
	sort.Slice(states, func(i, j int) bool {
		if (states[i] == "Verified") != (states[j] == "Verified") {
			return states[i] == "Verified"
		}
		if pi, pj := stepStatePriority(states[i]), stepStatePriority(states[j]); pi != pj {
			return pi > pj
		}
		return states[i] < states[j]
	})
	parts := make([]string, len(states))
	for i, st := range states {
		parts[i] = fmt.Sprintf("%d %s", count[st], envCellLabel(st))
	}
	return strings.Join(parts, ", ")
}

// pipelineRow is the table content of one pipeline: its current Bundle and
// that Bundle's state in each environment.
type pipelineRow struct {
	bundle string
	envs   map[string]string
	// hints are the roll-back hints of a current Bundle that is Rejected
	// with its change live (lifecycle.RejectedLiveStep).
	hints []string
}

// pipelineEnvStates returns, per namespace/pipeline, the VERSION and
// per-environment states of the pipeline table, all about one Bundle: the
// pipeline's current Bundle, lifecycle.CurrentBundle (E2E-R17). An
// environment whose newest Bundle is that one (the Pipeline's
// status.environments) shows its state there, held by a rollback hold or by
// gates when it is. Otherwise it shows "Waiting" while the Bundle is in
// flight and will still come (upstream not live yet), and nothing ("-") when
// the Bundle is terminal or does not promote the environment (skipped, or
// past its target). Another Bundle's state is never shown, so a row cannot
// pair a Bundle with a state it does not have.
func pipelineEnvStates(pipelines []v1alpha1.Pipeline, bundles []v1alpha1.Bundle,
	steps []v1alpha1.PromotionStep, gates []v1alpha1.PolicyGate) map[string]pipelineRow {
	// Gate instances by namespace/pipeline, for the projection of a
	// Pipeline whose controller predates status.environments.
	gatesByPipeline := make(map[string][]v1alpha1.PolicyGate)
	for i := range gates {
		if p := gates[i].Labels["kardinal.io/pipeline"]; p != "" {
			key := gates[i].Namespace + "/" + p
			gatesByPipeline[key] = append(gatesByPipeline[key], gates[i])
		}
	}
	bundlesByPipeline := make(map[string][]v1alpha1.Bundle)
	for i := range bundles {
		if bundles[i].Spec.Pipeline != "" {
			key := bundles[i].Namespace + "/" + bundles[i].Spec.Pipeline
			bundlesByPipeline[key] = append(bundlesByPipeline[key], bundles[i])
		}
	}
	// Steps per namespace/pipeline, read once: per Pipeline over every step
	// was O(pipelines x steps) with -A.
	stepsByPipeline := make(map[string][]v1alpha1.PromotionStep)
	for i := range steps {
		key := steps[i].Namespace + "/" + steps[i].Spec.PipelineName
		stepsByPipeline[key] = append(stepsByPipeline[key], steps[i])
	}

	out := make(map[string]pipelineRow, len(pipelines))
	for i := range pipelines {
		p := &pipelines[i]
		key := p.Namespace + "/" + p.Name
		own := stepsByPipeline[key]
		b := lifecycle.CurrentBundle(bundlesByPipeline[key], own)
		if b == nil {
			continue
		}
		version := createdOf(b).Version
		row := pipelineRow{bundle: versionLabel(version, b.Name), envs: make(map[string]string, len(p.Spec.Environments))}
		if lifecycle.Rejected(b) {
			// Current only because its change is live somewhere
			// (lifecycle.CurrentBundle): say so.
			row.bundle = versionLabel(version, b.Name) + " Rejected"
			for _, env := range lifecycle.RejectedLiveEnvs(b, own) {
				row.hints = append(row.hints, fmt.Sprintf("WARNING: pipeline %s: bundle %s is Rejected in %s: %s (kardinal rollback %s --env %s)",
					p.Name, b.Name, env, lifecycle.RejectedLiveHint, p.Name, env))
			}
		}
		if lifecycle.InFlightPhase(b.Status.Phase) {
			// The environments the Bundle promotes (its intent applied to the
			// ordering), resolved once per Pipeline: per environment it was
			// quadratic (44 s for 500 environments).
			if promoted, err := graph.PromotedEnvironments(p, b); err == nil {
				for _, env := range promoted {
					row.envs[env] = "Waiting"
				}
			}
		}
		// The current Bundle's state in each environment it has reached,
		// from status.environments (computed here with the controller's
		// code when the controller predates it). An environment whose newest
		// Bundle is another one keeps "Waiting" or nothing: a row never pairs
		// its Bundle with another Bundle's state.
		envs := p.Status.Environments
		if len(envs) == 0 {
			envs = pipelinerec.ProjectEnvironments(p, bundlesByPipeline[key], own, gatesByPipeline[key], nil, time.Time{})
		}
		for _, e := range envs {
			newest := e.Bundle
			if e.IncomingBundle != "" {
				newest = e.IncomingBundle
			}
			switch {
			case newest == b.Name && e.State != "":
				row.envs[e.Name] = heldCell(e.State, e.Held, e.BlockedBy, e.FrozenBy)
			case row.envs[e.Name] == "Waiting" && lifecycle.HeldFrom(p, e.Name, b.Name) != nil:
				row.envs[e.Name] = cellHeld
			}
		}
		// A fleet environment's column counts its targets.
		fleets, _ := graph.FleetTargetEnvironments(p)
		for fleet, targets := range fleets {
			s := fleetCell(row.envs, targets)
			if lifecycle.FleetPaused(p, fleet) {
				// kardinal pause --env <fleet>: no further target starts.
				s = strings.TrimPrefix(s+", paused", ", ")
				if s == "paused" {
					s = "Paused"
				}
			}
			if s != "" {
				row.envs[fleet] = s
			}
		}
		out[key] = row
	}
	return out
}

// fleetCell is the get pipelines cell of a fleet environment from its
// targets' states: "Verified" once every target is, else how many are
// Verified, with the Failed ones and the Superseded ones by name ("12/50
// Verified, 1 Failed, 1 Superseded (prod-eu)"; #1603: a newer Bundle, the
// rollback of that one target, deployed it). "" when no target has a state.
func fleetCell(envs map[string]string, targets []string) string {
	verified, failed, seen := 0, 0, 0
	var superseded []string
	for _, t := range targets {
		st, ok := envs[t]
		if !ok || st == "Waiting" || st == cellHeld || strings.HasPrefix(st, cellHeldByGate) {
			continue
		}
		seen++
		switch st {
		case "Verified":
			verified++
		case "Failed", "AbortedByAlarm", "RollingBack":
			failed++
		case "Superseded":
			superseded = append(superseded, t)
		}
	}
	if seen == 0 {
		return ""
	}
	if verified == len(targets) {
		return "Verified"
	}
	cell := fmt.Sprintf("%d/%d live", verified, len(targets))
	if failed > 0 {
		cell += fmt.Sprintf(", %d failed", failed)
	}
	if n := len(superseded); n > 0 {
		names := superseded
		if n > 3 {
			names = append(slices.Clone(superseded[:3]), "...")
		}
		cell += fmt.Sprintf(", %d replaced by newer version (%s)", n, strings.Join(names, ", "))
	}
	return cell
}

// formatPipelineTableInternal renders the pipeline table with one column per
// environment of envOrder (envColumnOrder: DAG order, merged across
// pipelines so that their columns align). rows maps
// namespace/pipeline → its current Bundle and environment states; a pipeline
// or environment without an entry shows "-". subCount maps
// namespace/pipeline → active subscription count; nil means no SUB column.
func formatPipelineTableInternal(w io.Writer, pipelines []v1alpha1.Pipeline, envOrder []string, rows map[string]pipelineRow,
	subCount map[string]int, showNamespace bool) error {
	if len(pipelines) == 0 {
		_, _ = fmt.Fprintln(w, "No pipelines found.")
		_, _ = fmt.Fprintln(w, "  Write one with: kardinal init")
		_, _ = fmt.Fprintln(w, "  Or follow the quickstart: https://pnz1990.github.io/kardinal-promoter/quickstart/")
		_, _ = fmt.Fprintln(w, "  Check the installation with: kardinal doctor")
		return nil
	}

	tw := style.NewTable(w, 3)

	// Build header: [NAMESPACE] PIPELINE VERSION <ENV1> <ENV2> ... [SUB] AGE
	header := ""
	if showNamespace {
		header = "NAMESPACE\t"
	}
	header += "PIPELINE\tVERSION"
	for _, env := range envOrder {
		header += "\t" + strings.ToUpper(env)
	}
	if subCount != nil {
		header += "\tSUB"
	}
	header += "\tAGE"
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return fmt.Errorf("write pipeline table header: %w", err)
	}

	for _, p := range pipelines {
		pipelineKey := p.Namespace + "/" + p.Name
		pr := rows[pipelineKey]
		bundleDisplay := orDash(pr.bundle)

		// Build the row. Append [PAUSED] to the pipeline name when the pipeline
		// has spec.paused=true so operators immediately see the frozen state.
		pipelineDisplay := p.Name
		if p.Spec.Paused {
			pipelineDisplay = p.Name + " [PAUSED]"
		}
		row := fmt.Sprintf("%s\t%s", pipelineDisplay, bundleDisplay)
		if showNamespace {
			row = fmt.Sprintf("%s\t%s\t%s", p.Namespace, pipelineDisplay, bundleDisplay)
		}
		for _, env := range envOrder {
			row += "\t" + sty.State(envCellState(pr.envs[env]))
		}
		if subCount != nil {
			row += "\t" + fmt.Sprintf("%d", subCount[pipelineKey])
		}
		row += "\t" + HumanAge(p.CreationTimestamp.Time)

		if _, err := fmt.Fprintln(tw, row); err != nil {
			return fmt.Errorf("write pipeline row: %w", err)
		}
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush pipeline table: %w", err)
	}
	for _, p := range pipelines {
		for _, h := range rows[p.Namespace+"/"+p.Name].hints {
			if _, err := fmt.Fprintln(w, notice(h)); err != nil {
				return fmt.Errorf("write pipeline hint: %w", err)
			}
		}
	}
	return nil
}

// FormatBundleErrors writes a plain-text error notice for each pipeline whose
// newest Bundle that is not Superseded or Rejected is Failed. A newer Bundle, promoting or
// Verified, means the failure is history, so it is not reported (E2E-R16).
// The notice is printed after the pipeline table so the root cause of a
// silent "Phase: Error" is visible without `kubectl describe graph`.
//
// Output format (one line per pipeline, sorted by pipeline):
//
//	ERROR: pipeline [<namespace>/]<pipeline>: <condition-message>
//
// The message comes from a True cause condition: InvalidSpec before Failed,
// and within a type CircularDependency, then TranslationError, then the first
// one. Other True conditions (GraphSynced) are not the cause and are skipped.
// With no cause condition the message is a describe hint.
// showNamespace prefixes the pipeline with its namespace (--all-namespaces).
// When no Failed bundles are present, nothing is written.
func FormatBundleErrors(w io.Writer, bundles []v1alpha1.Bundle, showNamespace bool) error {
	sorted := make([]v1alpha1.Bundle, 0, len(bundles))
	for _, b := range bundles {
		if !lifecycle.Halted(&b) {
			sorted = append(sorted, b)
		}
	}
	// Newest first (lifecycle.CompareCreation, the order supersession uses),
	// so the first bundle seen per pipeline is the current one.
	sort.SliceStable(sorted, func(i, j int) bool {
		return lifecycle.CompareCreation(&sorted[i], &sorted[j]) > 0
	})

	typeRank := map[string]int{"InvalidSpec": 20, "Failed": 10}
	reasonRank := map[string]int{"CircularDependency": 2, "TranslationError": 1}
	type pipelineError struct{ pipeline, message string }
	seen := make(map[string]bool)
	var errs []pipelineError
	for _, b := range sorted {
		pipeline := b.Spec.Pipeline
		if pipeline == "" {
			pipeline = b.Name // fallback: use bundle name if pipeline ref missing
		}
		if showNamespace && b.Namespace != "" {
			pipeline = b.Namespace + "/" + pipeline
		}
		key := b.Namespace + "/" + pipeline
		if seen[key] {
			continue
		}
		seen[key] = true
		if b.Status.Phase != "Failed" {
			continue
		}

		msg, rank := "", -1
		for _, cond := range b.Status.Conditions {
			t, cause := typeRank[cond.Type]
			if cond.Status != metav1.ConditionTrue || !cause {
				continue
			}
			if r := t + reasonRank[cond.Reason]; r > rank {
				msg, rank = cond.Message, r
			}
		}
		if msg == "" {
			msg = "promotion failed; run `kubectl describe bundle " + b.Name + "` for details"
		}
		errs = append(errs, pipelineError{pipeline: pipeline, message: msg})
	}
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].pipeline < errs[j].pipeline })

	for _, e := range errs {
		if _, err := fmt.Fprintf(w, "%s pipeline %s: %s\n", sty.Error("ERROR:"), e.pipeline, e.message); err != nil {
			return fmt.Errorf("write bundle error: %w", err)
		}
	}
	return nil
}

// formatBundleTable writes a table of bundles to w; showNS prepends a NAMESPACE column
// (--all-namespaces).
func formatBundleTable(w io.Writer, bundles []v1alpha1.Bundle, showNS bool) error {
	tw := style.NewTable(w, 3)
	if _, err := fmt.Fprintln(tw, nsCol(showNS, "NAMESPACE")+"VERSION\tTYPE\tPHASE\tAGE"); err != nil {
		return fmt.Errorf("write bundle table header: %w", err)
	}
	for _, b := range bundles {
		if _, err := fmt.Fprintf(tw, nsCol(showNS, b.Namespace)+"%s\t%s\t%s\t%s\n",
			versionLabel(createdOf(&b).Version, b.Name),
			b.Spec.Type,
			styledBundle(b.Status.Phase),
			HumanAge(b.CreationTimestamp.Time),
		); err != nil {
			return fmt.Errorf("write bundle row: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush bundle table: %w", err)
	}
	return nil
}

// formatStepsTable writes a table of promotion steps to w, the most active
// step per environment (see FormatStepsTable in export_test.go); showNS keeps each namespace's steps
// apart and prepends a NAMESPACE column (--all-namespaces).
func formatStepsTable(w io.Writer, steps []v1alpha1.PromotionStep, showNS bool) error {
	// Filter to the best step per environment (same priority as the pipeline table).
	type stepKey struct{ ns, env string }
	type bestEntry struct {
		step     v1alpha1.PromotionStep
		priority int
	}
	best := make(map[stepKey]bestEntry)
	for _, s := range steps {
		key := stepKey{env: s.Spec.Environment}
		if showNS {
			key.ns = s.Namespace
		}
		state := s.Status.State
		if state == "" {
			state = "Pending"
		}
		priority := stepStatePriority(state)
		existing, ok := best[key]
		if !ok ||
			priority > existing.priority ||
			(priority == existing.priority && s.CreationTimestamp.After(existing.step.CreationTimestamp.Time)) {
			best[key] = bestEntry{step: s, priority: priority}
		}
	}

	// Sort by namespace, then environment name, for stable output.
	envOrder := make([]stepKey, 0, len(best))
	for k := range best {
		envOrder = append(envOrder, k)
	}
	sort.Slice(envOrder, func(i, j int) bool {
		if envOrder[i].ns != envOrder[j].ns {
			return envOrder[i].ns < envOrder[j].ns
		}
		return envOrder[i].env < envOrder[j].env
	})

	// Determine if any PromotionStep has per-step detail populated.
	hasSubSteps := false
	for _, e := range best {
		if len(e.step.Status.Steps) > 0 {
			hasSubSteps = true
			break
		}
	}

	tw := style.NewTable(w, 3)
	if hasSubSteps {
		if _, err := fmt.Fprintln(tw, nsCol(showNS, "NAMESPACE")+"ENVIRONMENT\tSTEP\tSTATE\tDURATION\tMESSAGE"); err != nil {
			return fmt.Errorf("write steps table header: %w", err)
		}
		for _, key := range envOrder {
			s, env := best[key].step, key.env
			if len(s.Status.Steps) == 0 {
				// Fallback: no per-step detail, show environment-level row.
				state := s.Status.State
				if state == "" {
					state = "Pending"
				}
				msg := s.Status.Message
				if msg == "" {
					msg = "-"
				}
				if _, err := fmt.Fprintf(tw, nsCol(showNS, s.Namespace)+"%s\t(no step detail)\t%s\t-\t%s\n", env, styledStep(state), msg); err != nil {
					return fmt.Errorf("write step row: %w", err)
				}
				continue
			}
			for i, ss := range s.Status.Steps {
				dur := "-"
				if ss.DurationMs > 0 {
					dur = fmt.Sprintf("%dms", ss.DurationMs)
				}
				msg := ss.Message
				if msg == "" {
					msg = "-"
				}
				envCol := env
				if i > 0 {
					envCol = "" // only print env once per step group
				}
				nsCell := s.Namespace
				if i > 0 {
					nsCell = ""
				}
				if _, err := fmt.Fprintf(tw, nsCol(showNS, nsCell)+"%s\t%s\t%s\t%s\t%s\n",
					envCol, ss.Name, styledSubStep(string(ss.State)), dur, msg,
				); err != nil {
					return fmt.Errorf("write sub-step row: %w", err)
				}
			}
		}
	} else {
		if _, err := fmt.Fprintln(tw, nsCol(showNS, "NAMESPACE")+"ENVIRONMENT\tSTEP-TYPE\tSTATE\tMESSAGE"); err != nil {
			return fmt.Errorf("write steps table header: %w", err)
		}
		for _, key := range envOrder {
			s := best[key].step
			state := s.Status.State
			if state == "" {
				state = "Pending"
			}
			msg := s.Status.Message
			if msg == "" {
				msg = "-"
			}
			if _, err := fmt.Fprintf(tw, nsCol(showNS, s.Namespace)+"%s\t%s\t%s\t%s\n",
				s.Spec.Environment,
				s.Spec.StepType,
				styledStep(state),
				msg,
			); err != nil {
				return fmt.Errorf("write step row: %w", err)
			}
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush steps table: %w", err)
	}
	return nil
}

// nsCol is cell followed by a tab when showNS, else "": the NAMESPACE column
// of an --all-namespaces table.
func nsCol(showNS bool, cell string) string {
	if !showNS {
		return ""
	}
	return cell + "\t"
}

// ─── Output format helpers ────────────────────────────────────────────────────

// OutputFormat returns the global output format flag value, normalised to lower-case.
// Valid values: "" (table), "json", "yaml".
func OutputFormat() string {
	return strings.ToLower(globalOutput)
}

// WriteJSON serialises v as indented JSON to w.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

// WriteYAML serialises v as YAML to w.
func WriteYAML(w io.Writer, v any) error {
	data, err := sigsyaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write yaml: %w", err)
	}
	return nil
}
