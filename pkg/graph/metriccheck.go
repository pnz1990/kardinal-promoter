// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// LabelMetricTemplate, on a MetricCheck the Graph created, is the name of the
// per-promotion MetricCheck (spec.perPromotion) it was made from. Together
// with kardinal.io/bundle and kardinal.io/environment it tells the PolicyGate
// reconciler which instance a gate reads as metrics.<template name>.
const LabelMetricTemplate = "kardinal.io/metric-template"

// placeholderRE matches a MetricCheck placeholder such as {{ bundle.version }}.
var placeholderRE = regexp.MustCompile(`\{\{\s*([A-Za-z][A-Za-z.]*)\s*\}\}`)

// safeValueRE is the form a value must have to be put into a query. A
// Bundle's version or image tag comes from CI: a value such as `"} or vector(1)`
// would otherwise rewrite the query, so a value with any other character is
// not substituted, the placeholder stays, and the instance fails closed. An
// empty value is not substituted either: `commit=""` matches nothing, and a
// count over nothing (NRQL count(*), PromQL `or vector(0)`) is 0, which would
// pass a gate that should fail. A leading "." and ".." are refused too.
var safeValueRE = regexp.MustCompile(`^[A-Za-z0-9_+-][A-Za-z0-9._+-]*$`)

// digestRE is an OCI digest (sha256:<hex>), the one value with a ":".
var digestRE = regexp.MustCompile(`^[a-z0-9]+:[a-f0-9]{32,128}$`)

// safeValue reports whether v may be substituted for a placeholder: a
// version, tag, name or commit of [A-Za-z0-9._+-] (no "@" or ":", which
// would let a value change the host of a URL), or a digest.
func safeValue(v string) bool {
	return (safeValueRE.MatchString(v) && !strings.Contains(v, "..")) || digestRE.MatchString(v)
}

// MetricPlaceholders returns the placeholder names in s ("bundle.version"),
// in order of appearance.
func MetricPlaceholders(s string) []string {
	var names []string
	for _, m := range placeholderRE.FindAllStringSubmatch(s, -1) {
		names = append(names, m[1])
	}
	return names
}

// MetricTemplateVars are the placeholder values of one promotion of a Bundle
// into an environment.
func MetricTemplateVars(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle, env string) map[string]string {
	vars := map[string]string{
		"bundle.name":        bundle.Name,
		"bundle.version":     BundleVersion(bundle),
		"bundle.imageTag":    "",
		"bundle.imageDigest": "",
		"bundle.commitSHA":   "",
		"pipeline.name":      pipeline.Name,
		"environment.name":   env,
		"namespace":          pipeline.Namespace,
	}
	if len(bundle.Spec.Images) > 0 {
		vars["bundle.imageTag"] = bundle.Spec.Images[0].Tag
		vars["bundle.imageDigest"] = bundle.Spec.Images[0].Digest
	}
	if bundle.Spec.Provenance != nil {
		vars["bundle.commitSHA"] = bundle.Spec.Provenance.CommitSHA
	}
	return vars
}

// KnownMetricPlaceholder reports whether name is a placeholder the Graph
// builder replaces.
func KnownMetricPlaceholder(name string) bool {
	switch name {
	case "bundle.name", "bundle.version", "bundle.imageTag", "bundle.imageDigest", "bundle.commitSHA",
		"pipeline.name", "environment.name", "namespace":
		return true
	}
	return false
}

// RenderMetricText replaces the placeholders in s with vars. An unknown
// placeholder, or one whose value is empty, starts with ".", contains "..",
// or has a character outside [A-Za-z0-9._+-] (a digest excepted), is left
// as it is: the MetricCheck reconciler refuses to query a text with a
// placeholder left in it.
func RenderMetricText(s string, vars map[string]string) string {
	return placeholderRE.ReplaceAllStringFunc(s, func(m string) string {
		name := placeholderRE.FindStringSubmatch(m)[1]
		v, ok := vars[name]
		if !ok || !safeValue(v) {
			return m
		}
		return v
	})
}

// BundleVersion returns the version of a Bundle that gates read as
// bundle.version: the first 8 characters of configRef.commitSHA for a config
// Bundle, otherwise the first image's tag.
func BundleVersion(bundle *kardinalv1alpha1.Bundle) string {
	if bundle.Spec.Type == "config" && bundle.Spec.ConfigRef != nil {
		sha := bundle.Spec.ConfigRef.CommitSHA
		if len(sha) > 8 {
			return sha[:8]
		}
		return sha
	}
	if len(bundle.Spec.Images) > 0 {
		return bundle.Spec.Images[0].Tag
	}
	return ""
}

// ExprReadsMetric reports whether a PolicyGate expression names the metric:
// metrics["name"], metrics['name'] or metrics.name.
func ExprReadsMetric(expr, name string) bool {
	if strings.Contains(expr, `"`+name+`"`) || strings.Contains(expr, `'`+name+`'`) {
		return true
	}
	for rest := expr; ; {
		i := strings.Index(rest, "metrics."+name)
		if i < 0 {
			return false
		}
		rest = rest[i+len("metrics."+name):]
		if rest == "" || !isIdentChar(rest[0]) {
			return true
		}
	}
}

// isIdentChar reports whether c can continue a CEL identifier.
func isIdentChar(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// metricNodeName returns the node ID of a per-promotion MetricCheck instance:
// "metric0<template>0<env>00<bundle>", camelCase. ValidateNodeIDs rejects
// collisions.
func metricNodeName(bundleSlug, template, envName string) string {
	return "metric0" + CELSafeSlug(template) + "0" + CELSafeSlug(envName) + "00" + bundleSlug
}

// metricNodeK8sName returns the metadata.name of a per-promotion MetricCheck
// instance, "<template>-<env>--<bundle>", hash-suffixed when it would lose
// characters or is too long.
func metricNodeK8sName(bundle, template, envName string) string {
	preferred := fmt.Sprintf("%s-%s--%s", template, envName, bundle)
	exact := isSlug(template) && isSlug(envName) && isSlug(bundle)
	return boundedName(preferred, exact, nameKey("metric", template, envName, bundle), maxObjectNameLen)
}

// metricTemplatesFor returns the per-promotion MetricChecks the gates of one
// environment read, sorted by name. Only gates that read metrics.* from the
// Pipeline namespace count: an org gate reads its org policy namespace, where
// the Graph creates nothing.
func metricTemplatesFor(gates []kardinalv1alpha1.PolicyGate, templates []kardinalv1alpha1.MetricCheck,
	pipelineNS string, policyNamespaces []string) []kardinalv1alpha1.MetricCheck {
	var out []kardinalv1alpha1.MetricCheck
	for _, mc := range templates {
		if !mc.Spec.PerPromotion || mc.Namespace != pipelineNS {
			continue
		}
		for _, g := range gates {
			readsPipelineNS := g.Namespace == pipelineNS || !IsPolicyNamespace(g.Namespace, policyNamespaces)
			if readsPipelineNS && ExprReadsMetric(g.Spec.Expression, mc.Name) {
				out = append(out, mc)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// buildMetricCheckNode builds the Graph node of a per-promotion MetricCheck
// instance: a copy of the template's spec with the placeholders of this
// Bundle and environment replaced, perPromotion dropped, and two fields that
// kro keeps current from the Graph:
//
//   - spec.suspend is ${!(bundle.status.phase in ["Available", "Promoting"])}:
//     once the Bundle is Verified, Failed or Superseded the instance stops
//     querying (a Failed Bundle that promotes again resumes it). kro re-applies
//     the template when the Bundle ref changes.
//   - spec.query (spec.web.url for provider web) only resolves once every
//     upstream PromotionStep is Verified (resolvableWhen), so the instance is
//     created when the analysis of the upstream deployment can start. Until
//     then the gate sees the template, which has no result, and blocks.
//
// Every other string of the copied spec that contains "${" is passed to kro
// as a CEL string literal, so text in a query is never evaluated by kro.
func buildMetricCheckNode(nodeID, k8sName string, tmpl kardinalv1alpha1.MetricCheck,
	vars map[string]string, pipelineName, bundleName, envName string, upstreams []string) (GraphNode, error) {
	spec := *tmpl.Spec.DeepCopy()
	spec.PerPromotion = false
	spec.Suspend = false
	spec.Query = RenderMetricText(spec.Query, vars)
	if spec.Web != nil {
		spec.Web.URL = renderURL(spec.Web.URL, vars)
		spec.Web.Body = RenderMetricText(spec.Web.Body, vars)
		for i := range spec.Web.Headers {
			if v := spec.Web.Headers[i].Value; v != nil {
				r := RenderMetricText(*v, vars)
				spec.Web.Headers[i].Value = &r
			}
		}
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return GraphNode{}, fmt.Errorf("build: MetricCheck %q: %w", tmpl.Name, err)
	}
	var specMap map[string]interface{}
	if err := json.Unmarshal(raw, &specMap); err != nil {
		return GraphNode{}, fmt.Errorf("build: MetricCheck %q: %w", tmpl.Name, err)
	}
	quoteKroStrings(specMap)

	// The field that holds the instance back until the upstreams are Verified.
	held, key, text := specMap, "query", spec.Query
	if web, ok := specMap["web"].(map[string]interface{}); ok && spec.Provider == "web" {
		held, key, text = web, "url", spec.Web.URL
	}
	if len(upstreams) > 0 {
		conds := make([]string, len(upstreams))
		for i, up := range upstreams {
			conds[i] = verifiedCond(up)
		}
		held[key] = resolvableWhen(strings.Join(conds, " && "), strconv.Quote(text))
	}
	specMap["suspend"] = `${!(bundle.status.phase in ["Available", "Promoting"])}`

	return GraphNode{
		ID: nodeID,
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "MetricCheck",
			"metadata": map[string]interface{}{
				"name": k8sName,
				"labels": map[string]interface{}{
					"kardinal.io/pipeline":    pipelineName,
					"kardinal.io/bundle":      bundleName,
					"kardinal.io/environment": envName,
					LabelMetricTemplate:       tmpl.Name,
				},
			},
			"spec": specMap,
		},
	}, nil
}

// renderURL renders a web URL template. The host must be literal in the
// template and must not change: a placeholder in the host, or a value that
// would move the request to another host (userinfo "x@evil", a port), leaves
// the URL unrendered, so the instance fails closed.
func renderURL(tmpl string, vars map[string]string) string {
	before, err := url.Parse(tmpl)
	if err != nil || strings.Contains(before.Host, "{{") || strings.Contains(before.Scheme, "{{") {
		return tmpl
	}
	out := RenderMetricText(tmpl, vars)
	after, err := url.Parse(out)
	if err != nil || after.Host != before.Host || after.Scheme != before.Scheme || after.User != nil {
		return tmpl
	}
	return out
}

// quoteKroStrings rewrites, in place, every string in v that contains "${"
// into a kro expression that evaluates to the string itself: ${"<text>"}.
// kro reads "${...}" in a template as CEL; a CEL string literal ends the
// expression only at its closing quote, so the text comes through unchanged.
// strconv.Quote's escapes (\", \\, \n, \xhh, \uhhhh) are all valid in CEL.
func quoteKroStrings(v interface{}) {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, e := range t {
			if s, ok := e.(string); ok {
				t[k] = kroLiteral(s)
				continue
			}
			quoteKroStrings(e)
		}
	case []interface{}:
		for i, e := range t {
			if s, ok := e.(string); ok {
				t[i] = kroLiteral(s)
				continue
			}
			quoteKroStrings(e)
		}
	}
}

// kroLiteral returns s, or ${"<s>"} when s contains "${".
func kroLiteral(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return "${" + strconv.Quote(s) + "}"
}
