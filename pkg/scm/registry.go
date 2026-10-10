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
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/lru"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// ErrProviderGone is returned for a provider identity whose object no longer
// exists, or was deleted and created again (another UID): the PR was opened
// on an SCM that kardinal cannot reach the same way any more.
var ErrProviderGone = errors.New("the SCM provider the step started with is gone")

// ErrNamespaceNotAllowed is returned for a ClusterScmProvider whose
// spec.allowedNamespaces does not select the Pipeline's namespace.
var ErrNamespaceNotAllowed = errors.New("the ClusterScmProvider does not allow this namespace")

// ErrProviderConfig is returned for a provider that cannot be used as
// written: an http:// apiURL without --scm-providers-allow-http, or a Secret
// that is missing, lacks its key or is not labeled kardinal.io/referenceable.
var ErrProviderConfig = errors.New("the SCM provider cannot be used")

// ErrProviderURL is matched (errors.Is), besides ErrProviderConfig, by the
// error for a provider whose spec.apiURL is refused: not a URL, an
// unsupported scheme, or http:// without --scm-providers-allow-http.
// Unlike a Secret that is missing or not referenceable yet, only an edit of
// the provider's spec fixes it. Its message is not part of the error's.
var ErrProviderURL = errors.New("the SCM provider's apiURL is refused")

// urlError is a checkURL error: it is ErrProviderConfig (wrapped) and
// ErrProviderURL.
type urlError struct{ error }

func (e urlError) Unwrap() error        { return e.error }
func (e urlError) Is(target error) bool { return target == ErrProviderURL }

// LabelReferenceable must be "true" on every Secret a ScmProvider or
// ClusterScmProvider names: a user who may create providers but not read
// Secrets must not be able to send a Secret of the namespace to an apiURL of
// their choice. The same opt-in applies to every Secret a custom resource
// references (docs/guides/security.md).
const LabelReferenceable = "kardinal.io/referenceable"

// IsProviderRefError reports whether err says that a Pipeline's
// spec.git.providerRef cannot be used: the provider is missing or recreated,
// it does not allow the namespace or the repository, or it is misconfigured.
// Creating or changing the provider or its Secret fixes it; nothing about
// the Bundle does.
func IsProviderRefError(err error) bool {
	return errors.Is(err, ErrProviderGone) || errors.Is(err, ErrRepositoryNotAllowed) ||
		errors.Is(err, ErrNamespaceNotAllowed) || errors.Is(err, ErrProviderConfig)
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
	// AllowedNamespaces is a ClusterScmProvider's selector; nil for a
	// ScmProvider.
	AllowedNamespaces *metav1.LabelSelector
}

// GetProvider reads the provider kind/name: a ScmProvider in namespace ns,
// or a ClusterScmProvider. It returns ErrProviderGone (wrapped) when it does
// not exist.
func GetProvider(ctx context.Context, c client.Reader, ns, kind, name string) (ProviderSpec, error) {
	switch kind {
	case "", v1alpha1.KindScmProvider:
		var p v1alpha1.ScmProvider
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &p); err != nil {
			if apierrors.IsNotFound(err) {
				return ProviderSpec{}, fmt.Errorf("the ScmProvider %s/%s is not found: %w", ns, name, ErrProviderGone)
			}
			return ProviderSpec{}, fmt.Errorf("get ScmProvider %s/%s: %w", ns, name, err)
		}
		return ProviderSpec{
			Identity:        v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindScmProvider, Name: name, UID: string(p.UID)},
			Generation:      p.Generation,
			Spec:            p.Spec,
			SecretNamespace: ns,
		}, nil
	case v1alpha1.KindClusterScmProvider:
		var p v1alpha1.ClusterScmProvider
		if err := c.Get(ctx, types.NamespacedName{Name: name}, &p); err != nil {
			if apierrors.IsNotFound(err) {
				return ProviderSpec{}, fmt.Errorf("the ClusterScmProvider %s is not found: %w", name, ErrProviderGone)
			}
			return ProviderSpec{}, fmt.Errorf("get ClusterScmProvider %s: %w", name, err)
		}
		return ProviderSpec{
			Identity:          v1alpha1.ScmProviderIdentity{Kind: v1alpha1.KindClusterScmProvider, Name: name, UID: string(p.UID)},
			Generation:        p.Generation,
			Spec:              p.Spec.ScmProviderSpec,
			SecretNamespace:   p.Spec.SecretRef.Namespace,
			AllowedNamespaces: p.Spec.AllowedNamespaces,
		}, nil
	}
	return ProviderSpec{}, fmt.Errorf("unknown SCM provider kind %q", kind)
}

