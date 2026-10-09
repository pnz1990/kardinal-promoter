//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// uiAPI is the UI API's path prefix.
const uiAPI = "/api/v1/ui"

// The UI API's JSON, as cmd/kardinal-controller/ui_api.go writes it.
// encoding/json matches field names case-insensitively, so the structs list
// only the fields the tests read.
type (
	uiPipeline struct {
		Name, Namespace, Phase, ActiveBundleName string
		EnvironmentCount                         int
		Paused                                   bool
		EnvironmentStates                        map[string]string
		EnvironmentTopology                      []struct {
			Name      string
			DependsOn []string
			Approval  string
		}
		BlockerCount, FailedStepCount int
	}
	uiBundle struct {
		Name, Namespace, Phase, Type, Pipeline, CreatedAt string
		Provenance                                        *v1alpha1.BundleProvenance
		Environments                                      []struct{ Name, Phase, PRURL string }
		Images                                            []v1alpha1.ImageRef
	}
	uiNode struct {
		ID, Type, Label, Environment, State, Message, PRURL, Expression, StartedAt string
		Holding                                                                    bool
	}
	uiEdge  struct{ From, To string }
	uiGraph struct {
		Nodes []uiNode
		Edges []uiEdge
	}
	uiStep struct {
		Name, Namespace, Pipeline, Bundle, Environment, State, Message, PRURL string
		Steps                                                                 []struct{ Name, State string }
	}
	uiGate struct {
		Name, Namespace, Expression, Reason, Pipeline, Bundle, Environment, State string
		Ready, Template, Holding                                                  bool
		Overrides                                                                 []struct{ Reason, ExpiresAt, CreatedBy string }
	}
	uiEvent struct {
		Type, Reason, Message, FirstTimestamp, LastTimestamp string
		Count                                                int32
	}
)

// The UI server's refusals (cmd/kardinal-controller/ui_peer.go, ui_hosts.go).
const (
	uiPeerNotLocal = "UI API: no UI auth mode is set, so only local clients (kubectl port-forward) are served; " +
		"set Helm value ui.auth.tokenReview=true or ui.auth.tokenSecretRef.name (flags --ui-tokenreview-auth, --ui-auth-token)"
	uiHostNotAllowed = "UI API: host not allowed; add it to --ui-allowed-hosts (Helm value ui.allowedHosts)"
	uiAuthRealm      = `Bearer realm="kardinal-ui"`
)

// mainUI is a client for the main release's UI through kubectl port-forward,
// so the controller sees a loopback peer, as with a user's port-forward. The
// main release runs with no UI auth mode.
func mainUI(t *testing.T, e *framework.Env) framework.UIClient {
	t.Helper()
	return framework.UIClient{BaseURL: e.PortForward(t, framework.ControllerNamespace, framework.ControllerService, framework.UIPort)}
}

// getJSON GETs path, requires 200 and decodes the body into v.
func getJSON(t *testing.T, c framework.UIClient, path string, v any) {
	t.Helper()
	r := c.Get(t, path)
	require.Equal(t, http.StatusOK, r.Status, "GET %s: %s", path, r)
	r.JSON(t, v)
}

// uiPipelineIn is the test's Pipeline as GET /pipelines lists it.
func uiPipelineIn(t *testing.T, c framework.UIClient, ns string) (uiPipeline, bool) {
	t.Helper()
	var list []uiPipeline
	getJSON(t, c, uiAPI+"/pipelines", &list)
	for _, p := range list {
		if p.Namespace == ns && p.Name == pipelineName {
			return p, true
		}
	}
	return uiPipeline{}, false
}

// graphOf is GET /bundles/{bundle}/graph?namespace=ns.
func graphOf(t *testing.T, c framework.UIClient, ns, bundle string) uiGraph {
	t.Helper()
	var g uiGraph
	getJSON(t, c, uiAPI+"/bundles/"+bundle+"/graph?namespace="+ns, &g)
	return g
}

// node is the graph's node of type typ for env.
func (g uiGraph) node(typ, env string) (uiNode, bool) {
	for _, n := range g.Nodes {
		if n.Type == typ && n.Environment == env {
			return n, true
		}
	}
	return uiNode{}, false
}

// gatesOf is GET /gates, narrowed to the gate instances of bundle in ns.
func gatesOf(t *testing.T, c framework.UIClient, ns, bundle string) []uiGate {
	t.Helper()
	var all []uiGate
	getJSON(t, c, uiAPI+"/gates", &all)
	var out []uiGate
	for _, g := range all {
		if g.Namespace == ns && g.Bundle == bundle {
			out = append(out, g)
		}
	}
	return out
}

// eventsOf is GET /steps/{ns}/{step}/events.
func eventsOf(t *testing.T, c framework.UIClient, ns, step string) []uiEvent {
	t.Helper()
	var evs []uiEvent
	getJSON(t, c, uiAPI+"/steps/"+ns+"/"+step+"/events", &evs)
	return evs
}

// eventWith is the first event of evs with reason whose message contains msg.
func eventWith(evs []uiEvent, reason, msg string) (uiEvent, bool) {
	for _, ev := range evs {
		if ev.Reason == reason && strings.Contains(ev.Message, msg) {
			return ev, true
		}
	}
	return uiEvent{}, false
}

