// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package kindcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantOK  bool
		wantErr bool
	}{
		{name: "unset skips", value: ""},
		{name: "kind context accepted", value: "kind-kardinal-e2e", want: "kind-kardinal-e2e", wantOK: true},
		{name: "EKS context refused", value: "arn:aws:eks:us-east-2:111111111111:cluster/prod", wantErr: true},
		{name: "plain name refused", value: "prod", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := FromEnv(tt.value)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestClusterTestsRequireE2ETag guards against cluster-mutating tests landing
// in plain `go test ./...`: every test/e2e file that builds a real client must
// carry the e2e build tag and take its context from KARDINAL_E2E_CONTEXT.
func TestClusterTestsRequireE2ETag(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "e2e", "*_test.go"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	markers := []string{"infraClient(", "clientcmd.", "dynamic.NewForConfig", "ctrl.GetConfig"}
	for _, f := range files {
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		src := string(data)
		for _, m := range markers {
			if strings.Contains(src, m) {
				assert.True(t, strings.HasPrefix(src, "//go:build e2e\n"),
					"%s uses %s and must start with //go:build e2e", f, m)
				break
			}
		}
		if strings.Contains(src, "clientcmd.") {
			assert.Contains(t, src, "kindcontext.FromEnv", "%s must take its kube context from %s", f, EnvVar)
			assert.NotContains(t, src, "RecommendedHomeFile", "%s must not fall back to ~/.kube/config", f)
		}
	}
}
