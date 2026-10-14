// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// Round budgets: a promotion through four environments with PRs takes about
// 25 rounds; the budgets leave room for retries.
const (
	maxRounds   = 160
	blockRounds = 6
)

// RunCombo runs one scenario of the matrix: it sets c up in a World of its
// own, drives c.Outcome and checks what users see (Bundle phases, the
// version each environment runs, PRs, gates), then the invariants every
// scenario keeps (CheckInvariants).
func RunCombo(t *testing.T, c Combo) {
	t.Helper()
	ns := "team-a"
	block := c.Outcome == "gateblock" || c.Outcome == "superseded" || c.Outcome == "rejected"
	w := NewWorld(t, Options{SCM: c.SCM, PolicyNamespaces: []string{PolicyNS}})
	p := BuildPlan(c, ns, w.Now(), block)
	t.Cleanup(func() {
		if t.Failed() {
			desc := w.Describe(ns)
			t.Logf("%s after %d rounds:\n%s", c.Name(), w.Rounds(), desc)
			if dir := os.Getenv("KARDINAL_SCENARIO_DUMP"); dir != "" {
				_ = os.WriteFile(filepath.Join(dir, strings.ReplaceAll(c.Name(), "/", "_")+".txt"), []byte(desc), 0o600)
			}
		}
	})
	require.NoError(t, p.Setup(w))
	w.Metrics.Set(MetricQuery, metricPassing)
	if block && c.Gate == "metric" {
		w.Metrics.Set(MetricQuery, metricFailing)
	}
	if c.Delivery == "pr-review" {
		w.Actors = append(w.Actors, Reviewer)
	}
	if c.Gate == "approval" && !block {
		w.Actors = append(w.Actors, func(w *World) error { return w.ApproveAll() })
	}
	reasons, err := w.Apply(p.Pipeline)
	require.NoError(t, err)
	if rej := Rejected(rowOf(c)); rej != nil {
		require.NotEmpty(t, reasons, "%s: the API server must refuse the Pipeline", c.Name())
		assert.Contains(t, strings.Join(reasons, "; "), rej.Message)
		return
	}
	require.Empty(t, reasons, "admission refused the Pipeline")
	for i := 0; i < 2; i++ {
		w.Round()
	}
	fail := func(format string, args ...interface{}) {
		t.Helper()
		t.Fatalf("%s after %d rounds: %s\n%s", c.Name(), w.Rounds(), fmt.Sprintf(format, args...), w.Describe(ns))
	}
	v1 := ImageBundle(ns, PipelineName, "1.1.0")
	switch c.Outcome {
	case "success":
		w.MustApply(v1)
		if !w.RunUntil(maxRounds, func() bool { return w.BundlePhase(ns)[v1.Name] == "Verified" }) {
			fail("bundle %s not Verified", v1.Name)
		}
		p.expectRunning(t, w, v1.Spec.Images[0].Tag, p.Envs...)
		p.expectGatesReady(t, w, v1.Name)
		p.expectPRs(t, w, v1.Name)
	case "healthfail":
		for _, e := range p.GateEnvs {
			w.GitOps.Fail(ns, e, "1.1.0")
		}
		w.MustApply(v1)
		if !w.RunUntil(maxRounds, func() bool { return w.BundlePhase(ns)[v1.Name] == "Failed" }) {
			fail("bundle %s not Failed", v1.Name)
		}
		failed := 0
		for env, s := range w.Steps(ns, v1.Name) {
			switch {
			case s.Status.State == "Failed":
				failed++
				assert.Contains(t, p.GateEnvs, env, "only a gated environment fails its health check")
			case contains(p.GateEnvs, env):
				assert.NotEqual(t, "Verified", s.Status.State, "%s runs the failing version", env)
			}
		}
		assert.Positive(t, failed, "a step failed its health check")
		p.expectRunning(t, w, "1.1.0", p.upstreamOf(p.GateEnvs)...)
	case "gateblock":
		w.MustApply(v1)
		p.runBlocked(t, w, v1.Name, fail)
		require.NoError(t, p.Open(w, v1.Name))
		if !w.RunUntil(maxRounds, func() bool { return w.BundlePhase(ns)[v1.Name] == "Verified" }) {
			fail("bundle %s not Verified once the gate opened", v1.Name)
		}
		p.expectRunning(t, w, "1.1.0", p.Envs...)
	case "superseded":
		w.MustApply(v1)
		p.runBlocked(t, w, v1.Name, fail)
		v2 := ImageBundle(ns, PipelineName, "1.2.0")
		w.MustApply(v2)
		if !w.RunUntil(maxRounds, func() bool { return w.BundlePhase(ns)[v1.Name] == "Superseded" }) {
			fail("bundle %s not Superseded by %s", v1.Name, v2.Name)
		}
		p.runBlocked(t, w, v2.Name, fail)
		require.NoError(t, p.Open(w, v2.Name))
		if !w.RunUntil(maxRounds, func() bool { return w.BundlePhase(ns)[v2.Name] == "Verified" }) {
			fail("bundle %s not Verified", v2.Name)
		}
		p.expectRunning(t, w, "1.2.0", p.Envs...)
		for _, e := range p.GateEnvs {
			s, ok := w.Steps(ns, v1.Name)[e]
			assert.False(t, ok && s.Status.State == "Verified", "superseded %s promoted %s", v1.Name, e)
		}
	case "rejected":
		w.MustApply(v1)
		p.runBlocked(t, w, v1.Name, fail)
		_, err := lifecycle.Reject(w.Ctx, w.Client, ns, v1.Name, "scenario: bad build", "oncall@example.com", w.Now())
		require.NoError(t, err)
		if !w.RunUntil(maxRounds, func() bool { return w.BundlePhase(ns)[v1.Name] == "Rejected" }) {
			fail("bundle %s not Rejected", v1.Name)
		}
		require.NoError(t, p.Open(w, v1.Name))
		for i := 0; i < blockRounds; i++ {
			w.Round()
		}
		assert.Equal(t, "Rejected", w.BundlePhase(ns)[v1.Name], "a rejected Bundle stays rejected")
		for _, e := range p.GateEnvs {
			_, ok := w.Steps(ns, v1.Name)[e]
			assert.False(t, ok, "rejected %s promoted %s after its gate opened", v1.Name, e)
			assert.NotEqual(t, "1.1.0", p.Running(w)[e], "%s runs the rejected version", e)
		}
	case "rollback":
		w.MustApply(v1)
		if !w.RunUntil(maxRounds, func() bool { return w.BundlePhase(ns)[v1.Name] == "Verified" }) {
			fail("bundle %s not Verified", v1.Name)
		}
		v2 := ImageBundle(ns, PipelineName, "1.2.0")
		for _, e := range p.GateEnvs {
			w.GitOps.Fail(ns, e, "1.2.0")
		}
		w.MustApply(v2)
		rolledBack := func() bool {
			run := p.Running(w)
			for _, e := range p.GateEnvs {
				if run[e] != "1.1.0" || w.InFlight(ns, v2.Name) {
					return false
				}
			}
			_, failedOrSuperseded := map[string]bool{"Failed": true, "Superseded": true}[w.BundlePhase(ns)[v2.Name]]
			return failedOrSuperseded && rollbackVerified(w, ns)
		}
		if !w.RunUntil(maxRounds, rolledBack) {
			fail("environments %v not rolled back to 1.1.0", p.GateEnvs)
		}
	default:
		t.Fatalf("unknown outcome %q", c.Outcome)
	}
	CheckInvariants(t, w, p)
	t.Logf("%d rounds, reconciles %v", w.Rounds(), w.Reconciles())
}

