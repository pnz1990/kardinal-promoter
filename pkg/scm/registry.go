// Copyright 2026 The kardinal-promoter Authors.
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

package scm

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ErrProviderGone is returned for a provider identity whose object no longer
// exists, or was deleted and created again (another UID): the PR was opened
// on an SCM that kardinal cannot reach the same way any more.
var ErrProviderGone = errors.New("the SCM provider the step started with is gone")

// ErrRepositoryNotAllowed is returned for a repository outside a provider's
// spec.allowedRepositories.
var ErrRepositoryNotAllowed = errors.New("repository is not in the SCM provider's spec.allowedRepositories")

// ErrNamespaceNotAllowed is returned for a ClusterScmProvider whose
// spec.allowedNamespaces does not select the Pipeline's namespace.
var ErrNamespaceNotAllowed = errors.New("the ClusterScmProvider does not allow this namespace")

// IsProviderRefError reports whether err says that a Pipeline's
// spec.git.providerRef cannot be used: the provider is missing or recreated,
// or it does not allow the namespace or the repository. Creating or changing
// the provider fixes it; nothing about the Bundle does.
func IsProviderRefError(err error) bool {
	return errors.Is(err, ErrProviderGone) || errors.Is(err, ErrRepositoryNotAllowed) || errors.Is(err, ErrNamespaceNotAllowed)
}

// ProviderSpec is a ScmProvider or ClusterScmProvider read from the API:
// what Registry needs to build its client.
type ProviderSpec struct {
	Identity v1alpha1.ScmProviderIdentity
	// Generation is the object's metadata.generation.
	Generation int64
	Spec       v1alpha1.ScmProviderSpec
	// SecretNamespace is where its Secrets are: the ScmProvider's namespace,
	// or a ClusterScmProvider's secretRef.namespace.
	SecretNamespace string
}

// GetProvider reads the provider kind/name: a ScmProvider in namespace ns,
// or a ClusterScmProvider. It returns ErrProviderGone (wrapped) when it does
// not exist.
func GetProvider(ctx context.Context, c client.Reader, ns, kind, name string) (ProviderSpec, *v1alpha1.ClusterScmProvider, error) {
	switch kind {
	case "", v1alpha1.KindScmProvider:
		var p v1alpha1.ScmProvider
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &p); err != nil {
			if apierrors.IsNotFound(err) {
				return ProviderSpec{}, nil, fmt.Errorf("the ScmProvider %s/%s is not found: %w", ns, name, ErrProviderGone)
			}
			return ProviderSpec{}, nil, fmt.Errorf("get ScmProvider %s/%s: %w", ns, name, err)
		}
		return ProviderSpec{
			Identity:        v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: name, UID: string(p.UID)},
			Generation:      p.Generation,
			Spec:            p.Spec,
			SecretNamespace: ns,
		}, nil, nil
	case v1alpha1.KindClusterScmProvider:
		var p v1alpha1.ClusterScmProvider
		if err := c.Get(ctx, types.NamespacedName{Name: name}, &p); err != nil {
			if apierrors.IsNotFound(err) {
				return ProviderSpec{}, nil, fmt.Errorf("the ClusterScmProvider %s is not found: %w", name, ErrProviderGone)
			}
			return ProviderSpec{}, nil, fmt.Errorf("get ClusterScmProvider %s: %w", name, err)
		}
		return ProviderSpec{
			Identity:        v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindClusterScmProvider, Name: name, UID: string(p.UID)},
			Generation:      p.Generation,
			Spec:            p.Spec.ScmProviderSpec,
			SecretNamespace: p.Spec.SecretRef.Namespace,
		}, &p, nil
	}
	return ProviderSpec{}, nil, fmt.Errorf("unknown SCM provider kind %q", kind)
}

// RepositoryAllowed reports whether repo matches one of globs: "*" matches
// one path segment, a trailing "/**" any number of them. No globs allows
// every repository. Matching ignores case.
func RepositoryAllowed(globs []string, repo string) bool {
	if len(globs) == 0 {
		return true
	}
	repo = strings.ToLower(strings.Trim(repo, "/"))
	for _, g := range globs {
		g = strings.ToLower(strings.Trim(g, "/"))
		if prefix, ok := strings.CutSuffix(g, "/**"); ok {
			segs := strings.Count(prefix, "/") + 1
			parts := strings.SplitN(repo, "/", segs+1)
			if len(parts) > segs {
				if m, _ := path.Match(prefix, strings.Join(parts[:segs], "/")); m {
					return true
				}
			}
			continue
		}
		if m, _ := path.Match(g, repo); m {
			return true
		}
	}
	return false
}

