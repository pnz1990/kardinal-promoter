// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"strings"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// deployedLabel is what explain and status print for the Bundle deployed in
// an environment (lifecycle.DeployedBundle): "<bundle> (<version>)", the
// Bundle alone when it has no version or is gone, and "none" when no change
// has landed there.
func deployedLabel(name string, byName map[string]*v1alpha1.Bundle) string {
	if name == "" {
		return "none"
	}
	if v := bundleVersion(byName[name]); v != "" {
		return name + " (" + v + ")"
	}
	return name
}

// bundleVersion names what a Bundle ships: its image tags (a digest when an
// image has no tag), then "config <sha>" for the Git commit of a config or
// mixed Bundle. It is "" for a nil Bundle or one with neither.
func bundleVersion(b *v1alpha1.Bundle) string {
	if b == nil {
		return ""
	}
	var parts []string
	for _, img := range b.Spec.Images {
		switch {
		case img.Tag != "":
			parts = append(parts, img.Tag)
		case img.Digest != "":
			parts = append(parts, shortSHA(img.Digest))
		}
	}
	if b.Spec.ConfigRef != nil && b.Spec.ConfigRef.CommitSHA != "" {
		parts = append(parts, "config "+shortSHA(b.Spec.ConfigRef.CommitSHA))
	}
	return strings.Join(parts, ", ")
}

// shortSHA shortens a Git SHA or an image digest ("sha256:<hex>") to 7 hex
// characters, as git log --oneline does.
func shortSHA(s string) string {
	prefix := ""
	if i := strings.Index(s, ":"); i >= 0 {
		prefix, s = s[:i+1], s[i+1:]
	}
	if len(s) > 7 {
		s = s[:7]
	}
	return prefix + s
}

// deployedBundles returns lifecycle.DeployedBundle for each of envs.
func deployedBundles(steps []v1alpha1.PromotionStep, pipeline string, envs []string) map[string]string {
	out := make(map[string]string, len(envs))
	for _, env := range envs {
		out[env] = lifecycle.DeployedBundle(steps, pipeline, env)
	}
	return out
}
