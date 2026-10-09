//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// v081BundleCreated is the v0.8.1 CLI's create bundle output.
var v081BundleCreated = regexp.MustCompile(`Bundle (\S+) created for pipeline`)

const (
	// guideChart is the chart the upgrade guide's commands name; the test runs
	// them on the checkout's chart (KARDINAL_E2E_CHART).
	guideChart = "oci://ghcr.io/pnz1990/charts/kardinal-promoter --version 0.9.0"
	// legacyLongGate is a PolicyGate a user created under v0.8.1 with a name
	// the new CRD rejects; legacyShortGate is the name the guide's copy gives it.
	legacyLongGate  = "legacy-gate-name-longer-than-the-sixty-three-character-name-limit"
	legacyShortGate = "legacy-short"
	// upgradeRelease is the v0.8.1 release kardinal-v081.sh installed.
	upgradeRelease = "kardinal-promoter"
)

// upgrade is the state TestUpgrade_FromV081 builds on v0.8.1.
type upgrade struct {
	e    *framework.Env
	ns   string
	repo gitserver.Repo
	// main is the Pipeline (named after the namespace, so v0.8.1's default
	// Argo CD Applications <pipeline>-<env> are the test's own) with test
	// (auto) and prod (pr-review).
	main string
	// cliPaused was paused with the v0.8.1 CLI and specPaused with
	// spec.paused alone; each has one environment, test.
	cliPaused, specPaused string
}

// app is the upgrade's repo as assertEnvAt reads it.
func (u *upgrade) app() *app { return &app{e: u.e, ns: u.ns, repo: u.repo} }

// v081Pipeline is a v0.8.1 Pipeline over the test repo with argocd health on
// v0.8.1's default Applications. envs are name=path-env pairs; approval maps
// an environment to pr-review.
func (u *upgrade) v081Pipeline(name string, envs [][2]string, approval map[string]string) string {
	y := u.pipelineHead(name, "")
	for _, env := range envs {
		mode := approval[env[0]]
		if mode == "" {
			mode = "auto"
		}
		y += fmt.Sprintf(`  - name: %s
    path: %s
    approval: %s
    update:
      strategy: kustomize
    health:
      type: argocd
      timeout: 3m
`, env[0], fixtures.Path(env[1]), mode)
	}
	return y
}

// pipelineHead is a Pipeline over the test repo up to its environments key;
// extra is more of spec, indented two spaces.
func (u *upgrade) pipelineHead(name, extra string) string {
	return fmt.Sprintf(`apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: %s
spec:
  git:
    url: %s
    branch: %s
    secretRef:
      name: %s
%s  environments:
`, name, u.repo.CloneURL, u.repo.Branch, framework.GitSecretName, extra)
}

// legacyEnv is an environment of a legacy Pipeline; extra is more of it,
// indented four spaces.
func legacyEnv(name, extra string) string {
	return fmt.Sprintf(`  - name: %s
    path: %s
    update:
      strategy: kustomize
%s`, name, fixtures.Path("test"), extra)
}

// legacyObjects are objects v0.8.1 accepted and the new CRDs reject or
// report, one per finding of the guide's step 1, plus legacy-clean, which has
// none. None has a Bundle.
func (u *upgrade) legacyObjects() string {
	objs := []string{
		u.pipelineHead("legacy-steps", "") + legacyEnv("test", "    steps:\n    - uses: git-clone\n"),
		u.pipelineHead("legacy-autorollback", "") + legacyEnv("test", "    autoRollback:\n      failureThreshold: 3\n"),
		u.pipelineHead("legacy-reserved", "") + legacyEnv("graph", ""),
		u.pipelineHead("legacy-policygates", "  policyGates:\n  - name: upgrade-hold\n") + legacyEnv("test", ""),
		u.pipelineHead("legacy-badname", "") + legacyEnv("Test", ""),
		u.pipelineHead("legacy-dupenv", "") + legacyEnv("test", "") + legacyEnv("test", ""),
		u.pipelineHead("legacy-shard", "") + legacyEnv("test", "    shard: eu\n"),
		u.pipelineHead("legacy-clean", "") + legacyEnv("test", ""),
		`apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: legacy-selector
spec:
  expression: "true"
  selector:
    matchLabels:
      e2e.kardinal.io/none: "true"
`,
		fmt.Sprintf(`apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: %s
spec:
  expression: "true"
`, legacyLongGate),
	}
	return strings.Join(objs, "---\n")
}

// v081Bundle creates a Bundle of image version with the v0.8.1 CLI and
// returns its name.
func (u *upgrade) v081Bundle(t *testing.T, pipeline, version string) string {
	t.Helper()
	out, err := u.e.KardinalV081(t, u.ns, "create", "bundle", pipeline, "--image", fixtures.Image+":"+version)
	require.NoError(t, err, out)
	m := v081BundleCreated.FindStringSubmatch(out)
	require.NotNil(t, m, out)
	return m[1]
}

// step is the Bundle's PromotionStep for env.
func (u *upgrade) step(t *testing.T, pipeline, bundle, env string) *v1alpha1.PromotionStep {
	t.Helper()
	ps, ok, err := u.e.Step(context.Background(), u.ns, pipeline, bundle, env)
	require.NoError(t, err)
	require.True(t, ok, "no %s step for %s", env, bundle)
	return ps
}

// get reads ns/name into obj.
func (u *upgrade) get(t *testing.T, name string, obj client.Object) client.Object {
	t.Helper()
	require.NoError(t, u.e.Client.Get(context.Background(), types.NamespacedName{Namespace: u.ns, Name: name}, obj))
	return obj
}

