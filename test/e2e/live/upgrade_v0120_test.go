//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// guideChartV0120 is the chart reference of the "Upgrading from v0.12.0"
// guide: the test replaces it with the checkout's chart.
const guideChartV0120 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.12.1"

// The release the suite upgrades from, as components/kardinal-v0120.sh
// installs it (hack/e2e/versions.env).
const (
	v0120Image = "ghcr.io/pnz1990/kardinal-promoter/controller:v0.12.0"
	v0120Chart = "kardinal-promoter-0.12.0"
)

// u0120AgentRunDelay is how long the verify gate's AgentRun works before it
// writes its Verdict: long enough to be running when the upgrade starts.
const u0120AgentRunDelay = 4 * time.Minute

// u0120Estate is the estate v0.12.0 runs before the upgrade, one namespace
// per concern:
//
//   - flow: versions mid-promotion (a one-minute bake on uat and prod), prod
//     PRs open for review, a rollback v0.12.0 created still going through
//     test and uat, and a Pipeline with two Verified versions (and a dr
//     environment beside prod) to roll back after the upgrade;
//   - approve: prod behind a two-approver gate, one approval given (alice,
//     with the v0.12.0 CLI) or none;
//   - held: prod behind a gate whose rule is false;
//   - verify: prod behind verify gates, one waiting for an AgentRun still
//     running, one for two people's Verdicts with one recorded;
//   - frozen: an agent's version held at test by kardinal freeze --agents.
type u0120Estate struct {
	e                                   *framework.Env
	flow, approve, held, verify, frozen *scale.Fleet
	repos                               map[string]gitserver.Repo // namespace/pipeline → repo
}

func (s *u0120Estate) fleets() []*scale.Fleet {
	return []*scale.Fleet{s.flow, s.approve, s.held, s.verify, s.frozen}
}

// old is the estate in u0110Estate's shape, for its snapshot and checks.
func (s *u0120Estate) old() *u0110Estate {
	return &u0110Estate{e: s.e, auto: s.flow, review: s.approve, approve: s.held, fleet: s.verify, ops: s.frozen}
}

func (s *u0120Estate) pipeline(t *testing.T, f *scale.Fleet, name string, envs []v1alpha1.EnvironmentSpec, edits ...func(*v1alpha1.Pipeline)) string {
	t.Helper()
	var dirs []string
	for _, env := range envs {
		dirs = append(dirs, env.Name)
	}
	repo := f.Repo(t, f.NS+"-"+name, map[string][]string{name: dirs})
	p := f.PipelineSpec(name, repo, envs, edits...)
	f.Apply(t, p, repo)
	s.repos[f.NS+"/"+name] = repo
	return name
}

// envFile is the kustomization of pipeline's env in its repo, as git has it.
func (s *u0120Estate) envFile(t *testing.T, f *scale.Fleet, pipeline, env string) string {
	t.Helper()
	repo := s.repos[f.NS+"/"+pipeline]
	b, err := s.e.Git.ReadFile(context.Background(), repo, repo.Branch, scale.EnvPath(pipeline, env)+"/kustomization.yaml")
	require.NoError(t, err)
	return string(b)
}

// objects are the uids of the estate's gate instances, Approvals, Verdicts,
// AgentRuns and freezes (ChangeWindows), by "Kind namespace/name".
func (s *u0120Estate) objects(t *testing.T) map[string]types.UID {
	t.Helper()
	ctx := context.Background()
	out := map[string]types.UID{}
	put := func(kind string, o client.Object) { out[kind+" "+o.GetNamespace()+"/"+o.GetName()] = o.GetUID() }
	for _, f := range s.fleets() {
		var gl v1alpha1.PolicyGateList
		require.NoError(t, s.e.Client.List(ctx, &gl, client.InNamespace(f.NS), client.HasLabels{"kardinal.io/bundle"}))
		for i := range gl.Items {
			put("PolicyGate", &gl.Items[i])
		}
		var al v1alpha1.ApprovalList
		require.NoError(t, s.e.Client.List(ctx, &al, client.InNamespace(f.NS)))
		for i := range al.Items {
			put("Approval", &al.Items[i])
		}
		var vl v1alpha1.VerdictList
		require.NoError(t, s.e.Client.List(ctx, &vl, client.InNamespace(f.NS)))
		for i := range vl.Items {
			put("Verdict", &vl.Items[i])
		}
		var rl v1alpha1.AgentRunList
		require.NoError(t, s.e.Client.List(ctx, &rl, client.InNamespace(f.NS)))
		for i := range rl.Items {
			put("AgentRun", &rl.Items[i])
		}
	}
	var cw v1alpha1.ChangeWindowList
	require.NoError(t, s.e.Client.List(ctx, &cw))
	for i := range cw.Items {
		put("ChangeWindow", &cw.Items[i])
	}
	return out
}

func u0120Envs(names ...string) []v1alpha1.EnvironmentSpec {
	var envs []v1alpha1.EnvironmentSpec
	for _, n := range names {
		envs = append(envs, v1alpha1.EnvironmentSpec{Name: n})
	}
	return envs
}

// u0120Running is what says which release runs: the controller
// Deployment's image and chart label, its Pods' images by uid, and the
// kardinal CRDs a server-side apply (the guide's step 2) owns fields of.
type u0120Running struct {
	image, chart string
	pods         map[types.UID]string
	applied      map[string]bool // CRD name → kubectl owns fields of it by Apply
}

