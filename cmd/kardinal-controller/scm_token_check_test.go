// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// TestCheckSCMTokenAtStartup covers the startup token check main runs whenever
// a token is set. Before #1275, main skipped it whenever --scm-token-secret-name
// was set, and the chart sets KARDINAL_SCM_TOKEN_SECRET_NAME with every
// GITHUB_TOKEN (asserted in test/helm TestChartValuesWireControllerFlags and
// TestChartGitHubTokenNotPlaintext), so no chart install ran it.
func TestCheckSCMTokenAtStartup(t *testing.T) {
	const secret = "s3cr3t-token-value"
	tests := []struct {
		name         string
		provider     string
		token        string
		scopes       string
		status       int
		wantRequests int32
		wantMissing  []string
		wantLog      string
	}{
		{
			name:         "github classic PAT without repo warns",
			provider:     "github",
			token:        secret,
			scopes:       "read:user",
			wantRequests: 1,
			wantMissing:  []string{"repo"},
			wantLog:      "SCM TOKEN SCOPE WARNING",
		},
		{
			name:         "default provider is github",
			provider:     "",
			token:        secret,
			scopes:       "read:user",
			wantRequests: 1,
			wantMissing:  []string{"repo"},
			wantLog:      "SCM TOKEN SCOPE WARNING",
		},
		{
			name:         "token from a Secret with a trailing newline is trimmed",
			provider:     "github",
			token:        secret + "\n",
			scopes:       "repo",
			wantRequests: 1,
		},
		{
			name:         "rejected token warns",
			provider:     "github",
			token:        secret,
			status:       http.StatusUnauthorized,
			wantRequests: 1,
			wantMissing:  []string{"<valid token>"},
			wantLog:      "SCM TOKEN SCOPE WARNING",
		},
		{
			name:         "server error is logged at debug and does not warn",
			provider:     "github",
			token:        secret,
			status:       http.StatusInternalServerError,
			wantRequests: 1,
			wantLog:      "network or HTTP error",
		},
		{
			name:     "empty token is not checked",
			provider: "github",
			token:    "  \n",
		},
		{
			name:     "bitbucket has no startup check",
			provider: "bitbucket",
			token:    secret,
			wantLog:  "not available for this provider",
		},
		{
			name:     "azuredevops has no startup check",
			provider: "azuredevops",
			token:    secret,
			wantLog:  "not available for this provider",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal(t, "/user", r.URL.Path)
				assert.Equal(t, "Bearer "+secret, r.Header.Get("Authorization"),
					"the token must be sent trimmed")
				if tt.status != 0 {
					w.WriteHeader(tt.status)
					return
				}
				w.Header().Set("X-OAuth-Scopes", tt.scopes)
				_, _ = w.Write([]byte(`{"login":"bot"}`))
			}))
			defer srv.Close()

			var buf bytes.Buffer
			logger := zerolog.New(&buf).Level(zerolog.DebugLevel)
			warnings := checkSCMTokenAtStartup(context.Background(), logger, tt.provider, tt.token, srv.URL)

			assert.Equal(t, tt.wantRequests, requests.Load())
			var missing []string
			for _, w := range warnings {
				missing = append(missing, w.MissingScope)
			}
			assert.Equal(t, tt.wantMissing, missing)
			if tt.wantLog != "" {
				assert.Contains(t, buf.String(), tt.wantLog)
			}
			require.NotContains(t, buf.String(), secret, "the token must never be logged")
		})
	}
}

// TestCheckSCMTokenAtStartup_NotAvailable checks the startup token check for
// providers without a validator: no request reaches the SCM API, neither from
// the check nor from building the provider, and exactly one info line says
// the check is not available and where token problems show instead. The token
// is not logged, and an empty token logs nothing. Covers SCM-BB-06.
func TestCheckSCMTokenAtStartup_NotAvailable(t *testing.T) {
	const token = "s3cr3t-token-value"
	for _, provider := range []string{"bitbucket", "azuredevops"} {
		t.Run(provider, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				t.Errorf("unexpected request %s %s", r.Method, r.URL)
			}))
			defer srv.Close()

			// main builds the provider, then starts the check.
			_, err := scm.NewProvider(provider, token+"\n", srv.URL, "")
			require.NoError(t, err)
			_, err = scm.NewDynamicProvider(provider, token+"\n", srv.URL, "")
			require.NoError(t, err)

			var buf bytes.Buffer
			logger := zerolog.New(&buf).Level(zerolog.DebugLevel)
			assert.Nil(t, checkSCMTokenAtStartup(context.Background(), logger, provider, token+"\n", srv.URL))
			assert.Zero(t, requests.Load(), "no SCM API request")

			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			require.Len(t, lines, 1, buf.String())
			assert.JSONEq(t, `{"level":"info","provider":"`+provider+`",`+
				`"message":"SCM token check at startup is not available for this provider; token problems show on the first promotion step"}`,
				lines[0])
			assert.NotContains(t, buf.String(), token, "the token must never be logged")

			buf.Reset()
			assert.Nil(t, checkSCMTokenAtStartup(context.Background(), logger, provider, " \n", srv.URL))
			assert.Empty(t, buf.String(), "no token, no check and no log line")
		})
	}
}

