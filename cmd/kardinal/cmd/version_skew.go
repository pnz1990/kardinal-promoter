// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
)

// noSkewAnnotation on a command turns the version skew warning off: kardinal
// version prints both versions itself.
const noSkewAnnotation = "kardinal.io/no-version-skew-warning"

// skewCheckTimeout bounds the read of the controller's version, so a slow
// API server does not hold up the command for a warning.
const skewCheckTimeout = 2 * time.Second

// versionSkew is the version skew check of the running command (#1757): the
// first client the command builds reads the controller's version (the
// kardinal-version ConfigMap, as kardinal version does) and, when the CLI
// is a minor version apart from it, writes one warning to errOut.
type versionSkew struct {
	once sync.Once
	cmd  *cobra.Command
}

// skewCheck is the running command's check; the root command sets it before
// every command runs (useStyle).
var skewCheck *versionSkew

// check runs the check once, with c.
func (s *versionSkew) check(c sigs_client.Reader) {
	if s == nil {
		return
	}
	s.once.Do(func() {
		cli := buildInfoVersion()
		if structured() || s.cmd.Annotations[noSkewAnnotation] == "true" || releaseVersion(cli) == "" {
			return
		}
		ns := defaultControllerNamespace
		if f := s.cmd.Flags().Lookup("controller-namespace"); f != nil && f.Value.String() != "" {
			ns = f.Value.String()
		}
		ctx, cancel := context.WithTimeout(context.Background(), skewCheckTimeout)
		defer cancel()
		if msg := versionSkewWarning(cli, controllerVersion(ctx, c, ns), ns, runtime.GOOS, runtime.GOARCH); msg != "" {
			_, _ = fmt.Fprintln(errOut, msg)
		}
	})
}

// releaseVersion is v as a canonical semantic version ("v" added), or ""
// for a build that is not a release: not a version, a dev build
// (v0.1.0-dev) or a Go pseudo-version (go install at a commit).
func releaseVersion(v string) string {
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) || strings.Contains(semver.Prerelease(v), "dev") || module.IsPseudoVersion(v) {
		return ""
	}
	return v
}

// versionSkewWarning is the warning for CLI version cli and controller
// version controller (in namespace ns), or "" when they share a major and
// minor version or either is not a release. It says which side to upgrade:
// the controller (with the chart of the CLI's version) when the CLI is
// newer, else the CLI (the release binary of the controller's version for
// goos/goarch).
func versionSkewWarning(cli, controller, ns, goos, goarch string) string {
	c, k := releaseVersion(cli), releaseVersion(controller)
	if c == "" || k == "" || semver.MajorMinor(c) == semver.MajorMinor(k) {
		return ""
	}
	msg := fmt.Sprintf("warning: kardinal CLI %s talks to controller %s (in %s); some output may not match: ", cli, controller, ns)
	if semver.Compare(c, k) > 0 {
		return msg + "upgrade the controller: " + installCommand(c, ns)
	}
	file, exe := "kardinal", ""
	if goos == "windows" {
		file, exe = "kardinal.exe", ".exe"
	}
	return msg + fmt.Sprintf("upgrade the CLI: curl -Lo %s https://github.com/pnz1990/kardinal-promoter/releases/download/%s/kardinal-%s-%s%s",
		file, k, goos, goarch, exe)
}
