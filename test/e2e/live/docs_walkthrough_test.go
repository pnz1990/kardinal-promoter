//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/scale"
)

// The docs walkthrough runs the command blocks of the getting-started and
// how-to pages in order, as a new user would, and holds each command's
// output to the output the page shows (framework.PageSample,
// framework.SampleMismatches). The commands are read from the pages, so an
// edit to a page changes what runs. What the walkthrough changes in a block
// is only what a user changes too: their fork (a Forgejo repo laid out as
// pnz1990/kardinal-demo), their token, and the name of the Pipeline a how-to
// page calls my-app.

// demoTestImage is the kardinal-demo overlays' image, sha-9349a3f.
const demoTestImage = "ghcr.io/pnz1990/kardinal-test-app:sha-9349a3f"

// demoShipImage is the image the quickstart ships first and first-pipeline
// creates its version with: sha-9349a3f with its digest pinned.
const demoShipImage = demoTestImage + "@sha256:51a7355fc6cb8928c89cef5bdf55a7e1ea9fe8be102beb718486338fc7286cd0"

// transientState is a state line ship --wait prints only when it sees it: on
// a fast cluster an environment can go from Promoting to Live between polls.
var transientState = regexp.MustCompile(`\S+: (Promoting|Checking health)\s*$`)

// kardinalDemoRepo is pnz1990/kardinal-demo: environments/<env> with a
// kustomization over the kardinal-test-app Deployment in namespace default,
// at sha-9349a3f.
func kardinalDemoRepo() map[string][]byte {
	files := map[string][]byte{}
	for _, env := range []string{"test", "uat", "prod"} {
		files["environments/"+env+"/kustomization.yaml"] = []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
images:
- name: ghcr.io/pnz1990/kardinal-test-app
  newName: ghcr.io/pnz1990/kardinal-test-app
  newTag: sha-9349a3f
`)
		files["environments/"+env+"/deployment.yaml"] = []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: kardinal-test-app
  namespace: default
  labels:
    app: kardinal-test-app
    env: ` + env + `
spec:
  replicas: 1
  selector:
    matchLabels:
      app: kardinal-test-app
  template:
    metadata:
      labels:
        app: kardinal-test-app
    spec:
      containers:
        - name: app
          image: ` + demoTestImage + `
          ports:
            - containerPort: 8080
`)
	}
	return files
}

// walkthrough runs page blocks as a user, in a scratch directory (a block
// may write files, such as kardinal init's pipeline.yaml), with GITHUB_TOKEN
// set to the fork's token in the environment.
type walkthrough struct {
	t     *testing.T
	e     *framework.Env
	dir   string
	token string
	repo  gitserver.Repo
}

// run runs the block of page that contains contains, with each pair of
// fills replaced (framework.Fill fails when one is not in the block).
func (w *walkthrough) run(page, contains string, fills ...string) framework.ShellRun {
	w.t.Helper()
	return w.runAt(page, contains, -1, fills...)
}

// runAt is run on the n-th block that contains contains
// (framework.PageBlockAt).
func (w *walkthrough) runAt(page, contains string, n int, fills ...string) framework.ShellRun {
	w.t.Helper()
	block := framework.PageBlockAt(w.t, page, contains, n)
	if len(fills) > 0 {
		block = framework.Fill(w.t, block, fills...)
	}
	// The token reaches the block as $GITHUB_TOKEN, never in the logged script.
	return w.e.ShellEnv(w.t, []string{"GITHUB_TOKEN=" + w.token}, "cd "+strconv.Quote(w.dir)+"\n"+block)
}

// must is run that fails the test on a non-zero exit.
func (w *walkthrough) must(page, contains string, fills ...string) string {
	w.t.Helper()
	r := w.run(page, contains, fills...)
	require.Zero(w.t, r.Code, "docs/%s, the block with %q:\n%s", page, contains, r.Output)
	return r.Output
}

