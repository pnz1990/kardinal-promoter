// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/authentication/user"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ClientAs is a client of the World that makes every request as u, as a
// kubeconfig of u's would: what a CLI, an MCP server or a caller of the
// API server runs as. Writes go through admission as u.
//
// With namespaces, u may read and write namespaced objects only there, as a
// RoleBinding in each would grant (RBAC); ClientAsRBAC also grants reads.
func (w *World) ClientAs(u user.Info, namespaces ...string) client.Client {
	return &userClient{Client: w.Client, u: u, namespaces: namespaces, reads: namespaces}
}

// ClientAsRBAC is ClientAs with u's RBAC: writes in write, reads in write
// and read.
func (w *World) ClientAsRBAC(u user.Info, write, read []string) client.Client {
	return &userClient{Client: w.Client, u: u, namespaces: write, reads: append(append([]string(nil), write...), read...)}
}

type userClient struct {
	client.Client
	u user.Info
	// namespaces, when set, are the only namespaces u may write namespaced
	// objects in; reads, where it may read them.
	namespaces, reads []string
}

// allowed is RBAC for o: a Forbidden for a namespaced object outside
// c.namespaces (c.reads for get).
func (c *userClient) allowed(o client.Object, verb string) error {
	in := c.namespaces
	if verb == "get" {
		in = c.reads
	}
	if len(in) == 0 || o.GetNamespace() == "" || slices.Contains(in, o.GetNamespace()) {
		return nil
	}
	return apierrors.NewForbidden(schema.GroupResource{Group: "kardinal.io", Resource: strings.ToLower(objKind(o)) + "s"}, o.GetName(),
		fmt.Errorf("user %q cannot %s in the namespace %q", c.u.GetName(), verb, o.GetNamespace()))
}

func (c *userClient) ctx(ctx context.Context) context.Context { return AsInfo(ctx, c.u) }

func (c *userClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	probe := obj.DeepCopyObject().(client.Object)
	probe.SetNamespace(key.Namespace)
	if err := c.allowed(probe, "get"); err != nil {
		return err
	}
	return c.Client.Get(c.ctx(ctx), key, obj, opts...)
}

func (c *userClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if len(c.reads) > 0 {
		lo := &client.ListOptions{}
		lo.ApplyOptions(opts)
		kind := strings.TrimSuffix(fmt.Sprintf("%T", list), "List")
		kind = kind[strings.LastIndex(kind, ".")+1:]
		cluster := false
		for _, r := range apiResources {
			if r.kind == kind && r.cluster {
				cluster = true
			}
		}
		if !cluster && !slices.Contains(c.reads, lo.Namespace) {
			return apierrors.NewForbidden(schema.GroupResource{Group: "kardinal.io", Resource: strings.ToLower(kind) + "s"}, "",
				fmt.Errorf("user %q cannot list in the namespace %q", c.u.GetName(), lo.Namespace))
		}
	}
	return c.Client.List(c.ctx(ctx), list, opts...)
}

func (c *userClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.allowed(obj, "create"); err != nil {
		return err
	}
	return c.Client.Create(c.ctx(ctx), obj, opts...)
}

func (c *userClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := c.allowed(obj, "update"); err != nil {
		return err
	}
	return c.Client.Update(c.ctx(ctx), obj, opts...)
}

func (c *userClient) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
	if err := c.allowed(obj, "patch"); err != nil {
		return err
	}
	return c.Client.Patch(c.ctx(ctx), obj, p, opts...)
}

func (c *userClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if err := c.allowed(obj, "delete"); err != nil {
		return err
	}
	return c.Client.Delete(c.ctx(ctx), obj, opts...)
}

func (c *userClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return c.Client.DeleteAllOf(c.ctx(ctx), obj, opts...)
}

func (c *userClient) Status() client.SubResourceWriter {
	return &userSubClient{SubResourceWriter: c.Client.Status(), c: c}
}

func (c *userClient) SubResource(sub string) client.SubResourceClient {
	return &userSubClient{SubResourceWriter: c.Client.SubResource(sub), r: c.Client.SubResource(sub), c: c}
}

type userSubClient struct {
	client.SubResourceWriter
	r client.SubResourceClient
	c *userClient
}

func (s *userSubClient) Get(ctx context.Context, obj, sub client.Object, opts ...client.SubResourceGetOption) error {
	return s.r.Get(s.c.ctx(ctx), obj, sub, opts...)
}

func (s *userSubClient) Create(ctx context.Context, obj, sub client.Object, opts ...client.SubResourceCreateOption) error {
	return s.SubResourceWriter.Create(s.c.ctx(ctx), obj, sub, opts...)
}

func (s *userSubClient) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return s.SubResourceWriter.Update(s.c.ctx(ctx), obj, opts...)
}

func (s *userSubClient) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
	return s.SubResourceWriter.Patch(s.c.ctx(ctx), obj, p, opts...)
}

