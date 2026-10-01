// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package framework is the shared harness for the live e2e suites in
// test/e2e/live. The suites run against a real kind cluster set up by
// hack/e2e/up.sh: kro, the controller image built from the commit, and the
// components a suite needs (Argo CD, Flux, a git server, ...).
//
// A live test never skips. When the cluster or a component it needs is
// missing, the test fails: a skipped live test proves nothing.
package framework

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	czap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
	"github.com/kardinal-promoter/kardinal-promoter/test/kindcontext"
)

// Environment variables the harness reads.
const (
	// EnvCLI is the path of the kardinal CLI binary (default: kardinal on PATH).
	EnvCLI = "KARDINAL_E2E_CLI"
	// EnvArtifacts is the directory failure diagnostics are written to
	// (default: test/e2e/results under the working directory).
	EnvArtifacts = "KARDINAL_E2E_ARTIFACTS"
	// EnvKeep keeps test namespaces and git repos after the test when set to 1.
	EnvKeep = "KARDINAL_E2E_KEEP"
	// EnvWebhookURL is the controller's /webhook/scm URL as the git server
	// reaches it. Unset means the suite relies on PR polling.
	EnvWebhookURL = "KARDINAL_E2E_WEBHOOK_URL"
	// EnvWebhookSecret is the HMAC secret the controller verifies webhooks with.
	EnvWebhookSecret = "KARDINAL_E2E_WEBHOOK_SECRET"
)

// ControllerNamespace is where hack/e2e/up.sh installs the controller.
const ControllerNamespace = "kardinal-system"

// The controller-runtime client logs nothing useful here, and without a
// logger it prints a stack trace to warn that none was set.
func init() { ctrllog.SetLogger(czap.New(czap.WriteTo(io.Discard))) }

// Env holds the clients for the kind cluster named in KARDINAL_E2E_CONTEXT.
type Env struct {
	Context string
	Config  *rest.Config
	Client  client.Client
	Kube    kubernetes.Interface
	Dynamic dynamic.Interface
	// Git is the suite's git server (see gitserver.FromEnv).
	Git gitserver.Server
	cli string

	mu sync.Mutex
	// namespaces are the namespaces Namespace created (see beforeRepoDelete).
	namespaces []*testNamespace
}

// Scheme has the kardinal types plus core and apps.
func Scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		v1alpha1.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			panic(err)
		}
	}
	return s
}

// New connects to the kind context in KARDINAL_E2E_CONTEXT. It fails the
// test when the variable is unset or names a non-kind context.
func New(t *testing.T) *Env {
	t.Helper()
	kubeContext, ok, err := kindcontext.FromEnv(os.Getenv(kindcontext.EnvVar))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("%s is not set; live e2e tests need a kind cluster from hack/e2e/up.sh", kindcontext.EnvVar)
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		t.Fatalf("load kube context %s: %v", kubeContext, err)
	}
	cfg.QPS, cfg.Burst = 50, 100

	c, err := client.New(cfg, client.Options{Scheme: Scheme()})
	if err != nil {
		t.Fatalf("controller-runtime client: %v", err)
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("kubernetes client: %v", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("dynamic client: %v", err)
	}
	git, err := gitserver.FromEnv()
	if err != nil {
		t.Fatalf("git server: %v", err)
	}
	cli := os.Getenv(EnvCLI)
	if cli == "" {
		cli = "kardinal"
	}
	return &Env{Context: kubeContext, Config: cfg, Client: c, Kube: kube, Dynamic: dyn, Git: git, cli: cli}
}

// Kardinal runs the CLI against the test cluster and returns its combined
// output. It never fails the test; callers assert on err and the output.
func (e *Env) Kardinal(t *testing.T, namespace string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"--context", e.Context}, args...)
	if namespace != "" {
		full = append([]string{"-n", namespace}, full...)
	}
	out, err := exec.Command(e.cli, full...).CombinedOutput()
	t.Logf("$ kardinal %s\n%s", strings.Join(full, " "), out)
	return string(out), err
}

// MustKardinal is Kardinal that fails the test on a non-zero exit.
func (e *Env) MustKardinal(t *testing.T, namespace string, args ...string) string {
	t.Helper()
	out, err := e.Kardinal(t, namespace, args...)
	if err != nil {
		t.Fatalf("kardinal %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}
