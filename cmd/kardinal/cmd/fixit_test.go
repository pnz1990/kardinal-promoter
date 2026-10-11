// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// withHintClient makes the error hints read c in namespace default.
func withHintClient(t *testing.T, c sigs_client.Client) {
	t.Helper()
	prev := hintClient
	hintClient = func() (sigs_client.Client, string, error) { return c, "default", nil }
	t.Cleanup(func() { hintClient = prev })
}

func pipelineObj(name string, envs ...string) *v1alpha1.Pipeline {
	p := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}}
	for _, e := range envs {
		p.Spec.Environments = append(p.Spec.Environments, v1alpha1.EnvironmentSpec{Name: e})
	}
	return p
}

// TestHumanError (C3): each common failure says how to fix it, and any
// other error is left as it is.
//
// Covers CLI-FIXIT-01.
func TestHumanError(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(pipelineObj("api", "test", "prod"), pipelineObj("web")).Build()
	withHintClient(t, c)
	globalContext = "kind-dev"
	t.Cleanup(func() { globalContext = "" })

	gr := schema.GroupResource{Group: "kardinal.io", Resource: "pipelines"}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"CRDs not installed", fmt.Errorf("list pipelines: %w", &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "kardinal.io", Kind: "Pipeline"}}),
			`kardinal is not installed in cluster "kind-dev" (the kardinal.io API is not served).` + "\nInstall it: " + installHint + "\nor run: kardinal doctor"},
		{"raw no matches text", errors.New(`no matches for kind "Pipeline" in version "kardinal.io/v1alpha1"`),
			`kardinal is not installed in cluster "kind-dev"`},
		{"unreachable", fmt.Errorf("list pipelines: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}),
			`cannot reach the API server for context "kind-dev"`},
		{"forbidden", apierrors.NewForbidden(gr, "", errors.New(`User "alice" cannot list resource "pipelines" in API group "kardinal.io" in the namespace "team-a"`)),
			"you (alice) cannot list pipelines in namespace team-a.\nAsk your cluster admin for the kardinal-promoter-viewer role"},
		{"pipeline not found", fmt.Errorf("get: %w", apierrors.NewNotFound(gr, "apo")),
			`pipeline "apo" not found in namespace "default"; pipelines here: api, web. Other namespaces: kardinal get pipelines -A`},
		{"lifecycle not found", fmt.Errorf("pause apo: pipeline default/apo: %w", lifecycle.ErrNotFound),
			`pipeline "apo" not found in namespace "default"; pipelines here: api, web.`},
		{"other errors unchanged", errors.New("create rollback bundle: conflict"), "create rollback bundle: conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HumanError(tc.err)
			require.Error(t, got)
			assert.Contains(t, got.Error(), tc.want)
		})
	}
	assert.NoError(t, HumanError(nil))
}

// TestFixitHints (C3): a missing environment lists the pipeline's
// environments, a typo'd pipeline or environment is not an empty result,
// and override without --gate lists the gates not passing.
//
// Covers CLI-FIXIT-01.
func TestFixitHints(t *testing.T) {
	gate := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "no-weekend-api-b1-prod", Namespace: "default",
		Labels: map[string]string{"kardinal.io/pipeline": "api", "kardinal.io/environment": "prod", "kardinal.io/bundle": "b1",
			"kardinal.io/gate-template": "no-weekend"}}}
	done := &v1alpha1.PolicyGate{ObjectMeta: metav1.ObjectMeta{Name: "freeze-api-b0-prod", Namespace: "default",
		Labels: map[string]string{"kardinal.io/pipeline": "api", "kardinal.io/environment": "prod", "kardinal.io/bundle": "b0",
			"kardinal.io/gate-template": "freeze"}}}
	b0 := &v1alpha1.Bundle{ObjectMeta: metav1.ObjectMeta{Name: "b0", Namespace: "default"}, Status: v1alpha1.BundleStatus{Phase: "Verified"}}
	c := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).WithObjects(pipelineObj("api", "test", "prod"), gate, done, b0).
		WithStatusSubresource(gate, done, b0).WithIndex(&v1alpha1.Bundle{}, lifecycle.IndexBundlePipeline, lifecycle.BundlePipeline).Build()
	withHintClient(t, c)

	_, _, err := requireEnv("kardinal rollback", []string{"api"}, "")
	assert.EqualError(t, err, "which environment? api has: test, prod. Give it after the pipeline: kardinal rollback api <env>")

	err = checkPipelineEnv(context.Background(), c, "default", "api", "staging")
	assert.EqualError(t, err, `pipeline api has no environment "staging"; it has: test, prod`)
	err = checkPipelineEnv(context.Background(), c, "default", "apo", "")
	assert.EqualError(t, err, `pipeline "apo" not found in namespace "default"; pipelines here: api. Other namespaces: kardinal get pipelines -A`)

	var buf fmtBuf
	err = historyFn(&buf, c, "default", "apo", "", 10)
	assert.ErrorContains(t, err, `pipeline "apo" not found`, "a typo is not an empty history")

	assert.EqualError(t, missingGateError("api", "prod"), "which gate? Gates not passing for api in prod: no-weekend. Pass --gate <name>",
		"the gate of a finished Bundle is not listed")
	assert.EqualError(t, missingGateError("api", "test"), "--gate is required: no gate of api in test is holding a promotion back now")

	empty := fake.NewClientBuilder().WithScheme(cliTestScheme(t)).Build()
	assert.EqualError(t, pipelineNotFound(context.Background(), empty, "default", "x"),
		`pipeline "x" not found in namespace "default"; namespace default has no pipelines. Other namespaces: kardinal get pipelines -A`)
}

// fmtBuf is a writer for the fn tests.
type fmtBuf struct{ b []byte }

func (f *fmtBuf) Write(p []byte) (int, error) { f.b = append(f.b, p...); return len(p), nil }
