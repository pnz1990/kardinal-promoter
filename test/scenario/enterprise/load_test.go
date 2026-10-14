// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package enterprise

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

// size is the enterprise's size: Teams namespaces with PipelinesPerTeam
// Pipelines each, and VersionsPerPipeline Bundles created at once for each.
type size struct{ Teams, PipelinesPerTeam, VersionsPerPipeline int }

// full is the enterprise of the load test: 200 Pipelines, 1,000 versions.
// KARDINAL_SCENARIO_ENTERPRISE=full runs it (CI's scenario-enterprise job);
// otherwise the load test runs quick, a fifth of the Pipelines, so that
// go test -race ./... stays fast. KARDINAL_SCENARIO_TEAMS, _PIPELINES and
// _VERSIONS set one size each.
var (
	full  = size{Teams: 20, PipelinesPerTeam: 10, VersionsPerPipeline: 5}
	quick = size{Teams: 4, PipelinesPerTeam: 5, VersionsPerPipeline: 5}
)

func sizeFromEnv() size {
	s := quick
	if os.Getenv("KARDINAL_SCENARIO_ENTERPRISE") == "full" {
		s = full
	}
	for name, p := range map[string]*int{"TEAMS": &s.Teams, "PIPELINES": &s.PipelinesPerTeam, "VERSIONS": &s.VersionsPerPipeline} {
		if v, err := strconv.Atoi(os.Getenv("KARDINAL_SCENARIO_" + name)); err == nil && v > 0 {
			*p = v
		}
	}
	return s
}

const (
	releaseManagers = "release-managers"
	releaseManager  = "rm@corp.example.com"
	// forger is a release manager of another team: their Approval in
	// team-02 names a Bundle of team-01.
	forger = "mallory@corp.example.com"
)

func team(i int) string         { return fmt.Sprintf("team-%02d", i) }
func pipelineName(j int) string { return fmt.Sprintf("app-%02d", j) }

// orgGates are the platform team's gates every team's prod gets.
func orgGates() []client.Object {
	org := func(name string, spec v1alpha1.PolicyGateSpec) *v1alpha1.PolicyGate {
		return &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: scenario.PolicyNS, Name: name,
			Labels: map[string]string{"kardinal.io/scope": "org", "kardinal.io/applies-to": "prod"}}, Spec: spec}
	}
	return []client.Object{
		org("prod-weekdays", v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend", Message: "no weekend releases"}),
		org("prod-change-approval", v1alpha1.PolicyGateSpec{Expression: "true", Message: "a release manager approves",
			Approval: &v1alpha1.GateApprovalPolicy{Required: 1, AllowedGroups: []string{releaseManagers}}}),
	}
}

// env is a team's environment: its workloads run in the team's own
// namespace for it (<team>-<env>), which the health check names.
func env(ns, name, approval string) v1alpha1.EnvironmentSpec {
	return v1alpha1.EnvironmentSpec{Name: name, Approval: approval,
		Health: v1alpha1.HealthConfig{Resource: &v1alpha1.ResourceRef{Namespace: ns + "-" + name}}}
}

func repoURL(ns, p string) string { return "https://github.com/" + ns + "/" + p + "-gitops" }

// tenant creates team ns: its git Secret, its own soak gate and its
// Pipelines, each with a repository of its own. Even teams review every
// promotion in a PR; odd teams push directly.
func tenant(t *testing.T, w *scenario.World, ns string, pipelines int, review bool) {
	t.Helper()
	for _, o := range scenario.TenantObjects(ns) {
		require.NoError(t, w.Client.Create(w.Ctx, o))
	}
	w.MustApply(&v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "staging-soak",
		Labels: map[string]string{"kardinal.io/scope": "team", "kardinal.io/applies-to": "prod"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "upstream.staging.soakMinutes >= 1"}})
	approval := "auto"
	if review {
		approval = "pr-review"
	}
	for j := 1; j <= pipelines; j++ {
		p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: pipelineName(j)},
			Spec: v1alpha1.PipelineSpec{
				Git:          v1alpha1.PipelineGit{URL: repoURL(ns, pipelineName(j)), Branch: "main", SecretRef: &v1alpha1.SecretRef{Name: scenario.GitSecret}},
				Environments: []v1alpha1.EnvironmentSpec{env(ns, "dev", approval), env(ns, "staging", approval), env(ns, "prod", approval)}}}
		require.NoError(t, w.Git.CreateRepo(p.Spec.Git.URL, "main", scenario.RepoFiles(p, scenario.BaseTag)))
		w.MustApply(p)
	}
}

