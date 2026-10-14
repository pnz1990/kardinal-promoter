// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// dynClient is a dynamic.Interface over the scenario's controller-runtime
// client, so the Graph client, the health adapters and the argocd strategy
// read and write the same store as the reconcilers.
type dynClient struct {
	c     client.Client
	kinds map[schema.GroupVersionResource]string
}

var _ dynamic.Interface = (*dynClient)(nil)

func newDynClient(c client.Client, kinds map[schema.GroupVersionResource]string) *dynClient {
	return &dynClient{c: c, kinds: kinds}
}

func (d *dynClient) Resource(gvr schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &dynResource{d: d, gvr: gvr}
}

type dynResource struct {
	d   *dynClient
	gvr schema.GroupVersionResource
	ns  string
}

func (r *dynResource) Namespace(ns string) dynamic.ResourceInterface {
	return &dynResource{d: r.d, gvr: r.gvr, ns: ns}
}

func (r *dynResource) gvk() (schema.GroupVersionKind, error) {
	kind, ok := r.d.kinds[r.gvr]
	if !ok {
		return schema.GroupVersionKind{}, fmt.Errorf("scenario dynamic client: resource %s is not registered", r.gvr)
	}
	return r.gvr.GroupVersion().WithKind(kind), nil
}

func (r *dynResource) empty() (*unstructured.Unstructured, error) {
	gvk, err := r.gvk()
	if err != nil {
		return nil, err
	}
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u, nil
}

func (r *dynResource) Create(ctx context.Context, obj *unstructured.Unstructured, _ metav1.CreateOptions, sub ...string) (*unstructured.Unstructured, error) {
	if len(sub) > 0 {
		return nil, fmt.Errorf("scenario dynamic client: create subresource %v", sub)
	}
	o := obj.DeepCopy()
	if o.GetNamespace() == "" {
		o.SetNamespace(r.ns)
	}
	if err := r.d.c.Create(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

func (r *dynResource) Update(ctx context.Context, obj *unstructured.Unstructured, _ metav1.UpdateOptions, sub ...string) (*unstructured.Unstructured, error) {
	o := obj.DeepCopy()
	if o.GetNamespace() == "" {
		o.SetNamespace(r.ns)
	}
	var err error
	if len(sub) == 1 && sub[0] == "status" {
		err = r.d.c.Status().Update(ctx, o)
	} else {
		err = r.d.c.Update(ctx, o)
	}
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (r *dynResource) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return r.Update(ctx, obj, opts, "status")
}

func (r *dynResource) Delete(ctx context.Context, name string, opts metav1.DeleteOptions, _ ...string) error {
	u, err := r.empty()
	if err != nil {
		return err
	}
	u.SetNamespace(r.ns)
	u.SetName(name)
	var dopts []client.DeleteOption
	if opts.PropagationPolicy != nil {
		dopts = append(dopts, client.PropagationPolicy(*opts.PropagationPolicy))
	}
	if opts.Preconditions != nil {
		dopts = append(dopts, client.Preconditions(*opts.Preconditions))
	}
	return r.d.c.Delete(ctx, u, dopts...)
}

func (r *dynResource) DeleteCollection(context.Context, metav1.DeleteOptions, metav1.ListOptions) error {
	return fmt.Errorf("scenario dynamic client: DeleteCollection is not supported")
}

func (r *dynResource) Get(ctx context.Context, name string, _ metav1.GetOptions, sub ...string) (*unstructured.Unstructured, error) {
	if len(sub) > 0 {
		return nil, fmt.Errorf("scenario dynamic client: get subresource %v", sub)
	}
	u, err := r.empty()
	if err != nil {
		return nil, err
	}
	if err := r.d.c.Get(ctx, client.ObjectKey{Namespace: r.ns, Name: name}, u); err != nil {
		return nil, err
	}
	return u, nil
}

func (r *dynResource) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	gvk, err := r.gvk()
	if err != nil {
		return nil, err
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
	var lopts []client.ListOption
	if r.ns != "" {
		lopts = append(lopts, client.InNamespace(r.ns))
	}
	if opts.LabelSelector != "" {
		sel, err := labels.Parse(opts.LabelSelector)
		if err != nil {
			return nil, fmt.Errorf("label selector: %w", err)
		}
		lopts = append(lopts, client.MatchingLabelsSelector{Selector: sel})
	}
	if opts.FieldSelector != "" {
		return nil, fmt.Errorf("scenario dynamic client: field selectors are not supported (%s)", opts.FieldSelector)
	}
	if err := r.d.c.List(ctx, list, lopts...); err != nil {
		return nil, err
	}
	return list, nil
}

func (r *dynResource) Watch(context.Context, metav1.ListOptions) (watch.Interface, error) {
	return nil, fmt.Errorf("scenario dynamic client: Watch is not supported")
}

func (r *dynResource) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, _ metav1.PatchOptions, sub ...string) (*unstructured.Unstructured, error) {
	u, err := r.empty()
	if err != nil {
		return nil, err
	}
	u.SetNamespace(r.ns)
	u.SetName(name)
	if pt == types.StrategicMergePatchType {
		// Unstructured objects take merge patches; the fields kardinal
		// patches merge the same way.
		pt = types.MergePatchType
	}
	patch := client.RawPatch(pt, data)
	switch {
	case len(sub) == 1 && sub[0] == "status":
		err = r.d.c.Status().Patch(ctx, u, patch)
	case len(sub) == 0:
		err = r.d.c.Patch(ctx, u, patch)
	default:
		return nil, fmt.Errorf("scenario dynamic client: patch subresource %s", strings.Join(sub, "/"))
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (r *dynResource) Apply(context.Context, string, *unstructured.Unstructured, metav1.ApplyOptions, ...string) (*unstructured.Unstructured, error) {
	return nil, fmt.Errorf("scenario dynamic client: Apply is not supported")
}

func (r *dynResource) ApplyStatus(context.Context, string, *unstructured.Unstructured, metav1.ApplyOptions) (*unstructured.Unstructured, error) {
	return nil, fmt.Errorf("scenario dynamic client: ApplyStatus is not supported")
}
