// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package translator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"strings"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/graph"
)

// BuildError is the error Translate returns when the Graph builder refuses
// its input (graph.ErrInvalid is in the chain). Gates are the PolicyGate
// templates the build was given, so a caller can record exactly what failed
// (the Bundle reconciler's GraphBuildFailed retry, #1312).
type BuildError struct {
	Err   error
	Gates []kardinalv1alpha1.PolicyGate
}

func (e *BuildError) Error() string { return e.Err.Error() }

// Unwrap returns the wrapped build error.
func (e *BuildError) Unwrap() error { return e.Err }

// ApplicableGates returns the gates whose kardinal.io/applies-to names one
// of pipeline's environments (fleet targets included), in namespace and name
// order. A gate of other environments (an org gate for other Pipelines) is
// left out.
func ApplicableGates(pipeline *kardinalv1alpha1.Pipeline, gates []kardinalv1alpha1.PolicyGate) []kardinalv1alpha1.PolicyGate {
	if pipeline == nil {
		return nil
	}
	envs := make(map[string]bool, len(pipeline.Spec.Environments))
	for _, name := range graph.EnvironmentNames(pipeline) { // fleet targets included: a gate can name one
		envs[name] = true
	}
	var out []kardinalv1alpha1.PolicyGate
	for i := range gates {
		g := &gates[i]
		if slices.ContainsFunc(strings.Split(g.Labels["kardinal.io/applies-to"], ","),
			func(e string) bool { return envs[strings.TrimSpace(e)] }) {
			out = append(out, *g)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// GatesHash returns the SHA-256 hex hash of the gates that apply to
// pipeline's environments (ApplicableGates): namespace, name, generation,
// labels and spec of each, and graph.RenderVersion. The generation is in it
// so that an edit reverted before a Bundle saw it still changes the hash:
// the Bundle's gate instances then get the template's new generation
// (#2145). The render version is in it so that a controller that renders
// Graphs differently re-translates every Promoting Bundle once (#2271).
func GatesHash(pipeline *kardinalv1alpha1.Pipeline, gates []kardinalv1alpha1.PolicyGate) string {
	if pipeline == nil {
		return ""
	}
	type entry struct {
		Namespace  string                          `json:"namespace"`
		Name       string                          `json:"name"`
		Generation int64                           `json:"generation,omitempty"`
		Labels     map[string]string               `json:"labels,omitempty"`
		Spec       kardinalv1alpha1.PolicyGateSpec `json:"spec"`
	}
	entries := []entry{}
	for _, g := range ApplicableGates(pipeline, gates) {
		entries = append(entries, entry{Namespace: g.Namespace, Name: g.Name, Generation: g.Generation, Labels: g.Labels, Spec: g.Spec})
	}
	// The render version too: a controller that renders Graphs differently
	// re-translates the Promoting Bundles' Graphs once (graph.RenderVersion).
	data, err := json.Marshal(struct {
		Render int     `json:"render"`
		Gates  []entry `json:"gates"`
	}{graph.RenderVersion, entries})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
