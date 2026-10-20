//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
const guideChartV0120 = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version <version>"

// u012From is the v0.12 release an upgrade-v012x suite upgrades from: the
// prefix of its pins in hack/e2e/versions.env (V0120 for v0.12.0), which its
// component installs it by, the CLI that runs it (Env.KardinalV0120, …) and
// the chart reference its "Upgrading from" guide names. extra, when set,
// is a case of the suite's own: it runs before the estate's versions start,
// under the release, and returns the checks to make after the upgrade, once
// the held gates are checked.
type u012From struct {
	pins       string
	cli        func(e *framework.Env, t *testing.T, ns string, args ...string) (string, error)
	guideChart string
	// guide is the version of the "Upgrading from v<guide>" section the
	// suite follows, when not the release's own (v0.12.1 follows v0.12.0's).
	guide string
	extra func(t *testing.T, e *framework.Env, from u012From) (after func())
}

// The kinds this release adds: their CRDs arrive with the guide's step 2.
var u012NewCRDs = []string{"promotionreceipts.kardinal.io", "changerequests.kardinal.io"}

// u012Pins is the release from installs: its chart version ("0.12.0"),
// controller image and helm.sh/chart label, read from hack/e2e/versions.env.
func u012Pins(t *testing.T, from u012From) (version, image, chart string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(framework.RepoRoot(t), "hack", "e2e", "versions.env"))
	require.NoError(t, err)
	pins := map[string]string{}
	for _, l := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.HasPrefix(k, from.pins+"_") {
			pins[strings.TrimPrefix(k, from.pins+"_")] = v
		}
	}
	version, image = pins["CHART_VERSION"], pins["CONTROLLER_IMAGE"]
	require.NotEmpty(t, version, "hack/e2e/versions.env %s_CHART_VERSION", from.pins)
	require.NotEmpty(t, image, "hack/e2e/versions.env %s_CONTROLLER_IMAGE", from.pins)
	return version, image, "kardinal-promoter-" + version
}

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
// Deployment's image and chart label, its Pods' images by uid, the helm
// release's newest revision and its status, and the kardinal CRDs a
// server-side apply (the guide's step 2) owns fields of.
type u0120Running struct {
	image, chart string
	pods         map[types.UID]string
	revision     int
	status       string
	applied      map[string]bool // CRD name → kubectl owns fields of it by Apply
}

func u0120Release(t *testing.T, e *framework.Env, rel *framework.Release) u0120Running {
	t.Helper()
	r := u0120Running{pods: map[types.UID]string{}, applied: map[string]bool{}}
	// helm keeps each revision in a Secret labelled with its number and status.
	var secrets corev1.SecretList
	require.NoError(t, e.Client.List(context.Background(), &secrets, client.InNamespace(rel.Namespace),
		client.MatchingLabels{"owner": "helm", "name": rel.Name}))
	for _, sec := range secrets.Items {
		n, err := strconv.Atoi(sec.Labels["version"])
		require.NoError(t, err, "helm release Secret %s", sec.Name)
		if n > r.revision {
			r.revision, r.status = n, sec.Labels["status"]
		}
	}
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

// u012RunsIn waits until pipeline's status names bundle as the version that
// runs in env. That is what kardinal verify <pipeline> <env> verifies
// (runningBundle in cmd/kardinal/cmd/verify.go), and the Pipeline reconciler
// writes it after the step in env is Verified: a verify run right after
// WaitStepState can find no version there yet.
func u012RunsIn(t *testing.T, e *framework.Env, ns, pipeline, env, bundle string) {
	t.Helper()
	framework.Eventually(t, time.Minute, fmt.Sprintf("Pipeline %s/%s names %s in %s", ns, pipeline, bundle, env), func(ctx context.Context) (bool, string) {
		var p v1alpha1.Pipeline
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: pipeline}, &p); err != nil {
			return false, err.Error()
		}
		for _, s := range p.Status.Environments {
			if s.Name == env {
				return s.Bundle == bundle, fmt.Sprintf("status.environments[%s].bundle is %q", env, s.Bundle)
			}
		}
		return false, fmt.Sprintf("status.environments has no %s", env)
	})
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

