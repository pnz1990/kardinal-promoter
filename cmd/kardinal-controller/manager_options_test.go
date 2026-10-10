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

// Package main (controller) — manager options tests (spec #920, #574, issue-983).
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestManagerOptions checks the options main passes to ctrl.NewManager. The
// old test built its own empty ctrl.Options and exercised a local recover()
// wrapper, so it would not notice a change to main (C07-controller-34).
func TestManagerOptions(t *testing.T) {
	opts := buildManagerOptions(managerConfig{
		metricsBindAddress:     ":8080",
		healthProbeBindAddress: ":8081",
		leaderElect:            true,
		watchNamespace:         "team-a",
	})

	// RecoverPanic unset means controller-runtime's default, which is true.
	if rp := opts.Controller.RecoverPanic; rp != nil {
		assert.True(t, *rp, "RecoverPanic must not be disabled (spec #920)")
	}
	require.NotNil(t, opts.GracefulShutdownTimeout)
	assert.Equal(t, 30*time.Second, *opts.GracefulShutdownTimeout)
	assert.Less(t, serverShutdownTimeout, *opts.GracefulShutdownTimeout,
		"HTTP servers must finish draining before the manager gives up on them")
	assert.Same(t, scheme, opts.Scheme)
	assert.Equal(t, ":8080", opts.Metrics.BindAddress)
	assert.Equal(t, ":8081", opts.HealthProbeBindAddress)
	assert.True(t, opts.LeaderElection)
	assert.Equal(t, "kardinal-promoter-leader", opts.LeaderElectionID)
	assert.True(t, opts.LeaderElectionReleaseOnCancel, "a leader that shuts down releases its Lease")
	assert.Equal(t, []string{"team-a"}, keys(opts.Cache.DefaultNamespaces))
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// recordingReader stands in for the informer cache and records every read.
type recordingReader struct {
	mu    sync.Mutex
	reads []string
}

func (r *recordingReader) record(obj any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads = append(r.reads, fmt.Sprintf("%T", obj))
}

func (r *recordingReader) Get(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	r.record(obj)
	return nil
}

func (r *recordingReader) List(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
	r.record(list)
	return nil
}

// TestManagerOptions_SecretsConfigMapsEventsReadLive covers C07-controller-21
// and the Event informer of C07-controller-11: Secret, ConfigMap and Event
// reads through the manager client went through the informer cache, which
// held every Secret in the cluster in memory and failed in namespace-scoped
// mode for the SCM token Secret and version ConfigMap in the controller
// namespace. They must go to the API server; CRD reads stay cached.
func TestManagerOptions_SecretsConfigMapsEventsReadLive(t *testing.T) {
	var mu sync.Mutex
	var live []string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		live = append(live, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/namespaces/kardinal-system/secrets/scm-token":
			_, _ = fmt.Fprint(w, `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"scm-token","namespace":"kardinal-system"}}`)
		case "/api/v1/namespaces/kardinal-system/configmaps/kardinal-version":
			_, _ = fmt.Fprint(w, `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"kardinal-version","namespace":"kardinal-system"}}`)
		case "/api/v1/namespaces/team-a/events":
			_, _ = fmt.Fprint(w, `{"apiVersion":"v1","kind":"EventList","metadata":{},"items":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer apiServer.Close()

	mapper := meta.NewDefaultRESTMapper(nil)
	for _, gvk := range []schema.GroupVersionKind{
		corev1.SchemeGroupVersion.WithKind("Secret"),
		corev1.SchemeGroupVersion.WithKind("ConfigMap"),
		corev1.SchemeGroupVersion.WithKind("Event"),
		v1alpha1.GroupVersion.WithKind("Pipeline"),
	} {
		mapper.Add(gvk, meta.RESTScopeNamespace)
	}

	// Build the client the way the manager does (cluster.New): the options'
	// client settings plus the cache as the reader.
	opts := buildManagerOptions(managerConfig{watchNamespace: "team-a"}).Client
	require.NotNil(t, opts.Cache)
	cacheOpts := *opts.Cache
	cached := &recordingReader{}
	cacheOpts.Reader = cached
	opts.Cache = &cacheOpts
	opts.Scheme = scheme
	opts.Mapper = mapper
	c, err := client.New(&rest.Config{Host: apiServer.URL}, opts)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "kardinal-system", Name: "scm-token"}, &corev1.Secret{}))
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "kardinal-system", Name: "kardinal-version"}, &corev1.ConfigMap{}))
	require.NoError(t, c.List(ctx, &corev1.EventList{}, client.InNamespace("team-a"),
		client.MatchingFields{"involvedObject.name": "app-v1-prod"}))
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "app"}, &v1alpha1.Pipeline{}))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{
		"/api/v1/namespaces/kardinal-system/secrets/scm-token?",
		"/api/v1/namespaces/kardinal-system/configmaps/kardinal-version?",
		"/api/v1/namespaces/team-a/events?fieldSelector=involvedObject.name%3Dapp-v1-prod",
	}, live, "Secret, ConfigMap and Event reads must go to the API server")
	assert.Equal(t, []string{"*v1alpha1.Pipeline"}, cached.reads, "CRD reads stay on the informer cache")
}

// TestLeaderElectionIDPerShard: each --namespace-shard elects its own leader.
func TestLeaderElectionIDPerShard(t *testing.T) {
	assert.Equal(t, "kardinal-promoter-leader", buildManagerOptions(managerConfig{}).LeaderElectionID)
	assert.Equal(t, "kardinal-promoter-leader-b", buildManagerOptions(managerConfig{namespaceShard: "b"}).LeaderElectionID)
}

// TestManagerOptions_LeaderElectionClient (#1592): the Lease is renewed
// with a config of its own: its own rate limiter (never the reconcilers'), on a copy,
// so reconcile traffic in the process cannot take its tokens. Without a
// config the manager's default is kept.
//
// Covers CHART-LEASE-APF-01.
func TestManagerOptions_LeaderElectionClient(t *testing.T) {
	shared := flowcontrol.NewTokenBucketRateLimiter(1, 1)
	base := &rest.Config{Host: "https://api.example", QPS: -1, UserAgent: "kardinal-promoter", RateLimiter: shared}
	opts := buildManagerOptions(managerConfig{leaderElect: true, restConfig: base})
	le := opts.LeaderElectionConfig
	require.NotNil(t, le)
	assert.NotSame(t, base, le)
	assert.Equal(t, "https://api.example", le.Host)
	assert.InDelta(t, leaderElectionQPS, le.QPS, 0)
	assert.Equal(t, leaderElectionBurst, le.Burst)
	assert.Nil(t, le.RateLimiter, "the reconcilers' limiter is not shared: the copy builds its own")
	assert.Equal(t, "kardinal-promoter", le.UserAgent, "controller-runtime appends /leader-election")
	assert.InDelta(t, -1, base.QPS, 0, "the controller's config is not changed")
	assert.Same(t, shared, base.RateLimiter)

	assert.Nil(t, buildManagerOptions(managerConfig{leaderElect: true}).LeaderElectionConfig)
}

// TestAuditRetentionDefault: AuditEvent retention is on unless turned off
// (--audit-retention=false); the chart passes the flag either way.
//
// Covers AUDIT-RETENTION-02.
func TestAuditRetentionDefault(t *testing.T) {
	assert.True(t, auditRetentionDefault, "unbounded AuditEvents fill etcd: retention must default to on")
}

// TestPprofBindAddress: --pprof-address is off by default, binds to
// 127.0.0.1 when it names no host, and reaches the manager as given
// otherwise.
//
// Covers INST-PPROF-01.
func TestPprofBindAddress(t *testing.T) {
	for _, tc := range []struct{ in, want, err string }{
		{in: "", want: ""},
		{in: ":6060", want: "127.0.0.1:6060"},
		{in: "localhost:6060", want: "localhost:6060"},
		{in: "0.0.0.0:6060", want: "0.0.0.0:6060"},
		{in: "[::1]:6060", want: "[::1]:6060"},
		{in: "6060", err: "missing port"},
		{in: "127.0.0.1:", err: "no port"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := pprofBindAddress(tc.in)
			if tc.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	assert.Empty(t, buildManagerOptions(managerConfig{}).PprofBindAddress, "no profiles unless asked")
	assert.Equal(t, "127.0.0.1:6060", buildManagerOptions(managerConfig{pprofAddress: "127.0.0.1:6060"}).PprofBindAddress)
}
