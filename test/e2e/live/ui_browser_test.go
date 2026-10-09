//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The browser tests. Each one sets the cluster up, runs one Playwright spec
// of web/test/e2e/live against the main release's UI through kubectl
// port-forward (the documented access path), and checks the cluster after
// the spec's clicks. The specs read what they need from KARDINAL_UI_*
// variables (browserEnv).

// browserEnv is the environment of a spec: the UI's URL, the test's
// namespace, and kv (name, value, name, value...).
func browserEnv(c framework.UIClient, ns string, kv ...string) map[string]string {
	env := map[string]string{"KARDINAL_UI_URL": c.BaseURL, "KARDINAL_UI_NAMESPACE": ns}
	for i := 0; i+1 < len(kv); i += 2 {
		env[kv[i]] = kv[i+1]
	}
	return env
}

// refusedKindMsg is the step message for refusedHealthEnv.
const refusedKindMsg = `health.resource.kind "StatefulSet" is not supported: only Deployment is checked`

// refusedHealthEnv is an environment whose health check kardinal refuses
// (only Deployment is checked): a Bundle's step there fails before it
// touches git, a quick way to a Failed step and a Degraded pipeline.
func refusedHealthEnv(name, ns string) v1alpha1.EnvironmentSpec {
	return v1alpha1.EnvironmentSpec{
		Name: name, Path: fixtures.Path(name), Approval: "auto",
		Update: v1alpha1.UpdateConfig{Strategy: "kustomize"},
		Health: v1alpha1.HealthConfig{Type: "resource", Timeout: "1m",
			Resource: &v1alpha1.ResourceRef{Kind: "StatefulSet", Name: fixtures.Workload(name), Namespace: ns}},
	}
}

// pipelineOver is Pipeline name in ns over repo with envs.
func pipelineOver(ns, name string, repo gitserver.Repo, envs ...v1alpha1.EnvironmentSpec) *v1alpha1.Pipeline {
	return &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{
				URL: repo.CloneURL, Branch: repo.Branch,
				SecretRef: &v1alpha1.SecretRef{Name: framework.GitSecretName},
			},
			Environments: envs,
		},
	}
}

// createBundle creates Bundle name (GenerateName pipeline- when name is
// empty) of pipeline in ns for image tag, and returns it.
func createBundle(t *testing.T, e *framework.Env, ns, pipeline, name, tag string, prov *v1alpha1.BundleProvenance) *v1alpha1.Bundle {
	t.Helper()
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: v1alpha1.BundleSpec{
			Type: "image", Pipeline: pipeline, Provenance: prov,
			Images: []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: tag}},
		},
	}
	if name == "" {
		b.GenerateName = pipeline + "-"
	}
	lifecycle.StampCreatedAt(b, time.Now())
	require.NoError(t, e.Client.Create(context.Background(), b))
	return b
}

// gateTemplate is a team PolicyGate in ns that guards env with expr.
func gateTemplate(ns, name, env, expr string) *v1alpha1.PolicyGate {
	return &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{
			"kardinal.io/scope":      "team",
			"kardinal.io/applies-to": env,
			"kardinal.io/type":       "gate",
		}},
		Spec: v1alpha1.PolicyGateSpec{Expression: expr, Message: name, RecheckInterval: "10s"},
	}
}

// TestUI_BrowserInsecureBanner checks the warning for plain HTTP from a
// non-loopback address, and its kubectl port-forward hint: on the main
// release's NodePort, where the API refuses the client and no pipeline ever
// loads; on kui-token's NodePort, while the UI asks for the token; and on an
// allowed host name in the pipeline view. The documented port-forward on
// 127.0.0.1 or localhost and kui-tls over https show none. The hint names
// the Service and port the chart creates.
//
// Covers UI-INSECURE-01.
func TestUI_BrowserInsecureBanner(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	repo := e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"test"}}))
	require.NoError(t, e.Client.Create(context.Background(), pipelineOver(ns, pipelineName, repo, refusedHealthEnv("test", ns))))
	c := mainUI(t, e)
	pf, err := url.Parse(c.BaseURL)
	require.NoError(t, err)

	// The hint: kubectl port-forward svc/kardinal-promoter -n kardinal-system 8082:8082.
	var svc corev1.Service
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: "kardinal-system", Name: "kardinal-promoter"}, &svc))
	var ports []int32
	for _, p := range svc.Spec.Ports {
		ports = append(ports, p.Port)
	}
	assert.Contains(t, ports, int32(8082), "the hint's Service has port 8082")

	framework.Playwright(t, "insecure.spec.ts", browserEnv(c, ns,
		"KARDINAL_UI_PORT", pf.Port(),
		"KARDINAL_UI_ALLOWED_HOST", framework.UIAllowedHost,
		"KARDINAL_UI_NODEPORT_URL", framework.MustEnv(t, framework.EnvUINodePortURL),
		"KARDINAL_UI_TOKEN_URL", framework.MustEnv(t, framework.EnvUITokenURL),
		"KARDINAL_UI_TLS_URL", framework.MustEnv(t, framework.EnvUITLSURL)))
}

