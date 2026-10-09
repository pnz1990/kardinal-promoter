// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package health_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/health"
)

func kubeconfig(cluster, user string) []byte {
	return []byte(fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {%s}
users:
- name: u
  user: {%s}
- name: other
  user: {exec: {apiVersion: client.authentication.k8s.io/v1, command: /bin/true}}
contexts:
- name: c
  context: {cluster: c, user: u}
current-context: c
`, cluster, user))
}

// TestRemoteConfig: only inline credentials of the current context are
// accepted; plugins, file references and proxies are refused with
// ErrKubeconfigNotAllowed. Another user's exec entry, not in the current
// context, is ignored.
func TestRemoteConfig(t *testing.T) {
	server := `server: "https://spoke.example:6443"`
	tests := []struct {
		name, cluster, user, wantErr string
	}{
		{name: "token", cluster: server, user: "token: t"},
		{name: "client certificate data", cluster: server + `, certificate-authority-data: ""`, user: "username: a, password: b"},
		{name: "exec", cluster: server, user: "exec: {apiVersion: client.authentication.k8s.io/v1, command: aws}", wantErr: "users[].user.exec"},
		{name: "auth-provider", cluster: server, user: "auth-provider: {name: gcp}", wantErr: "auth-provider"},
		{name: "tokenFile", cluster: server, user: "tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token", wantErr: "tokenFile"},
		{name: "client-certificate file", cluster: server, user: "client-certificate: /etc/x.crt", wantErr: "client-certificate"},
		{name: "client-key file", cluster: server, user: "client-key: /etc/x.key", wantErr: "client-key"},
		{name: "CA file", cluster: server + ", certificate-authority: /etc/ca.crt", user: "token: t", wantErr: "certificate-authority"},
		{name: "proxy-url", cluster: server + `, proxy-url: "http://127.0.0.1:3128"`, user: "token: t", wantErr: "proxy-url"},
		{name: "no server", cluster: `insecure-skip-tls-verify: true`, user: "token: t", wantErr: "server is empty"},
		{name: "not a URL", cluster: `server: "ftp://x"`, user: "token: t", wantErr: "http or https URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := health.RemoteConfig(kubeconfig(tt.cluster, tt.user), nil)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.True(t, errors.Is(err, health.ErrKubeconfigNotAllowed))
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "https://spoke.example:6443", cfg.Host)
			assert.Nil(t, cfg.ExecProvider)
			assert.NotNil(t, cfg.Dial, "the egress-guarded dialer")
			assert.NotZero(t, cfg.Timeout)
		})
	}
	for name, data := range map[string]string{"garbage": "{{{", "no current context": "apiVersion: v1\nkind: Config\n"} {
		_, err := health.RemoteConfig([]byte(data), nil)
		assert.ErrorIs(t, err, health.ErrKubeconfigNotAllowed, name)
	}
}