func u0120Release(t *testing.T, e *framework.Env, rel *framework.Release) u0120Running {
	t.Helper()
	r := u0120Running{pods: map[types.UID]string{}, applied: map[string]bool{}}
	d := rel.Deployment(t)
	r.chart = d.Labels["helm.sh/chart"]
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == "controller" {
			r.image = c.Image
		}
	}
	for _, p := range rel.Pods(t) {
		for _, c := range p.Spec.Containers {
			if c.Name == "controller" {
				r.pods[p.UID] = c.Image
			}
		}
	}
	crds, err := e.Dynamic.Resource(crdGVR).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	for _, u := range crds.Items {
		if !strings.HasSuffix(u.GetName(), ".kardinal.io") {
			continue
		}
		r.applied[u.GetName()] = false
		for _, mf := range u.GetManagedFields() {
			if strings.HasPrefix(mf.Manager, "kubectl") && mf.Operation == metav1.ManagedFieldsOperationApply {
				r.applied[u.GetName()] = true
			}
		}
	}
	return r
}

// u0120CheckoutChart is the checkout chart's helm.sh/chart label.
func u0120CheckoutChart(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(framework.RepoRoot(t), "chart", "kardinal-promoter", "Chart.yaml"))
	require.NoError(t, err)
	var c struct{ Name, Version string }
	require.NoError(t, yaml.Unmarshal(data, &c))
	require.NotEmpty(t, c.Version)
	return c.Name + "-" + c.Version
}

// kroPods are the kro controller's Pods: uid → restarts.
func kroPods(t *testing.T, e *framework.Env) map[types.UID]int32 {
	t.Helper()
	var pods corev1.PodList
	require.NoError(t, e.Client.List(context.Background(), &pods, client.InNamespace("kro-system"),
		client.MatchingLabels{"app.kubernetes.io/name": "kro"}))
	out := map[types.UID]int32{}
	for _, p := range pods.Items {
		var n int32
		for _, cs := range p.Status.ContainerStatuses {
			n += cs.RestartCount
		}
		out[p.UID] = n
	}
	return out
}

// kroLevel matches a kro log line and its level: kro logs with
// controller-runtime's zap console encoder, tab-separated, the level second.
var kroLevel = regexp.MustCompile(`^\S+\t([A-Z]+)\t`)

// kroErrors are the lines at error level or above of the kro logs the
// Collector wrote to dir, but the benign ones (invariants.Benign). lines
// counts the lines with a level: none means the format is not the one
// kroLevel reads, and the check would see no error.
func kroErrors(t *testing.T, dir string) (lines int, errs []string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "kro-system.*.log"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "no kro log was read from %s: nothing was checked", dir)
	for _, name := range files {
		f, err := os.Open(name)
		require.NoError(t, err)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	line:
		for sc.Scan() {
			text := sc.Text()
			m := kroLevel.FindStringSubmatch(text)
			if m == nil {
				continue
			}
			lines++
			if !slices.Contains([]string{"ERROR", "DPANIC", "PANIC", "FATAL"}, m[1]) {
				continue
			}
			for _, re := range invariants.Benign {
				if re.MatchString(text) {
					continue line
				}
			}
			errs = append(errs, filepath.Base(name)+": "+text)
		}
		require.NoError(t, sc.Err())
		require.NoError(t, f.Close())
	}
	return lines, errs
}