// TestUI_BrowserDAG checks the promotion DAG of a Bundle that waits on its
// prod PR behind a passing gate: one node per environment and gate with its
// state, the PR badge, the hover tooltip with the PR link and the gate's
// expression. A Bundle of the same name in another namespace, of a Pipeline
// with other environments, must not leak into it: the UI asks for the graph
// and steps of the namespace on screen.
//
// Covers UI-DAG-01.
func TestUI_BrowserDAG(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	require.NoError(t, e.Client.Create(ctx, gateTemplate(a.ns, "check-prod", "prod", "true")))
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	const shared = "podinfo-shared"
	createBundle(t, e, a.ns, pipelineName, shared, fixtures.V2, nil)

	// The other namespace: Pipeline podinfo with one environment, solo, and a
	// Bundle of the same name that fails there.
	other := e.Namespace(t)
	repo := e.Repo(t, other, fixtures.KustomizeRepo(fixtures.App{Namespace: other, Envs: []string{"solo"}}))
	require.NoError(t, e.Client.Create(ctx, pipelineOver(other, pipelineName, repo, refusedHealthEnv("solo", other))))
	createBundle(t, e, other, pipelineName, shared, fixtures.V2, nil)

	e.WaitStepState(t, a.ns, pipelineName, shared, "test", "Verified", promoteTimeout)
	prod := e.WaitStepState(t, a.ns, pipelineName, shared, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	e.WaitStepState(t, other, pipelineName, shared, "solo", "Failed", promoteTimeout)
	c := mainUI(t, e)
	framework.Eventually(t, time.Minute, "the gate to pass", func(context.Context) (bool, string) {
		gates := gatesOf(t, c, a.ns, shared)
		return len(gates) == 1 && gates[0].State == "Pass", fmt.Sprintf("%+v", gates)
	})

	framework.Playwright(t, "dag.spec.ts", browserEnv(c, a.ns,
		"KARDINAL_UI_OTHER_NAMESPACE", other,
		"KARDINAL_UI_BUNDLE", shared,
		"KARDINAL_UI_GATE", "check-prod",
		"KARDINAL_UI_PR_URL", prod.Status.PRURL,
		"KARDINAL_UI_PR_NUMBER", strconv.Itoa(pr.Number)))
}

// listedPipeline is Pipeline name in ns as GET /pipelines lists it.
func listedPipeline(t *testing.T, c framework.UIClient, ns, name string) (uiPipeline, bool) {
	t.Helper()
	var list []uiPipeline
	getJSON(t, c, uiAPI+"/pipelines", &list)
	for _, p := range list {
		if p.Namespace == ns && p.Name == name {
			return p, true
		}
	}
	return uiPipeline{}, false
}

// TestUI_BrowserPipelineList checks the sidebar and the fleet health bar
// with three Pipelines in one namespace: podinfo, whose Bundle a gate holds
// before prod (Blocked); a paused one (PAUSED); and one whose only step
// failed (Degraded, 1 failed). The filter narrows the list by namespace and
// name; the fleet bar counts what the API lists, and its Blocked, CI Red and
// Healthy badges filter the list. Selecting a row opens that pipeline.
//
// It also checks the fleet board drawn from the same pipelines.
//
// Covers UI-LIST-01, UI-FLEET-01.
func TestUI_BrowserPipelineList(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	require.NoError(t, e.Client.Create(ctx, holdGate(a.ns, "prod")))
	a.apply(t, a.pipeline(nil))
	// The paused Pipeline never gets a Bundle, and the broken one fails before
	// it touches git, so both can share podinfo's repo.
	const paused, broken = pipelineName + "-paused", pipelineName + "-broken"
	pp := pipelineOver(a.ns, paused, a.repo, a.pipeline(nil).Spec.Environments[0])
	pp.Spec.Paused = true
	require.NoError(t, e.Client.Create(ctx, pp))
	require.NoError(t, e.Client.Create(ctx, pipelineOver(a.ns, broken, a.repo, refusedHealthEnv("solo", a.ns))))

	held := createBundle(t, e, a.ns, pipelineName, "", fixtures.V2, nil)
	failed := createBundle(t, e, a.ns, broken, "", fixtures.V2, nil)
	e.WaitStepState(t, a.ns, pipelineName, held.Name, "test", "Verified", promoteTimeout)
	e.WaitStepState(t, a.ns, broken, failed.Name, "solo", "Failed", promoteTimeout)
	c := mainUI(t, e)
	framework.Eventually(t, time.Minute, "the pipeline list to show the three states", func(context.Context) (bool, string) {
		h, _ := listedPipeline(t, c, a.ns, pipelineName)
		p, _ := listedPipeline(t, c, a.ns, paused)
		b, _ := listedPipeline(t, c, a.ns, broken)
		return h.Phase == "Promoting" && h.BlockerCount == 1 && !h.Paused &&
				p.Paused && p.EnvironmentCount == 1 &&
				b.Phase == "Degraded" && b.FailedStepCount == 1 && !b.Paused,
			fmt.Sprintf("held=%+v paused=%+v broken=%+v", h, p, b)
	})

	framework.Playwright(t, "list.spec.ts", browserEnv(c, a.ns,
		"KARDINAL_UI_PAUSED", paused,
		"KARDINAL_UI_BROKEN", broken))

	// The spec only reads: nothing moved.
	_, exists, err := e.Step(ctx, a.ns, pipelineName, held.Name, "prod")
	require.NoError(t, err)
	assert.False(t, exists, "the gate still holds prod")
	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: paused}, &p))
	assert.True(t, p.Spec.Paused)
}