// TestUI_APIReadsFollowPromotion reads one promotion through the UI API while
// it waits on its prod PR and again after the PR merges: the pipeline list
// (environment states and topology), the pipeline's Bundles, the Bundle's
// graph with the PR link, its steps, and the steps' events. Each must match
// what kardinal wrote to the cluster, and follow it when it changes.
//
// Covers UIAPI-PIPELINES-01, UIAPI-BUNDLES-01, UIAPI-GRAPH-01, UIAPI-STEPS-01,
// UIAPI-EVENTS-01.
func TestUI_APIReadsFollowPromotion(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(map[string]string{"prod": "pr-review"}))
	c := mainUI(t, e)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2,
		"--commit", "0123abc", "--author", "e2e-bot")
	testStep := e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	prodStep := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "WaitingForMerge", promoteTimeout)
	pr := e.WaitPR(t, a.repo, time.Minute, "prod promotion PR", func(pr gitserver.PR) bool { return pr.State == "open" })
	prURL := prodStep.Status.PRURL
	require.True(t, strings.HasSuffix(prURL, fmt.Sprintf("/%d", pr.Number)), "prod step PR URL %q names PR #%d", prURL, pr.Number)

	// GET /pipelines
	framework.Eventually(t, time.Minute, "the pipeline list to show test Verified and prod WaitingForMerge", func(context.Context) (bool, string) {
		p, ok := uiPipelineIn(t, c, a.ns)
		return ok && p.EnvironmentStates["test"] == "Verified" && p.EnvironmentStates["prod"] == "WaitingForMerge",
			fmt.Sprintf("listed=%v %+v", ok, p)
	})
	p, _ := uiPipelineIn(t, c, a.ns)
	var live v1alpha1.Pipeline
	require.NoError(t, e.Client.Get(context.Background(), types.NamespacedName{Namespace: a.ns, Name: pipelineName}, &live))
	assert.Equal(t, 2, p.EnvironmentCount)
	assert.Equal(t, bundle, p.ActiveBundleName)
	assert.False(t, p.Paused)
	assert.Zero(t, p.BlockerCount)
	assert.Zero(t, p.FailedStepCount)
	assert.NotEmpty(t, p.Phase)
	require.Len(t, p.EnvironmentTopology, 2)
	assert.Equal(t, "test", p.EnvironmentTopology[0].Name)
	assert.Equal(t, "auto", p.EnvironmentTopology[0].Approval)
	assert.Equal(t, "prod", p.EnvironmentTopology[1].Name)
	assert.Equal(t, "pr-review", p.EnvironmentTopology[1].Approval)

	// GET /pipelines/{p}/bundles
	var bundles []uiBundle
	getJSON(t, c, uiAPI+"/pipelines/"+pipelineName+"/bundles?namespace="+a.ns, &bundles)
	require.Len(t, bundles, 1, "the pipeline has one Bundle")
	b := bundles[0]
	assert.Equal(t, bundle, b.Name)
	assert.Equal(t, a.ns, b.Namespace)
	assert.Equal(t, pipelineName, b.Pipeline)
	assert.Equal(t, "image", b.Type)
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}, b.Images)
	require.NotNil(t, b.Provenance)
	assert.Equal(t, "0123abc", b.Provenance.CommitSHA)
	assert.Equal(t, "e2e-bot", b.Provenance.Author)
	_, err := time.Parse(time.RFC3339, b.CreatedAt)
	assert.NoError(t, err, "createdAt %q", b.CreatedAt)
	envs := map[string]string{}
	for _, env := range b.Environments {
		envs[env.Name] = env.Phase
		if env.Name == "prod" {
			assert.Equal(t, prURL, env.PRURL, "the Bundle's prod environment links the PR")
		}
	}
	assert.Equal(t, map[string]string{"test": "Verified", "prod": "WaitingForMerge"}, envs)
	var other []uiBundle
	getJSON(t, c, uiAPI+"/pipelines/"+pipelineName+"/bundles?namespace="+framework.ControllerNamespace, &other)
	assert.Empty(t, other, "?namespace= narrows the list to that namespace")

	// GET /bundles/{b}/graph
	g := graphOf(t, c, a.ns, bundle)
	require.Len(t, g.Nodes, 2, "%+v", g.Nodes)
	tn, ok := g.node("PromotionStep", "test")
	require.True(t, ok, "%+v", g.Nodes)
	assert.Equal(t, testStep.Name, tn.ID)
	assert.Equal(t, "test", tn.Label)
	assert.Equal(t, "Verified", tn.State)
	assert.NotEmpty(t, tn.StartedAt)
	pn, ok := g.node("PromotionStep", "prod")
	require.True(t, ok, "%+v", g.Nodes)
	assert.Equal(t, prodStep.Name, pn.ID)
	assert.Equal(t, "WaitingForMerge", pn.State)
	assert.Equal(t, prURL, pn.PRURL, "the prod node links the PR")
	assert.Equal(t, []uiEdge{{From: testStep.Name, To: prodStep.Name}}, g.Edges)

	// GET /bundles/{b}/steps
	var steps []uiStep
	getJSON(t, c, uiAPI+"/bundles/"+bundle+"/steps?namespace="+a.ns, &steps)
	require.Len(t, steps, 2, "%+v", steps)
	byEnv := map[string]uiStep{}
	for _, s := range steps {
		byEnv[s.Environment] = s
		assert.Equal(t, a.ns, s.Namespace)
		assert.Equal(t, pipelineName, s.Pipeline)
		assert.Equal(t, bundle, s.Bundle)
	}
	assert.Equal(t, testStep.Name, byEnv["test"].Name)
	assert.Equal(t, "Verified", byEnv["test"].State)
	assert.NotEmpty(t, byEnv["test"].Steps, "the step's progress list")
	for _, sub := range byEnv["test"].Steps {
		assert.Equal(t, "Completed", sub.State, "a Verified step completed %s", sub.Name)
	}
	assert.Equal(t, "WaitingForMerge", byEnv["prod"].State)
	assert.Equal(t, prURL, byEnv["prod"].PRURL)

	// GET /steps/{ns}/{name}/events
	framework.Eventually(t, time.Minute, "the prod step's WaitingForMerge event", func(context.Context) (bool, string) {
		evs := eventsOf(t, c, a.ns, prodStep.Name)
		_, ok := eventWith(evs, "WaitingForMerge", prURL)
		return ok, fmt.Sprintf("%+v", evs)
	})
	evs := eventsOf(t, c, a.ns, testStep.Name)
	ev, ok := eventWith(evs, "Verified", "env test: step completed successfully")
	require.True(t, ok, "test step events: %+v", evs)
	assert.Equal(t, "Normal", ev.Type)
	assert.GreaterOrEqual(t, ev.Count, int32(1))
	first, err := time.Parse(time.RFC3339, ev.FirstTimestamp)
	require.NoError(t, err, "firstTimestamp %q", ev.FirstTimestamp)
	last, err := time.Parse(time.RFC3339, ev.LastTimestamp)
	require.NoError(t, err, "lastTimestamp %q", ev.LastTimestamp)
	assert.False(t, last.Before(first), "first %s, last %s", first, last)
	for _, other := range evs {
		assert.NotContains(t, other.Message, "env prod", "only the test step's events: %+v", other)
	}
	for i := 1; i < len(evs); i++ {
		assert.GreaterOrEqual(t, evs[i-1].LastTimestamp, evs[i].LastTimestamp, "newest first: %+v", evs)
	}
	missing := c.Get(t, uiAPI+"/steps/"+a.ns+"/no-such-step/events")
	assert.Equal(t, http.StatusNotFound, missing.Status, missing.String())
	assert.Equal(t, "promotion step not found", strings.TrimSpace(missing.Body))

	// The PR merges: every view follows.
	require.NoError(t, e.Git.MergePR(context.Background(), a.repo, pr.Number))
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	framework.Eventually(t, time.Minute, "the UI API to show prod Verified", func(context.Context) (bool, string) {
		p, _ := uiPipelineIn(t, c, a.ns)
		pn, _ := graphOf(t, c, a.ns, bundle).node("PromotionStep", "prod")
		var steps []uiStep
		getJSON(t, c, uiAPI+"/bundles/"+bundle+"/steps?namespace="+a.ns, &steps)
		stepState := ""
		for _, s := range steps {
			if s.Environment == "prod" {
				stepState = s.State
			}
		}
		_, verifiedEvent := eventWith(eventsOf(t, c, a.ns, prodStep.Name), "Verified", "env prod")
		return p.EnvironmentStates["prod"] == "Verified" && pn.State == "Verified" && stepState == "Verified" && verifiedEvent,
			fmt.Sprintf("pipeline=%v graph=%q steps=%q event=%v", p.EnvironmentStates, pn.State, stepState, verifiedEvent)
	})
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)
	framework.Eventually(t, time.Minute, "the Bundle list to show the Bundle Verified", func(context.Context) (bool, string) {
		var bundles []uiBundle
		getJSON(t, c, uiAPI+"/pipelines/"+pipelineName+"/bundles?namespace="+a.ns, &bundles)
		return len(bundles) == 1 && bundles[0].Phase == "Verified", fmt.Sprintf("%+v", bundles)
	})
}

// holdGate is a team PolicyGate in ns that holds env until it is overridden.
func holdGate(ns, env string) *v1alpha1.PolicyGate {
	return &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Name: "hold-" + env, Namespace: ns, Labels: map[string]string{
			"kardinal.io/scope":      "team",
			"kardinal.io/applies-to": env,
			"kardinal.io/type":       "gate",
		}},
		Spec: v1alpha1.PolicyGateSpec{Expression: "false", Message: env + " is on hold", RecheckInterval: "10s"},
	}
}

