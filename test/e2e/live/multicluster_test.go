//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// The multi-cluster suite runs kardinal, Argo CD and Flux in the hub (the
// test cluster) and the workloads in the spoke, a second kind cluster that
// hack/e2e/components/spoke.sh registers with the hub's Argo CD and Flux.
// kardinal has no credentials for the spoke: the tests check that it
// verifies spoke workloads through the hub's objects alone.

// newSpokeApp is newArgoApp with the environments deployed into the spoke: a
// hub Application per environment whose destination is the spoke. files
// renders the repo (fixtures.KustomizeRepo or a RolloutRepo).
func newSpokeApp(t *testing.T, e *framework.Env, s *framework.Spoke, files func(fixtures.App) map[string][]byte, envs ...string) *app {
	t.Helper()
	ns := e.Namespace(t)
	s.Namespace(t, ns)
	a := &app{e: e, ns: ns, envs: envs, repo: e.Repo(t, ns, files(fixtures.App{Namespace: ns, Envs: envs}))}
	for _, env := range envs {
		e.ArgoAppIn(t, s.Server, a.argoApp(env), a.repo, fixtures.Path(env), ns)
	}
	for _, env := range envs {
		e.WaitArgoApp(t, a.argoApp(env), syncTimeout)
	}
	return a
}

// notInHub fails the test unless the hub has no Deployment ns/name: the
// workload runs only in the spoke.
func notInHub(t *testing.T, e *framework.Env, ns, name string) {
	t.Helper()
	err := e.Client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &appsv1.Deployment{})
	require.Truef(t, apierrors.IsNotFound(err), "Deployment %s/%s runs only in the spoke; the hub returned %v", ns, name, err)
}

// TestMultiCluster_ArgoHub checks the Argo CD hub of docs/multi-cluster.md:
// kardinal and Argo CD run in the hub, the workload in the spoke, and
// health.type argocd reads the hub Application whose destination is the
// spoke. A release is Verified once the spoke runs it and the Application
// reports the promoted commit. A release whose pods never start turns the
// Application Degraded, which counts as a health failure, and the step fails
// at health.timeout.
//
// Covers MC-ARGO-01.
func TestMultiCluster_ArgoHub(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	s := e.Spoke(t)
	a := newSpokeApp(t, e, s, fixtures.KustomizeRepo, "prod")
	application, workload := a.argoApp("prod"), fixtures.Workload("prod")
	s.WaitDeploymentImage(t, a.ns, workload, fixtures.Image+":"+fixtures.V1, syncTimeout)
	notInHub(t, e, a.ns, workload)
	p := a.pipeline(nil)
	p.Spec.Environments[0].Health.Timeout = "3m"
	a.apply(t, p)

	v2 := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", v2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Contains(t, ps.Status.Message, "health check passed via argocd: Healthy+Synced")
	require.Contains(t, e.ReadFile(t, a.repo, a.repo.Branch, fixtures.Path("prod")+"/kustomization.yaml"), "newTag: "+fixtures.V2)
	s.WaitDeploymentImage(t, a.ns, workload, v2, 30*time.Second)
	notInHub(t, e, a.ns, workload)
	st, err := e.ArgoAppState(context.Background(), application)
	require.NoError(t, err)
	require.NotEmpty(t, ps.Status.Outputs["commitSHA"])
	assert.Equal(t, ps.Status.Outputs["commitSHA"], st.Revision, "the Application synced the promoted commit: %s", st)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)

	broken := fixtures.Image + ":" + fixtures.BrokenTag
	bad := e.CreateBundle(t, a.ns, pipelineName, "--image", broken)
	ps = e.WaitStepMessageAll(t, a.ns, pipelineName, bad, "prod", "HealthChecking", 3*time.Minute,
		"unhealthy via argocd: health=Degraded")
	assert.Positive(t, ps.Status.ConsecutiveHealthFailures, "Degraded counts as a health failure")
	assert.Equal(t, broken, s.DeploymentImage(t, a.ns, workload), "the broken release reached the spoke")
	ps = e.WaitStepState(t, a.ns, pipelineName, bad, "prod", "Failed", 4*time.Minute)
	assert.Contains(t, ps.Status.Message, "health check timeout after 3m0s; last result: unhealthy via argocd: health=Degraded")
	e.WaitBundlePhase(t, a.ns, bad, "Failed", time.Minute)
}

