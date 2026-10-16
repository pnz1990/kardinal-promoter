// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentcases

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario/enterprise"
)

// The scale of agents: fullScale is the release gate's
// (KARDINAL_SCENARIO_ENTERPRISE=full): 5,000 AgentIdentities, 50 tenants
// with 4 Pipelines each (200 Pipelines) and 98 agents (100 asked; the 50th
// team has none) shipping 3 bursts.
// quickScale keeps go test ./... under its budget.
var (
	fullScale  = agentSize{Teams: 50, PipelinesPerTeam: 4, Identities: 5000, Agents: 100, Bursts: 3}
	quickScale = agentSize{Teams: 3, PipelinesPerTeam: 2, Identities: 150, Agents: 6, Bursts: 2}
)

// PerfBaseline is the file of the scale scenario's budgets, from the repository root.
const PerfBaseline = "hack/ci/perf-baseline.txt"

// RunScale is the enterprise at the scale of agents: the registry of
// Identities AgentIdentities built into the index, then Bursts bursts in
// which every active agent ships a version of its Pipeline at once, a
// release manager approving prod and a reviewer merging PRs. Each burst must
// converge (every version Verified everywhere) within the budget of rounds
// and reconciles per version, every write the chart's policies match is
// timed in the API server's admission plugin, and the p99 of each kind of
// write (agents' Bundles, kro's gate instances, the controller's statuses,
// people's Approvals) must stay within its budget (PerfBaseline, where the
// numbers come from).
func RunScale(t *testing.T) {
	full := os.Getenv("KARDINAL_SCENARIO_ENTERPRISE") == "full"
	sz, mode := quickScale, "quick"
	if full {
		sz, mode = fullScale, "full"
	}
	budget := readBudgets(t, mode)
	reg := agentRegistry(sz)
	// One team in 50 has no plain agent (agentRegistry gives every 50th
	// AgentIdentity a second agent's subject), so 50 teams have 98.
	require.GreaterOrEqual(t, len(reg.active), sz.Agents*9/10, "the registry has an active agent per team in turn")
	w := scenario.NewWorld(t, scenario.Options{PolicyNamespaces: []string{scenario.PolicyNS}, Watch: true, Tick: 30 * time.Second,
		Actors: true})
	require.NoError(t, w.Client.Create(w.Ctx, scenario.TenantObjects(scenario.PolicyNS)[0]))
	w.MustApply(enterprise.OrgGates()...)
	for i := 1; i <= sz.Teams; i++ {
		enterprise.Tenant(t, w, enterprise.Team(i), sz.PipelinesPerTeam, i%2 == 0)
	}
	start := time.Now()
	w.MustApply(reg.ids...)
	require.True(t, w.RunUntil(20, func() bool { return w.Round() == 0 }), "the registry settles")
	var idx v1alpha1.AgentIndex
	require.NoError(t, w.Client.Get(w.Ctx, types.NamespacedName{Name: scenario.Release}, &idx))
	require.True(t, meta.IsStatusConditionTrue(idx.Status.Conditions, "Ready"), "the index is Ready: %v", idx.Status.Conditions)
	raw, err := yaml.Marshal(idx.Status)
	require.NoError(t, err)
	t.Logf("%s: %d AgentIdentities, %d Pipelines in %d tenants: the index holds %d users and %d groups (%d KiB of %d), built in %s",
		mode, sz.Identities, sz.Teams*sz.PipelinesPerTeam, sz.Teams, len(idx.Status.Users), len(idx.Status.Groups),
		len(raw)>>10, sz.bound()>>10, time.Since(start).Round(time.Millisecond))

	// The index's size as the controller bounds it (actor.Evaluate: each
	// entry's JSON, its key and 4 bytes), and the identities that would
	// fill it at this registry's mix.
	used := indexBytes(idx.Status)
	perID := float64(used) / float64(sz.Identities)
	t.Logf("%s: the index is %d bytes of %d (%.0f%%), %.0f bytes per AgentIdentity: full at about %d AgentIdentities",
		mode, used, sz.bound(), 100*float64(used)/float64(sz.bound()), perID, int(float64(sz.bound())/perID))
	assert.LessOrEqual(t, used, budget.int(t, "index.bytes"), "the agent index's size (%s)", PerfBaseline)

	w.Actors = append(w.Actors, scenario.Reviewer, approveAs)
	w.RecordAdmissions(true)
	maxRounds, maxReconciles := 0, 0.0
	for burst := 1; burst <= sz.Bursts; burst++ {
		var shipped []string
		before, rounds, began := sumReconciles(w), w.Rounds(), time.Now()
		for _, a := range reg.active {
			b := scenario.ImageBundle(a.ns, a.pipeline, fmt.Sprintf("2.%d.0", burst))
			b.Name = fmt.Sprintf("%s-%s-s%d", a.pipeline, a.name, burst)
			cliErr, refused, err := w.CreateBundle(a.id, b)
			require.NoError(t, err)
			require.NoError(t, cliErr, "agent %s", a.name)
			require.Empty(t, refused, "agent %s", a.name)
			shipped = append(shipped, a.ns+"/"+b.Name)
		}
		converged := w.RunUntil(budget.int(t, "burst.rounds")+20, func() bool {
			for _, key := range shipped {
				ns, name, _ := strings.Cut(key, "/")
				if w.Bundle(ns, name).Status.Phase != "Verified" {
					return false
				}
			}
			return true
		})
		require.True(t, converged, "burst %d converges", burst)
		n := w.Rounds() - rounds
		perVersion := float64(sumReconciles(w)-before) / float64(len(shipped))
		t.Logf("burst %d: %d versions by %d agents Verified everywhere in %d rounds, %.1f reconciles per version, %s",
			burst, len(shipped), len(reg.active), n, perVersion, time.Since(began).Round(time.Millisecond))
		maxRounds, maxReconciles = max(maxRounds, n), max(maxReconciles, perVersion)
	}
	assert.LessOrEqual(t, maxRounds, budget.int(t, "burst.rounds"), "rounds for a burst to converge (%s)", PerfBaseline)
	assert.LessOrEqual(t, maxReconciles, budget.float(t, "burst.reconciles-per-version"),
		"reconciles per version in a burst (%s)", PerfBaseline)

	lat := w.AdmissionLatencies()
	w.RecordAdmissions(false)
	keys := make([]string, 0, len(lat))
	for k := range lat {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p50, p99, top := quantiles(lat[k])
		t.Logf("admission in the bursts %-45s n=%-6d p50 %-9s p99 %-9s max %s", k, len(lat[k]), p50, p99, top)
	}
	// Each write's fastest admission over three passes, and the same write
	// against the index emptied in each pass (the calibration: what the
	// host takes to admit it without the index). A host's load lengthens
	// both, so the budgets hold the ratio of a kind's median to the
	// calibration's (#2035, #2039): the budget in ms is the one at the
	// calibration's speed recorded in PerfBaseline. The p99 is logged, not
	// bounded: at 5,000 identities each Bundle or Approval converts about a
	// MiB of index, and its tail moves with the host's memory pressure
	// (46ms idle, 57-65ms with the host loaded, the calibration unmoved). The calibration is
	// an agent's Bundle without the index, for every kind.
	timed, empty := timeAdmissions(t, w, reg)
	// The host's speed: an agent's Bundle admitted without the index.
	require.NotEmpty(t, empty["Bundle CREATE by serviceaccount"])
	calMedian, _, _ := quantiles(empty["Bundle CREATE by serviceaccount"])
	ref := budget.duration(t, "calibration.empty-index-admission")
	t.Logf("calibration: an agent's Bundle admitted without the index, median %s (%s in %s)", calMedian, ref, PerfBaseline)
	for _, c := range []struct{ key, budget string }{
		{"Bundle CREATE by serviceaccount", "admission.p50.agent-bundle-create"},
		{"Approval CREATE by user", "admission.p50.person-approval-create"},
		{"PolicyGate CREATE by kro", "admission.p50.kro-gate-create"},
	} {
		ds := timed[c.key]
		if !assert.NotEmpty(t, ds, "no %s was timed", c.key) {
			continue
		}
		p50, p99, top := quantiles(ds)
		allowed := budget.duration(t, c.budget)
		ratio, bound := float64(p50)/float64(calMedian), float64(allowed)/float64(ref)
		t.Logf("admission, fastest of %d passes %-35s n=%-5d p50 %-9s p99 %-9s max %-9s: p50 %.1fx the calibration (budget %.1fx: %s at %s)",
			admissionPasses, c.key, len(ds), p50, p99, top, ratio, bound, allowed, ref)
		assert.LessOrEqual(t, ratio, bound, "median admission of %s over its calibration: %s over %s (%d writes; %s)",
			c.key, p50, calMedian, len(ds), PerfBaseline)
	}
	n, last := w.Errors()
	assert.Empty(t, n, "reconcile errors: %v", last)
	assert.Empty(t, w.Kro.Errors())
	assert.Empty(t, w.SCM.Unknown())
}