// TestUI_BrowserGates checks the gate and failure views of the pipeline page.
// A Bundle that passed gate check-test into test and that the false gate
// hold-prod holds before prod shows the blocking banner, whose toggle
// highlights the holding gate in the DAG, and the Policy Gates panel with
// each gate instance's state, expression and reason. A Bundle that failed in
// its only environment shows the Promotion Errors panel, whose environment
// link opens that step's details.
//
// Covers UI-GATES-01.
func TestUI_BrowserGates(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	pass := gateTemplate(a.ns, "check-test", "test", "true")
	hold := holdGate(a.ns, "prod")
	require.NoError(t, e.Client.Create(ctx, pass))
	require.NoError(t, e.Client.Create(ctx, hold))
	a.apply(t, a.pipeline(nil))
	held := createBundle(t, e, a.ns, pipelineName, "", fixtures.V2, nil)

	failedNs := e.Namespace(t)
	repo := e.Repo(t, failedNs, fixtures.KustomizeRepo(fixtures.App{Namespace: failedNs, Envs: []string{"solo"}}))
	require.NoError(t, e.Client.Create(ctx, pipelineOver(failedNs, pipelineName, repo, refusedHealthEnv("solo", failedNs))))
	failed := createBundle(t, e, failedNs, pipelineName, "", fixtures.V2, nil)

	e.WaitStepState(t, a.ns, pipelineName, held.Name, "test", "Verified", promoteTimeout)
	step := e.WaitStepState(t, failedNs, pipelineName, failed.Name, "solo", "Failed", promoteTimeout)
	require.Equal(t, refusedKindMsg, step.Status.Message)
	require.Empty(t, step.Status.Steps, "the step was refused before any sub-step ran")
	c := mainUI(t, e)
	var passed, holding uiGate
	framework.Eventually(t, time.Minute, "check-test to pass and hold-prod to hold the Bundle", func(context.Context) (bool, string) {
		gates := gatesOf(t, c, a.ns, held.Name)
		for _, g := range gates {
			switch g.Environment {
			case "test":
				passed = g
			case "prod":
				holding = g
			}
		}
		return len(gates) == 2 && passed.State == "Pass" && holding.State == "Block" && holding.Holding,
			fmt.Sprintf("%+v", gates)
	})
	require.NotEmpty(t, holding.Reason, "the panel shows why the gate holds")

	framework.Playwright(t, "gates.spec.ts", browserEnv(c, a.ns,
		"KARDINAL_UI_HOLD_GATE", hold.Name,
		"KARDINAL_UI_HOLD_INSTANCE", holding.Name,
		"KARDINAL_UI_HOLD_REASON", holding.Reason,
		"KARDINAL_UI_PASS_GATE", pass.Name,
		"KARDINAL_UI_PASS_INSTANCE", passed.Name,
		"KARDINAL_UI_FAILED_NAMESPACE", failedNs,
		"KARDINAL_UI_FAILED_ENV", "solo",
		"KARDINAL_UI_FAILED_MESSAGE", refusedKindMsg))

	// The spec only reads: the gate still holds prod.
	_, exists, err := e.Step(ctx, a.ns, pipelineName, held.Name, "prod")
	require.NoError(t, err)
	assert.False(t, exists, "the gate still holds prod")
}

