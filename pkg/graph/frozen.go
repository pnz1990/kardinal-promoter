// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"slices"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// annotationFrozenDigest carries, on a template FrozenTemplates rebuilt, the
// digest its instances had (AnnotationGateTemplateDigest): the copy in the
// Graph holds the template's rule, not every field TemplateDigest hashes.
// Build honors it only for the templates BuildInput.FrozenDigests names and
// removes it from every other gate, so a template a person wrote cannot set
// its own digest.
const annotationFrozenDigest = "kardinal.io/frozen-template-digest"

// FrozenTemplates rebuilds, from g's PolicyGateData node, the PolicyGate
// templates of namespaces that g's instances were made from, and their
// digests by namespace/name (#2286): a version in flight whose Pipeline names
// a namespace that no longer agrees to share its PolicyGates keeps the gates
// it started with, without the controller reading that namespace again.
//
// A template is what its instances copied: its name and namespace, generation
// and UID, the applies-to, scope and gate-name labels, and the spec fields
// the Graph renders (expression, message, recheckInterval, approval, and,
// from its instances, verify). Each instance of a verify gate records the
// environment it verifies; when its instances name one environment the
// template names it, when they name several (a gate whose verify environment
// is relative, its environment's upstream) it names none.
func FrozenTemplates(g *Graph, namespaces []string) ([]kardinalv1alpha1.PolicyGate, map[string]string, error) {
	if g == nil || len(namespaces) == 0 {
		return nil, nil, nil
	}
	var templates []interface{}
	var items []map[string]interface{}
	for _, n := range g.Spec.Nodes {
		if n.ID != NodePolicyGateData {
			continue
		}
		templates, _ = n.Def["templates"].([]interface{})
		for field, raw := range n.Def {
			list, ok := raw.([]interface{})
			if !ok || field == "templates" {
				continue
			}
			for _, it := range list {
				if item, ok := it.(map[string]interface{}); ok {
					items = append(items, item)
				}
			}
		}
	}
	var out []kardinalv1alpha1.PolicyGate
	digests := map[string]string{}
	for t, raw := range templates {
		tmpl, ok := raw.(map[string]interface{})
		if !ok {
			return nil, nil, fmt.Errorf("frozen templates: PolicyGateData template %d is not an object", t)
		}
		str := func(k string) string { s, _ := tmpl[k].(string); return s }
		ns, name := str("templateNamespace"), str("template")
		if !slices.Contains(namespaces, ns) {
			continue
		}
		gate := kardinalv1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(str("uid")),
			Labels: map[string]string{"kardinal.io/applies-to": str("appliesTo"), "kardinal.io/scope": str("scope")}}}
		if gen, err := strconv.ParseInt(str("generation"), 10, 64); err == nil {
			gate.Generation = gen
		}
		if gn := str("gateName"); gn != "" && gn != name {
			gate.Labels["kardinal.io/gate-name"] = gn
		}
		spec, _ := tmpl["spec"].(map[string]interface{})
		gate.Spec.Expression, _ = spec["expression"].(string)
		gate.Spec.Message, _ = spec["message"].(string)
		gate.Spec.RecheckInterval, _ = spec["recheckInterval"].(string)
		if p, ok := spec["approval"].(map[string]interface{}); ok {
			gate.Spec.Approval = approvalPolicyFromData(asBuilt(p).(map[string]interface{}))
		}
		var verified []string
		for _, item := range items {
			if index(item["t"]) != t {
				continue
			}
			v, ok := item["verify"].(map[string]interface{})
			if !ok {
				continue
			}
			p := verifyPolicyFromData(asBuilt(v).(map[string]interface{}))
			if gate.Spec.Verify == nil {
				gate.Spec.Verify = p
			}
			if !slices.Contains(verified, p.Environment) {
				verified = append(verified, p.Environment)
			}
		}
		if gate.Spec.Verify != nil && len(verified) > 1 {
			gate.Spec.Verify.Environment = ""
		}
		digest := str("digest")
		digests[ns+"/"+name] = digest
		gate.Annotations = map[string]string{annotationFrozenDigest: digest}
		out = append(out, gate)
	}
	return out, digests, nil
}

// asBuilt is v, Graph data, with its numbers as the builder wrote them
// (int64): read back from the API server, JSON gives a float64 for each,
// which approvalPolicyFromData and verifyPolicyFromData do not take.
func asBuilt(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, e := range x {
			out[k] = asBuilt(e)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, e := range x {
			out[i] = asBuilt(e)
		}
		return out
	case float64:
		if x == float64(int64(x)) {
			return int64(x)
		}
	case int:
		return int64(x)
	}
	return v
}

// index is a PolicyGateData item's template index (t): an int when the
// builder made it, an int64 or a float64 once read back from the API server.
func index(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return -1
}

// withFrozenDigests is gates with annotationFrozenDigest only on those
// digests names (namespace/name), set to its digest: removed from every
// other gate, which a person wrote.
func withFrozenDigests(gates []kardinalv1alpha1.PolicyGate, digests map[string]string) []kardinalv1alpha1.PolicyGate {
	out := make([]kardinalv1alpha1.PolicyGate, len(gates))
	for i, g := range gates {
		out[i] = g
		d, frozen := digests[g.Namespace+"/"+g.Name]
		if _, set := g.Annotations[annotationFrozenDigest]; !set && !frozen {
			continue
		}
		ann := map[string]string{}
		for k, v := range g.Annotations {
			if k != annotationFrozenDigest {
				ann[k] = v
			}
		}
		if frozen {
			ann[annotationFrozenDigest] = d
		}
		out[i].Annotations = ann
	}
	return out
}