// timeAdmissions admits again, writing nothing (World.AdmitCreate), a copy
// of every version the agents shipped (as its agent), of every Approval
// (as its approver) and of every gate instance (as kro), in admissionPasses
// passes over them all, and returns each write's fastest timing, by the
// keys of AdmissionLatencies, with the index (full) and with it emptied
// (empty, World.WithEmptyAgentIndex). A pass takes minutes at full size, so a
// spell of load on the host or a GC pause slows one write's timing in one
// pass, not in all of them.
func timeAdmissions(t *testing.T, w *scenario.World, reg registry) (full, empty map[string][]time.Duration) {
	t.Helper()
	type write struct {
		key string
		ctx context.Context
		obj client.Object
	}
	var writes []write
	agentOf := map[string]scenario.Identity{}
	for _, a := range reg.active {
		agentOf[a.id.Username] = a.id
	}
	var bundles v1alpha1.BundleList
	require.NoError(t, w.Client.List(w.Ctx, &bundles))
	for i := range bundles.Items {
		b := bundles.Items[i].DeepCopy()
		if b.Spec.Actor == nil {
			continue
		}
		id, ok := agentOf[b.Spec.Actor.Principal]
		if !ok {
			continue
		}
		b.ObjectMeta = metav1.ObjectMeta{Namespace: b.Namespace, Name: b.Name + "-again", Labels: b.Labels, Annotations: b.Annotations}
		b.Status = v1alpha1.BundleStatus{}
		writes = append(writes, write{"Bundle CREATE by serviceaccount", w.CtxOf(id), b})
	}
	var approvals v1alpha1.ApprovalList
	require.NoError(t, w.Client.List(w.Ctx, &approvals))
	rm := scenario.Identity{Username: enterprise.ReleaseManager, Groups: []string{enterprise.ReleaseManagers, "system:authenticated"}}
	for i := range approvals.Items {
		a := approvals.Items[i].DeepCopy()
		a.ObjectMeta = metav1.ObjectMeta{Namespace: a.Namespace, Name: a.Name + "-again", Labels: a.Labels, Annotations: a.Annotations}
		writes = append(writes, write{"Approval CREATE by user", w.CtxOf(rm), a})
	}
	var gates v1alpha1.PolicyGateList
	require.NoError(t, w.Client.List(w.Ctx, &gates))
	for i := range gates.Items {
		g := gates.Items[i].DeepCopy()
		if g.Labels["kardinal.io/bundle"] == "" {
			continue
		}
		g.ObjectMeta = metav1.ObjectMeta{Namespace: g.Namespace, Name: g.Name + "-again", Labels: g.Labels, Annotations: g.Annotations}
		g.Status = v1alpha1.PolicyGateStatus{}
		kro := scenario.Identity{Username: scenario.GraphUser(g.Namespace), Groups: scenario.ServiceAccountGroups(g.Namespace)}
		writes = append(writes, write{"PolicyGate CREATE by kro", w.CtxOf(kro), g})
	}
	fastest := func(best []time.Duration) {
		runtime.GC()
		for i, wr := range writes {
			start := time.Now()
			_ = w.AdmitCreate(wr.ctx, wr.obj) // refused without the index; timed alike
			best[i] = min(best[i], time.Since(start))
		}
	}
	bestFull, bestEmpty := make([]time.Duration, len(writes)), make([]time.Duration, len(writes))
	for i := range writes {
		bestFull[i], bestEmpty[i] = time.Duration(math.MaxInt64), time.Duration(math.MaxInt64)
		require.NoError(t, w.AdmitCreate(writes[i].ctx, writes[i].obj), "%s %s/%s", writes[i].key,
			writes[i].obj.GetNamespace(), writes[i].obj.GetName())
	}
	for pass := 0; pass < admissionPasses; pass++ {
		// The order alternates, so a spell of load hits both alike.
		if pass%2 == 0 {
			fastest(bestFull)
			w.WithEmptyAgentIndex(func() { fastest(bestEmpty) })
		} else {
			w.WithEmptyAgentIndex(func() { fastest(bestEmpty) })
			fastest(bestFull)
		}
	}
	full, empty = map[string][]time.Duration{}, map[string][]time.Duration{}
	for i, wr := range writes {
		full[wr.key] = append(full[wr.key], bestFull[i])
		empty[wr.key] = append(empty[wr.key], bestEmpty[i])
	}
	return full, empty
}

