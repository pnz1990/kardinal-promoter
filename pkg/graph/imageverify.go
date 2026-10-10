// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"

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
// Downstream steps need nothing: their upstream already waited. In the
// compact shape the PromotionSteps template renders
// spec.live.imageVerification itself and the root entries of the DAG carry
// the name (compact.go, compact_extras.go).
//
// Digests are required. Verifying a tag and then promoting the tag lets a
// different image be pushed under the tag in between; a selected image
// without a digest fails the Build.

// imageVerifyNodeID is the node ID of the ImageVerification node.
const imageVerifyNodeID = "imageVerify"

// digestPattern is a sha256 image digest.
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// fullCommitSHA is a full SHA-1 or SHA-256 git commit id.
var fullCommitSHA = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

// ImageVerificationName returns the ImageVerification name of a Bundle:
// "<pipeline>-<bundle>-verify-<hash>", where hash is a hash of the
// ImageVerification spec, cut to 253 characters. A policy change gives a new
// name, so kro creates a new ImageVerification (the spec is immutable) and
// the steps that have not consumed the old verdict wait for the new one.
func ImageVerificationName(pipeline, bundle, specHash string) string {
	preferred := pipeline + "-" + slugify(bundle) + "-verify-" + specHash
	return boundedName(preferred, isSlug(pipeline) && isSlug(bundle),
		nameKey("imageverification", pipeline, bundle, specHash), maxObjectNameLen)
}

// NormalizeRepository returns the canonical form of an image repository, so
// that names of one repository compare equal: the registry host lowercased,
// without :443, docker.io for Docker Hub (index.docker.io,
// registry-1.docker.io, or no host at all), and library/ for an official
// Docker Hub image ("nginx" is docker.io/library/nginx). A reference that
// does not parse is an error: image verification fails closed on it.
func NormalizeRepository(repository string) (string, error) {
	r := strings.TrimPrefix(strings.TrimPrefix(repository, "https://"), "http://")
	if i := strings.IndexByte(r, '/'); i > 0 && looksLikeHost(r[:i]) {
		r = strings.ToLower(r[:i]) + r[i:]
	}
	repo, err := name.NewRepository(r)
	if err != nil {
		return "", fmt.Errorf("image repository %q is not a valid reference: %w", repository, err)
	}
	return canonicalHost(repo.RegistryStr()) + "/" + repo.RepositoryStr(), nil
}

// looksLikeHost reports whether the first path segment of a reference is a
// registry host (as docker reads it): it has a dot or a port, or is
// localhost.
func looksLikeHost(seg string) bool {
	return strings.ContainsAny(seg, ".:") || strings.EqualFold(seg, "localhost")
}

func canonicalHost(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ":443")
	switch host {
	case name.DefaultRegistry, "registry-1.docker.io", "docker.io":
		return "docker.io"
	}
	return host
}

// normalizePattern is NormalizeRepository for an image pattern, whose "*"
// a reference parser refuses.
func normalizePattern(p string) string {
	p = strings.TrimPrefix(strings.TrimPrefix(p, "https://"), "http://")
	if p == "*" {
		return p
	}
	host, rest, ok := strings.Cut(p, "/")
	if !ok || !looksLikeHost(host) {
		host, rest = "docker.io", p
		if !strings.Contains(rest, "/") && !strings.Contains(rest, "*") {
			rest = "library/" + rest
		}
	}
	return canonicalHost(host) + "/" + rest
}

// imageSelected reports whether repository (normalized) matches one of the
// policy's image patterns ("*" matches any characters, "/" included); no
// pattern selects every image.
func imageSelected(patterns []string, repository string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		re := "^" + strings.ReplaceAll(regexp.QuoteMeta(normalizePattern(p)), `\*`, ".*") + "$"
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
			repo, err := NormalizeRepository(img.Repository)
			if err != nil {
				return nil, fmt.Errorf("build: spec.imageVerification: %w", err)
			}
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
		if !fullCommitSHA.MatchString(c.CommitSHA) {
			return nil, fmt.Errorf("build: spec.imageVerification.commits.requireSigned: configRef.commitSHA %q is not a full "+
				"40- or 64-character commit SHA; a signed commit is checked by its full SHA", c.CommitSHA)
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
	sum := sha256.Sum256(raw)
	ivName := ImageVerificationName(pipeline.Name, bundle.Name, hex.EncodeToString(sum[:])[:8])
	return &GraphNode{
		ID: imageVerifyNodeID,
		Template: map[string]interface{}{
			"apiVersion": "kardinal.io/v1alpha1",
			"kind":       "ImageVerification",
			"metadata": map[string]interface{}{
				"name": ivName,
				"labels": map[string]interface{}{
					"kardinal.io/pipeline": pipeline.Name,
					"kardinal.io/bundle":   bundle.Name,
				},
			},
			"spec": literalStrings(body),
		},
		ReadyWhen: []string{fmt.Sprintf(`${%s.?status.?phase.orValue("") == %q}`,
			imageVerifyNodeID, kardinalv1alpha1.ImageVerificationVerified)},
	}, ivName, nil
}

// imageVerifiedCond is the condition "the Bundle's images are verified"; it
// holds from then on (Verified is terminal).
func imageVerifiedCond() string {
	return fmt.Sprintf(`%s.?status.?phase.orValue("") == %q`, imageVerifyNodeID, kardinalv1alpha1.ImageVerificationVerified)
}

// imageVerificationLive is the mirror's spec.live.imageVerification: the
// ImageVerification's name, phase and message, and the images it verifies
// ("repository@digest"), which the step compares with the Bundle's before
// it promotes.
func imageVerificationLive() map[string]interface{} {
	return map[string]interface{}{
		"name":    fmt.Sprintf(`${%s.metadata.name}`, imageVerifyNodeID),
		"phase":   fmt.Sprintf(`${%s.?status.?phase.orValue("Pending")}`, imageVerifyNodeID),
		"message": fmt.Sprintf(`${%s.?status.?message.orValue("")}`, imageVerifyNodeID),
		"images":  fmt.Sprintf(`${%s.spec.?images.orValue([]).map(i, i.repository + "@" + i.digest)}`, imageVerifyNodeID),
	}
}