// TestUpgradeV0120_Estate builds an estate under kardinal-promoter v0.12.0 as
// released (its chart, CLI and kro) and follows "Upgrading from v0.12.0" in
// docs/upgrade.md, running the guide's own commands, while versions are in
// flight.
//
// The upgrade is checked to have happened: before it the controller runs
// v0.12.0's image and chart and no kardinal CRD is server-side applied;
// after it the Deployment and every Pod run the checkout's image and chart
// (new Pods), and the guide's apply owns every kardinal CRD. kro is not
// upgraded: its Pods are the same.
//
// After the upgrade:
//   - kardinal doctor passes and every Pipeline is Ready;
//   - every in-flight version is the same Bundle (uid) with the same Graph,
//     PromotionSteps and PR numbers; every gate instance, Approval, Verdict,
//     AgentRun and freeze is the same object;
//   - gates that held a version keep holding it (no step in their
//     environment for 30s): the two-approver gate until bob's approval
//     completes alice's (2 of 2); the false gate until kardinal override;
//     the verify gates until the AgentRun that was running finishes (the
//     same run, created before the upgrade) and its Verdict counts, and
//     until a second person's Verdict joins the one recorded before; the
//     freeze holds the agent's version until kardinal unfreeze;
//   - every in-flight version reaches Verified with its PRs; the rollback
//     v0.12.0 created goes through test and uat, as v0.12 rollbacks do;
//   - no Bundle has two Graphs, no (Bundle, environment) two steps, no PR
//     head branch two PRs; each namespace passes the invariants; every
//     stored object reads with this release's types;
//   - a rollback of prod after the upgrade restores the earlier version in
//     prod and in test and uat, the environments upstream of it (v0.12
//     rollbacks go through them), and leaves dr, beside prod, as it was;
//   - kardinal get pipelines -A, the UI API and a new version work;
//   - neither the controller nor kro restarted, and neither logged an
//     unexpected error from the upgrade on.
//
// Covers UPG-V0120-01, UPG-V0120-02, UPG-V0120-03, UPG-V0120-04, UPG-V0120-05.
func TestUpgradeV0120_Estate(t *testing.T) {
	testStart := time.Now()
	e := framework.New(t)
	ctx := context.Background()
	t.Logf("Kubernetes 1.%d", serverMinor(t, e))
	seed, err := scale.Seed()
	require.NoError(t, err)
	s := &u0120Estate{e: e, repos: map[string]gitserver.Repo{}}
	newFleet := func(name string) *scale.Fleet { return scale.NewFleet(t, e, scale.NewRNG(seed, name)) }
	s.flow, s.approve, s.held, s.verify, s.frozen = newFleet("flow"), newFleet("approve"), newFleet("held"), newFleet("verify"), newFleet("frozen")
	bundle := func(f *scale.Fleet, p string, i int) string { return f.MustCreateBundle(t, p, scale.Tag(p, i)).Name }
	// cliBundle creates version i of p with the v0.12.0 CLI, which stamps
	// the test's user as its author: a verify gate (excludeAuthor, the
	// default) holds a version with no verified author.
	cliBundle := func(f *scale.Fleet, p string, i int) string {
		out, err := e.KardinalV0120(t, f.NS, "create", "bundle", p, "--image", scale.ImageRepo+":"+scale.Tag(p, i))
		require.NoError(t, err, out)
		m := v081BundleCreated.FindStringSubmatch(out)
		require.Len(t, m, 2, "kardinal create bundle names the Bundle it created:\n%s", out)
		return m[1]
	}
	three := u0120Envs("test", "uat", "prod")

	// ── The estate, under v0.12.0 ───────────────────────────────────────
	var bakes, reviews []string
	for i := range 3 {
		bakes = append(bakes, s.pipeline(t, s.flow, u0110Name("bake", i), three, u0110Bake("uat", "prod")))
	}
	for i := range 2 {
		reviews = append(reviews, s.pipeline(t, s.flow, u0110Name("review", i), u0110Envs("pr-review")))
	}
	// steady: dr follows uat beside prod, so it is not upstream of prod.
	steady := s.pipeline(t, s.flow, "steady", append(u0120Envs("test", "uat", "prod"),
		v1alpha1.EnvironmentSpec{Name: "dr", DependsOn: []string{"uat"}}))
	rbPre := s.pipeline(t, s.flow, "rollback-pre", three, u0110Bake("test", "uat"))
	fresh := s.pipeline(t, s.flow, "fresh", u0120Envs("test", "prod"))

	gate := framework.Gate(s.approve.NS, "two-approvers", "prod", "true", recheck)
	gate.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 2, AllowedGroups: []string{"release-managers"}}
	e.CreateGate(t, gate)
	alice := userKubeconfig(t, e, s.approve.NS, "alice@example.com", "release-managers")
	bob := userKubeconfig(t, e, s.approve.NS, "bob@example.com", "release-managers")
	var approves []string
	for i := range 2 {
		approves = append(approves, s.pipeline(t, s.approve, u0110Name("approve", i), three))
	}

	e.CreateGate(t, framework.Gate(s.held.NS, "release-window", "prod", "false", recheck))
	var helds []string
	for i := range 2 {
		helds = append(helds, s.pipeline(t, s.held, u0110Name("held", i), three))
	}

	agent := registerAgent(t, e, s.verify.NS, "qa", "bob@example.com",
		map[string]string{"PROBE_DELAY": u0120AgentRunDelay.String(), "PROBE_RESULTS": "pass"})
	byRunGate := framework.Gate(s.verify.NS, "agent-check", "prod", "true", recheck)
	byRunGate.Spec.Verify = &v1alpha1.GateVerifyPolicy{Check: "smoke", Required: 1, AllowedAgents: []string{agent},
		Run: &v1alpha1.GateVerifyRun{Template: agent, Count: 1, Namespace: s.verify.NS}}
	e.CreateGate(t, byRunGate)
	byPersonGate := framework.Gate(s.verify.NS, "people-check", "live", "true", recheck)
	byPersonGate.Spec.Verify = &v1alpha1.GateVerifyPolicy{Check: "explore", Required: 2}
	e.CreateGate(t, byPersonGate)
	byRun := s.pipeline(t, s.verify, "by-run", u0120Envs("uat", "prod"))
	byPerson := s.pipeline(t, s.verify, "by-person", u0120Envs("qa", "live"))
	verifier := func(user string) string {
		kc := userKubeconfig(t, e, s.verify.NS, user)
		name := "verifier-" + strings.NewReplacer("@", "-", ".", "-").Replace(user)
		_, err := e.Kube.RbacV1().RoleBindings(s.verify.NS).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.verify.NS},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: verifierRole},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: user}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		waitBoundNamed(t, e, s.verify.NS, name)
		return kc
	}
	carol, dave := verifier("carol@example.com"), verifier("dave@example.com")

	shipped := s.pipeline(t, s.frozen, "shipped", u0120Envs("test", "prod"))
	fixer := policyAgent(t, e, s.frozen.NS, "fixer", "sponsor-"+s.frozen.NS+"@example.com", v1alpha1.AutonomyAuto)
	t.Logf("estate: %d Pipelines in %d namespaces", len(s.repos), len(s.fleets()))

	// Versions that finish under v0.12.0: two on steady (the rollback after
	// the upgrade returns prod to the first), and rollback-pre's two.
	steady1 := bundle(s.flow, steady, 1)
	e.WaitBundlePhase(t, s.flow.NS, steady1, "Verified", promoteTimeout)
	steady2 := bundle(s.flow, steady, 2)
	rbPre1 := bundle(s.flow, rbPre, 1)
	e.WaitBundlePhase(t, s.flow.NS, steady2, "Verified", promoteTimeout)
	e.WaitBundlePhase(t, s.flow.NS, rbPre1, "Verified", 2*promoteTimeout)
	rbPre2 := bundle(s.flow, rbPre, 2)
	e.WaitBundlePhase(t, s.flow.NS, rbPre2, "Verified", 2*promoteTimeout)

	// Versions in flight across the upgrade.
	inFlight := map[string]string{} // namespace/bundle → pipeline
	add := func(f *scale.Fleet, p string, i int) string {
		b := bundle(f, p, i)
		inFlight[f.NS+"/"+b] = p
		return b
	}
	for _, p := range reviews {
		add(s.flow, p, 1)
	}
	approveB := map[string]string{}
	for _, p := range approves {
		approveB[p] = add(s.approve, p, 1)
	}
	heldB := map[string]string{}
	for _, p := range helds {
		heldB[p] = add(s.held, p, 1)
	}
	byPersonB := cliBundle(s.verify, byPerson, 1)
	inFlight[s.verify.NS+"/"+byPersonB] = byPerson

	// The freeze, then the agent ships: its version waits at test.
	out, err := e.KardinalV0120(t, s.frozen.NS, "freeze", "--agents", "--reason", "INC-0120 agents paused", "--yes")
	require.NoError(t, err, out)
	freeze := "kardinal-freeze-agents-" + s.frozen.NS
	e.WaitChangeWindow(t, freeze, gateTimeout, "active", func(w *v1alpha1.ChangeWindow) bool { return w.Status.Active })
	out, err = e.KardinalV0120(t, s.frozen.NS, "--kubeconfig", fixer.Kubeconfig, "create", "bundle", shipped,
		"--image", scale.ImageRepo+":"+scale.Tag(shipped, 1))
	require.NoError(t, err, out)
	m := v081BundleCreated.FindStringSubmatch(out)
	require.Len(t, m, 2, "kardinal create bundle names the Bundle it created:\n%s", out)
	agentB := m[1]
	inFlight[s.frozen.NS+"/"+agentB] = shipped
	e.WaitStepMessage(t, s.frozen.NS, shipped, agentB, "test", "Pending", "frozen by "+freeze, promoteTimeout)

	for _, p := range approves {
		b := approveB[p]
		e.WaitGateReady(t, s.approve.NS, b, "prod", "two-approvers", false, "waiting for approvals: 0 of 2", gateTimeout)
		if p == approves[0] {
			out, err := e.KardinalV0120(t, s.approve.NS, "--kubeconfig", alice, "approve", b, "--env", "prod")
			require.NoError(t, err, out)
			e.WaitGateReady(t, s.approve.NS, b, "prod", "two-approvers", false, "waiting for approvals: 1 of 2 (alice@example.com)", gateTimeout)
		}
	}
	for _, b := range heldB {
		e.WaitGateReady(t, s.held.NS, b, "prod", "release-window", false, "= false", gateTimeout)
	}
	e.WaitStepState(t, s.verify.NS, byPerson, byPersonB, "qa", "Verified", promoteTimeout)
	out, err = e.KardinalV0120(t, s.verify.NS, "--kubeconfig", carol, "verify", byPerson, "qa", "--check", "explore",
		"--pass", "--summary", "checked before the upgrade")
	require.NoError(t, err, out)
	e.WaitGateReady(t, s.verify.NS, byPersonB, "live", "people-check", false, "1 of 2", gateTimeout)
	for k, p := range inFlight {
		if slices.Contains(reviews, p) {
			_, b, _ := strings.Cut(k, "/")
			e.WaitStepState(t, s.flow.NS, p, b, "prod", "WaitingForMerge", promoteTimeout)
		}
	}

	// Last, so they are mid-way at the upgrade: the rollback, the bakes and
	// the AgentRun. v0.12.0 rolls rollback-pre's prod back through test and
	// uat, with a bake on each.
	out, err = e.KardinalV0120(t, s.flow.NS, "rollback", rbPre, "--env", "prod")
	require.NoError(t, err, out)
	m = rbCreated.FindStringSubmatch(out)
	require.Len(t, m, 3, "kardinal rollback names the Bundle it created:\n%s", out)
	rbPreB := m[1]
	require.Equal(t, rbPre1, m[2], "the rollback restores rollback-pre's first version")
	inFlight[s.flow.NS+"/"+rbPreB] = rbPre
	for _, p := range bakes {
		add(s.flow, p, 1)
	}
	byRunB := cliBundle(s.verify, byRun, 1)
	inFlight[s.verify.NS+"/"+byRunB] = byRun
	var runBefore v1alpha1.AgentRun
	framework.Eventually(t, promoteTimeout, "the AgentRun of "+byRunB+" running", func(ctx context.Context) (bool, string) {
		runs, err := agentRuns(ctx, e, s.verify.NS, byRunB)
		if err != nil {
			return false, err.Error()
		}
		if len(runs) != 1 {
			return false, fmt.Sprintf("%d AgentRuns", len(runs))
		}
		runBefore = runs[0]
		return runs[0].Status.Phase == v1alpha1.AgentRunRunning, "phase " + runs[0].Status.Phase
	})
	framework.Eventually(t, 3*time.Minute, "a step HealthChecking (baking) in "+s.flow.NS, func(ctx context.Context) (bool, string) {
		var sl v1alpha1.PromotionStepList
		if err := e.Client.List(ctx, &sl, client.InNamespace(s.flow.NS)); err != nil {
			return false, err.Error()
		}
		for _, st := range sl.Items {
			if st.Status.State == "HealthChecking" {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d steps, none HealthChecking", len(sl.Items))
	})

	// ── The snapshot: it must hold everything the checks compare ────────
	before := s.old().snapshot(t)
	objsBefore := s.objects(t)
	bp, sp := before.phases()
	t.Logf("before the upgrade: Bundles %v, steps %v, %d Graphs, %d PRs, %d other objects", bp, sp, len(before.graphs), len(before.prs), len(objsBefore))
	for k := range inFlight {
		b, ok := before.bundles[k]
		require.True(t, ok, "%s exists at the upgrade", k)
		require.NotContains(t, []string{"Verified", "Failed", "Superseded", "Rejected"}, b.Status.Phase, "%s is in flight at the upgrade", k)
		_, ok = before.graphs[k]
		require.True(t, ok, "%s has a Graph at the upgrade", k)
	}
	for k, p := range inFlight {
		if slices.Contains(reviews, p) {
			ns, b, _ := strings.Cut(k, "/")
			_, ok := before.prs[ns+"/"+b+"/prod"]
			require.True(t, ok, "%s has its prod PR at the upgrade", k)
		}
	}
	for _, state := range []string{"Verified", "WaitingForMerge", "HealthChecking"} {
		require.NotZero(t, sp[state], "the estate has a %s step at the upgrade: %v", state, sp)
	}
	gates := map[string]string{} // bundle → the held gate's "Kind namespace/name"
	for _, held := range []struct {
		f        *scale.Fleet
		bundles  map[string]string
		env, tpl string
	}{
		{s.approve, approveB, "prod", "two-approvers"},
		{s.held, heldB, "prod", "release-window"},
		{s.verify, map[string]string{byPerson: byPersonB}, "live", "people-check"},
		{s.verify, map[string]string{byRun: byRunB}, "prod", "agent-check"},
	} {
		for _, b := range held.bundles {
			g, ok, err := e.GateInstance(ctx, held.f.NS, b, held.env, held.tpl)
			require.NoError(t, err)
			require.True(t, ok, "%s has its %s gate at the upgrade", b, held.tpl)
			require.False(t, g.Status.Ready, "%s's %s gate holds it at the upgrade: %s", b, held.tpl, framework.DescribeGate(g))
			gates[b] = "PolicyGate " + held.f.NS + "/" + g.Name
		}
	}
	var al v1alpha1.ApprovalList
	require.NoError(t, e.Client.List(ctx, &al, client.InNamespace(s.approve.NS)))
	var vl v1alpha1.VerdictList
	require.NoError(t, e.Client.List(ctx, &vl, client.InNamespace(s.verify.NS)))
	var alicesApproval, carolsVerdict string
	for _, a := range al.Items {
		if a.Spec.User == "alice@example.com" && a.Spec.Bundle == approveB[approves[0]] {
			alicesApproval = "Approval " + a.Namespace + "/" + a.Name
		}
	}
	for _, v := range vl.Items {
		if v.Spec.By == "carol@example.com" && v.Spec.Bundle == byPersonB {
			carolsVerdict = "Verdict " + v.Namespace + "/" + v.Name
		}
	}
	runNow, err := agentRuns(ctx, e, s.verify.NS, byRunB)
	require.NoError(t, err)
	require.Len(t, runNow, 1, "one AgentRun of %s at the upgrade", byRunB)
	require.Equal(t, v1alpha1.AgentRunRunning, runNow[0].Status.Phase, "the AgentRun is running at the upgrade")
	for _, k := range []string{alicesApproval, carolsVerdict, "AgentRun " + s.verify.NS + "/" + runBefore.Name, "ChangeWindow /" + freeze} {
		require.NotEmpty(t, k, "alice's Approval and carol's Verdict exist at the upgrade")
		require.Contains(t, objsBefore, k, "the snapshot has %s", k)
	}
	var stored v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: s.frozen.NS, Name: agentB}, &stored))
	require.NotNil(t, stored.Spec.Actor, "v0.12.0 stamped the agent's version")
	agentActor := *stored.Spec.Actor
	assert.Equal(t, v1alpha1.ActorAgent, agentActor.Kind)

	// The release before the upgrade: v0.12.0's, so each check below that
	// the upgrade happened fails on a cluster the upgrade skipped.
	rel := e.ExistingRelease(t, upgradeRelease, framework.ControllerNamespace)
	checkoutImage, checkoutChart := os.Getenv(framework.EnvImage), u0120CheckoutChart(t)
	old := u0120Release(t, e, rel)
	require.Equal(t, v0120Image, old.image, "the controller runs v0.12.0 before the upgrade")
	require.Equal(t, v0120Chart, old.chart, "the release is v0.12.0's chart before the upgrade")
	require.NotEqual(t, old.image, checkoutImage, "the checkout's image is not v0.12.0's")
	require.NotEqual(t, old.chart, checkoutChart, "the checkout's chart is not v0.12.0's")
	require.NotEmpty(t, old.pods, "v0.12.0's controller Pods")
	require.NotEmpty(t, old.applied, "the kardinal CRDs")
	for crd, applied := range old.applied {
		require.False(t, applied, "%s: nothing server-side applied it before the guide's step 2", crd)
	}
	kroBefore := kroPods(t, e)
	require.NotEmpty(t, kroBefore, "kro's Pods")
	upgradeStart := time.Now()

	// ── 2. Apply the new CRDs ───────────────────────────────────────────
	e.MustShell(t, framework.Fill(t, framework.GuideBlockFrom(t, "0.12.0", "helm show crds"), guideChartV0120, `"$KARDINAL_E2E_CHART"`))
	for crd := range old.applied {
		framework.Eventually(t, time.Minute, "CRD "+crd+" established", func(ctx context.Context) (bool, string) {
			u, err := e.Dynamic.Resource(crdGVR).Get(ctx, crd, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			return crdCondition(toCRD(t, u.Object), apiextensionsv1.Established), "not Established"
		})
	}

	// ── 3. Upgrade the chart ────────────────────────────────────────────
	upgradeCmd := strings.TrimRight(framework.Fill(t, framework.GuideBlockFrom(t, "0.12.0", "helm upgrade"), guideChartV0120, `"$KARDINAL_E2E_CHART"`), "\n") + ` \
  --set image.repository="${KARDINAL_E2E_IMAGE%:*}" --set image.tag="${KARDINAL_E2E_IMAGE##*:}" --set image.pullPolicy=Never
`
	up := e.Shell(t, upgradeCmd)
	require.Zero(t, up.Code, up.Output)
	rel.WaitRolledOut(t, 3*time.Minute)
	// Every Pod streamed from the upgrade on: the new controller's from its
	// start, and kro's.
	logs := invariants.CollectSince(t, e, invariants.Dir(t), upgradeStart)

	// The upgrade happened.
	now := u0120Release(t, e, rel)
	assert.Equal(t, checkoutImage, now.image, "the controller Deployment runs the checkout's image")
	assert.Equal(t, checkoutChart, now.chart, "the release is the checkout's chart")
	require.NotEmpty(t, now.pods, "the controller's Pods")
	for uid, image := range now.pods {
		assert.NotContains(t, old.pods, uid, "Pod %s is new", uid)
		assert.Equal(t, checkoutImage, image, "Pod %s runs the checkout's image", uid)
	}
	assert.Equal(t, len(old.applied), len(now.applied), "the same kardinal CRDs")
	for crd, applied := range now.applied {
		assert.True(t, applied, "%s: the guide's server-side apply owns it", crd)
	}
	kroNow := kroPods(t, e)
	assert.Equal(t, kroBefore, kroNow, "kro is not upgraded: the same Pods, no restart")

	// ── 4. Check the result ─────────────────────────────────────────────
	check := e.MustShell(t, framework.GuideBlockFrom(t, "0.12.0", "kardinal doctor"))
	assert.Regexp(t, `(?m)^\d+ check\(s\) passed$`, check, "kardinal doctor passes every check")
	for _, f := range s.fleets() {
		ns := f.NS
		framework.Eventually(t, 2*time.Minute, "every Pipeline of "+ns+" Ready", func(ctx context.Context) (bool, string) {
			var pl v1alpha1.PipelineList
			if err := e.Client.List(ctx, &pl, client.InNamespace(ns)); err != nil {
				return false, err.Error()
			}
			var not []string
			for _, p := range pl.Items {
				if c := meta.FindStatusCondition(p.Status.Conditions, "Ready"); c == nil || c.Status != metav1.ConditionTrue {
					not = append(not, fmt.Sprintf("%s: %v", p.Name, c))
				}
			}
			return len(not) == 0, strings.Join(not, "; ")
		})
	}

	// Right after the upgrade: every in-flight version is the same Bundle
	// with the same Graph, steps and PRs, and every gate instance, Approval,
	// Verdict, AgentRun and freeze is the same object.
	after := s.old().snapshot(t)
	for k := range inFlight {
		assert.Equal(t, before.bundles[k].UID, after.bundles[k].UID, "%s is the same Bundle", k)
		assert.Equal(t, before.graphs[k], after.graphs[k], "%s keeps its Graph (same uid)", k)
	}
	for k, st := range before.steps {
		ns, _, _ := strings.Cut(k, "/")
		if _, ok := inFlight[ns+"/"+st.Spec.BundleName]; !ok {
			continue
		}
		now, ok := after.steps[k]
		if assert.True(t, ok, "step %s survives the upgrade", k) {
			assert.Equal(t, st.UID, now.UID, "step %s is the same object", k)
		}
	}
	for k, n := range before.prs {
		ns, rest, _ := strings.Cut(k, "/")
		b, _, _ := strings.Cut(rest, "/")
		if _, ok := inFlight[ns+"/"+b]; ok {
			assert.Equal(t, n, after.prs[k], "%s keeps PR #%d", k, n)
		}
	}
	objsAfter := s.objects(t)
	for k, uid := range objsBefore {
		assert.Equal(t, uid, objsAfter[k], "%s is the same object after the upgrade", k)
	}

	// Gates that held keep holding.
	framework.Consistently(t, 30*time.Second, "the held gates still hold, and nothing reaches their environments", func(ctx context.Context) (bool, string) {
		for _, held := range []struct {
			f        *scale.Fleet
			bundles  map[string]string
			env, tpl string
		}{
			{s.approve, approveB, "prod", "two-approvers"},
			{s.held, heldB, "prod", "release-window"},
			{s.verify, map[string]string{byPerson: byPersonB}, "live", "people-check"},
		} {
			for p, b := range held.bundles {
				if st, ok, err := e.Step(ctx, held.f.NS, p, b, held.env); err != nil || ok {
					return false, fmt.Sprintf("%s %s: %v %v", b, held.env, st, err)
				}
				g, ok, err := e.GateInstance(ctx, held.f.NS, b, held.env, held.tpl)
				if err != nil || !ok || g.Status.Ready {
					return false, fmt.Sprintf("%s %s gate: ok=%v err=%v %v", b, held.tpl, ok, err, g)
				}
			}
		}
		return true, ""
	})
	e.StepHeld(t, s.frozen.NS, shipped, agentB, "test", "frozen by "+freeze, 10*time.Second)

	// ── Everything in flight finishes ───────────────────────────────────
	var reviewRepos []gitserver.Repo
	for _, p := range reviews {
		reviewRepos = append(reviewRepos, s.repos[s.flow.NS+"/"+p])
	}
	scale.Review(t, e.Git, nil, reviewRepos...)
	c := e.CLI(t)
	// bob approves both: with alice's approval from before the upgrade the
	// first gate is 2 of 2; the second still waits, for alice.
	for _, p := range approves {
		b := approveB[p]
		r := c.Exec(framework.CLIOptions{Kubeconfig: bob}, c.Args(s.approve.NS, "approve", b, "--env", "prod")...)
		require.Equal(t, 0, r.Code, r.Output())
		if p == approves[0] {
			e.WaitGateReady(t, s.approve.NS, b, "prod", "two-approvers", true, "(2 of 2)", gateTimeout)
			continue
		}
		e.WaitGateReady(t, s.approve.NS, b, "prod", "two-approvers", false, "waiting for approvals: 1 of 2 (bob@example.com)", gateTimeout)
		e.NoStep(t, s.approve.NS, p, b, "prod", 5*time.Second)
		r = c.Exec(framework.CLIOptions{Kubeconfig: alice}, c.Args(s.approve.NS, "approve", b, "--env", "prod")...)
		require.Equal(t, 0, r.Code, r.Output())
		e.WaitGateReady(t, s.approve.NS, b, "prod", "two-approvers", true, "(2 of 2)", gateTimeout)
	}
	// The false gate opens only by a person's override.
	me := whoAmI(t, e)
	for _, p := range helds {
		out := e.MustKardinal(t, s.held.NS, "override", p, "--stage", "prod", "--gate", "release-window", "--reason", "e2e: upgrade window over")
		assert.Contains(t, out, "Created by: "+me)
		g := e.WaitGateReady(t, s.held.NS, heldB[p], "prod", "release-window", true, "OVERRIDDEN by "+me+": e2e: upgrade window over", gateTimeout)
		assert.Equal(t, gates[heldB[p]], "PolicyGate "+s.held.NS+"/"+g.Name, "the override lands on the instance v0.12.0's Graph made")
	}
	// The verify gates: the AgentRun that was running finishes and counts;
	// dave's Verdict joins carol's.
	framework.Eventually(t, u0120AgentRunDelay+3*time.Minute, "the AgentRun of "+byRunB+" Succeeded", func(ctx context.Context) (bool, string) {
		runs, err := agentRuns(ctx, e, s.verify.NS, byRunB)
		if err != nil {
			return false, err.Error()
		}
		if len(runs) != 1 {
			return false, fmt.Sprintf("%d AgentRuns", len(runs))
		}
		if runs[0].UID != runBefore.UID {
			t.Fatalf("AgentRun %s (uid %s) is not the run that was running at the upgrade (%s, uid %s)",
				runs[0].Name, runs[0].UID, runBefore.Name, runBefore.UID)
		}
		return runs[0].Status.Phase == v1alpha1.AgentRunSucceeded, fmt.Sprintf("phase %s: %s", runs[0].Status.Phase, runs[0].Status.Message)
	})
	assert.True(t, runBefore.CreationTimestamp.Time.Before(upgradeStart), "the AgentRun was created before the upgrade began")
	e.WaitGateReady(t, s.verify.NS, byRunB, "prod", "agent-check", true, "verified: 1 of 1", gateTimeout)
	r := c.Exec(framework.CLIOptions{Kubeconfig: dave}, c.Args(s.verify.NS, "verify", byPerson, "qa", "--check", "explore",
		"--pass", "--summary", "checked after the upgrade")...)
	require.Equal(t, 0, r.Code, r.Output())
	e.WaitGateReady(t, s.verify.NS, byPersonB, "live", "people-check", true, "2 of 2", gateTimeout)
	// The freeze lifted: the agent's version goes on, with its actor.
	assert.Contains(t, c.Must(s.frozen.NS, "unfreeze", freeze), "Freeze "+freeze+" lifted")

	for k := range inFlight {
		ns, b, _ := strings.Cut(k, "/")
		e.WaitBundlePhase(t, ns, b, "Verified", 2*promoteTimeout)
	}
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: s.frozen.NS, Name: agentB}, &stored))
	assert.Equal(t, &agentActor, stored.Spec.Actor, "the agent's version keeps its actor")
	// The rollback v0.12.0 created went through test and uat.
	for _, env := range []string{"test", "uat", "prod"} {
		e.WaitStepState(t, s.flow.NS, rbPre, rbPreB, env, "Verified", time.Minute)
		assert.Contains(t, s.envFile(t, s.flow, rbPre, env), scale.Tag(rbPre, 1), "rollback-pre's %s runs its first version again", env)
	}

	// ── Nothing orphaned or duplicated ──────────────────────────────────
	end := s.old().snapshot(t)
	for k, n := range before.prs {
		ns, rest, _ := strings.Cut(k, "/")
		b, _, _ := strings.Cut(rest, "/")
		if _, ok := inFlight[ns+"/"+b]; ok {
			assert.Equal(t, n, end.prs[k], "%s finished with PR #%d", k, n)
		}
	}
	stepsPer := map[string][]string{}
	for k, st := range end.steps {
		ns, _, _ := strings.Cut(k, "/")
		key := ns + "/" + st.Spec.BundleName + "/" + st.Spec.Environment
		stepsPer[key] = append(stepsPer[key], st.Name)
	}
	for key, names := range stepsPer {
		assert.Len(t, names, 1, "one PromotionStep for %s: %v", key, names)
	}
	for _, f := range s.fleets() {
		graphs, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(f.NS).List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		per := map[string]int{}
		for _, g := range graphs.Items {
			per[g.GetLabels()["kardinal.io/bundle"]]++
		}
		for b, n := range per {
			assert.Equal(t, 1, n, "Bundle %s/%s has one Graph", f.NS, b)
		}
	}
	for k, repo := range s.repos {
		prs, err := e.Git.PullRequests(ctx, repo)
		require.NoError(t, err)
		heads := map[string][]int{}
		for _, pr := range prs {
			heads[pr.Head] = append(heads[pr.Head], pr.Number)
		}
		for h, nums := range heads {
			assert.Len(t, nums, 1, "%s: one PR for head %s, got %v", k, h, nums)
		}
	}
	for _, f := range s.fleets()[1:] {
		invariants.Check(t, e, invariants.Options{Namespace: f.NS, Targets: f.Targets(), Image: scale.ImageRepo,
			SeedTag: scale.SeedTag, Start: testStart, AllowEmpty: false})
	}
	u0120AllKinds(t, e, s)

	// ── A rollback after the upgrade ────────────────────────────────────
	// v0.12 rolls prod back through the environments upstream of it (test
	// and uat, docs/rollback.md#multi-environment-rollback); dr is not
	// upstream of prod and keeps the second version.
	out, err = e.Kardinal(t, s.flow.NS, "rollback", steady, "--env", "prod")
	require.NoError(t, err, out)
	m = rbCreated.FindStringSubmatch(out)
	require.Len(t, m, 3, "kardinal rollback names the Bundle it created:\n%s", out)
	rb := m[1]
	assert.Equal(t, steady1, m[2], "the rollback restores the first version")
	e.WaitBundlePhase(t, s.flow.NS, rb, "Verified", 2*promoteTimeout)
	for _, env := range []string{"test", "uat", "prod"} {
		e.WaitStepState(t, s.flow.NS, steady, rb, env, "Verified", time.Minute)
		assert.Contains(t, s.envFile(t, s.flow, steady, env), scale.Tag(steady, 1), "%s runs the first version again", env)
	}
	e.NoStep(t, s.flow.NS, steady, rb, "dr", 5*time.Second)
	assert.Contains(t, s.envFile(t, s.flow, steady, "dr"), scale.Tag(steady, 2), "dr keeps the second version")

	// ── The CLI, the UI API and a new version ───────────────────────────
	res := c.Run("", "get", "pipelines", "-A")
	require.Equal(t, 0, res.Code, res.Output())
	for _, f := range s.fleets() {
		assert.Contains(t, res.Stdout, f.NS, "kardinal get pipelines -A lists %s", f.NS)
	}
	spec := framework.LoadOpenAPI(t)
	ui := mainUI(t, e)
	got := ui.Get(t, uiAPI+"/pipelines")
	require.Equal(t, 200, got.Status, got.String())
	assert.Empty(t, spec.Validate(spec.ResponseSchema(t, "GET", "/api/v1/ui/pipelines", 200), []byte(got.Body)))
	got = ui.Get(t, uiAPI+"/gates")
	require.Equal(t, 200, got.Status, got.String())
	assert.Empty(t, spec.Validate(spec.ResponseSchema(t, "GET", "/api/v1/ui/gates", 200), []byte(got.Body)))
	newB := bundle(s.flow, fresh, 1)
	e.WaitBundlePhase(t, s.flow.NS, newB, "Verified", promoteTimeout)

	// ── The flow namespace, the controller's and kro's logs ─────────────
	invariants.Check(t, e, invariants.Options{Namespace: s.flow.NS, Targets: s.flow.Targets(), Image: scale.ImageRepo,
		SeedTag: scale.SeedTag, Start: testStart, AllowEmpty: false, Logs: logs,
		Skip: map[string]string{"env-content": "the rollbacks restore older versions on purpose; their content is checked above"}})
	logs.Stop()
	lines, kroErrs := kroErrors(t, invariants.Dir(t))
	t.Logf("kro: %d log lines since the upgrade", lines)
	assert.NotZero(t, lines, "kro's log lines have a level the check reads")
	assert.Empty(t, kroErrs, "kro logged no error from the upgrade on")
	assert.Equal(t, kroBefore, kroPods(t, e), "kro's Pods did not restart")
}

// u0120AllKinds reads every stored object of every kardinal kind in the
// estate with this release's types (a new required field or a changed type
// fails the decode), and checks the admission policies admit a label on the
// objects users write: Pipelines, Bundles and gate templates.
func u0120AllKinds(t *testing.T, e *framework.Env, s *u0120Estate) {
	t.Helper()
	ctx := context.Background()
	u0110AllKinds(t, e, s.old())
	lists := []client.ObjectList{&v1alpha1.AgentRunList{}, &v1alpha1.VerdictList{}}
	for _, f := range s.fleets() {
		for _, l := range lists {
			require.NoError(t, e.Client.List(ctx, l, client.InNamespace(f.NS)), "list %T in %s with this release's types", l, f.NS)
		}
	}
	var ids v1alpha1.AgentIdentityList
	require.NoError(t, e.Client.List(ctx, &ids))
	var tmpls v1alpha1.StepTemplateList
	require.NoError(t, e.Client.List(ctx, &tmpls))
	var idx v1alpha1.AgentIndexList
	require.NoError(t, e.Client.List(ctx, &idx))
}