// pipeline is the Pipeline name.
func (u *upgrade) pipeline(t *testing.T, name string) *v1alpha1.Pipeline {
	t.Helper()
	return u.get(t, name, &v1alpha1.Pipeline{}).(*v1alpha1.Pipeline)
}

// findings runs the guide's step 1 finder and returns its lines about the
// test namespace, sorted.
func (u *upgrade) findings(t *testing.T, finder string) []string {
	t.Helper()
	out := u.e.MustShell(t, finder)
	lines := []string{}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "pipeline "+u.ns+"/") || strings.HasPrefix(l, "policygate "+u.ns+"/") {
			lines = append(lines, l)
		}
	}
	sort.Strings(lines)
	return lines
}

// krocodileGraphs lists v0.8.1's Graphs in the namespace: their finalizers by
// the Bundle they belong to.
func (u *upgrade) krocodileGraphs(t *testing.T) map[string][]string {
	t.Helper()
	list, err := u.e.Dynamic.Resource(framework.KrocodileGraphGVR).Namespace(u.ns).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	graphs := map[string][]string{}
	for _, g := range list.Items {
		graphs[g.GetLabels()["kardinal.io/bundle"]] = g.GetFinalizers()
	}
	return graphs
}

// labelled counts the PromotionSteps, PRStatuses and PolicyGates labelled
// with the Bundle.
func (u *upgrade) labelled(t *testing.T, bundle string) int {
	t.Helper()
	ctx := context.Background()
	sel := []client.ListOption{client.InNamespace(u.ns), client.MatchingLabels{"kardinal.io/bundle": bundle}}
	var steps v1alpha1.PromotionStepList
	var prs v1alpha1.PRStatusList
	var gates v1alpha1.PolicyGateList
	require.NoError(t, u.e.Client.List(ctx, &steps, sel...))
	require.NoError(t, u.e.Client.List(ctx, &prs, sel...))
	require.NoError(t, u.e.Client.List(ctx, &gates, sel...))
	return len(steps.Items) + len(prs.Items) + len(gates.Items)
}

// kept is an object the guide says the upgrade keeps, with the uid and
// creationTimestamp it had under v0.8.1.
type kept struct {
	obj     client.Object
	uid     types.UID
	created metav1.Time
}

func keep(objs ...client.Object) []kept {
	out := make([]kept, 0, len(objs))
	for _, o := range objs {
		out = append(out, kept{obj: o, uid: o.GetUID(), created: o.GetCreationTimestamp()})
	}
	return out
}

// assertKept checks that each object still exists with the same name, uid
// and creationTimestamp.
func (u *upgrade) assertKept(t *testing.T, objs []kept) {
	t.Helper()
	for _, k := range objs {
		now := k.obj.DeepCopyObject().(client.Object)
		err := u.e.Client.Get(context.Background(), client.ObjectKeyFromObject(k.obj), now)
		if !assert.NoError(t, err, "%T %s", k.obj, k.obj.GetName()) {
			continue
		}
		assert.Equal(t, k.uid, now.GetUID(), "%T %s uid", k.obj, k.obj.GetName())
		assert.True(t, k.created.Equal(ptrTime(now.GetCreationTimestamp())), "%T %s creationTimestamp %v, was %v",
			k.obj, k.obj.GetName(), now.GetCreationTimestamp(), k.created)
	}
}

// ratchetCase is a legacy object and the writes that fail on it once the new
// CRDs are applied, with the CRD validation ratcheting of Kubernetes 1.33 and
// later (the table in the guide's "Kubernetes version"). status132 marks an
// object whose status write also fails on 1.30 to 1.32.
type ratchetCase struct {
	kind, name string
	fail       []string
	status132  bool
}

// ratchetWrite is the kubectl command of a write ("label", "spec", "status",
// or "env0": an edit of the first environment) to a legacy object.
func (u *upgrade) ratchetWrite(kind, name, write string) string {
	head := fmt.Sprintf("kubectl -n %s ", u.ns)
	switch write {
	case "label":
		return head + fmt.Sprintf("label %s %s e2e.kardinal.io/ratchet=1 --overwrite", kind, name)
	case "spec":
		if kind == "pipeline" {
			return head + fmt.Sprintf(`patch pipeline %s --type merge -p '{"spec":{"historyLimit":20}}'`, name)
		}
		return head + fmt.Sprintf(`patch policygate %s --type merge -p '{"spec":{"message":"ratchet"}}'`, name)
	case "status":
		if kind == "pipeline" {
			return head + fmt.Sprintf(`patch pipeline %s --subresource status --type merge -p '{"status":{"phase":"Degraded"}}'`, name)
		}
		return head + fmt.Sprintf(`patch policygate %s --subresource status --type merge -p '{"status":{"reason":"ratchet"}}'`, name)
	default:
		return head + fmt.Sprintf(`patch pipeline %s --type json -p '[{"op":"replace","path":"/spec/environments/0/path","value":"environments/x"}]'`, name)
	}
}