// ProviderAllowlist is the provider's spec.allowedRepositories as a
// RepositoryAllowlist on the provider's host (WebHost), so a provider's
// globs are matched like --scm-allowed-repositories: case-insensitive, "*"
// one segment, a trailing "/**" any depth, smuggled segments refused. nil
// (no globs) allows every repository.
func ProviderAllowlist(spec ProviderSpec) (*RepositoryAllowlist, string, error) {
	host, err := WebHost(spec.Spec.Type, spec.Spec.APIURL)
	if err != nil {
		return nil, "", fmt.Errorf("%s %s: %v: %w", spec.Identity.Kind, spec.Identity.Name, err, ErrProviderConfig)
	}
	patterns := make([]string, 0, len(spec.Spec.AllowedRepositories))
	for _, g := range spec.Spec.AllowedRepositories {
		patterns = append(patterns, host+"/"+strings.Trim(g, "/"))
	}
	a, err := ParseRepositoryAllowlist(patterns)
	if err != nil {
		return nil, "", fmt.Errorf("%s %s: spec.allowedRepositories: %v: %w", spec.Identity.Kind, spec.Identity.Name, err, ErrProviderConfig)
	}
	// A Bitbucket Data Center repository is matched as KEY/slug, however
	// the Pipeline's URL names it (/scm/, browse or ssh path).
	return a.WithProviderType(spec.Spec.Type), host, nil
}

// RepositoryAllowed returns an error wrapping ErrRepositoryNotAllowed when
// repo does not match the provider's spec.allowedRepositories on its host
// (ProviderAllowlist).
func RepositoryAllowed(spec ProviderSpec, repo string) error {
	a, host, err := ProviderAllowlist(spec)
	if err != nil {
		return err
	}
	if !a.AllowsRepo(host, repo) {
		return fmt.Errorf("%s %s: repository %s is not in its spec.allowedRepositories: %w",
			spec.Identity.Kind, spec.Identity.Name, repo, ErrRepositoryNotAllowed)
	}
	return nil
}

// Registry defaults.
const (
	// DefaultSecretTTL is how long a provider Secret read is reused. A
	// rotated token is used within this time.
	DefaultSecretTTL = 30 * time.Second
	// DefaultNamespaceTTL is how long a Namespace's labels are reused for a
	// ClusterScmProvider's allowedNamespaces. A namespace whose label is
	// removed loses the provider within this time.
	DefaultNamespaceTTL = 30 * time.Second
	// maxCacheEntries bounds the Secret and Namespace caches: a write drops
	// the expired entries, and past the bound the cache is emptied.
	maxCacheEntries = 4096
)

// maxRegistryClients bounds the client cache (least recently used out). A
// variable so a test can lower it.
var maxRegistryClients = 512

// Registry builds and caches one SCMProvider per ScmProvider or
// ClusterScmProvider. Every lookup re-reads the provider (cached client),
// checks its UID, a ClusterScmProvider's allowedNamespaces against the
// namespace's labels, the repository against its allowedRepositories and its
// apiURL scheme; the Secret and Namespace reads behind those checks are
// reused for SecretTTL and NamespaceTTL. A client is rebuilt when the
// provider's generation or a Secret's resourceVersion changes, and every
// call it makes is checked against allowedRepositories again (Guard). It is
// safe for concurrent use.
type Registry struct {
	// Client reads the providers (through the manager cache).
	Client client.Reader
	// APIReader reads Secrets and Namespaces, which the manager does not
	// cache; nil uses Client.
	APIReader client.Reader
	// New builds a provider; nil is NewProvider.
	New func(providerType, token, apiURL, webhookSecret string) (SCMProvider, error)
	// Transport carries the providers' API requests. main sets
	// egress.NewTransport, so a provider's apiURL cannot reach loopback or
	// cloud metadata addresses; nil leaves the provider's own (tests).
	Transport http.RoundTripper
	// AllowHTTP lets a provider use an http:// apiURL
	// (--scm-providers-allow-http): an in-cluster SCM without TLS. Off, only
	// https:// is used, so a token never crosses the network in clear text.
	AllowHTTP bool
	// SecretTTL and NamespaceTTL default to DefaultSecretTTL and
	// DefaultNamespaceTTL.
	SecretTTL, NamespaceTTL time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	once    sync.Once
	clients *lru.Cache // UID -> registryEntry
	uids    sync.Map   // identityKey -> UID of its cached client

	mu         sync.Mutex
	secrets    map[types.NamespacedName]secretEntry
	namespaces map[string]namespaceEntry
	// secretsOf is the Secrets read for a provider (identityKey), which
	// Evict forgets with its client.
	secretsOf map[string][]types.NamespacedName
}

