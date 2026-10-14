// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"fmt"
	"path"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// GitSecret is the git Secret every scenario Pipeline names.
const GitSecret = "git-token"

// TenantObjects returns a tenant namespace and its git Secret.
func TenantObjects(ns string) []client.Object {
	return []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: GitSecret,
			Labels: map[string]string{v1alpha1.LabelSecretReferenceable: "true"}},
			Data: map[string][]byte{"token": []byte("git-token-" + ns)}},
	}
}

// deploymentYAML is the application manifest every environment starts with.
func deploymentYAML(name, tag string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
    spec:
      containers:
        - name: app
          image: %s:%s
`, name, name, name, AppRepository, tag)
}

// RepoFiles returns the files of a GitOps repository holding envs of
// pipeline p at tag: each environment's directory in the layout its update
// strategy edits.
func RepoFiles(p *v1alpha1.Pipeline, tag string) map[string]string {
	files := map[string]string{"README.md": "# " + p.Name + "\n"}
	for _, env := range graph.ExpandedEnvironments(p) {
		dir := env.Path
		if dir == "" {
			dir = "environments/" + env.Name
		}
		switch env.Update.Strategy {
		case "argocd":
			continue
		case "helm":
			file := "values.yaml"
			if env.Update.Helm != nil && env.Update.Helm.ValuesFile != "" {
				file = env.Update.Helm.ValuesFile
			}
			files[path.Join(dir, file)] = fmt.Sprintf("replicaCount: 1\nimage:\n  repository: %s\n  tag: %q\n", AppRepository, tag)
		case "yaml":
			files[path.Join(dir, "deployment.yaml")] = deploymentYAML(p.Name, tag)
		default:
			files[path.Join(dir, "kustomization.yaml")] = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - deployment.yaml\n"
			files[path.Join(dir, "deployment.yaml")] = deploymentYAML(p.Name, tag)
		}
	}
	return files
}

// ArgoApplication is the Application the argocd strategy patches, at tag.
func ArgoApplication(ns, name, tag string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"project": "default",
			"source": map[string]interface{}{"repoURL": "https://charts.example.com", "chart": "app", "targetRevision": "1.0.0",
				"helm": map[string]interface{}{"valuesObject": map[string]interface{}{"image": map[string]interface{}{
					"repository": AppRepository, "tag": tag}}}},
			"destination": map[string]interface{}{"server": "https://kubernetes.default.svc", "namespace": name},
		},
	}}
	u.SetGroupVersionKind(ApplicationGVK)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

// ImageBundle is a Bundle of AppRepository:tag for pipeline.
func ImageBundle(ns, pipeline, tag string) *v1alpha1.Bundle {
	return &v1alpha1.Bundle{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: pipeline + "-" + strings.ReplaceAll(tag, ".", "-")},
		Spec: v1alpha1.BundleSpec{Type: "image", Pipeline: pipeline,
			Images:     []v1alpha1.ImageRef{{Repository: AppRepository, Tag: tag}},
			Provenance: &v1alpha1.BundleProvenance{Author: "ci", CommitSHA: "0123456789abcdef0123456789abcdef01234567"}},
	}
}

// BundlePhase returns the phase of every Bundle of ns, by name.
func (w *World) BundlePhase(ns string) map[string]string {
	var list v1alpha1.BundleList
	if err := w.Client.List(w.Ctx, &list, client.InNamespace(ns)); err != nil {
		w.T.Fatal(err)
	}
	out := map[string]string{}
	for _, b := range list.Items {
		out[b.Name] = b.Status.Phase
	}
	return out
}

// Bundle reads a Bundle.
func (w *World) Bundle(ns, name string) *v1alpha1.Bundle {
	var b v1alpha1.Bundle
	if err := w.Client.Get(w.Ctx, client.ObjectKey{Namespace: ns, Name: name}, &b); err != nil {
		w.T.Fatalf("get bundle %s/%s: %v", ns, name, err)
	}
	return &b
}

// Steps returns the PromotionSteps of a Bundle by environment.
func (w *World) Steps(ns, bundle string) map[string]v1alpha1.PromotionStep {
	var list v1alpha1.PromotionStepList
	if err := w.Client.List(w.Ctx, &list, client.InNamespace(ns), client.MatchingLabels{"kardinal.io/bundle": bundle}); err != nil {
		w.T.Fatal(err)
	}
	out := map[string]v1alpha1.PromotionStep{}
	for _, s := range list.Items {
		out[s.Spec.Environment] = s
	}
	return out
}

// Describe summarizes ns for a failure message: Bundles, steps, gates and
// Graph conditions.
func (w *World) Describe(ns string) string {
	var b strings.Builder
	var bundles v1alpha1.BundleList
	_ = w.Client.List(w.Ctx, &bundles, client.InNamespace(ns))
	for _, bu := range bundles.Items {
		fmt.Fprintf(&b, "bundle %s phase=%s graph=%s\n", bu.Name, bu.Status.Phase, bu.Status.GraphRef)
		for _, c := range bu.Status.Conditions {
			fmt.Fprintf(&b, "  cond %s=%s %s: %s\n", c.Type, c.Status, c.Reason, c.Message)
		}
	}
	var steps v1alpha1.PromotionStepList
	_ = w.Client.List(w.Ctx, &steps, client.InNamespace(ns))
	sort.Slice(steps.Items, func(i, j int) bool { return steps.Items[i].Name < steps.Items[j].Name })
	for _, s := range steps.Items {
		fmt.Fprintf(&b, "step %s env=%s state=%s step=%d msg=%q pr=%s\n", s.Name, s.Spec.Environment, s.Status.State,
			s.Status.CurrentStepIndex, s.Status.Message, s.Status.PRURL)
	}
	var gates v1alpha1.PolicyGateList
	_ = w.Client.List(w.Ctx, &gates, client.InNamespace(ns))
	for _, g := range gates.Items {
		if g.Labels["kardinal.io/bundle"] == "" {
			continue
		}
		fmt.Fprintf(&b, "gate %s ready=%v reason=%q\n", g.Name, g.Status.Ready, g.Status.Reason)
	}
	graphs := &unstructured.UnstructuredList{}
	graphs.SetGroupVersionKind(GraphGVK.GroupVersion().WithKind("GraphList"))
	_ = w.Client.List(w.Ctx, graphs, client.InNamespace(ns))
	for _, g := range graphs.Items {
		conds, _, _ := unstructured.NestedSlice(g.Object, "status", "conditions")
		for _, c := range conds {
			m, _ := c.(map[string]interface{})
			if m["type"] == "Ready" {
				fmt.Fprintf(&b, "graph %s Ready=%v %v: %v\n", g.GetName(), m["status"], m["reason"], m["message"])
			}
		}
	}
	for _, e := range w.Kro.Errors() {
		fmt.Fprintf(&b, "kro error: %s\n", e)
	}
	n, last := w.Errors()
	for k, v := range n {
		fmt.Fprintf(&b, "reconcile errors %s=%d last=%s\n", k, v, last[k])
	}
	for _, u := range w.SCM.Unknown() {
		fmt.Fprintf(&b, "scm unknown request: %s\n", u)
	}
	return b.String()
}
