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
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/tools/clientcmd"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// Errors that say how to fix them (UX-AUDIT §3.4): the CLI's most common
// failures name the cluster, the user or the valid values, and the command
// that fixes them, instead of the client's raw error.

// hintClient is the client the error hints read the cluster with
// (buildClient); a test replaces it.
var hintClient = buildClient

// installHint is the command that installs kardinal-promoter.
const installHint = "helm install kardinal-promoter oci://ghcr.io/pnz1990/charts/kardinal-promoter -n kardinal-system --create-namespace"

// HumanError rewrites err, the error a command returned, into one that says
// how to fix it when it is one of the common failures: kardinal not
// installed, the API server unreachable, a forbidden request, or a pipeline
// that does not exist. Any other error is returned as it is.
func HumanError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case isNoMatch(err):
		return fmt.Errorf("kardinal is not installed in cluster %s (the kardinal.io API is not served).\n"+
			"Install it: %s\nor run: kardinal doctor", contextLabel(), installHint)
	case apierrors.IsForbidden(err):
		return forbiddenError(err)
	case isUnreachable(err):
		return fmt.Errorf("cannot reach the API server for context %s%s.\n"+
			"Check: kubectl cluster-info%s, or pass --context", contextLabel(), serverLabel(), contextFlag())
	}
	if name, ok := notFoundPipeline(err); ok {
		c, ns, cerr := hintClient()
		if cerr == nil {
			return pipelineNotFound(context.Background(), c, ns, name)
		}
	}
	// The lifecycle package (pause, rollback, promote, holds) reports a
	// missing pipeline as "pipeline <ns>/<name>: not found".
	if m := lifecycleNotFoundRE.FindStringSubmatch(err.Error()); m != nil && errors.Is(err, lifecycle.ErrNotFound) {
		if c, _, cerr := hintClient(); cerr == nil {
			return pipelineNotFound(context.Background(), c, m[1], m[2])
		}
	}
	return err
}

// lifecycleNotFoundRE parses lifecycle's missing-pipeline error.
var lifecycleNotFoundRE = regexp.MustCompile(`pipeline ([a-z0-9][a-z0-9.-]*)/([a-z0-9][a-z0-9.-]*): not found`)

// isNoMatch reports whether err is the client's "no matches for kind": the
// kardinal CRDs are not installed.
func isNoMatch(err error) bool {
	return meta.IsNoMatchError(err) || strings.Contains(err.Error(), "no matches for kind")
}

