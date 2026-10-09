// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Node IDs and forEach iterators of the per-kind collections. They start
// with an uppercase letter, which CELSafeSlug never produces, so no
// environment, gate or health node can take them.
const (
	// NodePolicyGateData holds the gate templates and instances as data:
	// {templates: [...], gates: [...], skipGates: [...]}.
	NodePolicyGateData = "PolicyGateData"
	// NodePolicyGates creates one PolicyGate per NodePolicyGateData gates item.
	NodePolicyGates = "PolicyGates"
	// NodeSkipPermissionGates creates one PolicyGate per skipGates item: the
	// skip-permission instances, which carry a type label and an annotation.
	NodeSkipPermissionGates = "SkipPermissionGates"
	// NodePRStatusData holds one entry per environment's PRStatus.
	NodePRStatusData = "PRStatusData"
	// NodePRStatuses creates one PRStatus per NodePRStatusData item.
	NodePRStatuses = "PRStatuses"

	iterGate = "Gate"
	iterPR   = "PR"
)

// gateCollections collects a Graph's PolicyGate instances as data for the
// PolicyGates and SkipPermissionGates collections.
//
// What an instance copies from its template (spec and the template-level
// labels) is stored once per template in templates; an instance item holds
// only its name, environment and template index. An org gate that applies to
// many environments is then stored once, not once per environment (ledger
// gap G10).
type gateCollections struct {
	pipeline, bundle string
	templates        []interface{}
	index            map[string]int
	// gates and skipGates hold the items of each collection node, in chunks
	// of at most MaxCollectionItems (kro's default forEach limit).
	gates, skipGates [][]interface{}
	instances        []kardinalv1alpha1.PolicyGate
	// collection is the collection node of each instance name.
	collection map[string]string
	what       map[string]string
}

func newGateCollections(pipeline, bundle string) *gateCollections {
	return &gateCollections{pipeline: pipeline, bundle: bundle,
		index: map[string]int{}, collection: map[string]string{}, what: map[string]string{}}
}

// add records the instance of gate for envName, named k8sName. skipped is
// non-empty for a skip-permission instance: the environments it lets the
// Bundle skip. It returns the instance name.
func (c *gateCollections) add(gate kardinalv1alpha1.PolicyGate, envName, k8sName string, skipped []string) (string, error) {
	what := fmt.Sprintf("PolicyGate %q in namespace %q for environment %q", gate.Name, gate.Namespace, envName)
	if prev, dup := c.what[k8sName]; dup {
		return "", fmt.Errorf("build: %s and %s get the same instance name %q; rename one", prev, what, k8sName)
	}
	c.what[k8sName] = what

	key := gate.Namespace + "/" + gate.Name
	t, ok := c.index[key]
	tmpl := gateTemplateData(gate)
	if !ok {
		t = len(c.templates)
		c.index[key] = t
		c.templates = append(c.templates, tmpl)
	}
	item := map[string]interface{}{"name": k8sName, "environment": envName, "t": t}
	var collection string
	if len(skipped) > 0 {
		item["skipped"] = strings.Join(skipped, ",")
		collection = appendChunked(&c.skipGates, item, NodeSkipPermissionGates)
	} else {
		collection = appendChunked(&c.gates, item, NodePolicyGates)
	}
	c.collection[k8sName] = collection
	c.instances = append(c.instances, c.instance(tmpl, item, len(skipped) > 0))
	return k8sName, nil
}

