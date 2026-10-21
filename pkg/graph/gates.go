// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
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
	// NodeApprovalGates creates one PolicyGate per approvalGates item: the
	// instances of gates that read the Bundle's Approvals (approvals.go).
	NodeApprovalGates = "ApprovalGates"
	// NodeVerifyGates creates one PolicyGate per verifyGates item: the
	// instances of gates that read the Bundle's Verdicts (verify.go), with
	// their Approvals when they read those too.
	NodeVerifyGates = "VerifyGates"
	// NodeApprovalReadGates and NodeVerifyOnlyGates are, in a Graph where a
	// gate has spec.approval, the approval gates without one (they only read
	// approvals.*) and the verify gates without one. ApprovalGates and
	// VerifyGates then render spec.approval, and these do not: a collection
	// renders it for all its items or for none. kro renders an optional with
	// no value as null (unless its alpha CELOmitFunction gate is on), and the
	// CRD refuses a null spec.approval, so such an instance in a mixed
	// collection was never created (#2271, ledger G28). A Graph where no gate
	// has spec.approval keeps every gate where it was.
	NodeApprovalReadGates = "ApprovalReadGates"
	NodeVerifyOnlyGates   = "VerifyOnlyGates"
	// NodePRStatusData holds one entry per environment's PRStatus.
	NodePRStatusData = "PRStatusData"
	// NodePRStatuses creates one PRStatus per NodePRStatusData item.
	NodePRStatuses = "PRStatuses"

	iterGate = "Gate"
	iterPR   = "PR"
)

// RenderVersion is the version of how the builder renders a Graph from the
// same inputs. Every Graph carries it (LabelRenderVersion, with the value
// RenderLabel), and the Bundle reconciler re-translates a Promoting Bundle's
// Graph that carries another (or none) once: a version stuck under a
// rendering bug picks up the fix on upgrade. A Graph whose rendering did not
// change re-renders identically, which kro applies as a no-op. Verified,
// failed and superseded Bundles keep the Graph they were promoted with. The
// check is equality, not "older than": a controller downgraded to an earlier
// RenderVersion re-renders a newer controller's Graph once with its own
// rendering, which is right, since the running controller owns the
// rendering of the Graphs it serves.
//
// Bump it with every change that renders an existing Bundle's Graph
// differently (node IDs, templates, data), and say so in the changelog
// fragment; a change that leaves every existing Graph identical needs none.
// Release branches: see RenderLabel.
// TestRenderCorpus pins the corpus Graphs' hashes to it
// (testdata/render-corpus.json) and fails when one renders differently at
// the same version.
//
//   - 2: the gate collections render spec.approval for all their items or
//     for none (#2271). Graphs before it carry no label.
//   - 3: v0.12.3 (release/v0.12's #2271 backport) labels its Graphs 2, and
//     main renders differently from it: the upgrade from v0.12.3 re-renders
//     them once too, as from v0.12.0 to v0.12.2 (#2346).
const RenderVersion = 3

// RenderLabel is the LabelRenderVersion value this controller writes on every
// Graph and compares, for equality, with the one a Graph carries. On main it
// is RenderVersion in decimal (TestRenderLabel).
//
// A release-branch backport that changes the rendering must set a value main
// never uses: main's RenderVersion at the branch's release with the patch
// release, for example "3-v0.12.4". It must never take main's next integer.
// v0.12.3 did, labelling its Graphs 2 while main rendered differently under
// 2, and so the upgrade from it to main re-rendered nothing (#2346).
const RenderLabel = "3"

// LabelRenderVersion is the label of a Graph that holds the RenderVersion it
// was rendered with.
const LabelRenderVersion = "kardinal.io/render-version"

