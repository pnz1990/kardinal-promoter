// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package hack

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeVersionKubectl records its arguments and answers `kubectl version`
// with FAKE_VERSION_JSON, or fails like an unreachable server when it is
// empty. Every other call succeeds.
const fakeVersionKubectl = `#!/usr/bin/env bash
echo "kubectl $*" >> "$FAKE_LOG"
for a in "$@"; do
  if [ "$a" = version ]; then
    if [ -z "$FAKE_VERSION_JSON" ]; then
      echo "The connection to the server localhost:8080 was refused" >&2
      exit 1
    fi
    printf '%s\n' "$FAKE_VERSION_JSON"
    exit 0
  fi
done
exit 0
`

// versionJSON is `kubectl version -o json` output: a v1.36.5 client and a
// server with the given minor and gitVersion.
func versionJSON(minor, gitVersion string) string {
	return fmt.Sprintf(`{
  "clientVersion": {
    "major": "1",
    "minor": "36",
    "gitVersion": "v1.36.5",
    "platform": "linux/amd64"
  },
  "kustomizeVersion": "v5.7.1",
  "serverVersion": {
    "major": "1",
    "minor": %q,
    "gitVersion": %q,
    "platform": "linux/amd64"
  }
}`, minor, gitVersion)
}

// TestInstallKroRefusesOldKubernetes: hack/install-kro.sh reads the server
// version of the KUBE_CONTEXT cluster before it installs anything, and
// refuses Kubernetes older than 1.30, where kro's CRDs (CRD selectableFields)
// cannot be installed. A managed cluster's "30+" minor counts as 30, and a
// server it cannot read stops the script too. Covers UPG-OLDK8S-01.
func TestInstallKroRefusesOldKubernetes(t *testing.T) {
	const refused = "install-kro.sh: Kubernetes 1.29 is not supported: kro's CRDs need 1.30 or later (CRD selectableFields). Upgrade the cluster first."
	tests := []struct {
		name    string
		version string
		// wantErr is part of the error output, or "" when kro is installed.
		wantErr string
	}{
		{name: "1.29 refused", version: versionJSON("29", "v1.29.14"), wantErr: refused},
		{name: "GKE 29+ refused", version: versionJSON("29+", "v1.29.15-gke.1234000"), wantErr: refused},
		{name: "1.30 accepted", version: versionJSON("30", "v1.30.13")},
		{name: "EKS 30+ accepted", version: versionJSON("30+", "v1.30.14-eks-ce1d5eb")},
		{name: "1.37 accepted", version: versionJSON("37", "v1.37.0")},
		{name: "empty minor read from gitVersion", version: versionJSON("", "v1.31.2")},
		{name: "unreachable server refused", wantErr: "cannot read the Kubernetes server version (kubectl version failed)"},
		{
			name:    "no serverVersion refused",
			version: `{"clientVersion": {"major": "1", "minor": "36"}}`,
			wantErr: "cannot read the Kubernetes server version from kubectl version -o json",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "calls.log")
			out, err := runWithFakes(t, filepath.Join(repoRoot(t), "hack", "install-kro.sh"),
				map[string]string{"kubectl": fakeVersionKubectl, "helm": fakeRecorder},
				[]string{"FAKE_LOG=" + logPath, "FAKE_VERSION_JSON=" + tc.version, "KUBE_CONTEXT=kind-k130"})
			data, readErr := os.ReadFile(logPath)
			require.NoError(t, readErr)
			calls := strings.Split(strings.TrimSpace(string(data)), "\n")
			require.NotEmpty(t, calls)
			assert.Equal(t, "kubectl --context kind-k130 version -o json", calls[0],
				"the version check is the first call and uses KUBE_CONTEXT")

			if tc.wantErr != "" {
				var exit *exec.ExitError
				require.True(t, errors.As(err, &exit), "install-kro.sh must fail: %v\n%s", err, out)
				assert.Equal(t, 1, exit.ExitCode(), out)
				assert.Contains(t, out, tc.wantErr)
				assert.Len(t, calls, 1, "nothing is installed after the check fails: %v", calls)
				return
			}
			require.NoError(t, err, out)
			assert.Contains(t, out, "installed (graphs.kro.run served)")
			require.Greater(t, len(calls), 1)
			assert.True(t, strings.HasPrefix(calls[1], "helm --kube-context kind-k130 upgrade --install kro "), calls[1])
		})
	}
}
