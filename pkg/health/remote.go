// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
)

// DefaultKubeconfigKey is the Secret key health.kubeconfigSecretRef reads
// when its key is empty.
const DefaultKubeconfigKey = "kubeconfig"

// remoteTimeout bounds every request to a remote API server, so an
// unreachable cluster cannot hold a reconcile worker.
const remoteTimeout = 10 * time.Second

// ErrKubeconfigNotAllowed means a kubeconfig asks the controller to run a
// command or read one of its own files, or is not a usable kubeconfig. The
// step fails: waiting does not fix it.
var ErrKubeconfigNotAllowed = errors.New("kubeconfig not allowed")

// RemoteConfig turns a kubeconfig into a REST config for its current
// context. It refuses everything that would act inside the controller:
// exec and auth-provider plugins (arbitrary commands), and tokenFile and the
// file forms of certificates and keys (the controller's own files, such as
// its ServiceAccount token, sent to a server the kubeconfig names). Only
// inline credentials remain: a token, client certificate and key data,
// username and password. A proxy-url is refused too: the API server is
// dialled directly, through dial.
//
// dial is the connection dialer; nil means the egress guard's (no loopback,
// link-local or cloud metadata address).
func RemoteConfig(kubeconfig []byte, dial func(ctx context.Context, network, address string) (net.Conn, error)) (*rest.Config, error) {
	raw, err := clientcmd.Load(kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("%w: not a kubeconfig", ErrKubeconfigNotAllowed)
	}
	kctx, ok := raw.Contexts[raw.CurrentContext]
	if raw.CurrentContext == "" || !ok {
		return nil, fmt.Errorf("%w: no current-context", ErrKubeconfigNotAllowed)
	}
	cluster, ok := raw.Clusters[kctx.Cluster]
	if !ok {
		return nil, fmt.Errorf("%w: context %q names no cluster", ErrKubeconfigNotAllowed, raw.CurrentContext)
	}
	user, ok := raw.AuthInfos[kctx.AuthInfo]
	if !ok {
		user = clientcmdapi.NewAuthInfo()
	}
	if err := allowedKubeconfig(cluster, user); err != nil {
		return nil, err
	}
	// Only the current context, so no other entry (with a plugin or file)
	// can come into play.
	minimal := clientcmdapi.NewConfig()
	minimal.Clusters["c"] = cluster
	minimal.AuthInfos["u"] = user
	minimal.Contexts["c"] = &clientcmdapi.Context{Cluster: "c", AuthInfo: "u"}
	minimal.CurrentContext = "c"
	cfg, err := clientcmd.NewDefaultClientConfig(*minimal, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKubeconfigNotAllowed, err)
	}
	if dial == nil {
		dial = (&net.Dialer{Timeout: remoteTimeout, KeepAlive: 30 * time.Second, Control: egress.Control}).DialContext
	}
	cfg.Dial = dial
	cfg.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	cfg.Timeout = remoteTimeout
	cfg.QPS, cfg.Burst = 5, 10
	cfg.UserAgent = "kardinal-promoter-health"
	return cfg, nil
}

// allowedKubeconfig refuses plugins, file references and proxies.
func allowedKubeconfig(cluster *clientcmdapi.Cluster, user *clientcmdapi.AuthInfo) error {
	refuse := func(what string) error {
		return fmt.Errorf("%w: %s is not supported (only inline token, client certificate/key data, "+
			"or username and password)", ErrKubeconfigNotAllowed, what)
	}
	switch {
	case user.Exec != nil:
		return refuse("users[].user.exec")
	case user.AuthProvider != nil:
		return refuse("users[].user.auth-provider")
	case user.TokenFile != "":
		return refuse("users[].user.tokenFile")
	case user.ClientCertificate != "":
		return refuse("users[].user.client-certificate (use client-certificate-data)")
	case user.ClientKey != "":
		return refuse("users[].user.client-key (use client-key-data)")
	case cluster.CertificateAuthority != "":
		return refuse("clusters[].cluster.certificate-authority (use certificate-authority-data)")
	case cluster.ProxyURL != "":
		return refuse("clusters[].cluster.proxy-url")
	case cluster.InsecureSkipTLSVerify:
		return fmt.Errorf("%w: clusters[].cluster.insecure-skip-tls-verify is not supported: set certificate-authority-data",
			ErrKubeconfigNotAllowed)
	case cluster.Server == "":
		return fmt.Errorf("%w: clusters[].cluster.server is empty", ErrKubeconfigNotAllowed)
	}
	// https only: over http the health check would read an unauthenticated
	// answer anyone on the path can forge, and client-go drops the bearer
	// token there anyway.
	u, err := url.Parse(cluster.Server)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w: clusters[].cluster.server must be an https URL", ErrKubeconfigNotAllowed)
	}
	return nil
}