// The annotations that stamp a gate instance with the template it was copied
// from (#1360), as evidence of the rule a Bundle was held to: the template's
// metadata.generation and the digest of its rule (TemplateDigest). They
// change when the template is edited and the Bundle's Graph is re-translated.
const (
	AnnotationGateTemplateGeneration = "kardinal.io/gate-template-generation"
	AnnotationGateTemplateDigest     = "kardinal.io/gate-template-digest"
	// AnnotationGateTemplateUID is the template's metadata.uid, which a
	// promotion receipt records with the generation and digest (#1951).
	AnnotationGateTemplateUID = "kardinal.io/gate-template-uid"
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
	// orgNS are the org policy namespaces: a template there is an org gate
	// (scopeOf).
	orgNS     map[string]bool
	templates []interface{}
	index     map[string]int
	// gates, skipGates, approvalGates, approvalReadGates, verifyGates and
	// verifyOnlyGates hold the items of each collection node, in chunks of
	// at most MaxCollectionItems (kro's default forEach limit).
	gates, skipGates, approvalGates, approvalReadGates [][]interface{}
	verifyGates, verifyOnlyGates                       [][]interface{}
	instances                                          []kardinalv1alpha1.PolicyGate
	// collection is the collection node of each instance name.
	collection map[string]string
	what       map[string]string
	// items is the item of each instance name, and position its index in
	// instances.
	items    map[string]map[string]interface{}
	position map[string]int
	// approvals is whether the Graph has the Approvals selector ref, and
	// agentRuns whether it has AgentRuns (refAgentRuns); namespace and
	// bundleUID are the Bundle's.
	approvals, agentRuns bool
	namespace, bundleUID string
	// approvalPolicies is whether a gate of the Graph has spec.approval
	// (set before the first add: it decides where a gate goes). kro types
	// the templates list from its data, so spec.?approval compiles only when
	// one of them has the field.
	approvalPolicies bool
}

func newGateCollections(pipeline, bundle string, policyNamespaces []string) *gateCollections {
	return &gateCollections{pipeline: pipeline, bundle: bundle, orgNS: policyNamespaceSet(policyNamespaces),
		index: map[string]int{}, collection: map[string]string{}, what: map[string]string{},
		items: map[string]map[string]interface{}{}, position: map[string]int{}}
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
	tmpl := gateTemplateData(gate, c.orgNS)
	if !ok {
		t = len(c.templates)
		c.index[key] = t
		c.templates = append(c.templates, tmpl)
	}
	item := map[string]interface{}{"name": k8sName, "environment": envName, "t": t}
	var collection string
	switch {
	case len(skipped) > 0 && needsApprovals(gate):
		return "", fmt.Errorf("build: %s is a skip-permission gate with spec.approval or approvals.*; "+
			"approvals are not supported on skip-permission gates, so it would never pass: remove them", what)
	case len(skipped) > 0 && needsVerdicts(gate):
		return "", fmt.Errorf("build: %s is a skip-permission gate with spec.verify; "+
			"verify is not supported on skip-permission gates, so it would never pass: remove it", what)
	case len(skipped) > 0:
		item["skipped"] = strings.Join(skipped, ",")
		collection = appendChunked(&c.skipGates, item, NodeSkipPermissionGates)
	case needsVerdicts(gate):
		// A verify gate that reads approvals too copies them (every item of
		// the collection says whether it does, so the field is always there).
		item["readsApprovals"] = needsApprovals(gate)
		if c.approvalPolicies && gate.Spec.Approval == nil {
			collection = appendChunked(&c.verifyOnlyGates, item, NodeVerifyOnlyGates)
		} else {
			collection = appendChunked(&c.verifyGates, item, NodeVerifyGates)
		}
	case needsApprovals(gate) && c.approvalPolicies && gate.Spec.Approval == nil:
		collection = appendChunked(&c.approvalReadGates, item, NodeApprovalReadGates)
	case needsApprovals(gate):
		collection = appendChunked(&c.approvalGates, item, NodeApprovalGates)
	default:
		collection = appendChunked(&c.gates, item, NodePolicyGates)
	}
	c.collection[k8sName] = collection
	c.items[k8sName] = item
	c.position[k8sName] = len(c.instances)
	c.instances = append(c.instances, c.instance(tmpl, item, len(skipped) > 0))
	return k8sName, nil
}

// setVerify gives instance name its verify policy (verifyPolicyData): per
// item, because the verified environment depends on the instance's
// environment.
func (c *gateCollections) setVerify(name string, data map[string]interface{}) {
	c.items[name]["verify"] = data
	c.instances[c.position[name]].Spec.Verify = verifyPolicyFromData(data)
}