// kroErrors reads the whole log of each kro container, with the kubelet's
// timestamps (kind nodes share the host's clock; kro's Pods do not restart,
// which the test checks). leveled counts the lines kroLevel reads, over the
// whole log: kro logs its start at info and little after it, so none means
// another format, which the check cannot read. errs are the lines at error
// level or above written from since on, but the benign ones
// (invariants.Benign) and the ones expected (expected, counted in allowed).
func kroErrors(t *testing.T, e *framework.Env, since time.Time, expected func(at time.Time, line string) bool) (leveled, allowed int, errs []string) {
	t.Helper()
	ctx := context.Background()
	var pods corev1.PodList
	require.NoError(t, e.Client.List(ctx, &pods, client.InNamespace("kro-system"),
		client.MatchingLabels{"app.kubernetes.io/name": "kro"}))
	require.NotEmpty(t, pods.Items, "kro's Pods")
	for _, p := range pods.Items {
		for _, c := range p.Spec.Containers {
			raw, err := e.Kube.CoreV1().Pods(p.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{
				Container: c.Name, Timestamps: true,
			}).DoRaw(ctx)
			require.NoError(t, err, "the log of %s/%s", p.Name, c.Name)
		line:
			for _, l := range strings.Split(string(raw), "\n") {
				ts, text, ok := strings.Cut(l, " ")
				m := kroLevel.FindStringSubmatch(text)
				if !ok || m == nil {
					continue
				}
				leveled++
				at, err := time.Parse(time.RFC3339Nano, ts)
				require.NoError(t, err, "kubelet timestamp of %q", l)
				if at.Before(since) || !slices.Contains([]string{"ERROR", "DPANIC", "PANIC", "FATAL"}, m[1]) {
					continue
				}
				for _, re := range invariants.Benign {
					if re.MatchString(text) {
						continue line
					}
				}
				if expected != nil && expected(at, text) {
					allowed++
					continue
				}
				errs = append(errs, p.Name+"/"+c.Name+": "+text)
			}
		}
	}
	return leveled, allowed, errs
}

// u012HealthRefForbidden matches kro failing to read a v0.12 health ref node
// ("health" + the environment, upper first) because the new chart deleted
// the reader ClusterRole: docs/upgrade.md's "Between the chart upgrade and
// that start, kro cannot read those nodes" (#1283).
var u012HealthRefForbidden = regexp.MustCompile(`apply \\"health[A-Z][A-Za-z0-9]*\\" \(ref\): get external ref .* is forbidden`)

// u012HealthWatchForbidden matches the same gap in kro's watch manager: its
// informer for a kind the removed health ref nodes read (the kinds the
// deleted reader ClusterRole and the old kro-watch rules granted) cannot
// list or watch it at the cluster scope any more, while Graphs still have
// those nodes. Seen in one of four upgrade jobs (e2e-live 39476864470,
// upgrade-v0121 on 1.35): three lines, 5 to 9 seconds before the nodes were
// gone (#1283).
var u012HealthWatchForbidden = regexp.MustCompile(`dynamic-controller\.watch-manager\tWatch error\t\{"gvr": "(apps/v1, Resource=deployments|argoproj\.io/v1alpha1, Resource=(applications|rollouts)|kustomize\.toolkit\.fluxcd\.io/v1, Resource=kustomizations|flagger\.app/v1beta1, Resource=canaries)", "error": "(failed to list \*v1\.PartialObjectMetadata: )?[a-z.]+ is forbidden: User \\"system:serviceaccount:[^\\"]+\\" cannot (list|watch) resource `)

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
//   - this release's new kinds arrive cleanly: the PromotionReceipt and
//     ChangeRequest CRDs are applied by the guide and established, nothing
//     creates either, the receipts admission policy refuses a forged
//     receipt, and the auditor ClusterRole exists; the controller removes
//     the reader RoleBindings and health ref nodes v0.12 made (#1283);
//   - every in-flight version is the same Bundle (uid) with the same Graph,
//     PromotionSteps and PR numbers; every gate instance, Approval, Verdict,
//     AgentRun and freeze is the same object;
//   - gates that held a version keep holding it (no step in their
//     environment for 30s): the two-approver gate until bob's approval
//     completes alice's (2 of 2); the false gate until its template is
//     edited, which now reaches the version v0.12 started (#1360: the
//     instance takes the template's generation; v0.12's unstamped copy is
//     the baseline, so no GateChanged record);
//     the verify gates until the AgentRun that was running finishes (the
//     same run, created before the upgrade) and its Verdict counts, and
//     until a second person's Verdict joins the one recorded before; the
//     freeze holds the agent's version until kardinal unfreeze, and the new
//     controller records status.frozenBy on the step v0.12 made (#1974);
//   - every in-flight version reaches Verified with its PRs; the rollback
//     v0.12.0 created goes through test and uat, as v0.12 rollbacks do;
//   - no Bundle has two Graphs, no (Bundle, environment) two steps, no PR
//     head branch two PRs; each namespace passes the invariants; every
//     stored object reads with this release's types;
//   - a rollback of prod after the upgrade restores the earlier version in
//     prod and in test and uat, the environments upstream of it (the
//     estate's images are pinned by tag, so even this release's target-only
//     rollback, #1311, goes through them), and leaves dr, beside prod, as it
//     was;
//   - kardinal get pipelines -A, the UI API and a new version work;
//   - neither the controller nor kro restarted, and neither logged an
//     unexpected error from the upgrade on.
//
// Covers UPG-V0120-01, UPG-V0120-02, UPG-V0120-03, UPG-V0120-04, UPG-V0120-05.
func TestUpgradeV0120_Estate(t *testing.T) {
	u012Upgrade(t, u012From{pins: "V0120", cli: (*framework.Env).KardinalV0120, guideChart: guideChartV0120})
}

