// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Batched writes (spec.git.writes: batched) in the compact shape
// (docs/design/30-batched-branch-writes.md).
//
// A batched environment's step runs await-write instead of cloning,
// committing and pushing itself: it asks in its own status
// (status.writeRequest) and reads the answer from its spec.live.writes. The
// Graph has one BranchWrite per Bundle and repository branch (one template
// node each: a plain template, so one that cannot apply holds no other
// branch, G11), whose spec lists the environments it writes (fixed at the
// build) and the steps that asked (rendered from NodeStepsObserved: the
// Promoting steps of this BranchWrite with a request). Its reconciler writes
// each round and only its status; the PromotionSteps template mirrors the
// item's result back from refBranchWrites, as it does a RenderRun's
// (compact_render.go). The BranchWrite exists from the Graph's start: it is
// idle until a step asks, so a later change never prunes a write in flight.

// Node IDs of the compact shape's BranchWrites.
const (
	// NodeBranchWrites is the first BranchWrite's template node; further
	// branches get chunkID(NodeBranchWrites, i).
	NodeBranchWrites = "BranchWrites"
	// refBranchWritesNodeID is the selector ref that reads the Bundle's
	// BranchWrites back.
	refBranchWritesNodeID = "refBranchWrites"
)

// BranchWriteName returns the BranchWrite name of bundle's writes to branch
// of repository url: "<bundle>-write", cut to fit 63 characters, then "-" and
// a hash of the parts.
func BranchWriteName(bundle, url, branch string) string {
	return boundedName(slugify(bundle)+"-write", false,
		nameKey("branchwrite", bundle, strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git")), branch),
		validation.DNS1123LabelMaxLength)
}

// compactWriteName is env's BranchWrite for bundle, or "" when env writes its
// own change.
func compactWriteName(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle, env kardinalv1alpha1.EnvironmentSpec) string {
	if !kardinalv1alpha1.BatchedWrite(pipeline.Spec, env, bundle.Spec.Type) {
		return ""
	}
	return BranchWriteName(bundle.Name, pipeline.Spec.Git.URL, writeBranch(pipeline))
}

// writeBranch is the branch a Pipeline's batched environments push to: the
// base branch (batching is for directory-layout environments).
func writeBranch(pipeline *kardinalv1alpha1.Pipeline) string {
	if pipeline.Spec.Git.Branch != "" {
		return pipeline.Spec.Git.Branch
	}
	return "main"
}

// writesAny reports whether any of envs is batched for bundle.
func writesAny(pipeline *kardinalv1alpha1.Pipeline, envs []string, bundle *kardinalv1alpha1.Bundle) bool {
	for _, name := range envs {
		if kardinalv1alpha1.BatchedWrite(pipeline.Spec, findEnvSpec(pipeline, name), bundle.Spec.Type) {
			return true
		}
	}
	return false
}

// compactWriteNodes returns one BranchWrite template node per BranchWrite
// the steps name, or nil when none is batched.
func compactWriteNodes(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle, steps []compactStep) []GraphNode {
	var names []string
	envsOf := map[string][]interface{}{}
	for _, s := range steps {
		if s.write == "" {
			continue
		}
		if _, ok := envsOf[s.write]; !ok {
			names = append(names, s.write)
		}
		envsOf[s.write] = append(envsOf[s.write], map[string]interface{}{"name": s.env, "step": s.name})
	}
	nodes := make([]GraphNode, 0, len(names))
	for i, name := range names {
		requested := fmt.Sprintf(`${%s.filter(s, s.?spec.?write.orValue("") == %s && s.?status.?writeRequest.orValue(0) > 0 && `+
			`s.?status.?state.orValue("") == "Promoting").map(s, {"step": s.metadata.name, "uid": s.metadata.uid, `+
			`"request": s.status.writeRequest})}`, NodeStepsObserved, strconv.Quote(name))
		nodes = append(nodes, GraphNode{
			ID: chunkID(NodeBranchWrites, i),
			Template: map[string]interface{}{
				"apiVersion": "kardinal.io/v1alpha1",
				"kind":       "BranchWrite",
				"metadata": map[string]interface{}{
					"name": name,
					"labels": map[string]interface{}{
						"kardinal.io/pipeline": pipeline.Name,
						"kardinal.io/bundle":   bundle.Name,
						LabelBundleUID:         string(bundle.UID),
					},
				},
				"spec": map[string]interface{}{
					"pipelineName": pipeline.Name,
					"bundleName":   bundle.Name,
					"url":          pipeline.Spec.Git.URL,
					"branch":       writeBranch(pipeline),
					"environments": envsOf[name],
					"requested":    requested,
				},
			},
		})
	}
	return nodes
}

// compactLiveWrites is the compact PromotionSteps template's
// spec.live.writes: the item's BranchWrite (write, "" for an environment
// that writes its own change), applied by kro for this Bundle, with its
// result for the item's environment once it has one.
func compactLiveWrites(bundle *kardinalv1alpha1.Bundle) string {
	env := iterStep + ".environment"
	res := func(field, zero string) string {
		return fmt.Sprintf(`"%s": w.status.environments[%s].?%s.orValue(%s)`, field, env, field, zero)
	}
	record := "{" + strings.Join([]string{res("step", `""`), res("uid", `""`), res("request", "0"), res("round", "0"),
		res("commitSHA", `""`), res("noChanges", "false"), res("error", `""`), res("cancelled", "false"),
		res("newerBundle", `""`)}, ", ") + "}"
	return fmt.Sprintf(`${%s.filter(w, w.metadata.name == %s.write && `+
		`w.metadata.?labels[?%q].hasValue() && w.metadata.?labels[?%q].orValue("") == %s && `+
		`w.?status.?environments[?%s].hasValue()).map(w, {"name": w.metadata.name, "result": %s})}`,
		refBranchWritesNodeID, iterStep, LabelKRONodeID, LabelBundleUID, strconv.Quote(string(bundle.UID)), env, record)
}