// rollbackVerified reports whether a rollback Bundle of ns is Verified.
func rollbackVerified(w *World, ns string) bool {
	for name, phase := range w.BundlePhase(ns) {
		b := w.Bundle(ns, name)
		if phase == "Verified" && b.Spec.Provenance != nil && b.Spec.Provenance.RollbackOf != "" {
			return true
		}
	}
	return false
}

func rowOf(c Combo) []string {
	row := make([]string, len(Dimensions))
	row[DimUpdate], row[DimHealth], row[DimGate], row[DimTopology] = c.Update, c.Health, c.Gate, c.Topology
	row[DimDelivery], row[DimSCM], row[DimOutcome] = c.Delivery, c.SCM, c.Outcome
	return row
}

func contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

// upstreamOf is every environment that is not one of envs (no topology has
// an environment after its gated one).
func (p *Plan) upstreamOf(envs []string) []string {
	var out []string
	for _, e := range p.Envs {
		if !contains(envs, e) {
			out = append(out, e)
		}
	}
	return out
}

// runBlocked runs until bundle has promoted every environment its gate does
// not hold and then checks, both ways, that the gate holds: for
// blockRounds more rounds no gated environment gets a Verified step, the
// gate instance reports not ready, and the Bundle is still Promoting.
func (p *Plan) runBlocked(t *testing.T, w *World, bundle string, fail func(string, ...interface{})) {
	t.Helper()
	others := p.upstreamOf(p.GateEnvs)
	settled := func() bool {
		verified := w.Verified(p.NS, bundle)
		for _, e := range others {
			if !contains(verified, e) {
				return false
			}
		}
		return !w.InFlight(p.NS, bundle)
	}
	if !w.RunUntil(maxRounds, settled) {
		fail("bundle %s did not reach the gate (environments %v Verified)", bundle, others)
	}
	for i := 0; i < blockRounds; i++ {
		w.Round()
	}
	if ph := w.BundlePhase(p.NS)[bundle]; ph != "Promoting" {
		fail("bundle %s is %s while its gate is closed, want Promoting", bundle, ph)
	}
	for _, e := range p.GateEnvs {
		if s, ok := w.Steps(p.NS, bundle)[e]; ok && s.Status.State == "Verified" {
			fail("%s promoted %s past a closed gate", bundle, e)
		}
	}
	ready, found := p.gateReady(w, bundle)
	if !found {
		fail("no gate instance for %s", bundle)
	}
	if ready {
		fail("the gate of %s reports ready while closed", bundle)
	}
}