// gateTemplateData is what every instance of gate copies: its spec and the
// labels that name the template.
//
// spec.overrides is deliberately not copied. kro server-side applies the
// template with force and re-applies it on drift, so a template-owned
// overrides list would revert every `kardinal override` patch on the live
// instance. The CLI records overrides on the instances instead.
func gateTemplateData(gate kardinalv1alpha1.PolicyGate) map[string]interface{} {
	// Propagate scope and applies-to from the gate template so that
	// `kardinal policy list` can show the correct scope (org/team) and
	// applies-to value on the instantiated PolicyGate CRs (#249).
	scope := gate.Labels["kardinal.io/scope"]
	if scope == "" {
		scope = "team"
	}
	// kardinal.io/gate-name holds the user-defined gate name from the
	// original PolicyGate template (e.g. "no-weekend-deploys"), so the UI can
	// deduplicate by the human-readable name. A gate that already carries the
	// label (cross-product case) keeps it.
	gateName := gate.Labels["kardinal.io/gate-name"]
	if gateName == "" {
		gateName = gate.Name
	}
	// generated marks the instance as kardinal's: it is never used as a
	// template, which is what lets its name be longer than 63 characters
	// (the PolicyGate CRD name rule).
	spec := map[string]interface{}{
		"expression":      gate.Spec.Expression,
		"message":         gate.Spec.Message,
		"recheckInterval": gate.Spec.RecheckInterval,
		"generated":       true,
	}
	// when is copied only when set: the CRD defaults it to post-deploy, and an
	// empty string would fail the enum. It is deprecated and has no effect
	// (#1323); the copy keeps the instance a faithful copy of the template.
	if gate.Spec.When != "" { //nolint:staticcheck // SA1019: copied unchanged, no behaviour depends on it
		spec["when"] = gate.Spec.When //nolint:staticcheck // SA1019: as above
	}
	return map[string]interface{}{
		"spec":              spec,
		"template":          gate.Name,
		"templateNamespace": gate.Namespace,
		"gateName":          gateName,
		"scope":             scope,
		"appliesTo":         gate.Labels["kardinal.io/applies-to"],
	}
}

// instanceLabels are the labels of a gate instance, as the collection
// templates render them.
func (c *gateCollections) instanceLabels(tmpl, item map[string]interface{}, skip bool) map[string]string {
	labels := map[string]string{
		// These labels allow `kardinal explain` and the PolicyGate reconciler
		// to query instances by pipeline, bundle, and environment.
		"kardinal.io/pipeline":      c.pipeline,
		"kardinal.io/bundle":        c.bundle,
		"kardinal.io/environment":   item["environment"].(string),
		"kardinal.io/gate-template": tmpl["template"].(string),
		// The template's namespace, which can differ from the Pipeline's (an
		// org policy namespace or spec.policyNamespaces): `kardinal policy
		// list` matches the instance to its template with it, and an org
		// gate reads metrics.* there.
		LabelGateTemplateNamespace: tmpl["templateNamespace"].(string),
		"kardinal.io/gate-name":    tmpl["gateName"].(string),
		"kardinal.io/scope":        tmpl["scope"].(string),
		"kardinal.io/applies-to":   tmpl["appliesTo"].(string),
	}
	if skip {
		labels[LabelGateType] = GateTypeSkipPermission
	}
	return labels
}

// instance is the PolicyGate kro creates from item.
func (c *gateCollections) instance(tmpl, item map[string]interface{}, skip bool) kardinalv1alpha1.PolicyGate {
	spec := tmpl["spec"].(map[string]interface{})
	g := kardinalv1alpha1.PolicyGate{
		TypeMeta: metav1.TypeMeta{APIVersion: "kardinal.io/v1alpha1", Kind: "PolicyGate"},
		ObjectMeta: metav1.ObjectMeta{
			Name:   item["name"].(string),
			Labels: c.instanceLabels(tmpl, item, skip),
		},
		Spec: kardinalv1alpha1.PolicyGateSpec{
			Expression:      spec["expression"].(string),
			Message:         spec["message"].(string),
			RecheckInterval: spec["recheckInterval"].(string),
			Generated:       true,
		},
	}
	if w, ok := spec["when"].(string); ok {
		g.Spec.When = w //nolint:staticcheck // SA1019: copied unchanged
	}
	if skip {
		g.Annotations = map[string]string{AnnotationSkippedEnvironments: item["skipped"].(string)}
	}
	return g
}

// readyCond is the CEL condition "the gate instance name is ready", on the
// collection that creates it.
func (c *gateCollections) readyCond(name string) string {
	return fmt.Sprintf("%s.exists(g, g.metadata.name == %s && g.?status.?ready.orValue(false) == true)",
		c.collection[name], celString(name))
}

