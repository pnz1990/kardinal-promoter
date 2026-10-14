// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"fmt"
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
	mcrec "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/metriccheck"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/renderjob"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

func apimetaExtract(list client.ObjectList) ([]client.Object, error) {
	items, err := apimeta.ExtractList(list)
	if err != nil {
		return nil, fmt.Errorf("extract list: %w", err)
	}
	out := make([]client.Object, 0, len(items))
	for _, it := range items {
		o, ok := it.(client.Object)
		if !ok {
			return nil, fmt.Errorf("list item %T is not an object", it)
		}
		out = append(out, o)
	}
	return out, nil
}

// FakeMetrics is the metric backend (provider prometheus): each query
// returns the value set for it, and an unknown query fails.
type FakeMetrics struct {
	mu     sync.Mutex
	values map[string]float64
}

// Set sets the value query returns.
func (f *FakeMetrics) Set(query string, v float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[query] = v
}

// Evaluate implements mcrec.Backend.
func (f *FakeMetrics) Evaluate(_ context.Context, q mcrec.Query) (mcrec.Value, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[q.Spec.Query]
	if !ok {
		return mcrec.Value{}, fmt.Errorf("no series for query")
	}
	return mcrec.NumberValue(v), nil
}

// GitOps is the GitOps engine and the workloads: for every environment of
// every Pipeline it reads what git (or, for the argocd strategy, the
// Application) says the environment runs, and makes the environment's
// health object report that version at the branch's head commit, as Argo
// CD, Flux, Argo Rollouts, Flagger and the Deployment controller would once
// they synced. An environment set to fail (Fail) reports the failure of its
// health type instead.
type GitOps struct {
	w  *World
	mu sync.Mutex
	// fail holds environment/tag pairs whose rollout fails.
	fail map[string]bool
}

// Fail makes env fail its health check while it runs tag.
func (g *GitOps) Fail(ns, env, tag string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fail == nil {
		g.fail = map[string]bool{}
	}
	g.fail[ns+"/"+env+"@"+tag] = true
}

func (g *GitOps) failing(ns, env, tag string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fail[ns+"/"+env+"@"+tag]
}

// AppRepository is the image repository every scenario application runs.
const AppRepository = "ghcr.io/acme/app"

var imageRef = regexp.MustCompile(regexp.QuoteMeta(AppRepository) + `:([A-Za-z0-9_.-]+)`)

// EnvState is what an environment runs: the image tag and the commit.
type EnvState struct {
	Tag, Revision, Branch string
}