// gateReady reports whether every instance of the plan's gate for bundle is
// ready, and whether there is one.
func (p *Plan) gateReady(w *World, bundle string) (ready, found bool) {
	ready = true
	for _, g := range w.GateInstances(p.NS, bundle) {
		if g.Labels["kardinal.io/gate-name"] != p.Gate.Name {
			continue
		}
		found = true
		ready = ready && g.Status.Ready
	}
	return ready, found
}

func (p *Plan) expectGatesReady(t *testing.T, w *World, bundle string) {
	t.Helper()
	ready, found := p.gateReady(w, bundle)
	assert.True(t, found, "the Graph created the %s gate for %s", p.Gate.Name, bundle)
	assert.True(t, ready, "the %s gate of %s is ready", p.Gate.Name, bundle)
}

func (p *Plan) expectRunning(t *testing.T, w *World, tag string, envs ...string) {
	t.Helper()
	run := p.Running(w)
	for _, e := range envs {
		assert.Equal(t, tag, run[e], "%s runs %s", e, tag)
	}
}

// expectPRs checks the pull requests of bundle: one merged PR per
// environment for pr-review, none for direct delivery.
func (p *Plan) expectPRs(t *testing.T, w *World, bundle string) {
	t.Helper()
	steps := w.Steps(p.NS, bundle)
	for _, e := range p.Envs {
		s := steps[e]
		if p.Delivery == "direct" {
			assert.Empty(t, s.Status.PRURL, "%s: direct delivery opens no PR", e)
			continue
		}
		assert.NotEmpty(t, s.Status.PRURL, "%s: pr-review opens a PR", e)
	}
	if p.Delivery == "direct" {
		assert.Empty(t, w.SCM.PRs(), "direct delivery opens no PR")
		return
	}
	// The gated environment's PR carries the gate results as a commit status.
	for _, e := range p.GateEnvs {
		url := steps[e].Status.PRURL
		for _, pr := range w.SCM.PRs() {
			if strings.HasSuffix(url, fmt.Sprintf("/%d", pr.Number)) {
				assert.Equal(t, "success", w.SCM.Statuses(pr.HeadSHA)["kardinal/gates"], "%s: the PR's kardinal/gates status", e)
			}
		}
	}
}

// CheckInvariants are the properties every scenario ends with: kro saw no
// hard error (every Graph compiles and evaluates), the fake SCM served
// every request kardinal made, no reconciler returned an error, no PR is
// left open by a finished Bundle, every Verified step has its AuditEvent,
// and every environment runs the version of the last Bundle Verified
// there.
func CheckInvariants(t *testing.T, w *World, p *Plan) {
	t.Helper()
	assert.Empty(t, w.Kro.Errors(), "kro hard errors")
	assert.Empty(t, w.SCM.Unknown(), "requests the fake SCM does not serve")
	n, last := w.Errors()
	assert.Empty(t, n, "reconcile errors: %v", last)
	for _, pr := range w.SCM.PRs() {
		assert.NotEqual(t, "open", pr.State, "PR %s#%d (%s) is left open", pr.Repo, pr.Number, pr.Head)
	}
	// Every Verified step is in the audit trail.
	var events v1alpha1.AuditEventList
	require.NoError(t, w.Client.List(w.Ctx, &events, client.InNamespace(p.NS)))
	audited := map[string]bool{}
	for _, e := range events.Items {
		if e.Spec.Action == "PromotionSucceeded" || e.Spec.Action == "RollbackSucceeded" {
			audited[e.Spec.BundleName+"/"+e.Spec.Environment] = true
		}
	}
	var steps v1alpha1.PromotionStepList
	require.NoError(t, w.Client.List(w.Ctx, &steps, client.InNamespace(p.NS)))
	for _, s := range steps.Items {
		if s.Status.State == "Verified" {
			assert.True(t, audited[s.Spec.BundleName+"/"+s.Spec.Environment], "no AuditEvent for Verified %s", s.Name)
		}
	}
	run := p.Running(w)
	last2 := w.LastVerified(p.NS)
	for _, e := range p.Envs {
		if tag, ok := last2[e]; ok {
			assert.Equal(t, tag, run[e], "%s runs the version of the last Bundle Verified there", e)
		}
	}
}