type registryEntry struct {
	generation int64
	secretRVs  string
	provider   SCMProvider
}

type secretEntry struct {
	secret  *corev1.Secret // nil: not found
	expires time.Time
}

type namespaceEntry struct {
	labels  map[string]string
	err     error
	expires time.Time
}

// Resolved is a provider client and the spec it was built from.
type Resolved struct {
	Provider SCMProvider
	Spec     ProviderSpec
}

func (r *Registry) init() {
	r.once.Do(func() { r.clients = lru.New(maxRegistryClients) })
}

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Registry) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// ForIdentity returns the client of the provider id, for a step or PRStatus
// in namespace ns: the object must still have id's UID, a ClusterScmProvider
// must still select ns, and repo (empty: not checked) must be allowed.
func (r *Registry) ForIdentity(ctx context.Context, ns string, id v1alpha1.ScmProviderIdentity, repo string) (Resolved, error) {
	spec, err := GetProvider(ctx, r.Client, ns, id.Kind, id.Name)
	if err != nil {
		return Resolved{}, err
	}
	if spec.Identity.UID != id.UID {
		return Resolved{}, fmt.Errorf("%s %s was deleted and created again since the Graph was built: %w", id.Kind, id.Name, ErrProviderGone)
	}
	if err := r.Check(ctx, spec, ns, repo); err != nil {
		return Resolved{}, err
	}
	p, err := r.client(ctx, spec)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Provider: p, Spec: spec}, nil
}

// Check applies the use checks of ForIdentity to spec for a Pipeline in
// namespace ns and repository repo (empty: not checked): a
// ClusterScmProvider's allowedNamespaces, the apiURL scheme and
// allowedRepositories. The translator calls it when it resolves a
// providerRef.
func (r *Registry) Check(ctx context.Context, spec ProviderSpec, ns, repo string) error {
	if spec.Identity.Kind == v1alpha1.KindClusterScmProvider {
		if err := r.namespaceAllowed(ctx, spec, ns); err != nil {
			return err
		}
	}
	if err := r.checkURL(spec); err != nil {
		return err
	}
	if repo != "" {
		return RepositoryAllowed(spec, repo)
	}
	return nil
}

// checkURL refuses an apiURL that is not https://, unless AllowHTTP.
func (r *Registry) checkURL(spec ProviderSpec) error {
	raw := strings.TrimSpace(spec.Spec.APIURL)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return urlError{fmt.Errorf("%s %s: spec.apiURL %q is not a URL: %w", spec.Identity.Kind, spec.Identity.Name, RedactURL(raw), ErrProviderConfig)}
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && r.AllowHTTP:
		return nil
	case u.Scheme == "http":
		return urlError{fmt.Errorf("%s %s: spec.apiURL %s is http://, which would send the token in clear text; use https://, "+
			"or ask the cluster admin to set scm.providersAllowInsecureHTTP for an in-cluster SCM without TLS: %w",
			spec.Identity.Kind, spec.Identity.Name, RedactURL(raw), ErrProviderConfig)}
	}
	return urlError{fmt.Errorf("%s %s: spec.apiURL scheme %q is not supported: %w", spec.Identity.Kind, spec.Identity.Name, u.Scheme, ErrProviderConfig)}
}