// TestUI_APIGateHoldsUntilApproved checks the gate views and the approve
// endpoint: a gate that is false holds prod, and GET /gates, the Bundle graph
// and the pipeline list all say so; bad approve requests change nothing; an
// approve writes an override that opens the gate, and prod then promotes. The
// main release has no UI auth, so the override and the gate reason name the
// UI, kardinal-ui.
//
// Covers UIAPI-GATES-01, UIAPI-APPROVE-01, UIAPI-REQUESTER-04.
func TestUI_APIGateHoldsUntilApproved(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	ctx := context.Background()
	tmpl := holdGate(a.ns, "prod")
	require.NoError(t, e.Client.Create(ctx, tmpl))
	a.apply(t, a.pipeline(nil))
	c := mainUI(t, e)

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)

	var gate uiGate
	framework.Eventually(t, time.Minute, "GET /gates to show the prod gate holding", func(context.Context) (bool, string) {
		gates := gatesOf(t, c, a.ns, bundle)
		if len(gates) != 1 {
			return false, fmt.Sprintf("%d gate instances: %+v", len(gates), gates)
		}
		gate = gates[0]
		return gate.State == "Block" && gate.Holding, fmt.Sprintf("%+v", gate)
	})
	assert.Equal(t, pipelineName, gate.Pipeline)
	assert.Equal(t, "prod", gate.Environment)
	assert.Equal(t, "false", gate.Expression)
	assert.False(t, gate.Ready)
	assert.False(t, gate.Template)
	assert.Empty(t, gate.Overrides)
	var all []uiGate
	getJSON(t, c, uiAPI+"/gates", &all)
	var listedTemplate bool
	for _, g := range all {
		if g.Namespace == a.ns && g.Name == tmpl.Name {
			listedTemplate = true
			assert.True(t, g.Template, "%+v", g)
			assert.False(t, g.Holding, "a template holds nothing: %+v", g)
		}
	}
	assert.True(t, listedTemplate, "GET /gates lists the template too")

	gn, ok := graphOf(t, c, a.ns, bundle).node("PolicyGate", "prod")
	require.True(t, ok, "the graph has the prod gate")
	assert.Equal(t, "gate-"+gate.Name, gn.ID)
	assert.Equal(t, tmpl.Name, gn.Label)
	assert.Equal(t, "Block", gn.State)
	assert.True(t, gn.Holding)
	assert.Equal(t, "false", gn.Expression)
	g := graphOf(t, c, a.ns, bundle)
	tn, _ := g.node("PromotionStep", "test")
	pn, _ := g.node("PromotionStep", "prod")
	assert.Equal(t, "NotStarted", pn.State, "prod has no step while the gate holds it")
	assert.ElementsMatch(t, []uiEdge{{From: tn.ID, To: gn.ID}, {From: gn.ID, To: pn.ID}}, g.Edges)
	p, _ := uiPipelineIn(t, c, a.ns)
	assert.Equal(t, 1, p.BlockerCount, "the pipeline list counts the gate as a blocker")

	framework.Consistently(t, 20*time.Second, "the gate holds prod", func(ctx context.Context) (bool, string) {
		_, exists, err := e.Step(ctx, a.ns, pipelineName, bundle, "prod")
		return err == nil && !exists, fmt.Sprintf("prod step exists=%v err=%v", exists, err)
	})
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))

	approve := uiAPI + "/gates/" + a.ns + "/" + gate.Name + "/approve"
	for _, bad := range []struct {
		path string
		body map[string]any
		code int
		want string
	}{
		{approve, map[string]any{"expiresInMinutes": 30}, http.StatusBadRequest, "reason is required"},
		{approve, map[string]any{"reason": "e2e", "expiresInMinutes": 1441}, http.StatusBadRequest, "expiresInMinutes must be between 1 and 1440"},
		{uiAPI + "/gates/" + a.ns + "/no-such-gate/approve", map[string]any{"reason": "e2e"}, http.StatusNotFound, "gate not found"},
	} {
		r := c.Post(t, bad.path, bad.body)
		assert.Equal(t, bad.code, r.Status, "%v: %s", bad.body, r)
		assert.Equal(t, bad.want, strings.TrimSpace(r.Body))
	}
	var inst v1alpha1.PolicyGate
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: gate.Name}, &inst))
	assert.Empty(t, inst.Spec.Overrides, "a refused approve writes nothing")

	before := time.Now()
	r := c.Post(t, approve, map[string]any{"reason": "e2e emergency", "expiresInMinutes": 30})
	require.Equal(t, http.StatusOK, r.Status, r.String())
	var msg struct{ Message string }
	r.JSON(t, &msg)
	require.True(t, strings.HasPrefix(msg.Message, "gate overridden until "), msg.Message)
	until, err := time.Parse(time.RFC3339, strings.TrimPrefix(msg.Message, "gate overridden until "))
	require.NoError(t, err)
	assert.WithinDuration(t, before.Add(30*time.Minute), until, time.Minute)

	framework.Eventually(t, time.Minute, "the override to open the gate", func(context.Context) (bool, string) {
		gates := gatesOf(t, c, a.ns, bundle)
		if len(gates) != 1 {
			return false, fmt.Sprintf("%+v", gates)
		}
		gate = gates[0]
		return gate.Ready && gate.State == "Pass" && !gate.Holding, fmt.Sprintf("%+v", gate)
	})
	require.Len(t, gate.Overrides, 1)
	assert.Equal(t, "e2e emergency", gate.Overrides[0].Reason)
	assert.Equal(t, "kardinal-ui", gate.Overrides[0].CreatedBy)
	assert.Equal(t, until.UTC().Format(time.RFC3339), gate.Overrides[0].ExpiresAt)
	assert.True(t, strings.HasPrefix(gate.Reason, "OVERRIDDEN by kardinal-ui: e2e emergency"), gate.Reason)

	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	framework.Eventually(t, time.Minute, "the blocker count to drop", func(context.Context) (bool, string) {
		p, _ := uiPipelineIn(t, c, a.ns)
		return p.BlockerCount == 0, fmt.Sprintf("blockerCount=%d", p.BlockerCount)
	})
}

// TestUI_APIPauseResume checks POST /pause and /resume: a paused pipeline
// holds a new Bundle before it deploys anything, and resuming lets it
// promote. The pipeline list shows the paused flag.
//
// Covers UIAPI-PAUSE-01.
func TestUI_APIPauseResume(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))
	c := mainUI(t, e)
	ctx := context.Background()
	key := types.NamespacedName{Namespace: a.ns, Name: pipelineName}

	for _, bad := range []struct {
		body map[string]any
		code int
		want string
	}{
		{map[string]any{"namespace": a.ns}, http.StatusBadRequest, "pipeline is required"},
		{map[string]any{"pipeline": "ghost", "namespace": a.ns}, http.StatusNotFound, "ghost"},
	} {
		r := c.Post(t, uiAPI+"/pause", bad.body)
		assert.Equal(t, bad.code, r.Status, "%v: %s", bad.body, r)
		assert.Contains(t, r.Body, bad.want)
	}

	r := c.Post(t, uiAPI+"/pause", map[string]any{"pipeline": pipelineName, "namespace": a.ns})
	require.Equal(t, http.StatusOK, r.Status, r.String())
	assert.JSONEq(t, `{"message":"pipeline podinfo paused"}`, r.Body)
	var p v1alpha1.Pipeline
	framework.Eventually(t, time.Minute, "the freeze gate", func(ctx context.Context) (bool, string) {
		if err := e.Client.Get(ctx, key, &p); err != nil {
			return false, err.Error()
		}
		cond := metaCondition(p.Status.Conditions, "Paused")
		var freeze v1alpha1.PolicyGate
		err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: "freeze-" + pipelineName}, &freeze)
		return p.Spec.Paused && cond == "True/FreezeGateActive" && err == nil,
			fmt.Sprintf("spec.paused=%v Paused=%s freeze gate: %v", p.Spec.Paused, cond, err)
	})
	up, _ := uiPipelineIn(t, c, a.ns)
	assert.True(t, up.Paused, "the pipeline list shows paused")

	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	framework.Consistently(t, 30*time.Second, "the paused pipeline holds the Bundle", func(ctx context.Context) (bool, string) {
		ps, exists, err := e.Step(ctx, a.ns, pipelineName, bundle, "test")
		if err != nil {
			return false, err.Error()
		}
		if exists && ps.Status.State != "" && ps.Status.State != "Pending" {
			return false, fmt.Sprintf("test step %s: %s", ps.Status.State, ps.Status.Message)
		}
		return true, ""
	})
	assert.Equal(t, fixtures.Image+":"+fixtures.V1, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
	assert.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("test")+"/kustomization.yaml"), "newTag: "+fixtures.V1)

	r = c.Post(t, uiAPI+"/resume", map[string]any{"pipeline": pipelineName, "namespace": a.ns})
	require.Equal(t, http.StatusOK, r.Status, r.String())
	assert.JSONEq(t, `{"message":"pipeline podinfo resumed"}`, r.Body)
	require.NoError(t, e.Client.Get(ctx, key, &p))
	assert.False(t, p.Spec.Paused)
	for _, env := range a.envs {
		e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
		assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload(env)))
	}
	up, _ = uiPipelineIn(t, c, a.ns)
	assert.False(t, up.Paused)
	var freeze v1alpha1.PolicyGate
	err := e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: "freeze-" + pipelineName}, &freeze)
	assert.True(t, apierrors.IsNotFound(err), "resume deletes the freeze gate: %v", err)
}

// metaCondition is "Status/Reason" of the condition typ, or "" when unset.
func metaCondition(conds []metav1.Condition, typ string) string {
	for _, c := range conds {
		if c.Type == typ {
			return string(c.Status) + "/" + c.Reason
		}
	}
	return ""
}

