//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestInstallOnAPIServer installs every CRD in config/crd/bases and every
// ValidatingAdmissionPolicy and binding the chart renders on a real
// kube-apiserver (envtest), the one KUBEBUILDER_ASSETS names. CI runs it on
// the oldest Kubernetes the chart supports and on the newest
// (ENVTEST_K8S_MIN and ENVTEST_K8S_MAX in hack/tool-versions.env), so a
// schema or CEL rule only newer API servers accept never ships (#1835). The
// API server compiles each policy's CEL when it is created (the
// type-checking warnings come from kube-controller-manager, which envtest
// does not run). A few writes then check the fleet rules end to end: the
// CRD's maxUnavailable rule and the fleets policy.
//
// Covers FLEET-16.
func TestInstallOnAPIServer(t *testing.T) {
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"), "KUBEBUILDER_ASSETS names the envtest binaries (make envtest-install)")
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(repoRoot(t), "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err, "the API server installs every CRD of config/crd/bases")
	t.Cleanup(func() { _ = env.Stop() })
	dyn, err := dynamic.NewForConfig(cfg)
	require.NoError(t, err)
	ctx := context.Background()

	vapGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicies"}
	bindingGVR := schema.GroupVersionResource{Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicybindings"}
	var policies []string
	for _, d := range render(t, "kardinal-promoter") {
		gvr := vapGVR
		switch d.Kind {
		case "ValidatingAdmissionPolicy":
			policies = append(policies, d.Name)
		case "ValidatingAdmissionPolicyBinding":
			gvr = bindingGVR
		default:
			continue
		}
		var obj unstructured.Unstructured
		require.NoError(t, json.Unmarshal(d.raw, &obj.Object))
		_, err := dyn.Resource(gvr).Create(ctx, &obj, metav1.CreateOptions{})
		require.NoError(t, err, "the API server accepts %s %s", d.Kind, d.Name)
	}
	require.NotEmpty(t, policies)

	// The fleet rules end to end.
	_, err = dyn.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Create(ctx,
		&unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "Namespace",
			"metadata": map[string]interface{}{"name": "team-a"}}}, metav1.CreateOptions{})
	require.NoError(t, err)
	pipelines := dyn.Resource(schema.GroupVersionResource{Group: "kardinal.io", Version: "v1alpha1", Resource: "pipelines"}).Namespace("team-a")
	pipeline := func(name string, maxUnavailable interface{}, paused ...string) *unstructured.Unstructured {
		fleet := map[string]interface{}{"targets": []interface{}{map[string]interface{}{"name": "eu"}}}
		if maxUnavailable != nil {
			fleet["maxUnavailable"] = maxUnavailable
		}
		spec := map[string]interface{}{
			"git":          map[string]interface{}{"url": "https://git.example/app.git"},
			"environments": []interface{}{map[string]interface{}{"name": "test"}, map[string]interface{}{"name": "prod", "fleet": fleet}},
		}
		if len(paused) > 0 {
			ps := make([]interface{}, len(paused))
			for i, p := range paused {
				ps[i] = p
			}
			spec["pausedFleets"] = ps
		}
		return &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "kardinal.io/v1alpha1", "kind": "Pipeline",
			"metadata": map[string]interface{}{"name": name}, "spec": spec}}
	}
	// The policies apply once the API server has loaded them: retry the
	// first refusal for a while.
	refused := func(p *unstructured.Unstructured, want string) {
		t.Helper()
		var last error
		require.Eventually(t, func() bool {
			_, last = pipelines.Create(ctx, p, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
			return last != nil && strings.Contains(last.Error(), want)
		}, 30*time.Second, 500*time.Millisecond, "%s is refused with %q; last: %v", p.GetName(), want, last)
	}
	for i, v := range []interface{}{int64(1), int64(50), "1%", "10%", "100%", nil} {
		_, err := pipelines.Create(ctx, pipeline(fmt.Sprintf("ok-%d", i), v), metav1.CreateOptions{})
		assert.NoError(t, err, "maxUnavailable %v", v)
	}
	for i, v := range []interface{}{"0%", "101%", "ten", "2", "5.5%"} {
		_, err := pipelines.Create(ctx, pipeline(fmt.Sprintf("bad-%d", i), v), metav1.CreateOptions{})
		assert.Error(t, err, "maxUnavailable %v is refused by the schema", v)
	}
	refused(pipeline("zero", int64(0)), "fleet.maxUnavailable: a count of at least 1")
	refused(pipeline("stale", nil, "prod", "test"), "spec.pausedFleets names test")
	_, err = pipelines.Create(ctx, pipeline("paused", nil, "prod"), metav1.CreateOptions{})
	assert.NoError(t, err, "a paused fleet")
}
