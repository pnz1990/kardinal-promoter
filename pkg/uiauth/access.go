// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package uiauth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	authv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AccessReviewer decides whether a user may perform an action, using the
// Kubernetes SubjectAccessReview API. It lets tests run without a cluster.
type AccessReviewer interface {
	// Allowed reports whether user may perform attrs. It returns an error only
	// on transport or API failures; a denial is (false, reason, nil).
	Allowed(ctx context.Context, user authv1.UserInfo, attrs authzv1.ResourceAttributes) (bool, string, error)
}

// KubeAccessReviewer implements AccessReviewer with SubjectAccessReviews.
type KubeAccessReviewer struct {
	clientset kubernetes.Interface
}

// NewKubeAccessReviewer creates a KubeAccessReviewer from a rest.Config. The
// controller ServiceAccount needs create on
// subjectaccessreviews.authorization.k8s.io.
func NewKubeAccessReviewer(cfg *rest.Config) (*KubeAccessReviewer, error) {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("uiauth: creating Kubernetes clientset for AccessReviewer: %w", err)
	}
	return &KubeAccessReviewer{clientset: cs}, nil
}

// Allowed submits a SubjectAccessReview for the user and attributes.
func (r *KubeAccessReviewer) Allowed(ctx context.Context, user authv1.UserInfo, attrs authzv1.ResourceAttributes) (bool, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	extra := make(map[string]authzv1.ExtraValue, len(user.Extra))
	for k, v := range user.Extra {
		extra[k] = authzv1.ExtraValue(v)
	}
	sar := &authzv1.SubjectAccessReview{
		Spec: authzv1.SubjectAccessReviewSpec{
			ResourceAttributes: &attrs,
			User:               user.Username,
			Groups:             user.Groups,
			UID:                user.UID,
			Extra:              extra,
		},
	}
	result, err := r.clientset.AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
	if err != nil {
		return false, "", fmt.Errorf("uiauth: SubjectAccessReview API call failed: %w", err)
	}
	return result.Status.Allowed && !result.Status.Denied, result.Status.Reason, nil
}

type userKey struct{}

// WithUser returns a context that carries the authenticated UI user.
func WithUser(ctx context.Context, user authv1.UserInfo) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

// UserFrom returns the authenticated UI user stored by Middleware.
func UserFrom(ctx context.Context) (authv1.UserInfo, bool) {
	u, ok := ctx.Value(userKey{}).(authv1.UserInfo)
	return u, ok
}

// denial is the first authorization failure seen while serving one request.
// Middleware turns it into the HTTP response, so a handler that maps every
// client error to 500 (or ignores a List error) still answers 403 or 503.
type denial struct {
	code int
	msg  string
}

type requestAuth struct {
	mu     sync.Mutex
	denied *denial
}

func (s *requestAuth) record(d denial) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.denied == nil {
		s.denied = &d
	}
}

func (s *requestAuth) get() *denial {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.denied
}

type requestAuthKey struct{}

func withRequestAuth(ctx context.Context) (context.Context, *requestAuth) {
	s := &requestAuth{}
	return context.WithValue(ctx, requestAuthKey{}, s), s
}

func requestAuthFrom(ctx context.Context) *requestAuth {
	s, _ := ctx.Value(requestAuthKey{}).(*requestAuth)
	return s
}

// AuthorizingClient wraps a client.Client so that every read and write the UI
// API makes on behalf of a user is first checked with a SubjectAccessReview for
// that user, verb, resource, namespace and name. The check runs against the
// object that is actually read or written, so it cannot drift from how a
// handler resolves namespaces. Requests without an authenticated user in the
// context are denied. After one denial, every later call in the same request
// is denied too, so a handler cannot act on partial data.
type AuthorizingClient struct {
	client.Client
	access AccessReviewer
	// scopeNamespace is the namespace a request without one is checked
	// against. Set it to --watch-namespace: the controller cache only holds
	// that namespace, so an all-namespaces List only returns its objects.
	scopeNamespace string
}

// NewAuthorizingClient returns c wrapped with per-user authorization.
func NewAuthorizingClient(c client.Client, access AccessReviewer, scopeNamespace string) *AuthorizingClient {
	return &AuthorizingClient{Client: c, access: access, scopeNamespace: scopeNamespace}
}

