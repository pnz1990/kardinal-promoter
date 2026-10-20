// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// verifiedPipeline is policyPipeline("demo", "test", "prod") whose test
// environment runs the AnalysisTemplate smoke and the ClusterAnalysisTemplate
// org-slo after its health check (spec.verification).
func verifiedPipeline() *v1alpha1.Pipeline {
	p := policyPipeline("demo", "test", "prod")
	p.Spec.Environments[0].Verification = &v1alpha1.VerificationSpec{AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{
		{Name: "smoke"}, {Name: "org-slo", Kind: "ClusterAnalysisTemplate"}}, Timeout: "10m"}
	return p
}

// analysisTemplate is an Argo Rollouts AnalysisTemplate (ns set) or
// ClusterAnalysisTemplate (ns "") with one Job metric.
func analysisTemplate(name, ns string) *unstructured.Unstructured {
	kind := graph.KindAnalysisTemplate
	if ns == "" {
		kind = graph.KindClusterAnalysisTemplate
	}
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{"metrics": []interface{}{map[string]interface{}{
			"name": "ok", "provider": map[string]interface{}{"job": map[string]interface{}{"spec": map[string]interface{}{}}}}}},
	}}
	u.SetAPIVersion(graph.AnalysisRunAPIVersion)
	u.SetKind(kind)
	u.SetName(name)
	u.SetNamespace(ns)
	return u
}

// verifyCluster is a cluster for verification previews: rollouts says
// whether it serves Argo Rollouts' kinds, getErr fails reading the
// templates (RBAC).
func verifyCluster(t *testing.T, rollouts bool, getErr error, objs ...sigs_client.Object) sigs_client.Client {
	t.Helper()
	s := buildPolicyScheme(t)
	mapper := meta.NewDefaultRESTMapper(nil)
	for gvk := range s.AllKnownTypes() {
		mapper.Add(gvk, meta.RESTScopeNamespace)
	}
	if rollouts {
		gv := schema.GroupVersion{Group: "argoproj.io", Version: "v1alpha1"}
		mapper.Add(gv.WithKind("AnalysisRun"), meta.RESTScopeNamespace)
		mapper.Add(gv.WithKind(graph.KindAnalysisTemplate), meta.RESTScopeNamespace)
		mapper.Add(gv.WithKind(graph.KindClusterAnalysisTemplate), meta.RESTScopeRoot)
	}
	b := fake.NewClientBuilder().WithScheme(s).WithRESTMapper(mapper).WithObjects(objs...).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline)
	if getErr != nil {
		b = b.WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c sigs_client.WithWatch, key sigs_client.ObjectKey,
			obj sigs_client.Object, opts ...sigs_client.GetOption) error {
			if u, ok := obj.(*unstructured.Unstructured); ok && u.GetAPIVersion() == graph.AnalysisRunAPIVersion {
				return getErr
			}
			return c.Get(ctx, key, obj, opts...)
		}})
	}
	return b.Build()
}

// #2011: the CLI previews of a Pipeline with verification fail as the
// controller does when they can read the templates, and say when they
// cannot. With both templates there: the gates' result, nothing about
// verification. A template missing, or Argo Rollouts not installed: policy
// simulate is BLOCKED by verification and exits non-zero, policy list says
// verification fails, create bundle --dry-run fails. The templates not
// readable (RBAC): the gates' result, and "verification not checked".
//
// Covers CLI-PREVIEW-VERIFY-01.
func TestPreview_Verification(t *testing.T) {
	t.Parallel()
	for _, tc := range previewVerificationCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := verifyCluster(t, tc.rollouts, tc.getErr, tc.objs()...)

			// policy simulate of prod: the version goes through test first.
			out, err := runSimulate(t, c, simulateOptions{Time: "Tuesday 10am"})
			switch {
			case tc.fails != "":
				require.Error(t, err, out)
				assert.Contains(t, err.Error(), "verification: "+tc.fails)
				assert.Contains(t, out, "RESULT: BLOCKED\nBlocked by: verification\n")
				assert.Contains(t, out, "the controller fails the version")
			default:
				require.NoError(t, err)
				assert.Contains(t, out, "RESULT: BLOCKED\nBlocked by: block-prod\n", "the gates' result")
				assert.NotContains(t, out, "Blocked by: verification")
			}
			if tc.notChecked != "" {
				assert.Contains(t, out, "Verification not checked: "+tc.notChecked)
			} else {
				assert.NotContains(t, out, "Verification not checked")
			}
			// The environment without verification is not affected.
			_, err = runSimulate(t, c, simulateOptions{Env: "test", Time: "Tuesday 10am"})
			if tc.fails != "" {
				require.Error(t, err, "test is the verified environment")
			} else {
				require.NoError(t, err)
			}

			var list bytes.Buffer
			require.NoError(t, policyListFn(t.Context(), &list, c, "default", "demo", nil))
			assert.Contains(t, list.String(), "block-prod")
			if tc.fails != "" {
				assert.Contains(t, list.String(), "Verification fails: "+tc.fails)
			}
			if tc.notChecked != "" {
				assert.Contains(t, list.String(), "Verification not checked: "+tc.notChecked)
			}
		})
	}
}

