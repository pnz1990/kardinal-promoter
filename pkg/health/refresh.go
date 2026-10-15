// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// The annotations that ask a GitOps tool to sync now instead of at its next
// poll. Argo CD's application controller refreshes an Application carrying
// ArgoCDRefreshAnnotation and then removes it; Flux's controllers reconcile an
// object whose FluxRequestedAtAnnotation differs from its
// status.lastHandledReconcileAt (what `flux reconcile` sets).
const (
	ArgoCDRefreshAnnotation   = "argocd.argoproj.io/refresh"
	ArgoCDRefreshNormal       = "normal"
	FluxRequestedAtAnnotation = "reconcile.fluxcd.io/requestedAt"
)

var fluxGitRepositoryGVR = schema.GroupVersionResource{
	Group:    "source.toolkit.fluxcd.io",
	Version:  "v1",
	Resource: "gitrepositories",
}

// RefreshTarget is one object a refresh request annotates.
type RefreshTarget struct {
	GVR       schema.GroupVersionResource
	Kind      string
	Namespace string
	Name      string
	// Destination is the namespace the object deploys to in this cluster:
	// an in-cluster Application's spec.destination.namespace, a
	// Kustomization's spec.targetNamespace (without spec.kubeConfig), and for
	// a GitRepository in its Kustomization's namespace the Kustomization's
	// (or that namespace). "" when it names none.
	Destination string
}

// String is "Kind namespace/name".
func (t RefreshTarget) String() string {
	return fmt.Sprintf("%s %s/%s", t.Kind, t.Namespace, t.Name)
}

// RefreshSupported reports whether a health type names a GitOps object that
// can be asked to sync: argocd (an Application) and flux (a Kustomization).
func RefreshSupported(healthType string) bool {
	return healthType == "argocd" || healthType == "flux"
}

// RefreshTargets returns the objects a refresh of the health check opts
// describes annotates, in the order they are annotated: the Argo CD
// Application; or the Flux Kustomization's source GitRepository (when its
// spec.sourceRef is one) and then the Kustomization. Flux applies only what
// its source has fetched, so the source comes first. It reads the
// Kustomization for its source and each object for the namespace it deploys
// to (RefreshTarget.Destination); it writes nothing. Other health types have
// no target.
func (d *AutoDetector) RefreshTargets(ctx context.Context, opts CheckOptions) ([]RefreshTarget, error) {
	switch opts.Type {
	case "argocd":
		ns := opts.ArgoCD.Namespace
		if ns == "" {
			ns = "argocd"
		}
		target := RefreshTarget{GVR: argoCDApplicationGVR, Kind: "Application", Namespace: ns, Name: opts.ArgoCD.Name}
		app, err := d.dynamic.Resource(argoCDApplicationGVR).Namespace(ns).Get(ctx, opts.ArgoCD.Name, metav1.GetOptions{})
		if err != nil {
			return nil, &RefreshError{Op: "get", Target: target, Err: err}
		}
		if inCluster(app) {
			target.Destination, _, _ = unstructured.NestedString(app.Object, "spec", "destination", "namespace")
		}
		return []RefreshTarget{target}, nil
	case "flux":
		ns := opts.Flux.Namespace
		if ns == "" {
			ns = "flux-system"
		}
		target := RefreshTarget{GVR: fluxKustomizationGVR, Kind: "Kustomization", Namespace: ns, Name: opts.Flux.Name}
		ks, err := d.dynamic.Resource(fluxKustomizationGVR).Namespace(ns).Get(ctx, opts.Flux.Name, metav1.GetOptions{})
		if err != nil {
			return nil, &RefreshError{Op: "get", Target: target, Err: err}
		}
		if _, remote, _ := unstructured.NestedMap(ks.Object, "spec", "kubeConfig"); !remote {
			// A Kustomization that applies to another cluster
			// (spec.kubeConfig) deploys to a namespace there: it is judged
			// by its own namespace.
			target.Destination, _, _ = unstructured.NestedString(ks.Object, "spec", "targetNamespace")
		}
		var targets []RefreshTarget
		if src, ok := fluxGitSource(ks); ok {
			// A source in the Kustomization's namespace is refreshed for it
			// and in scope when the Kustomization is. A source in another
			// namespace (a shared flux-system GitRepository, or another
			// tenant's) is judged by its own namespace: naming it in a
			// Kustomization does not make it the Pipeline's.
			if src.Namespace == ks.GetNamespace() {
				src.Destination = cmp.Or(target.Destination, target.Namespace)
			}
			targets = append(targets, src)
		}
		return append(targets, target), nil
	}
	return nil, nil
}

