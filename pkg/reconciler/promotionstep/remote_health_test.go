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
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: "uid-1", ResourceVersion: "1",
		Labels: map[string]string{"kardinal.io/referenceable": "true"}},
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
		switch r.Header.Get("Authorization") {
		case "Bearer remote-token":
		case "Bearer broken":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"kind":"Status","message":"SECRET-BODY from the server"}`))
			return
		default:
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
			remote: &health.RemoteClusters{Dial: loopbackDial}, wantState: "HealthChecking",
			wantMsg: "waiting for argocd: ClusterUnreachable: unauthorized (HTTP 401)", wantCalls: true},
		{name: "server body is not copied", secret: kubeconfigSecret("spoke", remoteKubeconfig(srv.URL, "token: broken")),
			remote: &health.RemoteClusters{Dial: loopbackDial}, wantState: "HealthChecking",
			wantMsg: "waiting for argocd: ClusterUnreachable: ", wantCalls: true},
		{name: "unlabelled Secret waits and is not read", secret: unlabelled(kubeconfigSecret("spoke", remoteKubeconfig(srv.URL, ""))),
			remote: &health.RemoteClusters{Dial: loopbackDial}, wantState: "HealthChecking",
			wantMsg: `ClusterUnreachable: SecretNotReferenceable: kubeconfig Secret "spoke" does not have the label kardinal.io/referenceable: "true"`},
		{name: "insecure-skip-tls-verify fails the step", secret: kubeconfigSecret("spoke", strings.Replace(
			remoteKubeconfig(srv.URL, ""), "certificate-authority-data: "+remoteCA, "insecure-skip-tls-verify: true", 1)),
			remote: &health.RemoteClusters{Dial: loopbackDial}, wantState: "Failed", wantMsg: "insecure-skip-tls-verify is not supported"},
		{name: "exec plugin fails the step", secret: kubeconfigSecret("spoke",
			remoteKubeconfig(srv.URL, "exec: {apiVersion: client.authentication.k8s.io/v1, command: /bin/sh}")),
			remote: &health.RemoteClusters{Dial: loopbackDial}, wantState: "Failed", wantMsg: "users[].user.exec is not supported"},
		{name: "tokenFile fails the step", secret: kubeconfigSecret("spoke",
			remoteKubeconfig(srv.URL, "tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token")),
			remote: &health.RemoteClusters{Dial: loopbackDial}, wantState: "Failed", wantMsg: "tokenFile is not supported"},
		{name: "egress guard refuses loopback", secret: kubeconfigSecret("spoke", remoteKubeconfig(srv.URL, "")),
			remote: &health.RemoteClusters{}, wantState: "HealthChecking", wantMsg: "ClusterUnreachable: destination address is not allowed"},
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
			assert.NotContains(t, ps.Status.Message, "SECRET-BODY", "the server's answer is never copied to status")
			assert.NotContains(t, ps.Status.Message, "127.0.0.1", "no address in status")
			if tt.wantCalls {
				assert.Greater(t, calls.Load(), before, "the remote API server was asked")
			} else {
				assert.Equal(t, before, calls.Load(), "no request to the remote API server")
			}
		})
	}
}

func unlabelled(s *corev1.Secret) *corev1.Secret {
	s.Labels = nil
	return s
}

// TestRemoteClusterHealth_UnreachableStopsBake (QA #1495): a bake whose
// window started 40 minutes ago must not complete on a check that cannot
// reach the cluster: the window stops, no failure is counted, and
// health.timeout bounds the wait for the next healthy check again.
func TestRemoteClusterHealth_UnreachableStopsBake(t *testing.T) {
	closed := httptest.NewTLSServer(http.NotFoundHandler())
	fakeRemoteAPI(t) // sets remoteCA
	closedURL := closed.URL
	closed.Close()
	started := metav1.NewTime(time.Now().Add(-40 * time.Minute))
	env := v1alpha1.EnvironmentSpec{Name: "test", Bake: &v1alpha1.BakeConfig{Minutes: 30},
		Health: v1alpha1.HealthConfig{Type: "argocd", Timeout: "10m", ArgoCD: &v1alpha1.HealthTargetRef{Name: "custom"},
			KubeconfigSecretRef: &v1alpha1.KubeconfigSecretRef{Name: "spoke"}}}
	hc := healthCase{env: env, remote: &health.RemoteClusters{Dial: loopbackDial},
		objs:   []client.Object{kubeconfigSecret("spoke", remoteKubeconfig(closedURL, ""))},
		status: v1alpha1.PromotionStepStatus{BakeStartedAt: &started, BakeElapsedMinutes: 39}}
	_, ps, _ := hc.run(t)
	assert.Equal(t, "HealthChecking", ps.Status.State, ps.Status.Message)
	assert.Nil(t, ps.Status.BakeStartedAt, "the bake window stopped")
	assert.Contains(t, ps.Status.Message, "bake: window stopped, waiting for argocd: ClusterUnreachable: ")
	assert.Zero(t, ps.Status.ConsecutiveHealthFailures)
	require.NotNil(t, ps.Status.HealthCheckExpiry)
	assert.WithinDuration(t, time.Now().Add(10*time.Minute), ps.Status.HealthCheckExpiry.Time, time.Minute,
		"health.timeout is in force again")
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
	assert.Equal(t, `secret "spoke" has no key "other"`, err.Error(), "the Secret's keys are not listed")

	// Two keys of one Secret are two entries; Forget drops both.
	s.Data["second"] = s.Data["kubeconfig"]
	_, err = r.Detector(s, "second")
	require.NoError(t, err)
	assert.Equal(t, 2, r.Len())
	r.Forget("default", "spoke")
	assert.Zero(t, r.Len())
}

// TestRemoteClusters_CacheExpires: an entry unused for an hour is dropped.
func TestRemoteClusters_CacheExpires(t *testing.T) {
	now := time.Now()
	r := &health.RemoteClusters{Dial: loopbackDial, NowFn: func() time.Time { return now }}
	a := kubeconfigSecret("a", remoteKubeconfig("https://a.example:6443", ""))
	_, err := r.Detector(a, "")
	require.NoError(t, err)
	now = now.Add(2 * time.Hour)
	b := kubeconfigSecret("b", remoteKubeconfig("https://b.example:6443", ""))
	_, err = r.Detector(b, "")
	require.NoError(t, err)
	assert.Equal(t, 1, r.Len(), "the expired entry was evicted")
}

// TestRemoteClusters_CacheEvictsLeastRecentlyUsed: at most 128 client sets
// are kept. The 129th evicts the least recently used one, not the oldest
// built: an entry used again stays.
func TestRemoteClusters_CacheEvictsLeastRecentlyUsed(t *testing.T) {
	now := time.Now()
	r := &health.RemoteClusters{Dial: loopbackDial, NowFn: func() time.Time { return now }}
	secrets := make([]*corev1.Secret, 129)
	for i := range secrets {
		secrets[i] = kubeconfigSecret(fmt.Sprintf("spoke-%03d", i), remoteKubeconfig(fmt.Sprintf("https://spoke-%03d.example:6443", i), ""))
	}
	first := map[int]*health.AutoDetector{}
	for i := range 128 {
		now = now.Add(time.Second)
		d, err := r.Detector(secrets[i], "")
		require.NoError(t, err)
		first[i] = d
	}
	require.Equal(t, 128, r.Len())
	// spoke-000 is used again, so spoke-001 is now the least recently used.
	now = now.Add(time.Second)
	d, err := r.Detector(secrets[0], "")
	require.NoError(t, err)
	require.Same(t, first[0], d)

	now = now.Add(time.Second)
	_, err = r.Detector(secrets[128], "")
	require.NoError(t, err)
	assert.Equal(t, 128, r.Len(), "the cap holds")

	d, err = r.Detector(secrets[0], "")
	require.NoError(t, err)
	assert.Same(t, first[0], d, "the recently used entry was kept")
	for i := 2; i < 128; i++ {
		d, err := r.Detector(secrets[i], "")
		require.NoError(t, err)
		assert.Same(t, first[i], d, "spoke-%03d was kept", i)
	}
	assert.Equal(t, 128, r.Len())
	d, err = r.Detector(secrets[1], "")
	require.NoError(t, err)
	assert.NotSame(t, first[1], d, "spoke-001, the least recently used, was evicted and is rebuilt")
}
