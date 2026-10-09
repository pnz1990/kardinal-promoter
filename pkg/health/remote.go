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
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	case cluster.Server == "":
		return fmt.Errorf("%w: clusters[].cluster.server is empty", ErrKubeconfigNotAllowed)
	}
	u, err := url.Parse(cluster.Server)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%w: clusters[].cluster.server must be an http or https URL", ErrKubeconfigNotAllowed)
	}
	return nil
}

// RemoteClusters builds and caches the health clients of remote clusters,
// one set per kubeconfig Secret. The cache key holds the Secret's UID and
// resourceVersion, so a rotated or recreated Secret gets new clients. It is
// safe for concurrent use.
type RemoteClusters struct {
	// Scheme decodes typed objects (Deployments) from remote clusters; nil
	// means client-go's scheme.
	Scheme *runtime.Scheme
	// Dial, when set, replaces the egress-guarded dialer (tests).
	Dial func(ctx context.Context, network, address string) (net.Conn, error)

	mu      sync.Mutex
	entries map[types.NamespacedName]remoteEntry
}

type remoteEntry struct {
	version  string
	detector *AutoDetector
}

// Detector returns the health adapters for the cluster the kubeconfig in
// secret's key selects. An error wrapping ErrKubeconfigNotAllowed means the
// kubeconfig is refused; any other error is a missing key.
func (r *RemoteClusters) Detector(secret *corev1.Secret, key string) (*AutoDetector, error) {
	if key == "" {
		key = DefaultKubeconfigKey
	}
	data, ok := secret.Data[key]
	if !ok || len(data) == 0 {
		return nil, fmt.Errorf("secret %q has no key %q", secret.Name, key)
	}
	id := types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}
	version := string(secret.UID) + "/" + secret.ResourceVersion + "/" + key

	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[id]; ok && e.version == version {
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
		r.entries = map[types.NamespacedName]remoteEntry{}
	}
	r.entries[id] = remoteEntry{version: version, detector: d}
	return d, nil
}