// matches checks out against the output the page shows for the block.
func (w *walkthrough) matches(page, contains, out string, optional *regexp.Regexp, names ...string) {
	w.t.Helper()
	sample := framework.PageSample(w.t, page, contains, -1)
	assert.Empty(w.t, framework.SampleMismatches(sample, out, optional, names...),
		"docs/%s shows, for %q:\n%s\nthe command printed:\n%s", page, contains, sample, out)
}

// commentSample checks out against the "# " lines that follow each kardinal
// command of the block: the output the page shows inline.
func (w *walkthrough) commentSample(page, contains, out string, names ...string) {
	w.t.Helper()
	var sample []string
	after, cont := false, false
	for _, line := range strings.Split(framework.PageBlock(w.t, page, contains), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "kardinal "):
			after = true
		case cont:
			// A continuation line of the command (after a trailing \).
		case after && trimmed == "#":
			sample = append(sample, "") // a blank line of the output
		case after && strings.HasPrefix(trimmed, "# "):
			sample = append(sample, strings.TrimPrefix(trimmed, "# "))
		default:
			after = false
		}
		cont = strings.HasSuffix(trimmed, `\`)
	}
	require.NotEmpty(w.t, sample, "docs/%s: the block with %q shows no output", page, contains)
	assert.Empty(w.t, framework.SampleMismatches(strings.Join(sample, "\n"), out, nil, names...),
		"docs/%s shows, for %q:\n%s\nthe commands printed:\n%s", page, contains, strings.Join(sample, "\n"), out)
}

// TestDocs_Walkthrough runs, on the suite's cluster (Forgejo, Argo CD, kro
// and this checkout's controller), the command blocks of:
//
//   - quickstart.md: kardinal quickstart on a fork laid out as
//     kardinal-demo, why, approve, status, ship --wait (exit code 3 at the
//     gate), each output held to the page's;
//   - approvals.md: kardinal inbox names the waiting approval, approve with a
//     comment ships it;
//   - rollback.md: rollback, hold and unhold, and history;
//   - policy-gates.md: kardinal policy simulate;
//   - pause.md: pause and resume a Pipeline, and pause and resume one fleet
//     of a small fleet Pipeline;
//   - agentic/connect-an-agent.md: the agent kubeconfig the page builds, and
//     kardinal mcp's read tools as that identity;
//   - first-pipeline.md, after kardinal quickstart --delete: the
//     ApplicationSet, the git Secret, kardinal init --apply, a version, its
//     prod PR (titled as the page says), the merge, and the org gate.
//
// Covers DOCS-WALK-01, DOCS-WALK-02, DOCS-WALK-03, DOCS-WALK-04.
func TestDocs_Walkthrough(t *testing.T) {
	checkWalkManifest(t)
	if t.Failed() {
		t.FailNow()
	}
	lockDemoNames(t)
	e := framework.New(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	repo := e.Repo(t, ns, kardinalDemoRepo())
	require.Equal(t, "main", repo.Branch, "the demo's Applications deploy main")
	tok, err := e.Kube.CoreV1().Secrets(ns).Get(ctx, framework.GitSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	w := &walkthrough{t: t, e: e, dir: t.TempDir(), token: string(tok.Data["token"]), repo: repo}
	const app = "kardinal-test-app"
	envs := []string{"test", "uat", "prod"}
	for _, env := range envs {
		e.DeleteOnCleanup(t, objectRef("v1", "Namespace", "", app+"-"+env), true)
		e.DeleteOnCleanup(t, objectRef("argoproj.io/v1alpha1", "Application", framework.ArgoCDNamespace, app+"-"+env), true)
	}
	e.DeleteOnCleanup(t, objectRef("argoproj.io/v1alpha1", "ApplicationSet", framework.ArgoCDNamespace, app), true)
	for _, kind := range []string{"Pipeline"} {
		for _, name := range []string{"demo", app} {
			e.DeleteOnCleanup(t, objectRef(v1alpha1.GroupVersion.String(), kind, "default", name), true)
		}
	}
	e.DeleteOnCleanup(t, objectRef(v1alpha1.GroupVersion.String(), "PolicyGate", "default", "prod-approval"), true)
	e.DeleteOnCleanup(t, objectRef(v1alpha1.GroupVersion.String(), "PolicyGate", "platform-policies", "no-weekend-deploys"), false)
	e.DeleteOnCleanup(t, objectRef("v1", "Secret", "default", "github-token"), false)
	t.Cleanup(func() { deleteBundlesOf(t, e, "default", "demo", app) })

	demoBundle := func() string { return newestBundle(t, e, "default", "demo") }

	// ── quickstart.md ───────────────────────────────────────────────────
	q := "quickstart.md"
	out := w.must(q, "kardinal quickstart --repo", "https://github.com/<you>/kardinal-demo", repo.CloneURL)
	// The installs differ (the suite installed kro, Argo CD and kardinal);
	// what the quickstart applies and its next steps are the page's.
	sample := framework.PageSample(t, q, "kardinal quickstart --repo", -1)
	var applied []string
	for _, l := range strings.Split(sample, "\n") {
		if strings.Contains(l, "PolicyGate default/prod-approval") || strings.Contains(l, "Pipeline default/demo") ||
			strings.HasPrefix(strings.TrimSpace(l), "prod: Held by gate") || strings.HasPrefix(l, "Version ") ||
			l == "Next:" || strings.Contains(l, "kardinal ") && strings.Contains(l, ":") && strings.HasPrefix(l, "  ") {
			applied = append(applied, l)
		}
	}
	require.Greater(t, len(applied), 5, "the quickstart page shows what it applies and the next steps:\n%s", sample)
	assert.Empty(t, framework.SampleMismatches(strings.Join(applied, "\n"), out, nil, "demo"), "quickstart output:\n%s", out)
	first := demoBundle()
	for _, env := range []string{"test", "uat"} {
		e.WaitStepState(t, "default", "demo", first, env, "Verified", time.Minute)
		e.WaitDeploymentImage(t, app+"-"+env, app, demoShipImage, syncTimeout)
	}

	out = w.must(q, "kardinal why demo prod")
	w.matches(q, "kardinal why demo prod", out, nil, "demo")

	// The page approves twice: here, and after shipping the next version
	// (approvals.md's approve stands in for the second).
	r := w.runAt(q, "kardinal approve demo prod", 0)
	require.Zero(t, r.Code, r.Output)
	assert.Empty(t, framework.SampleMismatches(framework.PageSample(t, q, "kardinal approve demo prod", 0), r.Output, nil, "demo"),
		"quickstart.md's approve sample; the command printed:\n%s", r.Output)
	e.WaitBundlePhase(t, "default", first, "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, app+"-prod", app, demoShipImage, syncTimeout)

	out = w.must(q, "kardinal status demo")
	w.matches(q, "kardinal status demo", out, nil, "demo")

	r = w.run(q, "kardinal ship demo --image")
	w.matches(q, "kardinal ship demo --image", r.Output, transientState, "demo")
	code := regexp.MustCompile(`(?m)^# (\d+)$`).FindStringSubmatch(framework.PageBlock(t, q, "echo $?"))
	require.NotNil(t, code, "the page says which exit code ship ends with")
	assert.Equal(t, code[1], strconv.Itoa(r.Code), "ship --wait's exit code at the gate:\n%s", r.Output)
	second := demoBundle()
	require.NotEqual(t, first, second)

	// ── approvals.md ────────────────────────────────────────────────────
	a := "approvals.md"
	out = w.must(a, "kardinal inbox")
	assert.Regexp(t, `(?m)^Waiting on you \(\d+\):`, out)
	assert.Regexp(t, `(?m)^\s+demo\s+prod\s.*approval at prod-approval.*kardinal approve demo prod`, out, "inbox names demo's approval")
	out = w.must(a, `kardinal approve my-app prod --comment`, "my-app", "demo")
	assert.Contains(t, out, "Recorded: kubernetes-admin approves "+second+" for prod")
	e.WaitBundlePhase(t, "default", second, "Verified", promoteTimeout)

	// ── rollback.md ─────────────────────────────────────────────────────
	rb := "rollback.md"
	out = w.must(rb, "kardinal rollback my-app prod\n", "my-app", "demo")
	rollback := demoBundle()
	require.NotEqual(t, second, rollback, "rollback creates a version:\n%s", out)
	var rv v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: "default", Name: rollback}, &rv))
	assert.Equal(t, "true", rv.Labels["kardinal.io/rollback"], "the rollback version is labelled")
	require.NotNil(t, rv.Spec.Provenance)
	assert.Equal(t, first, rv.Spec.Provenance.RollbackOf, "it rolls back to the version that ran there before")
	// prod's approval gate holds a rollback too: the page says it goes
	// through the same gates.
	e.WaitGateReady(t, "default", rollback, "prod", "prod-approval", false, "waiting for approvals", gateTimeout)
	e.MustKardinal(t, "default", "approve", "demo", "prod", "--yes")
	e.WaitBundlePhase(t, "default", rollback, "Verified", promoteTimeout)

	w.must(rb, `kardinal hold my-app prod --reason "INC-4521: freeze prod`, "my-app", "demo")
	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo"}, &p))
	assert.Empty(t, p.Spec.Holds, "the block holds prod, then releases it")
	out = w.must(rb, "kardinal history my-app", "my-app", "demo")
	assert.Contains(t, out, rollback, "history lists the rollback")

	// ── policy-gates.md: the ScheduleClock; the gates on first-pipeline's
	// Pipeline below ──────────────────────────────────────────────────
	out = w.must("policy-gates.md", "kubectl get scheduleclock kardinal-clock")
	assert.Regexp(t, `(?m)^kardinal-clock\s+1m\s+\S+\s+\S+\s*$`, out, "the page's ScheduleClock")

	// ── pause.md ────────────────────────────────────────────────────────
	ps := "pause.md"
	w.must(ps, "kardinal pause my-app --reason", "my-app", "demo")
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: "default", Name: "demo"}, &p))
	assert.False(t, p.Spec.Paused, "the block pauses, then resumes")
	walkFleet(t, w)

	// ── agentic/connect-an-agent.md ─────────────────────────────────────
	walkAgent(t, w)

	// ── first-pipeline.md, on a clean demo ──────────────────────────────
	// A fork of its own: the quickstart's commits already pinned the
	// digest the page's first version adds, which would change nothing.
	e.MustKardinal(t, "", "quickstart", "--delete", "--yes")
	// Argo CD deletes the demo's Applications after their ApplicationSet:
	// the page creates new ones of the same names, which must not be the
	// old ones being deleted.
	framework.Eventually(t, 2*time.Minute, "the quickstart's Applications deleted", func(ctx context.Context) (bool, string) {
		apps := &unstructured.UnstructuredList{}
		apps.SetGroupVersionKind(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ApplicationList"})
		if err := e.Client.List(ctx, apps, client.InNamespace("argocd")); err != nil {
			return false, err.Error()
		}
		var left []string
		for _, a := range apps.Items {
			if strings.HasPrefix(a.GetName(), "kardinal-test-app-") {
				left = append(left, a.GetName())
			}
		}
		return len(left) == 0, fmt.Sprintf("left: %v", left)
	})
	w.repo = e.Repo(t, e.Namespace(t), kardinalDemoRepo())
	walkFirstPipeline(t, w)
}