// TestUI_BrowserNoGateOverride checks that the web app cannot approve or
// override a gate: #1245 removed its Override gate dialog, and an override
// is made with `kardinal override` (the UI API's approve endpoint stays, see
// UIAPI-APPROVE-01). The scripts the controller serves are the whole app
// (no lazily loaded chunk) and never name the approve endpoint. In a
// browser, while gate hold-prod holds a Bundle, neither the Policy Gates
// panel nor the gate's node details offers an approve or override control,
// and the page sends no request but GETs and the node details' CEL check
// (POST validate-cel, which writes nothing). Afterwards the gate instance has
// no override and still holds prod.
//
// Covers UI-OVERRIDE-01.
func TestUI_BrowserNoGateOverride(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	hold := holdGate(a.ns, "prod")
	require.NoError(t, e.Client.Create(ctx, hold))
	a.apply(t, a.pipeline(nil))
	held := createBundle(t, e, a.ns, pipelineName, "", fixtures.V2, nil)
	e.WaitStepState(t, a.ns, pipelineName, held.Name, "test", "Verified", promoteTimeout)
	c := mainUI(t, e)
	var holding uiGate
	framework.Eventually(t, time.Minute, "hold-prod to hold the Bundle", func(context.Context) (bool, string) {
		gates := gatesOf(t, c, a.ns, held.Name)
		if len(gates) == 1 {
			holding = gates[0]
		}
		return len(gates) == 1 && holding.State == "Block" && holding.Holding, fmt.Sprintf("%+v", gates)
	})

	index := c.Get(t, "/ui/")
	require.Equal(t, http.StatusOK, index.Status, "GET /ui/: %s", index)
	scripts := regexp.MustCompile(`<script[^>]*\ssrc="(/ui/[^"]+\.js)"`).FindAllStringSubmatch(index.Body, -1)
	require.NotEmpty(t, scripts, "index.html names no script: %s", index.Body)
	for _, m := range scripts {
		r := c.Get(t, m[1])
		require.Equal(t, http.StatusOK, r.Status, "GET %s: %s", m[1], r)
		assert.NotContains(t, r.Body, "import(", "%s loads a chunk this test does not read", m[1])
		assert.NotContains(t, r.Body, "/approve", "%s calls the approve endpoint", m[1])
	}

	framework.Playwright(t, "override.spec.ts", browserEnv(c, a.ns,
		"KARDINAL_UI_HOLD_GATE", hold.Name,
		"KARDINAL_UI_HOLD_INSTANCE", holding.Name))

	var inst v1alpha1.PolicyGate
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: holding.Name}, &inst))
	assert.Empty(t, inst.Spec.Overrides, "no override on the gate instance")
	_, exists, err := e.Step(ctx, a.ns, pipelineName, held.Name, "prod")
	require.NoError(t, err)
	assert.False(t, exists, "the gate still holds prod")
}

// subSteps is a PromotionStep's status.steps as the node.spec.ts reads them.
func subSteps(t *testing.T, ps *v1alpha1.PromotionStep) string {
	t.Helper()
	type sub struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	var out []sub
	for _, s := range ps.Status.Steps {
		out = append(out, sub{s.Name, string(s.State)})
	}
	require.NotEmpty(t, out, "step %s has no status.steps", ps.Name)
	b, err := json.Marshal(out)
	require.NoError(t, err)
	return string(b)
}