// Running returns what env of p runs according to its source of truth.
func (g *GitOps) Running(p *v1alpha1.Pipeline, env v1alpha1.EnvironmentSpec) (EnvState, error) {
	if env.Update.Strategy == "argocd" && env.Update.ArgoCD != nil {
		app := &unstructured.Unstructured{}
		app.SetGroupVersionKind(ApplicationGVK)
		ns := env.Update.ArgoCD.Namespace
		if ns == "" {
			ns = "argocd"
		}
		if err := g.w.Client.Get(g.w.Ctx, client.ObjectKey{Namespace: ns, Name: env.Update.ArgoCD.Application}, app); err != nil {
			return EnvState{}, fmt.Errorf("argocd strategy application: %w", err)
		}
		key := env.Update.ArgoCD.ImageKey
		if key == "" {
			key = "image.tag"
		}
		fields := append([]string{"spec", "source", "helm", "valuesObject"}, strings.Split(key, ".")...)
		tag, _, _ := unstructured.NestedString(app.Object, fields...)
		return EnvState{Tag: tag, Revision: "valuesObject-" + tag}, nil
	}
	dir := env.Path
	if dir == "" {
		dir = "environments/" + env.Name
	}
	branch := p.Spec.Git.Branch
	if branch == "" {
		branch = "main"
	}
	if env.Layout == "branch" {
		branch = "env/" + env.Name
		if env.Render != nil && env.Render.Branch != "" {
			branch = env.Render.Branch
		}
	}
	sha, err := g.w.Git.Head(p.Spec.Git.URL, branch)
	if err != nil || sha == "" {
		return EnvState{Branch: branch}, err
	}
	st := EnvState{Revision: sha, Branch: branch}
	read := func(p2 string) (string, bool, error) { return g.w.Git.ReadFile(p.Spec.Git.URL, branch, p2) }
	switch {
	case env.Layout == "branch":
		files, err := g.w.Git.ListFiles(p.Spec.Git.URL, branch)
		if err != nil {
			return st, err
		}
		for _, f := range files {
			c, _, err := read(f)
			if err != nil {
				return st, err
			}
			if m := imageRef.FindStringSubmatch(c); m != nil {
				st.Tag = m[1]
			}
		}
	case env.Update.Strategy == "helm":
		file := "values.yaml"
		if env.Update.Helm != nil && env.Update.Helm.ValuesFile != "" {
			file = env.Update.Helm.ValuesFile
		}
		c, ok, err := read(path.Join(dir, file))
		if err != nil || !ok {
			return st, err
		}
		var v struct {
			Image struct {
				Tag string `json:"tag"`
			} `json:"image"`
		}
		if err := yaml.Unmarshal([]byte(c), &v); err != nil {
			return st, fmt.Errorf("values: %w", err)
		}
		st.Tag = v.Image.Tag
	case env.Update.Strategy == "yaml":
		for _, u := range env.Update.YAML.Updates {
			c, ok, err := read(path.Join(dir, u.File))
			if err != nil || !ok {
				return st, err
			}
			if m := imageRef.FindStringSubmatch(c); m != nil {
				st.Tag = m[1]
			}
		}
	default:
		c, ok, err := read(path.Join(dir, "kustomization.yaml"))
		if err != nil || !ok {
			return st, err
		}
		var k struct {
			Images []struct {
				Name   string `json:"name"`
				NewTag string `json:"newTag"`
			} `json:"images"`
		}
		if err := yaml.Unmarshal([]byte(c), &k); err != nil {
			return st, fmt.Errorf("kustomization: %w", err)
		}
		for _, im := range k.Images {
			if im.Name == AppRepository {
				st.Tag = im.NewTag
			}
		}
		if st.Tag == "" {
			d, _, _ := read(path.Join(dir, "deployment.yaml"))
			if m := imageRef.FindStringSubmatch(d); m != nil {
				st.Tag = m[1]
			}
		}
	}
	return st, nil
}

// Sync brings every environment's health object to what it runs.
func (g *GitOps) Sync() error {
	var pipelines v1alpha1.PipelineList
	if err := g.w.Client.List(g.w.Ctx, &pipelines); err != nil {
		return fmt.Errorf("list pipelines: %w", err)
	}
	for i := range pipelines.Items {
		p := &pipelines.Items[i]
		for _, env := range graph.ExpandedEnvironments(p) {
			st, err := g.Running(p, env)
			if err != nil {
				return fmt.Errorf("pipeline %s/%s env %s: %w", p.Namespace, p.Name, env.Name, err)
			}
			if st.Tag == "" {
				continue
			}
			if err := g.reflect(p, env, st); err != nil {
				return fmt.Errorf("pipeline %s/%s env %s: %w", p.Namespace, p.Name, env.Name, err)
			}
		}
	}
	return nil
}