// TestUI_APIPromoteAndRollback checks POST /promote and /rollback. A Bundle
// that stops at test (intent.targetEnvironment) is promoted to prod from the
// UI, which deploys its version there; a rollback then puts prod back on the
// Bundle before it. The main release has no UI auth, so both Bundles name
// kardinal-ui as the requester. The requests the endpoints must refuse change
// nothing.
//
// Covers UIAPI-PROMOTE-01, UIAPI-ROLLBACK-01, UIAPI-REQUESTER-02.
func TestUI_APIPromoteAndRollback(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))
	c := mainUI(t, e)
	ctx := context.Background()

	first := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, first, "prod", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, first, "Verified", time.Minute)

	rb := c.Post(t, uiAPI+"/rollback", map[string]any{"pipeline": pipelineName, "environment": "prod", "namespace": a.ns})
	assert.Equal(t, http.StatusConflict, rb.Status, "nothing before the first Bundle: %s", rb)

	// A Bundle that stops at test.
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
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))

	for _, bad := range []struct {
		body map[string]any
		code int
		want string
	}{
		{map[string]any{"pipeline": pipelineName, "namespace": a.ns}, http.StatusBadRequest, "pipeline and environment are required"},
		{map[string]any{"pipeline": "ghost", "environment": "prod", "namespace": a.ns}, http.StatusNotFound, "ghost"},
		{map[string]any{"pipeline": pipelineName, "environment": "nope", "namespace": a.ns}, http.StatusBadRequest, `has no environment "nope"`},
		{map[string]any{"pipeline": pipelineName, "environment": "test", "namespace": a.ns}, http.StatusBadRequest, "test is the first environment"},
	} {
		r := c.Post(t, uiAPI+"/promote", bad.body)
		assert.Equal(t, bad.code, r.Status, "%v: %s", bad.body, r)
		assert.Contains(t, r.Body, bad.want)
	}

	r := c.Post(t, uiAPI+"/promote", map[string]any{"pipeline": pipelineName, "environment": "prod", "namespace": a.ns})
	require.Equal(t, http.StatusCreated, r.Status, r.String())
	var promoted struct{ Bundle, Source, Message string }
	r.JSON(t, &promoted)
	assert.Equal(t, held.Name, promoted.Source, "the newest Bundle Verified in test")
	assert.Equal(t, "promoting "+held.Name+" (Verified in test) to prod — track with kardinal get bundles "+pipelineName, promoted.Message)
	var pb v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: promoted.Bundle}, &pb))
	assert.Equal(t, held.Spec.Images, pb.Spec.Images)
	assert.Equal(t, held.Name, pb.Annotations[lifecycle.AnnotationPromotedFrom])
	assert.Equal(t, "kardinal-ui", pb.Annotations[lifecycle.AnnotationRequestedBy])
	require.NotNil(t, pb.Spec.Intent)
	assert.Equal(t, "prod", pb.Spec.Intent.TargetEnvironment)
	e.WaitStepState(t, a.ns, pipelineName, promoted.Bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V3, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
	e.WaitBundlePhase(t, a.ns, promoted.Bundle, "Verified", time.Minute)

	again := c.Post(t, uiAPI+"/promote", map[string]any{"pipeline": pipelineName, "environment": "prod", "namespace": a.ns})
	assert.Equal(t, http.StatusConflict, again.Status, "prod already runs it: %s", again)

	ghost := c.Post(t, uiAPI+"/rollback", map[string]any{"pipeline": "ghost", "environment": "prod", "namespace": a.ns})
	assert.Equal(t, http.StatusNotFound, ghost.Status, ghost.String())
	r = c.Post(t, uiAPI+"/rollback", map[string]any{"pipeline": pipelineName, "environment": "prod", "namespace": a.ns})
	require.Equal(t, http.StatusCreated, r.Status, r.String())
	var rolled struct{ Bundle, RollbackOf, Message string }
	r.JSON(t, &rolled)
	assert.Equal(t, first, rolled.RollbackOf, "prod goes back to the Bundle before the promoted one")
	assert.Equal(t, "rollback started — rolling prod back to "+first, rolled.Message)
	var rbb v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: rolled.Bundle}, &rbb))
	assert.Equal(t, "true", rbb.Labels[lifecycle.LabelRollback])
	assert.Equal(t, promoted.Bundle, rbb.Annotations[lifecycle.AnnotationRollbackFrom], "the Bundle prod ran")
	assert.Equal(t, "kardinal-ui", rbb.Annotations[lifecycle.AnnotationRequestedBy])
	require.NotNil(t, rbb.Spec.Provenance)
	assert.Equal(t, first, rbb.Spec.Provenance.RollbackOf)
	e.WaitStepState(t, a.ns, pipelineName, rolled.Bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
}

// TestUI_APICreateBundle checks POST /bundles: a valid request creates a
// Bundle that promotes, and the requests the Bundle API refuses are refused
// here too, with the reason and without creating anything. The main release
// has no UI auth, so the Bundle's kardinal.io/requested-by is kardinal-ui,
// and spec.provenance.author is the author in the request.
//
// Covers UIAPI-CREATE-01, UIAPI-REQUESTER-03.
func TestUI_APICreateBundle(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	c := mainUI(t, e)
	ctx := context.Background()

	for _, bad := range []struct {
		body map[string]any
		code int
		want string
	}{
		{map[string]any{"image": fixtures.Image + ":" + fixtures.V2, "namespace": a.ns}, http.StatusBadRequest, "pipeline is required"},
		{map[string]any{"pipeline": pipelineName, "namespace": a.ns}, http.StatusBadRequest, "image is required"},
		{map[string]any{"pipeline": "Podinfo App", "image": fixtures.Image + ":" + fixtures.V2, "namespace": a.ns},
			http.StatusBadRequest, "pipeline must be a valid Kubernetes object name"},
		{map[string]any{"pipeline": "ghost", "image": fixtures.Image + ":" + fixtures.V2, "namespace": a.ns},
			http.StatusNotFound, "pipeline " + a.ns + "/ghost not found"},
		{map[string]any{"pipeline": pipelineName, "image": ":" + fixtures.V2, "namespace": a.ns},
			http.StatusBadRequest, "bundle rejected by validation: "},
	} {
		r := c.Post(t, uiAPI+"/bundles", bad.body)
		assert.Equal(t, bad.code, r.Status, "%v: %s", bad.body, r)
		assert.Contains(t, r.Body, bad.want)
	}
	var list v1alpha1.BundleList
	require.NoError(t, e.Client.List(ctx, &list, client.InNamespace(a.ns)))
	assert.Empty(t, list.Items, "a refused request creates no Bundle")

	r := c.Post(t, uiAPI+"/bundles", map[string]any{"pipeline": pipelineName, "image": fixtures.Image + ":" + fixtures.V2,
		"commitSHA": "0123abc", "author": "e2e-ui", "namespace": a.ns})
	require.Equal(t, http.StatusCreated, r.Status, r.String())
	var created struct{ Bundle, Message string }
	r.JSON(t, &created)
	assert.Equal(t, "bundle created — track with kardinal get bundles "+pipelineName, created.Message)
	var b v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: created.Bundle}, &b))
	assert.Equal(t, pipelineName, b.Labels["kardinal.io/pipeline"])
	assert.Equal(t, "image", b.Spec.Type)
	assert.Equal(t, []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}}, b.Spec.Images)
	require.NotNil(t, b.Spec.Provenance)
	assert.Equal(t, "0123abc", b.Spec.Provenance.CommitSHA)
	assert.Equal(t, "e2e-ui", b.Spec.Provenance.Author)
	assert.Equal(t, "kardinal-ui", b.Annotations[lifecycle.AnnotationRequestedBy])
	e.WaitStepState(t, a.ns, pipelineName, created.Bundle, "test", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("test")))
}

// TestUI_APIValidateCEL checks POST /validate-cel: a valid expression, a
// compile error with its message, and a request without an expression.
//
// Covers UIAPI-VALIDATECEL-01.
func TestUI_APIValidateCEL(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := mainUI(t, e)

	for _, expr := range []string{"!schedule.isWeekend", `bundle.provenance.author != "dependabot[bot]"`, "true"} {
		r := c.Post(t, uiAPI+"/validate-cel", map[string]string{"expression": expr})
		require.Equal(t, http.StatusOK, r.Status, "%s: %s", expr, r)
		assert.JSONEq(t, `{"valid":true}`, r.Body, expr)
	}
	for _, expr := range []string{"bundle.version ==", "nosuch.thing == 1", `"a" + 1`} {
		r := c.Post(t, uiAPI+"/validate-cel", map[string]string{"expression": expr})
		require.Equal(t, http.StatusOK, r.Status, "%s: %s", expr, r)
		var got struct {
			Valid bool
			Error string
		}
		r.JSON(t, &got)
		assert.False(t, got.Valid, expr)
		assert.True(t, strings.HasPrefix(got.Error, "CEL compile error: "), "%s: %q", expr, got.Error)
	}
	for _, body := range []string{`{}`, `{"expression":""}`, `not json`} {
		r := c.Do(t, http.MethodPost, uiAPI+"/validate-cel", strings.NewReader(body))
		assert.Equal(t, http.StatusBadRequest, r.Status, "%s: %s", body, r)
		assert.Equal(t, "expression field required", strings.TrimSpace(r.Body))
	}
}