// namespaceAllowed checks a ClusterScmProvider's allowedNamespaces against
// the labels of namespace ns (cached for NamespaceTTL). No selector allows
// no namespace.
func (r *Registry) namespaceAllowed(ctx context.Context, spec ProviderSpec, ns string) error {
	name := spec.Identity.Name
	if spec.AllowedNamespaces == nil {
		return fmt.Errorf("ClusterScmProvider %s has no spec.allowedNamespaces: %w", name, ErrNamespaceNotAllowed)
	}
	sel, err := metav1.LabelSelectorAsSelector(spec.AllowedNamespaces)
	if err != nil {
		return fmt.Errorf("ClusterScmProvider %s: spec.allowedNamespaces: %v: %w", name, err, ErrNamespaceNotAllowed)
	}
	nsLabels, err := r.namespaceLabels(ctx, ns)
	if err != nil {
		return err
	}
	if !sel.Matches(labels.Set(nsLabels)) {
		return fmt.Errorf("ClusterScmProvider %s, namespace %s: %w", name, ns, ErrNamespaceNotAllowed)
	}
	return nil
}

func (r *Registry) namespaceLabels(ctx context.Context, ns string) (map[string]string, error) {
	now := r.now()
	r.mu.Lock()
	e, ok := r.namespaces[ns]
	r.mu.Unlock()
	if ok && now.Before(e.expires) {
		return e.labels, e.err
	}
	var n corev1.Namespace
	if err := r.apiReader().Get(ctx, types.NamespacedName{Name: ns}, &n); err != nil {
		// Not cached: retried at the next use.
		return nil, fmt.Errorf("read namespace %s: %w", ns, err)
	}
	ttl := r.NamespaceTTL
	if ttl <= 0 {
		ttl = DefaultNamespaceTTL
	}
	r.mu.Lock()
	for k, old := range r.namespaces {
		if !now.Before(old.expires) {
			delete(r.namespaces, k)
		}
	}
	if r.namespaces == nil || len(r.namespaces) >= maxCacheEntries {
		r.namespaces = map[string]namespaceEntry{}
	}
	r.namespaces[ns] = namespaceEntry{labels: n.Labels, expires: now.Add(ttl)}
	r.mu.Unlock()
	return n.Labels, nil
}

// ForSpec returns the client of a provider read with GetProvider.
func (r *Registry) ForSpec(ctx context.Context, spec ProviderSpec) (Resolved, error) {
	p, err := r.client(ctx, spec)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Provider: p, Spec: spec}, nil
}

// WebhookSecret returns the provider's webhook secret, reading only that
// Secret (cached for SecretTTL, found or not), so an unauthenticated webhook
// delivery reads neither the token nor the SCM.
func (r *Registry) WebhookSecret(ctx context.Context, spec ProviderSpec) (string, error) {
	ref := spec.Spec.WebhookSecretRef
	if ref == nil {
		return "", fmt.Errorf("%s %s has no spec.webhookSecretRef: %w", spec.Identity.Kind, spec.Identity.Name, ErrProviderConfig)
	}
	nn := types.NamespacedName{Namespace: webhookSecretNamespace(spec), Name: ref.Name}
	r.recordSecrets(spec, nn)
	v, _, err := r.secretValue(ctx, nn.Namespace, *ref, "secret")
	if err != nil {
		return "", fmt.Errorf("%s %s: webhook secret: %w", spec.Identity.Kind, spec.Identity.Name, err)
	}
	return v, nil
}

func webhookSecretNamespace(spec ProviderSpec) string {
	if ref := spec.Spec.WebhookSecretRef; ref != nil && ref.Namespace != "" {
		return ref.Namespace
	}
	return spec.SecretNamespace
}

// Evict drops the cached client of the provider kind/name (ns for a
// ScmProvider) and the Secrets read for it, for a provider that was
// deleted: its token and webhook secret are not kept in memory after it is
// gone.
func (r *Registry) Evict(kind, ns, name string) {
	r.init()
	key := identityKey(kind, ns, name)
	if uid, ok := r.uids.LoadAndDelete(key); ok {
		r.clients.Remove(uid)
	}
	r.mu.Lock()
	for _, nn := range r.secretsOf[key] {
		delete(r.secrets, nn)
	}
	delete(r.secretsOf, key)
	r.mu.Unlock()
}