// Registry builds and caches one SCMProvider per ScmProvider or
// ClusterScmProvider. An entry is rebuilt when the object's generation or
// the resourceVersion of its token or webhook Secret changes, so a token
// rotation is picked up at the next use, as the controller's own Secret is
// by its SecretWatcher. It is safe for concurrent use.
type Registry struct {
	// Client reads the providers and their Secrets.
	Client client.Reader
	// New builds a provider; nil is NewProvider.
	New func(providerType, token, apiURL, webhookSecret string) (SCMProvider, error)

	mu      sync.Mutex
	entries map[string]registryEntry
}

type registryEntry struct {
	generation int64
	secretRVs  string
	provider   SCMProvider
}

// maxRegistryEntries bounds the cache; past it the cache is emptied.
const maxRegistryEntries = 1024

// Resolved is a provider client and the spec it was built from.
type Resolved struct {
	Provider SCMProvider
	Spec     ProviderSpec
}

// ForIdentity returns the client of the provider id, for a step or PRStatus
// in namespace ns, checking that the object still has id's UID and that repo
// is allowed. repo may be empty (a webhook before the event is parsed).
func (r *Registry) ForIdentity(ctx context.Context, ns string, id v1alpha1.ScmProviderIdentity, repo string) (Resolved, error) {
	spec, _, err := GetProvider(ctx, r.Client, ns, id.Kind, id.Name)
	if err != nil {
		return Resolved{}, err
	}
	if spec.Identity.UID != id.UID {
		return Resolved{}, fmt.Errorf("%s %s was deleted and created again since the Graph was built: %w", id.Kind, id.Name, ErrProviderGone)
	}
	if repo != "" && !RepositoryAllowed(spec.Spec.AllowedRepositories, repo) {
		return Resolved{}, fmt.Errorf("%s %s: %s: %w", id.Kind, id.Name, repo, ErrRepositoryNotAllowed)
	}
	p, err := r.client(ctx, spec)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Provider: p, Spec: spec}, nil
}

// ForSpec returns the client of a provider read with GetProvider.
func (r *Registry) ForSpec(ctx context.Context, spec ProviderSpec) (Resolved, error) {
	p, err := r.client(ctx, spec)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Provider: p, Spec: spec}, nil
}

// client returns the cached client of spec, building it when the object or
// its Secrets changed.
func (r *Registry) client(ctx context.Context, spec ProviderSpec) (SCMProvider, error) {
	token, tokenRV, err := r.secretValue(ctx, spec.SecretNamespace, spec.Spec.SecretRef, "token")
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", spec.Identity.Kind, spec.Identity.Name, err)
	}
	webhookSecret, webhookRV := "", ""
	if ref := spec.Spec.WebhookSecretRef; ref != nil {
		ns := spec.SecretNamespace
		if ref.Namespace != "" {
			ns = ref.Namespace
		}
		if webhookSecret, webhookRV, err = r.secretValue(ctx, ns, *ref, "secret"); err != nil {
			return nil, fmt.Errorf("%s %s: webhook secret: %w", spec.Identity.Kind, spec.Identity.Name, err)
		}
	}
	key := spec.Identity.UID
	rvs := tokenRV + "/" + webhookRV
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[key]; ok && e.generation == spec.Generation && e.secretRVs == rvs {
		return e.provider, nil
	}
	build := r.New
	if build == nil {
		build = NewProvider
	}
	p, err := build(spec.Spec.Type, token, spec.Spec.APIURL, webhookSecret)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", spec.Identity.Kind, spec.Identity.Name, err)
	}
	if r.entries == nil || len(r.entries) >= maxRegistryEntries {
		r.entries = map[string]registryEntry{}
	}
	r.entries[key] = registryEntry{generation: spec.Generation, secretRVs: rvs, provider: p}
	return p, nil
}

// secretValue reads key (defaultKey when ref.Key is empty) of the Secret ref
// names in ns, trimmed, and the Secret's resourceVersion.
func (r *Registry) secretValue(ctx context.Context, ns string, ref v1alpha1.ScmSecretKeyRef, defaultKey string) (string, string, error) {
	key := ref.Key
	if key == "" {
		key = defaultKey
	}
	var s corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
		return "", "", fmt.Errorf("read Secret %s/%s: %w", ns, ref.Name, err)
	}
	v, ok := s.Data[key]
	if !ok || strings.TrimSpace(string(v)) == "" {
		return "", "", fmt.Errorf("the Secret %s/%s has no %s key", ns, ref.Name, key)
	}
	return strings.TrimSpace(string(v)), s.ResourceVersion, nil
}