// TestUI_APILocalClientsOnly checks the main release, which has no UI auth
// mode: its API answers a kubectl port-forward and refuses a client that
// comes through a NodePort, or through the port-forward with a header a
// proxy adds. The app at /ui/ is served to both.
//
// Covers UIAPI-LOCAL-01.
func TestUI_APILocalClientsOnly(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	local := mainUI(t, e)
	remote := framework.UIClient{BaseURL: framework.MustEnv(t, framework.EnvUINodePortURL)}

	for _, r := range []framework.UIResponse{
		remote.Get(t, uiAPI+"/pipelines"),
		remote.Post(t, uiAPI+"/validate-cel", map[string]string{"expression": "true"}),
	} {
		assert.Equal(t, http.StatusForbidden, r.Status, r.String())
		assert.Equal(t, uiPeerNotLocal, strings.TrimSpace(r.Body))
	}
	app := remote.Get(t, "/ui/")
	assert.Equal(t, http.StatusOK, app.Status)
	assert.Contains(t, app.Body, `<div id="root">`)

	r := local.Get(t, uiAPI+"/pipelines")
	assert.Equal(t, http.StatusOK, r.Status, r.String())
	var list []uiPipeline
	r.JSON(t, &list)
	for _, h := range [][2]string{
		{"X-Forwarded-For", "203.0.113.7"},
		{"Forwarded", "for=203.0.113.7"},
		{"X-Real-Ip", "203.0.113.7"},
		{"X-Envoy-External-Address", "203.0.113.7"},
		{"L5d-Dst-Canonical", "kardinal-promoter.kardinal-system.svc.cluster.local:8082"},
	} {
		r := local.With(h[0], h[1]).Get(t, uiAPI+"/pipelines")
		assert.Equal(t, http.StatusForbidden, r.Status, "%s: %s", h[0], r)
		assert.Equal(t, uiPeerNotLocal, strings.TrimSpace(r.Body), h[0])
	}
}

// TestUI_APIAllowedHosts checks the DNS rebinding defence. With UI auth off,
// the API refuses every Host but localhost and --ui-allowed-hosts (a port or
// a trailing dot is ignored), while /ui/ is served to any Host. With auth on
// (kui-token), another Host is served when the request has no Origin, since
// the token protects it, but never gets a same-origin grant.
//
// Covers UIAPI-HOST-01.
func TestUI_APIAllowedHosts(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := mainUI(t, e)

	for _, host := range []string{"evil.example", "evil.example:8082", "kardinal-ui.test.evil.example"} {
		for _, r := range []framework.UIResponse{
			framework.UIClient{BaseURL: c.BaseURL, Host: host}.Get(t, uiAPI+"/pipelines"),
			framework.UIClient{BaseURL: c.BaseURL, Host: host}.Post(t, uiAPI+"/validate-cel", map[string]string{"expression": "true"}),
		} {
			assert.Equal(t, http.StatusForbidden, r.Status, "%s: %s", host, r)
			assert.Equal(t, uiHostNotAllowed, strings.TrimSpace(r.Body), host)
		}
		app := framework.UIClient{BaseURL: c.BaseURL, Host: host}.Get(t, "/ui/")
		assert.Equal(t, http.StatusOK, app.Status, "/ui/ is not host-checked: %s", host)
	}
	for _, host := range []string{
		framework.UIAllowedHost, framework.UIAllowedHost + ":8082", framework.UIAllowedHost + ".",
		"localhost:8082", "127.0.0.1", "[::1]:8082",
		// The chart always allows the Service's DNS names.
		"kardinal-promoter", "kardinal-promoter.kardinal-system.svc", "kardinal-promoter.kardinal-system.svc.cluster.local:8082",
	} {
		r := framework.UIClient{BaseURL: c.BaseURL, Host: host}.Get(t, uiAPI+"/pipelines")
		assert.Equal(t, http.StatusOK, r.Status, "%s: %s", host, r)
	}

	tokenURL := framework.MustEnv(t, framework.EnvUITokenURL)
	authed := framework.UIClient{BaseURL: tokenURL, Token: framework.UIToken(t)}
	r := framework.UIClient{BaseURL: tokenURL, Token: authed.Token, Host: "evil.example"}.Get(t, uiAPI+"/pipelines")
	assert.Equal(t, http.StatusOK, r.Status, "auth on: another Host without an Origin is served: %s", r)
	r = framework.UIClient{BaseURL: tokenURL, Host: "evil.example"}.Get(t, uiAPI+"/pipelines")
	assert.Equal(t, http.StatusUnauthorized, r.Status, "auth on: the token is still required: %s", r)
	r = framework.UIClient{BaseURL: tokenURL, Token: authed.Token, Host: "evil.example"}.
		With("Origin", "http://evil.example").Get(t, uiAPI+"/pipelines")
	assert.Equal(t, http.StatusForbidden, r.Status, "a rebound page's matching Origin is no same-origin grant: %s", r)
	assert.Equal(t, "CORS: origin not allowed", strings.TrimSpace(r.Body))
	port := tokenURL[strings.LastIndex(tokenURL, ":")+1:]
	r = framework.UIClient{BaseURL: tokenURL, Token: authed.Token, Host: framework.UIAllowedHost + ":" + port}.
		With("Origin", "http://"+framework.UIAllowedHost+":"+port).Get(t, uiAPI+"/pipelines")
	assert.Equal(t, http.StatusOK, r.Status, "an allowed Host is same-origin with its own Origin: %s", r)
}

// TestUI_APIStaticToken checks kui-token, which has a shared UI token: the
// API answers 401 with Www-Authenticate without the token or with a wrong
// one, and 200 with it; the app at /ui/ needs no token.
//
// Covers UIAPI-TOKEN-01.
func TestUI_APIStaticToken(t *testing.T) {
	t.Parallel()
	url := framework.MustEnv(t, framework.EnvUITokenURL)
	token := framework.UIToken(t)
	anon := framework.UIClient{BaseURL: url}

	for name, c := range map[string]framework.UIClient{
		"no token":    anon,
		"wrong token": {BaseURL: url, Token: token + "x"},
		"prefix":      {BaseURL: url, Token: token[:len(token)-1]},
		"not bearer":  anon.With("Authorization", "Basic "+token),
	} {
		for _, r := range []framework.UIResponse{
			c.Get(t, uiAPI+"/pipelines"),
			c.Post(t, uiAPI+"/validate-cel", map[string]string{"expression": "true"}),
		} {
			assert.Equal(t, http.StatusUnauthorized, r.Status, "%s: %s", name, r)
			assert.Equal(t, "unauthorized", strings.TrimSpace(r.Body), name)
			assert.Equal(t, uiAuthRealm, r.Header.Get("Www-Authenticate"), name)
		}
	}
	c := framework.UIClient{BaseURL: url, Token: token}
	r := c.Get(t, uiAPI+"/pipelines")
	require.Equal(t, http.StatusOK, r.Status, r.String())
	var list []uiPipeline
	r.JSON(t, &list)
	r = c.Post(t, uiAPI+"/validate-cel", map[string]string{"expression": "true"})
	assert.Equal(t, http.StatusOK, r.Status, r.String())
	assert.JSONEq(t, `{"valid":true}`, r.Body)
	app := anon.Get(t, "/ui/")
	assert.Equal(t, http.StatusOK, app.Status, "/ui/ needs no token")
	assert.Contains(t, app.Body, `<div id="root">`)
}