// recordSecrets notes that the Secrets nns were read for spec, so Evict
// forgets them.
func (r *Registry) recordSecrets(spec ProviderSpec, nns ...types.NamespacedName) {
	key := identityKey(spec.Identity.Kind, spec.SecretNamespace, spec.Identity.Name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.secretsOf == nil || len(r.secretsOf) >= maxCacheEntries {
		r.secretsOf = map[string][]types.NamespacedName{}
	}
	have := r.secretsOf[key]
	for _, nn := range nns {
		found := false
		for _, h := range have {
			found = found || h == nn
		}
		if !found {
			have = append(have, nn)
		}
	}
	r.secretsOf[key] = have
}

func identityKey(kind, ns, name string) string {
	if kind == v1alpha1.KindClusterScmProvider {
		ns = ""
	}
	return kind + "/" + ns + "/" + name
}

// client returns the cached client of spec, building it when the object or
// its Secrets changed. The client is wrapped in Guard with the provider's
// allowedRepositories, so every call checks its repository again.
func (r *Registry) client(ctx context.Context, spec ProviderSpec) (SCMProvider, error) {
	r.init()
	if err := r.checkURL(spec); err != nil {
		return nil, err
	}
	r.recordSecrets(spec, types.NamespacedName{Namespace: spec.SecretNamespace, Name: spec.Spec.SecretRef.Name})
	if ref := spec.Spec.WebhookSecretRef; ref != nil {
		r.recordSecrets(spec, types.NamespacedName{Namespace: webhookSecretNamespace(spec), Name: ref.Name})
	}
	token, tokenRV, err := r.secretValue(ctx, spec.SecretNamespace, spec.Spec.SecretRef, "token")
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", spec.Identity.Kind, spec.Identity.Name, err)
	}
	webhookSecret, webhookRV := "", ""
	if ref := spec.Spec.WebhookSecretRef; ref != nil {
		if webhookSecret, webhookRV, err = r.secretValue(ctx, webhookSecretNamespace(spec), *ref, "secret"); err != nil {
			return nil, fmt.Errorf("%s %s: webhook secret: %w", spec.Identity.Kind, spec.Identity.Name, err)
		}
	}
	key := spec.Identity.UID
	rvs := tokenRV + "/" + webhookRV
	if v, ok := r.clients.Get(key); ok {
		if e := v.(registryEntry); e.generation == spec.Generation && e.secretRVs == rvs {
			return e.provider, nil
		}
	}
	build := r.New
	if build == nil {
		build = NewProvider
	}
	p, err := build(spec.Spec.Type, token, spec.Spec.APIURL, webhookSecret)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %v: %w", spec.Identity.Kind, spec.Identity.Name, err, ErrProviderConfig)
	}
	p = WithTransport(p, r.Transport)
	allow, host, err := ProviderAllowlist(spec)
	if err != nil {
		return nil, err
	}
	if allow != nil {
		p = allow.Guard(p, host)
	}
	r.clients.Add(key, registryEntry{generation: spec.Generation, secretRVs: rvs, provider: p})
	r.uids.Store(identityKey(spec.Identity.Kind, spec.SecretNamespace, spec.Identity.Name), key)
	return p, nil
}

// secretValue reads key (defaultKey when ref.Key is empty) of the Secret ref
// names in ns, trimmed, and the Secret's resourceVersion. The Secret must be
// labeled LabelReferenceable=true. Reads are reused for SecretTTL, a
// NotFound included.
func (r *Registry) secretValue(ctx context.Context, ns string, ref v1alpha1.ScmSecretKeyRef, defaultKey string) (string, string, error) {
	key := ref.Key
	if key == "" {
		key = defaultKey
	}
	s, err := r.secret(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name})
	if err != nil {
		return "", "", err
	}
	if s.Labels[LabelReferenceable] != "true" {
		return "", "", fmt.Errorf("the Secret %s/%s is not labeled %s=true; label it to let SCM providers use it: %w",
			ns, ref.Name, LabelReferenceable, ErrProviderConfig)
	}
	v, ok := s.Data[key]
	if !ok || strings.TrimSpace(string(v)) == "" {
		return "", "", fmt.Errorf("the Secret %s/%s has no %s key: %w", ns, ref.Name, key, ErrProviderConfig)
	}
	return strings.TrimSpace(string(v)), s.ResourceVersion, nil
}

