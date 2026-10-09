// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package steps_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	parentsteps "github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestHelmSetImage_ChartVersion covers chart Bundles: helm-set-image writes
// spec.chart.version at update.helm.chartVersionPath in chartVersionFile (an
// umbrella Chart.yaml by default, an Argo CD Application, a Flux
// HelmRelease, a kustomization helmCharts entry), keeps comments, is
// idempotent, and fails on a path through a missing list element or a
// scalar.
func TestHelmSetImage_ChartVersion(t *testing.T) {
	tests := []struct {
		name          string
		file, content string
		helm          *v1alpha1.HelmUpdateConfig
		want          string
		wantErr       string
	}{
		{name: "umbrella Chart.yaml default: the dependency named after the chart", file: "Chart.yaml",
			content: "apiVersion: v2\nname: app\nversion: 0.1.0\ndependencies:\n  - name: redis\n    version: 1.0.0\n  - name: podinfo # the app\n    version: 6.14.0\n    repository: https://charts.example.com\n",
			want:    "apiVersion: v2\nname: app\nversion: 0.1.0\ndependencies:\n  - name: redis\n    version: 1.0.0\n  - name: podinfo # the app\n    version: 6.15.0\n    repository: https://charts.example.com\n"},
		{name: "umbrella without the dependency", file: "Chart.yaml",
			content: "dependencies:\n  - name: redis\n    version: 1.0.0\n", wantErr: `dependencies has no element with name "podinfo"`},
		{name: "explicit index", file: "Chart.yaml",
			helm:    &v1alpha1.HelmUpdateConfig{ChartVersionPath: ".dependencies.0.version"},
			content: "dependencies:\n  - name: other\n    version: 1.0.0\n",
			want:    "dependencies:\n  - name: other\n    version: 6.15.0\n"},
		{name: "Argo CD Application", file: "app.yaml",
			helm:    &v1alpha1.HelmUpdateConfig{ChartVersionFile: "app.yaml", ChartVersionPath: ".spec.source.targetRevision"},
			content: "kind: Application\nspec:\n  source:\n    chart: podinfo\n    targetRevision: 6.14.0\n",
			want:    "kind: Application\nspec:\n  source:\n    chart: podinfo\n    targetRevision: 6.15.0\n"},
		{name: "Flux HelmRelease, version unset", file: "release.yaml",
			helm:    &v1alpha1.HelmUpdateConfig{ChartVersionFile: "release.yaml", ChartVersionPath: ".spec.chart.spec.version"},
			content: "kind: HelmRelease\nspec:\n  chart:\n    spec:\n      chart: podinfo\n",
			want:    "kind: HelmRelease\nspec:\n  chart:\n    spec:\n      chart: podinfo\n      version: 6.15.0\n"},
		{name: "kustomize helmCharts", file: "kustomization.yaml",
			helm:    &v1alpha1.HelmUpdateConfig{ChartVersionFile: "kustomization.yaml", ChartVersionPath: ".helmCharts[name=podinfo].version"},
			content: "helmCharts:\n- name: redis\n  version: 1.0.0\n- name: podinfo\n  version: 6.14.0\n",
			want:    "helmCharts:\n- name: redis\n  version: 1.0.0\n- name: podinfo\n  version: 6.15.0\n"},
		{name: "list element missing", file: "Chart.yaml", content: "dependencies: []\n",
			helm: &v1alpha1.HelmUpdateConfig{ChartVersionPath: ".dependencies.0.version"}, wantErr: "dependencies.0: the list has 0 elements"},
		{name: "list missing", file: "Chart.yaml", content: "name: app\n",
			wantErr: "dependencies does not exist"},
		{name: "non-numeric list index", file: "Chart.yaml", content: "dependencies:\n- version: 1\n",
			helm: &v1alpha1.HelmUpdateConfig{ChartVersionPath: ".dependencies.first.version"}, wantErr: "dependencies is a list; index it with a number or [field=value]"},
		{name: "path into a scalar", file: "Chart.yaml", content: "dependencies: none\n",
			wantErr: "dependencies is not a list"},
		{name: "bad selector", file: "Chart.yaml", content: "dependencies: []\n",
			helm: &v1alpha1.HelmUpdateConfig{ChartVersionPath: ".dependencies[name].version"}, wantErr: "invalid chartVersionPath"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			envDir := filepath.Join(dir, "environments", "prod")
			require.NoError(t, os.MkdirAll(envDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(envDir, tt.file), []byte(tt.content), 0o644))
			state := &parentsteps.StepState{
				WorkDir:     dir,
				Environment: v1alpha1.EnvironmentSpec{Name: "prod", Update: v1alpha1.UpdateConfig{Strategy: "helm", Helm: tt.helm}},
				Bundle: v1alpha1.BundleSpec{Type: "chart",
					Chart: &v1alpha1.ChartRef{Name: "podinfo", Version: "6.15.0", RepoURL: "https://charts.example.com"}},
				Outputs: map[string]string{},
			}
			for range 2 { // idempotent
				res, err := mustLookup(t, "helm-set-image").Execute(context.Background(), state)
				if tt.wantErr != "" {
					require.Error(t, err)
					// Retried, like a values path that runs into a scalar: a
					// commit to the file can fix it.
					// An unparsable path is permanent: only a Pipeline edit fixes it.
					assert.Equal(t, strings.Contains(tt.wantErr, "invalid chartVersionPath"),
						errors.Is(err, parentsteps.ErrPermanent), "%v", err)
					assert.Contains(t, res.Message, tt.wantErr)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, parentsteps.StepSuccess, res.Status)
				assert.Equal(t, "6.15.0", res.Outputs["chartVersion"])
				raw, err := os.ReadFile(filepath.Join(envDir, tt.file))
				require.NoError(t, err)
				assert.Equal(t, tt.want, string(raw))
			}
		})
	}

	// A chart Bundle without spec.chart fails for good.
	state := &parentsteps.StepState{WorkDir: t.TempDir(), Bundle: v1alpha1.BundleSpec{Type: "chart"}}
	_, err := mustLookup(t, "helm-set-image").Execute(context.Background(), state)
	require.Error(t, err)
	assert.ErrorIs(t, err, parentsteps.ErrPermanent)
}

// TestDefaultSequenceForBundle_Chart routes chart Bundles through
// helm-set-image.
func TestDefaultSequenceForBundle_Chart(t *testing.T) {
	assert.Equal(t, []string{"git-clone", "helm-set-image", "git-commit", "git-push", "open-pr", "wait-for-merge", "health-check"},
		parentsteps.DefaultSequenceForBundle("pr-review", "chart", "helm", ""))
	assert.Equal(t, []string{"argocd-set-image", "health-check"},
		parentsteps.DefaultSequenceForBundle("auto", "chart", "argocd", ""), "argocd-set-image refuses it")
}