// u012Upgrade is TestUpgradeV0120_Estate's estate, upgrade and checks, from
// the v0.12 release from (TestUpgradeV0121_Estate runs it from v0.12.1).
func u012Upgrade(t *testing.T, from u012From) {
	testStart := time.Now()
	e := framework.New(t)
	fromVersion, fromImage, fromChart := u012Pins(t, from)
	guide := cmp.Or(from.guide, fromVersion)
	t.Logf("upgrading from v%s (%s, %s) by \"Upgrading from v%s\"", fromVersion, fromImage, fromChart, guide)
	ctx := context.Background()
	t.Logf("Kubernetes 1.%d", serverMinor(t, e))
	seed, err := scale.Seed()
	require.NoError(t, err)
	s := &u0120Estate{e: e, repos: map[string]gitserver.Repo{}}
	newFleet := func(name string) *scale.Fleet { return scale.NewFleet(t, e, scale.NewRNG(seed, name)) }
	s.flow, s.approve, s.held, s.verify, s.frozen = newFleet("flow"), newFleet("approve"), newFleet("held"), newFleet("verify"), newFleet("frozen")
	bundle := func(f *scale.Fleet, p string, i int) string { return f.MustCreateBundle(t, p, scale.Tag(p, i)).Name }
	// cliBundle creates version i of p with the released CLI, which stamps
	// the test's user as its author: a verify gate (excludeAuthor, the
	// default) holds a version with no verified author.
	cliBundle := func(f *scale.Fleet, p string, i int) string {
		out, err := from.cli(e, t, f.NS, "create", "bundle", p, "--image", scale.ImageRepo+":"+scale.Tag(p, i))
		require.NoError(t, err, out)
		m := v081BundleCreated.FindStringSubmatch(out)
		require.Len(t, m, 2, "kardinal create bundle names the Bundle it created:\n%s", out)
		return m[1]
	}
	three := u0120Envs("test", "uat", "prod")

	// ── The estate, under the release it upgrades from ──────────────────
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
	var extraAfter func()
	if from.extra != nil {
		extraAfter = from.extra(t, e, from)
	}

	// Versions that finish before the upgrade: two on steady (the rollback after
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
	out, err := from.cli(e, t, s.frozen.NS, "freeze", "--agents", "--reason", "INC-0120 agents paused", "--yes")
	require.NoError(t, err, out)
	freeze := "kardinal-freeze-agents-" + s.frozen.NS
	e.WaitChangeWindow(t, freeze, gateTimeout, "active", func(w *v1alpha1.ChangeWindow) bool { return w.Status.Active })
	out, err = from.cli(e, t, s.frozen.NS, "--kubeconfig", fixer.Kubeconfig, "create", "bundle", shipped,
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
			out, err := from.cli(e, t, s.approve.NS, "--kubeconfig", alice, "approve", b, "--env", "prod")
			require.NoError(t, err, out)
			e.WaitGateReady(t, s.approve.NS, b, "prod", "two-approvers", false, "waiting for approvals: 1 of 2 (alice@example.com)", gateTimeout)
		}
	}
	for _, b := range heldB {
		e.WaitGateReady(t, s.held.NS, b, "prod", "release-window", false, "= false", gateTimeout)
	}
	e.WaitStepState(t, s.verify.NS, byPerson, byPersonB, "qa", "Verified", promoteTimeout)
	u012RunsIn(t, e, s.verify.NS, byPerson, "qa", byPersonB)
	out, err = from.cli(e, t, s.verify.NS, "--kubeconfig", carol, "verify", byPerson, "qa", "--check", "explore",
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
	// the AgentRun. The release rolls rollback-pre's prod back through test and
	// uat, with a bake on each.
	out, err = from.cli(e, t, s.flow.NS, "rollback", rbPre, "--env", "prod")
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
	require.NotNil(t, stored.Spec.Actor, "v%s stamped the agent's version", fromVersion)
	agentActor := *stored.Spec.Actor
	assert.Equal(t, v1alpha1.ActorAgent, agentActor.Kind)

	// The release before the upgrade: from's, so each check below that
	// the upgrade happened fails on a cluster the upgrade skipped.
	rel := e.ExistingRelease(t, upgradeRelease, framework.ControllerNamespace)
	checkoutImage, checkoutChart := os.Getenv(framework.EnvImage), u0120CheckoutChart(t)
	old := u0120Release(t, e, rel)
	require.Equal(t, fromImage, old.image, "the controller runs v%s before the upgrade", fromVersion)
	require.Equal(t, fromChart, old.chart, "the release is v%s's chart before the upgrade", fromVersion)
	require.NotEqual(t, old.image, checkoutImage, "the checkout's image is not v%s's", fromVersion)
	require.NotEmpty(t, old.pods, "v%s's controller Pods", fromVersion)
	require.Equal(t, "deployed", old.status, "helm revision %d of the release is deployed", old.revision)
	require.NotEmpty(t, old.applied, "the kardinal CRDs")
	for crd, applied := range old.applied {
		require.False(t, applied, "%s: nothing server-side applied it before the guide's step 2", crd)
	}
	for _, crd := range u012NewCRDs {
		require.NotContains(t, old.applied, crd, "v%s has no %s", fromVersion, crd)
	}
	// v0.12 binds the Graph ServiceAccount to the reader role in each
	// namespace its health ref nodes read: here, each estate namespace.
	readerRole := framework.ChartFullname(upgradeRelease) + "-graph-reader"
	for _, f := range s.fleets() {
		_, err := e.Kube.RbacV1().RoleBindings(f.NS).Get(ctx, readerRole+"-"+f.NS, metav1.GetOptions{})
		require.NoError(t, err, "v%s has a reader RoleBinding in %s", fromVersion, f.NS)
	}
	kroBefore := kroPods(t, e)
	require.NotEmpty(t, kroBefore, "kro's Pods")
	upgradeStart := time.Now()

	// ── 2. Apply the new CRDs ───────────────────────────────────────────
	e.MustShell(t, framework.Fill(t, framework.GuideBlockFrom(t, guide, "helm show crds"), from.guideChart, `"$KARDINAL_E2E_CHART"`))
	for _, crd := range append(slices.Collect(maps.Keys(old.applied)), u012NewCRDs...) {
		framework.Eventually(t, time.Minute, "CRD "+crd+" established", func(ctx context.Context) (bool, string) {
			u, err := e.Dynamic.Resource(crdGVR).Get(ctx, crd, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			return crdCondition(toCRD(t, u.Object), apiextensionsv1.Established), "not Established"
		})
	}

	// ── 3. Upgrade the chart ────────────────────────────────────────────
	upgradeCmd := strings.TrimRight(framework.Fill(t, framework.GuideBlockFrom(t, guide, "helm upgrade"), from.guideChart, `"$KARDINAL_E2E_CHART"`), "\n") + ` \
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
	// The chart version alone does not tell a skipped helm upgrade (a patch
	// release's checkout may still carry the version it upgrades from):
	// the upgrade is a new deployed revision.
	assert.Equal(t, old.revision+1, now.revision, "the guide's helm upgrade made one new revision")
	assert.Equal(t, "deployed", now.status, "helm revision %d of the release is deployed", now.revision)
	require.NotEmpty(t, now.pods, "the controller's Pods")
	for uid, image := range now.pods {
		assert.NotContains(t, old.pods, uid, "Pod %s is new", uid)
		assert.Equal(t, checkoutImage, image, "Pod %s runs the checkout's image", uid)
	}
	for crd := range old.applied {
		assert.Contains(t, now.applied, crd, "%s is still served", crd)
	}
	for _, crd := range u012NewCRDs {
		assert.Contains(t, now.applied, crd, "the upgrade installs %s", crd)
	}
	assert.Len(t, now.applied, len(old.applied)+len(u012NewCRDs), "the kardinal CRDs: v%s's and this release's new kinds", fromVersion)
	for crd, applied := range now.applied {
		assert.True(t, applied, "%s: the guide's server-side apply owns it", crd)
	}
	kroNow := kroPods(t, e)
	assert.Equal(t, kroBefore, kroNow, "kro is not upgraded: the same Pods, no restart")

	// ── 4. Check the result ─────────────────────────────────────────────
	check := e.MustShell(t, framework.GuideBlockFrom(t, guide, "kardinal doctor"))
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

	// #1283: the new controller removes the reader RoleBindings and the
	// health ref nodes v0.12 made, in place.
	for _, f := range s.fleets() {
		ns := f.NS
		framework.Eventually(t, 2*time.Minute, "the reader RoleBinding of "+ns+" deleted", func(ctx context.Context) (bool, string) {
			_, err := e.Kube.RbacV1().RoleBindings(ns).Get(ctx, readerRole+"-"+ns, metav1.GetOptions{})
			return apierrors.IsNotFound(err), fmt.Sprintf("reader RoleBinding: %v", err)
		})
		framework.Eventually(t, 2*time.Minute, "the Graphs of "+ns+" without health ref nodes", func(ctx context.Context) (bool, string) {
			list, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, err.Error()
			}
			var left []string
			for _, g := range list.Items {
				nodes, _, _ := unstructured.NestedSlice(g.Object, "spec", "nodes")
				for _, n := range nodes {
					// v0.12's health ref node is "health" + the environment's
					// slug, upper first ("healthUat").
					if id, _ := n.(map[string]interface{})["id"].(string); len(id) > 6 && strings.HasPrefix(id, "health") && id[6] >= 'A' && id[6] <= 'Z' {
						left = append(left, g.GetName()+"/"+id)
					}
				}
			}
			return len(left) == 0, strings.Join(left, ", ")
		})
	}
	refsGone := time.Now()
	t.Logf("the health ref nodes and reader RoleBindings were gone %s after the upgrade started", refsGone.Sub(upgradeStart).Round(time.Second))

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

	// The new kinds are installed and inert: no receipt, no change request,
	// and only kardinal may write a receipt.
	var receipts v1alpha1.PromotionReceiptList
	require.NoError(t, e.Client.List(ctx, &receipts))
	assert.Empty(t, receipts.Items, "receipts are off by default: none is created")
	var crs v1alpha1.ChangeRequestList
	require.NoError(t, e.Client.List(ctx, &crs))
	assert.Empty(t, crs.Items, "no gate reads a change request: none is created")
	sum := sha256.Sum256([]byte("{}"))
	err = e.Client.Create(ctx, &v1alpha1.PromotionReceipt{
		ObjectMeta: metav1.ObjectMeta{Name: "forged", Namespace: s.flow.NS},
		Spec:       v1alpha1.PromotionReceiptSpec{Statement: "{}", StatementDigest: "sha256:" + hex.EncodeToString(sum[:])},
	})
	require.Error(t, err, "a forged receipt is refused")
	assert.Contains(t, err.Error(), "only kardinal writes PromotionReceipts")
	_, err = e.Kube.RbacV1().ClusterRoles().Get(ctx, framework.ChartFullname(upgradeRelease)+"-auditor", metav1.GetOptions{})
	assert.NoError(t, err, "the chart's auditor ClusterRole")

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
	// The new controller records the freeze on the step the release made (#1974).
	e.WaitStep(t, s.frozen.NS, shipped, agentB, "test", gateTimeout, "status.frozenBy names the freeze",
		func(ps *v1alpha1.PromotionStep) (bool, string) {
			if ps.Status.FrozenBy == nil {
				return false, "frozenBy unset"
			}
			return ps.Status.FrozenBy.Name == freeze, fmt.Sprintf("%+v", *ps.Status.FrozenBy)
		})
	if extraAfter != nil {
		extraAfter()
	}

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
	// #1360, #2271: an edit of the false gate reaches the versions the
	// release started. The upgrade rendered their Graphs again once, which
	// stamped the instances the release made, with no stamp, with the
	// template's revision: the baseline the edit is measured from. The rule
	// did not change at the upgrade, so it recorded no GateChanged; the edit
	// writes one per held version, from that revision to the edited one.
	var tmpl v1alpha1.PolicyGate
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: s.held.NS, Name: "release-window"}, &tmpl))
	for _, b := range heldB {
		g, ok, err := e.GateInstance(ctx, s.held.NS, b, "prod", "release-window")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, fmt.Sprint(tmpl.Generation), g.Annotations["kardinal.io/gate-template-generation"],
			"the upgrade stamped the instance v%s made with the template's revision", fromVersion)
	}
	heldChanges := func(ctx context.Context) (map[string][]v1alpha1.AuditEventSpec, error) {
		var evs v1alpha1.AuditEventList
		if err := e.Client.List(ctx, &evs, client.InNamespace(s.held.NS), client.MatchingLabels{"kardinal.io/action": "GateChanged"}); err != nil {
			return nil, err
		}
		out := map[string][]v1alpha1.AuditEventSpec{}
		for _, ev := range evs.Items {
			out[ev.Spec.BundleName] = append(out[ev.Spec.BundleName], ev.Spec)
		}
		return out, nil
	}
	changed, err := heldChanges(ctx)
	require.NoError(t, err)
	assert.Empty(t, changed, "the upgrade changed no rule, and recorded no GateChanged")
	gen := editGate(t, e, s.held.NS, "release-window", "true")
	for _, b := range heldB {
		g := e.WaitGateReady(t, s.held.NS, b, "prod", "release-window", true, "= true", gateTimeout)
		assert.Equal(t, fmt.Sprint(gen), g.Annotations["kardinal.io/gate-template-generation"], "the instance names the edited revision")
		assert.Equal(t, gates[b], "PolicyGate "+s.held.NS+"/"+g.Name, "the edit updates the instance v%s's Graph made", fromVersion)
	}
	framework.Eventually(t, gateTimeout, "one GateChanged record per held version, from the upgrade's stamp to the edit", func(ctx context.Context) (bool, string) {
		got, err := heldChanges(ctx)
		if err != nil {
			return false, err.Error()
		}
		for _, b := range heldB {
			recs := got[b]
			if len(recs) != 1 || len(recs[0].Gates) != 1 || recs[0].Gates[0].Template != "release-window" ||
				recs[0].Gates[0].OldGeneration != tmpl.Generation || recs[0].Gates[0].NewGeneration != gen {
				return false, fmt.Sprintf("%s: %+v", b, recs)
			}
		}
		return len(got) == len(heldB), fmt.Sprintf("GateChanged records %+v", got)
	})
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
	u012RunsIn(t, e, s.verify.NS, byPerson, "qa", byPersonB)
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
	// The rollback the release created went through test and uat.
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
	// The one error the guide expects (#1283): kro cannot read a health ref
	// node between the chart upgrade and the new controller's removal of
	// those nodes. Only that read, and only before the nodes were gone.
	leveled, allowed, kroErrs := kroErrors(t, e, upgradeStart, func(at time.Time, line string) bool {
		return at.Before(refsGone) && (u012HealthRefForbidden.MatchString(line) || u012HealthWatchForbidden.MatchString(line))
	})
	t.Logf("kro: %d log lines with a level; %d errors read a health ref node before the controller removed them (#1283)", leveled, allowed)
	assert.NotZero(t, leveled, "kro's log lines have a level the check reads")
	assert.Empty(t, kroErrs, "kro logged no other error from the upgrade on")
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
	lists := []client.ObjectList{&v1alpha1.AgentRunList{}, &v1alpha1.VerdictList{},
		&v1alpha1.PromotionReceiptList{}, &v1alpha1.ChangeRequestList{}}
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