// approveAll is the release manager approving every version that waits
// for the org approval gate, except in the namespaces of hold.
func approveAll(w *scenario.World, hold map[string]bool) error {
	var gates v1alpha1.PolicyGateList
	if err := w.Client.List(w.Ctx, &gates); err != nil {
		return err
	}
	for _, g := range gates.Items {
		b, env := g.Labels["kardinal.io/bundle"], g.Labels["kardinal.io/environment"]
		if g.Spec.Approval == nil || b == "" || g.Status.Ready || hold[g.Namespace] {
			continue
		}
		_, _, err := lifecycle.RecordApproval(w.Ctx, w.Client, lifecycle.ApprovalRequest{Namespace: g.Namespace, Bundle: b,
			Environment: env, User: releaseManager, Groups: []string{releaseManagers}, Decision: lifecycle.DecisionApprove})
		if err != nil && !strings.Contains(err.Error(), "never promotes again") && !strings.Contains(err.Error(), "already") {
			return fmt.Errorf("approve %s/%s: %w", g.Namespace, b, err)
		}
	}
	return nil
}

// TestEnterprise_MultiTenantLoad is an enterprise on one controller:
// Teams namespaces with PipelinesPerTeam Pipelines each (200 in full),
// the platform team's org gates (a CEL schedule gate and a release-manager
// approval gate) on every prod, each team's own soak gate, half the teams
// on pr-review and half pushing directly, and VersionsPerPipeline versions
// created at once for every Pipeline (1,000 Bundles in full). The
// newest version of every Pipeline ends Verified in every environment and
// the others Superseded or Verified; no object, Graph reference, PR or
// approval crosses a team's namespace; an Approval by another team's
// release manager, in another namespace, for a Bundle of the same name and
// UID, is neither copied into nor opens a team's gate; no object is
// reconciled more than maxPerObject times, and once the writes stop
// nothing is reconciled at all. Each of these fails when the property
// breaks (see the PR: an Approvals ref without its namespace, an injected
// requeue loop). Covers SCEN-TENANT-01, SCEN-TENANT-02.
func TestEnterprise_MultiTenantLoad(t *testing.T) {
	sz := sizeFromEnv()
	w := scenario.NewWorld(t, scenario.Options{PolicyNamespaces: []string{scenario.PolicyNS}, Watch: true, Tick: 30 * time.Second})
	require.NoError(t, w.Client.Create(w.Ctx, scenario.TenantObjects(scenario.PolicyNS)[0]))
	w.MustApply(orgGates()...)
	for i := 1; i <= sz.Teams; i++ {
		tenant(t, w, team(i), sz.PipelinesPerTeam, i%2 == 0)
	}
	held := map[string]bool{team(1): true}
	w.Actors = append(w.Actors, scenario.Reviewer, func(w *scenario.World) error { return approveAll(w, held) })
	w.Round()

	start := time.Now()
	newest := map[string]string{}
	for i := 1; i <= sz.Teams; i++ {
		for j := 1; j <= sz.PipelinesPerTeam; j++ {
			for v := 1; v <= sz.VersionsPerPipeline; v++ {
				b := scenario.ImageBundle(team(i), pipelineName(j), fmt.Sprintf("1.%d.0", v))
				w.MustApply(b)
				newest[team(i)+"/"+pipelineName(j)] = b.Name
			}
		}
	}

	// team-01's approval waits: an Approval in team-02 naming team-01's
	// Bundle, with its UID, must not open team-01's gate.
	victim := newest[team(1)+"/"+pipelineName(1)]
	vb := w.Bundle(team(1), victim)
	forged := &v1alpha1.Approval{ObjectMeta: metav1.ObjectMeta{Namespace: team(2), Name: "forged",
		Labels: map[string]string{"kardinal.io/bundle": victim, "kardinal.io/environment": "prod", "kardinal.io/pipeline": pipelineName(1)}},
		Spec: v1alpha1.ApprovalSpec{Bundle: victim, BundleUID: string(vb.UID), Environment: "prod", User: forger,
			Groups: []string{releaseManagers}, Decision: lifecycle.DecisionApprove}}
	w.MustApply(forged)
	waiting := func() bool {
		_, found := gateReady(w, team(1), victim, "prod-change-approval")
		return found && len(w.Verified(team(1), victim)) == 2
	}
	require.True(t, w.RunUntil(200, waiting), "team-01's newest version reaches its approval gate\n%s", w.Describe(team(1)))
	for i := 0; i < 4; i++ {
		w.Round()
	}
	// The forged Approval is still there (the approver actor, another user,
	// never replaces it), and team-01's gate neither copied it nor opened.
	var still v1alpha1.Approval
	require.NoError(t, w.Client.Get(w.Ctx, client.ObjectKeyFromObject(forged), &still), "the forged Approval exists")
	ready, _ := gateReady(w, team(1), victim, "prod-change-approval")
	assert.False(t, ready, "an Approval in another namespace opened team-01's gate")
	for _, g := range w.GateInstances(team(1), victim) {
		for _, a := range g.Spec.Approvals {
			assert.NotEqual(t, forger, a.User, "gate %s copied the Approval of another namespace", g.Name)
		}
	}
	delete(held, team(1))

	done := func() bool {
		for key, b := range newest {
			ns := strings.SplitN(key, "/", 2)[0]
			if w.Bundle(ns, b).Status.Phase != "Verified" {
				return false
			}
		}
		return true
	}
	if !w.RunUntil(400, done) {
		var stuck []string
		for key, b := range newest {
			ns := strings.SplitN(key, "/", 2)[0]
			if ph := w.Bundle(ns, b).Status.Phase; ph != "Verified" {
				stuck = append(stuck, key+"="+ph)
			}
		}
		sort.Strings(stuck)
		t.Fatalf("after %d rounds, %d newest versions are not Verified: %v\n%s", w.Rounds(), len(stuck), stuck[:min(len(stuck), 10)],
			w.Describe(strings.SplitN(stuck[0], "/", 2)[0]))
	}
	t.Logf("%d Pipelines, %d Bundles: %d rounds, %s, reconciles %v", sz.Teams*sz.PipelinesPerTeam, len(newest)*sz.VersionsPerPipeline,
		w.Rounds(), time.Since(start).Round(time.Second), w.Reconciles())

	checkPhases(t, w, sz, newest)
	checkIsolation(t, w, sz)
	checkBounded(t, w)
	n, last := w.Errors()
	assert.Empty(t, n, "reconcile errors: %v", last)
	assert.Empty(t, w.Kro.Errors())
	assert.Empty(t, w.SCM.Unknown())
}

