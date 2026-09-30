// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"
)

// diagnosedGroups are the API groups whose objects Diagnose dumps.
var diagnosedGroups = []string{"kardinal.io", "kro.run"}

// logNamespaces are the namespaces whose pod logs Diagnose saves.
var logNamespaces = []string{ControllerNamespace, "kro-system"}

// Diagnose writes the state a failed test needs for debugging to
// $KARDINAL_E2E_ARTIFACTS/<ns>/: every kardinal.io and kro.run object in
// ns, the namespace's events, and the controller and kro logs.
func (e *Env) Diagnose(t *testing.T, ns string) {
	t.Helper()
	dir := filepath.Join(artifactsDir(), ns)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("diagnostics: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	for _, gvr := range e.namespacedResources(t, diagnosedGroups) {
		list, err := e.Dynamic.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil || len(list.Items) == 0 {
			continue
		}
		items := make([]interface{}, 0, len(list.Items))
		for _, it := range list.Items {
			items = append(items, it.Object)
		}
		out, err := yaml.Marshal(items)
		if err != nil {
			continue
		}
		write(t, dir, gvr.Resource+"."+gvr.Group+".yaml", out)
	}

	if evs, err := e.Kube.CoreV1().Events(ns).List(ctx, metav1.ListOptions{}); err == nil {
		var b strings.Builder
		for _, ev := range evs.Items {
			fmt.Fprintf(&b, "%s %s %s/%s %s: %s\n", ev.LastTimestamp.UTC().Format(time.RFC3339),
				ev.Type, ev.InvolvedObject.Kind, ev.InvolvedObject.Name, ev.Reason, ev.Message)
		}
		write(t, dir, "events.txt", []byte(b.String()))
	}

	for _, lns := range logNamespaces {
		pods, err := e.Kube.CoreV1().Pods(lns).List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		for _, p := range pods.Items {
			tail := int64(3000)
			raw, err := e.Kube.CoreV1().Pods(lns).GetLogs(p.Name, &corev1.PodLogOptions{TailLines: &tail}).DoRaw(ctx)
			if err != nil {
				continue
			}
			write(t, dir, "logs-"+lns+"-"+p.Name+".txt", raw)
		}
	}
	t.Logf("diagnostics written to %s", dir)
}

// namespacedResources lists the namespaced, listable resources of groups.
func (e *Env) namespacedResources(t *testing.T, groups []string) []schema.GroupVersionResource {
	t.Helper()
	var out []schema.GroupVersionResource
	lists, err := e.Kube.Discovery().ServerPreferredNamespacedResources()
	if err != nil && len(lists) == 0 {
		t.Logf("diagnostics: discovery: %v", err)
		return nil
	}
	for _, l := range lists {
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil || !contains(groups, gv.Group) {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") || !contains(r.Verbs, "list") {
				continue
			}
			out = append(out, gv.WithResource(r.Name))
		}
	}
	return out
}

func artifactsDir() string {
	if d := os.Getenv(EnvArtifacts); d != "" {
		return d
	}
	return filepath.Join("results")
}

func write(t *testing.T, dir, name string, data []byte) {
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Logf("diagnostics: write %s: %v", name, err)
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
