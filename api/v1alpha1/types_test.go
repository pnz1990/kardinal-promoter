// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package v1alpha1_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// Pipeline roundtrip
// ---------------------------------------------------------------------------

// TestPipelineRoundtrip verifies that a fully-populated Pipeline serializes
// and deserializes correctly via JSON (the format used by the Kubernetes API
// server).
func TestPipelineRoundtrip(t *testing.T) {
	original := &v1alpha1.Pipeline{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "kardinal.io/v1alpha1",
			Kind:       "Pipeline",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo",
			Namespace: "default",
		},
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{
					Name:             "test",
					GitRepo:          "https://github.com/myorg/gitops.git",
					Branch:           "main",
					Path:             "apps/nginx/overlays/test",
					ApprovalMode:     "auto",
					UpdateStrategy:   "kustomize",
					HealthAdapter:    "deployment",
					HealthTimeout:    "30m",
					DeliveryDelegate: "",
					DependsOn:        []string{},
					GitCredentials: &v1alpha1.GitCredentials{
						SecretName:      "gitops-creds",
						SecretNamespace: "default",
					},
				},
				{Name: "uat", ApprovalMode: "auto", UpdateStrategy: "kustomize"},
				{
					Name:             "prod",
					ApprovalMode:     "pr-review",
					UpdateStrategy:   "kustomize",
					HealthAdapter:    "argocd",
					HealthTimeout:    "60m",
					DeliveryDelegate: "argoRollouts",
					DependsOn:        []string{"uat"},
				},
			},
			PolicyGates: []v1alpha1.PipelinePolicyGateRef{
				{Name: "no-weekend-deploys", Namespace: "platform-policies"},
			},
			Paused: false,
			Shard:  "us-east",
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err, "marshal Pipeline")

	var got v1alpha1.Pipeline
	require.NoError(t, json.Unmarshal(data, &got), "unmarshal Pipeline")

	assert.Equal(t, original.Name, got.Name)
	assert.Len(t, got.Spec.Environments, 3)

	e0 := got.Spec.Environments[0]
	assert.Equal(t, "test", e0.Name)
	assert.Equal(t, "https://github.com/myorg/gitops.git", e0.GitRepo)
	assert.Equal(t, "main", e0.Branch)
	assert.Equal(t, "apps/nginx/overlays/test", e0.Path)
	assert.Equal(t, "auto", e0.ApprovalMode)
	assert.Equal(t, "kustomize", e0.UpdateStrategy)
	assert.Equal(t, "deployment", e0.HealthAdapter)
	assert.Equal(t, "30m", e0.HealthTimeout)
	require.NotNil(t, e0.GitCredentials)
	assert.Equal(t, "gitops-creds", e0.GitCredentials.SecretName)
	assert.Equal(t, "default", e0.GitCredentials.SecretNamespace)

	e2 := got.Spec.Environments[2]
	assert.Equal(t, "prod", e2.Name)
	assert.Equal(t, "pr-review", e2.ApprovalMode)
	assert.Equal(t, "argocd", e2.HealthAdapter)
	assert.Equal(t, "60m", e2.HealthTimeout)
	assert.Equal(t, "argoRollouts", e2.DeliveryDelegate)
	assert.Equal(t, []string{"uat"}, e2.DependsOn)

	require.Len(t, got.Spec.PolicyGates, 1)
	assert.Equal(t, "no-weekend-deploys", got.Spec.PolicyGates[0].Name)
	assert.Equal(t, "platform-policies", got.Spec.PolicyGates[0].Namespace)

	assert.False(t, got.Spec.Paused)
	assert.Equal(t, "us-east", got.Spec.Shard)
}

// ---------------------------------------------------------------------------
// Bundle roundtrip
// ---------------------------------------------------------------------------