func gateReady(w *scenario.World, ns, bundle, gate string) (ready, found bool) {
	ready = true
	for _, g := range w.GateInstances(ns, bundle) {
		if g.Labels["kardinal.io/gate-name"] == gate {
			found = true
			ready = ready && g.Status.Ready
		}
	}
	return ready, found
}

// checkPhases: the newest version of each Pipeline is Verified and runs
// everywhere; the older ones are Superseded, or Verified when they
// finished first.
func checkPhases(t *testing.T, w *scenario.World, sz size, newest map[string]string) {
	t.Helper()
	for i := 1; i <= sz.Teams; i++ {
		ns := team(i)
		for name, phase := range w.BundlePhase(ns) {
			b := w.Bundle(ns, name)
			if newest[ns+"/"+b.Spec.Pipeline] == name {
				continue
			}
			assert.Contains(t, []string{"Superseded", "Verified"}, phase, "%s/%s", ns, name)
		}
		for j := 1; j <= sz.PipelinesPerTeam; j++ {
			for _, env := range []string{"dev", "staging", "prod"} {
				dir := "environments/" + env
				c, _, err := w.Git.ReadFile(repoURL(ns, pipelineName(j)), "main", dir+"/kustomization.yaml")
				require.NoError(t, err)
				assert.Contains(t, c, fmt.Sprintf("newTag: 1.%d.0", sz.VersionsPerPipeline), "%s/%s %s", ns, pipelineName(j), env)
			}
		}
	}
	for _, pr := range w.SCM.PRs() {
		assert.NotEqual(t, "open", pr.State, "PR %s#%d left open", pr.Repo, pr.Number)
	}
}