// TestMultiCluster_FluxHub checks the Flux hub of docs/multi-cluster.md: a
// Kustomization in the hub with spec.kubeConfig.secretRef applies the
// environment to the spoke, and health.type flux reads it in the hub. With
// spec.wait its Ready condition covers the spoke's workload: a release is
// Verified once the spoke runs it and the Kustomization applied the promoted
// commit, and a release whose pods never start stalls in the spoke: Flux
// gives up on the promoted commit (Ready=False) and the step fails.
//
// Covers MC-FLUX-01.
func TestMultiCluster_FluxHub(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	s := e.Spoke(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	s.Namespace(t, ns)
	a := &app{e: e, ns: ns, envs: []string{"prod"},
		repo: e.Repo(t, ns, fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: []string{"prod"}}))}
	ks, workload := ns+"-prod", fixtures.Workload("prod")
	// Flux marks the Kustomization Ready=False once the wait times out, and
	// retries 5m later: 2m leaves room for a cold image pull.
	e.RemoteKustomization(t, ks, a.repo, fixtures.Path("prod"), 2*time.Minute)
	e.WaitFluxReady(t, framework.KustomizationGVR, framework.FluxNamespace, ks, syncTimeout)
	s.WaitDeploymentImage(t, ns, workload, fixtures.Image+":"+fixtures.V1, 30*time.Second)
	notInHub(t, e, ns, workload)

	p := a.pipeline(nil)
	p.Spec.Environments[0].Health = v1alpha1.HealthConfig{Type: "flux", Timeout: "4m",
		Flux: &v1alpha1.HealthTargetRef{Name: ks, Namespace: framework.FluxNamespace}}
	a.apply(t, p)

	v2 := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, ns, pipelineName, "--image", v2)
	ps := e.WaitStepState(t, ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Contains(t, ps.Status.Message, "health check passed via flux: Ready=True, generation=")
	s.WaitDeploymentImage(t, ns, workload, v2, 30*time.Second)
	notInHub(t, e, ns, workload)
	k, err := e.FluxObject(ctx, framework.KustomizationGVR, framework.FluxNamespace, ks)
	require.NoError(t, err)
	require.NotEmpty(t, ps.Status.Outputs["commitSHA"])
	assert.True(t, strings.HasSuffix(framework.FluxAppliedRevision(k), "sha1:"+ps.Status.Outputs["commitSHA"]),
		"the Kustomization applied the promoted commit %s: lastAppliedRevision=%s", ps.Status.Outputs["commitSHA"], framework.FluxAppliedRevision(k))
	e.WaitBundlePhase(t, ns, bundle, "Verified", time.Minute)

	broken := fixtures.Image + ":" + fixtures.BrokenTag
	bad := e.CreateBundle(t, ns, pipelineName, "--image", broken)
	// Flux reports the spoke's stalled rollout as Ready=False, which fails
	// the step at once when Flux names the stall, or at the 4m health.timeout
	// when its own 2m wait expires first (HEALTH-FLUX-08 covers the stall).
	ps = e.WaitStepState(t, ns, pipelineName, bad, "prod", "Failed", 5*time.Minute)
	assert.Regexp(t, `^(health alarm via flux \(onHealthFailure=none\): |health check timeout after 4m0s; last result: unhealthy via flux: )Ready=False`,
		ps.Status.Message, "the step fails on the Kustomization's Ready=False for the spoke")
	assert.Equal(t, broken, s.DeploymentImage(t, ns, workload), "the broken release reached the spoke")
	e.WaitBundlePhase(t, ns, bad, "Failed", time.Minute)
}

