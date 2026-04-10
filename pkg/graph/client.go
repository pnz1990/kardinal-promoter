// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rs/zerolog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// GraphClient handles Graph CR CRUD operations via the Kubernetes dynamic client.
// It does NOT import any kro Go module — it uses the dynamic client with GraphGVR.
type GraphClient struct {
	dynamic dynamic.Interface
	log     zerolog.Logger
}

// NewGraphClient creates a new GraphClient.
func NewGraphClient(dyn dynamic.Interface, log zerolog.Logger) *GraphClient {
	return &GraphClient{
		dynamic: dyn,
		log:     log,
	}
}

// Create creates a Graph CR in the given namespace.
// Idempotent: returns nil if the Graph already exists (AlreadyExists).
func (c *GraphClient) Create(ctx context.Context, g *Graph) error {
	u, err := toUnstructured(g)
	if err != nil {
		return fmt.Errorf("graph.Create: marshal: %w", err)
	}
	ns := g.Namespace
	_, createErr := c.dynamic.Resource(GraphGVR).Namespace(ns).Create(ctx, u, metav1.CreateOptions{})
	if createErr != nil {
		if isAlreadyExists(createErr) {
			zerolog.Ctx(ctx).Debug().
				Str("graph", g.Name).
				Str("namespace", ns).
				Msg("graph already exists, skipping create")
			return nil
		}
		return fmt.Errorf("graph.Create %s/%s: %w", ns, g.Name, createErr)
	}
	zerolog.Ctx(ctx).Info().
		Str("graph", g.Name).
		Str("namespace", ns).
		Int("nodes", len(g.Spec.Nodes)).
		Msg("graph created")
	return nil
}

// Get retrieves a Graph CR by name and namespace.
func (c *GraphClient) Get(ctx context.Context, namespace, name string) (*Graph, error) {
	u, err := c.dynamic.Resource(GraphGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("graph.Get %s/%s: %w", namespace, name, err)
	}
	g, err := fromUnstructured(u)
	if err != nil {
		return nil, fmt.Errorf("graph.Get %s/%s: unmarshal: %w", namespace, name, err)
	}
	return g, nil
}

// Delete deletes a Graph CR. Returns nil if the Graph is not found.
func (c *GraphClient) Delete(ctx context.Context, namespace, name string) error {
	err := c.dynamic.Resource(GraphGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("graph.Delete %s/%s: %w", namespace, name, err)
	}
	zerolog.Ctx(ctx).Info().
		Str("graph", name).
		Str("namespace", namespace).
		Msg("graph deleted")
	return nil
}

// List lists all Graph CRs in a namespace matching the given labels.
func (c *GraphClient) List(ctx context.Context, namespace string,
	matchLabels map[string]string) ([]*Graph, error) {
	selector := labelsToSelector(matchLabels)
	list, err := c.dynamic.Resource(GraphGVR).Namespace(namespace).List(ctx,
		metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("graph.List %s: %w", namespace, err)
	}
	result := make([]*Graph, 0, len(list.Items))
	for i := range list.Items {
		g, err := fromUnstructured(&list.Items[i])
		if err != nil {
			return nil, fmt.Errorf("graph.List %s: item %d unmarshal: %w", namespace, i, err)
		}
		result = append(result, g)
	}
	return result, nil
}

// --- marshaling helpers ---

// toUnstructured converts a Graph to an unstructured.Unstructured for the dynamic client.
func toUnstructured(g *Graph) (*unstructured.Unstructured, error) {
	data, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: obj}, nil
}

// fromUnstructured converts an unstructured.Unstructured to a Graph.
func fromUnstructured(u *unstructured.Unstructured) (*Graph, error) {
	data, err := json.Marshal(u.Object)
	if err != nil {
		return nil, err
	}
	var g Graph
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// labelsToSelector converts a label map to a label selector string.
func labelsToSelector(labels map[string]string) string {
	var parts []string
	for k, v := range labels {
		parts = append(parts, k+"="+v)
	}
	// Sort for determinism
	sortStrings(parts)
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += ","
		}
		result += p
	}
	return result
}

// --- Kubernetes error helpers (avoid importing apierrors to keep deps minimal) ---

func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return hasStatusCode(err, 409)
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return hasStatusCode(err, 404)
}

func hasStatusCode(err error, code int) bool {
	// Most robust: check if error wraps a StatusError with ErrStatus().Code
	type statusCodeErr interface {
		ErrStatus() metav1.Status
	}
	if se, ok := err.(statusCodeErr); ok {
		return int(se.ErrStatus().Code) == code
	}
	// Fallback: check via string (less reliable but avoids apierrors import)
	s := err.Error()
	switch code {
	case 404:
		return contains(s, "not found") || contains(s, "404")
	case 409:
		return contains(s, "already exists") || contains(s, "409")
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
