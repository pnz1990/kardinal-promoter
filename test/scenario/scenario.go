// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// The scenario matrix's dimensions.
const (
	DimUpdate = iota
	DimHealth
	DimGate
	DimTopology
	DimDelivery
	DimSCM
	DimOutcome
	DimActor
)

// Dimensions are the scenario matrix: every way kardinal is configured or a
// promotion ends that a scenario varies.
var Dimensions = []Dimension{
	DimUpdate:   {Name: "update", Values: []string{"kustomize", "helm", "yaml", "argocd", "rendered"}},
	DimHealth:   {Name: "health", Values: []string{"deployment", "resource", "argocd", "flux", "argoRollouts", "flagger"}},
	DimGate:     {Name: "gate", Values: []string{"cel", "approval", "metric", "changewindow", "soak", "upstream", "verify"}},
	DimTopology: {Name: "topology", Values: []string{"linear", "fanout", "fanin", "waves", "fleet"}},
	DimDelivery: {Name: "delivery", Values: []string{"pr-review", "direct"}},
	DimSCM:      {Name: "scm", Values: []string{SCMGitHub, SCMGitLab, SCMForgejo}},
	DimOutcome:  {Name: "outcome", Values: []string{"success", "healthfail", "gateblock", "superseded", "rejected", "rollback"}},
	// Who creates the versions (ActorIdentities): a person, an
	// unregistered ServiceAccount, an agent by its ServiceAccount and an
	// agent by a group; the refused actors are ActorRefusals.
	DimActor: {Name: "actor", Values: []string{"human", "bot", "agent", "agent-group"}},
}

// Combo is one row of the matrix.
type Combo struct {
	Update, Health, Gate, Topology, Delivery, SCM, Outcome, Actor string
	// Org puts the gate in the org policy namespace instead of the
	// Pipeline's.
	Org bool
}

// ComboOf maps a row of Dimensions to a Combo.
func ComboOf(row []string, org bool) Combo {
	return Combo{Update: row[DimUpdate], Health: row[DimHealth], Gate: row[DimGate], Topology: row[DimTopology],
		Delivery: row[DimDelivery], SCM: row[DimSCM], Outcome: row[DimOutcome], Actor: row[DimActor], Org: org}
}

// Name is the combo's subtest name.
func (c Combo) Name() string {
	scope := "team"
	if c.Org {
		scope = "org"
	}
	return strings.Join([]string{c.Update, c.Health, c.Gate + "." + scope, c.Topology, c.Delivery, c.SCM, c.Outcome, c.Actor}, "/")
}

// Rejection is a combination the API server refuses, with the CRD rule's
// message (config/crd/bases/kardinal.io_pipelines.yaml).
type Rejection struct {
	// Matches reports whether a row has the combination.
	Matches func(row []string) bool
	// Message is part of the refusal the API server returns.
	Message string
	// Example sets the dimensions (by index) that make a row hit it.
	Example map[int]string
}

// Rejections are the combinations of Dimensions that cannot exist.
var Rejections = []Rejection{
	{
		// Every target of a fleet would patch the fleet's one Application.
		Matches: func(row []string) bool { return row[DimUpdate] == "argocd" && row[DimTopology] == "fleet" },
		Message: "update.strategy argocd cannot promote a fleet",
		Example: map[int]string{DimUpdate: "argocd", DimTopology: "fleet", DimDelivery: "direct"},
	},
	{
		// update.strategy argocd patches the Application and opens no PR.
		Matches: func(row []string) bool { return row[DimUpdate] == "argocd" && row[DimDelivery] == "pr-review" },
		Message: "update.strategy argocd patches the Application directly and cannot honour approval: pr-review",
		Example: map[int]string{DimUpdate: "argocd", DimDelivery: "pr-review", DimTopology: "linear"},
	},
}

// Rejected returns the rejection a row hits, or nil.
func Rejected(row []string) *Rejection {
	for i := range Rejections {
		if Rejections[i].Matches(row) {
			return &Rejections[i]
		}
	}
	return nil
}

// Plan is a combo turned into objects.
type Plan struct {
	Combo
	NS       string
	PolicyNS string
	Pipeline *v1alpha1.Pipeline
	// GateEnv is the environment the gate applies to; GateEnvs are the
	// environments it holds (the fleet's targets for a fleet).
	GateEnv  string
	GateEnvs []string
	// Upstream is the environment the soak gate reads.
	Upstream string
	Gate     *v1alpha1.PolicyGate
	Extra    []client.Object
	// Envs are every environment (fleet targets expanded).
	Envs []string
}