// Remote client cache bounds: an entry unused for remoteCacheTTL is dropped,
// and at most remoteCacheMax entries are kept (the least recently used goes).
const (
	remoteCacheTTL = time.Hour
	remoteCacheMax = 128
)

// RemoteClusters builds and caches the health clients of remote clusters,
// one set per kubeconfig Secret key. An entry is rebuilt when the Secret's
// UID or resourceVersion changes (rotation), dropped when the Secret is gone
// (Forget), and expires when unused. It is safe for concurrent use.
type RemoteClusters struct {
	// Scheme decodes typed objects (Deployments) from remote clusters; nil
	// means client-go's scheme.
	Scheme *runtime.Scheme
	// Dial, when set, replaces the egress-guarded dialer (tests).
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// NowFn returns the current time (tests); nil means time.Now.
	NowFn func() time.Time

	mu      sync.Mutex
	entries map[remoteKey]*remoteEntry
}

// remoteKey identifies one kubeconfig: a Secret and the key in it.
type remoteKey struct {
	namespace, name, key string
}

type remoteEntry struct {
	version  string
	detector *AutoDetector
	lastUsed time.Time
}

func (r *RemoteClusters) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now()
}

// Forget drops every cached client built from the Secret namespace/name.
func (r *RemoteClusters) Forget(namespace, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.entries {
		if k.namespace == namespace && k.name == name {
			delete(r.entries, k)
		}
	}
}

// Len is the number of cached client sets.
func (r *RemoteClusters) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// Detector returns the health adapters for the cluster the kubeconfig in
// secret's key selects. An error wrapping ErrKubeconfigNotAllowed means the
// kubeconfig is refused; any other error is a missing key. Errors never
// list the Secret's keys.
func (r *RemoteClusters) Detector(secret *corev1.Secret, key string) (*AutoDetector, error) {
	if key == "" {
		key = DefaultKubeconfigKey
	}
	data, ok := secret.Data[key]
	if !ok || len(data) == 0 {
		return nil, fmt.Errorf("secret %q has no key %q", secret.Name, key)
	}
	id := remoteKey{namespace: secret.Namespace, name: secret.Name, key: key}
	version := string(secret.UID) + "/" + secret.ResourceVersion
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[id]; ok && e.version == version {
		e.lastUsed = now
		return e.detector, nil
	}
	cfg, err := RemoteConfig(data, r.Dial)
	if err != nil {
		return nil, err
	}
	sch := r.Scheme
	if sch == nil {
		sch = clientgoscheme.Scheme
	}
	c, err := sigs_client.New(cfg, sigs_client.Options{Scheme: sch})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKubeconfigNotAllowed, err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKubeconfigNotAllowed, err)
	}
	d := NewAutoDetector(c, dyn)
	if r.entries == nil {
		r.entries = map[remoteKey]*remoteEntry{}
	}
	r.entries[id] = &remoteEntry{version: version, detector: d, lastUsed: now}
	r.evict(now)
	return d, nil
}

// evict drops expired entries, then the least recently used ones over the
// cap. r.mu is held.
func (r *RemoteClusters) evict(now time.Time) {
	for k, e := range r.entries {
		if now.Sub(e.lastUsed) > remoteCacheTTL {
			delete(r.entries, k)
		}
	}
	for len(r.entries) > remoteCacheMax {
		var oldest remoteKey
		var at time.Time
		first := true
		for k, e := range r.entries {
			if first || e.lastUsed.Before(at) {
				oldest, at, first = k, e.lastUsed, false
			}
		}
		delete(r.entries, oldest)
	}
}

// ClassifyRemoteError turns an error from a remote API server call into a
// short reason for status: the full error, which can quote the server's
// answer, belongs in the log only.
func ClassifyRemoteError(err error) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	var ne net.Error
	var dnsErr *net.DNSError
	var opErr *net.OpError
	switch {
	case errors.Is(err, egress.ErrBlockedAddress):
		return "destination address is not allowed"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "timed out"
	case errors.As(err, &dnsErr):
		return "host not found"
	case apierrors.IsUnauthorized(err):
		return "unauthorized (HTTP 401)"
	case apierrors.IsForbidden(err):
		return "forbidden (HTTP 403)"
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return "connection refused or unreachable"
	case strings.Contains(err.Error(), "x509:") || strings.Contains(err.Error(), "tls:"):
		return "TLS handshake failed"
	case apierrors.IsNotFound(err):
		return "API not found (HTTP 404)"
	case errors.As(err, &ue):
		return "request failed"
	}
	if st, ok := err.(apierrors.APIStatus); ok {
		return fmt.Sprintf("API error (HTTP %d)", st.Status().Code)
	}
	return "request failed"
}