// newestBundle is the newest Bundle of pipeline in ns.
func newestBundle(t *testing.T, e *framework.Env, ns, pipeline string) string {
	t.Helper()
	var bl v1alpha1.BundleList
	require.NoError(t, e.Client.List(context.Background(), &bl, client.InNamespace(ns)))
	newest := ""
	var at time.Time
	for _, b := range bl.Items {
		if b.Spec.Pipeline == pipeline && !b.CreationTimestamp.Time.Before(at) {
			newest, at = b.Name, b.CreationTimestamp.Time
		}
	}
	require.NotEmpty(t, newest, "%s has a version", pipeline)
	return newest
}

// commandLine is the one line of block that starts with prefix.
func commandLine(t *testing.T, block, prefix string) string {
	t.Helper()
	var found []string
	for _, l := range strings.Split(block, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), prefix) {
			found = append(found, strings.TrimSpace(l))
		}
	}
	require.Len(t, found, 1, "one line starting with %q in:\n%s", prefix, block)
	return found[0]
}

// deleteBundlesOf deletes the Bundles of the pipelines in ns.
func deleteBundlesOf(t *testing.T, e *framework.Env, ns string, pipelines ...string) {
	var bl v1alpha1.BundleList
	if err := e.Client.List(context.Background(), &bl, client.InNamespace(ns)); err != nil {
		t.Errorf("list Bundles: %v", err)
		return
	}
	for i := range bl.Items {
		for _, p := range pipelines {
			if bl.Items[i].Spec.Pipeline == p {
				_ = e.Client.Delete(context.Background(), &bl.Items[i])
			}
		}
	}
}

