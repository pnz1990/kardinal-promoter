// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// doctorRemoteDial dials a remote cluster for checkRemoteHealthAccess; nil is
// health.RemoteConfig's egress guard. Tests replace it.
var doctorRemoteDial func(ctx context.Context, network, address string) (net.Conn, error)

// doctorRemoteTimeout bounds each environment's remote check.
const doctorRemoteTimeout = 10 * time.Second

// remoteAccess is what one environment's health check does on its remote
// cluster: get the object it names, and patch it when it asks the GitOps
// tool to refresh.
type remoteAccess struct {
	group, resource, kind, namespace, name string
	refresh                                bool
}

// remoteAccessOf is the access env's health check needs remotely, or false
// when its health type reads nothing doctor can name.
func remoteAccessOf(pipeline string, env v1alpha1.EnvironmentSpec) (remoteAccess, bool) {
	opts := health.OptionsForEnv(pipeline, env)
	refresh := env.Health.Refresh == nil || *env.Health.Refresh
	switch opts.Type {
	case "argocd":
		return remoteAccess{group: "argoproj.io", resource: "applications", kind: "Application",
			namespace: opts.ArgoCD.Namespace, name: opts.ArgoCD.Name, refresh: refresh}, true
	case "flux":
		return remoteAccess{group: "kustomize.toolkit.fluxcd.io", resource: "kustomizations", kind: "Kustomization",
			namespace: opts.Flux.Namespace, name: opts.Flux.Name, refresh: refresh}, true
	case "resource":
		return remoteAccess{group: "apps", resource: "deployments", kind: "Deployment",
			namespace: opts.Resource.Namespace, name: opts.Resource.Name}, true
	}
	return remoteAccess{}, false
}

// checkRemoteHealthAccess checks, for each environment of p whose health
// check reads a remote cluster (health.kubeconfigSecretRef), what the
// controller will find there (#1883): the kubeconfig Secret it reads, a
// kubeconfig it accepts, and, with a SelfSubjectAccessReview as the
// kubeconfig's identity, get on the object the check names and patch on it
// when health.refresh is on. A missing patch is a WARN: promotions then wait
// for the GitOps tool's poll. The remote server's answers and address are
// not printed.
func checkRemoteHealthAccess(ctx context.Context, c sigs_client.Client, p *v1alpha1.Pipeline) []doctorResult {
	var out []doctorResult
	for _, env := range p.Spec.Environments {
		ref := env.Health.KubeconfigSecretRef
		if ref == nil {
			continue
		}
		r := doctorResult{label: fmt.Sprintf("Remote health %s", env.Name)}
		access, ok := remoteAccessOf(p.Name, env)
		if !ok {
			r.icon, r.detail = doctorPass, fmt.Sprintf("health.type %s: nothing to check", health.EffectiveType(env))
			out = append(out, r)
			continue
		}
		out = append(out, remoteAccessResult(ctx, c, p.Namespace, ref, access, r))
	}
	return out
}