// gateTemplateData is what every instance of gate copies: its spec and the
// labels that name the template.
//
// spec.overrides is deliberately not copied. kro server-side applies the
// template with force and re-applies it on drift, so a template-owned
// overrides list would revert every `kardinal override` patch on the live
// instance. The CLI records overrides on the instances instead.
//
// TemplateRules compares the spec this returns across controller versions
// (a re-render on upgrade records what changed under a version in flight):
// a change to its keys or values for the same template would record a false
// GateChanged for every promoting version at that upgrade. TestTemplateDataShape
// pins them.
func gateTemplateData(gate kardinalv1alpha1.PolicyGate, orgNS map[string]bool) map[string]interface{} {
	// Propagate scope and applies-to from the gate template so that
	// `kardinal policy list` can show the correct scope (org/team) and
	// applies-to value on the instantiated PolicyGate CRs (#249). The scope
	// is the one the gate behaves with (GateScope, #1720).
	scope := scopeOf(gate, orgNS)
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
	if p := approvalPolicyData(gate); p != nil {
		spec["approval"] = p
	}
	return map[string]interface{}{
		"spec":              spec,
		"generation":        strconv.FormatInt(gate.Generation, 10),
		"uid":               string(gate.UID),
		"digest":            TemplateDigest(gate),
		"template":          gate.Name,
		"templateNamespace": gate.Namespace,
		"gateName":          gateName,
		"scope":             scope,
		"appliesTo":         gate.Labels["kardinal.io/applies-to"],
	}
}