// TestUI_BrowserNodeDetail checks the node details panel. The prod step of a
// Bundle that waits on its PR shows its sub-steps with their states (the
// running one "waiting for merge"), an elapsed time that ticks, the merge
// link and its events; the test step shows its finished sub-steps and its
// events. A gate shows its CEL expression checked as valid; a gate whose
// expression does not compile shows the compile error. The test step's
// finished sub-steps are timing bars in the order they ran.
//
// Covers UI-NODE-01, UI-STEPTIME-01.
func TestUI_BrowserNodeDetail(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	require.NoError(t, e.Client.Create(ctx, gateTemplate(a.ns, "check-prod", "prod", "true")))
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	// A second Pipeline, whose only environment a gate that does not compile
	// holds: its step never starts (and would fail before git if it did), so
	// it can share podinfo's repo.
	const celPipeline, badExpr = pipelineName + "-cel", "nosuch.thing == 1"
	bad := gateTemplate(a.ns, "bad-cel", "solo", badExpr)
	require.NoError(t, e.Client.Create(ctx, bad))
	require.NoError(t, e.Client.Create(ctx, pipelineOver(a.ns, celPipeline, a.repo, refusedHealthEnv("solo", a.ns))))
	b := createBundle(t, e, a.ns, pipelineName, "", fixtures.V2, nil)
	cb := createBundle(t, e, a.ns, celPipeline, "", fixtures.V2, nil)

	testStep := e.WaitStepState(t, a.ns, pipelineName, b.Name, "test", "Verified", promoteTimeout)
	prodStep := e.WaitStepState(t, a.ns, pipelineName, b.Name, "prod", "WaitingForMerge", promoteTimeout)
	e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	var running int
	for _, s := range prodStep.Status.Steps {
		if s.State == "InProgress" {
			running++
		}
	}
	require.Equal(t, 1, running, "prod has one running sub-step: %+v", prodStep.Status.Steps)
	c := mainUI(t, e)
	framework.Eventually(t, time.Minute, "the steps' events", func(context.Context) (bool, string) {
		_, waiting := eventWith(eventsOf(t, c, a.ns, prodStep.Name), "WaitingForMerge", prodStep.Status.PRURL)
		_, verified := eventWith(eventsOf(t, c, a.ns, testStep.Name), "Verified", "env test: step completed successfully")
		return waiting && verified, fmt.Sprintf("waiting=%v verified=%v", waiting, verified)
	})
	framework.Eventually(t, time.Minute, "the gates to be evaluated", func(context.Context) (bool, string) {
		good := gatesOf(t, c, a.ns, b.Name)
		bad := gatesOf(t, c, a.ns, cb.Name)
		return len(good) == 1 && good[0].State == "Pass" && len(bad) == 1 && !bad[0].Ready && bad[0].Holding,
			fmt.Sprintf("good=%+v bad=%+v", good, bad)
	})

	framework.Playwright(t, "node.spec.ts", browserEnv(c, a.ns,
		"KARDINAL_UI_GATE", "check-prod",
		"KARDINAL_UI_CEL_PIPELINE", celPipeline,
		"KARDINAL_UI_BAD_GATE", bad.Name,
		"KARDINAL_UI_BAD_EXPRESSION", badExpr,
		"KARDINAL_UI_PR_URL", prodStep.Status.PRURL,
		"KARDINAL_UI_TEST_STEPS", subSteps(t, testStep),
		"KARDINAL_UI_PROD_STEPS", subSteps(t, prodStep)))

	// The spec only reads: prod still waits on its PR, and nothing promoted
	// the held Bundle.
	ps, _, err := e.Step(ctx, a.ns, pipelineName, b.Name, "prod")
	require.NoError(t, err)
	assert.Equal(t, "WaitingForMerge", ps.Status.State)
	_, exists, err := e.Step(ctx, a.ns, celPipeline, cb.Name, "solo")
	require.NoError(t, err)
	assert.False(t, exists, "the gate that does not compile holds solo")
}