// TestCheckSCMTokenAtStartup_GiteaFamily covers the Forgejo and Gitea check.
// A token with only the documented scopes (write:repository, write:issue)
// cannot read /api/v1/user, which needs read:user, so the SCM answers 403.
// That is a working token whose scopes the call cannot show: it is logged at
// info, not as a debug "network error", and it names the configured provider.
func TestCheckSCMTokenAtStartup_GiteaFamily(t *testing.T) {
	const secret = "s3cr3t-token-value"
	tests := []struct {
		name        string
		provider    string
		status      int
		wantMissing []string
		wantLevel   string
		wantLog     string
		wantName    string // provider name the logged text must use
	}{
		{name: "forgejo documented scopes 403", provider: "forgejo", status: http.StatusForbidden,
			wantLevel: "info", wantLog: "token scopes not checked: /user returned 403 (the documented scopes don't include read:user)"},
		{name: "gitea documented scopes 403", provider: "gitea", status: http.StatusForbidden,
			wantLevel: "info", wantLog: "token scopes not checked: /user returned 403 (the documented scopes don't include read:user)"},
		{name: "forgejo token with read:user", provider: "forgejo", status: http.StatusOK},
		{name: "gitea rejected token warns and says Gitea", provider: "gitea", status: http.StatusUnauthorized,
			wantMissing: []string{"<valid token>"}, wantLevel: "warn", wantLog: "SCM TOKEN SCOPE WARNING", wantName: "Gitea"},
		{name: "forgejo rejected token warns and says Forgejo", provider: "forgejo", status: http.StatusUnauthorized,
			wantMissing: []string{"<valid token>"}, wantLevel: "warn", wantLog: "SCM TOKEN SCOPE WARNING", wantName: "Forgejo"},
		{name: "gitea server error is debug and says gitea", provider: "gitea", status: http.StatusInternalServerError,
			wantLevel: "debug", wantLog: "network or HTTP error", wantName: "gitea"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assert.Equal(t, "/api/v1/user", r.URL.Path)
				assert.Equal(t, "token "+secret, r.Header.Get("Authorization"))
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"message":"token does not have at least one of required scope(s)"}`))
			}))
			defer srv.Close()

			var buf bytes.Buffer
			logger := zerolog.New(&buf).Level(zerolog.DebugLevel)
			warnings := checkSCMTokenAtStartup(context.Background(), logger, tt.provider, secret, srv.URL)

			assert.Equal(t, int32(1), requests.Load())
			var missing []string
			for _, w := range warnings {
				missing = append(missing, w.MissingScope)
			}
			assert.Equal(t, tt.wantMissing, missing)
			out := buf.String()
			assert.NotContains(t, out, "network error —", "a reachable SCM is not a network error")
			if tt.wantLog == "" {
				assert.Empty(t, out, "a token that reads /user logs nothing")
				return
			}
			assert.Contains(t, out, tt.wantLog)
			assert.Contains(t, out, `"level":"`+tt.wantLevel+`"`)
			assert.Contains(t, out, `"provider":"`+tt.provider+`"`)
			if tt.wantName != "" {
				assert.Contains(t, out, tt.wantName)
			}
			other := map[string]string{"forgejo": "gitea", "gitea": "forgejo"}[tt.provider]
			assert.NotContains(t, strings.ToLower(strings.ReplaceAll(out, `"provider":"`+tt.provider+`"`, "")), other,
				"the logged text must name the configured provider only")
			require.NotContains(t, out, secret, "the token must never be logged")
		})
	}
}