// TestPreview_VerificationDryRun: create bundle --dry-run of the cases of
// TestPreview_Verification fails as the controller does, or names the
// analyses it builds and counts. It runs the command's code, which uses the
// output style, so it is not parallel.
//
// Covers CLI-PREVIEW-VERIFY-01.
func TestPreview_VerificationDryRun(t *testing.T) {
	keepStyle(t)
	for _, tc := range previewVerificationCases() {
		t.Run(tc.name, func(t *testing.T) {
			c := verifyCluster(t, tc.rollouts, tc.getErr, tc.objs()...)
			var dry bytes.Buffer
			err := createBundleDryRun(&dry, c, "default", "demo",
				createBundleOptions{Images: []string{"ghcr.io/org/app:1.0.0"}, Type: "image"})
			if tc.fails != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "dry-run: verification: "+tc.fails)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, dry.String(), "test (verification: smoke, cluster/org-slo)")
			if tc.notChecked != "" {
				assert.Contains(t, dry.String(), "Verification not checked: "+tc.notChecked)
				assert.Contains(t, dry.String(), "The node count leaves out verification")
			} else {
				assert.NotContains(t, dry.String(), "leaves out verification", "the AnalysisRuns are built and counted")
			}
		})
	}
}

// previewVerificationCase is one case of the verification previews.
type previewVerificationCase struct {
	name       string
	rollouts   bool
	getErr     error
	templates  []sigs_client.Object
	fails      string // "" when it does not
	notChecked string
}

// objs are the case's cluster: the verified Pipeline, a blocking prod gate
// and the case's templates.
// The templates are copied: the fake client writes to the objects it is
// built from, and the cases run in parallel.
func (tc previewVerificationCase) objs() []sigs_client.Object {
	out := []sigs_client.Object{verifiedPipeline(), policyGate("block-prod", "default", "prod", "false")}
	for _, o := range tc.templates {
		out = append(out, o.DeepCopyObject().(sigs_client.Object))
	}
	return out
}

func previewVerificationCases() []previewVerificationCase {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "argoproj.io", Resource: "analysistemplates"}, "smoke",
		assert.AnError)
	both := []sigs_client.Object{analysisTemplate("smoke", "default"), analysisTemplate("org-slo", "")}
	return []previewVerificationCase{
		{name: "the templates are there", rollouts: true, templates: both},
		{name: "a template is missing", rollouts: true, templates: both[1:],
			fails: `environment "test" verification: AnalysisTemplate "smoke" not found in namespace "default"`},
		{name: "a ClusterAnalysisTemplate is missing", rollouts: true, templates: both[:1],
			fails: `environment "test" verification: ClusterAnalysisTemplate "org-slo" not found`},
		{name: "Argo Rollouts is not installed", templates: both,
			fails: `environment "test" verification: argoproj.io/v1alpha1 AnalysisRun is not served by the cluster: install Argo Rollouts`},
		{name: "the templates cannot be read", rollouts: true, getErr: forbidden, templates: both,
			notChecked: "the AnalysisTemplates could not be read: get AnalysisTemplate default/smoke"},
	}
}