// TestMultiCluster_RolloutsInSpoke checks the limit docs/pipeline-reference.md
// states for Argo Rollouts: health.type argoRollouts (with delivery.delegate,
// as the multi-cluster example sets) reads the Rollout in the controller's
// cluster only. A Rollout that the hub's Argo CD deploys to the spoke rolls
// the release out there and is Healthy, but the step finds no Rollout in the
// hub (which serves the Rollout CRD), counts it as a health failure and fails
// at health.timeout.
//
// Covers MC-ROLLOUTS-01.
func TestMultiCluster_RolloutsInSpoke(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	s := e.Spoke(t)
	a := newSpokeApp(t, e, s, func(app fixtures.App) map[string][]byte { return fixtures.RolloutRepo(app, "5s") }, "prod")
	rollout := fixtures.Workload("prod")
	s.WaitRolloutHealthy(t, a.ns, rollout, fixtures.Image+":"+fixtures.V1, syncTimeout)
	a.apply(t, a.deliveryPipeline(rollouts, false, func(env *v1alpha1.EnvironmentSpec) {
		env.Delivery.Delegate = rollouts
		env.Health.Timeout = "1m"
	}))

	v2 := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", v2)
	missing := fmt.Sprintf("unhealthy via argoRollouts: Rollout %s/%s not found", a.ns, rollout)
	ps := e.WaitStepMessageAll(t, a.ns, pipelineName, bundle, "prod", "HealthChecking", promoteTimeout, missing)
	assert.Positive(t, ps.Status.ConsecutiveHealthFailures, "a missing Rollout counts as a health failure")
	s.WaitRolloutHealthy(t, a.ns, rollout, v2, 2*time.Minute)
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", 2*time.Minute)
	assert.Contains(t, ps.Status.Message, "health check timeout after 1m0s; last result: "+missing)
	e.WaitBundlePhase(t, a.ns, bundle, "Failed", time.Minute)

	inHub, err := e.Dynamic.Resource(framework.RolloutGVR).Namespace(a.ns).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err, "the hub serves the Rollout CRD")
	assert.Empty(t, inHub.Items, "the Rollout runs only in the spoke")
	r, err := s.GetRollout(context.Background(), a.ns, rollout)
	require.NoError(t, err)
	assert.True(t, r.Image == v2 && r.Phase == "Healthy" && r.Done, "the spoke rolled the release out: %s", r)
}

// The multi-cluster-fleet example.
const (
	fleetDir      = "examples/multi-cluster-fleet"
	fleetPipeline = "rollouts-demo"
)

// fleetEnvs are the example's environments; fleetProds run Argo Rollouts in
// the spoke, the others Deployments in the hub.
var (
	fleetEnvs  = []string{"test", "pre-prod", "prod-eu", "prod-us"}
	fleetProds = []string{"prod-eu", "prod-us"}
)

// readFleet reads file of the example.
func readFleet(t *testing.T, file string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", fleetDir, file))
	require.NoError(t, err)
	return raw
}

// fleetPipelineFor is the example's pipeline.yaml over repo in ns: git and the
// environment paths are the test's, everything else is the example's,
// including health.type argocd on the Application named after the Pipeline
// and the environment.
func fleetPipelineFor(t *testing.T, ns string, repo gitserver.Repo) *v1alpha1.Pipeline {
	t.Helper()
	var p v1alpha1.Pipeline
	require.NoError(t, yaml.UnmarshalStrict(readFleet(t, "pipeline.yaml"), &p))
	require.Equal(t, fleetPipeline, p.Name)
	p.Namespace = ns
	p.Spec.Git.URL, p.Spec.Git.Branch = repo.CloneURL, repo.Branch
	p.Spec.Git.SecretRef = &v1alpha1.SecretRef{Name: framework.GitSecretName}
	var names []string
	for i := range p.Spec.Environments {
		env := &p.Spec.Environments[i]
		names = append(names, env.Name)
		require.Equal(t, "env/"+env.Name, env.Path)
		env.Path = fixtures.Path(env.Name)
		require.Equal(t, "argocd", env.Health.Type, env.Name)
		require.Nil(t, env.Health.ArgoCD, "%s checks the default Application", env.Name)
	}
	require.Equal(t, fleetEnvs, names)
	return &p
}