// walkFleet runs pause.md's fleet block on a small fleet Pipeline: test,
// then prod as two targets one at a time, with a bake, in its own namespace.
func walkFleet(t *testing.T, w *walkthrough) {
	t.Helper()
	e := w.e
	ctx := context.Background()
	seed, err := scale.Seed()
	require.NoError(t, err)
	f := scale.NewFleet(t, e, scale.NewRNG(seed, "docs-fleet"))
	repo := f.Repo(t, f.NS+"-my-app", map[string][]string{"my-app": {"test", "prod/eu", "prod/us"}})
	pl := f.PipelineSpec("my-app", repo, []v1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod",
		Bake:  &v1alpha1.BakeConfig{Minutes: 1},
		Fleet: &v1alpha1.FleetSpec{MaxConcurrent: 1, Targets: []v1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}}}}})
	f.Apply(t, pl, repo)
	b := f.MustCreateBundle(t, "my-app", scale.Tag("my-app", 1)).Name
	e.WaitStepState(t, f.NS, "my-app", b, "prod-eu", "HealthChecking", promoteTimeout)

	block := framework.PageBlock(t, "pause.md", "kardinal pause my-app --env prod")
	pause := framework.Fill(t, commandLine(t, block, "kardinal pause my-app --env prod"), "my-app", "my-app -n "+f.NS)
	resume := framework.Fill(t, commandLine(t, block, "kardinal resume my-app --env prod"), "my-app", "my-app -n "+f.NS)
	e.MustShell(t, pause)
	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: f.NS, Name: "my-app"}, &p))
	assert.Equal(t, []string{"prod"}, p.Spec.PausedFleets, "pause --env prod pauses the fleet")
	// The target in flight finishes; the next does not start.
	e.WaitStepState(t, f.NS, "my-app", b, "prod-eu", "Verified", promoteTimeout)
	framework.Consistently(t, 30*time.Second, "no further target of prod starts", func(ctx context.Context) (bool, string) {
		st, ok, err := e.Step(ctx, f.NS, "my-app", b, "prod-us")
		if err != nil {
			return false, err.Error()
		}
		return !ok || st.Status.State == "" || st.Status.State == "Pending", fmt.Sprintf("prod-us: %v", st)
	})
	e.MustShell(t, resume)
	e.WaitBundlePhase(t, f.NS, b, "Verified", 2*promoteTimeout)
}