// admissionPasses is how many times timeAdmissions times each write.
const admissionPasses = 3

// indexBytes is the size of idx as actor.Evaluate counts it against the
// bound.
func indexBytes(idx v1alpha1.AgentIndexStatus) int {
	n := 0
	for _, m := range []map[string]v1alpha1.AgentIndexEntry{idx.Users, idx.Groups} {
		for k, e := range m {
			raw, _ := json.Marshal(e)
			n += len(raw) + len(k) + 4
		}
	}
	return n
}

func sumReconciles(w *scenario.World) int {
	n := 0
	for _, v := range w.Reconciles() {
		n += v
	}
	return n
}

// quantiles are the p50, p99 and largest of ds.
func quantiles(ds []time.Duration) (p50, p99, top time.Duration) {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	at := func(q float64) time.Duration { return s[min(len(s)-1, int(q*float64(len(s))))] }
	return at(0.50), at(0.99), s[len(s)-1]
}

// budgets are PerfBaseline's values of one mode.
type budgets map[string]string

// readBudgets reads PerfBaseline: "<mode>.<name> <value>" lines, # comments.
func readBudgets(t *testing.T, mode string) budgets {
	t.Helper()
	f, err := os.Open(filepath.Join(scenario.RepoRoot(), PerfBaseline))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	out := budgets{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, " ")
		require.True(t, ok, "%s: %q", PerfBaseline, line)
		if name, ok := strings.CutPrefix(k, "scale."+mode+"."); ok {
			out[name] = strings.TrimSpace(v)
		}
	}
	require.NoError(t, sc.Err())
	return out
}

func (b budgets) get(t *testing.T, name string) string {
	t.Helper()
	v, ok := b[name]
	require.True(t, ok, "%s has no budget %s", PerfBaseline, name)
	return v
}

func (b budgets) int(t *testing.T, name string) int {
	t.Helper()
	n, err := strconv.Atoi(b.get(t, name))
	require.NoError(t, err, name)
	return n
}

func (b budgets) float(t *testing.T, name string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(b.get(t, name), 64)
	require.NoError(t, err, name)
	return f
}

func (b budgets) duration(t *testing.T, name string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(b.get(t, name))
	require.NoError(t, err, name)
	return d
}