// fleetGates are the example's policy-gates.yaml PolicyGates, with the
// pre-prod soak cut from 30 minutes to 1.
func fleetGates(t *testing.T) []*v1alpha1.PolicyGate {
	t.Helper()
	var gates []*v1alpha1.PolicyGate
	for _, doc := range strings.Split(string(readFleet(t, "policy-gates.yaml")), "\n---") {
		var obj unstructured.Unstructured
		require.NoError(t, yaml.Unmarshal([]byte(doc), &obj.Object))
		switch obj.GetKind() {
		case "":
			continue
		case "Namespace":
			require.Equal(t, framework.PolicyNamespace, obj.GetName())
		case "PolicyGate":
			var g v1alpha1.PolicyGate
			require.NoError(t, yaml.UnmarshalStrict([]byte(doc), &g))
			require.Equal(t, framework.PolicyNamespace, g.Namespace)
			if strings.HasPrefix(g.Name, "pre-prod-soak-") {
				soak := strings.Replace(g.Spec.Expression, ">= 30", ">= 1", 1)
				require.NotEqual(t, g.Spec.Expression, soak, "%s: %s", g.Name, g.Spec.Expression)
				g.Spec.Expression = soak
			}
			gates = append(gates, &g)
		default:
			t.Fatalf("policy-gates.yaml: unexpected %s", obj.GetKind())
		}
	}
	require.Len(t, gates, 4)
	return gates
}

// fleetApplicationSet is the example's ApplicationSet over repo: test and
// pre-prod deploy into the hub, both prod regions into the spoke (server
// spoke), all into namespace ns, where the fixtures put the workloads.
func fleetApplicationSet(t *testing.T, repo gitserver.Repo, ns, spoke string) *unstructured.Unstructured {
	t.Helper()
	set := &unstructured.Unstructured{}
	require.NoError(t, yaml.Unmarshal(readFleet(t, "argocd-applications.yaml"), &set.Object))
	require.Equal(t, "ApplicationSet", set.GetKind())
	gens, _, err := unstructured.NestedSlice(set.Object, "spec", "generators")
	require.NoError(t, err)
	require.Len(t, gens, 1)
	gen, _ := gens[0].(map[string]interface{})
	elements, _, err := unstructured.NestedSlice(gen, "list", "elements")
	require.NoError(t, err)
	var envs []string
	for _, el := range elements {
		m, _ := el.(map[string]interface{})
		env, _ := m["env"].(string)
		envs = append(envs, env)
		m["server"] = framework.InClusterServer
		if strings.HasPrefix(env, "prod-") {
			m["server"] = spoke
		}
	}
	require.Equal(t, fleetEnvs, envs)
	require.NoError(t, unstructured.SetNestedSlice(gen, elements, "list", "elements"))
	require.NoError(t, unstructured.SetNestedSlice(set.Object, []interface{}{gen}, "spec", "generators"))

	spec := []string{"spec", "template", "spec"}
	for field, want := range map[string]string{"source.path": "env/{{env}}", "destination.namespace": "{{env}}"} {
		got, _, _ := unstructured.NestedString(set.Object, append(spec, strings.Split(field, ".")...)...)
		require.Equal(t, want, got, field)
	}
	for field, value := range map[string]string{
		"source.repoURL":        repo.CloneURL,
		"source.targetRevision": repo.Branch,
		"source.path":           fixtures.Path("{{env}}"),
		"destination.namespace": ns,
	} {
		require.NoError(t, unstructured.SetNestedField(set.Object, value, append(spec, strings.Split(field, ".")...)...))
	}
	return set
}

// fleet is the multi-cluster-fleet example running in a test namespace.
type fleet struct {
	e    *framework.Env
	s    *framework.Spoke
	ns   string
	repo gitserver.Repo
}

