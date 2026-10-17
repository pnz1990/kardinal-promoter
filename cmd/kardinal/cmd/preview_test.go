// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
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

// #2011: the CLI previews a Pipeline with verification without reading its
// AnalysisTemplates. Before, policy simulate, policy list, validate and
// create bundle --dry-run failed with `AnalysisTemplate "smoke" not found`
// although the controller promotes it.
//
// Covers CLI-PREVIEW-VERIFY-01.
func TestPreview_VerificationNeedsNoTemplates(t *testing.T) {
	t.Run("policy simulate", func(t *testing.T) {
		t.Parallel()
		c := policyClient(t, verifiedPipeline(), policyGate("block-prod", "default", "prod", "false"))
		out, err := runSimulate(t, c, simulateOptions{Time: "Tuesday 10am"})
		require.NoError(t, err)
		assert.Contains(t, out, "RESULT: BLOCKED")
		assert.Contains(t, out, "Blocked by: block-prod")
	})
	t.Run("policy simulate of the verified environment", func(t *testing.T) {
		t.Parallel()
		c := policyClient(t, verifiedPipeline(), policyGate("test-gate", "default", "test", "true"))
		out, err := runSimulate(t, c, simulateOptions{Env: "test", Time: "Tuesday 10am"})
		require.NoError(t, err)
		assert.Contains(t, out, "RESULT: PASS")
	})
	t.Run("policy list --pipeline", func(t *testing.T) {
		t.Parallel()
		c := policyClient(t, verifiedPipeline(), policyGate("block-prod", "default", "prod", "false"))
		var out bytes.Buffer
		require.NoError(t, policyListFn(&out, c, "default", "demo", nil))
		assert.Contains(t, out.String(), "block-prod")
	})
	t.Run("create bundle --dry-run", func(t *testing.T) {
		keepStyle(t)
		c := fake.NewClientBuilder().WithScheme(buildCreateBundleScheme(t)).WithObjects(verifiedPipeline()).Build()
		var out bytes.Buffer
		require.NoError(t, createBundleDryRun(&out, c, "default", "demo",
			createBundleOptions{Images: []string{"ghcr.io/org/app:1.0.0"}, Type: "image"}))
		assert.Contains(t, out.String(), "test (verification: smoke, cluster/org-slo)")
		assert.Contains(t, out.String(), "The node count leaves out verification")

		setForTest(t, &globalOutput, "json")
		out.Reset()
		require.NoError(t, createBundleDryRun(&out, c, "default", "demo",
			createBundleOptions{Images: []string{"ghcr.io/org/app:1.0.0"}, Type: "image"}))
		var res dryRunResult
		require.NoError(t, json.Unmarshal(out.Bytes(), &res), out.String())
		require.Len(t, res.Environments, 2)
		assert.Equal(t, []string{"smoke", "cluster/org-slo"}, res.Environments[0].Analyses)
		assert.Empty(t, res.Environments[1].Analyses)
	})
	t.Run("validate", func(t *testing.T) {
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

		// What the builder checks without the templates is still checked.
		bad := bytes.Replace([]byte(doc), []byte("timeout: 10m"), []byte("timeout: soon"), 1)
		require.NoError(t, os.WriteFile("p.yaml", bad, 0o600))
		out, err = executeRoot(t, "validate", "-f", "p.yaml")
		require.Error(t, err)
		assert.Contains(t, out, `build: environment "test" verification:`)
	})
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