// Fixed scenario names.
const (
	PipelineName   = "app"
	PolicyNS       = "platform-policies"
	Approver       = "alice"
	MetricQuery    = "error_rate"
	WindowName     = "freeze"
	BaseTag        = "1.0.0"
	blockingSoak   = 30
	passingSoak    = 1
	metricPassing  = 0.01
	metricFailing  = 0.5
	healthTimeout  = "4m"
	repoBase       = "acme/app-gitops"
	gitLabRepoBase = "acme/platform/app-gitops"
)

// BuildPlan turns c into the objects of tenant namespace ns. block makes
// the gate start closed.
func BuildPlan(c Combo, ns string, now time.Time, block bool) *Plan {
	p := &Plan{Combo: c, NS: ns, PolicyNS: ns}
	if c.Org {
		p.PolicyNS = PolicyNS
	}
	repo := repoBase
	if c.SCM == SCMGitLab {
		repo = gitLabRepoBase // a GitLab subgroup: a project path with three segments
	}
	approval := "auto"
	if c.Delivery == "pr-review" {
		approval = "pr-review"
	}
	envSpec := func(name string) v1alpha1.EnvironmentSpec {
		e := v1alpha1.EnvironmentSpec{Name: name, Approval: approval, Health: healthConfig(c.Health)}
		switch c.Update {
		case "helm":
			e.Update.Strategy = "helm"
		case "yaml":
			e.Update.Strategy = "yaml"
			e.Update.YAML = &v1alpha1.YAMLUpdateConfig{Updates: []v1alpha1.YAMLUpdate{{File: "deployment.yaml",
				Path: "spec.template.spec.containers[name=app].image", Value: "image"}}}
		case "argocd":
			e.Update.Strategy = "argocd"
			e.Update.ArgoCD = &v1alpha1.ArgoCDUpdateConfig{Application: PipelineName + "-" + name, Namespace: "argocd"}
		case "rendered":
			e.Layout = "branch"
		}
		return e
	}
	var envs []v1alpha1.EnvironmentSpec
	switch c.Topology {
	case "linear":
		envs = []v1alpha1.EnvironmentSpec{envSpec("dev"), envSpec("staging"), envSpec("prod")}
		p.GateEnv, p.Upstream = "prod", "staging"
	case "fanout":
		eu, us := envSpec("prod-eu"), envSpec("prod-us")
		eu.DependsOn, us.DependsOn = []string{"dev"}, []string{"dev"}
		envs = []v1alpha1.EnvironmentSpec{envSpec("dev"), eu, us}
		p.GateEnv, p.Upstream = "prod-eu", "dev"
	case "fanin":
		a, b, prod := envSpec("qa-a"), envSpec("qa-b"), envSpec("prod")
		a.DependsOn, b.DependsOn, prod.DependsOn = []string{"dev"}, []string{"dev"}, []string{"qa-a", "qa-b"}
		envs = []v1alpha1.EnvironmentSpec{envSpec("dev"), a, b, prod}
		p.GateEnv, p.Upstream = "prod", "qa-a"
	case "waves":
		canary, eu, us := envSpec("canary"), envSpec("eu"), envSpec("us")
		canary.Wave, eu.Wave, us.Wave = 1, 2, 2
		envs = []v1alpha1.EnvironmentSpec{canary, eu, us}
		p.GateEnv, p.Upstream = "eu", "canary"
	case "fleet":
		prod := envSpec("prod")
		one := intstr.FromInt32(1)
		prod.Fleet = &v1alpha1.FleetSpec{MaxConcurrent: 1, MaxUnavailable: &one,
			Targets: []v1alpha1.FleetTarget{{Name: "eu"}, {Name: "us"}, {Name: "ap"}}}
		prod.DependsOn = []string{"dev"}
		envs = []v1alpha1.EnvironmentSpec{envSpec("dev"), prod}
		p.GateEnv, p.Upstream = "prod", "dev"
	}
	if c.Outcome == "rollback" {
		for i := range envs {
			if envs[i].Name == p.GateEnv {
				envs[i].OnHealthFailure = "rollback"
			}
		}
	}
	p.Pipeline = &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: PipelineName},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://" + SCMHost[c.SCM] + "/" + repo, Branch: "main",
				SecretRef: &v1alpha1.SecretRef{Name: GitSecret}},
			Environments: envs,
		},
	}
	if c.Org {
		p.Pipeline.Spec.PolicyNamespaces = nil
	}
	for _, e := range graph.ExpandedEnvironments(p.Pipeline) {
		p.Envs = append(p.Envs, e.Name)
		if e.Name == p.GateEnv || graph.FleetOf(p.Pipeline, e.Name) == p.GateEnv {
			p.GateEnvs = append(p.GateEnvs, e.Name)
		}
	}
	p.Gate, p.Extra = gateObjects(c, p, now, block)
	if c.Update == "argocd" {
		for _, e := range p.Envs {
			p.Extra = append(p.Extra, ArgoApplication("argocd", PipelineName+"-"+e, BaseTag))
		}
	}
	return p
}

