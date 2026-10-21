// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"fmt"
	"path/filepath"
	"strings"

	"helm.sh/helm/v4/pkg/chart/common"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	"helm.sh/helm/v4/pkg/engine"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// The release the scenario cluster installs the chart as, and the identities
// the chart names: the controller's ServiceAccount and the ServiceAccount kro
// impersonates for a Graph.
const (
	Release          = "kardinal-promoter"
	ReleaseNamespace = "kardinal-system"
	ControllerUser   = "system:serviceaccount:" + ReleaseNamespace + ":" + Release
	graphSA          = "kardinal-graph"
)

// GraphUser is the user kro applies a Graph's objects in ns as.
func GraphUser(ns string) string {
	return "system:serviceaccount:" + ns + ":" + graphSA
}

// Chart is what the rendered chart holds of RBAC and admission, by name.
type Chart struct {
	Roles    map[string]*rbacv1.ClusterRole
	CRBs     []rbacv1.ClusterRoleBinding
	VAPs     map[string]*admissionregistrationv1.ValidatingAdmissionPolicy
	Bindings map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding
}

// RenderChart renders the chart in process (helm.sh/helm/v4, the engine helm
// template uses) as Release in ReleaseNamespace with its default values.
func RenderChart() (Chart, error) {
	return RenderChartWith(map[string]any{})
}

// RenderChartWith is RenderChart with values over the defaults.
func RenderChartWith(values map[string]any) (Chart, error) {
	chrt, err := loader.Load(filepath.Join(RepoRoot(), "chart", "kardinal-promoter"))
	if err != nil {
		return Chart{}, fmt.Errorf("load chart: %w", err)
	}
	vals, err := chartutil.ToRenderValues(chrt, values, common.ReleaseOptions{Name: Release, Namespace: ReleaseNamespace,
		IsInstall: true}, common.DefaultCapabilities)
	if err != nil {
		return Chart{}, fmt.Errorf("chart values: %w", err)
	}
	files, err := engine.Render(chrt, vals)
	if err != nil {
		return Chart{}, fmt.Errorf("render chart: %w", err)
	}
	c := Chart{Roles: map[string]*rbacv1.ClusterRole{}, VAPs: map[string]*admissionregistrationv1.ValidatingAdmissionPolicy{},
		Bindings: map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding{}}
	for name, content := range files {
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		for _, doc := range strings.Split(content, "\n---") {
			var head struct {
				Kind string `json:"kind"`
			}
			if err := yaml.Unmarshal([]byte(doc), &head); err != nil || head.Kind == "" {
				continue
			}
			var into interface{}
			switch head.Kind {
			case "ClusterRole":
				into = &rbacv1.ClusterRole{}
			case "ClusterRoleBinding":
				into = &rbacv1.ClusterRoleBinding{}
			case "ValidatingAdmissionPolicyBinding":
				into = &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
			case "ValidatingAdmissionPolicy":
				into = &admissionregistrationv1.ValidatingAdmissionPolicy{}
			default:
				continue
			}
			if err := utilyaml.Unmarshal([]byte(doc), into); err != nil {
				return Chart{}, fmt.Errorf("%s: %w", name, err)
			}
			switch x := into.(type) {
			case *rbacv1.ClusterRole:
				c.Roles[x.Name] = x
			case *rbacv1.ClusterRoleBinding:
				c.CRBs = append(c.CRBs, *x)
			case *admissionregistrationv1.ValidatingAdmissionPolicyBinding:
				c.Bindings[x.Name] = x
			case *admissionregistrationv1.ValidatingAdmissionPolicy:
				c.VAPs[x.Name] = x
			}
		}
	}
	return c, nil
}