// stepPR waits until the Bundle's env step waits for its PR, and returns the PR.
func (f *fleet) stepPR(t *testing.T, bundle, env string) gitserver.PR {
	t.Helper()
	ps := f.e.WaitStepState(t, f.ns, fleetPipeline, bundle, env, "WaitingForMerge", promoteTimeout)
	require.NotEmpty(t, ps.Status.PRURL, env)
	return f.e.WaitPR(t, f.repo, time.Minute, env+" PR", func(pr gitserver.PR) bool {
		return pr.State == "open" && strings.HasSuffix(ps.Status.PRURL, fmt.Sprintf("/%d", pr.Number))
	})
}

// weekend reports whether schedule.isWeekend is true at at.
func weekend(at time.Time) bool {
	d := at.UTC().Weekday()
	return d == time.Saturday || d == time.Sunday
}

// promote takes the Bundle through the example's flow to the prod regions
// prods, checking each stage: test is Verified on its own, pre-prod once
// its PR is merged. No prod step starts while the soak gates hold; once they
// pass, every region's step opens its PR at the same time. After the merges,
// Argo CD syncs each region's Rollout in the spoke, whose canary stops at its
// pause step: the hub Application is Suspended, and the step waits without
// counting failures. Promoting the canaries verifies the regions.
func (f *fleet) promote(t *testing.T, bundle, tag string, prods ...string) {
	t.Helper()
	e, ctx, image := f.e, context.Background(), fixtures.Image+":"+tag
	ps := e.WaitStepState(t, f.ns, fleetPipeline, bundle, "test", "Verified", promoteTimeout)
	assert.Contains(t, ps.Status.Message, "health check passed via argocd: Healthy+Synced")
	e.WaitDeploymentImage(t, f.ns, fixtures.Workload("test"), image, 30*time.Second)

	pr := f.stepPR(t, bundle, "pre-prod")
	require.NoError(t, e.Git.MergePR(ctx, f.repo, pr.Number))
	e.WaitStepState(t, f.ns, fleetPipeline, bundle, "pre-prod", "Verified", promoteTimeout)
	e.WaitDeploymentImage(t, f.ns, fixtures.Workload("pre-prod"), image, 30*time.Second)

	for _, env := range prods {
		g := e.WaitGateReady(t, f.ns, bundle, env, "pre-prod-soak-"+env, false, "bundle.upstreamSoakMinutes >= 1 = false", gateTimeout)
		assert.Equal(t, "org", g.Labels["kardinal.io/scope"])
	}
	framework.Consistently(t, holdFor, "no prod step while the soak gates hold", func(ctx context.Context) (bool, string) {
		for _, env := range prods {
			ps, ok, err := e.Step(ctx, f.ns, fleetPipeline, bundle, env)
			if err != nil || ok {
				return false, fmt.Sprintf("%s: step %v, err %v", env, ps, err)
			}
		}
		return true, ""
	})
	for _, env := range prods {
		e.WaitGateReady(t, f.ns, bundle, env, "pre-prod-soak-"+env, true, "= true", 3*time.Minute)
		g := e.WaitGate(t, f.ns, bundle, env, "no-weekend-deploys-"+env, gateTimeout, "evaluated",
			func(g *v1alpha1.PolicyGate) bool { return g.Status.LastEvaluatedAt != nil })
		assert.Equal(t, "org", g.Labels["kardinal.io/scope"])
		require.Equal(t, !weekend(g.Status.LastEvaluatedAt.Time), g.Status.Ready, "no-weekend-deploys: %s", framework.DescribeGate(g))
		if !g.Status.Ready {
			oncall := whoAmI(t, e)
			e.Override(t, g, v1alpha1.PolicyGateOverride{Reason: "e2e runs on weekends", Stage: env,
				CreatedBy: oncall, ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour))})
			e.WaitGateReady(t, f.ns, bundle, env, "no-weekend-deploys-"+env, true, "OVERRIDDEN by "+oncall, gateTimeout)
		}
	}

	prs := map[string]gitserver.PR{}
	for _, env := range prods {
		prs[env] = f.stepPR(t, bundle, env)
	}
	soaked := upstreamStatus(t, e, f.ns, bundle, "pre-prod")
	require.NotNil(t, soaked.HealthCheckedAt)
	all, err := e.Git.PullRequests(ctx, f.repo)
	require.NoError(t, err)
	state := map[int]string{}
	for _, pr := range all {
		state[pr.Number] = pr.State
	}
	for _, env := range prods {
		assert.Equal(t, "open", state[prs[env].Number], "every region's PR is open before any is merged")
		ps, _, err := e.Step(ctx, f.ns, fleetPipeline, bundle, env)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, ps.CreationTimestamp.Sub(soaked.HealthCheckedAt.Time), time.Minute, "%s started after the soak", env)
	}
	for _, env := range prods {
		require.NoError(t, e.Git.MergePR(ctx, f.repo, prs[env].Number))
	}

	for _, env := range prods {
		f.s.WaitRollout(t, f.ns, fixtures.Workload(env), syncTimeout, "paused at the canary step on "+image, func(r framework.Rollout) bool {
			return r.Image == image && r.Phase == "Paused"
		})
		framework.Eventually(t, time.Minute, "Application "+fleetPipeline+"-"+env+" Suspended", func(ctx context.Context) (bool, string) {
			st, err := e.ArgoAppState(ctx, fleetPipeline+"-"+env)
			if err != nil {
				return false, err.Error()
			}
			return st.Health == "Suspended" && st.Sync == "Synced", st.String()
		})
		e.WaitStepMessageAll(t, f.ns, fleetPipeline, bundle, env, "HealthChecking", time.Minute, "waiting for argocd: health=Suspended")
	}
	for _, env := range prods {
		e.HoldStep(t, deliveryHold/2, f.ns, fleetPipeline, bundle, env, "a paused canary waits", func(ps *v1alpha1.PromotionStep) bool {
			return ps.Status.State == "HealthChecking" && ps.Status.ConsecutiveHealthFailures == 0 &&
				strings.Contains(ps.Status.Message, "waiting for argocd: health=Suspended")
		})
	}
	for _, env := range prods {
		f.s.PromoteRollout(t, f.ns, fixtures.Workload(env))
	}
	for _, env := range prods {
		ps := e.WaitStepState(t, f.ns, fleetPipeline, bundle, env, "Verified", deliveryTimeout)
		assert.Contains(t, ps.Status.Message, "health check passed via argocd: Healthy+Synced")
		f.s.WaitRolloutHealthy(t, f.ns, fixtures.Workload(env), image, 30*time.Second)
	}
	e.WaitBundlePhase(t, f.ns, bundle, "Verified", time.Minute)
}

