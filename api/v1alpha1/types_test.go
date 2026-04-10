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
// server). Field names match the canonical design docs and examples/:
//   - spec.git (top-level, not per-environment)
//   - spec.environments[].approval (not approvalMode)
//   - spec.environments[].update.strategy (nested)
//   - spec.environments[].health.type/.timeout (nested)
//   - spec.environments[].delivery.delegate (nested)
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
			Git: v1alpha1.PipelineGit{
				URL:      "https://github.com/myorg/gitops.git",
				Branch:   "main",
				Layout:   "directory",
				Provider: "github",
				SecretRef: &v1alpha1.SecretRef{
					Name:      "github-token",
					Namespace: "default",
				},
			},
			Environments: []v1alpha1.EnvironmentSpec{
				{
					Name:     "test",
					Path:     "environments/test",
					Approval: "auto",
					Update:   v1alpha1.UpdateConfig{Strategy: "kustomize"},
					Health:   v1alpha1.HealthConfig{Type: "deployment", Timeout: "30m"},
				},
				{Name: "uat", Approval: "auto", Update: v1alpha1.UpdateConfig{Strategy: "kustomize"}},
				{
					Name:      "prod",
					Path:      "environments/prod",
					Approval:  "pr-review",
					Update:    v1alpha1.UpdateConfig{Strategy: "kustomize"},
					Health:    v1alpha1.HealthConfig{Type: "argocd", Timeout: "60m"},
					Delivery:  v1alpha1.DeliveryConfig{Delegate: "argoRollouts"},
					DependsOn: []string{"uat"},
					Shard:     "prod-cluster",
				},
			},
			PolicyGates: []v1alpha1.PipelinePolicyGateRef{
				{Name: "no-weekend-deploys", Namespace: "platform-policies"},
			},
			Paused:       false,
			HistoryLimit: 20,
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err, "marshal Pipeline")

	var got v1alpha1.Pipeline
	require.NoError(t, json.Unmarshal(data, &got), "unmarshal Pipeline")

	assert.Equal(t, original.Name, got.Name)

	// Git block
	assert.Equal(t, "https://github.com/myorg/gitops.git", got.Spec.Git.URL)
	assert.Equal(t, "main", got.Spec.Git.Branch)
	assert.Equal(t, "directory", got.Spec.Git.Layout)
	assert.Equal(t, "github", got.Spec.Git.Provider)
	require.NotNil(t, got.Spec.Git.SecretRef)
	assert.Equal(t, "github-token", got.Spec.Git.SecretRef.Name)

	assert.Len(t, got.Spec.Environments, 3)

	e0 := got.Spec.Environments[0]
	assert.Equal(t, "test", e0.Name)
	assert.Equal(t, "environments/test", e0.Path)
	assert.Equal(t, "auto", e0.Approval)
	assert.Equal(t, "kustomize", e0.Update.Strategy)
	assert.Equal(t, "deployment", e0.Health.Type)
	assert.Equal(t, "30m", e0.Health.Timeout)

	e2 := got.Spec.Environments[2]
	assert.Equal(t, "prod", e2.Name)
	assert.Equal(t, "pr-review", e2.Approval)
	assert.Equal(t, "argocd", e2.Health.Type)
	assert.Equal(t, "60m", e2.Health.Timeout)
	assert.Equal(t, "argoRollouts", e2.Delivery.Delegate)
	assert.Equal(t, []string{"uat"}, e2.DependsOn)
	assert.Equal(t, "prod-cluster", e2.Shard)

	require.Len(t, got.Spec.PolicyGates, 1)
	assert.Equal(t, "no-weekend-deploys", got.Spec.PolicyGates[0].Name)
	assert.Equal(t, "platform-policies", got.Spec.PolicyGates[0].Namespace)

	assert.False(t, got.Spec.Paused)
	assert.Equal(t, 20, got.Spec.HistoryLimit)
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
// serializes and deserializes correctly. Note: PromotionStep uses status.state
// (not status.phase) because the Graph controller's readyWhen expressions
// reference ${step.status.state == "Verified"}.
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

// TestPromotionStepStatusState verifies that status.state (not status.phase)
// and all related fields roundtrip correctly.
func TestPromotionStepStatusState(t *testing.T) {
	ps := &v1alpha1.PromotionStep{}
	ps.Status = v1alpha1.PromotionStepStatus{
		State:            "WaitingForMerge",
		Message:          "PR opened, waiting for merge",
		CurrentStepIndex: 3,
		PRURL:            "https://github.com/myorg/gitops/pull/42",
		Outputs: map[string]string{
			"prURL": "https://github.com/myorg/gitops/pull/42",
		},
	}

	data, err := json.Marshal(ps)
	require.NoError(t, err)

	var got v1alpha1.PromotionStep
	require.NoError(t, json.Unmarshal(data, &got))

	assert.Equal(t, "WaitingForMerge", got.Status.State)
	assert.Equal(t, "PR opened, waiting for merge", got.Status.Message)
	assert.Equal(t, 3, got.Status.CurrentStepIndex)
	assert.Equal(t, "https://github.com/myorg/gitops/pull/42", got.Status.PRURL)
	require.NotNil(t, got.Status.Outputs)
	assert.Equal(t, "https://github.com/myorg/gitops/pull/42", got.Status.Outputs["prURL"])
}

// TestPromotionStepStateValues verifies each valid state value can be
// marshaled and unmarshaled.
func TestPromotionStepStateValues(t *testing.T) {
	validStates := []string{
		"Pending", "Promoting", "WaitingForMerge",
		"HealthChecking", "Verified", "Failed",
	}
	for _, state := range validStates {
		t.Run(state, func(t *testing.T) {
			ps := &v1alpha1.PromotionStep{}
			ps.Status.State = state
			data, err := json.Marshal(ps)
			require.NoError(t, err)
			var got v1alpha1.PromotionStep
			require.NoError(t, json.Unmarshal(data, &got))
			assert.Equal(t, state, got.Status.State)
		})
	}
}

// ---------------------------------------------------------------------------
// DeepCopy
// ---------------------------------------------------------------------------

// TestDeepCopy verifies that DeepCopy (generated by controller-gen) produces
// independent copies — a mutation of the copy does not affect the original.
func TestDeepCopy(t *testing.T) {
	orig := &v1alpha1.Pipeline{
		Spec: v1alpha1.PipelineSpec{
			Git: v1alpha1.PipelineGit{URL: "https://github.com/myorg/gitops.git"},
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