// The dry run's -o json names the analyses and, when it could not read
// them, why.
func TestPreview_DryRunJSON(t *testing.T) {
	keepStyle(t)
	setForTest(t, &globalOutput, "json")
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "argoproj.io", Resource: "analysistemplates"}, "smoke", assert.AnError)
	c := verifyCluster(t, true, forbidden, verifiedPipeline())
	var out bytes.Buffer
	require.NoError(t, createBundleDryRun(&out, c, "default", "demo",
		createBundleOptions{Images: []string{"ghcr.io/org/app:1.0.0"}, Type: "image"}))
	var res dryRunResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &res), out.String())
	require.Len(t, res.Environments, 2)
	assert.Equal(t, []string{"smoke", "cluster/org-slo"}, res.Environments[0].Analyses)
	assert.Empty(t, res.Environments[1].Analyses)
	assert.Contains(t, res.VerificationNotChecked, "could not be read")
}

// validate is offline: it previews verification without reading the
// templates, and still checks verification.timeout.
func TestPreview_ValidateIsOffline(t *testing.T) {
	keepStyle(t)
	doc := `apiVersion: kardinal.io/v1alpha1
kind: Pipeline
metadata:
  name: demo
spec:
  git:
    url: https://github.com/o/r
  environments:
  - name: test
    verification:
      analysisTemplates: [{name: smoke}]
      timeout: 10m
  - name: prod
    dependsOn: [test]
`
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("p.yaml", []byte(doc), 0o600))
	out, err := executeRoot(t, "validate", "-f", "p.yaml")
	require.NoError(t, err, out)
	assert.Contains(t, out, "p.yaml is valid")

	bad := bytes.Replace([]byte(doc), []byte("timeout: 10m"), []byte("timeout: soon"), 1)
	require.NoError(t, os.WriteFile("p.yaml", bad, 0o600))
	out, err = executeRoot(t, "validate", "-f", "p.yaml")
	require.Error(t, err)
	assert.Contains(t, out, `build: environment "test" verification:`)
}

// previewPipeline leaves the caller's Pipeline as it was.
func TestPreviewPipeline_CopiesThePipeline(t *testing.T) {
	p := verifiedPipeline()
	preview, analyses, err := previewPipeline(p)
	require.NoError(t, err)
	assert.Nil(t, preview.Spec.Environments[0].Verification)
	assert.NotNil(t, p.Spec.Environments[0].Verification, "the original keeps its verification")
	assert.Equal(t, map[string][]string{"test": {"smoke", "cluster/org-slo"}}, analyses)
}

// QA-3 on #2171: a template missing for an environment after the one
// simulated still fails every version, so simulate of the earlier
// environment is BLOCKED by verification too.
func TestPreview_VerificationOfTheWholePipeline(t *testing.T) {
	p := policyPipeline("demo", "test", "prod")
	p.Spec.Environments[1].Verification = &v1alpha1.VerificationSpec{AnalysisTemplates: []v1alpha1.AnalysisTemplateRef{{Name: "smoke"}}}
	c := verifyCluster(t, true, nil, p)
	out, err := runSimulate(t, c, simulateOptions{Env: "test", Time: "Tuesday 10am"})
	require.Error(t, err, out)
	assert.Contains(t, out, "RESULT: BLOCKED\nBlocked by: verification\n")
	assert.Contains(t, err.Error(), `environment "prod" verification: AnalysisTemplate "smoke" not found`)
}

// erringMapper is a RESTMapper whose discovery of Argo Rollouts fails with
// err (a timeout, a forbidden discovery request), not a "no match".
type erringMapper struct {
	meta.RESTMapper
	err error
}

func (m erringMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if gk.Group == "argoproj.io" {
		return nil, m.err
	}
	return m.RESTMapper.RESTMapping(gk, versions...)
}

// QA-3 on #2171: only a "no match" from discovery is Argo Rollouts not
// installed. A discovery error of another kind is "Verification not
// checked", with the gates' result.
func TestPreview_DiscoveryErrorIsNotChecked(t *testing.T) {
	t.Parallel()
	base := verifyCluster(t, true, nil)
	s := base.Scheme()
	c := fake.NewClientBuilder().WithScheme(s).WithRESTMapper(erringMapper{RESTMapper: base.RESTMapper(), err: context.DeadlineExceeded}).
		WithObjects(verifiedPipeline(), policyGate("block-prod", "default", "prod", "false")).
		WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	out, err := runSimulate(t, c, simulateOptions{Time: "Tuesday 10am"})
	require.NoError(t, err, out)
	assert.Contains(t, out, "Blocked by: block-prod")
	assert.NotContains(t, out, "Blocked by: verification")
	assert.Contains(t, out, "Verification not checked: the AnalysisTemplates could not be read: discover argoproj.io/v1alpha1 AnalysisRun")
}
