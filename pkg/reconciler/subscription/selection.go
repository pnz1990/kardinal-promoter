// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package subscription

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// defaultImageDiscoveryLimit is the discoveryLimit an image Subscription
// without one uses (source.OCIWatcher's default).
const defaultImageDiscoveryLimit = 50

// selectionKey identifies what picks the tracked tag (or chart version): the
// fields of spec.image or spec.helm that choose among the tags, with defaults
// filled in and tag lists sorted, so an edit that does not change the choice
// keeps the key. Empty for a Git Subscription (pathGlob has its own baseline,
// status.observedPathGlob).
func selectionKey(sub *kardinalv1alpha1.Subscription) string {
	var sel struct {
		Strategy       kardinalv1alpha1.TagSelectionStrategy `json:"strategy,omitempty"`
		DiscoveryLimit int32                                 `json:"discoveryLimit,omitempty"`
		kardinalv1alpha1.TagSelection
	}
	switch {
	case sub.Spec.Type == kardinalv1alpha1.SubscriptionTypeImage && sub.Spec.Image != nil:
		sel.TagSelection = sub.Spec.Image.TagSelection
		sel.Strategy = sub.Spec.Image.Strategy
		if sel.Strategy == "" {
			sel.Strategy = kardinalv1alpha1.TagStrategyAuto
		}
		sel.DiscoveryLimit = sub.Spec.Image.DiscoveryLimit
		if sel.DiscoveryLimit <= 0 {
			sel.DiscoveryLimit = defaultImageDiscoveryLimit
		}
	case sub.Spec.Type == kardinalv1alpha1.SubscriptionTypeHelm && sub.Spec.Helm != nil:
		sel.TagSelection = sub.Spec.Helm.TagSelection
	default:
		return ""
	}
	sel.AllowTags = sortedSet(sel.AllowTags)
	sel.IgnoreTags = sortedSet(sel.IgnoreTags)
	raw, err := json.Marshal(sel)
	if err != nil { // strings, ints and string lists always marshal
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// sortedSet returns the tags sorted without duplicates, nil for none.
func sortedSet(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := slices.Clone(tags)
	slices.Sort(out)
	return slices.Compact(out)
}
