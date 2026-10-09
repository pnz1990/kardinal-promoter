// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

func rolloutsMapper(served bool) meta.RESTMapper {
	m := meta.NewDefaultRESTMapper(nil)
	if served {
		for _, k := range []string{"AnalysisRun", "AnalysisTemplate"} {
			m.Add(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: k}, meta.RESTScopeNamespace)
		}
		m.Add(schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "ClusterAnalysisTemplate"}, meta.RESTScopeRoot)
	}
	return m
}

func analysisTemplateObj(kind, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1", "kind": kind,
		"metadata": map[string]interface{}{"name": name},
		"spec": map[string]interface{}{"metrics": []interface{}{
			map[string]interface{}{"name": "m", "provider": map[string]interface{}{"web": map[string]interface{}{"url": "http://x"}}},
		}},
	}}
	if ns != "" {
		u.SetNamespace(ns)
	}
	return u
}

func verifyingPipeline(refs ...kardinalv1alpha1.AnalysisTemplateRef) *kardinalv1alpha1.Pipeline {
	p := makePipeline("app", []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}, {Name: "prod"}})
	p.Namespace = "team"
	p.Spec.Environments[1].Verification = &kardinalv1alpha1.VerificationSpec{AnalysisTemplates: refs}
	return p
}

// TestCollectAnalyses reads the namespaced templates from the Pipeline
// namespace and the cluster ones cluster-wide, leaves missing ones out, and
// says when Argo Rollouts is not served (verification fails closed).
func TestCollectAnalyses(t *testing.T) {
	scheme := runtime.NewScheme()
	objs := []client.Object{
		analysisTemplateObj("AnalysisTemplate", "team", "smoke"),
		analysisTemplateObj("AnalysisTemplate", "other", "elsewhere"),
		analysisTemplateObj("ClusterAnalysisTemplate", "", "slo"),
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	p := verifyingPipeline(
		kardinalv1alpha1.AnalysisTemplateRef{Name: "smoke"},
		kardinalv1alpha1.AnalysisTemplateRef{Name: "slo", Kind: "ClusterAnalysisTemplate"},
		kardinalv1alpha1.AnalysisTemplateRef{Name: "elsewhere"},
	)

	tr := New(nil, nil, c, nil, zerolog.Nop()).WithRESTMapper(rolloutsMapper(true))
	in, err := tr.collectAnalyses(context.Background(), p)
	require.NoError(t, err)
	assert.Empty(t, in.Unavailable)
	assert.Contains(t, in.Templates, graph.AnalysisTemplateKey("AnalysisTemplate", "smoke"))
	assert.Contains(t, in.Templates, graph.AnalysisTemplateKey("ClusterAnalysisTemplate", "slo"))
	assert.NotContains(t, in.Templates, graph.AnalysisTemplateKey("AnalysisTemplate", "elsewhere"),
		"a namespaced template is read only from the Pipeline namespace")
	assert.NotEmpty(t, in.Templates[graph.AnalysisTemplateKey("AnalysisTemplate", "smoke")].Spec["metrics"])

	tr = New(nil, nil, c, nil, zerolog.Nop()).WithRESTMapper(rolloutsMapper(false))
	in, err = tr.collectAnalyses(context.Background(), p)
	require.NoError(t, err)
	assert.Contains(t, in.Unavailable, "AnalysisRun is not served")

	// No verification: nothing read, nothing unavailable.
	in, err = tr.collectAnalyses(context.Background(), makePipeline("app", []kardinalv1alpha1.EnvironmentSpec{{Name: "test"}}))
	require.NoError(t, err)
	assert.Empty(t, in.Unavailable)
	assert.Empty(t, in.Templates)
}

// TestCollectAnalyses_APIErrorIsRetried: an error other than NotFound is
// returned, so the translation is retried rather than failing the Bundle.
func TestCollectAnalyses_APIErrorIsRetried(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("connection refused")
		},
	}).Build()
	tr := New(nil, nil, c, nil, zerolog.Nop()).WithRESTMapper(rolloutsMapper(true))
	_, err := tr.collectAnalyses(context.Background(), verifyingPipeline(kardinalv1alpha1.AnalysisTemplateRef{Name: "smoke"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
}