func (c *AuthorizingClient) authorize(ctx context.Context, verb string, obj runtime.Object, namespace, name, subresource string) error {
	gvk, err := c.GroupVersionKindFor(obj)
	if err != nil {
		return fmt.Errorf("uiauth: resolve kind: %w", err)
	}
	if _, isList := obj.(client.ObjectList); isList {
		gvk.Kind = strings.TrimSuffix(gvk.Kind, "List")
	}
	plural, _ := meta.UnsafeGuessKindToResource(gvk)
	gr := schema.GroupResource{Group: gvk.Group, Resource: plural.Resource}
	if namespace == "" {
		namespace = c.scopeNamespace
	}

	state := requestAuthFrom(ctx)
	if state != nil && state.get() != nil {
		return apierrors.NewForbidden(gr, name, fmt.Errorf("an earlier check in this request was denied"))
	}
	fail := func(d denial, err error) error {
		if state != nil {
			state.record(d)
		}
		return err
	}

	user, ok := UserFrom(ctx)
	if !ok || user.Username == "" {
		return fail(denial{code: 401, msg: "unauthorized"},
			apierrors.NewUnauthorized("no authenticated user"))
	}
	attrs := authzv1.ResourceAttributes{
		Namespace:   namespace,
		Verb:        verb,
		Group:       gr.Group,
		Resource:    gr.Resource,
		Subresource: subresource,
		Name:        name,
	}
	allowed, reason, err := c.access.Allowed(ctx, user, attrs)
	if err != nil {
		return fail(denial{code: 503, msg: "auth unavailable"},
			apierrors.NewServiceUnavailable(err.Error()))
	}
	if !allowed {
		where := "all namespaces"
		if namespace != "" {
			where = "namespace " + namespace
		}
		msg := fmt.Sprintf("forbidden: user %q cannot %s %s in %s", user.Username, verb, gr.String(), where)
		if reason != "" {
			msg += ": " + reason
		}
		return fail(denial{code: 403, msg: msg}, apierrors.NewForbidden(gr, name, fmt.Errorf("%s", msg)))
	}
	return nil
}

// AuthorizeSubresource checks that the request's user may verb the
// (virtual) subresource of obj, such as update pipelines/hold, without
// reading or writing anything. Handlers call it for a change the API server
// would authorize against a subresource the controller's own write skips.
func (c *AuthorizingClient) AuthorizeSubresource(ctx context.Context, verb string, obj client.Object, subresource string) error {
	return c.authorize(ctx, verb, obj, obj.GetNamespace(), obj.GetName(), subresource)
}

// Get authorizes "get" and then reads through the wrapped client.
func (c *AuthorizingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := c.authorize(ctx, "get", obj, key.Namespace, key.Name, ""); err != nil {
		return err
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

// List authorizes "list" in the requested namespace (all namespaces when unset).
func (c *AuthorizingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	lo := &client.ListOptions{}
	lo.ApplyOptions(opts)
	if err := c.authorize(ctx, "list", list, lo.Namespace, "", ""); err != nil {
		return err
	}
	return c.Client.List(ctx, list, opts...)
}

// Create authorizes "create" in the object's namespace.
func (c *AuthorizingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err := c.authorize(ctx, "create", obj, obj.GetNamespace(), obj.GetName(), ""); err != nil {
		return err
	}
	return c.Client.Create(ctx, obj, opts...)
}

// Update authorizes "update" on the named object.
func (c *AuthorizingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if err := c.authorize(ctx, "update", obj, obj.GetNamespace(), obj.GetName(), ""); err != nil {
		return err
	}
	return c.Client.Update(ctx, obj, opts...)
}

// Patch authorizes "patch" on the named object.
func (c *AuthorizingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if err := c.authorize(ctx, "patch", obj, obj.GetNamespace(), obj.GetName(), ""); err != nil {
		return err
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

// Delete authorizes "delete" on the named object.
func (c *AuthorizingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if err := c.authorize(ctx, "delete", obj, obj.GetNamespace(), obj.GetName(), ""); err != nil {
		return err
	}
	return c.Client.Delete(ctx, obj, opts...)
}

// DeleteAllOf authorizes "deletecollection" in the requested namespace.
func (c *AuthorizingClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	o := &client.DeleteAllOfOptions{}
	o.ApplyOptions(opts)
	if err := c.authorize(ctx, "deletecollection", obj, o.Namespace, "", ""); err != nil {
		return err
	}
	return c.Client.DeleteAllOf(ctx, obj, opts...)
}

// Apply is not used by the UI API and is always refused.
func (c *AuthorizingClient) Apply(_ context.Context, _ runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
	return errNotSupported
}

// Status returns a writer that refuses every call; the UI API never writes status.
func (c *AuthorizingClient) Status() client.SubResourceWriter { return refusedSubResource{} }

// SubResource returns a client that refuses every call; the UI API uses none.
func (c *AuthorizingClient) SubResource(string) client.SubResourceClient { return refusedSubResource{} }

var errNotSupported = apierrors.NewMethodNotSupported(schema.GroupResource{}, "ui-api")

type refusedSubResource struct{}

func (refusedSubResource) Get(context.Context, client.Object, client.Object, ...client.SubResourceGetOption) error {
	return errNotSupported
}
func (refusedSubResource) Create(context.Context, client.Object, client.Object, ...client.SubResourceCreateOption) error {
	return errNotSupported
}
func (refusedSubResource) Update(context.Context, client.Object, ...client.SubResourceUpdateOption) error {
	return errNotSupported
}
func (refusedSubResource) Patch(context.Context, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
	return errNotSupported
}
func (refusedSubResource) Apply(context.Context, runtime.ApplyConfiguration, ...client.SubResourceApplyOption) error {
	return errNotSupported
}