// TemplateDigest is the AnnotationGateTemplateDigest of gate's instances:
// "sha256:" and the hex SHA-256 of the JSON of the template's spec without
// the fields that are not the rule: spec.overrides (recorded on each
// instance, never copied) and the instance-only approvals and verdicts. An
// edit to the template's overrides or labels keeps the digest.
func TemplateDigest(gate kardinalv1alpha1.PolicyGate) string {
	spec := gate.Spec
	spec.Overrides, spec.Approvals, spec.Verdicts = nil, nil, nil
	data, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
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
	if p, ok := spec["approval"].(map[string]interface{}); ok {
		g.Spec.Approval = approvalPolicyFromData(p)
	}
	g.Annotations = map[string]string{
		AnnotationGateTemplateGeneration: tmpl["generation"].(string),
		AnnotationGateTemplateUID:        tmpl["uid"].(string),
		AnnotationGateTemplateDigest:     tmpl["digest"].(string),
	}
	if skip {
		g.Annotations[AnnotationSkippedEnvironments] = item["skipped"].(string)
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
	for i := range c.approvalGates {
		ids = append(ids, chunkID(NodeApprovalGates, i))
	}
	for i := range c.approvalReadGates {
		ids = append(ids, chunkID(NodeApprovalReadGates, i))
	}
	for i := range c.verifyGates {
		ids = append(ids, chunkID(NodeVerifyGates, i))
	}
	for i := range c.verifyOnlyGates {
		ids = append(ids, chunkID(NodeVerifyOnlyGates, i))
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
	for i, items := range c.approvalGates {
		field := chunkID("approvalGates", i)
		data[field] = items
		out = append(out, c.approvalCollectionNode(chunkID(NodeApprovalGates, i), field, c.approvalPolicies))
	}
	for i, items := range c.approvalReadGates {
		field := chunkID("approvalReadGates", i)
		data[field] = items
		out = append(out, c.approvalCollectionNode(chunkID(NodeApprovalReadGates, i), field, false))
	}
	for i, items := range c.verifyGates {
		field := chunkID("verifyGates", i)
		data[field] = items
		out = append(out, c.verifyCollectionNode(chunkID(NodeVerifyGates, i), field, c.approvalPolicies))
	}
	for i, items := range c.verifyOnlyGates {
		field := chunkID("verifyOnlyGates", i)
		data[field] = items
		out = append(out, c.verifyCollectionNode(chunkID(NodeVerifyOnlyGates, i), field, false))
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
	annotations := map[string]interface{}{
		AnnotationGateTemplateGeneration: tmpl("generation"),
		AnnotationGateTemplateUID:        tmpl("uid"),
		AnnotationGateTemplateDigest:     tmpl("digest"),
	}
	metadata := map[string]interface{}{
		"name":        "${" + iterGate + ".name}",
		"labels":      labels,
		"annotations": annotations,
	}
	if skip {
		labels[LabelGateType] = GateTypeSkipPermission
		annotations[AnnotationSkippedEnvironments] = "${" + iterGate + ".skipped}"
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

// approvalPolicyData is gate's spec.approval as Graph data, or nil.
func approvalPolicyData(gate kardinalv1alpha1.PolicyGate) map[string]interface{} {
	p := gate.Spec.Approval
	if p == nil {
		return nil
	}
	policy := map[string]interface{}{"excludeAuthor": p.ExcludeAuthor, "required": int64(max(p.Required, 1)),
		"allowedUsers": toInterfaces(p.AllowedUsers), "allowedGroups": toInterfaces(p.AllowedGroups),
		// Every field approvalsOfItem reads is in every template entry: kro
		// types the PolicyGateData node from its data, and a field no entry
		// has is an undefined field at compile time, optional or not.
		"allowAgents": p.AllowAgents, "allowedAgents": toInterfaces(p.AllowedAgents)}
	// requiredWhen and fields only when set: the collection copies the
	// policy whole and reads no field of it.
	if p.RequiredWhen != "" {
		policy["requiredWhen"] = p.RequiredWhen
	}
	if len(p.Fields) > 0 {
		policy["fields"] = approvalFieldsData(p.Fields)
	}
	return policy
}

// approvalFieldsData is approval.fields as Graph data. An optional key is
// left out when empty: the instance's CRD rules refuse, for example, an
// empty pattern on a bool field.
func approvalFieldsData(fields []kardinalv1alpha1.ApprovalField) []interface{} {
	out := make([]interface{}, len(fields))
	for i, f := range fields {
		typ := f.Type
		if typ == "" {
			typ = kardinalv1alpha1.ApprovalFieldString
		}
		m := map[string]interface{}{"name": f.Name, "type": typ, "required": f.Required}
		for k, v := range map[string]string{"pattern": f.Pattern, "mustBe": f.MustBe, "description": f.Description} {
			if v != "" {
				m[k] = v
			}
		}
		if len(f.Values) > 0 {
			m["values"] = toInterfaces(f.Values)
		}
		out[i] = m
	}
	return out
}

// approvalPolicyFromData is the inverse of approvalPolicyData.
func approvalPolicyFromData(p map[string]interface{}) *kardinalv1alpha1.GateApprovalPolicy {
	out := &kardinalv1alpha1.GateApprovalPolicy{}
	if n, ok := p["required"].(int64); ok {
		out.Required = int(n)
	}
	out.ExcludeAuthor, _ = p["excludeAuthor"].(bool)
	for _, u := range p["allowedUsers"].([]interface{}) {
		out.AllowedUsers = append(out.AllowedUsers, u.(string))
	}
	for _, g := range p["allowedGroups"].([]interface{}) {
		out.AllowedGroups = append(out.AllowedGroups, g.(string))
	}
	out.AllowAgents, _ = p["allowAgents"].(bool)
	if agents, ok := p["allowedAgents"].([]interface{}); ok {
		for _, a := range agents {
			out.AllowedAgents = append(out.AllowedAgents, a.(string))
		}
	}
	out.RequiredWhen, _ = p["requiredWhen"].(string)
	fields, _ := p["fields"].([]interface{})
	for _, raw := range fields {
		m, _ := raw.(map[string]interface{})
		f := kardinalv1alpha1.ApprovalField{}
		f.Name, _ = m["name"].(string)
		f.Type, _ = m["type"].(string)
		f.Required, _ = m["required"].(bool)
		f.Pattern, _ = m["pattern"].(string)
		f.MustBe, _ = m["mustBe"].(string)
		f.Description, _ = m["description"].(string)
		values, _ := m["values"].([]interface{})
		for _, v := range values {
			if s, ok := v.(string); ok {
				f.Values = append(f.Values, s)
			}
		}
		out.Fields = append(out.Fields, f)
	}
	return out
}

// approvalCollectionNode is the collection of approval gate instances. It
// renders the spec field by field, unlike collectionNode, because spec.approvals
// is not template data: it is the live list of the Bundle's Approvals for the
// item's environment, read from the ApprovalsNodeID selector ref. Every field
// read is present in every template entry (approvalPolicyData always sets the
// four policy fields; when is read optionally), so the collection never goes
// data-pending (K3, G11).
func (c *gateCollections) approvalCollectionNode(id, field string, policy bool) GraphNode {
	n := c.collectionNode(id, field, false)
	spec := c.liveSpec(policy)
	spec["approvals"] = approvalsOfItem(c.approvalPolicies)
	n.Template["spec"] = spec
	return n
}

// verifyCollectionNode is the collection of verify gate instances: like
// approvalCollectionNode, with the item's verify policy (setVerify) and the
// live list of the Bundle's Verdicts for its check, read from the
// VerdictsNodeID selector ref (verdictsOfItem), and the Approvals for the
// items that read them.
func (c *gateCollections) verifyCollectionNode(id, field string, policy bool) GraphNode {
	n := c.collectionNode(id, field, false)
	spec := c.liveSpec(policy)
	if c.approvals {
		inner := strings.TrimSuffix(strings.TrimPrefix(approvalsOfItem(c.approvalPolicies), "${"), "}")
		spec["approvals"] = "${" + iterGate + ".readsApprovals ? (" + inner + ") : []}"
	}
	spec["verify"] = "${" + iterGate + ".verify}"
	spec["verdicts"] = verdictsOfItem(c.pipeline, c.namespace, c.bundleUID, c.agentRuns)
	n.Template["spec"] = spec
	return n
}

// liveSpec is the template-copied part of a live gate instance's spec, with
// spec.approval when policy: the collection's items all have one. kro
// renders an optional with no value as null, which the CRD refuses, so a
// collection of items without a policy must not render the field (#2271).
func (c *gateCollections) liveSpec(policy bool) map[string]interface{} {
	tmpl := func(f string) string {
		return fmt.Sprintf("${%s.templates[%s.t].spec.%s}", NodePolicyGateData, iterGate, f)
	}
	spec := map[string]interface{}{
		"expression":      tmpl("expression"),
		"message":         tmpl("message"),
		"recheckInterval": tmpl("recheckInterval"),
		"generated":       true,
	}
	if policy {
		spec["approval"] = fmt.Sprintf("${%s.templates[%s.t].spec.?approval}", NodePolicyGateData, iterGate)
	}
	return spec
}

// approvalsOfItem is the spec.approvals of an approval gate instance: the
// specs of the Approvals for the item's environment and for this very Bundle
// (spec.bundleUID == the Bundle's UID, so an Approval of an earlier Bundle
// with the same name never counts), from approvers the gate's policy allows
// (allowedUsers or allowedGroups, anyone when both are empty; an agent in
// allowedAgents when allowAgents is true), sorted by name, at most
// maxCopiedApprovals of them. Filtering before the cap means Approvals from
// people who may not approve cannot crowd out the ones that count. The gate
// blocks when it gets more than it counts (policygate maxGateApprovals).
// The PolicyGate reconciler applies the remaining rules (agents counted only
// with the opt-in, the one-party rule; 18-actor-policy sections 4 and 5).
func approvalsOfItem(policies bool) string {
	policy := fmt.Sprintf("%s.templates[%s.t].spec.?approval", NodePolicyGateData, iterGate)
	users := policy + ".?allowedUsers.orValue([])"
	groups := policy + ".?allowedGroups.orValue([])"
	agents := policy + ".?allowedAgents.orValue([])"
	allowAgents := policy + ".?allowAgents.orValue(false)"
	eligible := fmt.Sprintf("((size(%[1]s) == 0 && size(%[2]s) == 0) || a.spec.user in %[1]s || "+
		"a.spec.?groups.orValue([]).exists(g, g in %[2]s) || "+
		"(%[4]s && a.spec.?actor.?kind.orValue('') == 'agent' && a.spec.?actor.?agent.orValue('') in %[3]s))",
		users, groups, agents, allowAgents)
	if !policies {
		// No template has spec.approval: everyone's approval is copied
		// (spec.?approval would not compile, see approvalPolicies).
		eligible = "true"
	}
	list := fmt.Sprintf("%s.filter(a, a.spec.environment == %s.environment && a.spec.bundleUID == bundle.metadata.uid && %s)"+
		".sortBy(a, a.metadata.name).map(a, a.spec)", ApprovalsNodeID, iterGate, eligible)
	return fmt.Sprintf("${size(%[1]s) > %[2]d ? %[1]s.slice(0, %[2]d) : %[1]s}", list, maxCopiedApprovals)
}

// maxCopiedApprovals caps spec.approvals (the CRD's maxItems).
const maxCopiedApprovals = 101

// TemplateStamp is what a Graph's gate instances copied from one PolicyGate
// template: its digest and generation (AnnotationGateTemplateDigest and
// AnnotationGateTemplateGeneration).
type TemplateStamp struct {
	Digest     string
	Generation int64
}

// TemplateRules returns, by "namespace/name" of each template, the rule g's
// gate instances copy from it, read from its NodePolicyGateData node: the
// JSON of the template entry's spec (expression, message, recheck interval,
// approval policy) and of the verify policies of its instances. It reads
// what every controller since v0.12.0 writes there, so the rule a Graph of
// an earlier controller copied can be compared with the one it copies
// after a re-render (#2271): a template edited after the version started
// changes it, a template stamp alone does not.
func TemplateRules(g *Graph) map[string]string {
	out := map[string]string{}
	if g == nil {
		return out
	}
	for _, n := range g.Spec.Nodes {
		if n.ID != NodePolicyGateData {
			continue
		}
		templates, _ := n.Def["templates"].([]interface{})
		keys := make([]string, len(templates))
		specs := make([]string, len(templates))
		verify := make([]map[string]bool, len(templates))
		for i, raw := range templates {
			t, _ := raw.(map[string]interface{})
			name, _ := t["template"].(string)
			ns, _ := t["templateNamespace"].(string)
			keys[i] = ns + "/" + name
			spec, _ := json.Marshal(t["spec"])
			specs[i] = string(spec)
			verify[i] = map[string]bool{}
		}
		for field, raw := range n.Def {
			items, ok := raw.([]interface{})
			if !ok || field == "templates" {
				continue
			}
			for _, it := range items {
				item, _ := it.(map[string]interface{})
				v, has := item["verify"]
				if !has {
					continue
				}
				idx, err := strconv.Atoi(fmt.Sprint(item["t"]))
				if err != nil || idx < 0 || idx >= len(templates) {
					continue
				}
				data, _ := json.Marshal(v)
				verify[idx][string(data)] = true
			}
		}
		for i, key := range keys {
			var vs []string
			for v := range verify[i] {
				vs = append(vs, v)
			}
			sort.Strings(vs)
			out[key] = specs[i] + " verify " + strings.Join(vs, ",")
		}
	}
	return out
}

// TemplateEnvironments returns, by "namespace/name" of each template, the
// environments of g's version its gate instances are in, sorted: what a
// kardinal.io/applies-to edit changes without changing the rule. It reads the
// items of NodePolicyGateData as every controller since v0.12.0 writes them.
func TemplateEnvironments(g *Graph) map[string][]string {
	out := map[string][]string{}
	if g == nil {
		return out
	}
	for _, n := range g.Spec.Nodes {
		if n.ID != NodePolicyGateData {
			continue
		}
		templates, _ := n.Def["templates"].([]interface{})
		keys := make([]string, len(templates))
		envs := make([]map[string]bool, len(templates))
		for i, raw := range templates {
			t, _ := raw.(map[string]interface{})
			name, _ := t["template"].(string)
			ns, _ := t["templateNamespace"].(string)
			keys[i] = ns + "/" + name
			envs[i] = map[string]bool{}
		}
		for field, raw := range n.Def {
			items, ok := raw.([]interface{})
			if !ok || field == "templates" {
				continue
			}
			for _, it := range items {
				item, _ := it.(map[string]interface{})
				idx, err := strconv.Atoi(fmt.Sprint(item["t"]))
				env, _ := item["environment"].(string)
				if err != nil || idx < 0 || idx >= len(templates) || env == "" {
					continue
				}
				envs[idx][env] = true
			}
		}
		for i, key := range keys {
			list := make([]string, 0, len(envs[i]))
			for e := range envs[i] {
				list = append(list, e)
			}
			sort.Strings(list)
			out[key] = list
		}
	}
	return out
}

// TemplateStamps returns, by "namespace/name" of each template, the stamp
// g's gate instances carry, read from its NodePolicyGateData node. A Graph
// built before the stamps (#1360) has entries with an empty stamp; one with
// no gates has none.
func TemplateStamps(g *Graph) map[string]TemplateStamp {
	out := map[string]TemplateStamp{}
	if g == nil {
		return out
	}
	for _, n := range g.Spec.Nodes {
		if n.ID != NodePolicyGateData {
			continue
		}
		templates, _ := n.Def["templates"].([]interface{})
		for _, raw := range templates {
			t, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := t["template"].(string)
			ns, _ := t["templateNamespace"].(string)
			digest, _ := t["digest"].(string)
			gen, _ := strconv.ParseInt(fmt.Sprint(t["generation"]), 10, 64)
			out[ns+"/"+name] = TemplateStamp{Digest: digest, Generation: gen}
		}
	}
	return out
}
