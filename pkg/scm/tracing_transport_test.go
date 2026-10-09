// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestProviders_HTTPClientsAreTraced builds every SCM provider each way the
// controller does (the New*Provider constructors, NewProvider and the
// DynamicProvider the controller runs with, and a provider whose transport
// the Registry replaced with WithTransport) and checks that an API request
// emits a client span: a provider whose HTTP client lost the tracing
// transport fails here.
func TestProviders_HTTPClientsAreTraced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	host := mustHost(t, srv.URL)

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev); _ = tp.Shutdown(context.Background()) })

	repo := "org/project/repo"
	build := map[string]func() (scm.SCMProvider, error){
		"NewGitHubProvider":      func() (scm.SCMProvider, error) { return scm.NewGitHubProvider("t", srv.URL, ""), nil },
		"NewGitLabProvider":      func() (scm.SCMProvider, error) { return scm.NewGitLabProvider("t", srv.URL, ""), nil },
		"NewForgejoProvider":     func() (scm.SCMProvider, error) { return scm.NewForgejoProvider("t", srv.URL, ""), nil },
		"NewBitbucketProvider":   func() (scm.SCMProvider, error) { return scm.NewBitbucketProvider("t", srv.URL, ""), nil },
		"NewAzureDevOpsProvider": func() (scm.SCMProvider, error) { return scm.NewAzureDevOpsProvider("t", srv.URL, ""), nil },
		"NewBitbucketDCProvider": func() (scm.SCMProvider, error) { return scm.NewBitbucketDCProvider("t", srv.URL, ""), nil },
	}
	for _, typ := range []string{"github", "gitlab", "forgejo", "gitea", "bitbucket", "azuredevops", "bitbucket-datacenter"} {
		typ := typ
		build["NewProvider("+typ+")"] = func() (scm.SCMProvider, error) { return scm.NewProvider(typ, "t", srv.URL, "") }
		build["NewDynamicProvider("+typ+")"] = func() (scm.SCMProvider, error) { return scm.NewDynamicProvider(typ, "t", srv.URL, "") }
		// The Registry replaces a provider's transport with the egress one.
		build["WithTransport("+typ+")"] = func() (scm.SCMProvider, error) {
			p, err := scm.NewProvider(typ, "t", srv.URL, "")
			if err != nil {
				return nil, err
			}
			return scm.WithTransport(p, http.DefaultTransport.(*http.Transport).Clone()), nil
		}
	}
	for name, newProvider := range build {
		t.Run(name, func(t *testing.T) {
			p, err := newProvider()
			require.NoError(t, err)
			rec.Reset()
			r := repo
			if strings.Contains(name, "DC") || strings.Contains(name, "datacenter") {
				r = "PROJ/repo" // Data Center: <project key>/<repo slug>
			}
			_, _, _ = p.GetPRStatus(context.Background(), r, 1)
			var hosts []string
			for _, s := range rec.Ended() {
				for _, a := range s.Attributes() {
					if a.Key == "server.address" || a.Key == "net.peer.name" || a.Key == "http.host" {
						hosts = append(hosts, a.Value.AsString())
					}
				}
			}
			assert.Contains(t, hosts, host, "an API request emits a client span (spans: %d)", len(rec.Ended()))
		})
	}
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u.Hostname()
}
