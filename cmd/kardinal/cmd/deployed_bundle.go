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

// deployedEnv is what runs in an environment (lifecycle.DeployedEnv).
type deployedEnv struct {
	bundle     string
	configFrom string
	imagesFrom string
}

// deployedBundles returns the deployedEnv of each of envs. byName holds the
// pipeline's Bundles by name: their types say what each one deployed.
func deployedBundles(steps []v1alpha1.PromotionStep, pipeline string, envs []string,
	byName map[string]*v1alpha1.Bundle) map[string]deployedEnv {
	out := make(map[string]deployedEnv, len(envs))
	for _, env := range envs {
		d := lifecycle.DeployedIn(steps, pipeline, env, byName)
		out[env] = deployedEnv{bundle: d.Bundle, configFrom: d.ConfigFrom, imagesFrom: d.ImagesFrom}
	}
	return out
}

// deployedLabelOf is deployedLabel of d.bundle, followed by where the rest
// comes from: "; config <sha> from <bundle>" under an image Bundle, or
// "; images <tags> from <bundle>" under a config Bundle.
func deployedLabelOf(d deployedEnv, byName map[string]*v1alpha1.Bundle) string {
	label := deployedLabel(d.bundle, byName)
	if b := byName[d.configFrom]; b != nil {
		label += "; config " + shortSHA(b.Spec.ConfigRef.CommitSHA) + " from " + d.configFrom
	}
	if b := byName[d.imagesFrom]; b != nil {
		label += "; images " + bundleVersion(&v1alpha1.Bundle{Spec: v1alpha1.BundleSpec{Images: b.Spec.Images}}) +
			" from " + d.imagesFrom
	}
	return label
}