// TestBundleRoundtrip verifies that a fully-populated Bundle serializes and
// deserializes correctly.
func TestBundleRoundtrip(t *testing.T) {
	ts := metav1.Now()
	original := &v1alpha1.Bundle{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "kardinal.io/v1alpha1",
			Kind:       "Bundle",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-v1",
			Namespace: "default",
		},
		Spec: v1alpha1.BundleSpec{
			Type:     "image",
			Pipeline: "nginx-demo",
			Images: []v1alpha1.ImageRef{
				{
					Repository: "ghcr.io/nginx/nginx",
					Tag:        "1.29.0",
					Digest:     "sha256:abc123def456",
				},
			},
			ConfigRef: &v1alpha1.ConfigRef{
				GitRepo:   "https://github.com/myorg/gitops.git",
				CommitSHA: "abc123",
			},
			Provenance: &v1alpha1.BundleProvenance{
				CommitSHA:  "def456",
				CIRunURL:   "https://github.com/myorg/myapp/actions/runs/123",
				Author:     "alice",
				Timestamp:  ts,
				RollbackOf: "",
			},
			Intent: &v1alpha1.BundleIntent{
				TargetEnvironment: "prod",
				SkipEnvironments:  []string{"uat"},
			},
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err, "marshal Bundle")

	var got v1alpha1.Bundle
	require.NoError(t, json.Unmarshal(data, &got), "unmarshal Bundle")

	assert.Equal(t, "nginx-demo", got.Spec.Pipeline)
	assert.Equal(t, "image", got.Spec.Type)
	require.Len(t, got.Spec.Images, 1)
	assert.Equal(t, "ghcr.io/nginx/nginx", got.Spec.Images[0].Repository)
	assert.Equal(t, "1.29.0", got.Spec.Images[0].Tag)
	assert.Equal(t, "sha256:abc123def456", got.Spec.Images[0].Digest)

	require.NotNil(t, got.Spec.ConfigRef)
	assert.Equal(t, "https://github.com/myorg/gitops.git", got.Spec.ConfigRef.GitRepo)
	assert.Equal(t, "abc123", got.Spec.ConfigRef.CommitSHA)

	require.NotNil(t, got.Spec.Provenance)
	assert.Equal(t, "def456", got.Spec.Provenance.CommitSHA)
	assert.Equal(t, "https://github.com/myorg/myapp/actions/runs/123", got.Spec.Provenance.CIRunURL)
	assert.Equal(t, "alice", got.Spec.Provenance.Author)

	require.NotNil(t, got.Spec.Intent)
	assert.Equal(t, "prod", got.Spec.Intent.TargetEnvironment)
	assert.Equal(t, []string{"uat"}, got.Spec.Intent.SkipEnvironments)
}

// TestBundleStatusEnvironments verifies Bundle status per-environment evidence.
func TestBundleStatusEnvironments(t *testing.T) {
	now := metav1.Now()
	b := &v1alpha1.Bundle{}
	b.Status = v1alpha1.BundleStatus{
		Phase: "Verified",
		Environments: []v1alpha1.EnvironmentStatus{
			{
				Name:  "prod",
				Phase: "Verified",
				PRURL: "https://github.com/myorg/gitops/pull/42",
				GateResults: []v1alpha1.GateResult{
					{
						GateName:    "no-weekend-deploys",
						Result:      "pass",
						Reason:      "Not a weekend",
						EvaluatedAt: now,
					},
				},
			},
		},
	}

	data, err := json.Marshal(b)
	require.NoError(t, err)

	var got v1alpha1.Bundle
	require.NoError(t, json.Unmarshal(data, &got))

	require.Len(t, got.Status.Environments, 1)
	env := got.Status.Environments[0]
	assert.Equal(t, "prod", env.Name)
	assert.Equal(t, "Verified", env.Phase)
	assert.Equal(t, "https://github.com/myorg/gitops/pull/42", env.PRURL)
	require.Len(t, env.GateResults, 1)
	assert.Equal(t, "no-weekend-deploys", env.GateResults[0].GateName)
	assert.Equal(t, "pass", env.GateResults[0].Result)
}

// ---------------------------------------------------------------------------
// PolicyGate roundtrip
// ---------------------------------------------------------------------------

// TestPolicyGateRoundtrip verifies that a fully-populated PolicyGate
// serializes and deserializes correctly.
func TestPolicyGateRoundtrip(t *testing.T) {
	original := &v1alpha1.PolicyGate{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "kardinal.io/v1alpha1",
			Kind:       "PolicyGate",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-weekend-deploys",
			Namespace: "platform-policies",
		},
		Spec: v1alpha1.PolicyGateSpec{
			Expression:      "!schedule.isWeekend",
			Message:         "Production deployments are blocked on weekends",
			RecheckInterval: "5m",
			SkipPermission:  true,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"policy/scope": "org"},
			},
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err, "marshal PolicyGate")

	var got v1alpha1.PolicyGate
	require.NoError(t, json.Unmarshal(data, &got), "unmarshal PolicyGate")

	assert.Equal(t, "!schedule.isWeekend", got.Spec.Expression)
	assert.Equal(t, "5m", got.Spec.RecheckInterval)
	assert.True(t, got.Spec.SkipPermission)
	require.NotNil(t, got.Spec.Selector)
	assert.Equal(t, "org", got.Spec.Selector.MatchLabels["policy/scope"])
}

// ---------------------------------------------------------------------------
// PromotionStep roundtrip
// ---------------------------------------------------------------------------

