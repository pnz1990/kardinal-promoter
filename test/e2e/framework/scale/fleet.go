// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/invariants"
)

// ImageRepo is the image every scale Bundle promotes, tagged per Bundle.
// Nothing pulls it: the scale suite checks what kardinal writes to git, and
// health passes on HealthDeployment, which runs another image.
const ImageRepo = "ghcr.io/kardinal-scale/app"

// SeedTag is the tag every environment starts at.
const SeedTag = "seed"

// HealthDeployment is the Deployment, in each scale namespace, that every
// environment's resource health check reads. It runs the pause image, not
// ImageRepo, so the check reports it healthy without comparing images: the
// scale suite measures kardinal, not a GitOps engine's sync of 150
// environments.
const HealthDeployment = "scale-health"

// pauseImage is the image HealthDeployment runs; kind's node has it.
const pauseImage = "registry.k8s.io/pause:3.10"

// Fleet is one scale test's namespace, its repos and its Pipelines.
type Fleet struct {
	E  *framework.Env
	NS string

	// rng picks the Pipeline of each sustained-load Bundle.
	rng *RNG

	mu        sync.Mutex
	pipelines map[string]*v1alpha1.Pipeline
	repos     map[string]gitserver.Repo
}

// NewFleet creates the test's namespace (framework.Env.Namespace) and its
// HealthDeployment, and waits until that is available. rng drives the
// fleet's random choices.
func NewFleet(t *testing.T, e *framework.Env, rng *RNG) *Fleet {
	t.Helper()
	f := &Fleet{E: e, NS: e.Namespace(t), rng: rng, pipelines: map[string]*v1alpha1.Pipeline{}, repos: map[string]gitserver.Repo{}}
	f.healthDeployment(t)
	return f
}

func (f *Fleet) healthDeployment(t *testing.T) {
	t.Helper()
	one := int32(1)
	labels := map[string]string{"app": HealthDeployment}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: HealthDeployment, Namespace: f.NS},
		Spec: appsv1.DeploymentSpec{
			Replicas: &one,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "pause", Image: pauseImage, ImagePullPolicy: corev1.PullIfNotPresent,
				}}},
			},
		},
	}
	if err := f.E.Client.Create(context.Background(), d); err != nil {
		t.Fatalf("create %s/%s: %v", f.NS, HealthDeployment, err)
	}
	framework.Eventually(t, 3*time.Minute, "health Deployment available", func(ctx context.Context) (bool, string) {
		var got appsv1.Deployment
		if err := f.E.Client.Get(ctx, types.NamespacedName{Namespace: f.NS, Name: HealthDeployment}, &got); err != nil {
			return false, err.Error()
		}
		return got.Status.AvailableReplicas == 1, fmt.Sprintf("available %d", got.Status.AvailableReplicas)
	})
}