// assertRatchets writes to every legacy object and checks which writes the
// API server rejects.
func (u *upgrade) assertRatchets(t *testing.T, minor int) {
	t.Helper()
	pipelineWrites := []string{"label", "spec", "status", "env0"}
	gateWrites := []string{"label", "spec", "status"}
	cases := []ratchetCase{
		{"pipeline", "legacy-steps", []string{"env0"}, true},
		{"pipeline", "legacy-autorollback", []string{"env0"}, true},
		{"pipeline", "legacy-reserved", []string{"env0"}, true},
		{"pipeline", "legacy-policygates", nil, true},
		{"policygate", "legacy-selector", nil, true},
		{"pipeline", "legacy-badname", nil, false},
		{"policygate", legacyLongGate, gateWrites, false},
		{"pipeline", "legacy-dupenv", nil, false},
		{"pipeline", "legacy-shard", nil, false},
		{"pipeline", "legacy-clean", nil, false},
	}
	for _, c := range cases {
		fails := c.fail
		if c.status132 && minor < 33 {
			fails = append([]string{"status"}, fails...)
		}
		writes := pipelineWrites
		if c.kind == "policygate" {
			writes = gateWrites
		}
		for _, w := range writes {
			run := u.e.Shell(t, u.ratchetWrite(c.kind, c.name, w))
			if contains(fails, w) {
				assert.True(t, run.Code != 0 && strings.Contains(run.Output, " is invalid"),
					"%s %s: the %s write should be rejected as invalid:\n%s", c.kind, c.name, w, run.Output)
			} else {
				assert.Zero(t, run.Code, "%s %s: the %s write should succeed:\n%s", c.kind, c.name, w, run.Output)
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// logEntry is one JSON line of the controller's log.
type logEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Graph   string `json:"graph"`
}

// logEntries parses the JSON lines of out.
func logEntries(out string) []logEntry {
	var entries []logEntry
	for _, l := range strings.Split(out, "\n") {
		var e logEntry
		if strings.HasPrefix(l, "{") && json.Unmarshal([]byte(l), &e) == nil {
			entries = append(entries, e)
		}
	}
	return entries
}

// helmRevision is the release's revision in helm list.
func (u *upgrade) helmRevision(t *testing.T) string {
	t.Helper()
	out, err := u.e.Helm(t, "list", "-n", framework.ControllerNamespace, "--filter", "^"+upgradeRelease+"$", "-o", "json")
	require.NoError(t, err, out)
	var releases []struct {
		Revision string `json:"revision"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &releases), out)
	require.Len(t, releases, 1, out)
	return releases[0].Revision
}

// deploymentReady waits until ns/name has a ready replica.
func (u *upgrade) deploymentReady(t *testing.T, ns, name string, timeout time.Duration) {
	t.Helper()
	framework.Eventually(t, timeout, "Deployment "+ns+"/"+name+" ready", func(ctx context.Context) (bool, string) {
		var d appsv1.Deployment
		if err := u.e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &d); err != nil {
			return false, err.Error()
		}
		return d.Status.ReadyReplicas >= 1, fmt.Sprintf("ready replicas %d", d.Status.ReadyReplicas)
	})
}

// kroSystem is the kro-system Namespace.
func (u *upgrade) kroSystem(t *testing.T) *corev1.Namespace {
	t.Helper()
	ns, err := u.e.Kube.CoreV1().Namespaces().Get(context.Background(), "kro-system", metav1.GetOptions{})
	require.NoError(t, err)
	return ns
}

// noPods checks that no Pod in ns matches the selector.
func (u *upgrade) noPods(t *testing.T, ns, selector string) {
	t.Helper()
	pods, err := u.e.Kube.CoreV1().Pods(ns).List(context.Background(), metav1.ListOptions{LabelSelector: selector})
	require.NoError(t, err)
	assert.Empty(t, pods.Items, "pods %s in %s", selector, ns)
}

// pausedMessage is the message of a step a paused Pipeline holds.
func pausedMessage(p string) string {
	return "pipeline " + p + " is paused — resume with: kardinal resume " + p
}

// serverMinor is the test cluster's Kubernetes minor version.
func serverMinor(t *testing.T, e *framework.Env) int {
	t.Helper()
	v, err := e.Kube.Discovery().ServerVersion()
	require.NoError(t, err)
	minor, err := strconv.Atoi(strings.TrimSuffix(v.Minor, "+"))
	require.NoError(t, err, "server minor %q", v.Minor)
	return minor
}

// TestUpgrade_FromV081 follows the upgrade guide ("Upgrading from v0.8.1" in
// docs/installation.md) step by step on a cluster running v0.8.1 as released,
// running the guide's own commands, and checks what each step says happens.
//
// Under v0.8.1 it builds: a Pipeline whose Bundle is Verified in test and held
// in prod by two gates, one of them on a MetricCheck; a Pipeline paused with
// the v0.8.1 CLI and one with spec.paused alone; a Superseded and a Promoting
// Bundle; and one stored object for each finding of step 1.
//
//   - Step 1's finder reports each of those objects once. The fixes run after
//     step 7 (the guide's "If you already applied the CRDs" path), so the test
//     first checks the table in "Kubernetes version": which label, spec,
//     status and environment writes the new CRDs reject on each object with
//     CRD validation ratcheting: on 1.30 to 1.32 status writes fail too. The
//     cluster must run Kubernetes 1.30 or later; the suite runs on 1.30, the
//     oldest minor kardinal supports, and the newest.
//   - Step 7 applies every kardinal CRD by hand; the API server then enforces them
//     (a reserved environment name and a 64-character PolicyGate name are
//     rejected), and kardinal policy simulate reads the pre-upgrade MetricCheck
//     result as stale. After the fixes the finder prints nothing.
//   - Step 8 with --reuse-values fails on the krocodile key and changes
//     nothing; with --reset-then-reuse-values it upgrades, and the release's
//     objects change exactly as the guide lists. The test adds the
//     checkout's image and the suite's git server to step 8.
//   - Step 9: doctor passes, there is one kro Graph per Bundle that was
//     Promoting and none for the Superseded one, the controller logs graph
//     created for each and only the two documented warnings.
//   - In-flight state: the steps, PRStatuses and gate instances keep their
//     uid and creationTimestamp, the gates are re-evaluated at startup, the
//     MetricCheck result is refreshed, a template edit doesn't change the
//     in-flight instance, and kardinal override releases it: prod opens its
//     PR and the Bundle finishes after the merge. test kept the old image, as
//     the guide warns, and a new Bundle writes it.
//   - Step 11: both paused Pipelines stay held, resume releases them, and the
//     leftover command removes what a Superseded Bundle left after deletion.
//
// Covers UPG-CRDS-01, UPG-REUSE-01, UPG-INFLIGHT-01, UPG-PAUSED-01, UPG-GATES-01, UPG-RATCHET-01.
func TestUpgrade_FromV081(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	u := &upgrade{e: e, ns: ns, main: ns, cliPaused: ns + "-cli", specPaused: ns + "-spec"}
	minor := serverMinor(t, e)
	require.GreaterOrEqual(t, minor, 30, "kardinal needs Kubernetes 1.30 or later; the cluster runs 1.%d", minor)
	t.Logf("Kubernetes 1.%d", minor)
	require.Greater(t, len(legacyLongGate), 63)

	u.repo = e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"test", "prod", "cli", "spec"}}))
	apps := map[string]string{
		u.main + "-test": "test", u.main + "-prod": "prod",
		u.cliPaused + "-test": "cli", u.specPaused + "-test": "spec",
	}
	for app, env := range apps {
		e.ArgoApp(t, app, u.repo, fixtures.Path(env), ns)
	}
	for app, env := range apps {
		e.WaitArgoApp(t, app, syncTimeout)
		e.WaitDeploymentImage(t, ns, fixtures.Workload(env), fixtures.Image+":"+fixtures.V1, syncTimeout)
	}
	prom := e.BarePrometheus(t, ns)

	// ── v0.8.1 ──────────────────────────────────────────────────────────
	e.Kubectl(t, ns, u.v081Pipeline(u.main, [][2]string{{"test", "test"}, {"prod", "prod"}}, map[string]string{"prod": "pr-review"})+
		"---\n"+u.v081Pipeline(u.cliPaused, [][2]string{{"test", "cli"}}, nil)+
		"---\n"+u.v081Pipeline(u.specPaused, [][2]string{{"test", "spec"}}, nil)+
		fmt.Sprintf(`---
apiVersion: kardinal.io/v1alpha1
kind: MetricCheck
metadata:
  name: errors
spec:
  interval: 30s
  prometheusURL: %s
  query: vector(0)
  threshold:
    operator: lt
    value: 1
---
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: upgrade-hold
  labels:
    kardinal.io/applies-to: prod
spec:
  expression: "false"
  message: held across the upgrade
  recheckInterval: 1h
---
apiVersion: kardinal.io/v1alpha1
kind: PolicyGate
metadata:
  name: metrics-ok
  labels:
    kardinal.io/applies-to: prod
spec:
  expression: metrics.errors.result == "Pass"
  message: the error count must be below 1
  recheckInterval: 10s
---
`, prom)+u.legacyObjects(), "apply", "-f", "-")

	out, err := e.KardinalV081(t, ns, "pause", u.cliPaused)
	require.NoError(t, err, out)
	assert.Equal(t, "Pipeline "+u.cliPaused+" paused. No new promotions will start.", strings.TrimSpace(out))
	cliFreeze := e.WaitFreezeGate(t, ns, u.cliPaused, gateTimeout)
	e.SetPipelinePaused(t, ns, u.specPaused, true)

	b1 := u.v081Bundle(t, u.main, fixtures.V2)
	e.WaitStepState(t, ns, u.main, b1, "test", "Verified", promoteTimeout)
	held := e.WaitGateReady(t, ns, b1, "prod", "upgrade-hold", false, "= false", gateTimeout)
	metrics := e.WaitGateReady(t, ns, b1, "prod", "metrics-ok", true, "", gateTimeout)
	e.NoStep(t, ns, u.main, b1, "prod", holdFor)
	framework.Eventually(t, time.Minute, "v0.8.1 MetricCheck errors Pass", func(ctx context.Context) (bool, string) {
		var mc v1alpha1.MetricCheck
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: "errors"}, &mc); err != nil {
			return false, err.Error()
		}
		return mc.Status.Result == "Pass" && mc.Status.ValidUntil == nil,
			fmt.Sprintf("result %q validUntil %v", mc.Status.Result, mc.Status.ValidUntil)
	})

	b0a := u.v081Bundle(t, u.specPaused, fixtures.V3)
	e.WaitStepState(t, ns, u.specPaused, b0a, "test", "Verified", promoteTimeout)
	b0b := u.v081Bundle(t, u.specPaused, fixtures.V2)
	e.WaitBundlePhase(t, ns, b0a, "Superseded", promoteTimeout)
	e.WaitStepState(t, ns, u.specPaused, b0b, "test", "Verified", promoteTimeout)
	t.Logf("v0.8.1 Bundles: %s (Promoting, held in prod), %s (Superseded), %s (Promoting)", b1, b0a, b0b)

	inFlight := keep(
		u.step(t, u.main, b1, "test"),
		u.get(t, "prstatus-"+b1+"-test", &v1alpha1.PRStatus{}),
		u.get(t, "prstatus-"+b1+"-prod", &v1alpha1.PRStatus{}),
		held, metrics,
		u.step(t, u.specPaused, b0b, "test"),
		cliFreeze,
	)

	// ── 1. Find stored objects the new CRDs reject ──────────────────────
	finder := framework.GuideBlock(t, "as $reserved")
	assert.Equal(t, []string{
		"pipeline " + ns + "/legacy-autorollback env test: autoRollback",
		"pipeline " + ns + "/legacy-badname env Test: invalid or reserved name",
		"pipeline " + ns + "/legacy-dupenv: duplicate environment name test",
		"pipeline " + ns + "/legacy-policygates: spec.policyGates",
		"pipeline " + ns + "/legacy-reserved env graph: invalid or reserved name",
		"pipeline " + ns + "/legacy-shard env test: shard",
		"pipeline " + ns + "/legacy-steps env test: steps",
		"policygate " + ns + "/" + legacyLongGate + ": name longer than 63 characters",
		"policygate " + ns + "/legacy-selector: spec.selector",
	}, u.findings(t, finder), "the finder reports each legacy object once, and no gate instance or freeze gate")

	// ── 2. Check your Helm values ───────────────────────────────────────
	values := e.MustShell(t, framework.GuideBlock(t, "helm get values"))
	assert.NotContains(t, values, "krocodile")
	assert.Contains(t, values, "name: git-token")

	// ── 3. Stop both v0.8.1 controllers ─────────────────────────────────
	e.MustShell(t, framework.GuideBlock(t, "scale deploy/kardinal-promoter"))
	u.noPods(t, framework.ControllerNamespace, "app.kubernetes.io/name=kardinal-promoter")
	u.noPods(t, "kro-system", "app=graph-controller")

	// ── 4. Remove the krocodile finalizers ──────────────────────────────
	graphs := u.krocodileGraphs(t)
	require.Len(t, graphs, 3, "v0.8.1 has one Graph per Bundle, Superseded included: %v", graphs)
	for _, b := range []string{b1, b0a, b0b} {
		assert.Equal(t, []string{"experimental.kro.run/graph-controller"}, graphs[b], "finalizers of %s's Graph", b)
	}
	e.MustShell(t, framework.GuideBlock(t, `"finalizers":null`))
	for b, f := range u.krocodileGraphs(t) {
		assert.Empty(t, f, "finalizers of %s's Graph", b)
	}

	// ── 5. Keep kro-system ──────────────────────────────────────────────
	e.MustShell(t, framework.GuideBlock(t, "helm.sh/resource-policy=keep"))
	assert.Equal(t, "keep", u.kroSystem(t).Annotations["helm.sh/resource-policy"])

	// ── 6. Install kro ──────────────────────────────────────────────────
	kro := e.Shell(t, framework.Fill(t, framework.GuideBlock(t, "install-kro.sh"), "<your-context>", `"$E2E_CONTEXT"`))
	require.Zero(t, kro.Code, kro.Output)
	assert.Contains(t, kro.Output, "installed (graphs.kro.run served)")
	u.deploymentReady(t, "kro-system", "kro", 3*time.Minute)
	_, err = e.Dynamic.Resource(crdGVR).Get(ctx, "graphs.kro.run", metav1.GetOptions{})
	require.NoError(t, err, "CRD graphs.kro.run")

	// ── 7. Apply the new kardinal CRDs ──────────────────────────────────
	crds := framework.Fill(t, framework.GuideBlock(t, "helm show crds"), guideChart, `"$KARDINAL_E2E_CHART"`)
	run := e.Shell(t, crds)
	if run.Code != 0 && strings.Contains(run.Output, "conflict") {
		t.Logf("the CRD apply reported field-manager conflicts; adding --force-conflicts as the guide says")
		run = e.Shell(t, framework.Fill(t, crds, "--server-side", "--server-side --force-conflicts"))
	}
	require.Zero(t, run.Code, run.Output)
	var applied []string
	for _, m := range regexp.MustCompile(`(?m)^customresourcedefinition\.apiextensions\.k8s\.io/(\S+) serverside-applied$`).FindAllStringSubmatch(run.Output, -1) {
		applied = append(applied, m[1])
	}
	// Every CRD of this checkout (the chart ships config/crd/bases).
	assert.Len(t, applied, len(crdFiles(t)), "the guide applies every kardinal CRD: %v", applied)
	assert.Contains(t, applied, "notificationhooks.kardinal.io")
	framework.Eventually(t, 30*time.Second, "the API server enforces the new CRDs", func(context.Context) (bool, string) {
		reserved := e.Shell(t, u.dryRun(u.pipelineHead("e2e-dry-run", "")+legacyEnv("graph", "")))
		long := e.Shell(t, u.dryRun(fmt.Sprintf("apiVersion: kardinal.io/v1alpha1\nkind: PolicyGate\nmetadata:\n  name: %s\nspec:\n  expression: \"true\"\n", legacyLongGate+"-x")))
		return reserved.Code != 0 && strings.Contains(reserved.Output, "reserved environment name") &&
				long.Code != 0 && strings.Contains(long.Output, "PolicyGate names are at most 63 characters"),
			reserved.Output + long.Output
	})
	u.assertRatchets(t, minor)

	sim := e.MustKardinal(t, ns, "policy", "simulate", "--pipeline", u.main, "--env", "prod")
	assert.True(t, strings.HasPrefix(sim, "RESULT: BLOCKED\n"), sim)
	assert.Contains(t, sim, "Blocked by: metrics-ok\n")
	assert.Contains(t, sim, "Blocked by: upgrade-hold\n")
	assert.Regexp(t, `(?m)^metrics-ok:\s+BLOCK\s+\(.*metric "errors" result is stale\)$`, sim,
		"the v0.8.1 MetricCheck result is stale once the new CRDs are applied")

	// The fixes of step 1, after the CRDs, as "If you already applied the
	// CRDs, run the fixes from step 1 now" says.
	fix := func(finding string, pairs ...string) {
		t.Helper()
		e.MustShell(t, framework.Fill(t, framework.GuideFix(t, finding), append([]string{"<ns>", ns}, pairs...)...))
	}
	fix("`steps`", "<name>", "legacy-steps", "<i>", "0")
	fix("`autoRollback`", "<name>", "legacy-autorollback", "<i>", "0")
	fix("`shard`", "<name>", "legacy-shard", "<i>", "0")
	fix("`spec.policyGates`", "<name>", "legacy-policygates")
	fix("PolicyGate `spec.selector`", "<name>", "legacy-selector")
	const rename = "Invalid, reserved or duplicate environment name"
	fix(rename, "<name>", "legacy-reserved", "<i>", "0", "<new-name>", "preview")
	fix(rename, "<name>", "legacy-badname", "<i>", "0", "<new-name>", "test")
	fix(rename, "<name>", "legacy-dupenv", "<i>", "1", "<new-name>", "prod")
	e.MustShell(t, framework.Fill(t, framework.GuideBlock(t, "<long-name>"),
		"<ns>", ns, "<long-name>", legacyLongGate, "<short-name>", legacyShortGate))
	assert.Empty(t, u.findings(t, finder), "the finder prints nothing once every finding is fixed")
	short := u.get(t, legacyShortGate, &v1alpha1.PolicyGate{}).(*v1alpha1.PolicyGate)
	assert.Equal(t, "true", short.Spec.Expression)
	err = e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: legacyLongGate}, &v1alpha1.PolicyGate{})
	assert.True(t, apierrors.IsNotFound(err), "the long-named gate is deleted: %v", err)

	// ── 8. Upgrade the chart ────────────────────────────────────────────
	upgradeCmd := strings.TrimRight(framework.Fill(t, framework.GuideBlock(t, "helm upgrade"), guideChart, `"$KARDINAL_E2E_CHART"`), "\n") + ` \
  --set image.repository="${KARDINAL_E2E_IMAGE%:*}" --set image.tag="${KARDINAL_E2E_IMAGE##*:}" --set image.pullPolicy=Never \
  --set scm.provider="$KARDINAL_E2E_SCM_PROVIDER" --set scm.apiURL="$KARDINAL_E2E_SCM_API"
`
	reuse := e.Shell(t, framework.Fill(t, upgradeCmd, "--reset-then-reuse-values", "--reuse-values"))
	assert.NotZero(t, reuse.Code, reuse.Output)
	// Helm 3.18.5 and later say "additional properties 'krocodile' not
	// allowed", older versions "Additional property krocodile is not allowed".
	assert.Regexp(t, `additional properties 'krocodile' not allowed|Additional property krocodile is not allowed`, reuse.Output)
	assert.Equal(t, "1", u.helmRevision(t), "a failed --reuse-values upgrade changes nothing")
	_, err = e.Kube.AppsV1().Deployments("kro-system").Get(ctx, "graph-controller", metav1.GetOptions{})
	assert.NoError(t, err, "v0.8.1's graph-controller survives the failed upgrade")

	rel := e.ExistingRelease(t, upgradeRelease, framework.ControllerNamespace)
	up := e.Shell(t, upgradeCmd)
	require.Zero(t, up.Code, up.Output)
	assert.Equal(t, "2", u.helmRevision(t))
	before, after := framework.ManifestObjects(t, rel.Manifest(t, 1)), framework.ManifestObjects(t, rel.Manifest(t, 2))
	var removed, added []string
	for o := range before {
		if !after[o] {
			removed = append(removed, o)
		}
	}
	for o := range after {
		if !before[o] {
			added = append(added, o)
		}
	}
	assert.ElementsMatch(t, []string{
		"ClusterRole /kardinal-graph-controller", "ClusterRoleBinding /kardinal-graph-controller",
		"CustomResourceDefinition /graphrevisions.experimental.kro.run", "CustomResourceDefinition /graphs.experimental.kro.run",
		"Deployment kro-system/graph-controller", "Namespace /kro-system", "ServiceAccount kro-system/graph-controller",
	}, removed, "objects only v0.8.1 needed")
	// The Graph identity and leader election are new in v0.9; past them the
	// chart only adds admission policies (identity, holds, Graph objects...)
	// and ClusterRoles of its own: no other workload or binding appears
	// with the upgrade (#1560: listing each policy broke on every feature).
	assert.Subset(t, added, []string{
		"ClusterRole /kardinal-promoter-graph-applier", "ClusterRole /kardinal-promoter-graph-reader",
		"ClusterRole /kardinal-promoter-kro-watch",
		"Role kardinal-system/kardinal-promoter-leader-election", "RoleBinding kardinal-system/kardinal-promoter-leader-election",
		"ValidatingAdmissionPolicy /kardinal-promoter-hold-writes", "ValidatingAdmissionPolicyBinding /kardinal-promoter-hold-writes",
	}, "objects the new chart adds")
	for _, o := range added {
		ok := strings.HasPrefix(o, "ValidatingAdmissionPolicy /kardinal-promoter-") ||
			strings.HasPrefix(o, "ValidatingAdmissionPolicyBinding /kardinal-promoter-") ||
			strings.HasPrefix(o, "ClusterRole /kardinal-promoter-") ||
			strings.HasPrefix(o, "Role kardinal-system/kardinal-promoter-") ||
			strings.HasPrefix(o, "RoleBinding kardinal-system/kardinal-promoter-")
		assert.True(t, ok, "the upgrade adds %s, which is not an admission policy or role of the chart", o)
	}
	framework.Eventually(t, time.Minute, "v0.8.1's Graph controller and CRDs deleted", func(ctx context.Context) (bool, string) {
		if _, err := e.Kube.AppsV1().Deployments("kro-system").Get(ctx, "graph-controller", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("Deployment graph-controller: %v", err)
		}
		for _, crd := range []string{"graphs.experimental.kro.run", "graphrevisions.experimental.kro.run"} {
			if _, err := e.Dynamic.Resource(crdGVR).Get(ctx, crd, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				return false, fmt.Sprintf("CRD %s: %v", crd, err)
			}
		}
		return true, ""
	})
	assert.Nil(t, u.kroSystem(t).DeletionTimestamp, "kro-system is kept")
	u.deploymentReady(t, "kro-system", "kro", time.Minute)
	rel.WaitRolledOut(t, 3*time.Minute)
	pods := rel.Pods(t)
	require.Len(t, pods, 1)
	started := pods[0].Status.StartTime
	require.NotNil(t, started)

	// ── 9. Check the result ─────────────────────────────────────────────
	var graphNames []string
	framework.Eventually(t, 2*time.Minute, "one kro Graph per Bundle that was Promoting", func(ctx context.Context) (bool, string) {
		list, err := e.Dynamic.Resource(framework.GraphGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		byBundle := map[string]string{}
		for _, g := range list.Items {
			byBundle[g.GetLabels()["kardinal.io/bundle"]] = g.GetName()
		}
		graphNames = []string{byBundle[b1], byBundle[b0b]}
		return len(list.Items) == 2 && byBundle[b1] != "" && byBundle[b0b] != "", fmt.Sprintf("Graphs by Bundle: %v", byBundle)
	})
	check := e.MustShell(t, framework.GuideBlock(t, "kardinal doctor"))
	assert.Regexp(t, `(?m)^\d+ check\(s\) passed$`, check, "kardinal doctor passes every check")
	assert.NotContains(t, check, "warning(s)")
	var warnings []string
	for _, l := range logEntries(check) {
		// The fixtures' Pipelines share one repository and write the same
		// paths on purpose: each logs a PathConflict warning (#1504), which
		// the guide describes apart from the startup warnings.
		if strings.HasPrefix(l.Message, "environments write overlapping paths of the same repository and branch") {
			continue
		}
		warnings = append(warnings, l.Level+": "+l.Message)
	}
	// The three the guide lists (docs/installation.md, step 9): the upgrade
	// keeps the v0.8.1 values, which set none of these (#1560).
	if assert.Len(t, warnings, 3, "the controller logs three warnings: %q", warnings) {
		sort.Strings(warnings)
		assert.True(t, strings.HasPrefix(warnings[0], "warn: --scm-allowed-repositories (Helm scm.allowedRepositories) is not set"), warnings[0])
		assert.True(t, strings.HasPrefix(warnings[1], "warn: SCM webhooks disabled: no --webhook-secret set"), warnings[1])
		assert.True(t, strings.HasPrefix(warnings[2], "warn: UI API authentication is off"), warnings[2])
	}
	created := map[string]bool{}
	for _, l := range logEntries(rel.AllLogs(t)) {
		if l.Message == "graph created" {
			created[l.Graph] = true
		}
	}
	for _, g := range graphNames {
		assert.True(t, created[g], "the controller logs graph created for %s", g)
	}

	// ── In-flight Bundles ───────────────────────────────────────────────
	u.assertKept(t, inFlight)
	e.WaitGate(t, ns, b1, "prod", "upgrade-hold", gateTimeout, "re-evaluated since the new controller started",
		func(g *v1alpha1.PolicyGate) bool {
			return g.Spec.Generated && g.Status.LastEvaluatedAt != nil && !g.Status.LastEvaluatedAt.Before(started) && !g.Status.Ready
		})
	framework.Eventually(t, 2*time.Minute, "MetricCheck errors re-evaluated", func(ctx context.Context) (bool, string) {
		var mc v1alpha1.MetricCheck
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: "errors"}, &mc); err != nil {
			return false, err.Error()
		}
		return mc.Status.ValidUntil != nil && mc.Status.Result == "Pass",
			fmt.Sprintf("result %q validUntil %v", mc.Status.Result, mc.Status.ValidUntil)
	})
	e.WaitGate(t, ns, b1, "prod", "metrics-ok", gateTimeout, "passing on a fresh result",
		func(g *v1alpha1.PolicyGate) bool {
			return g.Spec.Generated && g.Status.LastEvaluatedAt != nil && !g.Status.LastEvaluatedAt.Before(started) &&
				g.Status.Ready && !strings.Contains(g.Status.Reason, "stale")
		})
	assert.Equal(t, "Verified", u.step(t, u.main, b1, "test").Status.State, "a Verified environment stays Verified")
	assertEnvAt(t, u.app(), "test", fixtures.V1)

	// ── 10. Tidy up ─────────────────────────────────────────────────────
	e.MustShell(t, framework.GuideBlock(t, "app.kubernetes.io/managed-by-"))
	kroNS := u.kroSystem(t)
	for _, a := range []string{"helm.sh/resource-policy", "meta.helm.sh/release-name", "meta.helm.sh/release-namespace"} {
		assert.NotContains(t, kroNS.Annotations, a)
	}
	for _, l := range []string{"app.kubernetes.io/managed-by", "app.kubernetes.io/component"} {
		assert.NotContains(t, kroNS.Labels, l)
	}

	// Gate instances keep their old expression; override releases the Bundle.
	e.Kubectl(t, ns, "", "patch", "policygate", "upgrade-hold", "--type", "merge", "-p", `{"spec":{"expression":"true"}}`)
	e.NoStep(t, ns, u.main, b1, "prod", holdFor)
	held = e.WaitGate(t, ns, b1, "prod", "upgrade-hold", time.Second, "with the expression it was built with",
		func(g *v1alpha1.PolicyGate) bool { return g.Spec.Expression == "false" && !g.Status.Ready })
	out = e.MustShell(t, framework.Fill(t, framework.GuideBlock(t, "kardinal override"),
		"kardinal override", "kardinal -n "+ns+" override", "<pipeline>", u.main, "<env>", "prod",
		"<gate>", "upgrade-hold", "<why>", "upgrade e2e"))
	assert.Contains(t, out, fmt.Sprintf("Override applied: gate=%s pipeline=%s stage=prod", held.Name, u.main))
	e.WaitStepState(t, ns, u.main, b1, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, u.repo, time.Minute, "prod PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	require.NoError(t, e.Git.MergePR(ctx, u.repo, pr.Number))
	e.WaitStepState(t, ns, u.main, b1, "prod", "Verified", promoteTimeout)
	assertEnvAt(t, u.app(), "prod", fixtures.V2)
	e.WaitBundlePhase(t, ns, b1, "Verified", promoteTimeout)

	// test was reported Verified by v0.8.1 without a commit; a new Bundle for
	// the same image writes it.
	b2 := e.CreateBundle(t, ns, u.main, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, ns, u.main, b2, "test", "Verified", promoteTimeout)
	assertEnvAt(t, u.app(), "test", fixtures.V2)

	// ── 11. Paused Pipelines ────────────────────────────────────────────
	paused := strings.SplitN(framework.GuideBlock(t, "kardinal -n <ns> resume"), "\n", 3)
	list := e.MustShell(t, paused[0])
	for _, p := range []string{u.cliPaused, u.specPaused} {
		assert.Regexp(t, `(?m)^`+regexp.QuoteMeta(p)+`\s+true\s*$`, list)
	}
	assert.Regexp(t, `(?m)^`+regexp.QuoteMeta(u.main)+`\s+(false|<none>)\s*$`, list)
	assert.Equal(t, map[string]string{
		"kardinal.io/pipeline": u.cliPaused, "kardinal.io/scope": "system", "kardinal.io/freeze": "true",
	}, u.get(t, "freeze-"+u.cliPaused, &v1alpha1.PolicyGate{}).GetLabels(), "the v0.8.1 freeze gate is kept")
	specGate := e.WaitFreezeGate(t, ns, u.specPaused, gateTimeout)
	assertFreezeGate(t, specGate, u.pipeline(t, u.specPaused))
	assert.True(t, specGate.Spec.Generated, "kardinal marks the freeze gate it creates")
	appEnv := map[string]string{u.cliPaused: "cli", u.specPaused: "spec"}
	bundles := map[string]string{}
	for _, p := range []string{u.cliPaused, u.specPaused} {
		e.WaitPipeline(t, ns, p, gateTimeout, "Paused", pausedCondition)
		bundles[p] = e.CreateBundle(t, ns, p, "--image", fixtures.Image+":"+fixtures.V2)
		e.WaitStepMessage(t, ns, p, bundles[p], "test", "Pending", pausedMessage(p), time.Minute)
	}
	for _, p := range []string{u.cliPaused, u.specPaused} {
		e.StepHeld(t, ns, p, bundles[p], "test", pausedMessage(p), holdFor)
		assertEnvAt(t, u.app(), appEnv[p], fixtures.V1)
	}
	for _, p := range []string{u.cliPaused, u.specPaused} {
		assert.Equal(t, "Pipeline "+p+" resumed.\n", e.MustShell(t, framework.Fill(t, paused[1], "<ns>", ns, "<pipeline>", p)))
		e.WaitNoFreezeGate(t, ns, p, gateTimeout)
	}
	for _, p := range []string{u.cliPaused, u.specPaused} {
		e.WaitStepState(t, ns, p, bundles[p], "test", "Verified", resumeTimeout+promoteTimeout)
		assertEnvAt(t, u.app(), appEnv[p], fixtures.V2)
	}

	// ── 11. Leftover children ───────────────────────────────────────────
	e.Kubectl(t, ns, "", "delete", "bundle", b0a, "--wait=true", "--timeout=60s")
	require.NotZero(t, u.labelled(t, b0a), "the Superseded Bundle's children outlive it")
	out = e.MustShell(t, framework.GuideBlock(t, "is gone:"))
	assert.Equal(t, []string{"Bundle " + ns + "/" + b0a + " is gone:"},
		regexp.MustCompile(`(?m)^Bundle \S+ is gone:$`).FindAllString(out, -1))
	assert.Zero(t, u.labelled(t, b0a))
	u.get(t, "upgrade-hold", &v1alpha1.PolicyGate{})
	u.get(t, "metrics-ok", &v1alpha1.PolicyGate{})
	// The children of a Bundle that exists are kept. b1's own steps may be
	// gone already: b2 replaced it in every environment, so its Graph is
	// retired after graph.retire.superseded (#1527) and its steps live on in
	// status.retiredSteps.
	u.step(t, u.main, b2, "test")
	// b1's prod step is not lost by the cleanup: it is either still a
	// PromotionStep or, its Graph retired, a record in status.retiredSteps.
	if _, live, err := e.Step(context.Background(), ns, u.main, b1, "prod"); assert.NoError(t, err) && !live {
		var b v1alpha1.Bundle
		u.get(t, b1, &b)
		envs := []string{}
		for _, r := range b.Status.RetiredSteps {
			envs = append(envs, r.Environment)
		}
		assert.Contains(t, envs, "prod", "b1's prod step is kept in status.retiredSteps")
	}
}

// dryRun is a script that server-side dry-runs applying y in the namespace.
func (u *upgrade) dryRun(y string) string {
	return fmt.Sprintf("kubectl -n %s apply --dry-run=server -f - <<'EOF'\n%sEOF\n", u.ns, y)
}