// walkAgent builds the agent kubeconfig as connect-an-agent.md does, for the
// ServiceAccount of the page's RBAC in default, and calls kardinal mcp's
// read tools on demo as that identity.
func walkAgent(t *testing.T, w *walkthrough) {
	t.Helper()
	e := w.e
	ctx := context.Background()
	// The page's Role for the org gates is in platform-policies, the
	// controller's default --policy-namespaces.
	if err := e.Client.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "platform-policies"}}); err != nil &&
		!apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
	for _, obj := range agentRBAC(t, "default") {
		require.NoError(t, client.IgnoreAlreadyExists(e.Client.Create(ctx, obj)), "%s %s", obj.GetKind(), obj.GetName())
		t.Cleanup(func() { _ = e.Client.Delete(context.Background(), obj) })
	}
	w.must("agentic/connect-an-agent.md", "kubectl config set-credentials release-agent", "my-app", "default")
	kubeconfig := filepath.Join(w.dir, "agent.kubeconfig")
	_, err := os.Stat(kubeconfig)
	require.NoError(t, err, "the page's block writes agent.kubeconfig")
	cs := mcpSession(t, e.CLI(t).Path(), kubeconfig)
	for tool, args := range map[string]map[string]any{
		"status":   {"pipeline": "demo"},
		"why":      {"pipeline": "demo", "environment": "prod"},
		"history":  {"pipeline": "demo"},
		"explain":  {"pipeline": "demo", "environment": "prod"},
		"gates":    {"pipeline": "demo"},
		"inbox":    {},
		"simulate": {"pipeline": "demo", "environment": "prod"},
	} {
		res, body := mcpCall(t, cs, tool, args)
		assert.False(t, res.IsError, "%s as the agent: %v", tool, body)
	}
}