// checkIsolation: every object a Pipeline's promotions made is in the
// Pipeline's namespace and names that namespace's Pipeline and Bundle; a
// Graph reads nothing outside its namespace but the org policy namespace;
// every PR is on a repository of the namespace whose step opened it.
func checkIsolation(t *testing.T, w *scenario.World, sz size) {
	t.Helper()
	ctx := w.Ctx
	pipelines := map[string]bool{}
	bundles := map[string]string{} // ns/name -> UID
	for i := 1; i <= sz.Teams; i++ {
		for j := 1; j <= sz.PipelinesPerTeam; j++ {
			pipelines[team(i)+"/"+pipelineName(j)] = true
		}
		var bl v1alpha1.BundleList
		require.NoError(t, w.Client.List(ctx, &bl, client.InNamespace(team(i))))
		for _, b := range bl.Items {
			bundles[b.Namespace+"/"+b.Name] = string(b.UID)
		}
	}
	belongs := func(kind string, o metav1.Object) {
		l := o.GetLabels()
		ns := o.GetNamespace()
		if p := l["kardinal.io/pipeline"]; p != "" {
			assert.True(t, pipelines[ns+"/"+p], "%s %s/%s names Pipeline %s of another namespace", kind, ns, o.GetName(), p)
		}
		if b := l["kardinal.io/bundle"]; b != "" {
			uid, ok := bundles[ns+"/"+b]
			assert.True(t, ok, "%s %s/%s names Bundle %s of another namespace", kind, ns, o.GetName(), b)
			if u := l["kardinal.io/bundle-uid"]; u != "" {
				assert.Equal(t, uid, u, "%s %s/%s: bundle UID", kind, ns, o.GetName())
			}
		}
	}
	var steps v1alpha1.PromotionStepList
	require.NoError(t, w.Client.List(ctx, &steps))
	repoNS := map[string]string{}
	for i := 1; i <= sz.Teams; i++ {
		for j := 1; j <= sz.PipelinesPerTeam; j++ {
			repoNS[team(i)+"/"+pipelineName(j)+"-gitops"] = team(i)
		}
	}
	for _, s := range steps.Items {
		belongs("PromotionStep", &s)
		assert.True(t, pipelines[s.Namespace+"/"+s.Spec.PipelineName], "step %s/%s", s.Namespace, s.Name)
		if s.Status.PRURL != "" {
			repo, _, found := strings.Cut(strings.TrimPrefix(s.Status.PRURL, "https://github.com/"), "/pull/")
			assert.True(t, found, "step %s/%s PR URL %s", s.Namespace, s.Name, s.Status.PRURL)
			assert.Equal(t, s.Namespace, repoNS[repo], "step %s/%s opened a PR on %s", s.Namespace, s.Name, repo)
		}
	}
	var gates v1alpha1.PolicyGateList
	require.NoError(t, w.Client.List(ctx, &gates))
	for _, g := range gates.Items {
		if g.Labels["kardinal.io/bundle"] == "" {
			continue
		}
		assert.NotEqual(t, scenario.PolicyNS, g.Namespace, "a gate instance of %s is in the org namespace", g.Name)
		belongs("PolicyGate", &g)
	}
	var prs v1alpha1.PRStatusList
	require.NoError(t, w.Client.List(ctx, &prs))
	for _, p := range prs.Items {
		belongs("PRStatus", &p)
	}
	var events v1alpha1.AuditEventList
	require.NoError(t, w.Client.List(ctx, &events))
	for _, e := range events.Items {
		assert.True(t, pipelines[e.Namespace+"/"+e.Spec.PipelineName], "AuditEvent %s/%s names Pipeline %s", e.Namespace, e.Name, e.Spec.PipelineName)
	}
	var approvals v1alpha1.ApprovalList
	require.NoError(t, w.Client.List(ctx, &approvals))
	for _, a := range approvals.Items {
		if a.Name == "forged" {
			continue
		}
		belongs("Approval", &a)
	}
	graphs := &unstructured.UnstructuredList{}
	graphs.SetGroupVersionKind(scenario.GraphGVK.GroupVersion().WithKind("GraphList"))
	require.NoError(t, w.Client.List(ctx, graphs))
	for _, g := range graphs.Items {
		nodes, _, _ := unstructured.NestedSlice(g.Object, "spec", "nodes")
		for _, n := range nodes {
			ref, _, _ := unstructured.NestedMap(n.(map[string]interface{}), "ref")
			if ns, _, _ := unstructured.NestedString(ref, "metadata", "namespace"); ns != "" {
				assert.Contains(t, []string{g.GetNamespace(), scenario.PolicyNS}, ns, "Graph %s/%s reads namespace %s", g.GetNamespace(), g.GetName(), ns)
			}
		}
		owners := g.GetOwnerReferences()
		require.Len(t, owners, 1, "Graph %s/%s", g.GetNamespace(), g.GetName())
		assert.Equal(t, bundles[g.GetNamespace()+"/"+owners[0].Name], string(owners[0].UID), "Graph %s/%s owner", g.GetNamespace(), g.GetName())
	}
}

