// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package promotionstep_test

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

// remoteCA is the PEM of the fake remote API server's certificate, base64.
// client-go sends credentials to https servers only.
var remoteCA string

// remoteKubeconfig is a token kubeconfig for server; user adds YAML to the
// user entry.
func remoteKubeconfig(server, user string) string {
	if user == "" {
		user = "token: remote-token"
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: spoke
  cluster: {server: %q, certificate-authority-data: %s}
users:
- name: u
  user: {%s}
contexts:
- name: spoke
  context: {cluster: spoke, user: u}
current-context: spoke
`, server, remoteCA, user)
}

func kubeconfigSecret(name, data string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: "uid-1", ResourceVersion: "1"},
		Data: map[string][]byte{"kubeconfig": []byte(data)}}
}

// fakeRemoteAPI serves the Argo CD Application argocd/custom as a remote API
// server would, counting requests and checking the bearer token.
func fakeRemoteAPI(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	app := argoApplication("custom", oldSHA)
	app.SetAPIVersion("argoproj.io/v1alpha1")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer remote-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/custom" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(app.Object)
	}))
	t.Cleanup(srv.Close)
	remoteCA = base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	return srv, &calls
}

// loopbackDial lets the test reach httptest servers, which the egress guard
// refuses.
var loopbackDial = (&net.Dialer{}).DialContext

// TestRemoteClusterHealth covers #1458: with health.kubeconfigSecretRef the
// health check reads the object in the kubeconfig's cluster, not the
// controller's. A missing Secret or an unreachable cluster is
// ClusterUnreachable: no health failure is counted and the step keeps
// checking. A kubeconfig that would run a command or read a controller file
// fails the step without contacting any server.
func TestRemoteClusterHealth(t *testing.T) {
	srv, calls := fakeRemoteAPI(t)
	closed := httptest.NewTLSServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	ref := &v1alpha1.KubeconfigSecretRef{Name: "spoke"}
	env := v1alpha1.EnvironmentSpec{Name: "test", Health: v1alpha1.HealthConfig{
		Type: "argocd", ArgoCD: &v1alpha1.HealthTargetRef{Name: "custom"}, KubeconfigSecretRef: ref}}

	tests := []struct {
		name      string
		secret    *corev1.Secret
		remote    *health.RemoteClusters
		wantState string
		wantMsg   string
		wantCalls bool
	}{
		{name: "the remote Application decides", secret: kubeconfigSecret("spoke", remoteKubeconfig(srv.URL, "")),
			remote: &health.RemoteClusters{Dial: loopbackDial}, wantState: "Verified", wantMsg: "health check passed via argocd", wantCalls: true},
		{name: "missing Secret waits", remote: &health.RemoteClusters{Dial: loopbackDial},
			wantState: "HealthChecking", wantMsg: `waiting for argocd: ClusterUnreachable: kubeconfig Secret "spoke" not found`},
		{name: "unreachable cluster waits", secret: kubeconfigSecret("spoke", remoteKubeconfig(closedURL, "")),
			remote: &health.RemoteClusters{Dial: loopbackDial}, wantState: "HealthChecking", wantMsg: "waiting for argocd: ClusterUnreachable: "},
		{name: "wrong token is not healthy", secret: kubeconfigSecret("spoke", remoteKubeconfig(srv.URL, "token: other")),
			remote: &health.RemoteClusters{Dial: loopbackDial}, wantState: "HealthChecking", wantMsg: "ClusterUnreachable", wantCalls: true},
		{name: "exec plugin fails the step", secret: kubeconfigSecret("spoke",
			remoteKubeconfig(srv.URL, "exec: {apiVersion: client.authentication.k8s.io/v1, command: /bin/sh}")),
			remote: &health.RemoteClusters{Dial: loopbackDial}, wantState: "Failed", wantMsg: "users[].user.exec is not supported"},
		{name: "tokenFile fails the step", secret: kubeconfigSecret("spoke",
			remoteKubeconfig(srv.URL, "tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token")),
			remote: &health.RemoteClusters{Dial: loopbackDial}, wantState: "Failed", wantMsg: "tokenFile is not supported"},
		{name: "egress guard refuses loopback", secret: kubeconfigSecret("spoke", remoteKubeconfig(srv.URL, "")),
			remote: &health.RemoteClusters{}, wantState: "HealthChecking", wantMsg: "destination address is not allowed"},
		{name: "controller without remote support fails", secret: kubeconfigSecret("spoke", remoteKubeconfig(srv.URL, "")),
			wantState: "Failed", wantMsg: "health.kubeconfigSecretRef is not supported by this controller"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := calls.Load()
			hc := healthCase{env: env, remote: tt.remote}
			if tt.secret != nil {
				hc.objs = []client.Object{tt.secret}
			}
			_, ps, _ := hc.run(t)
			assert.Equal(t, tt.wantState, ps.Status.State, ps.Status.Message)
			assert.Contains(t, ps.Status.Message, tt.wantMsg)
			assert.Zero(t, ps.Status.ConsecutiveHealthFailures, "an unreachable cluster is not a health failure")
			assert.NotContains(t, ps.Status.Message, "remote-token")
			if tt.wantCalls {
				assert.Greater(t, calls.Load(), before, "the remote API server was asked")
			} else {
				assert.Equal(t, before, calls.Load(), "no request to the remote API server")
			}
		})
	}
}

// TestRemoteClusters_CacheFollowsSecretVersion: the clients are reused while
// the Secret is unchanged and rebuilt when it is updated (rotation).
func TestRemoteClusters_CacheFollowsSecretVersion(t *testing.T) {
	r := &health.RemoteClusters{Dial: loopbackDial}
	s := kubeconfigSecret("spoke", remoteKubeconfig("https://spoke.example:6443", ""))
	d1, err := r.Detector(s, "")
	require.NoError(t, err)
	d2, err := r.Detector(s, "")
	require.NoError(t, err)
	assert.Same(t, d1, d2)
	s.ResourceVersion = "2"
	d3, err := r.Detector(s, "")
	require.NoError(t, err)
	assert.NotSame(t, d1, d3)
	_, err = r.Detector(s, "other")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `secret "spoke" has no key "other"`)
}