// TestMultiCluster_FleetExample runs examples/multi-cluster-fleet as its
// README describes, with the hub's Argo CD deploying test and pre-prod into
// the hub and both prod regions into the spoke, through the example's
// ApplicationSet. The example's files are used as they are, except for the
// git repo, the paths, the namespaces and the soak, cut from 30 minutes to 1.
//
// The README's Bundle (kardinal create bundle) promotes test, then pre-prod
// on its PR; the org gates hold both regions until the soak passes (and the
// weekend gate matches the day); then both regions open their PRs in
// parallel and are Verified through their hub Applications once their
// canaries finish. bundle.yaml's Bundle, whose intent.targetEnvironment is
// prod-us, promotes only test, pre-prod and prod-us: prod-eu gets no gates
// and no step, and keeps the first release.
//
// Covers EX-FLEET-01.
func TestMultiCluster_FleetExample(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	s := e.Spoke(t)
	ctx := context.Background()
	ns := e.Namespace(t)
	s.Namespace(t, ns)
	files := fixtures.KustomizeRepo(fixtures.App{Namespace: ns, Envs: fleetEnvs[:2]})
	for path, data := range fixtures.RolloutRepo(fixtures.App{Namespace: ns, Envs: fleetProds}, fixtures.IndefinitePause) {
		files[path] = data
	}
	f := &fleet{e: e, s: s, ns: ns, repo: e.Repo(t, ns, files)}

	e.EnsureNamespace(t, framework.PolicyNamespace)
	for _, g := range fleetGates(t) {
		e.CreateGate(t, g)
	}
	e.ApplicationSet(t, fleetApplicationSet(t, f.repo, ns, s.Server))
	for _, env := range fleetEnvs {
		e.WaitArgoApp(t, fleetPipeline+"-"+env, syncTimeout)
	}
	v1 := fixtures.Image + ":" + fixtures.V1
	for _, env := range fleetEnvs[:2] {
		e.WaitDeploymentImage(t, ns, fixtures.Workload(env), v1, syncTimeout)
	}
	for _, env := range fleetProds {
		s.WaitRolloutHealthy(t, ns, fixtures.Workload(env), v1, syncTimeout)
		notInHub(t, e, ns, fixtures.Workload(env))
	}
	require.NoError(t, e.Client.Create(ctx, fleetPipelineFor(t, ns, f.repo)))

	first := e.CreateBundle(t, ns, fleetPipeline, "--image", fixtures.Image+":"+fixtures.V2)
	f.promote(t, first, fixtures.V2, fleetProds...)

	var b v1alpha1.Bundle
	require.NoError(t, yaml.UnmarshalStrict(readFleet(t, "bundle.yaml"), &b))
	require.Equal(t, fleetPipeline, b.Spec.Pipeline)
	require.NotNil(t, b.Spec.Intent)
	require.Equal(t, "prod-us", b.Spec.Intent.TargetEnvironment)
	require.Len(t, b.Spec.Images, 1)
	b.Namespace = ns
	b.Spec.Images[0].Repository, b.Spec.Images[0].Tag = fixtures.Image, fixtures.V3
	targeted := e.CreateBundleObject(t, &b)
	f.promote(t, targeted, fixtures.V3, "prod-us")

	_, found, err := e.Step(ctx, ns, fleetPipeline, targeted, "prod-eu")
	require.NoError(t, err)
	assert.False(t, found, "targetEnvironment prod-us: no prod-eu step")
	for _, template := range []string{"no-weekend-deploys-prod-eu", "pre-prod-soak-prod-eu"} {
		gates, err := e.GateInstances(ctx, ns, targeted, "prod-eu", template)
		require.NoError(t, err)
		assert.Empty(t, gates, "targetEnvironment prod-us: no %s gate", template)
	}
	assert.Contains(t, e.ReadFile(t, f.repo, f.repo.Branch, fixtures.Path("prod-eu")+"/kustomization.yaml"), "newTag: "+fixtures.V2)
	s.WaitRolloutHealthy(t, ns, fixtures.Workload("prod-eu"), fixtures.Image+":"+fixtures.V2, time.Second)
}