// checkBounded: no object is reconciled more than maxPerObject times
// while the load runs, whatever the number of tenants and versions, and
// once every version settled and the writes stopped, quietRounds more
// rounds reconcile nothing: nothing spins.
func checkBounded(t *testing.T, w *scenario.World) {
	t.Helper()
	maxOf := map[string]int{}
	worst := map[string]string{}
	for key, n := range w.ReconcilesPerObject() {
		kind := strings.SplitN(key, "/", 2)[0]
		if n > maxOf[kind] {
			maxOf[kind], worst[kind] = n, key
		}
	}
	for _, kind := range []string{"Bundle", "PromotionStep", "PolicyGate", "Pipeline", "PRStatus"} {
		t.Logf("%s: at most %d reconciles per object (%s)", kind, maxOf[kind], worst[kind])
		assert.LessOrEqual(t, maxOf[kind], maxPerObject, "%s reconciled %d times", worst[kind], maxOf[kind])
	}
	// The last writes of the load (Graph and Pipeline status) settle
	// within a few rounds of the last Verified.
	settled := false
	for i := 0; i < 10 && !settled; i++ {
		settled = w.Round() == 0
	}
	require.True(t, settled, "writes did not stop within 10 rounds of the last version Verified")
	before := w.Reconciles()
	beforeObj := w.ReconcilesPerObject()
	for i := 0; i < quietRounds; i++ {
		w.Round()
	}
	after := w.Reconciles()
	for kind, n := range after {
		if d := n - before[kind]; d != 0 {
			var busy []string
			for key, m := range w.ReconcilesPerObject() {
				if strings.HasPrefix(key, kind+"/") && m != beforeObj[key] {
					busy = append(busy, fmt.Sprintf("%s(+%d)", key, m-beforeObj[key]))
				}
			}
			sort.Strings(busy)
			assert.Zero(t, d, "%d %s reconciles after every version settled: %v", d, kind, busy[:min(len(busy), 5)])
		}
	}
}

// maxPerObject bounds the reconciles of one object over the whole load;
// the measured maximum is in the test log. quietRounds are the rounds after
// the load settled that must reconcile nothing.
const (
	maxPerObject = 30
	quietRounds  = 6
)
