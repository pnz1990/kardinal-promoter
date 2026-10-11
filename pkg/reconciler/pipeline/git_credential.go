// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package pipeline

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// reasonGitCredentialMissing: spec.git.url is an HTTP(S) remote that needs a
// token, and spec.git.secretRef is not set, names a Secret that does not
// exist, or a Secret with neither a token nor GitHub App credentials. Every
// git step of a Bundle would wait for the credential (the PromotionStep
// condition of the same name), so the Pipeline says so as soon as it is
// applied, with the command that fixes it.
const reasonGitCredentialMissing = "GitCredentialMissing"

// gitSecret is spec.git.secretRef's Secret as the reconciler read it.
type gitSecret struct {
	// name is the Secret's name; "" when spec.git.secretRef is not set.
	name string
	// secret is the Secret; nil when name is "", the Secret does not exist,
	// or it could not be read.
	secret *corev1.Secret
	// notFound is true when the Secret does not exist.
	notFound bool
}

// readGitSecret reads spec.git.secretRef's Secret from the API server
// (Secrets are not cached). A Secret the controller may not read is
// reported as neither found nor missing: the steps report why.
func (r *Reconciler) readGitSecret(ctx context.Context, p *kardinalv1alpha1.Pipeline) (gitSecret, error) {
	ref := p.Spec.Git.SecretRef
	if ref == nil || ref.Name == "" {
		return gitSecret{}, nil
	}
	gs := gitSecret{name: ref.Name}
	var secret corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Namespace: p.Namespace, Name: ref.Name}, &secret)
	switch {
	case err == nil:
		gs.secret = &secret
	case apierrors.IsNotFound(err):
		gs.notFound = true
	case apierrors.IsForbidden(err):
	default:
		return gs, fmt.Errorf("get git secret %s: %w", ref.Name, err)
	}
	return gs, nil
}

// gitCredentialMissing returns the Ready=False/GitCredentialMissing
// condition of p, or nil when git has a credential or needs none (an ssh or
// file remote, a password in the URL), or the Secret could not be read.
func gitCredentialMissing(p *kardinalv1alpha1.Pipeline, gs gitSecret) *metav1.Condition {
	if !scm.RemoteNeedsToken(p.Spec.Git.URL) {
		return nil
	}
	name := gs.name
	if name == "" {
		name = "git-token"
	}
	// A token used as the URL's user name ("https://<token>@host/...") must
	// not reach the status, the CLI or the logs.
	remote := withoutUserinfo(p.Spec.Git.URL)
	create := fmt.Sprintf("kubectl create secret generic %s -n %s --from-literal=token=<TOKEN> && "+
		"kubectl label secret %s -n %s %s=true", name, p.Namespace, name, p.Namespace, labelReferenceable)
	var msg string
	switch {
	case gs.name == "":
		msg = fmt.Sprintf("spec.git.secretRef is not set, so git has no token for %s; create the Secret (%s) "+
			"and set spec.git.secretRef.name: %s", remote, create, name)
	case gs.notFound:
		msg = fmt.Sprintf("git Secret %s not found in namespace %s, so git has no token for %s; create it: %s",
			name, p.Namespace, remote, create)
	case gs.secret == nil:
		return nil
	default:
		if strings.TrimSpace(string(gs.secret.Data["token"])) != "" {
			return nil
		}
		if _, isApp, err := scm.GitHubAppCredentialsFromData(gs.secret.Data); isApp || err != nil {
			// GitHub App credentials: the steps mint the token, and report
			// credentials they cannot mint one with.
			return nil
		}
		fix := fmt.Sprintf(`kubectl patch secret %s -n %s --type merge -p '{"stringData":{"token":"<TOKEN>"}}'`, name, p.Namespace)
		if gs.secret.Labels[labelReferenceable] != "true" {
			fix += fmt.Sprintf(" && kubectl label secret %s -n %s %s=true", name, p.Namespace, labelReferenceable)
		}
		msg = fmt.Sprintf("git Secret %s has no token key, so git has no token for %s; add it: %s",
			name, remote, fix)
	}
	return &metav1.Condition{
		Type: "Ready", Status: metav1.ConditionFalse, Reason: reasonGitCredentialMissing,
		Message: msg, ObservedGeneration: p.Generation,
	}
}

// withoutUserinfo is remote without its user name and password, or "the
// spec.git.url remote" when it does not parse.
func withoutUserinfo(remote string) string {
	u, err := url.Parse(strings.TrimSpace(remote))
	if err != nil {
		return "the spec.git.url remote"
	}
	u.User = nil
	return u.String()
}
