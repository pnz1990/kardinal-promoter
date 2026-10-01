// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

// GrafanaPrometheusUID is the uid of the Prometheus datasource
// hack/e2e/components/grafana.sh provisions as Grafana's default.
const GrafanaPrometheusUID = "prometheus"

// grafanaProxy reaches Grafana's Service through the API server. Anonymous
// requests are Viewers (hack/e2e/components/grafana.sh), so no credentials
// are needed.
const grafanaProxy = "/api/v1/namespaces/" + MonitoringNamespace + "/services/http:grafana:80/proxy"

// GrafanaGet GETs path (no leading slash) from Grafana's HTTP API and
// decodes the JSON response into out.
func (e *Env) GrafanaGet(ctx context.Context, path string, params map[string]string, out interface{}) error {
	req := e.Kube.CoreV1().RESTClient().Get().AbsPath(grafanaProxy, path)
	for k, v := range params {
		req = req.Param(k, v)
	}
	raw, err := req.DoRaw(ctx)
	if err != nil {
		return fmt.Errorf("grafana GET %s: %w: %s", path, err, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode grafana %s: %w", path, err)
	}
	return nil
}

// GrafanaSearchHit is one result of Grafana's /api/search.
type GrafanaSearchHit struct {
	UID   string `json:"uid"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

// GrafanaSearch returns the dashboards and folders whose title matches query.
func (e *Env) GrafanaSearch(ctx context.Context, query string) ([]GrafanaSearchHit, error) {
	var hits []GrafanaSearchHit
	err := e.GrafanaGet(ctx, "api/search", map[string]string{"query": query}, &hits)
	return hits, err
}

// GrafanaDatasourceRef is a panel's or a query's datasource: a uid, or a
// datasource variable such as ${datasource}.
type GrafanaDatasourceRef struct {
	Type string `json:"type"`
	UID  string `json:"uid"`
}

// GrafanaDatasource is a datasource as Grafana's frontend lists it.
type GrafanaDatasource struct {
	Name      string `json:"name"`
	UID       string `json:"uid"`
	Type      string `json:"type"`
	IsDefault bool   `json:"isDefault"`
}

// GrafanaDatasources lists the datasources a Viewer can query, from
// /api/frontend/settings (/api/datasources needs an admin).
func (e *Env) GrafanaDatasources(ctx context.Context) ([]GrafanaDatasource, error) {
	var settings struct {
		Datasources map[string]GrafanaDatasource `json:"datasources"`
	}
	if err := e.GrafanaGet(ctx, "api/frontend/settings", nil, &settings); err != nil {
		return nil, err
	}
	out := make([]GrafanaDatasource, 0, len(settings.Datasources))
	for name, ds := range settings.Datasources {
		ds.Name = name
		out = append(out, ds)
	}
	return out, nil
}

// GrafanaDashboard is a dashboard from /api/dashboards/uid/<uid>.
type GrafanaDashboard struct {
	Meta struct {
		Provisioned bool `json:"provisioned"`
	} `json:"meta"`
	Dashboard struct {
		UID        string         `json:"uid"`
		Title      string         `json:"title"`
		Panels     []GrafanaPanel `json:"panels"`
		Templating struct {
			List []GrafanaVariable `json:"list"`
		} `json:"templating"`
	} `json:"dashboard"`
}

// GrafanaPanel is a dashboard panel; a collapsed row holds its panels.
type GrafanaPanel struct {
	Title      string                `json:"title"`
	Type       string                `json:"type"`
	Datasource *GrafanaDatasourceRef `json:"datasource"`
	Targets    []struct {
		Datasource *GrafanaDatasourceRef `json:"datasource"`
		Expr       string                `json:"expr"`
	} `json:"targets"`
	Panels []GrafanaPanel `json:"panels"`
}

// GrafanaVariable is a dashboard template variable. For a datasource
// variable, Query is the datasource type it selects.
type GrafanaVariable struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Query   string `json:"query"`
	Current struct {
		Value interface{} `json:"value"`
	} `json:"current"`
}

// GrafanaPanelQuery is one query of a dashboard panel, with the datasource
// it names (the panel's when the query names none).
type GrafanaPanelQuery struct {
	Panel      string
	Expr       string
	Datasource *GrafanaDatasourceRef
}

// Queries lists the queries of every panel, in collapsed rows too.
func (d GrafanaDashboard) Queries() []GrafanaPanelQuery {
	var out []GrafanaPanelQuery
	var walk func([]GrafanaPanel)
	walk = func(panels []GrafanaPanel) {
		for _, p := range panels {
			walk(p.Panels)
			for _, t := range p.Targets {
				ds := t.Datasource
				if ds == nil {
					ds = p.Datasource
				}
				out = append(out, GrafanaPanelQuery{Panel: p.Title, Expr: t.Expr, Datasource: ds})
			}
		}
	}
	walk(d.Dashboard.Panels)
	return out
}

var grafanaVariableRef = regexp.MustCompile(`^\$(?:\{(\w+)\}|(\w+))$`)

// Resolve returns the datasource ref names, as the dashboard does in
// Grafana: no ref is the default datasource; ${name} or $name is the
// datasource variable name, whose value is its current one or else the
// default datasource of its type (else the first of that type); any other
// uid is the datasource with that uid. It fails as the panel does, with
// "datasource <uid> was not found".
func (d GrafanaDashboard) Resolve(ref *GrafanaDatasourceRef, all []GrafanaDatasource) (GrafanaDatasource, error) {
	find := func(match func(GrafanaDatasource) bool) (GrafanaDatasource, bool) {
		for _, ds := range all {
			if match(ds) {
				return ds, true
			}
		}
		return GrafanaDatasource{}, false
	}
	if ref == nil {
		if ds, ok := find(func(ds GrafanaDatasource) bool { return ds.IsDefault }); ok {
			return ds, nil
		}
		return GrafanaDatasource{}, fmt.Errorf("no default datasource")
	}
	if m := grafanaVariableRef.FindStringSubmatch(ref.UID); m != nil {
		name := m[1] + m[2]
		for _, v := range d.Dashboard.Templating.List {
			if v.Name != name || v.Type != "datasource" {
				continue
			}
			if cur, _ := v.Current.Value.(string); cur != "" && cur != "default" {
				if ds, ok := find(func(ds GrafanaDatasource) bool { return ds.UID == cur || ds.Name == cur }); ok {
					return ds, nil
				}
			}
			if ds, ok := find(func(ds GrafanaDatasource) bool { return ds.IsDefault && ds.Type == v.Query }); ok {
				return ds, nil
			}
			if ds, ok := find(func(ds GrafanaDatasource) bool { return ds.Type == v.Query }); ok {
				return ds, nil
			}
		}
	}
	if ds, ok := find(func(ds GrafanaDatasource) bool { return ds.UID == ref.UID }); ok {
		return ds, nil
	}
	return GrafanaDatasource{}, fmt.Errorf("datasource %s was not found", ref.UID)
}

// GrafanaFrame is one data frame of a query result: its fields (a time
// field and the value fields, with their series labels) and their values,
// one column per field.
type GrafanaFrame struct {
	Schema struct {
		Fields []struct {
			Name   string            `json:"name"`
			Type   string            `json:"type"`
			Labels map[string]string `json:"labels"`
		} `json:"fields"`
	} `json:"schema"`
	Data struct {
		Values [][]interface{} `json:"values"`
	} `json:"data"`
}

// GrafanaQueryResult is the result of one query through Grafana.
type GrafanaQueryResult struct {
	Status int            `json:"status"`
	Error  string         `json:"error"`
	Frames []GrafanaFrame `json:"frames"`
}

// Rows is the number of values the query returned in all, what a panel
// shows: 0 is "No data".
func (r GrafanaQueryResult) Rows() int {
	n := 0
	for _, f := range r.Frames {
		if len(f.Data.Values) > 0 {
			n += len(f.Data.Values[0])
		}
	}
	return n
}

// SeriesLabels lists the labels of every series the query returned.
func (r GrafanaQueryResult) SeriesLabels() []map[string]string {
	var out []map[string]string
	for _, f := range r.Frames {
		for _, field := range f.Schema.Fields {
			if field.Type == "number" {
				out = append(out, field.Labels)
			}
		}
	}
	return out
}

// GrafanaQuery runs a PromQL instant query on datasource ds through
// Grafana's /api/ds/query, as a panel does. A query Prometheus rejects
// returns its error in the result, not as an error.
func (e *Env) GrafanaQuery(ctx context.Context, ds GrafanaDatasource, expr string) (GrafanaQueryResult, error) {
	body, err := json.Marshal(map[string]interface{}{
		"from": "now-15m", "to": "now",
		"queries": []map[string]interface{}{{
			"refId": "A", "expr": expr, "instant": true,
			"datasource": GrafanaDatasourceRef{Type: ds.Type, UID: ds.UID},
		}},
	})
	if err != nil {
		return GrafanaQueryResult{}, err
	}
	raw, err := e.Kube.CoreV1().RESTClient().Post().AbsPath(grafanaProxy, "api/ds/query").
		SetHeader("Content-Type", "application/json").Body(body).DoRaw(ctx)
	// A query error is a 400 with the error in the result.
	var resp struct {
		Message string                        `json:"message"`
		Results map[string]GrafanaQueryResult `json:"results"`
	}
	if jerr := json.Unmarshal(raw, &resp); jerr != nil || resp.Results["A"].Status == 0 {
		if err == nil {
			err = fmt.Errorf("no result: %s", raw)
		}
		return GrafanaQueryResult{}, fmt.Errorf("grafana query %q on %s: %w: %s", expr, ds.UID, err, resp.Message)
	}
	return resp.Results["A"], nil
}