func healthConfig(h string) v1alpha1.HealthConfig {
	hc := v1alpha1.HealthConfig{Timeout: healthTimeout}
	switch h {
	case "resource":
		hc.Type = "resource"
		hc.LabelSelector = map[string]string{"app": PipelineName}
	case "deployment":
	default:
		hc.Type = h
	}
	return hc
}

func gateObjects(c Combo, p *Plan, now time.Time, block bool) (*v1alpha1.PolicyGate, []client.Object) {
	scope := "team"
	if c.Org {
		scope = "org"
	}
	gate := &v1alpha1.PolicyGate{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.PolicyNS, Name: c.Gate + "-gate",
			Labels: map[string]string{"kardinal.io/scope": scope, "kardinal.io/applies-to": p.GateEnv}},
		Spec: v1alpha1.PolicyGateSpec{Message: "scenario " + c.Gate + " gate"},
	}
	soak := passingSoak
	if block {
		soak = blockingSoak
	}
	var extra []client.Object
	switch c.Gate {
	case "cel":
		hour := 9
		if block {
			hour = 11
		}
		gate.Spec.Expression = fmt.Sprintf("!schedule.isWeekend && schedule.hour >= %d", hour)
	case "approval":
		gate.Spec.Expression = "true"
		gate.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, AllowedUsers: []string{Approver}}
	case "metric":
		gate.Spec.Expression = `metrics["error-rate"].result == "Pass"`
		extra = append(extra, &v1alpha1.MetricCheck{
			ObjectMeta: metav1.ObjectMeta{Namespace: p.PolicyNS, Name: "error-rate"},
			Spec: v1alpha1.MetricCheckSpec{Provider: "prometheus", PrometheusURL: "http://prometheus.monitoring:9090",
				Query: MetricQuery, Interval: "10s", Threshold: v1alpha1.MetricThreshold{Operator: "lt", Value: 0.05}},
		})
	case "changewindow":
		gate.Spec.Expression = fmt.Sprintf("!changewindow[%q]", WindowName)
		start, end := now.Add(48*time.Hour), now.Add(49*time.Hour)
		if block {
			start, end = now.Add(-time.Minute), now.Add(2*time.Hour)
		}
		extra = append(extra, &v1alpha1.ChangeWindow{
			ObjectMeta: metav1.ObjectMeta{Name: WindowName},
			Spec: v1alpha1.ChangeWindowSpec{Type: "blackout", Start: metav1.NewTime(start), End: metav1.NewTime(end),
				Reason: "release freeze"},
		})
	case "soak":
		gate.Spec.Expression = fmt.Sprintf("upstream[%q].soakMinutes >= %d", p.Upstream, soak)
	case "upstream":
		gate.Spec.Expression = fmt.Sprintf("bundle.upstreamSoakMinutes >= %d", soak)
	case "verify":
		// An in-cluster agent verifies the version upstream; while the
		// gate is closed its run is inconclusive (held), and a QA lead's
		// pass opens it.
		gate.Spec.Expression = "true"
		gate.Spec.Verify = &v1alpha1.GateVerifyPolicy{Check: VerifyCheck, Environment: p.Upstream,
			AllowedAgents: []string{QAAgent}, AllowedUsers: []string{QALead}, OnInconclusive: "hold",
			Run: &v1alpha1.GateVerifyRun{Template: QAAgent, Count: 1}}
		extra = append(extra, QATemplate())
	}
	return gate, extra
}

// Opener opens a scenario's closed gate the way the people or the time
// around it would.
func (p *Plan) Open(w *World, bundles ...string) error {
	switch p.Gate.Spec.Approval {
	case nil:
	default:
		return w.ApproveAll(bundles...)
	}
	switch p.Combo.Gate {
	case "cel":
		w.Advance(time.Hour)
	case "metric":
		w.Metrics.Set(MetricQuery, metricPassing)
	case "changewindow":
		var cw v1alpha1.ChangeWindow
		if err := w.Client.Get(w.Ctx, client.ObjectKey{Name: WindowName}, &cw); err != nil {
			return fmt.Errorf("get window: %w", err)
		}
		// The release manager ends the freeze early: as a person, not the
		// controller (the freezes admission policy lets the controller write
		// only freezes, docs/design/18-actor-policy.md section 9.5).
		cw.Spec.End = metav1.NewTime(w.Now().Add(-time.Second))
		if err := w.Client.Update(AsUser(w.Ctx, AdminUser, AdminGroups...), &cw); err != nil {
			return fmt.Errorf("end window: %w", err)
		}
	case "soak", "upstream":
		w.Advance(time.Duration(blockingSoak+1) * time.Minute)
	case "verify":
		return w.VerifyAll(QALead, v1alpha1.VerdictPass, bundles...)
	}
	return nil
}