func (g *GitOps) reflect(p *v1alpha1.Pipeline, env v1alpha1.EnvironmentSpec, st EnvState) error {
	opts := health.OptionsForEnv(p.Name, env)
	failed := g.failing(p.Namespace, env.Name, st.Tag)
	image := AppRepository + ":" + st.Tag
	switch opts.Type {
	case "argocd":
		return g.upsert(ApplicationGVK, opts.ArgoCD.Namespace, opts.ArgoCD.Name, nil, func(o map[string]interface{}) {
			healthStatus := "Healthy"
			if failed {
				healthStatus = "Degraded"
			}
			o["status"] = map[string]interface{}{
				"health":         map[string]interface{}{"status": healthStatus},
				"sync":           map[string]interface{}{"status": "Synced", "revision": st.Revision},
				"operationState": map[string]interface{}{"phase": "Succeeded", "syncResult": map[string]interface{}{"revision": st.Revision}},
				// Argo CD lists the images of the live resources.
				"summary": map[string]interface{}{"images": []interface{}{image}},
			}
		})
	case "flux":
		return g.upsert(KustomizationGVK, opts.Flux.Namespace, opts.Flux.Name, map[string]interface{}{"interval": "1m", "path": "./" + env.Name},
			func(o map[string]interface{}) {
				ready, reason := "True", "ReconciliationSucceeded"
				if failed {
					ready, reason = "False", "HealthCheckFailed"
				}
				rev := st.Branch + "@sha1:" + st.Revision
				o["status"] = map[string]interface{}{
					"observedGeneration": int64(1), "lastAppliedRevision": rev, "lastAttemptedRevision": rev,
					"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": ready, "reason": reason,
						"message": "applied revision " + rev}},
				}
			})
	case "argoRollouts":
		spec := map[string]interface{}{"replicas": int64(1), "template": podSpec(image)}
		return g.upsert(RolloutGVK, opts.ArgoRollouts.Namespace, opts.ArgoRollouts.Name, spec, func(o map[string]interface{}) {
			gen := fmt.Sprint(nestedInt(o, "metadata", "generation"))
			if failed {
				o["status"] = map[string]interface{}{"phase": "Degraded", "observedGeneration": gen, "abort": true,
					"message": "RolloutAborted: the canary failed its analysis", "replicas": int64(1), "availableReplicas": int64(1)}
				return
			}
			o["status"] = map[string]interface{}{"phase": "Healthy", "observedGeneration": gen, "stableRS": "rs" + st.Tag,
				"currentPodHash": "rs" + st.Tag, "replicas": int64(1), "updatedReplicas": int64(1), "availableReplicas": int64(1)}
		})
	case "flagger":
		target := opts.Flagger.Name
		// Flagger's target Deployment runs the new version; once the
		// analysis passes, the primary runs it too.
		if err := g.deployment(opts.Flagger.Namespace, target, image, false); err != nil {
			return err
		}
		if err := g.deployment(opts.Flagger.Namespace, target+"-primary", image, failed); err != nil {
			return err
		}
		spec := map[string]interface{}{"targetRef": map[string]interface{}{"apiVersion": "apps/v1", "kind": "Deployment", "name": target},
			"service": map[string]interface{}{"port": int64(80)}, "analysis": map[string]interface{}{"interval": "10s", "iterations": int64(1)}}
		now := g.w.Now().UTC().Format("2006-01-02T15:04:05Z")
		return g.upsert(CanaryGVK, opts.Flagger.Namespace, opts.Flagger.Name, spec, func(o map[string]interface{}) {
			phase := "Succeeded"
			if failed {
				phase = "Failed"
			}
			old, _ := o["status"].(map[string]interface{})
			if old != nil && old["phase"] == phase && old["image"] == image {
				return
			}
			o["status"] = map[string]interface{}{"phase": phase, "lastTransitionTime": now, "image": image,
				"conditions": []interface{}{map[string]interface{}{"type": "Promoted", "status": "True", "reason": phase,
					"message": "Canary analysis completed", "lastUpdateTime": now, "lastTransitionTime": now}}}
		})
	default:
		return g.deployment(opts.Resource.Namespace, opts.Resource.Name, image, failed)
	}
}

func podSpec(image string) map[string]interface{} {
	return map[string]interface{}{"metadata": map[string]interface{}{"labels": map[string]interface{}{"app": "app"}},
		"spec": map[string]interface{}{"containers": []interface{}{map[string]interface{}{"name": "app", "image": image}}}}
}

func nestedInt(o map[string]interface{}, fields ...string) int64 {
	v, _, _ := unstructured.NestedInt64(o, fields...)
	return v
}

// deployment makes the Deployment ns/name run image: rolled out, or stuck
// past its progress deadline when failed.
func (g *GitOps) deployment(ns, name, image string, failed bool) error {
	ctx := g.w.Ctx
	if err := g.namespace(ns); err != nil {
		return err
	}
	var d appsv1.Deployment
	err := g.w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &d)
	one := int32(1)
	if apierrors.IsNotFound(err) {
		d = appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Generation: 1, Labels: map[string]string{"app": name}},
			Spec: appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}}}}}
		if err := g.w.Client.Create(ctx, &d); err != nil {
			return fmt.Errorf("create deployment: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("get deployment: %w", err)
	}
	if d.Spec.Template.Spec.Containers[0].Image != image {
		d.Spec.Template.Spec.Containers[0].Image = image
		d.Generation++
		if err := g.w.Client.Update(ctx, &d); err != nil {
			return fmt.Errorf("update deployment: %w", err)
		}
	}
	want := appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
		Conditions: []appsv1.DeploymentCondition{
			{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Reason: "MinimumReplicasAvailable"},
			{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable"},
		}}
	if failed {
		want = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, UnavailableReplicas: 1,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, Reason: "MinimumReplicasUnavailable"},
				{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
					Message: "ReplicaSet has timed out progressing."},
			}}
	}
	if reflect.DeepEqual(d.Status, want) {
		return nil
	}
	d.Status = want
	if err := g.w.Client.Status().Update(ctx, &d); err != nil {
		return fmt.Errorf("update deployment status: %w", err)
	}
	return nil
}

