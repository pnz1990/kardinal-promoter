// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rs/zerolog"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
)

// ErrGraphOwnedByOther is returned by Create when a Graph with the desired
// name exists but is controlled by another object (another Bundle, or none).
// Create never takes over such a Graph.
var ErrGraphOwnedByOther = errors.New("graph exists and is controlled by another owner")

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

// Create creates a Graph CR in the given namespace, or updates the spec,
// labels and ownerReferences of an existing one in place. It returns
// ErrGraphOwnedByOther, and changes nothing, when the existing Graph is not
// controlled by the same Bundle (UID) as g. Update conflicts with kro's own
// writes are retried with a fresh read.
//
// kro reconciles an updated Graph by applying the new desired state and
// pruning only the resources whose nodes were removed, so PromotionSteps that
// are already Verified keep their status. Deleting and recreating the Graph
// would instead remove every child and restart the promotion
// (docs/design/16-graph-capability-ledger.md G6).
func (c *GraphClient) Create(ctx context.Context, g *Graph) error {
	u, err := toUnstructured(g)
	if err != nil {
		return fmt.Errorf("graph.Create: marshal: %w", err)
	}
	ns := g.Namespace
	res := c.dynamic.Resource(GraphGVR).Namespace(ns)
	_, createErr := res.Create(ctx, u, metav1.CreateOptions{})
	if createErr == nil {
		zerolog.Ctx(ctx).Info().
			Str("graph", g.Name).
			Str("namespace", ns).
			Int("nodes", len(g.Spec.Nodes)).
			Msg("graph created")
		return nil
	}
	if !apierrors.IsAlreadyExists(createErr) {
		return fmt.Errorf("graph.Create %s/%s: %w", ns, g.Name, createErr)
	}

	// kro writes the Graph's status and finalizers, so an update can race with
	// it; retry on optimistic-lock conflicts with a fresh read.
	updated := false
	err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		existing, getErr := res.Get(ctx, g.Name, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("get existing: %w", getErr)
		}
		if ownErr := checkController(existing, u); ownErr != nil {
			return ownErr
		}
		// Merge labels so labels other controllers set on the Graph survive.
		labels := existing.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		for k, v := range u.GetLabels() {
			labels[k] = v
		}
		if equality.Semantic.DeepEqual(existing.Object["spec"], u.Object["spec"]) &&
			equality.Semantic.DeepEqual(existing.GetLabels(), labels) &&
			equality.Semantic.DeepEqual(existing.GetOwnerReferences(), u.GetOwnerReferences()) {
			return nil
		}
		existing.Object["spec"] = u.Object["spec"]
		existing.SetLabels(labels)
		existing.SetOwnerReferences(u.GetOwnerReferences())
		if _, updErr := res.Update(ctx, existing, metav1.UpdateOptions{}); updErr != nil {
			return updErr
		}
		updated = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("graph.Create %s/%s: update: %w", ns, g.Name, err)
	}
	if !updated {
		zerolog.Ctx(ctx).Debug().
			Str("graph", g.Name).
			Str("namespace", ns).
			Msg("graph already up to date")
		return nil
	}
	zerolog.Ctx(ctx).Info().
		Str("graph", g.Name).
		Str("namespace", ns).
		Int("nodes", len(g.Spec.Nodes)).
		Msg("graph updated in place")
	return nil
}

// checkController returns ErrGraphOwnedByOther unless existing is controlled
// by the same object (UID) as desired. Graph names are derived from the
// Pipeline and Bundle names, so a name clash must never re-parent another
// Bundle's Graph or overwrite a Graph a user created.
func checkController(existing, desired *unstructured.Unstructured) error {
	want := metav1.GetControllerOfNoCopy(desired)
	have := metav1.GetControllerOfNoCopy(existing)
	if want == nil || have == nil || have.UID != want.UID {
		owner := "none"
		if have != nil {
			owner = fmt.Sprintf("%s/%s (uid %s)", have.Kind, have.Name, have.UID)
		}
		return fmt.Errorf("%w: controller is %s", ErrGraphOwnedByOther, owner)
	}
	return nil
}

// Get retrieves a Graph CR by name and namespace.
func (c *GraphClient) Get(ctx context.Context, namespace, name string) (*Graph, error) {
	u, err := c.dynamic.Resource(GraphGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("graph.Get %s/%s: %w", namespace, name, err)
	}
	g, err := FromUnstructured(u)
	if err != nil {
		return nil, fmt.Errorf("graph.Get %s/%s: unmarshal: %w", namespace, name, err)
	}
	return g, nil
}

// GraphExists returns true if a Graph CR with the given name exists in the given namespace.
// Returns false (not an error) when the Graph is not found (HTTP 404).
// Used by the Bundle reconciler to detect external Graph deletions (#490).
func (c *GraphClient) GraphExists(ctx context.Context, namespace, name string) (bool, error) {
	_, err := c.dynamic.Resource(GraphGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("graph.GraphExists %s/%s: %w", namespace, name, err)
	}
	return true, nil
}

// List lists the Graph CRs in a namespace that kardinal generated (those
// carrying the kardinal.io/bundle label).
func (c *GraphClient) List(ctx context.Context, namespace string) ([]*Graph, error) {
	list, err := c.dynamic.Resource(GraphGVR).Namespace(namespace).List(ctx,
		metav1.ListOptions{LabelSelector: "kardinal.io/bundle"})
	if err != nil {
		return nil, fmt.Errorf("graph.List %s: %w", namespace, err)
	}
	result := make([]*Graph, 0, len(list.Items))
	for i := range list.Items {
		g, err := FromUnstructured(&list.Items[i])
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

// FromUnstructured converts an unstructured.Unstructured to a Graph (a JSON round trip).
func FromUnstructured(u *unstructured.Unstructured) (*Graph, error) {
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