// TestUI_APICORS checks the CORS allow-list of kui-token (origin
// UICORSOrigin): an allowed origin gets its CORS headers and its preflight is
// answered before the token check; any other origin gets 403, preflight
// included. The main release, with no allow-list, refuses every cross origin
// and serves its own.
//
// Covers UIAPI-CORS-01.
func TestUI_APICORS(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	url := framework.MustEnv(t, framework.EnvUITokenURL)
	c := framework.UIClient{BaseURL: url, Token: framework.UIToken(t)}
	wantCORS := func(t *testing.T, r framework.UIResponse) {
		t.Helper()
		assert.Equal(t, framework.UICORSOrigin, r.Header.Get("Access-Control-Allow-Origin"))
		assert.Equal(t, "GET, POST, OPTIONS", r.Header.Get("Access-Control-Allow-Methods"))
		assert.Equal(t, "Authorization, Content-Type", r.Header.Get("Access-Control-Allow-Headers"))
		assert.Equal(t, "Origin", r.Header.Get("Vary"))
	}

	allowed := c.With("Origin", framework.UICORSOrigin)
	r := allowed.Get(t, uiAPI+"/pipelines")
	require.Equal(t, http.StatusOK, r.Status, r.String())
	wantCORS(t, r)
	r = allowed.Post(t, uiAPI+"/validate-cel", map[string]string{"expression": "true"})
	require.Equal(t, http.StatusOK, r.Status, r.String())
	wantCORS(t, r)
	preflight := framework.UIClient{BaseURL: url}.With("Origin", framework.UICORSOrigin).
		With("Access-Control-Request-Method", "POST").With("Access-Control-Request-Headers", "authorization,content-type")
	r = preflight.Do(t, http.MethodOptions, uiAPI+"/validate-cel", nil)
	assert.Equal(t, http.StatusOK, r.Status, "preflight without a token: %s", r)
	wantCORS(t, r)

	for _, origin := range []string{"http://other.example", "https://allowed.example", "http://allowed.example:8080"} {
		for _, r := range []framework.UIResponse{
			c.With("Origin", origin).Get(t, uiAPI+"/pipelines"),
			framework.UIClient{BaseURL: url}.With("Origin", origin).With("Access-Control-Request-Method", "POST").
				Do(t, http.MethodOptions, uiAPI+"/validate-cel", nil),
		} {
			assert.Equal(t, http.StatusForbidden, r.Status, "%s: %s", origin, r)
			assert.Equal(t, "CORS: origin not allowed", strings.TrimSpace(r.Body), origin)
			assert.Empty(t, r.Header.Get("Access-Control-Allow-Origin"), origin)
		}
	}

	local := mainUI(t, e)
	r = local.With("Origin", framework.UICORSOrigin).Get(t, uiAPI+"/pipelines")
	assert.Equal(t, http.StatusForbidden, r.Status, "no allow-list: %s", r)
	assert.Equal(t, "CORS: origin not allowed", strings.TrimSpace(r.Body))
	self := "http://" + strings.TrimPrefix(local.BaseURL, "http://")
	r = local.With("Origin", self).Post(t, uiAPI+"/validate-cel", map[string]string{"expression": "true"})
	assert.Equal(t, http.StatusOK, r.Status, "the UI's own origin: %s", r)
	assert.Empty(t, r.Header.Get("Access-Control-Allow-Origin"), "same origin needs no CORS headers")
}

// TestUI_APIBodyLimit checks the 1 MiB cap on UI API request bodies: a body
// over it is refused with 400 and creates nothing; one just under it is read.
//
// Covers UIAPI-BODY-01.
func TestUI_APIBodyLimit(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	c := mainUI(t, e)
	const limit = 1 << 20
	padded := func(prefix string, size int) string {
		const suffix = `"}`
		return prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
	}

	r := c.Do(t, http.MethodPost, uiAPI+"/validate-cel", strings.NewReader(padded(`{"expression":"true","pad":"`, limit+1)))
	assert.Equal(t, http.StatusBadRequest, r.Status, r.String())
	assert.Equal(t, "expression field required", strings.TrimSpace(r.Body))
	r = c.Do(t, http.MethodPost, uiAPI+"/validate-cel", strings.NewReader(padded(`{"expression":"true","pad":"`, limit)))
	assert.Equal(t, http.StatusOK, r.Status, "exactly 1 MiB is read: %s", r)
	assert.JSONEq(t, `{"valid":true}`, r.Body)

	create := fmt.Sprintf(`{"pipeline":%q,"image":"%s:%s","namespace":%q,"author":"`, pipelineName, fixtures.Image, fixtures.V2, ns)
	r = c.Do(t, http.MethodPost, uiAPI+"/bundles", strings.NewReader(padded(create, limit+1)))
	assert.Equal(t, http.StatusBadRequest, r.Status, r.String())
	assert.Equal(t, "invalid request body", strings.TrimSpace(r.Body))
	var list v1alpha1.BundleList
	require.NoError(t, e.Client.List(context.Background(), &list, client.InNamespace(ns)))
	assert.Empty(t, list.Items)
}

// TestUI_APISecurityHeaders checks that every UI server response carries the
// documented browser security headers: the app, API answers, and refusals
// with and without auth.
//
// Covers UIAPI-HEADERS-01.
func TestUI_APISecurityHeaders(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := mainUI(t, e)
	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
			"connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'",
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	}
	tokenURL := framework.MustEnv(t, framework.EnvUITokenURL)
	for name, r := range map[string]framework.UIResponse{
		"app":             c.Get(t, "/ui/"),
		"asset":           c.Get(t, "/ui/logo.png"),
		"api":             c.Get(t, uiAPI+"/pipelines"),
		"api 404":         c.Get(t, uiAPI+"/steps/default/no-such-step/events"),
		"host refused":    framework.UIClient{BaseURL: c.BaseURL, Host: "evil.example"}.Get(t, uiAPI+"/pipelines"),
		"peer refused":    framework.UIClient{BaseURL: framework.MustEnv(t, framework.EnvUINodePortURL)}.Get(t, uiAPI+"/pipelines"),
		"unauthorized":    framework.UIClient{BaseURL: tokenURL}.Get(t, uiAPI+"/pipelines"),
		"origin refused":  framework.UIClient{BaseURL: tokenURL}.With("Origin", "http://other.example").Get(t, uiAPI+"/pipelines"),
		"token-mode api":  framework.UIClient{BaseURL: tokenURL, Token: framework.UIToken(t)}.Get(t, uiAPI+"/pipelines"),
		"token-mode app":  framework.UIClient{BaseURL: tokenURL}.Get(t, "/ui/"),
		"method refused":  c.Do(t, http.MethodDelete, uiAPI+"/pipelines", nil),
		"missing on /ui/": c.Get(t, "/ui/no-such-file.js"),
	} {
		for k, v := range want {
			assert.Equal(t, v, r.Header.Get(k), "%s (HTTP %d): %s", name, r.Status, k)
		}
	}
}

