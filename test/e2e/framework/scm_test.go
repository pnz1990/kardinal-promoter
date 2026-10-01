// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// TestProxiedStatus checks that the status of a text/plain error answer,
// which client-go keeps only in the error, is read back.
func TestProxiedStatus(t *testing.T) {
	for _, tc := range []struct {
		code        int
		contentType string
	}{
		{http.StatusUnauthorized, "text/plain; charset=utf-8"},
		{http.StatusBadRequest, "text/plain; charset=utf-8"},
		{http.StatusUnauthorized, "application/json"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", tc.contentType)
			w.WriteHeader(tc.code)
			_, _ = w.Write([]byte("invalid signature\n"))
		}))
		kube, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
		require.NoError(t, err)
		var code int
		res := kube.CoreV1().RESTClient().Post().Namespace(ControllerNamespace).
			Resource("services").Name(ControllerName+":webhook").SubResource("proxy").
			Suffix("webhook", "scm").Body([]byte("{}")).Do(context.Background()).StatusCode(&code)
		require.Error(t, res.Error(), tc.contentType)
		if code == 0 {
			code = proxiedStatus(res.Error())
		}
		assert.Equal(t, tc.code, code, tc.contentType)
		srv.Close()
	}
	assert.Zero(t, proxiedStatus(nil))
}