// MaxCollectionItems is the most items the builder puts in one collection
// node: kro's default --rgd-max-collection-size, which also bounds a
// standalone Graph's forEach. More items go into further collection nodes
// (PolicyGates2, PolicyGates3, ...).
const MaxCollectionItems = 1000

// chunkID is the node ID (and data field suffix) of chunk i of base: base
// itself for the first chunk, then base2, base3, ...
func chunkID(base string, i int) string {
	if i == 0 {
		return base
	}
	return fmt.Sprintf("%s%d", base, i+1)
}

// appendChunked appends item to the last chunk of *chunks, starting a new
// chunk when it is full, and returns the node ID of the chunk it went to.
func appendChunked(chunks *[][]interface{}, item interface{}, base string) string {
	if n := len(*chunks); n == 0 || len((*chunks)[n-1]) >= MaxCollectionItems {
		*chunks = append(*chunks, nil)
	}
	last := len(*chunks) - 1
	(*chunks)[last] = append((*chunks)[last], item)
	return chunkID(base, last)
}

// collectionIDs are the node IDs of every gate collection node.
func (c *gateCollections) collectionIDs() []string {
	var ids []string
	for i := range c.gates {
		ids = append(ids, chunkID(NodePolicyGates, i))
	}
	for i := range c.skipGates {
		ids = append(ids, chunkID(NodeSkipPermissionGates, i))
	}
	return ids
}

// nodes returns the data node and the collection nodes, or nil when the
// Graph has no gates.
func (c *gateCollections) nodes() []GraphNode {
	if len(c.instances) == 0 {
		return nil
	}
	data := map[string]interface{}{"templates": c.templates}
	var out []GraphNode
	for i, items := range c.gates {
		field := chunkID("gates", i)
		data[field] = items
		out = append(out, c.collectionNode(chunkID(NodePolicyGates, i), field, false))
	}
	for i, items := range c.skipGates {
		field := chunkID("skipGates", i)
		data[field] = items
		out = append(out, c.collectionNode(chunkID(NodeSkipPermissionGates, i), field, true))
	}
	return append([]GraphNode{{ID: NodePolicyGateData, Def: data}}, out...)
}

// collectionNode is a collection that creates one PolicyGate per item of
// NodePolicyGateData's list field. Every field is set from the item or its
// template entry; kro owns exactly the fields rendered here.
func (c *gateCollections) collectionNode(id, field string, skip bool) GraphNode {
	tmpl := func(f string) string {
		return fmt.Sprintf("${%s.templates[%s.t].%s}", NodePolicyGateData, iterGate, f)
	}
	labels := map[string]interface{}{
		"kardinal.io/pipeline":      c.pipeline,
		"kardinal.io/bundle":        c.bundle,
		"kardinal.io/environment":   "${" + iterGate + ".environment}",
		"kardinal.io/gate-template": tmpl("template"),
		LabelGateTemplateNamespace:  tmpl("templateNamespace"),
		"kardinal.io/gate-name":     tmpl("gateName"),
		"kardinal.io/scope":         tmpl("scope"),
		"kardinal.io/applies-to":    tmpl("appliesTo"),
	}
	metadata := map[string]interface{}{
		"name":   "${" + iterGate + ".name}",
		"labels": labels,
	}
	if skip {
		labels[LabelGateType] = GateTypeSkipPermission
		metadata["annotations"] = map[string]interface{}{
			AnnotationSkippedEnvironments: "${" + iterGate + ".skipped}",
		}
	}
	return GraphNode{
		ID:      id,
		ForEach: []map[string]string{{iterGate: fmt.Sprintf("${%s.%s}", NodePolicyGateData, field)}},
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "PolicyGate",
			"metadata":   metadata,
			"spec":       tmpl("spec"),
		},
		// ReadyWhen is the UI/Graph health signal. The blocking itself is done
		// by the dependent PromotionStep's spec.requiredGates expression.
		ReadyWhen: []string{"${each.?status.?ready.orValue(false) == true}"},
	}
}
