// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package uiauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGuardedWriter_ChallengeIsTheAPIsRealm: a 401 recorded during a request
// (an authorization check that found no user) answers with the realm of the
// API the middleware guards, not always kardinal-ui (#1511 QA).
func TestGuardedWriter_ChallengeIsTheAPIsRealm(t *testing.T) {
	tests := []struct {
		name, challenge string
		denial          denial
		wantHeader      string
	}{
		{"bundle API 401", `Bearer realm="kardinal-bundle-api"`, denial{code: http.StatusUnauthorized, msg: "unauthorized"},
			`Bearer realm="kardinal-bundle-api"`},
		{"UI 401", `Bearer realm="kardinal-ui"`, denial{code: http.StatusUnauthorized, msg: "unauthorized"},
			`Bearer realm="kardinal-ui"`},
		{"403 has no challenge", `Bearer realm="kardinal-bundle-api"`, denial{code: http.StatusForbidden, msg: "forbidden"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, state := withRequestAuth(context.Background())
			state.record(tt.denial)
			rec := httptest.NewRecorder()
			gw := &guardedWriter{ResponseWriter: rec, state: state, challenge: tt.challenge}
			gw.WriteHeader(http.StatusCreated)
			assert.Equal(t, tt.denial.code, rec.Code)
			assert.Equal(t, tt.wantHeader, rec.Header().Get("Www-Authenticate"))
		})
	}
}