// Kustomization is the overlay of one environment: only an images entry for
// ImageRepo at tag, which the kustomize update strategy rewrites.
func Kustomization(tag string) []byte {
	return []byte(fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images:
  - name: %s
    newTag: %s
`, ImageRepo, tag))
}

// EnvPath is the directory of pipeline's environment env: apps/<pipeline>/<env>,
// so several Pipelines can share a repo.
func EnvPath(pipeline, env string) string { return "apps/" + pipeline + "/" + env }

// RepoFiles seeds a repo with SeedTag overlays for each pipeline's envs.
func RepoFiles(pipelineEnvs map[string][]string) map[string][]byte {
	files := map[string][]byte{}
	for p, envs := range pipelineEnvs {
		for _, env := range envs {
			files[EnvPath(p, env)+"/kustomization.yaml"] = Kustomization(SeedTag)
		}
	}
	return files
}

// Repo creates a repo named name holding the SeedTag overlays of
// pipelineEnvs, with the suite's webhook (framework.Env.Repo: deleted when the
// test ends).
func (f *Fleet) Repo(t *testing.T, name string, pipelineEnvs map[string][]string) gitserver.Repo {
	t.Helper()
	return f.E.Repo(t, name, RepoFiles(pipelineEnvs))
}

// PipelineSpec is a Pipeline named name over repo with envs (from a topology):
// each environment gets EnvPath, the kustomize strategy, approval auto unless
// set, and resource health on HealthDeployment. edits run last.
func (f *Fleet) PipelineSpec(name string, repo gitserver.Repo, envs []v1alpha1.EnvironmentSpec, edits ...func(*v1alpha1.Pipeline)) *v1alpha1.Pipeline {
	p := &v1alpha1.Pipeline{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.NS},
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{
				URL: repo.CloneURL, Branch: repo.Branch,
				SecretRef: &v1alpha1.SecretRef{Name: framework.GitSecretName},
			},
		},
	}
	for _, env := range envs {
		env.Path = EnvPath(name, env.Name)
		if env.Approval == "" {
			env.Approval = "auto"
		}
		env.Update = v1alpha1.UpdateConfig{Strategy: "kustomize"}
		env.Health = v1alpha1.HealthConfig{
			Type: "resource", Timeout: "5m",
			Resource: &v1alpha1.ResourceRef{Name: HealthDeployment, Namespace: f.NS},
		}
		p.Spec.Environments = append(p.Spec.Environments, env)
	}
	for _, edit := range edits {
		edit(p)
	}
	return p
}

// Apply creates p and records it with its repo for the invariants.
func (f *Fleet) Apply(t *testing.T, p *v1alpha1.Pipeline, repo gitserver.Repo) {
	t.Helper()
	if err := f.E.Client.Create(context.Background(), p); err != nil {
		t.Fatalf("create Pipeline %s (%d environments): %v", p.Name, len(p.Spec.Environments), err)
	}
	f.mu.Lock()
	f.pipelines[p.Name] = p
	f.repos[p.Name] = repo
	f.mu.Unlock()
}

// Pipeline creates a repo of its own and a Pipeline named name over it.
func (f *Fleet) Pipeline(t *testing.T, name string, envs []v1alpha1.EnvironmentSpec, edits ...func(*v1alpha1.Pipeline)) *v1alpha1.Pipeline {
	t.Helper()
	repo := f.Repo(t, f.NS+"-"+name, map[string][]string{name: Names(envs)})
	p := f.PipelineSpec(name, repo, envs, edits...)
	f.Apply(t, p, repo)
	return p
}

// Pipelines creates n Pipelines named <prefix>-001 ..., each over a repo of
// its own with envs, eight at a time, and returns their names.
func (f *Fleet) Pipelines(t *testing.T, prefix string, n int, envs []v1alpha1.EnvironmentSpec) []string {
	t.Helper()
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("%s-%03d", prefix, i+1)
	}
	err := Parallel(8, len(names), func(i int) error {
		name := names[i]
		repo, err := f.E.Git.CreateRepo(context.Background(), f.NS+"-"+name, RepoFiles(map[string][]string{name: Names(envs)}))
		if err != nil {
			return fmt.Errorf("create repo for %s: %w", name, err)
		}
		f.trackRepo(t, repo)
		p := f.PipelineSpec(name, repo, envs)
		if err := f.E.Client.Create(context.Background(), p); err != nil {
			return fmt.Errorf("create Pipeline %s: %w", name, err)
		}
		f.mu.Lock()
		f.pipelines[name] = p
		f.repos[name] = repo
		f.mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// trackRepo registers the webhook on repo and deletes it when the test ends,
// as framework.Env.Repo does for the repos it creates itself. Created
// concurrently, the repos skip Env.Repo's t.Fatal.
func (f *Fleet) trackRepo(t *testing.T, repo gitserver.Repo) {
	t.Helper()
	if url := WebhookURL(); url != "" {
		if err := f.E.Git.AddWebhook(context.Background(), repo, url, WebhookSecret()); err != nil {
			t.Errorf("add webhook to %s: %v", repo.Name, err)
		}
	}
	t.Cleanup(func() {
		if Keep() {
			return
		}
		f.E.DrainNamespaces(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := f.E.Git.DeleteRepo(ctx, repo); err != nil {
			t.Errorf("delete repo %s: %v", repo.Name, err)
		}
	})
}

// Targets returns every Pipeline the fleet created with its repo, sorted by
// name, for the invariants. A Pipeline edited since (Update) is read again.
func (f *Fleet) Targets() []invariants.Target {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]invariants.Target, 0, len(f.pipelines))
	for name, p := range f.pipelines {
		var cur v1alpha1.Pipeline
		if err := f.E.Client.Get(context.Background(), client.ObjectKeyFromObject(p), &cur); err == nil {
			p = &cur
		}
		out = append(out, invariants.Target{Pipeline: p, Repo: f.repos[name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pipeline.Name < out[j].Pipeline.Name })
	return out
}

// Track records a Pipeline the test created itself, with its repo.
func (f *Fleet) Track(p *v1alpha1.Pipeline, repo gitserver.Repo) {
	f.mu.Lock()
	f.pipelines[p.Name] = p
	f.repos[p.Name] = repo
	f.mu.Unlock()
}

// Tag is the image tag of the i-th Bundle a test creates for pipeline.
func Tag(pipeline string, i int) string { return fmt.Sprintf("%s-b%04d", pipeline, i) }

// BundleSpec is an image Bundle of pipeline at ImageRepo:tag.
func (f *Fleet) BundleSpec(pipeline, tag string) *v1alpha1.Bundle {
	b := &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Namespace: f.NS, GenerateName: pipeline + "-"},
		Spec: v1alpha1.BundleSpec{
			Type: "image", Pipeline: pipeline,
			Images: []v1alpha1.ImageRef{{Repository: ImageRepo, Tag: tag}},
			Provenance: &v1alpha1.BundleProvenance{
				Author: "scale-e2e", CommitSHA: fmt.Sprintf("%040x", time.Now().UnixNano()),
			},
		},
	}
	return b
}

// Created is a Bundle a test created and how long the create call took.
type Created struct {
	Name, Pipeline, Tag string
	At                  time.Time
	Latency             time.Duration
}

// CreateBundle creates one Bundle of pipeline at tag, as CI does (the API,
// with kardinal.io/created-at stamped).
func (f *Fleet) CreateBundle(ctx context.Context, pipeline, tag string) (Created, error) {
	b := f.BundleSpec(pipeline, tag)
	start := time.Now()
	lifecycle.StampCreatedAt(b, start)
	if err := f.E.Client.Create(ctx, b); err != nil {
		return Created{}, fmt.Errorf("create Bundle of %s at %s: %w", pipeline, tag, err)
	}
	return Created{Name: b.Name, Pipeline: pipeline, Tag: tag, At: start, Latency: time.Since(start)}, nil
}

// MustCreateBundle is CreateBundle that fails the test.
func (f *Fleet) MustCreateBundle(t *testing.T, pipeline, tag string) Created {
	t.Helper()
	c, err := f.CreateBundle(context.Background(), pipeline, tag)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Bundles lists the namespace's Bundles.
func (f *Fleet) Bundles(ctx context.Context) ([]v1alpha1.Bundle, error) {
	var list v1alpha1.BundleList
	if err := f.E.Client.List(ctx, &list, client.InNamespace(f.NS)); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// TerminalPhase reports whether a Bundle phase is final.
func TerminalPhase(phase string) bool {
	switch phase {
	case "Verified", "Failed", "Superseded":
		return true
	}
	return false
}

// PhaseCounts counts Bundles by phase ("" as "New").
func PhaseCounts(bundles []v1alpha1.Bundle) map[string]int {
	out := map[string]int{}
	for _, b := range bundles {
		p := b.Status.Phase
		if p == "" {
			p = "New"
		}
		out[p]++
	}
	return out
}

// WaitSettled waits until every Bundle in the namespace is in a terminal
// phase, logging progress each minute. On timeout it reports an error (not
// fatal: the invariants still run and list the stragglers) and returns false.
func (f *Fleet) WaitSettled(t *testing.T, timeout time.Duration) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start, lastLog := time.Now(), time.Now()
	var last string
	for {
		bundles, err := f.Bundles(ctx)
		if err == nil {
			counts := PhaseCounts(bundles)
			var open []string
			for _, b := range bundles {
				if !TerminalPhase(b.Status.Phase) {
					open = append(open, b.Name+"="+b.Status.Phase)
				}
			}
			last = fmt.Sprintf("%v", counts)
			if len(open) == 0 {
				// A terminal Bundle's steps finish too: a superseded step still
				// closing its PR is not done.
				open, err = f.openSteps(ctx, bundles)
				if err != nil {
					open = []string{err.Error()}
				}
			}
			if len(open) == 0 {
				t.Logf("all %d Bundles and their steps terminal after %s: %s", len(bundles), time.Since(start).Round(time.Second), last)
				return true
			}
			if time.Since(lastLog) > time.Minute {
				lastLog = time.Now()
				t.Logf("settling %s: %s", time.Since(start).Round(time.Second), last)
			}
			if len(open) > 10 {
				open = append(open[:10], fmt.Sprintf("... %d more", len(open)-10))
			}
			last += " open: " + strings.Join(open, ", ")
		} else {
			last = err.Error()
		}
		select {
		case <-ctx.Done():
			t.Errorf("not every Bundle reached a terminal phase within %s: %s", timeout, last)
			return false
		case <-time.After(5 * time.Second):
		}
	}
}

// openSteps lists the steps not yet in a terminal state.
func (f *Fleet) openSteps(ctx context.Context, bundles []v1alpha1.Bundle) ([]string, error) {
	var list v1alpha1.PromotionStepList
	if err := f.E.Client.List(ctx, &list, client.InNamespace(f.NS)); err != nil {
		return nil, err
	}
	var open []string
	for _, s := range list.Items {
		switch s.Status.State {
		case "Verified", "Failed", "AbortedByAlarm", "RollingBack":
		default:
			open = append(open, fmt.Sprintf("step %s=%q (%s)", s.Name, s.Status.State, s.Status.Message))
		}
	}
	return open, nil
}

// Parallel runs fn(0..n-1) with at most workers at once and returns the
// first error.
func Parallel(workers, n int, fn func(i int) error) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	next := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				if err := fn(i); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
	return first
}
