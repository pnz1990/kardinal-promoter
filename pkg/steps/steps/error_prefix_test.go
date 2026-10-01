// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package steps_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestStepErrors_NameTheStepOnce runs failing built-in steps through the step
// engine and checks the exact error the PromotionStep shows. The engine adds
// "step <name>: ", so a step's own error must not name the step again (B47:
// "step argocd-set-image: argocd-set-image: patch Application: ...").
func TestStepErrors_NameTheStepOnce(t *testing.T) {
	images := []v1alpha1.ImageRef{{Repository: "ghcr.io/o/app", Tag: "v2"}}
	argoState := func(t *testing.T, patchErr error, objs ...client.Object) *parentsteps.StepState {
		k8s := fake.NewClientBuilder().WithScheme(newArgoCDScheme(t)).WithObjects(objs...).
			WithInterceptorFuncs(interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, o ...client.PatchOption) error {
					if patchErr != nil {
						return patchErr
					}
					return c.Patch(ctx, obj, p, o...)
				},
			}).Build()
		return &parentsteps.StepState{
			K8sClient: k8s,
			Environment: v1alpha1.EnvironmentSpec{Name: "prod", Approval: "auto",
				Update: v1alpha1.UpdateConfig{Strategy: "argocd",
					ArgoCD: &v1alpha1.ArgoCDUpdateConfig{Application: "my-app"}}},
			Bundle:  v1alpha1.BundleSpec{Images: images},
			Outputs: map[string]string{},
		}
	}
	gitState := func(t *testing.T, git *mockGitClient, scmP *mockSCMProvider) *parentsteps.StepState {
		state := makeState(t, git, scmP)
		state.Outputs["branch"] = "kardinal/x"
		return state
	}

	cases := []struct {
		name  string
		step  string
		state func(t *testing.T) *parentsteps.StepState
		want  string
		once  string // a string the message names once, besides the step
	}{
		{name: "argocd-set-image patch fails", step: "argocd-set-image",
			state: func(t *testing.T) *parentsteps.StepState {
				return argoState(t, errors.New("connection refused"), makeArgoCDApp("argocd", "my-app", nil))
			},
			want: "step argocd-set-image: patch Application argocd/my-app: connection refused"},
		{name: "argocd-set-image application missing", step: "argocd-set-image",
			state: func(t *testing.T) *parentsteps.StepState { return argoState(t, nil) },
			want:  "step argocd-set-image: argocd Application argocd/my-app not found"},
		{name: "argocd-set-image config Bundle", step: "argocd-set-image",
			state: func(t *testing.T) *parentsteps.StepState {
				s := argoState(t, nil)
				s.Bundle = v1alpha1.BundleSpec{Type: "config", ConfigRef: &v1alpha1.ConfigRef{CommitSHA: "abc"}}
				return s
			},
			want: "step argocd-set-image: config Bundles are not supported by update.strategy argocd"},
		// The GitClient names the operation and the URL, as scm.GoGitClient
		// does; the step adds neither again.
		{name: "git-clone clone fails", step: "git-clone",
			state: func(t *testing.T) *parentsteps.StepState {
				return gitState(t, &mockGitClient{cloneErr: errors.New(
					"git clone https://github.com/owner/repo: authentication required: Unauthorized")}, nil)
			},
			want: "step git-clone: git clone https://github.com/owner/repo: authentication required: Unauthorized",
			once: "https://github.com/owner/repo"},
		{name: "git-clone config source clone fails", step: "git-clone",
			state: func(t *testing.T) *parentsteps.StepState {
				s := gitState(t, &mockGitClient{cloneAtErr: errors.New(
					"resolve commit abc123 in https://github.com/org/config: reference not found")}, nil)
				s.Bundle.Type = "config"
				s.Bundle.ConfigRef = &v1alpha1.ConfigRef{GitRepo: "https://github.com/org/config", CommitSHA: "abc123"}
				return s
			},
			want: "step git-clone: config source: resolve commit abc123 in https://github.com/org/config: reference not found",
			once: "https://github.com/org/config"},
		{name: "git-commit commit fails", step: "git-commit",
			state: func(t *testing.T) *parentsteps.StepState { return gitState(t, &mockGitClient{failCommit: true}, nil) },
			want:  "step git-commit: mock commit error"},
		{name: "git-push push fails", step: "git-push",
			state: func(t *testing.T) *parentsteps.StepState { return gitState(t, &mockGitClient{failPush: true}, nil) },
			want:  "step git-push: mock push error"},
		{name: "open-pr SCM fails", step: "open-pr",
			state: func(t *testing.T) *parentsteps.StepState {
				return gitState(t, &mockGitClient{}, &mockSCMProvider{openPRErr: errors.New("open PR owner/repo: 502 Bad Gateway")})
			},
			want: "step open-pr: open PR owner/repo: 502 Bad Gateway"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := parentsteps.NewEngine([]string{tc.step})
			_, _, err := eng.ExecuteFrom(context.Background(), tc.state(t), 0)
			require.Error(t, err)
			assert.Equal(t, tc.want, err.Error())
			assert.Equal(t, 1, strings.Count(err.Error(), tc.step+":"), "the step is named once: %v", err)
			if tc.once != "" {
				assert.Equal(t, 1, strings.Count(err.Error(), tc.once), "%s is named once: %v", tc.once, err)
			}
		})
	}
}