// Snapshot is every kardinal object a caller can write, by
// kind/namespace/name, with its resourceVersion: compare two to see what a
// request changed.
func (w *World) Snapshot() map[string]string {
	w.T.Helper()
	out := map[string]string{}
	lists := map[string]client.ObjectList{
		"Bundle": &v1alpha1.BundleList{}, "Approval": &v1alpha1.ApprovalList{}, "Verdict": &v1alpha1.VerdictList{},
		"Pipeline": &v1alpha1.PipelineList{}, "PolicyGate": &v1alpha1.PolicyGateList{}, "ChangeWindow": &v1alpha1.ChangeWindowList{},
		"AgentIdentity": &v1alpha1.AgentIdentityList{}, "PromotionStep": &v1alpha1.PromotionStepList{},
	}
	for kind, l := range lists {
		if err := w.store.List(w.Ctx, l); err != nil {
			w.T.Fatalf("snapshot %s: %v", kind, err)
		}
		objs, err := apimetaExtract(l)
		if err != nil {
			w.T.Fatal(err)
		}
		for _, o := range objs {
			out[fmt.Sprintf("%s/%s/%s", kind, o.GetNamespace(), o.GetName())] = o.GetResourceVersion()
		}
	}
	return out
}

// Changed is what changed from before to after: created, updated or
// deleted keys, sorted.
func Changed(before, after map[string]string) []string {
	var out []string
	for k, v := range after {
		if before[k] != v {
			out = append(out, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// validMeta checks o's name, namespace, labels and annotations as the API
// server does before it stores an object (ValidateObjectMeta): the fake
// client stores any.
func (w *World) validMeta(c client.Client, o, _ client.Object) error {
	gvk, err := c.GroupVersionKindFor(o)
	if err != nil {
		return nil //nolint:nilerr // not a kind the World knows
	}
	namespaced := o.GetNamespace() != ""
	for _, r := range apiResources {
		if r.group == gvk.Group && r.kind == gvk.Kind {
			namespaced = !r.cluster
		}
	}
	// The API server generates a name from generateName before it
	// validates (BeforeCreate): validate as if it had.
	v, ok := o.DeepCopyObject().(client.Object)
	if !ok {
		return nil
	}
	if v.GetName() == "" && v.GetGenerateName() != "" {
		v.SetName(v.GetGenerateName() + "x0000")
	}
	errs := apivalidation.ValidateObjectMetaAccessor(v, namespaced, apivalidation.NameIsDNSSubdomain, field.NewPath("metadata"))
	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(gvk.GroupKind(), o.GetName(), errs)
}

// validSchema checks a create of o, or an update (old: the stored object),
// against its CRD's schema and CEL rules, transition rules included, as the
// API server does: a 422 Invalid when it refuses. Kinds without a CRD file
// pass.
func (w *World) validSchema(o, old client.Object) error {
	if _, ok := crdFiles[objKind(o)]; !ok {
		return nil
	}
	cp, ok := o.DeepCopyObject().(client.Object)
	if !ok {
		return nil
	}
	var prev k8sruntime.Object
	if old != nil {
		prev = old
	}
	reasons, err := w.Admit.Admit(cp, prev)
	if err != nil || len(reasons) == 0 {
		return nil //nolint:nilerr // a kind the schema cannot load is not judged
	}
	errs := field.ErrorList{}
	for _, r := range reasons {
		errs = append(errs, field.Invalid(field.NewPath(""), "", r))
	}
	return apierrors.NewInvalid(schema.GroupKind{Group: "kardinal.io", Kind: objKind(o)}, o.GetName(), errs)
}

// LongStrings returns, for the objects in changed, every string in their
// spec longer than max bytes, as "key path length": text a caller sent that
// kardinal would carry on (into PR bodies, gate reasons, notifications)
// without a bound.
func (w *World) LongStrings(changed []string, max int) []string {
	w.T.Helper()
	var out []string
	for _, k := range changed {
		parts := strings.SplitN(k, "/", 3)
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: "kardinal.io", Version: "v1alpha1", Kind: parts[0]})
		if w.store.Get(w.Ctx, client.ObjectKey{Namespace: parts[1], Name: parts[2]}, u) != nil {
			continue
		}
		var walk func(v interface{}, path string)
		walk = func(v interface{}, path string) {
			switch x := v.(type) {
			case map[string]interface{}:
				for key, val := range x {
					walk(val, path+"."+key)
				}
			case []interface{}:
				for i, val := range x {
					walk(val, fmt.Sprintf("%s[%d]", path, i))
				}
			case string:
				if len(x) > max {
					out = append(out, fmt.Sprintf("%s %s %d", k, path, len(x)))
				}
			}
		}
		walk(u.Object["spec"], "spec")
	}
	return out
}

// Writes is how many writes the World's store has taken: unchanged, no
// object changed.
func (w *World) Writes() int64 { return w.writes.Load() }
