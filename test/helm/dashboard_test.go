// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dashboardDatasource is a panel's or a query's datasource reference.
type dashboardDatasource struct {
	Type string `json:"type"`
	UID  string `json:"uid"`
}

type dashboardPanel struct {
	Title      string               `json:"title"`
	Type       string               `json:"type"`
	Datasource *dashboardDatasource `json:"datasource"`
	Targets    []struct {
		Datasource *dashboardDatasource `json:"datasource"`
		Expr       string               `json:"expr"`
	} `json:"targets"`
	Panels []dashboardPanel `json:"panels"` // a collapsed row's panels
}

// dashboardVariableRef is ${name} or $name.
var dashboardVariableRef = regexp.MustCompile(`^\$(?:\{(\w+)\}|(\w+))$`)

// TestChartDashboardDatasourceResolves: the chart's dashboard works when the
// Grafana sidecar provisions it from the ConfigMap, not only when a user
// imports it in the UI. Every panel and query names its datasource through a
// datasource variable of that type, which Grafana sets to the default
// datasource and users can change. There is no __inputs placeholder: only
// the UI import fills those in, so a provisioned dashboard whose panels used
// ${DS_PROMETHEUS} showed "Datasource ${DS_PROMETHEUS} was not found" in
// every panel and sent no queries.
func TestChartDashboardDatasourceResolves(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(chartPath(t), "dashboards", "kardinal-promoter-dashboard.json"))
	require.NoError(t, err)
	var dash struct {
		Inputs     []map[string]any `json:"__inputs"`
		Panels     []dashboardPanel `json:"panels"`
		Templating struct {
			List []struct {
				Name  string `json:"name"`
				Type  string `json:"type"`
				Query string `json:"query"`
			} `json:"list"`
		} `json:"templating"`
	}
	require.NoError(t, json.Unmarshal(raw, &dash))
	assert.Empty(t, dash.Inputs, "__inputs is filled in only by the UI import; a provisioned dashboard keeps the placeholders")

	// datasource variable name -> the datasource type it selects
	vars := map[string]string{}
	for _, v := range dash.Templating.List {
		if v.Type == "datasource" {
			vars[v.Name] = v.Query
		}
	}
	assert.NotEmpty(t, vars, "the dashboard needs a datasource variable")

	check := func(where string, ds *dashboardDatasource) {
		if !assert.NotNil(t, ds, "%s names no datasource", where) {
			return
		}
		m := dashboardVariableRef.FindStringSubmatch(ds.UID)
		if !assert.NotNil(t, m, "%s: datasource uid %q is not a datasource variable", where, ds.UID) {
			return
		}
		name := m[1] + m[2]
		typ, ok := vars[name]
		if assert.True(t, ok, "%s: datasource uid %q names no datasource variable", where, ds.UID) {
			assert.Equal(t, ds.Type, typ, "%s: variable %s selects another datasource type", where, name)
		}
	}
	queries := 0
	var walk func([]dashboardPanel)
	walk = func(panels []dashboardPanel) {
		for _, p := range panels {
			walk(p.Panels)
			if p.Type == "row" {
				continue
			}
			check("panel "+p.Title, p.Datasource)
			for _, q := range p.Targets {
				check("query "+q.Expr+" of "+p.Title, q.Datasource)
				queries++
			}
		}
	}
	walk(dash.Panels)
	assert.Positive(t, queries, "the dashboard has queries")
}