// TestUI_APITokenReview checks kui-tr (TokenReview mode, watching
// EnvUITRNamespace) with ServiceAccount tokens: 401 without a valid token;
// 403 naming the verb, resource and namespace for a user RBAC denies; 200
// for a user bound to the documented viewer rules; the allow is cached for
// 30s after the RoleBinding is deleted, then the user gets 403. kui-tr-norbac
// cannot create TokenReviews and answers 503.
//
// Covers UIAPI-TR-01.
func TestUI_APITokenReview(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	trNS := framework.MustEnv(t, framework.EnvUITRNamespace)
	url := framework.MustEnv(t, framework.EnvUITRURL)
	ctx := context.Background()

	// A Pipeline in the watched namespace for the viewer to see.
	pipeline := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: ns, Namespace: trNS},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://git.example/kardinal/" + ns + ".git", Branch: "main"},
			Environments: []v1alpha1.EnvironmentSpec{{Name: "test", Path: fixtures.Path("test"), Approval: "auto",
				Update: v1alpha1.UpdateConfig{Strategy: "kustomize"}}},
		},
	}
	require.NoError(t, e.Client.Create(ctx, pipeline))
	t.Cleanup(func() { _ = e.Client.Delete(context.Background(), pipeline) })

	token := func(sa string) string {
		t.Helper()
		_, err := e.Kube.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: sa}}, metav1.CreateOptions{})
		require.NoError(t, err)
		tr, err := e.Kube.CoreV1().ServiceAccounts(ns).CreateToken(ctx, sa, &authnv1.TokenRequest{
			Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](1800), Audiences: []string{"kardinal-promoter"}}}, metav1.CreateOptions{})
		require.NoError(t, err)
		return tr.Status.Token
	}
	user := func(sa string) string { return "system:serviceaccount:" + ns + ":" + sa }
	stranger, viewer := token("stranger"), token("viewer")
	listPipelines := func(c framework.UIClient) (framework.UIResponse, bool) {
		r := c.Get(t, uiAPI+"/pipelines")
		if r.Status != http.StatusOK {
			return r, false
		}
		var list []uiPipeline
		r.JSON(t, &list)
		for _, p := range list {
			if p.Namespace == trNS && p.Name == pipeline.Name {
				return r, true
			}
		}
		return r, false
	}

	for name, c := range map[string]framework.UIClient{
		"no token":      {BaseURL: url},
		"invalid token": {BaseURL: url, Token: "not-a-kubernetes-token"},
	} {
		r := c.Get(t, uiAPI+"/pipelines")
		assert.Equal(t, http.StatusUnauthorized, r.Status, "%s: %s", name, r)
		assert.Equal(t, "unauthorized", strings.TrimSpace(r.Body), name)
		assert.Equal(t, uiAuthRealm, r.Header.Get("Www-Authenticate"), name)
	}
	r := framework.UIClient{BaseURL: url, Token: stranger}.Get(t, uiAPI+"/pipelines")
	assert.Equal(t, http.StatusForbidden, r.Status, r.String())
	assert.True(t, strings.HasPrefix(strings.TrimSpace(r.Body),
		fmt.Sprintf("forbidden: user %q cannot list pipelines.kardinal.io in namespace %s", user("stranger"), trNS)), r.Body)

	// The viewer rules of docs/guides/security.md, as a Role in the watched
	// namespace, which the docs say is enough with --watch-namespace.
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "ui-viewer-" + ns, Namespace: trNS},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"kardinal.io"}, Resources: []string{"pipelines", "bundles", "policygates", "promotionsteps"}, Verbs: []string{"get", "list"}},
			{APIGroups: []string{""}, Resources: []string{"events"}, Verbs: []string{"list"}},
		},
	}
	_, err := e.Kube.RbacV1().Roles(trNS).Create(ctx, role, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = e.Kube.RbacV1().Roles(trNS).Delete(context.Background(), role.Name, metav1.DeleteOptions{})
	})
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: trNS},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: "viewer", Namespace: ns}},
	}
	_, err = e.Kube.RbacV1().RoleBindings(trNS).Create(ctx, binding, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = e.Kube.RbacV1().RoleBindings(trNS).Delete(context.Background(), binding.Name, metav1.DeleteOptions{})
	})
	// The UI caches denials too, so the viewer's first UI request waits until
	// the API server grants the binding.
	apiServerAllows := func(ctx context.Context) (bool, error) {
		sar, err := e.Kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{
			Spec: authzv1.SubjectAccessReviewSpec{User: user("viewer"),
				Groups:             []string{"system:serviceaccounts", "system:serviceaccounts:" + ns, "system:authenticated"},
				ResourceAttributes: &authzv1.ResourceAttributes{Namespace: trNS, Verb: "list", Group: "kardinal.io", Resource: "pipelines"}},
		}, metav1.CreateOptions{})
		if err != nil {
			return false, err
		}
		return sar.Status.Allowed, nil
	}
	framework.Eventually(t, time.Minute, "the API server to grant the viewer binding", func(ctx context.Context) (bool, string) {
		ok, err := apiServerAllows(ctx)
		return ok && err == nil, fmt.Sprintf("allowed=%v err=%v", ok, err)
	})
	vc := framework.UIClient{BaseURL: url, Token: viewer}
	granted := time.Now()
	r, listed := listPipelines(vc)
	require.True(t, listed, "the viewer sees the Pipeline: %s", r)
	for _, path := range []string{uiAPI + "/gates", uiAPI + "/pipelines/" + pipeline.Name + "/bundles?namespace=" + trNS} {
		r := vc.Get(t, path)
		assert.Equal(t, http.StatusOK, r.Status, "%s: %s", path, r)
	}

	require.NoError(t, e.Kube.RbacV1().RoleBindings(trNS).Delete(ctx, binding.Name, metav1.DeleteOptions{}))
	framework.Eventually(t, 15*time.Second, "the API server to revoke the viewer", func(ctx context.Context) (bool, string) {
		ok, err := apiServerAllows(ctx)
		return !ok && err == nil, fmt.Sprintf("allowed=%v err=%v", ok, err)
	})
	cached := time.Until(granted.Add(20 * time.Second))
	require.Positive(t, cached, "the revocation took too long to leave a cache window to check")
	framework.Consistently(t, cached, "the cached allow", func(context.Context) (bool, string) {
		r, listed := listPipelines(vc)
		return listed, r.String()
	})
	framework.Eventually(t, time.Until(granted.Add(45*time.Second)), "the viewer to lose access within 30s", func(context.Context) (bool, string) {
		r := vc.Get(t, uiAPI+"/pipelines")
		return r.Status == http.StatusForbidden, r.String()
	})
	r = vc.Get(t, uiAPI+"/pipelines")
	assert.True(t, strings.HasPrefix(strings.TrimSpace(r.Body),
		fmt.Sprintf("forbidden: user %q cannot list pipelines.kardinal.io in namespace %s", user("viewer"), trNS)), r.Body)
	assert.GreaterOrEqual(t, time.Since(granted), 30*time.Second, "not before the 30s cache expires")

	norbac := framework.UIClient{BaseURL: framework.MustEnv(t, framework.EnvUITRNoRBACURL), Token: viewer}
	r = norbac.Get(t, uiAPI+"/pipelines")
	assert.Equal(t, http.StatusServiceUnavailable, r.Status, "the review fails: %s", r)
	assert.Equal(t, "auth unavailable", strings.TrimSpace(r.Body))
	app := framework.UIClient{BaseURL: url}.Get(t, "/ui/")
	assert.Equal(t, http.StatusOK, app.Status, "/ui/ is not gated")
}

// tokenReviewClusterRole is the ClusterRole the chart renders for the kui-tr
// release (ui.auth.tokenReview with controller.watchNamespace): the
// cluster-scoped rules, among them create on TokenReviews and
// SubjectAccessReviews.
const tokenReviewClusterRole = "kui-tr-kardinal-promoter-cluster-scoped"