func (r *Registry) secret(ctx context.Context, nn types.NamespacedName) (*corev1.Secret, error) {
	now := r.now()
	r.mu.Lock()
	e, ok := r.secrets[nn]
	r.mu.Unlock()
	if !ok || !now.Before(e.expires) {
		var s corev1.Secret
		err := r.apiReader().Get(ctx, nn, &s)
		e = secretEntry{expires: now.Add(r.secretTTL())}
		switch {
		case err == nil:
			e.secret = &s
		case apierrors.IsNotFound(err):
		default:
			// Not cached: an API error is retried at the next call.
			return nil, fmt.Errorf("read Secret %s: %w", nn, err)
		}
		r.mu.Lock()
		for k, old := range r.secrets {
			if !now.Before(old.expires) {
				delete(r.secrets, k)
			}
		}
		if r.secrets == nil || len(r.secrets) >= maxCacheEntries {
			r.secrets = map[types.NamespacedName]secretEntry{}
		}
		r.secrets[nn] = e
		r.mu.Unlock()
	}
	if e.secret == nil {
		return nil, fmt.Errorf("the Secret %s is not found: %w", nn, ErrProviderConfig)
	}
	return e.secret, nil
}

func (r *Registry) secretTTL() time.Duration {
	if r.SecretTTL > 0 {
		return r.SecretTTL
	}
	return DefaultSecretTTL
}

// Validate returns what keeps spec from being used, for its Ready
// condition: the apiURL scheme, the allowedRepositories globs, the
// allowedNamespaces selector, and the Secrets (present, labeled
// LabelReferenceable=true, with their keys). It reads the Secrets past the
// cache, so a fixed Secret turns the provider Ready at its next check.
func (r *Registry) Validate(ctx context.Context, spec ProviderSpec) []string {
	var problems []string
	if err := r.checkURL(spec); err != nil {
		problems = append(problems, "spec.apiURL: "+trimSentinel(err, ErrProviderConfig))
	}
	if _, _, err := ProviderAllowlist(spec); err != nil {
		problems = append(problems, trimSentinel(err, ErrProviderConfig))
	}
	if spec.AllowedNamespaces != nil {
		if _, err := metav1.LabelSelectorAsSelector(spec.AllowedNamespaces); err != nil {
			problems = append(problems, fmt.Sprintf("spec.allowedNamespaces: %v", err))
		}
	}
	// The Secrets Validate reads are cached like the ones a client reads:
	// recorded for the provider, so Evict forgets them when it is deleted.
	tokenNN := types.NamespacedName{Namespace: spec.SecretNamespace, Name: spec.Spec.SecretRef.Name}
	r.recordSecrets(spec, tokenNN)
	r.forgetSecret(tokenNN)
	if _, _, err := r.secretValue(ctx, spec.SecretNamespace, spec.Spec.SecretRef, "token"); err != nil {
		problems = append(problems, "spec.secretRef: "+trimSentinel(err, ErrProviderConfig))
	}
	if ref := spec.Spec.WebhookSecretRef; ref != nil {
		ns := webhookSecretNamespace(spec)
		r.recordSecrets(spec, types.NamespacedName{Namespace: ns, Name: ref.Name})
		r.forgetSecret(types.NamespacedName{Namespace: ns, Name: ref.Name})
		if _, _, err := r.secretValue(ctx, ns, *ref, "secret"); err != nil {
			problems = append(problems, "spec.webhookSecretRef: "+trimSentinel(err, ErrProviderConfig))
		}
	}
	return problems
}

func (r *Registry) forgetSecret(nn types.NamespacedName) {
	r.mu.Lock()
	delete(r.secrets, nn)
	r.mu.Unlock()
}

// trimSentinel returns err's text without ": <sentinel>" at its end.
func trimSentinel(err, sentinel error) string {
	return strings.TrimSuffix(err.Error(), ": "+sentinel.Error())
}