// isUnreachable reports whether err is a failure to reach the API server.
func isUnreachable(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"connection refused", "no such host", "i/o timeout", "dial tcp", "network is unreachable"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// forbiddenRE parses the API server's Forbidden message.
var forbiddenRE = regexp.MustCompile(`User "([^"]*)" cannot (\w+) resource "([^"]+)"(?: in API group "([^"]*)")?(?: in the namespace "([^"]+)")?`)

// forbiddenError says who cannot do what where, and the roles that allow it.
func forbiddenError(err error) error {
	m := forbiddenRE.FindStringSubmatch(err.Error())
	if m == nil {
		return fmt.Errorf("%w\nAsk your cluster admin for the kardinal-promoter-viewer (read) or kardinal-promoter-promoter (act) role, or pass -n", err)
	}
	where := "the cluster"
	if m[5] != "" {
		where = "namespace " + m[5]
	}
	return fmt.Errorf("you (%s) cannot %s %s in %s.\n"+
		"Ask your cluster admin for the kardinal-promoter-viewer role (read) or the kardinal-promoter-promoter role (ship, approve, roll back) there, or pass -n for another namespace",
		m[1], m[2], m[3], where)
}

// notFoundPipeline returns the name of the Pipeline a NotFound err is about.
func notFoundPipeline(err error) (string, bool) {
	var status apierrors.APIStatus
	if !errors.As(err, &status) || !apierrors.IsNotFound(err) {
		return "", false
	}
	d := status.Status().Details
	if d == nil || d.Name == "" || (d.Kind != "pipelines" && d.Kind != "Pipeline") {
		return "", false
	}
	return d.Name, true
}

// errPipelineNotFound is the error of a pipeline that does not exist.
type errPipelineNotFound struct {
	name, ns string
	others   []string
}

func (e *errPipelineNotFound) Error() string {
	msg := fmt.Sprintf("pipeline %q not found in namespace %q", e.name, e.ns)
	switch {
	case len(e.others) == 0:
		msg += fmt.Sprintf("; namespace %s has no pipelines", e.ns)
	default:
		msg += "; pipelines here: " + strings.Join(e.others, ", ")
	}
	return msg + ". Other namespaces: kardinal get pipelines -A"
}

// pipelineNotFound is the error for pipeline name, missing in ns: it lists
// the pipelines ns has.
func pipelineNotFound(ctx context.Context, c sigs_client.Reader, ns, name string) error {
	e := &errPipelineNotFound{name: name, ns: ns}
	var list v1alpha1.PipelineList
	if err := c.List(ctx, &list, sigs_client.InNamespace(ns)); err == nil {
		for _, p := range list.Items {
			e.others = append(e.others, p.Name)
		}
		sort.Strings(e.others)
	}
	return e
}

// requirePipeline returns pipelineNotFound when name does not exist in ns,
// so a command does not report an empty result for a typo.
func requirePipeline(ctx context.Context, c sigs_client.Reader, ns, name string) (*v1alpha1.Pipeline, error) {
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, sigs_client.ObjectKey{Namespace: ns, Name: name}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, pipelineNotFound(ctx, c, ns, name)
		}
		return nil, fmt.Errorf("get pipeline %s: %w", name, err)
	}
	return &p, nil
}

// envNames lists the environments of p, in spec order.
func envNames(p *v1alpha1.Pipeline) []string {
	out := make([]string, 0, len(p.Spec.Environments))
	for _, e := range p.Spec.Environments {
		out = append(out, e.Name)
	}
	return out
}

// envHint is the list of pipeline's environments for an error ("test, uat,
// prod"), read from the cluster within a short timeout; "" when it cannot be
// read.
func envHint(pipeline string) string {
	c, ns, err := hintClient()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var p v1alpha1.Pipeline
	if err := c.Get(ctx, sigs_client.ObjectKey{Namespace: ns, Name: pipeline}, &p); err != nil {
		return ""
	}
	return strings.Join(envNames(&p), ", ")
}

// contextLabel is the kubeconfig context the CLI uses, quoted for messages.
func contextLabel() string {
	if globalContext != "" {
		return fmt.Sprintf("%q", globalContext)
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if globalKubeconfig != "" {
		rules.ExplicitPath = globalKubeconfig
	}
	raw, err := rules.Load()
	if err != nil || raw.CurrentContext == "" {
		return "(current)"
	}
	return fmt.Sprintf("%q", raw.CurrentContext)
}

// serverLabel is " (<server URL>)" of the context in use, or "".
func serverLabel() string {
	cfg, _, err := buildRestConfig()
	if err != nil || cfg.Host == "" {
		return ""
	}
	return " (" + cfg.Host + ")"
}

// contextFlag is the --context of the kubectl command a message suggests.
func contextFlag() string {
	if globalContext == "" {
		return ""
	}
	return " --context " + globalContext
}

// checkPipelineEnv returns pipelineNotFound when pipeline does not exist in
// ns, and an error listing its environments when env is set and not one of
// them, so a typo is not mistaken for an empty result.
func checkPipelineEnv(ctx context.Context, c sigs_client.Reader, ns, pipeline, env string) error {
	p, err := requirePipeline(ctx, c, ns, pipeline)
	if err != nil {
		return err
	}
	if env == "" {
		return nil
	}
	envs := envNames(p)
	for _, e := range envs {
		if e == env {
			return nil
		}
	}
	return fmt.Errorf("pipeline %s has no environment %q; it has: %s", pipeline, env, strings.Join(envs, ", "))
}