// TestUI_APIRequesterTokenReview checks who a UI promote and a Bundle created
// from the UI record with TokenReview auth: the caller's Kubernetes username,
// in the new Bundle's kardinal.io/requested-by annotation and in the
// requestedBy field of the controller's log line. The caller is a
// ServiceAccount bound only to the documented Promote rules, which hold the
// Create a Bundle rules. The UI is a controller variant run with
// --ui-tokenreview-auth and bound to the chart's TokenReview rules
// (tokenReviewClusterRole); it watches the test's namespace and is a standby,
// so the main release alone reconciles it. Both Bundles deploy to prod.
//
// Covers UIAPI-REQUESTER-01.
func TestUI_APIRequesterTokenReview(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	a := newArgoApp(t, e, "test", "prod")
	a.apply(t, a.pipeline(nil))
	ctx := context.Background()

	// A Bundle that stops at test, for the UI to promote to prod.
	held := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{GenerateName: pipelineName + "-", Namespace: a.ns},
		Spec: v1alpha1.BundleSpec{
			Type: "image", Pipeline: pipelineName,
			Images: []v1alpha1.ImageRef{{Repository: fixtures.Image, Tag: fixtures.V2}},
			Intent: &v1alpha1.BundleIntent{TargetEnvironment: "test"},
		},
	}
	lifecycle.StampCreatedAt(held, time.Now())
	require.NoError(t, e.Client.Create(ctx, held))

	v := e.ControllerVariant(t, a.ns, []string{"--ui-tokenreview-auth=true"})
	_, err := e.Kube.RbacV1().ClusterRoles().Get(ctx, tokenReviewClusterRole, metav1.GetOptions{})
	require.NoError(t, err, "hack/e2e/components/ui.sh installs kui-tr")
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: v.Name + "-tokenreview"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: tokenReviewClusterRole},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: v.Name, Namespace: framework.ControllerNamespace}},
	}
	_, err = e.Kube.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = e.Kube.RbacV1().ClusterRoleBindings().Delete(context.Background(), crb.Name, metav1.DeleteOptions{})
	})

	// The caller: a ServiceAccount token bound to the Promote row of
	// docs/guides/security.md, as a Role in the Pipeline's namespace.
	const sa = "operator"
	_, err = e.Kube.CoreV1().ServiceAccounts(a.ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: sa}}, metav1.CreateOptions{})
	require.NoError(t, err)
	tr, err := e.Kube.CoreV1().ServiceAccounts(a.ns).CreateToken(ctx, sa, &authnv1.TokenRequest{
		Spec: authnv1.TokenRequestSpec{ExpirationSeconds: ptr.To[int64](1800), Audiences: []string{"kardinal-promoter"}}}, metav1.CreateOptions{})
	require.NoError(t, err)
	user := "system:serviceaccount:" + a.ns + ":" + sa
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "ui-promote", Namespace: a.ns},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"kardinal.io"}, Resources: []string{"pipelines"}, Verbs: []string{"get"}},
			{APIGroups: []string{"kardinal.io"}, Resources: []string{"promotionsteps", "bundles"}, Verbs: []string{"list"}},
			{APIGroups: []string{"kardinal.io"}, Resources: []string{"bundles"}, Verbs: []string{"create"}},
		},
	}
	_, err = e.Kube.RbacV1().Roles(a.ns).Create(ctx, role, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = e.Kube.RbacV1().RoleBindings(a.ns).Create(ctx, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: a.ns},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: sa, Namespace: a.ns}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	// The UI caches denials, so its first request waits until the API server
	// grants both bindings.
	allows := func(ctx context.Context, account, ns string, ra authzv1.ResourceAttributes) string {
		sar, err := e.Kube.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authzv1.SubjectAccessReview{
			Spec: authzv1.SubjectAccessReviewSpec{User: "system:serviceaccount:" + ns + ":" + account,
				Groups:             []string{"system:serviceaccounts", "system:serviceaccounts:" + ns, "system:authenticated"},
				ResourceAttributes: &ra},
		}, metav1.CreateOptions{})
		switch {
		case err != nil:
			return err.Error()
		case !sar.Status.Allowed:
			return fmt.Sprintf("%s/%s cannot %s %s", ns, account, ra.Verb, ra.Resource)
		}
		return ""
	}
	framework.Eventually(t, time.Minute, "the API server to grant the variant and the caller their bindings", func(ctx context.Context) (bool, string) {
		msg := allows(ctx, v.Name, framework.ControllerNamespace,
			authzv1.ResourceAttributes{Verb: "create", Group: "authentication.k8s.io", Resource: "tokenreviews"})
		if msg == "" {
			msg = allows(ctx, sa, a.ns, authzv1.ResourceAttributes{Namespace: a.ns, Verb: "create", Group: "kardinal.io", Resource: "bundles"})
		}
		return msg == "", msg
	})

	e.WaitStepState(t, a.ns, pipelineName, held.Name, "test", "Verified", promoteTimeout)
	e.WaitBundlePhase(t, a.ns, held.Name, "Verified", time.Minute)

	promote := map[string]any{"pipeline": pipelineName, "environment": "prod", "namespace": a.ns}
	anon := framework.UIClient{BaseURL: v.UIURL}.Post(t, uiAPI+"/promote", promote)
	require.Equal(t, http.StatusUnauthorized, anon.Status, "the variant runs with TokenReview auth: %s", anon)
	sent := time.Now()
	r := framework.UIClient{BaseURL: v.UIURL, Token: tr.Status.Token}.Post(t, uiAPI+"/promote", promote)
	require.Equal(t, http.StatusCreated, r.Status, r.String())
	var promoted struct{ Bundle, Source string }
	r.JSON(t, &promoted)
	assert.Equal(t, held.Name, promoted.Source)
	var pb v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: promoted.Bundle}, &pb))
	assert.Equal(t, user, pb.Annotations[lifecycle.AnnotationRequestedBy], "the token's user, not kardinal-ui")
	assert.Equal(t, held.Name, pb.Annotations[lifecycle.AnnotationPromotedFrom])

	framework.Eventually(t, 30*time.Second, "the variant to log the promote", func(context.Context) (bool, string) {
		got, ok := loggedRequester(e.VariantLogs(t, v, sent.Add(-time.Second)), "ui: promote triggered", promoted.Bundle)
		return ok && got == user, fmt.Sprintf("logged=%v requestedBy=%q", ok, got)
	})

	e.WaitStepState(t, a.ns, pipelineName, promoted.Bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V2, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))

	// A Bundle created from the UI records the same user; the author is the
	// build's, as typed.
	create := map[string]any{"pipeline": pipelineName, "image": fixtures.Image + ":" + fixtures.V3,
		"author": "e2e-ci", "namespace": a.ns}
	sent = time.Now()
	r = framework.UIClient{BaseURL: v.UIURL, Token: tr.Status.Token}.Post(t, uiAPI+"/bundles", create)
	require.Equal(t, http.StatusCreated, r.Status, r.String())
	var created struct{ Bundle string }
	r.JSON(t, &created)
	var cb v1alpha1.Bundle
	require.NoError(t, e.Client.Get(ctx, types.NamespacedName{Namespace: a.ns, Name: created.Bundle}, &cb))
	assert.Equal(t, user, cb.Annotations[lifecycle.AnnotationRequestedBy], "the token's user, not kardinal-ui")
	require.NotNil(t, cb.Spec.Provenance)
	assert.Equal(t, "e2e-ci", cb.Spec.Provenance.Author)

	framework.Eventually(t, 30*time.Second, "the variant to log the new Bundle", func(context.Context) (bool, string) {
		got, ok := loggedRequester(e.VariantLogs(t, v, sent.Add(-time.Second)), "ui: bundle created", created.Bundle)
		return ok && got == user, fmt.Sprintf("logged=%v requestedBy=%q", ok, got)
	})

	e.WaitStepState(t, a.ns, pipelineName, created.Bundle, "prod", "Verified", promoteTimeout)
	assert.Equal(t, fixtures.Image+":"+fixtures.V3, e.DeploymentImage(t, a.ns, fixtures.Workload("prod")))
}

// loggedRequester returns the requestedBy field of the controller's JSON log
// line with message msg about Bundle bundle.
func loggedRequester(logs, msg, bundle string) (string, bool) {
	for _, line := range strings.Split(logs, "\n") {
		var entry struct{ Message, Bundle, RequestedBy string }
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Message == msg && entry.Bundle == bundle {
			return entry.RequestedBy, true
		}
	}
	return "", false
}

// TestUI_APITLS checks kui-tls, which serves its UI and webhook ports over
// TLS: both answer HTTPS with the configured certificate and refuse plain
// HTTP. A controller given only one of the certificate and key (flag or
// environment variable) exits at startup.
//
// Covers UIAPI-TLS-01.
func TestUI_APITLS(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	ns := e.Namespace(t)
	ctx := context.Background()
	hc := framework.TLSClient(t, framework.MustEnv(t, framework.EnvUITLSCA))
	uiURL := framework.MustEnv(t, framework.EnvUITLSURL)
	hookURL := framework.MustEnv(t, framework.EnvUITLSWebhookURL)
	c := framework.UIClient{BaseURL: uiURL, Token: framework.UIToken(t), HTTP: hc}

	app := c.Get(t, "/ui/")
	assert.Equal(t, http.StatusOK, app.Status)
	assert.Contains(t, app.Body, `<div id="root">`)
	r := c.Get(t, uiAPI+"/pipelines")
	assert.Equal(t, http.StatusOK, r.Status, r.String())
	var list []uiPipeline
	r.JSON(t, &list)
	health := framework.UIClient{BaseURL: hookURL, HTTP: hc}.Get(t, "/webhook/scm/health")
	assert.Equal(t, http.StatusOK, health.Status, health.String())
	assert.Contains(t, health.Body, `"status":"ok"`)
	for _, u := range []string{uiURL + "/ui/", hookURL + "/webhook/scm/health"} {
		plain := framework.UIClient{BaseURL: "http://" + strings.TrimPrefix(u, "https://")}.Get(t, "")
		assert.Equal(t, http.StatusBadRequest, plain.Status, "plain HTTP to %s: %s", u, plain)
		assert.Contains(t, plain.Body, "Client sent an HTTP request to an HTTPS server")
	}

	// The pods run the image under test, which kind has loaded but no
	// registry serves.
	ctrl, err := e.Kube.AppsV1().Deployments(framework.ControllerNamespace).Get(ctx, framework.ControllerService, metav1.GetOptions{})
	require.NoError(t, err)
	image := ctrl.Spec.Template.Spec.Containers[0].Image
	for name, half := range map[string]corev1.Container{
		"cert-flag-only": {Args: []string{"--tls-cert-file=/etc/kardinal/tls/tls.crt"}},
		"key-env-only":   {Env: []corev1.EnvVar{{Name: "KARDINAL_TLS_KEY_FILE", Value: "/etc/kardinal/tls/tls.key"}}},
	} {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{
				Name: "controller", Image: image, ImagePullPolicy: corev1.PullNever,
				Args: append([]string{"--leader-elect=false", "--watch-namespace=" + ns}, half.Args...),
				Env:  half.Env,
			}}},
		}
		_, err := e.Kube.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	for name := range map[string]bool{"cert-flag-only": true, "key-env-only": true} {
		var exit *corev1.ContainerStateTerminated
		framework.Eventually(t, 2*time.Minute, name+" to exit", func(ctx context.Context) (bool, string) {
			p, err := e.Kube.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			for _, s := range p.Status.ContainerStatuses {
				if s.State.Terminated != nil {
					exit = s.State.Terminated
					return true, ""
				}
			}
			return false, fmt.Sprintf("phase=%s", p.Status.Phase)
		})
		assert.NotZero(t, exit.ExitCode, "%s exits with an error", name)
		logs, err := e.Kube.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{}).DoRaw(ctx)
		require.NoError(t, err)
		assert.Contains(t, string(logs), "--tls-cert-file and --tls-key-file must be set together", name)
		assert.Contains(t, string(logs), "unable to configure webhook server", name)
	}
}
