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
			wantLog:      "network error",
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