// TestUI_BrowserActions checks the pipeline actions, each clicked in the UI
// and confirmed in its dialog, against the cluster after each click. Pause
// in the action bar pauses podinfo (spec.paused, the freeze gate, the Paused
// condition), and the header and the sidebar say PAUSED; Resume undoes it.
// Promote in the stage lane, offered for prod and not for the first
// environment, promotes the Bundle that stopped at test, and prod then runs
// its version. Roll back in prod's node details creates a rollback Bundle
// that puts prod back on the Bundle before. Cancel in a dialog changes
// nothing.
//
// Covers UI-ACTIONS-01.
func TestUI_BrowserActions(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))
	first := createBundle(t, e, a.ns, pipelineName, "", fixtures.V2, nil)
	e.WaitStepState(t, a.ns, pipelineName, first.Name, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, first.Name, "Verified", time.Minute)

	// A Bundle that stops at test: prod can be promoted.
	held := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{GenerateName: pipelineName + "-", Namespace: a.ns},
		Spec: v1alpha1.BundleSpec{
			Type: "image", Pipeline: pipelineName,
			Images: []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V3}},
			Intent: &v1alpha1.BundleIntent{TargetEnvironment: "test"},
		},
	}
	lifecycle.StampCreatedAt(held, time.Now())
	require.NoError(t, e.Client.Create(ctx, held))
	e.WaitStepState(t, a.ns, pipelineName, held.Name, "test", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, held.Name, "Verified", time.Minute)
	_, exists, err := e.Step(ctx, a.ns, pipelineName, held.Name, "prod")
	require.NoError(t, err)
	require.False(t, exists, "the Bundle stops at test")

	c := mainUI(t, e)
	key := types.NamespacedName{Namespace: a.ns, Name: pipelineName}
	freezeKey := types.NamespacedName{Namespace: a.ns, Name: "freeze-" + pipelineName}
	click := func(t *testing.T, action string) {
		framework.Playwright(t, "actions.spec.ts", browserEnv(c, a.ns, "KARDINAL_UI_ACTION", action))
	}
	bundles := func(t *testing.T) []v1alpha1.Bundle {
		var l v1alpha1.BundleList
		require.NoError(t, e.Client.List(ctx, &l, client.InNamespace(a.ns)))
		return l.Items
	}
	// Each step needs the one before it.
	step := func(name string, f func(t *testing.T)) {
		if !t.Run(name, f) {
			t.FailNow()
		}
	}

	step("pause", func(t *testing.T) {
		click(t, "pause")
		framework.Eventually(t, time.Minute, "podinfo paused", func(ctx context.Context) (bool, string) {
			var p v1alpha1.Pipeline
			if err := e.Client.Get(ctx, key, &p); err != nil {
				return false, err.Error()
			}
			cond := metaCondition(p.Status.Conditions, "Paused")
			var freeze v1alpha1.PolicyGate
			err := e.Client.Get(ctx, freezeKey, &freeze)
			return p.Spec.Paused && cond == "True/FreezeGateActive" && err == nil,
				fmt.Sprintf("spec.paused=%v Paused=%s freeze gate: %v", p.Spec.Paused, cond, err)
		})
	})

	step("resume", func(t *testing.T) {
		click(t, "resume")
		var p v1alpha1.Pipeline
		require.NoError(t, e.Client.Get(ctx, key, &p))
		assert.False(t, p.Spec.Paused)
		framework.Eventually(t, time.Minute, "the freeze gate deleted", func(ctx context.Context) (bool, string) {
			var freeze v1alpha1.PolicyGate
			err := e.Client.Get(ctx, freezeKey, &freeze)
			return apierrors.IsNotFound(err), fmt.Sprintf("freeze gate: %v", err)
		})
	})

	var promoted string
	step("promote", func(t *testing.T) {
		click(t, "promote")
		bs := bundles(t)
		require.Len(t, bs, 3, "one Bundle for the confirmed Promote, none for the cancelled one")
		for _, b := range bs {
			if b.Annotations[lifecycle.AnnotationPromotedFrom] == held.Name {
				promoted = b.Name
				assert.Equal(t, held.Spec.Images, b.Spec.Images)
				require.NotNil(t, b.Spec.Intent)
				assert.Equal(t, "prod", b.Spec.Intent.TargetEnvironment)
			}
		}
		require.NotEmpty(t, promoted, "a Bundle promoted from %s: %+v", held.Name, bs)
		e.WaitStepState(t, a.ns, pipelineName, promoted, "prod", "Verified", promoteTimeout)
		assert.Equal(t, fixtures.Image+":"+fixtures.V3, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
		e.WaitBundlePhase(t, a.ns, promoted, "Verified", time.Minute)
	})

	step("rollback", func(t *testing.T) {
		click(t, "rollback")
		bs := bundles(t)
		require.Len(t, bs, 4, "one rollback Bundle")
		var rolled *v1alpha1.Bundle
		for i := range bs {
			if bs[i].Labels[lifecycle.LabelRollback] == "true" {
				rolled = &bs[i]
			}
		}
		require.NotNil(t, rolled, "a rollback Bundle: %+v", bs)
		assert.Equal(t, promoted, rolled.Annotations[lifecycle.AnnotationRollbackFrom], "the Bundle prod ran")
		require.NotNil(t, rolled.Spec.Provenance)
		assert.Equal(t, first.Name, rolled.Spec.Provenance.RollbackOf)
		e.WaitStepState(t, a.ns, pipelineName, rolled.Name, "prod", "Verified", promoteTimeout)
		assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	})
}

// TestUI_BrowserCreateBundle checks the Create Bundle dialog. It asks for an
// image before it sends anything, shows the API's reason when the Bundle is
// refused and stays open, and Cancel closes it; none of these creates a
// Bundle. The Bundle created with an image, a commit SHA and an author shows
// on screen with its provenance, and promotes: test runs the image.
//
// Covers UI-CREATE-01.
func TestUI_BrowserCreateBundle(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	c := mainUI(t, e)
	const sha, author = "3f2a9c1d7e5b4a6c8d0e2f4a6b8c0d2e4f6a8b0c", "ui-e2e"
	image := fixtures.Image + ":" + fixtures.V2

	framework.Playwright(t, "create.spec.ts", browserEnv(c, a.ns,
		"KARDINAL_UI_IMAGE", image,
		"KARDINAL_UI_COMMIT_SHA", sha,
		"KARDINAL_UI_AUTHOR", author))

	var l v1alpha1.BundleList
	require.NoError(t, e.Client.List(ctx, &l, client.InNamespace(a.ns)))
	require.Len(t, l.Items, 1, "the refused and cancelled attempts create no Bundle")
	b := l.Items[0]
	assert.Equal(t, pipelineName, b.Spec.Pipeline)
	assert.Equal(t, pipelineName, b.Labels["kardinal.io/pipeline"])
	assert.Equal(t, "image", b.Spec.Type)
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}, b.Spec.Images)
	require.NotNil(t, b.Spec.Provenance)
	assert.Equal(t, sha, b.Spec.Provenance.CommitSHA)
	assert.Equal(t, author, b.Spec.Provenance.Author)
	e.WaitStepState(t, a.ns, pipelineName, b.Name, "test", "Verified", promoteTimeout)
	assert.Equal(t, image, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
	e.WaitBundlePhase(t, a.ns, b.Name, "Verified", time.Minute)
}