// spokeKubeconfig returns the spoke kubeconfig hack/e2e/components/spoke.sh
// gave the hub's Flux: a ServiceAccount token, the spoke's CA, and the
// server as the hub reaches it.
func spokeKubeconfig(t *testing.T, e *framework.Env) string {
	t.Helper()
	s, err := e.Kube.CoreV1().Secrets(framework.FluxNamespace).Get(context.Background(), framework.SpokeKubeconfigSecret, metav1.GetOptions{})
	require.NoError(t, err)
	kc := string(s.Data[framework.SpokeKubeconfigKey])
	require.NotEmpty(t, kc)
	return kc
}

// putKubeconfig creates or replaces the Secret ns/name holding kubeconfig
// under key "kubeconfig".
func putKubeconfig(t *testing.T, e *framework.Env, ns, name, kubeconfig string) {
	t.Helper()
	ctx := context.Background()
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns,
		Labels: map[string]string{"kardinal.io/referenceable": "true"}},
		Data: map[string][]byte{"kubeconfig": []byte(kubeconfig)}}
	if _, err := e.Kube.CoreV1().Secrets(ns).Create(ctx, s, metav1.CreateOptions{}); apierrors.IsAlreadyExists(err) {
		_, err = e.Kube.CoreV1().Secrets(ns).Update(ctx, s, metav1.UpdateOptions{})
		require.NoError(t, err)
	} else {
		require.NoError(t, err)
	}
}

