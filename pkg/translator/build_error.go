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

// GatesHash returns the SHA-256 hex hash of the gates that apply to
// pipeline's environments: namespace, name, labels and spec of each gate
// whose kardinal.io/applies-to names one of them, in namespace and name
// order. A gate of other environments (an org gate for other Pipelines) is
// left out, so changing it does not change the hash.
func GatesHash(pipeline *kardinalv1alpha1.Pipeline, gates []kardinalv1alpha1.PolicyGate) string {
	if pipeline == nil {
		return ""
	}
	envs := make(map[string]bool, len(pipeline.Spec.Environments))
	for _, e := range pipeline.Spec.Environments {
		envs[e.Name] = true
	}
	type entry struct {
		Namespace string                          `json:"namespace"`
		Name      string                          `json:"name"`
		Labels    map[string]string               `json:"labels,omitempty"`
		Spec      kardinalv1alpha1.PolicyGateSpec `json:"spec"`
	}
	entries := []entry{}
	for i := range gates {
		g := &gates[i]
		if !slices.ContainsFunc(strings.Split(g.Labels["kardinal.io/applies-to"], ","),
			func(e string) bool { return envs[strings.TrimSpace(e)] }) {
			continue
		}
		entries = append(entries, entry{Namespace: g.Namespace, Name: g.Name, Labels: g.Labels, Spec: g.Spec})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Namespace != entries[j].Namespace {
			return entries[i].Namespace < entries[j].Namespace
		}
		return entries[i].Name < entries[j].Name
	})
	data, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
