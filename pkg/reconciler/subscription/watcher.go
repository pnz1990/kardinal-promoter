// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package subscription

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kardinalv1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/source"
)

// NewWatcher builds the watcher for a Subscription's source with the
// credentials read from its secretRef. It is the controller's WatcherFn.
func NewWatcher(sub *kardinalv1alpha1.Subscription, creds source.Credentials) (source.Watcher, error) {
	switch sub.Spec.Type {
	case kardinalv1alpha1.SubscriptionTypeImage:
		img := sub.Spec.Image
		if img == nil {
			return nil, fmt.Errorf("image subscription missing spec.image")
		}
		w := source.NewOCIWatcher(img.Registry, img.TagFilter)
		w.Filters = tagFilters(img.TagSelection)
		w.Strategy = string(img.Strategy)
		w.DiscoveryLimit = int(img.DiscoveryLimit)
		w.Credentials = creds
		return w, nil
	case kardinalv1alpha1.SubscriptionTypeGit:
		g := sub.Spec.Git
		if g == nil {
			return nil, fmt.Errorf("git subscription missing spec.git")
		}
		w := source.NewGitWatcher(g.RepoURL, g.Branch, g.PathGlob)
		w.DiscoveryLimit = int(g.DiscoveryLimit)
		w.LastRevision = sub.Status.LastSeenRevision
		w.Credentials = creds
		return w, nil
	case kardinalv1alpha1.SubscriptionTypeHelm:
		h := sub.Spec.Helm
		if h == nil {
			return nil, fmt.Errorf("helm subscription missing spec.helm")
		}
		w := source.NewHelmWatcher(h.RepoURL, h.Chart)
		w.Filters = tagFilters(h.TagSelection)
		w.Credentials = creds
		return w, nil
	default:
		return nil, fmt.Errorf("unknown subscription type %q", sub.Spec.Type)
	}
}

// tagFilters maps the spec filters to the watcher's.
func tagFilters(s kardinalv1alpha1.TagSelection) source.TagFilters {
	return source.TagFilters{
		Include:          s.TagFilter,
		Exclude:          s.ExcludeTagFilter,
		SemverConstraint: s.SemverConstraint,
		Allow:            s.AllowTags,
		Ignore:           s.IgnoreTags,
	}
}

// secretRefOf returns the source's spec secretRef, nil when none is set.
func secretRefOf(sub *kardinalv1alpha1.Subscription) *kardinalv1alpha1.SubscriptionSecretRef {
	switch sub.Spec.Type {
	case kardinalv1alpha1.SubscriptionTypeImage:
		if sub.Spec.Image != nil {
			return sub.Spec.Image.SecretRef
		}
	case kardinalv1alpha1.SubscriptionTypeGit:
		if sub.Spec.Git != nil {
			return sub.Spec.Git.SecretRef
		}
	case kardinalv1alpha1.SubscriptionTypeHelm:
		if sub.Spec.Helm != nil {
			return sub.Spec.Helm.SecretRef
		}
	}
	return nil
}

// Secret keys the Subscription reads.
const (
	keyUsername      = "username"
	keyPassword      = "password"
	keyToken         = "token"
	keySSHPrivateKey = corev1.SSHAuthPrivateKey // ssh-privatekey
	keyKnownHosts    = "known_hosts"
)

// resolveCredentials reads the Secret the source's secretRef names, in the
// Subscription's namespace. No secretRef is anonymous access. The error
// names the Secret and the keys, never a value.
func resolveCredentials(ctx context.Context, c client.Client, sub *kardinalv1alpha1.Subscription) (source.Credentials, error) {
	ref := secretRefOf(sub)
	if ref == nil || ref.Name == "" {
		return source.Credentials{}, nil
	}
	var secret corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: sub.Namespace, Name: ref.Name}, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return source.Credentials{}, fmt.Errorf("secretRef: Secret %q not found in namespace %q", ref.Name, sub.Namespace)
		}
		return source.Credentials{}, fmt.Errorf("secretRef: read Secret %q: %w", ref.Name, err)
	}
	d := secret.Data
	creds := source.Credentials{
		DockerConfigJSON: d[corev1.DockerConfigJsonKey],
		Username:         string(d[keyUsername]),
		Password:         string(d[keyPassword]),
		Token:            string(d[keyToken]),
		SSHPrivateKey:    d[keySSHPrivateKey],
		SSHKnownHosts:    d[keyKnownHosts],
	}
	if creds.IsZero() {
		return source.Credentials{}, fmt.Errorf("secretRef: Secret %q has none of the keys %s, %s and %s, %s, %s and %s",
			ref.Name, corev1.DockerConfigJsonKey, keyUsername, keyPassword, keyToken, keySSHPrivateKey, keyKnownHosts)
	}
	return creds, nil
}
