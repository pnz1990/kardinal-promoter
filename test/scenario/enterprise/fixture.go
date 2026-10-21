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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/scenario"
)

// size is the enterprise's size: Teams namespaces with PipelinesPerTeam
// Pipelines each, and VersionsPerPipeline Bundles created at once for each.
type Size struct{ Teams, PipelinesPerTeam, VersionsPerPipeline int }

// full is the enterprise of the load test: 200 Pipelines, 1,000 versions.
// KARDINAL_SCENARIO_ENTERPRISE=full runs it (CI's scenario-enterprise job);
// otherwise the load test runs quick (3 teams of 3 Pipelines, 3 versions each), so that
// go test -race ./... stays fast. KARDINAL_SCENARIO_TEAMS, _PIPELINES and
// _VERSIONS set one size each.
var (
	full  = Size{Teams: 20, PipelinesPerTeam: 10, VersionsPerPipeline: 5}
	quick = Size{Teams: 3, PipelinesPerTeam: 3, VersionsPerPipeline: 3}
)

func SizeFromEnv() Size {
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
	ReleaseManagers = "release-managers"
	ReleaseManager  = "rm@corp.example.com"
	// forger is a release manager of another team: their Approval in
	// team-02 names a Bundle of team-01.
	forger = "mallory@corp.example.com"
)

func Team(i int) string         { return fmt.Sprintf("team-%02d", i) }
func PipelineName(j int) string { return fmt.Sprintf("app-%02d", j) }

// orgGates are the platform team's gates every team's prod gets.
func OrgGates() []client.Object {
	org := func(name string, spec v1alpha1.PolicyGateSpec) *v1alpha1.PolicyGate {
		return &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: scenario.PolicyNS, Name: name,
			Labels: map[string]string{"kardinal.io/scope": "org", "kardinal.io/applies-to": "prod"}}, Spec: spec}
	}
	return []client.Object{
		org("prod-weekdays", v1alpha1.PolicyGateSpec{Expression: "!schedule.isWeekend", Message: "no weekend releases"}),
		org("prod-change-approval", v1alpha1.PolicyGateSpec{Expression: "true", Message: "a release manager approves",
			Approval: &v1alpha1.GateApprovalPolicy{Required: 1, AllowedGroups: []string{ReleaseManagers}}}),
	}
}

// env is a team's environment: its workloads run in the team's own
// namespace for it (<team>-<env>), which the health check names.
func env(ns, name, approval string) v1alpha1.EnvironmentSpec {
	return v1alpha1.EnvironmentSpec{Name: name, Approval: approval,
		Health: v1alpha1.HealthConfig{Resource: &v1alpha1.ResourceRef{Namespace: ns + "-" + name}}}
}

// RepoURL is the GitOps repository of team ns's Pipeline p.
func RepoURL(ns, p string) string { return repoURL(ns, p) }

func repoURL(ns, p string) string { return "https://github.com/" + ns + "/" + p + "-gitops" }

// tenant creates team ns: its git Secret, its own soak gate and its
// Pipelines, each with a repository of its own. Even teams review every
// promotion in a PR; odd teams push directly.
func Tenant(t *testing.T, w *scenario.World, ns string, pipelines int, review bool) {
	t.Helper()
	for _, o := range scenario.TenantObjects(ns) {
		require.NoError(t, w.Client.Create(w.Ctx, o))
	}
	w.MustApply(&v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "staging-soak",
		Labels: map[string]string{"kardinal.io/scope": "team", "kardinal.io/applies-to": "prod"}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "upstream.staging.soakMinutes >= 1"}})
	for j := 1; j <= pipelines; j++ {
		AddPipeline(t, w, ns, PipelineName(j), review)
	}
}

// AddPipeline creates Pipeline name of team ns, dev, staging and prod, with
// a repository of its own, as Tenant creates each of the team's.
func AddPipeline(t *testing.T, w *scenario.World, ns, name string, review bool) {
	t.Helper()
	approval := "auto"
	if review {
		approval = "pr-review"
	}
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: v1alpha1.PipelineSpec{
			Git:          v1alpha1.PipelineGit{URL: repoURL(ns, name), Branch: "main", SecretRef: &v1alpha1.SecretRef{Name: scenario.GitSecret}},
			Environments: []v1alpha1.EnvironmentSpec{env(ns, "dev", approval), env(ns, "staging", approval), env(ns, "prod", approval)}}}
	require.NoError(t, w.Git.CreateRepo(p.Spec.Git.URL, "main", scenario.RepoFiles(p, scenario.BaseTag)))
	w.MustApply(p)
}

// checkIsolation: every object a Pipeline's promotions made is in the
// Pipeline's namespace and names that namespace's Pipeline and Bundle; a
// Graph reads nothing outside its namespace but the org policy namespace;
// every PR is on a repository of the namespace whose step opened it.
func CheckIsolation(t *testing.T, w *scenario.World, sz Size) {
	t.Helper()
	ctx := w.Ctx
	pipelines := map[string]bool{}
	bundles := map[string]string{} // ns/name -> UID
	for i := 1; i <= sz.Teams; i++ {
		for j := 1; j <= sz.PipelinesPerTeam; j++ {
			pipelines[Team(i)+"/"+PipelineName(j)] = true
		}
		var bl v1alpha1.BundleList
		require.NoError(t, w.Client.List(ctx, &bl, client.InNamespace(Team(i))))
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
			repoNS[Team(i)+"/"+PipelineName(j)+"-gitops"] = Team(i)
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

// checkBounded: no object is reconciled more than bound times while the
// load runs, whatever the number of tenants and versions, and
// once every version settled and the writes stopped, quietRounds more
// rounds reconcile nothing: nothing spins.
func CheckBounded(t *testing.T, w *scenario.World, bound int) {
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
		assert.LessOrEqual(t, maxOf[kind], bound, "%s reconciled %d times", worst[kind], maxOf[kind])
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

// MaxPerObject bounds the reconciles of one object over a load of versions
// created at once;
// the measured maximum is in the test log. quietRounds are the rounds after
// the load settled that must reconcile nothing.
const (
	MaxPerObject = 30
	quietRounds  = 6
)
