// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// readCache serves the reconcilers' typed reads of kardinal objects from
// memory, as the manager's informer cache does, instead of the fake
// client's JSON round trip per object and read. It is never stale: every
// write through the World's client drops what it changed (invalidate), so
// the next read takes the stored object. Reads it cannot serve
// (unstructured objects, other field selectors) go to the client.
type readCache struct {
	client.Client
	mu sync.Mutex
	// objs holds the typed objects by kind/namespace/name.
	objs map[string]k8sruntime.Object
	// lists records the kind/namespace (or kind/* for every namespace)
	// whose objects objs holds completely, and their keys.
	lists map[string]map[string]bool
	// indexes are the field indexes the World's client has.
	indexes map[string]func(client.Object) []string
}

func newReadCache(c client.Client) *readCache {
	return &readCache{Client: c, objs: map[string]k8sruntime.Object{}, lists: map[string]map[string]bool{},
		indexes: map[string]func(client.Object) []string{
			"Bundle/" + lifecycle.IndexBundlePipeline: lifecycle.BundlePipeline,
			"PromotionStep/spec.pipelineName": func(o client.Object) []string {
				if ps, ok := o.(*v1alpha1.PromotionStep); ok && ps.Spec.PipelineName != "" {
					return []string{ps.Spec.PipelineName}
				}
				return nil
			},
		}}
}

// cachedKind reports whether reads of obj's Go type are served from memory:
// the typed kardinal kinds.
func cachedKind(obj k8sruntime.Object) bool {
	if _, ok := obj.(*unstructured.Unstructured); ok {
		return false
	}
	if _, ok := obj.(*unstructured.UnstructuredList); ok {
		return false
	}
	t := reflect.TypeOf(obj)
	return t.Kind() == reflect.Ptr && t.Elem().PkgPath() == reflect.TypeOf(v1alpha1.Bundle{}).PkgPath()
}

// invalidate drops what a write of kind ns/name changed; created marks a
// create or delete, which changes the kind's lists.
func (r *readCache) invalidate(kind, ns, name string, created bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.objs, kind+"/"+ns+"/"+name)
	if created {
		delete(r.lists, kind+"/"+ns)
		delete(r.lists, kind+"/*")
	}
}

func (r *readCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if !cachedKind(obj) || len(opts) > 0 {
		return r.Client.Get(ctx, key, obj, opts...)
	}
	kind := kindOf(obj)
	k := kind + "/" + key.Namespace + "/" + key.Name
	r.mu.Lock()
	cached, ok := r.objs[k]
	r.mu.Unlock()
	if !ok {
		if err := r.Client.Get(ctx, key, obj); err != nil {
			return err
		}
		r.mu.Lock()
		r.objs[k] = obj.DeepCopyObject()
		r.mu.Unlock()
		return nil
	}
	reflect.ValueOf(obj).Elem().Set(reflect.ValueOf(cached.DeepCopyObject()).Elem())
	return nil
}

func (r *readCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if !cachedKind(list) {
		return r.Client.List(ctx, list, opts...)
	}
	lo := &client.ListOptions{}
	lo.ApplyOptions(opts)
	kind := kindOf(list)
	kind = kind[:len(kind)-len("List")]
	var match func(client.Object) bool
	if lo.FieldSelector != nil && !lo.FieldSelector.Empty() {
		reqs := lo.FieldSelector.Requirements()
		if len(reqs) != 1 {
			return r.Client.List(ctx, list, opts...)
		}
		idx, ok := r.indexes[kind+"/"+reqs[0].Field]
		if !ok || reqs[0].Operator != "=" && reqs[0].Operator != "==" {
			return r.Client.List(ctx, list, opts...)
		}
		want := reqs[0].Value
		match = func(o client.Object) bool {
			for _, v := range idx(o) {
				if v == want {
					return true
				}
			}
			return false
		}
	}
	scope := kind + "/" + lo.Namespace
	if lo.Namespace == "" {
		scope = kind + "/*"
	}
	r.mu.Lock()
	keys, ok := r.lists[scope]
	r.mu.Unlock()
	if !ok {
		full := list.DeepCopyObject().(client.ObjectList)
		var lopts []client.ListOption
		if lo.Namespace != "" {
			lopts = append(lopts, client.InNamespace(lo.Namespace))
		}
		if err := r.Client.List(ctx, full, lopts...); err != nil {
			return err
		}
		items, err := apimeta.ExtractList(full)
		if err != nil {
			return fmt.Errorf("read cache: %w", err)
		}
		keys = map[string]bool{}
		r.mu.Lock()
		for _, it := range items {
			o := it.(client.Object)
			k := kind + "/" + o.GetNamespace() + "/" + o.GetName()
			keys[k] = true
			r.objs[k] = o.DeepCopyObject()
		}
		r.lists[scope] = keys
		r.mu.Unlock()
	}
	var out []k8sruntime.Object
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		r.mu.Lock()
		o, have := r.objs[k]
		r.mu.Unlock()
		if !have {
			// Changed since the list was read: read it again.
			fresh, err := r.refetch(ctx, list, k)
			if err != nil {
				return err
			}
			if fresh == nil {
				continue
			}
			o = fresh
		}
		obj := o.(client.Object)
		if lo.LabelSelector != nil && !lo.LabelSelector.Matches(labels.Set(obj.GetLabels())) {
			continue
		}
		if match != nil && !match(obj) {
			continue
		}
		out = append(out, o.DeepCopyObject())
	}
	return apimeta.SetList(list, out)
}

// refetch reads the object of cache key k (kind/ns/name) of list's item
// type; nil when it is gone.
func (r *readCache) refetch(ctx context.Context, list client.ObjectList, k string) (k8sruntime.Object, error) {
	parts := splitKey(k)
	ns, name := parts[1], parts[2]
	itemType := reflect.ValueOf(list).Elem().FieldByName("Items").Type().Elem()
	obj := reflect.New(itemType).Interface().(client.Object)
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, obj); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil, nil
		}
		return nil, err
	}
	r.mu.Lock()
	r.objs[k] = obj.DeepCopyObject()
	r.mu.Unlock()
	return obj, nil
}

func splitKey(k string) []string { return strings.SplitN(k, "/", 3) }