// TestPromotionStepRoundtrip verifies that a fully-populated PromotionStep
// serializes and deserializes correctly.
func TestPromotionStepRoundtrip(t *testing.T) {
	original := &v1alpha1.PromotionStep{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "kardinal.io/v1alpha1",
			Kind:       "PromotionStep",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx-demo-prod-open-pr",
			Namespace: "default",
		},
		Spec: v1alpha1.PromotionStepSpec{
			PipelineName: "nginx-demo",
			BundleName:   "nginx-demo-v1",
			Environment:  "prod",
			StepType:     "open-pr",
			Inputs: map[string]string{
				"repo":   "myorg/gitops",
				"branch": "kardinal/promote-nginx-demo-v1-prod",
			},
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err, "marshal PromotionStep")

	var got v1alpha1.PromotionStep
	require.NoError(t, json.Unmarshal(data, &got), "unmarshal PromotionStep")

	assert.Equal(t, "open-pr", got.Spec.StepType)
	assert.Equal(t, "prod", got.Spec.Environment)
	assert.Equal(t, "nginx-demo", got.Spec.PipelineName)
	assert.Equal(t, "nginx-demo-v1", got.Spec.BundleName)
	require.NotNil(t, got.Spec.Inputs)
	assert.Equal(t, "myorg/gitops", got.Spec.Inputs["repo"])
	assert.Equal(t, "kardinal/promote-nginx-demo-v1-prod", got.Spec.Inputs["branch"])
}

// TestPromotionStepStatusOutputs verifies the Outputs map in the status.
func TestPromotionStepStatusOutputs(t *testing.T) {
	ps := &v1alpha1.PromotionStep{}
	ps.Status = v1alpha1.PromotionStepStatus{
		Phase:   "Succeeded",
		Message: "PR merged",
		Outputs: map[string]string{
			"prURL": "https://github.com/myorg/gitops/pull/42",
		},
	}

	data, err := json.Marshal(ps)
	require.NoError(t, err)

	var got v1alpha1.PromotionStep
	require.NoError(t, json.Unmarshal(data, &got))

	assert.Equal(t, "Succeeded", got.Status.Phase)
	assert.Equal(t, "PR merged", got.Status.Message)
	require.NotNil(t, got.Status.Outputs)
	assert.Equal(t, "https://github.com/myorg/gitops/pull/42", got.Status.Outputs["prURL"])
}

// ---------------------------------------------------------------------------
// DeepCopy
// ---------------------------------------------------------------------------

// TestDeepCopy verifies that DeepCopy (generated by controller-gen) produces
// independent copies — a mutation of the copy does not affect the original.
func TestDeepCopy(t *testing.T) {
	orig := &v1alpha1.Pipeline{
		Spec: v1alpha1.PipelineSpec{
			Environments: []v1alpha1.EnvironmentSpec{
				{Name: "test"},
			},
		},
	}

	cp := orig.DeepCopy()
	cp.Spec.Environments[0].Name = "mutated"

	assert.NotEqual(t, "mutated", orig.Spec.Environments[0].Name, "DeepCopy is shallow")
}

// TestDeepCopyBundle verifies Bundle DeepCopy independence.
func TestDeepCopyBundle(t *testing.T) {
	ts := metav1.Now()
	orig := &v1alpha1.Bundle{
		Spec: v1alpha1.BundleSpec{
			Pipeline: "nginx-demo",
			Images: []v1alpha1.ImageRef{
				{Repository: "ghcr.io/nginx/nginx", Tag: "1.0"},
			},
			Provenance: &v1alpha1.BundleProvenance{
				CommitSHA: "abc",
				Timestamp: ts,
			},
			Intent: &v1alpha1.BundleIntent{
				SkipEnvironments: []string{"uat"},
			},
		},
	}

	cp := orig.DeepCopy()
	cp.Spec.Images[0].Tag = "mutated"
	cp.Spec.Provenance.CommitSHA = "mutated"
	cp.Spec.Intent.SkipEnvironments[0] = "mutated"

	assert.Equal(t, "1.0", orig.Spec.Images[0].Tag, "Images slice is shallow")
	assert.Equal(t, "abc", orig.Spec.Provenance.CommitSHA, "Provenance pointer is shallow")
	assert.Equal(t, "uat", orig.Spec.Intent.SkipEnvironments[0], "Intent slice is shallow")
}

// ---------------------------------------------------------------------------
// GroupVersion
// ---------------------------------------------------------------------------

// TestGroupVersion verifies the API group and version constants.
func TestGroupVersion(t *testing.T) {
	assert.Equal(t, "kardinal.io", v1alpha1.GroupVersion.Group)
	assert.Equal(t, "v1alpha1", v1alpha1.GroupVersion.Version)
}
