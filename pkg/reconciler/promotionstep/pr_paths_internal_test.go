// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPRPaths: the paths a PR refresh compares with the base branch's changes
// are the environment's directory and the Helm valuesFile and
// chartVersionFile it writes, cleaned, including ones outside the directory.
func TestPRPaths(t *testing.T) {
	helm := func(values, chart string) v1alpha1.UpdateConfig {
		return v1alpha1.UpdateConfig{Strategy: "helm", Helm: &v1alpha1.HelmUpdateConfig{ValuesFile: values, ChartVersionFile: chart}}
	}
	for _, tc := range []struct {
		name string
		env  v1alpha1.EnvironmentSpec
		want []string
	}{
		{"default directory", v1alpha1.EnvironmentSpec{Name: "test"}, []string{"environments/test"}},
		{"path cleaned", v1alpha1.EnvironmentSpec{Name: "test", Path: "./apps/test/"}, []string{"apps/test"}},
		{"helm files inside", v1alpha1.EnvironmentSpec{Name: "prod", Path: "envs/prod", Update: helm("values.yaml", "Chart.yaml")},
			[]string{"envs/prod", "envs/prod/values.yaml", "envs/prod/Chart.yaml"}},
		{"helm files outside", v1alpha1.EnvironmentSpec{Name: "prod", Path: "envs/prod", Update: helm("../shared/values.yaml", "../../charts/app/Chart.yaml")},
			[]string{"envs/prod", "envs/shared/values.yaml", "charts/app/Chart.yaml"}},
		{"chartVersionFile only", v1alpha1.EnvironmentSpec{Name: "prod", Path: "envs/prod", Update: helm("", "../argo/app.yaml")},
			[]string{"envs/prod", "envs/argo/app.yaml"}},
		{"not the helm strategy", v1alpha1.EnvironmentSpec{Name: "prod", Path: "envs/prod",
			Update: v1alpha1.UpdateConfig{Strategy: "kustomize", Helm: &v1alpha1.HelmUpdateConfig{ChartVersionFile: "../x.yaml"}}},
			[]string{"envs/prod"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, prPaths(tc.env))
		})
	}
	assert.True(t, touchesAny([]string{"envs/argo/app.yaml"}, prPaths(v1alpha1.EnvironmentSpec{Name: "prod", Path: "envs/prod", Update: helm("", "../argo/app.yaml")})),
		"a base change to the chartVersionFile outside the directory rebuilds the PR")
}