func remoteAccessResult(ctx context.Context, c sigs_client.Client, ns string, ref *v1alpha1.KubeconfigSecretRef,
	access remoteAccess, r doctorResult) doctorResult {
	fail := func(detail, hint string) doctorResult {
		r.icon, r.failed, r.detail, r.hint = doctorFail, true, detail, hint
		return r
	}
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &secret); err != nil {
		r.icon, r.warned = doctorWarn, true
		r.detail = fmt.Sprintf("kubeconfig Secret %s/%s could not be read: %s", ns, ref.Name, HumanError(err))
		r.hint = "doctor needs get on that Secret to check the remote cluster; the controller reads it with its own identity."
		return r
	}
	if secret.Labels["kardinal.io/referenceable"] != "true" {
		return fail(fmt.Sprintf("kubeconfig Secret %s/%s does not have the label kardinal.io/referenceable: \"true\"", ns, ref.Name),
			fmt.Sprintf("kubectl label secret -n %s %s kardinal.io/referenceable=true", ns, ref.Name))
	}
	key := ref.Key
	if key == "" {
		key = health.DefaultKubeconfigKey
	}
	if ref.EKS != nil {
		// The controller signs the cluster's tokens with its own AWS
		// identity, as roleARN: doctor cannot act as that role, so it
		// checks the kubeconfig only.
		if err := health.CheckEKSKubeconfig(secret.Data[key]); err != nil {
			return fail(fmt.Sprintf("kubeconfig Secret %s/%s key %q: %v", ns, ref.Name, key, err),
				"See docs/health-adapters.md#remote-clusters-on-eks.")
		}
		r.icon, r.warned = doctorWarn, true
		r.detail = fmt.Sprintf("EKS cluster %s (%s) as %s: the controller signs the tokens; access is not checked from here",
			ref.EKS.ClusterName, ref.EKS.Region, ref.EKS.RoleARN)
		r.hint = "The role must be in the controller's health.eks.allowedRoleARNs, trust the controller with the session tag " +
			"kardinal-namespace=" + ns + ", and have an EKS access entry: docs/health-adapters.md#remote-clusters-on-eks."
		return r
	}
	cfg, err := health.RemoteConfig(secret.Data[key], doctorRemoteDial)
	if err != nil {
		return fail(fmt.Sprintf("kubeconfig Secret %s/%s key %q: %v", ns, ref.Name, key, err),
			"See docs/health-adapters.md#remote-clusters for the kubeconfigs the controller accepts.")
	}
	cfg.Timeout = doctorRemoteTimeout
	cfg.ContentType, cfg.AcceptContentTypes = "application/json", "application/json"
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fail(fmt.Sprintf("kubeconfig Secret %s/%s: %v", ns, ref.Name, err), "")
	}
	object := fmt.Sprintf("%s %s/%s", access.kind, access.namespace, access.name)
	verbs := []string{"get"}
	if access.refresh {
		verbs = append(verbs, "patch")
	}
	var denied []string
	for _, verb := range verbs {
		review := &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authorizationv1.ResourceAttributes{Group: access.group, Resource: access.resource,
				Namespace: access.namespace, Name: access.name, Verb: verb}}}
		rctx, cancel := context.WithTimeout(ctx, doctorRemoteTimeout)
		got, err := kube.AuthorizationV1().SelfSubjectAccessReviews().Create(rctx, review, metav1.CreateOptions{})
		cancel()
		if err != nil {
			r.icon, r.warned = doctorWarn, true
			r.detail = fmt.Sprintf("the remote cluster could not be checked: %s", health.ClassifyRemoteError(err))
			r.hint = "Check that the kubeconfig's server is reachable from here and its credentials are current."
			return r
		}
		if !got.Status.Allowed {
			denied = append(denied, verb)
		}
	}
	switch {
	case len(denied) > 0 && denied[0] == "get":
		return fail(fmt.Sprintf("the kubeconfig's identity may not %s %s", strings.Join(denied, " or "), object),
			fmt.Sprintf("Grant it %s on %s.%s %q in %s on the remote cluster: the health check cannot read it.",
				strings.Join(verbs, " and "), access.resource, access.group, access.name, access.namespace))
	case len(denied) > 0:
		r.icon, r.warned = doctorWarn, true
		r.detail = fmt.Sprintf("the kubeconfig's identity may get but not patch %s: health.refresh is refused there, "+
			"so promotions wait for the GitOps tool's poll", object)
		r.hint = fmt.Sprintf("Grant it patch on %s.%s %q in %s (resourceNames), or set health.refresh: false on the environment.",
			access.resource, access.group, access.name, access.namespace)
		return r
	}
	r.icon = doctorPass
	r.detail = fmt.Sprintf("the kubeconfig's identity may %s %s", strings.Join(verbs, " and "), object)
	return r
}
