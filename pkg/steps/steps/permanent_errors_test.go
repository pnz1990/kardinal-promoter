// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps/steps"
)

// TestStepErrors_PermanentMarker checks which step errors carry
// parentsteps.ErrPermanent, the marker the PromotionStep reconciler uses to
// fail a step at once instead of retrying it. Configuration a step refuses
// and path confinement denials are permanent. Clone, commit, push and open-pr
// failures are not, so they are retried.
func TestStepErrors_PermanentMarker(t *testing.T) {
	images := []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "v1"}}

	// envState returns a kustomize/helm state for workDir with the env path
	// set, creating a symlink that escapes the checkout when escape is true.
	envState := func(t *testing.T, path string, escape bool) *parentsteps.StepState {
		base := t.TempDir()
		workDir := filepath.Join(base, "work")
		outside := filepath.Join(base, "victim")
		require.NoError(t, os.MkdirAll(filepath.Join(workDir, "environments"), 0o755))
		writeKustomization(t, outside, "kind: Kustomization\n")
		require.NoError(t, os.WriteFile(filepath.Join(outside, "values.yaml"), []byte("image:\n  tag: old\n"), 0o644))
		if escape {
			require.NoError(t, os.Symlink(outside, filepath.Join(workDir, "environments", "prod")))
		}
		state := makeKustomizeState(workDir, "prod", images)
		state.Environment.Path = path
		return state
	}
	build := func(state *parentsteps.StepState) (parentsteps.StepResult, error) {
		return steps.NewKustomizeBuildStep(&stubKustomizeBuilder{output: []byte("x")}).Execute(context.Background(), state)
	}
	argo := func(t *testing.T, approval string, bundle v1alpha1.BundleSpec, app string) (parentsteps.StepResult, error) {
		state := &parentsteps.StepState{
			K8sClient: fake.NewClientBuilder().WithScheme(newArgoCDScheme(t)).Build(),
			Environment: v1alpha1.EnvironmentSpec{Name: "prod", Approval: approval,
				Update: v1alpha1.UpdateConfig{Strategy: "argocd", ArgoCD: &v1alpha1.ArgoCDUpdateConfig{Application: app}}},
			Bundle: bundle, Inputs: map[string]string{}, Outputs: map[string]string{},
		}
		return mustLookup(t, "argocd-set-image").Execute(context.Background(), state)
	}
	configMerge := func(t *testing.T, mutate func(s *parentsteps.StepState)) (parentsteps.StepResult, error) {
		workDir, srcDir := configMergeFixture(t)
		state := configMergeState(workDir, srcDir)
		mutate(state)
		return mustLookup(t, "config-merge").Execute(context.Background(), state)
	}
	gitStep := func(t *testing.T, name string, git *mockGitClient, scmP *mockSCMProvider, mutate func(s *parentsteps.StepState)) (parentsteps.StepResult, error) {
		state := makeState(t, git, scmP)
		if mutate != nil {
			mutate(state)
		}
		return runStep(t, name, state)
	}

	cases := []struct {
		name          string
		run           func(t *testing.T) (parentsteps.StepResult, error)
		wantPermanent bool
	}{
		// Configuration the step refuses.
		{name: "argocd-set-image: approval pr-review", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return argo(t, "pr-review", v1alpha1.BundleSpec{Images: images}, "app")
		}},
		{name: "argocd-set-image: config Bundle", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return argo(t, "auto", v1alpha1.BundleSpec{Type: "config", ConfigRef: &v1alpha1.ConfigRef{CommitSHA: "abc"}}, "app")
		}},
		{name: "argocd-set-image: no application", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return argo(t, "auto", v1alpha1.BundleSpec{Images: images}, "")
		}},
		{name: "git-clone: layout branch", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return gitStep(t, "git-clone", &mockGitClient{}, nil, func(s *parentsteps.StepState) { s.Environment.Layout = "branch" })
		}},
		{name: "git-push: layout branch", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return gitStep(t, "git-push", &mockGitClient{}, nil, func(s *parentsteps.StepState) { s.Pipeline.Git.Layout = "branch" })
		}},
		{name: "config-merge: config source not checked out", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return configMerge(t, func(s *parentsteps.StepState) { s.WorkDir = filepath.Join(s.WorkDir, "environments") })
		}},
		{name: "config-merge: env subtree missing in the commit", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return configMerge(t, func(s *parentsteps.StepState) { s.Environment.Name = "uat" })
		}},
		{name: "helm-set-image: two images", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			state := envState(t, "", false)
			state.Bundle.Images = []v1alpha1.ImageRef{{Repository: "r/a", Tag: "1"}, {Repository: "r/b", Tag: "2"}}
			return mustLookup(t, "helm-set-image").Execute(context.Background(), state)
		}},
		{name: "helm-set-image: digest without tag", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			state := envState(t, "", false)
			state.Bundle.Images = []v1alpha1.ImageRef{{Repository: "r/a", Digest: "sha256:abc"}}
			return mustLookup(t, "helm-set-image").Execute(context.Background(), state)
		}},

		// Path confinement denials.
		{name: "config-merge: env path climbs out", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return configMerge(t, func(s *parentsteps.StepState) { s.Environment.Path = "../escape" })
		}},
		{name: "config-merge: absolute env path", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return configMerge(t, func(s *parentsteps.StepState) { s.Environment.Path = "/etc" })
		}},
		{name: "config-merge: env dir in the checkout is a symlink out", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return configMerge(t, func(s *parentsteps.StepState) {
				envDir := filepath.Join(s.WorkDir, "environments", "prod")
				require.NoError(t, os.RemoveAll(envDir))
				require.NoError(t, os.Symlink(t.TempDir(), envDir))
			})
		}},
		{name: "kustomize-set-image: env path climbs out", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return mustLookup(t, "kustomize-set-image").Execute(context.Background(), envState(t, "../victim", false))
		}},
		{name: "kustomize-set-image: absolute env path", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return mustLookup(t, "kustomize-set-image").Execute(context.Background(), envState(t, "/tmp/victim", false))
		}},
		{name: "kustomize-set-image: symlink escape", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return mustLookup(t, "kustomize-set-image").Execute(context.Background(), envState(t, "environments/prod", true))
		}},
		{name: "helm-set-image: env path climbs out", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return mustLookup(t, "helm-set-image").Execute(context.Background(), envState(t, "../victim", false))
		}},
		{name: "helm-set-image: valuesFile climbs out", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			state := envState(t, "", false)
			state.Environment.Update.Helm = &v1alpha1.HelmUpdateConfig{ValuesFile: "../../../values.yaml"}
			return mustLookup(t, "helm-set-image").Execute(context.Background(), state)
		}},
		{name: "helm-set-image: symlink escape", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return mustLookup(t, "helm-set-image").Execute(context.Background(), envState(t, "environments/prod", true))
		}},
		{name: "kustomize-build: env path climbs out", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return build(envState(t, "../victim", false))
		}},
		{name: "kustomize-build: symlink escape", wantPermanent: true, run: func(t *testing.T) (parentsteps.StepResult, error) {
			return build(envState(t, "environments/prod", true))
		}},

		// Network, API and git failures are retried.
		{name: "git-clone: clone error", run: func(t *testing.T) (parentsteps.StepResult, error) {
			return gitStep(t, "git-clone", &mockGitClient{failClone: true}, nil, nil)
		}},
		{name: "git-commit: commit error", run: func(t *testing.T) (parentsteps.StepResult, error) {
			return gitStep(t, "git-commit", &mockGitClient{failCommit: true}, nil, nil)
		}},
		{name: "git-push: push error", run: func(t *testing.T) (parentsteps.StepResult, error) {
			return gitStep(t, "git-push", &mockGitClient{failPush: true}, nil, nil)
		}},
		{name: "open-pr: SCM 502", run: func(t *testing.T) (parentsteps.StepResult, error) {
			scmP := &mockSCMProvider{openPRErr: &scm.APIError{Provider: "GitHub", Method: "POST", Path: "/repos/owner/repo/pulls",
				StatusCode: 502, Transient: true}}
			return gitStep(t, "open-pr", &mockGitClient{}, scmP, func(s *parentsteps.StepState) { s.Outputs["branch"] = "kardinal/x" })
		}},
		{name: "kustomize-build: build error", run: func(t *testing.T) (parentsteps.StepResult, error) {
			state := envState(t, "", false)
			require.NoError(t, os.MkdirAll(filepath.Join(state.WorkDir, "environments", "prod"), 0o755))
			return steps.NewKustomizeBuildStep(&stubKustomizeBuilder{err: errors.New("exit status 1")}).Execute(context.Background(), state)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.run(t)
			require.Error(t, err)
			assert.Equal(t, parentsteps.StepFailed, res.Status)
			assert.Equal(t, tc.wantPermanent, errors.Is(err, parentsteps.ErrPermanent), "error: %v", err)
		})
	}
}
