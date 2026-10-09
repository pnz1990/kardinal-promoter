// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestBuildControllerSCM_DataCenter: the controller's Bitbucket Data Center
// provider, static or dynamic, with --scm-allowed-repositories: the
// allowlist matches a Pipeline's URL in KEY/slug form (/scm/, browse, ssh,
// personal ~user), and every call the Guard lets through reaches the
// server, while a call for another project is refused before any request.
//
// Covers SCM-BBDC-06.
func TestBuildControllerSCM_DataCenter(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	hostname, _, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	allowed, err := scm.ParseRepositoryAllowlist([]string{hostname + "/PLAT/*", hostname + "/~alice/*"})
	require.NoError(t, err)
	pipeline := func(url string) *v1alpha1.Pipeline {
		p := &v1alpha1.Pipeline{}
		p.Spec.Git.URL = url
		p.Spec.Environments = []v1alpha1.EnvironmentSpec{{Name: "prod", Approval: "pr-review"}}
		return p
	}
	for _, dynamic := range []bool{false, true} {
		provider, dyn, canonical, err := buildControllerSCM(controllerSCMConfig{
			providerType: "bitbucket-datacenter", token: "t", apiURL: srv.URL, dynamic: dynamic, allowed: allowed})
		require.NoError(t, err)
		assert.Equal(t, dynamic, dyn != nil)
		for _, url := range []string{srv.URL + "/scm/PLAT/web.git", srv.URL + "/projects/plat/repos/web/browse",
			"ssh://git@" + hostname + ":7999/plat/web.git", srv.URL + "/scm/~alice/tools.git"} {
			assert.NoError(t, canonical.CheckPipeline(pipeline(url), false), "dynamic=%v %s", dynamic, url)
		}
		assert.ErrorIs(t, canonical.CheckPipeline(pipeline(srv.URL+"/scm/OTHER/web.git"), false), scm.ErrRepositoryNotAllowed)

		before := requests.Load()
		_, _, err = provider.GetPRStatus(context.Background(), "scm/OTHER/web", 1)
		assert.ErrorIs(t, err, scm.ErrRepositoryNotAllowed)
		assert.Equal(t, before, requests.Load(), "a refused call sends no request")
		_, _, _ = provider.GetPRStatus(context.Background(), "scm/PLAT/web", 1)
		assert.Greater(t, requests.Load(), before, "an allowed call reaches the server")
	}

	_, _, none, err := buildControllerSCM(controllerSCMConfig{providerType: "bitbucket-datacenter", token: "t", apiURL: srv.URL})
	require.NoError(t, err)
	assert.Nil(t, none, "no --scm-allowed-repositories: no allowlist")
	_, _, _, err = buildControllerSCM(controllerSCMConfig{providerType: "bitbucket-datacenter", token: "t", allowed: allowed})
	assert.Error(t, err, "Data Center needs --scm-api-url")
}