// ApproveAll records Approver's approval of every gate instance with an
// approval policy that waits, for bundles (all when empty), as kardinal
// approve does (lifecycle.RecordApproval).
func (w *World) ApproveAll(bundles ...string) error {
	var gates v1alpha1.PolicyGateList
	if err := w.Client.List(w.Ctx, &gates); err != nil {
		return fmt.Errorf("list gates: %w", err)
	}
	want := map[string]bool{}
	for _, b := range bundles {
		want[b] = true
	}
	seen := map[string]bool{}
	for _, g := range gates.Items {
		b, env := g.Labels["kardinal.io/bundle"], g.Labels["kardinal.io/environment"]
		if g.Spec.Approval == nil || b == "" || env == "" || (len(want) > 0 && !want[b]) || g.Status.Ready {
			continue
		}
		key := g.Namespace + "/" + b + "/" + env
		if seen[key] {
			continue
		}
		seen[key] = true
		id := Identity{Username: Approver, Groups: []string{"system:authenticated"}}
		stamp, err := w.StampFor(id, actor.Where{Namespace: g.Namespace, Environment: env})
		if err != nil {
			return fmt.Errorf("approve %s: %w", key, err)
		}
		_, _, err = lifecycle.RecordApproval(w.CtxOf(id), w.Client, lifecycle.ApprovalRequest{Namespace: g.Namespace, Bundle: b,
			Environment: env, User: Approver, Decision: lifecycle.DecisionApprove, Comment: "scenario", Actor: stamp})
		if err != nil && !strings.Contains(err.Error(), "never promotes again") {
			return fmt.Errorf("approve %s: %w", key, err)
		}
	}
	return nil
}

// Reviewer approves and merges every open PR, as a reviewer does.
func Reviewer(w *World) error {
	for _, pr := range w.SCM.PRs() {
		if pr.State != "open" {
			continue
		}
		if err := w.SCM.Approve(pr.Repo, pr.Number, "reviewer"); err != nil {
			return err
		}
		if err := w.SCM.Merge(pr.Repo, pr.Number); err != nil {
			return err
		}
	}
	return nil
}

// Setup creates the tenant, the repository and the plan's objects.
func (p *Plan) Setup(w *World) error {
	for _, o := range TenantObjects(p.NS) {
		if err := w.Client.Create(w.Ctx, o); err != nil {
			return fmt.Errorf("tenant: %w", err)
		}
	}
	if p.PolicyNS != p.NS {
		if _, err := w.Apply(TenantObjects(p.PolicyNS)[0]); err != nil {
			return err
		}
	}
	if err := w.Git.CreateRepo(p.Pipeline.Spec.Git.URL, "main", RepoFiles(p.Pipeline, BaseTag)); err != nil {
		return err
	}
	objs := append([]client.Object{}, p.Extra...)
	objs = append(objs, p.Gate)
	for _, o := range objs {
		reasons, err := w.Apply(o)
		if err != nil {
			return err
		}
		if len(reasons) > 0 {
			return fmt.Errorf("admission refused %s %s: %s", kindOf(o), o.GetName(), strings.Join(reasons, "; "))
		}
	}
	return nil
}

// Running returns the tag each environment runs.
func (p *Plan) Running(w *World) map[string]string {
	var pl v1alpha1.Pipeline
	if err := w.Client.Get(w.Ctx, client.ObjectKeyFromObject(p.Pipeline), &pl); err != nil {
		w.T.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range graph.ExpandedEnvironments(&pl) {
		st, err := w.GitOps.Running(&pl, e)
		if err != nil {
			w.T.Fatalf("running %s: %v", e.Name, err)
		}
		out[e.Name] = st.Tag
	}
	return out
}

// Verified returns the environments where bundle's step is Verified.
func (w *World) Verified(ns, bundle string) []string {
	var out []string
	for env, s := range w.Steps(ns, bundle) {
		if s.Status.State == "Verified" {
			out = append(out, env)
		}
	}
	sort.Strings(out)
	return out
}

// InFlight reports whether a step of bundle is not in a terminal state.
func (w *World) InFlight(ns, bundle string) bool {
	for _, s := range w.Steps(ns, bundle) {
		switch s.Status.State {
		case "Verified", "Failed", "Superseded", "AbortedByAlarm", "RollingBack":
		default:
			return true
		}
	}
	return false
}
