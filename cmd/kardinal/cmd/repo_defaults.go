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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/validation"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Per-repository defaults (#1759): in an application's repository the
// pipeline and the namespace need not be repeated. Each is, in order: the
// argument or -n, the KARDINAL_PIPELINE or KARDINAL_NAMESPACE environment
// variable, the .kardinal.yaml file, then none (the namespace then comes
// from the kubeconfig, as before). The file never names a context or a
// cluster: which cluster a command talks to is never decided by a file in a
// repository someone else may have written.

const (
	repoDefaultsFile = ".kardinal.yaml"
	pipelineEnvVar   = "KARDINAL_PIPELINE"
	namespaceEnvVar  = "KARDINAL_NAMESPACE"
)

// repoDefaults is .kardinal.yaml.
type repoDefaults struct {
	Pipeline  string `json:"pipeline,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// findRepoDefaults returns the path of the .kardinal.yaml that applies to
// the working directory: the nearest one from it up to the root of its git
// repository (the first directory with a .git), never above; outside a
// repository, only the working directory's. "" when there is none.
func findRepoDefaults() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	var dirs []string
	for dir := wd; ; {
		dirs = append(dirs, dir)
		if exists(filepath.Join(dir, ".git")) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			dirs = dirs[:1] // not in a repository
			break
		}
		dir = parent
	}
	for _, dir := range dirs {
		if p := filepath.Join(dir, repoDefaultsFile); exists(p) {
			return p, nil
		}
	}
	return "", nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

// loadRepoDefaults reads the .kardinal.yaml that applies (findRepoDefaults),
// or none. Only pipeline and namespace may be set, each a DNS-1123 label.
func loadRepoDefaults() (repoDefaults, error) {
	var d repoDefaults
	path, err := findRepoDefaults()
	if err != nil || path == "" {
		return d, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return d, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.UnmarshalStrict(b, &d); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "unknown field") {
			msg += ": it may set pipeline and namespace only (a repository file never picks a cluster)"
		}
		return d, fmt.Errorf("%s: %s", path, msg)
	}
	for key, v := range map[string]string{"pipeline": d.Pipeline, "namespace": d.Namespace} {
		if err := checkDefaultName(key, v); err != nil {
			return d, fmt.Errorf("%s: %w", path, err)
		}
	}
	return d, nil
}

// checkDefaultName checks a default named what is a DNS-1123 label (or
// empty).
func checkDefaultName(what, v string) error {
	if v == "" {
		return nil
	}
	if errs := validation.IsDNS1123Label(v); len(errs) > 0 {
		return fmt.Errorf("%s %q is not a valid name: %s", what, v, strings.Join(errs, "; "))
	}
	return nil
}

// repoDefault is the default of key: environment variable envVar, else the
// file's value (field).
func repoDefault(envVar string, field func(repoDefaults) string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v, checkDefaultName(envVar, v)
	}
	d, err := loadRepoDefaults()
	if err != nil {
		return "", err
	}
	return field(d), nil
}

// defaultPipeline is the pipeline a command takes when none is given:
// KARDINAL_PIPELINE, else .kardinal.yaml's, else "".
func defaultPipeline() (string, error) {
	return repoDefault(pipelineEnvVar, func(d repoDefaults) string { return d.Pipeline })
}

// defaultNamespace is the namespace when -n is not given: KARDINAL_NAMESPACE,
// else .kardinal.yaml's, else "" (the kubeconfig's).
func defaultNamespace() (string, error) {
	return repoDefault(namespaceEnvVar, func(d repoDefaults) string { return d.Namespace })
}

// errNoPipeline is the error of a command given no pipeline and no default.
func errNoPipeline(cmd *cobra.Command) error {
	path := "kardinal <command>"
	if cmd != nil {
		path = cmd.Name()
	}
	return fmt.Errorf("a pipeline name is required: give it as an argument (%s <pipeline>), set %s, "+
		"or write \"pipeline: <name>\" in %s at the repository's root", path, pipelineEnvVar, repoDefaultsFile)
}

// withDefaultPipeline is args of a <pipeline> [<env>] command with the
// default pipeline filled in: no argument is the default pipeline, and one
// argument (with no environment flag) is an environment when the default
// pipeline has one of that name and is not itself called that; otherwise
// args are unchanged. Explicit arguments always win.
func withDefaultPipeline(args []string, flagEnv string) ([]string, error) {
	if len(args) > 1 || (len(args) == 1 && flagEnv != "") {
		return args, nil
	}
	def, err := defaultPipeline()
	if err != nil || def == "" {
		return args, err
	}
	if len(args) == 0 {
		return []string{def}, nil
	}
	if args[0] != def && slices.Contains(pipelineEnvironments(def), args[0]) {
		return []string{def, args[0]}, nil
	}
	return args, nil
}

// pipelineEnvironments is the environments of pipeline in the cluster, or
// none when it cannot be read.
func pipelineEnvironments(pipeline string) []string {
	c, ns, err := hintClient()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, sigs_client.ObjectKey{Namespace: ns, Name: pipeline}, &p); err != nil {
		return nil
	}
	return envNames(&p)
}

// pipelineArgs is the Args check of a <pipeline> [<env>...] command taking
// at most max arguments: with no argument, a default pipeline must exist.
func pipelineArgs(maxArgs int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.MaximumNArgs(maxArgs)(cmd, args); err != nil {
			return err
		}
		if len(args) > 0 {
			return nil
		}
		def, err := defaultPipeline()
		if err != nil {
			return err
		}
		if def == "" {
			return errNoPipeline(cmd)
		}
		return nil
	}
}

// pipelineOf is the pipeline of a <pipeline> command: args[0], else the
// default (pipelineArgs checked there is one).
func pipelineOf(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	return defaultPipeline()
}