// walkFirstPipeline runs first-pipeline.md on the cluster quickstart --delete
// left: the ApplicationSet over the fork, the git Secret, kardinal init, a
// version, its prod PR and the merge, then the org gate.
func walkFirstPipeline(t *testing.T, w *walkthrough) {
	t.Helper()
	e := w.e
	ctx := context.Background()
	fp := "first-pipeline.md"
	const app = "kardinal-test-app"
	fork := "https://github.com/pnz1990/kardinal-demo"
	w.must(fp, "kind: ApplicationSet", fork, w.repo.CloneURL)
	w.must(fp, "kubectl wait --for=create -n argocd")
	w.must(fp, "--from-literal=token=<your-github-pat>", "<your-github-pat>", `"$GITHUB_TOKEN"`)
	// The suite's Forgejo serves http, which init takes with
	// --insecure-http (#1949).
	require.True(t, strings.HasPrefix(w.repo.CloneURL, "http://"), w.repo.CloneURL)
	out := w.must(fp, "kardinal init --name kardinal-test-app", fork, w.repo.CloneURL+" --insecure-http")
	w.commentSample(fp, "kardinal init --name kardinal-test-app", out)
	var initP v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: "default", Name: app}, &initP))
	assert.Equal(t, w.repo.CloneURL, initP.Spec.Git.URL, "init writes the fork it was given")
	out = w.must(fp, "kardinal-test-app   -         -")
	w.commentSample(fp, "kardinal-test-app   -         -", out, app)

	w.must(fp, "kardinal create bundle kardinal-test-app --image $TEST_IMAGE")
	var bl v1alpha1.BundleList
	require.NoError(t, e.Client.List(ctx, &bl, client.InNamespace("default")))
	bundle := ""
	for _, b := range bl.Items {
		if b.Spec.Pipeline == app {
			bundle = b.Name
		}
	}
	require.NotEmpty(t, bundle)
	for _, env := range []string{"test", "uat"} {
		e.WaitStepState(t, "default", app, bundle, env, "Verified", promoteTimeout)
	}
	e.WaitStepState(t, "default", app, bundle, "prod", "WaitingForMerge", promoteTimeout)
	// The PR is titled as the page says.
	page, err := os.ReadFile(filepath.Join(framework.RepoRoot(t), "docs", fp))
	require.NoError(t, err)
	m := regexp.MustCompile(`> \*\*(\[kardinal\] Promote )kardinal-test-app-[a-z0-9]{5}( to prod)\*\*`).FindSubmatch(page)
	require.NotNil(t, m, "the page shows the PR title")
	title := string(m[1]) + bundle + string(m[2])
	pr := e.WaitPR(t, w.repo, time.Minute, "the prod PR "+title, func(pr gitserver.PR) bool {
		return pr.State == "open" && pr.Title == title
	})
	require.NoError(t, e.Git.MergePR(ctx, w.repo, pr.Number))
	e.WaitBundlePhase(t, "default", bundle, "Verified", promoteTimeout)
	out = w.must(fp, "Live   Live   Live")
	w.commentSample(fp, "Live   Live   Live", out, app)

	// The org gate: the next version, shipped as the page ships it, gets it
	// at prod. explain shows the gate with its expression and the version
	// deployed there; its state is the day's (Blocking on a weekend, the
	// page's sample, or Passing). The weekend case runs on any day through
	// policy simulate --time, the page's way to see it.
	w.must(fp, "name: no-weekend-deploys")
	w.must(fp, "kardinal create bundle kardinal-test-app --image ghcr.io/pnz1990/kardinal-test-app:main")
	next := newestBundle(t, e, "default", app)
	require.NotEqual(t, bundle, next)
	weekend := time.Now().UTC().Weekday() == time.Saturday || time.Now().UTC().Weekday() == time.Sunday
	e.WaitGateReady(t, "default", next, "prod", "no-weekend-deploys", !weekend, "", promoteTimeout)
	if weekend {
		// The gate is not ready at once on a weekend, but explain says
		// Blocking only once it holds prod: once uat, prod's upstream, is
		// Verified for the version.
		e.WaitStepState(t, "default", app, next, "uat", "Verified", promoteTimeout)
	}
	const explain = "no-weekend-deploys   Blocking"
	out = w.must(fp, explain)
	if weekend {
		w.commentSample(fp, explain, out, app)
	}
	state := "Passing"
	if weekend {
		state = "Blocking"
	}
	page, err = os.ReadFile(filepath.Join(framework.RepoRoot(t), "docs", fp))
	require.NoError(t, err)
	require.Contains(t, string(page), "the gate's STATE is `"+"Passing"+"`", "the page names the weekday state")
	assert.Regexp(t, `(?m)^prod\s+main \(`+next+`\)\s+PolicyGate\s+no-weekend-deploys\s+`+state+`\s+!schedule\.isWeekend\s`, out,
		"explain shows the gate on the next version")
	assert.Regexp(t, `(?m)^prod\s+deployed: sha-9349a3f \(`+bundle+`\)\s*$`, out, "and the version deployed in prod")
	out = w.must(fp, "kardinal policy simulate --pipeline kardinal-test-app")
	w.commentSample(fp, "kardinal policy simulate --pipeline kardinal-test-app", out, app)

	// policy-gates.md's inspection block, on this Pipeline (its policy test
	// line needs a file the page does not write).
	pg := framework.PageBlock(t, "policy-gates.md", "# List all PolicyGates")
	r := e.Shell(t, commandLine(t, pg, "kardinal policy list"))
	require.Zero(t, r.Code, r.Output)
	assert.Contains(t, r.Output, "no-weekend-deploys", "policy list names the org gate")
	r = e.Shell(t, framework.Fill(t, commandLine(t, pg, "kardinal explain my-app"), "my-app", app))
	require.Zero(t, r.Code, r.Output)
	assert.Regexp(t, `(?m)^prod\s+main \(`+next+`\)\s+PolicyGate\s+no-weekend-deploys\s`, r.Output)
	r = e.Shell(t, framework.Fill(t, commandLine(t, pg, "kardinal policy simulate"), "my-app", app))
	require.Zero(t, r.Code, r.Output)
	assert.Contains(t, r.Output, "RESULT: BLOCKED\nBlocked by: no-weekend-deploys\n", "Saturday 3pm is a weekend")

}