// inCluster reports whether an Application deploys to the cluster Argo CD
// runs in (spec.destination.server https://kubernetes.default.svc, or name
// in-cluster, or neither set). An Application that deploys to another cluster
// names a namespace of that cluster, which says nothing about who owns it
// here: it is judged by its own namespace.
func inCluster(app *unstructured.Unstructured) bool {
	server, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "server")
	name, _, _ := unstructured.NestedString(app.Object, "spec", "destination", "name")
	switch {
	case server != "":
		return strings.TrimSuffix(server, "/") == "https://kubernetes.default.svc"
	case name != "":
		return name == "in-cluster"
	}
	return true
}

// fluxGitSource is the GitRepository a Kustomization's spec.sourceRef names.
// Other source kinds (OCIRepository, Bucket) are not refreshed.
func fluxGitSource(ks *unstructured.Unstructured) (RefreshTarget, bool) {
	kind, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "kind")
	name, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "name")
	if kind != "GitRepository" || name == "" {
		return RefreshTarget{}, false
	}
	ns, _, _ := unstructured.NestedString(ks.Object, "spec", "sourceRef", "namespace")
	if ns == "" {
		ns = ks.GetNamespace()
	}
	return RefreshTarget{GVR: fluxGitRepositoryGVR, Kind: "GitRepository", Namespace: ns, Name: name}, true
}

// FluxRequestValue is the reconcile.fluxcd.io/requestedAt value a step
// sends: the time it recorded (RFC 3339) and the step, so that two steps
// that ask in the same second, for example a fan-out sharing one
// GitRepository, send different values. Flux reconciles an object when the
// value differs from its status.lastHandledReconcileAt; the value is opaque
// to it.
func FluxRequestValue(at time.Time, namespace, name string) string {
	return at.UTC().Format(time.RFC3339) + " " + namespace + "/" + name
}

// RefreshError is the failure of a refresh request on one target: reading
// it (Op "get", the Kustomization for its source) or annotating it (Op
// "annotate").
type RefreshError struct {
	Op     string
	Target RefreshTarget
	Err    error
}

func (e *RefreshError) Error() string { return fmt.Sprintf("%s %s: %v", e.Op, e.Target, e.Err) }

func (e *RefreshError) Unwrap() error { return e.Err }

// RequestRefresh merge-patches the refresh annotation onto each target: for
// an Application ArgoCDRefreshNormal, for a Flux object fluxValue
// (FluxRequestValue of the time the caller recorded in the PromotionStep
// status first). Only metadata.annotations changes. Every target is tried;
// the errors are joined *RefreshErrors.
func (d *AutoDetector) RequestRefresh(ctx context.Context, targets []RefreshTarget, fluxValue string) error {
	var errs []error
	for _, t := range targets {
		key, value := ArgoCDRefreshAnnotation, ArgoCDRefreshNormal
		if t.GVR.Group != argoCDApplicationGVR.Group {
			key, value = FluxRequestedAtAnnotation, fluxValue
		}
		body, err := json.Marshal(map[string]interface{}{
			"metadata": map[string]interface{}{"annotations": map[string]string{key: value}},
		})
		if err != nil {
			return fmt.Errorf("marshal refresh patch: %w", err)
		}
		if _, err := d.dynamic.Resource(t.GVR).Namespace(t.Namespace).
			Patch(ctx, t.Name, types.MergePatchType, body, metav1.PatchOptions{}); err != nil {
			errs = append(errs, &RefreshError{Op: "annotate", Target: t, Err: err})
		}
	}
	return errors.Join(errs...)
}

// ClassifyRefreshError is a short reason for status and Events, per
// target: what failed on which object, and the class of the error
// (ClassifyRemoteError; a missing object is "not found (HTTP 404)"). It
// never quotes the API server's answer or address, which can come from a
// remote cluster (health.kubeconfigSecretRef): the full error belongs in
// the log only.
func ClassifyRefreshError(err error) string {
	if err == nil {
		return ""
	}
	errs := []error{err}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs = j.Unwrap()
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		reason := ClassifyRemoteError(e)
		if apierrors.IsNotFound(e) {
			reason = "not found (HTTP 404)"
		}
		var re *RefreshError
		if errors.As(e, &re) {
			reason = re.Op + " " + re.Target.String() + ": " + reason
		}
		parts = append(parts, reason)
	}
	return strings.Join(parts, "; ")
}

// DescribeTargets lists targets for a status message.
func DescribeTargets(targets []RefreshTarget) string {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.String()
	}
	return strings.Join(names, ", ")
}