// timelineEntry is what timeline.spec.ts knows about a Bundle.
type timelineEntry struct {
	Name         string `json:"name"`
	Phase        string `json:"phase"`
	Image        string `json:"image"`
	Author       string `json:"author"`
	CommitSHA    string `json:"commitSHA"`
	Environments string `json:"environments"`
	Step         string `json:"step"`
}

// TestUI_BrowserTimeline checks the bundle timeline and the comparison
// panel. The timeline lists podinfo's two Bundles newest first, each with
// its phase, and shows the newest. A click shows the older one (its
// provenance and its step in the DAG), which stays on screen across polls.
// Shift-clicking the newer one opens the comparison of the two, the one on
// screen as A: the images, authors and commit SHAs differ, and the other
// fields print the same on both sides. Close and Esc close it; a link with
// bundle= opens it. The Bundle card names the Bundle type, and the timeline
// is one Tab stop moved through with the arrow keys.
//
// Covers UI-TIMELINE-01, UI-BUNDLETYPE-01, UI-KEYBOARD-01.
func TestUI_BrowserTimeline(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))

	var entries []timelineEntry
	for _, v := range []struct{ tag, author, sha string }{
		{fixtures.V2, "alice", "1111111111aaaaaaaaaa2222222222bbbbbbbbbb"},
		{fixtures.V3, "bob", "3333333333cccccccccc4444444444dddddddddd"},
	} {
		// One after the other, so neither supersedes the other.
		b := createBundle(t, e, a.ns, pipelineName, "", v.tag, &v1alpha1.BundleProvenance{Author: v.author, CommitSHA: v.sha})
		ps := e.WaitStepState(t, a.ns, pipelineName, b.Name, "test", "Verified", promoteTimeout)
		e.WaitBundlePhase(t, a.ns, b.Name, "Verified", time.Minute)
		entries = append(entries, timelineEntry{
			Name: b.Name, Image: fixtures.Image + ":" + v.tag, Author: v.author, CommitSHA: v.sha, Step: ps.Name,
		})
	}
	// The phases and environments as they are now; the panel prints them.
	for i := range entries {
		var b v1alpha1.Bundle
		require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: entries[i].Name}, &b))
		require.Len(t, b.Status.Environments, 1, "%s: %+v", b.Name, b.Status.Environments)
		entries[i].Phase = b.Status.Phase
		entries[i].Environments = b.Status.Environments[0].Name + ": " + b.Status.Environments[0].Phase
	}
	assert.Equal(t, "Verified", entries[0].Phase, "a newer Bundle does not supersede a Verified one")
	assert.Equal(t, "test: Verified", entries[0].Environments)
	bundles, err := json.Marshal(entries)
	require.NoError(t, err)

	framework.Playwright(t, "timeline.spec.ts", browserEnv(mainUI(t, e), a.ns, "KARDINAL_UI_BUNDLES", string(bundles)))
}

// TestUI_BrowserShell checks what every page of the UI has: the keyboard
// shortcuts (/, ?, r, Esc) and their help; the dark and light themes, which
// follow the system until the user picks one; links that open a pipeline
// and a node, with Back and Forward through the selections; and the 5 s
// polling, whose indicator tells how old the data is and why it is old (an
// API that never answers, one that fails). podinfo has one Verified Bundle;
// a second pipeline has none, and the spec pauses it through the API while
// the page is open, which the page shows without a reload. The first Tab
// stop skips to the main content, and a fleet line is one Tab stop.
//
// Covers UI-SHELL-01, UI-KEYBOARD-01.
func TestUI_BrowserShell(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	// It never gets a Bundle, so it can share podinfo's repo.
	const idle = pipelineName + "-idle"
	require.NoError(t, e.Client.Create(ctx, pipelineOver(a.ns, idle, a.repo, a.pipeline(nil).Spec.Environments[0])))
	b := createBundle(t, e, a.ns, pipelineName, "", fixtures.V2, nil)
	ps := e.WaitStepState(t, a.ns, pipelineName, b.Name, "test", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, b.Name, "Verified", time.Minute)

	framework.Playwright(t, "shell.spec.ts", browserEnv(mainUI(t, e), a.ns,
		"KARDINAL_UI_TEST_STEP", ps.Name,
		"KARDINAL_UI_IDLE_PIPELINE", idle))

	var p v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: idle}, &p))
	assert.True(t, p.Spec.Paused, "the spec paused %s through the API", idle)
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &p))
	assert.False(t, p.Spec.Paused, "nothing paused %s", pipelineName)
}