// TestMultiCluster_KubeconfigHealth checks health.kubeconfigSecretRef: the
// hub's Argo CD deploys prod into the spoke, and health.type resource reads
// the Deployment in the spoke through a kubeconfig Secret in the Pipeline
// namespace, so the release is Verified on what runs there. When the Secret
// points at an address where no API server listens, the step reports
// ClusterUnreachable without counting a health failure, and once the Secret
// is fixed (rotation) it is Verified. A kubeconfig with an exec plugin fails
// the step: the controller never runs a command for it.
//
// Covers MC-KUBECONFIG-01, MC-KUBECONFIG-02, MC-KUBECONFIG-03.
func TestMultiCluster_KubeconfigHealth(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	s := e.Spoke(t)
	a := newSpokeApp(t, e, s, fixtures.KustomizeRepo, "prod")
	workload := fixtures.Workload("prod")
	s.WaitDeploymentImage(t, a.ns, workload, fixtures.Image+":"+fixtures.V1, syncTimeout)
	notInHub(t, e, a.ns, workload)
	good := spokeKubeconfig(t, e)
	putKubeconfig(t, e, a.ns, "spoke", good)

	p := a.pipeline(nil)
	p.Spec.Environments[0].Health = v1alpha1.HealthConfig{Type: "resource", Timeout: "4m",
		Resource:            &v1alpha1.ResourceRef{Name: workload, Namespace: a.ns},
		KubeconfigSecretRef: &v1alpha1.KubeconfigSecretRef{Name: "spoke"}}
	a.apply(t, p)

	v2 := fixtures.Image + ":" + fixtures.V2
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", v2)
	ps := e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Contains(t, ps.Status.Message, "health check passed via resource")
	assert.Equal(t, v2, s.DeploymentImage(t, a.ns, workload), "the spoke runs the release")
	notInHub(t, e, a.ns, workload)
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)

	// No API server listens on port 1 of the spoke's node.
	unreachable := strings.Replace(good, ":6443", ":1", 1)
	require.NotEqual(t, good, unreachable)
	putKubeconfig(t, e, a.ns, "spoke", unreachable)
	v3 := fixtures.Image + ":" + fixtures.V3
	bundle = e.CreateBundle(t, a.ns, pipelineName, "--image", v3)
	ps = e.WaitStepMessage(t, a.ns, pipelineName, bundle, "prod", "HealthChecking",
		"waiting for resource: ClusterUnreachable: ", 3*time.Minute)
	assert.Zero(t, ps.Status.ConsecutiveHealthFailures, "an unreachable cluster is not a health failure")
	assert.NotContains(t, ps.Status.Message, "token")
	putKubeconfig(t, e, a.ns, "spoke", good)
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	assert.Contains(t, ps.Status.Message, "health check passed via resource")
	assert.Equal(t, v3, s.DeploymentImage(t, a.ns, workload))
	e.WaitBundlePhase(t, a.ns, bundle, "Verified", time.Minute)

	exec := strings.Replace(good, "    user:\n", "    user:\n      exec: {apiVersion: client.authentication.k8s.io/v1, command: /bin/sh, args: [-c, \"touch /tmp/pwned\"]}\n", 1)
	require.NotEqual(t, good, exec, "the kubeconfig has a users[].user entry")
	putKubeconfig(t, e, a.ns, "spoke", exec)
	bundle = e.CreateBundle(t, a.ns, pipelineName, "--image", v2)
	ps = e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Failed", promoteTimeout)
	assert.Contains(t, ps.Status.Message, `health.kubeconfigSecretRef "spoke": kubeconfig not allowed: users[].user.exec is not supported`)
}
