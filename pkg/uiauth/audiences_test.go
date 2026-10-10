// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package uiauth

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func jwtWith(t *testing.T, payload string) string {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(payload)) + ".sig"
}

// TestAPIServerAudiences reads the aud claim of the controller's own token
// (a list or a string) on top of the well-known API server audiences.
func TestAPIServerAudiences(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name, token string
		want        []string
	}{
		{"list", jwtWith(t, `{"aud":["https://oidc.eks.example/id/X","api"]}`), []string{"https://oidc.eks.example/id/X", "api"}},
		{"string", jwtWith(t, `{"aud":"https://kubernetes.default.svc.cluster.local"}`), []string{"https://kubernetes.default.svc.cluster.local"}},
		{"not a JWT", "garbage", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(dir, tt.name)
			require.NoError(t, os.WriteFile(p, []byte(tt.token+"\n"), 0o600))
			got := APIServerAudiences(p)
			assert.Subset(t, got, wellKnownAPIServerAudiences)
			assert.Subset(t, got, tt.want)
		})
	}
	assert.Equal(t, wellKnownAPIServerAudiences, APIServerAudiences(filepath.Join(dir, "missing")))
}

// TestCheckAudiences: an API server audience in tokenReview.audiences is
// refused, pointing to the explicit opt-in.
func TestCheckAudiences(t *testing.T) {
	api := []string{"https://kubernetes.default.svc", "https://oidc.eks.example/id/X"}
	assert.NoError(t, CheckAudiences([]string{"kardinal-promoter", "ci"}, api))
	err := CheckAudiences([]string{"kardinal-promoter", "https://oidc.eks.example/id/X"}, api)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--tokenreview-accept-apiserver-audience")
}