// TestUI_BrowserStatic checks the web app the controller embeds, over the
// documented kubectl port-forward. /ui/ is the app's index.html; every
// script, stylesheet and image it names is served with its type; /ui
// redirects to /ui/; a directory is not listed and a missing file is 404.
// In a browser the app starts with no failed request and no console error,
// shows its logo and the landing page, and lists the test's pipeline.
//
// Covers UI-STATIC-01.
func TestUI_BrowserStatic(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	c := mainUI(t, e)

	index := c.Get(t, "/ui/")
	require.Equal(t, http.StatusOK, index.Status, "GET /ui/: %s", index)
	assert.Contains(t, index.Header.Get("Content-Type"), "text/html")
	assert.Contains(t, index.Body, `<div id="root"></div>`)
	assert.Contains(t, index.Body, "<title>Kardinal Promoter</title>")

	// The Dockerfile builds web/ itself, so the asset names are the image's,
	// not the checkout's web/dist: read them from index.html.
	types := map[string]string{".js": "javascript", ".css": "text/css", ".png": "image/png"}
	named := map[string]int{}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?:src|href)="(/ui/[^"]+)"`).FindAllStringSubmatch(index.Body, -1) {
		p := m[1]
		if seen[p] {
			continue
		}
		seen[p] = true
		ext := path.Ext(p)
		want, ok := types[ext]
		if !assert.True(t, ok, "index.html names %s, of no type the app uses", p) {
			continue
		}
		named[ext]++
		r := c.Get(t, p)
		if assert.Equal(t, http.StatusOK, r.Status, "GET %s: %s", p, r) {
			assert.Contains(t, r.Header.Get("Content-Type"), want, "GET %s", p)
			assert.NotEmpty(t, r.Body, "GET %s", p)
		}
	}
	for ext := range types {
		assert.Positive(t, named[ext], "index.html names no %s file: %s", ext, index.Body)
	}

	noFollow := c
	noFollow.HTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	r := noFollow.Get(t, "/ui")
	// ServeMux's subtree redirect; its status (307 today) is Go's choice.
	assert.True(t, r.Status >= 300 && r.Status < 400, "GET /ui redirects: %s", r)
	assert.Equal(t, "/ui/", r.Header.Get("Location"), "GET /ui")
	for _, p := range []string{"/ui/assets/", "/ui/no-such-file.js"} {
		r := c.Get(t, p)
		assert.Equal(t, http.StatusNotFound, r.Status, "GET %s: %s", p, r)
	}

	framework.Playwright(t, "static.spec.ts", browserEnv(c, a.ns))
}

// TestUI_BrowserApprovals checks an approval gate and a rejected Bundle in
// the UI. podinfo's prod waits at a gate that needs two approvals from
// release-managers; one allowed person approves with the CLI and one outside
// the group does too. The pipeline view shows 1 of 2, which decision counts
// and which does not and why, and the approve command. The Bundle is then
// rejected with the CLI: the view says who rejected it and why, its chip is
// Rejected, and the gate no longer holds it.
//
// Covers UI-APPROVALS-01, UI-REJECTED-01.
func TestUI_BrowserApprovals(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	a := newArgoApp(t, e, "test", "prod")
	g := framework.Gate(a.ns, "two-approvers", "prod", "true", recheck)
	g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 2, AllowedGroups: []string{"release-managers"}, ExcludeAuthor: true}
	e.CreateGate(t, g)
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	e.WaitGateReady(t, a.ns, bundle, "prod", "two-approvers", false, "waiting for approvals: 0 of 2", gateTimeout)

	const approver, outsider = "alice@example.com", "mallory@example.com"
	as := func(path string, args ...string) {
		t.Helper()
		r := c.Exec(framework.CLIOptions{Kubeconfig: path}, c.Args(a.ns, args...)...)
		require.Equal(t, 0, r.Code, r.Output())
	}
	as(userKubeconfig(t, e, a.ns, approver, "release-managers"), "approve", bundle, "--env", "prod", "--comment", "canary looks clean")
	as(userKubeconfig(t, e, a.ns, outsider, "devs"), "approve", bundle, "--env", "prod")
	e.WaitGate(t, a.ns, bundle, "prod", "two-approvers", gateTimeout, "two decisions, one counted", func(g *v1alpha1.PolicyGate) bool {
		return len(g.Status.Approvals) == 2 && !g.Status.Ready
	})
	framework.Playwright(t, "approvals.spec.ts", browserEnv(mainUI(t, e), a.ns,
		"KARDINAL_UI_BUNDLE", bundle, "KARDINAL_UI_APPROVER", approver, "KARDINAL_UI_OUTSIDER", outsider))

	const reason = "e2e: CVE in the base image"
	e.MustKardinal(t, a.ns, "reject", bundle, "--reason", reason)
	e.WaitBundlePhase(t, a.ns, bundle, "Rejected", time.Minute)
	framework.Playwright(t, "rejected.spec.ts", browserEnv(mainUI(t, e), a.ns,
		"KARDINAL_UI_BUNDLE", bundle, "KARDINAL_UI_REJECTER", whoAmI(t, e), "KARDINAL_UI_REASON", reason))
}