func (g *GitOps) namespace(ns string) error {
	err := g.w.Client.Create(g.w.Ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", ns, err)
	}
	return nil
}

// upsert creates gvk ns/name with spec (when not nil) and lets status set
// its status; the generation moves when the spec changes.
func (g *GitOps) upsert(gvk schema.GroupVersionKind, ns, name string, spec map[string]interface{}, status func(o map[string]interface{})) error {
	ctx := g.w.Ctx
	if err := g.namespace(ns); err != nil {
		return err
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	err := g.w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, u)
	if apierrors.IsNotFound(err) {
		u = &unstructured.Unstructured{Object: map[string]interface{}{}}
		u.SetGroupVersionKind(gvk)
		u.SetNamespace(ns)
		u.SetName(name)
		u.SetGeneration(1)
		if spec != nil {
			u.Object["spec"] = spec
		}
		if err := g.w.Client.Create(ctx, u); err != nil {
			return fmt.Errorf("create %s: %w", gvk.Kind, err)
		}
	} else if err != nil {
		return fmt.Errorf("get %s: %w", gvk.Kind, err)
	}
	if spec != nil {
		cur, _ := u.Object["spec"].(map[string]interface{})
		merged := deepMerge(cur, spec)
		if !reflect.DeepEqual(cur, merged) {
			u.Object["spec"] = merged
			u.SetGeneration(u.GetGeneration() + 1)
			if err := g.w.Client.Update(ctx, u); err != nil {
				return fmt.Errorf("update %s: %w", gvk.Kind, err)
			}
		}
	}
	before := u.DeepCopy()
	status(u.Object)
	if reflect.DeepEqual(before.Object["status"], u.Object["status"]) {
		return nil
	}
	if err := g.w.Client.Status().Update(ctx, u); err != nil {
		return fmt.Errorf("update %s status: %w", gvk.Kind, err)
	}
	return nil
}

// runJobs is the kubelet for render Jobs: each Job a RenderRun created runs
// the render (renderjob.Run, the code the render image runs) in process
// against the scenario's git server, and finishes with its Pod's
// termination message.
func (w *World) runJobs() error {
	var jobs batchv1.JobList
	if err := w.Client.List(w.Ctx, &jobs); err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}
	for i := range jobs.Items {
		job := &jobs.Items[i]
		done := false
		for _, c := range job.Status.Conditions {
			if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
				done = true
			}
		}
		owner := metav1.GetControllerOf(job)
		if done || owner == nil || owner.Kind != "RenderRun" {
			continue
		}
		var run v1alpha1.RenderRun
		if err := w.Client.Get(w.Ctx, client.ObjectKey{Namespace: job.Namespace, Name: owner.Name}, &run); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fmt.Errorf("get renderrun: %w", err)
		}
		auth := scm.GitAuth{}
		cfg := renderjob.ConfigFromRun(&run, "kardinal-promoter", "kardinal@kardinal.io")
		dir, err := os.MkdirTemp(w.Dir, "render")
		if err != nil {
			return err
		}
		res, rerr := renderjob.Run(w.Ctx, cfg, dir, auth, w.gitClient)
		if rerr != nil {
			res = renderjob.Result{Error: rerr.Error()}
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: job.Namespace, Name: job.Name + "-x1",
			Labels: map[string]string{"batch.kubernetes.io/controller-uid": string(job.UID), "job-name": job.Name},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID,
				Controller: boolPtr(true)}}}}
		if err := w.Client.Create(w.Ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create render pod: %w", err)
		}
		exit := int32(0)
		if rerr != nil {
			exit = renderjob.ExitPermanent
		}
		pod.Status = corev1.PodStatus{Phase: corev1.PodSucceeded, ContainerStatuses: []corev1.ContainerStatus{{Name: "render",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exit, Message: string(renderjob.Message(res)),
				FinishedAt: metav1.NewTime(w.Now())}}}}}
		if err := w.Client.Status().Update(w.Ctx, pod); err != nil {
			return fmt.Errorf("render pod status: %w", err)
		}
		cond := batchv1.JobComplete
		if rerr != nil {
			cond = batchv1.JobFailed
		}
		job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: cond, Status: corev1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(w.Now())})
		if err := w.Client.Status().Update(w.Ctx, job); err != nil {
			return fmt.Errorf("render job status: %w", err)
		}
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }
