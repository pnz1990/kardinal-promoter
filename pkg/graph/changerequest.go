// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/labelvalue"
)

// Per-promotion ChangeRequests (docs/design/23-change-requests.md section 4):
// the MetricCheck mechanism of metriccheck.go, reused. A ChangeRequest with
// spec.perPromotion is a template; the Graph creates an instance of it per
// Bundle and environment whose gates read changeRequests.<name>, once the
// environment's upstreams are Verified, and suspends it once the Bundle stops
// promoting. Creating an instance opens a ticket, so which gates count is
// stricter than for metrics (ExprReadsChangeRequest).

// LabelChangeRequestTemplate, on a ChangeRequest the Graph created, is the
// name of the template it was made from. With kardinal.io/bundle and
// kardinal.io/environment it tells the PolicyGate reconciler which instance a
// gate reads as changeRequests.<template name>.
const LabelChangeRequestTemplate = "kardinal.io/change-request-template"

// MaxChangeRequestValue is the most bytes a placeholder value puts into a
// ticket field.
const MaxChangeRequestValue = 256

// ExprReadsChangeRequest reports whether a PolicyGate expression reads the
// change request name: changeRequests["name"], changeRequests['name'] or
// changeRequests.name. Unlike ExprReadsMetric, a quoted name elsewhere in the
// expression does not count: an instance opens a ticket.
func ExprReadsChangeRequest(expr, name string) bool {
	q := regexp.QuoteMeta(name)
	re := regexp.MustCompile(`\bchangeRequests\s*(\[\s*("` + q + `"|'` + q + `')\s*\]|\.` + q + `($|[^A-Za-z0-9_]))`)
	return re.MatchString(expr)
}

// ChangeRequestApplies reports whether template cr applies to environment
// env (or to its fleet, for a fleet target): spec.appliesTo is unset or names
// one of them.
func ChangeRequestApplies(cr *kardinalv1alpha1.ChangeRequest, env, fleet string) bool {
	if len(cr.Spec.AppliesTo) == 0 {
		return true
	}
	return slices.Contains(cr.Spec.AppliesTo, env) || (fleet != "" && slices.Contains(cr.Spec.AppliesTo, fleet))
}

// changeRequestTemplatesFor returns the templates whose instance the gates
// of environment env read, sorted by name: per-promotion ChangeRequests of
// the Pipeline namespace that apply to env and that a gate reading the
// Pipeline namespace names. An org gate reads its org policy namespace, where
// the Graph creates nothing, so its changeRequests stay empty (fail closed).
func changeRequestTemplatesFor(gates []kardinalv1alpha1.PolicyGate, templates []kardinalv1alpha1.ChangeRequest,
	pipelineNS string, policyNamespaces []string, env, fleet string) []kardinalv1alpha1.ChangeRequest {
	var out []kardinalv1alpha1.ChangeRequest
	for i := range templates {
		cr := &templates[i]
		if !cr.Spec.PerPromotion || cr.Namespace != pipelineNS || !ChangeRequestApplies(cr, env, fleet) {
			continue
		}
		for _, g := range gates {
			readsPipelineNS := g.Namespace == pipelineNS || !IsPolicyNamespace(g.Namespace, policyNamespaces)
			if readsPipelineNS && ExprReadsChangeRequest(g.Spec.Expression, cr.Name) {
				out = append(out, *cr)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// changeRequestNodeName is the node ID of a ChangeRequest instance:
// "creq0<template>0<env>00<bundle>", camelCase. ValidateNodeIDs rejects
// collisions.
func changeRequestNodeName(bundleSlug, template, envName string) string {
	return "creq0" + CELSafeSlug(template) + "0" + CELSafeSlug(envName) + "00" + bundleSlug
}

// changeRequestK8sName is the metadata.name of a ChangeRequest instance,
// "<template>-<env>--<bundle>", hash-suffixed when it would lose characters
// or is too long.
func changeRequestK8sName(bundle, template, envName string) string {
	preferred := fmt.Sprintf("%s-%s--%s", template, envName, bundle)
	exact := isSlug(template) && isSlug(envName) && isSlug(bundle)
	return boundedName(preferred, exact, nameKey("changerequest", template, envName, bundle), maxObjectNameLen)
}

// RenderChangeRequestText replaces the placeholders of s (the MetricCheck
// set, KnownMetricPlaceholder) with vars. Unlike a query, a ticket field is
// text sent as a JSON string, so any value is put in: control characters
// are dropped and a value is cut to MaxChangeRequestValue bytes. An unknown
// placeholder is left as it is.
func RenderChangeRequestText(s string, vars map[string]string) string {
	return placeholderRE.ReplaceAllStringFunc(s, func(m string) string {
		v, ok := vars[placeholderRE.FindStringSubmatch(m)[1]]
		if !ok {
			return m
		}
		return ticketValue(v)
	})
}

// ticketValue is v without control characters, cut to
// MaxChangeRequestValue bytes at a character boundary.
func ticketValue(v string) string {
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, v)
	if len(v) <= MaxChangeRequestValue {
		return v
	}
	cut := MaxChangeRequestValue
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return v[:cut]
}

// changeRequestSpec renders template tmpl's spec for one promotion: the
// placeholders of the ticket templates and of link.requireField.value
// replaced, perPromotion dropped, spec.suspend set to metricSuspendExpr, and
// every other string with "${" quoted for kro.
func changeRequestSpec(tmpl kardinalv1alpha1.ChangeRequest, vars map[string]string) (map[string]interface{}, error) {
	spec := *tmpl.Spec.DeepCopy()
	spec.PerPromotion = false
	spec.Suspend = false
	spec.AppliesTo = nil
	render := func(m map[string]string) {
		for k, v := range m {
			m[k] = RenderChangeRequestText(v, vars)
		}
	}
	if spec.ServiceNow != nil {
		render(spec.ServiceNow.Template)
	}
	if spec.Jira != nil {
		render(spec.Jira.Template)
	}
	if spec.Link != nil && spec.Link.RequireField != nil {
		spec.Link.RequireField.Value = RenderChangeRequestText(spec.Link.RequireField.Value, vars)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("build: ChangeRequest %q: %w", tmpl.Name, err)
	}
	var specMap map[string]interface{}
	if err := json.Unmarshal(raw, &specMap); err != nil {
		return nil, fmt.Errorf("build: ChangeRequest %q: %w", tmpl.Name, err)
	}
	quoteKroStrings(specMap)
	specMap["suspend"] = metricSuspendExpr
	return specMap, nil
}

// changeRequestLabels are the labels of a ChangeRequest instance. The
// Bundle's UID is part of the ticket's correlation id, so a re-created
// Bundle of the same name gets a new ticket.
func changeRequestLabels(pipelineName, bundleName, bundleUID, envName, template string) map[string]interface{} {
	return map[string]interface{}{
		"kardinal.io/pipeline":     pipelineName,
		"kardinal.io/bundle":       bundleName,
		"kardinal.io/environment":  envName,
		LabelBundleUID:             bundleUID,
		LabelChangeRequestTemplate: labelvalue.Fit(template),
	}
}

// buildChangeRequestNode builds the Graph node of a ChangeRequest instance.
// spec.provider only resolves once every upstream PromotionStep is Verified
// (resolvableWhen), so kro creates the instance, and the reconciler opens
// its ticket, only for a version that reached the gate's environment. Until
// then the gate sees no instance: changeRequests.<name>.state is "" and it
// blocks.
func buildChangeRequestNode(nodeID, k8sName string, tmpl kardinalv1alpha1.ChangeRequest, vars map[string]string,
	pipelineName, bundleName, bundleUID, envName string, upstreams []string) (GraphNode, error) {
	specMap, err := changeRequestSpec(tmpl, vars)
	if err != nil {
		return GraphNode{}, err
	}
	if len(upstreams) > 0 {
		conds := make([]string, len(upstreams))
		for i, up := range upstreams {
			conds[i] = verifiedCond(up)
		}
		specMap["provider"] = resolvableWhen(strings.Join(conds, " && "), strconv.Quote(tmpl.Spec.Provider))
	}
	return GraphNode{
		ID: nodeID,
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "ChangeRequest",
			"metadata": map[string]interface{}{
				"name":   k8sName,
				"labels": changeRequestLabels(pipelineName, bundleName, bundleUID, envName, tmpl.Name),
			},
			"spec": specMap,
		},
	}, nil
}

// The compact shape's ChangeRequest instances, as its MetricChecks
// (compactMetricNodes).
const (
	// NodeChangeRequestData holds every instance's rendered spec as data.
	NodeChangeRequestData = "ChangeRequestData"
	// NodePromotionChangeRequests is the instances that may exist.
	NodePromotionChangeRequests = "PromotionChangeRequests"
	// NodeChangeRequests creates one ChangeRequest per
	// NodePromotionChangeRequests item.
	NodeChangeRequests = "ChangeRequests"

	iterChangeRequest = "ChangeReq"
)
