// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package actor

import (
	"context"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Source reads the agent index a stamp is derived from.
type Source struct {
	// Reader reads AgentIndexes.
	Reader client.Reader
	// Name is the AgentIndex to read: the controller's own (its release).
	// Empty reads the first ready AgentIndex by name, as the CLI does: every
	// release builds the same index from the same AgentIdentities.
	Name string
}

// Index returns the agent index, or nil when there is none: no AgentIndex
// (the controller has not built it, or the CRD is not installed) means no
// agents.
func (s *Source) Index(ctx context.Context) (*v1alpha1.AgentIndexStatus, error) {
	if s == nil || s.Reader == nil {
		return nil, nil
	}
	if s.Name != "" {
		var idx v1alpha1.AgentIndex
		if err := s.Reader.Get(ctx, client.ObjectKey{Name: s.Name}, &idx); err != nil {
			if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("read agent index %s: %w", s.Name, err)
		}
		return &idx.Status, nil
	}
	var list v1alpha1.AgentIndexList
	if err := s.Reader.List(ctx, &list); err != nil {
		if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list agent indexes: %w", err)
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	for i := range list.Items {
		if meta.IsStatusConditionTrue(list.Items[i].Status.Conditions, "Ready") {
			return &list.Items[i].Status, nil
		}
	}
	return nil, nil
}

// Resolve derives u's actor and checks the agent's scope at w.
func (s *Source) Resolve(ctx context.Context, u User, w Where) (Resolved, error) {
	idx, err := s.Index(ctx)
	if err != nil {
		return Resolved{}, err
	}
	r, err := Derive(u, idx)
	if err != nil {
		return Resolved{}, err
	}
	if err := Scope(r.Entry, w); err != nil {
		return r, err
	}
	return r, nil
}
