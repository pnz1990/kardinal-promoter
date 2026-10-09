// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Image signature verification (docs/image-verification.md, #1456).
//
// When the Pipeline sets spec.imageVerification, the Graph gets one
// ImageVerification node (an owned kardinal CRD) with the Bundle's selected
// images, pinned by digest, and the policy copied in. It has no gating: it
// is created with the Graph, and its reconciler checks the signatures in the
// registry (and a config Bundle's commit through the SCM API). Every step
// with no upstream in the Graph carries spec.imageVerification and waits in
// Pending until the mirror patch node copies phase Verified onto
// spec.live.imageVerification; its pre-deploy hooks wait for it too.
// Downstream steps need nothing: their upstream already waited.
//
// Digests are required. Verifying a tag and then promoting the tag lets a
// different image be pushed under the tag in between; a selected image
// without a digest fails the Build.

// imageVerifyNodeID is the node ID of the ImageVerification node.
const imageVerifyNodeID = "imageVerify"

// digestPattern is a sha256 image digest.
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// ImageVerificationName returns the ImageVerification name of a Bundle:
// "<pipeline>-<bundle>-verify", hash-suffixed when it would lose
// characters or exceed 253 characters.
func ImageVerificationName(pipeline, bundle string) string {
	preferred := pipeline + "-" + slugify(bundle) + "-verify"
	return boundedName(preferred, isSlug(pipeline) && isSlug(bundle),
		nameKey("imageverification", pipeline, bundle), maxObjectNameLen)
}

// imageSelected reports whether repository matches one of the policy's
// image patterns ("*" matches any characters, "/" included); no pattern
// selects every image.
func imageSelected(patterns []string, repository string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		re := "^" + strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, ".*") + "$"
		if ok, _ := regexp.MatchString(re, repository); ok {
			return true
		}
	}
	return false
}

// imageVerificationSpec returns the ImageVerification spec for bundle under
// the Pipeline's policy, or nil when the policy selects nothing to verify.
func imageVerificationSpec(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle) (*kardinalv1alpha1.ImageVerificationSpec, error) {
	policy := pipeline.Spec.ImageVerification
	if policy == nil {
		return nil, nil
	}
	spec := &kardinalv1alpha1.ImageVerificationSpec{
		PipelineName: pipeline.Name,
		BundleName:   bundle.Name,
		Policy:       *policy.DeepCopy(),
	}
	spec.Policy.Images = nil
	if len(policy.Authorities) > 0 {
		for _, img := range bundle.Spec.Images {
			repo := strings.TrimPrefix(strings.TrimPrefix(img.Repository, "https://"), "http://")
			if !imageSelected(policy.Images, repo) {
				continue
			}
			if !digestPattern.MatchString(img.Digest) {
				ref := repo
				if img.Tag != "" {
					ref += ":" + img.Tag
				}
				return nil, fmt.Errorf("build: spec.imageVerification: image %s is not pinned by digest; "+
					"a verified image must be promoted by digest (create the Bundle with --image %s@sha256:...)", ref, repo)
			}
			spec.Images = append(spec.Images, kardinalv1alpha1.VerifiedImage{Repository: repo, Digest: img.Digest})
		}
	}
	if policy.Commits != nil && policy.Commits.RequireSigned && bundle.Spec.ConfigRef != nil {
		c := bundle.Spec.ConfigRef
		if c.CommitSHA == "" {
			return nil, fmt.Errorf("build: spec.imageVerification.commits.requireSigned: the config Bundle has no configRef.commitSHA")
		}
		repo := c.GitRepo
		if repo == "" {
			repo = pipeline.Spec.Git.URL
		}
		spec.Commit = &kardinalv1alpha1.VerifiedCommit{Repo: repo, SHA: c.CommitSHA}
	}
	if len(spec.Images) == 0 && spec.Commit == nil {
		return nil, nil
	}
	return spec, nil
}

// buildImageVerificationNode returns the ImageVerification node of bundle,
// and its name; nil when there is nothing to verify.
func buildImageVerificationNode(pipeline *kardinalv1alpha1.Pipeline, bundle *kardinalv1alpha1.Bundle) (*GraphNode, string, error) {
	spec, err := imageVerificationSpec(pipeline, bundle)
	if err != nil || spec == nil {
		return nil, "", err
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, "", fmt.Errorf("build: spec.imageVerification: %w", err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, "", fmt.Errorf("build: spec.imageVerification: %w", err)
	}
	name := ImageVerificationName(pipeline.Name, bundle.Name)
	return &GraphNode{
		ID: imageVerifyNodeID,
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "ImageVerification",
			"metadata": map[string]interface{}{
				"name": name,
				"labels": map[string]interface{}{
					"kardinal.io/pipeline": pipeline.Name,
					"kardinal.io/bundle":   bundle.Name,
				},
			},
			"spec": literalStrings(body),
		},
		ReadyWhen: []string{fmt.Sprintf(`${%s.?status.?phase.orValue("") == %q}`,
			imageVerifyNodeID, kardinalv1alpha1.ImageVerificationVerified)},
	}, name, nil
}

// imageVerifiedCond is the condition "the Bundle's images are verified"; it
// holds from then on (Verified is terminal).
func imageVerifiedCond() string {
	return fmt.Sprintf(`%s.?status.?phase.orValue("") == %q`, imageVerifyNodeID, kardinalv1alpha1.ImageVerificationVerified)
}

// imageVerificationLive is the mirror's spec.live.imageVerification.
func imageVerificationLive() map[string]interface{} {
	return map[string]interface{}{
		"phase":   fmt.Sprintf(`${%s.?status.?phase.orValue("Pending")}`, imageVerifyNodeID),
		"message": fmt.Sprintf(`${%s.?status.?message.orValue("")}`, imageVerifyNodeID),
	}
}
