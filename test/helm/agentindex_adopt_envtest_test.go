//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/agentidentity"
)

// TestHelmInstallsTheIndex (#1929): Helm installs the Deployment before the
// AgentIndex, so a new controller runs while the install is still creating
// objects. Helm checks ownership before it starts, so an index the
// controller created meanwhile would fail the install with "already
// exists" (seen on e2e-live). With --agent-index-from-chart the controller
// never creates it: it reconciles throughout the install here, and the
// install succeeds; the controller then builds the chart's index. Deleted
// after the install, it is recreated with the release's Helm ownership and
// the next helm upgrade adopts it.
//
// Covers ACTOR-ADMIT-06.
func TestHelmInstallsTheIndex(t *testing.T) {
	require.NotEmpty(t, os.Getenv("KUBEBUILDER_ASSETS"))
	// The CRDs first, as the upgrade guide applies them before the chart.
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(repoRoot(t), "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	user, err := env.AddUser(envtest.User{Name: "admin", Groups: []string{"system:masters"}}, cfg)
	require.NoError(t, err)
	kubeconfig, err := user.KubeConfig()
	require.NoError(t, err)
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kc, kubeconfig, 0o600))
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, v1alpha1.AddToScheme(sch))
	c, err := client.New(cfg, client.Options{Scheme: sch})
	require.NoError(t, err)
	ctrlc := asController(t, cfg, sch)
	r := &agentidentity.IndexReconciler{Client: ctrlc, Name: "kardinal-promoter", ChartCreated: true,
		Guard: "kardinal-promoter-agent-index", Reader: ctrlc}

	// The controller reconciles all through the install.
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			_, _ = r.Reconcile(ctx, reconcile.Request{})
			time.Sleep(10 * time.Millisecond)
		}
	}()
	cache, config := t.TempDir(), t.TempDir()
	helm := func(args ...string) ([]byte, error) {
		cmd := exec.Command(helmBin(t), append(args, "--kubeconfig", kc)...)
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kc, "HELM_CACHE_HOME="+cache, "HELM_CONFIG_HOME="+config)
		return cmd.CombinedOutput()
	}
	out, err := helm("install", "kardinal-promoter", chartPath(t), "--namespace", releaseNS, "--create-namespace", "--skip-crds")
	stop()
	<-done
	require.NoError(t, err, "helm install, with the controller running:\n%s", out)

	_, err = r.Reconcile(context.Background(), reconcile.Request{})
	require.NoError(t, err)
	var idx v1alpha1.AgentIndex
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	assert.Equal(t, "kardinal-promoter", idx.Annotations["meta.helm.sh/release-name"], "the chart's index")
	require.NotEmpty(t, idx.Status.Conditions)
	assert.Equal(t, "Built", idx.Status.Conditions[0].Reason, "built by the controller")

	// Deleted after the install (with the guard unbound): the controller
	// recreates it with the release's Helm ownership, and the next helm
	// upgrade adopts it (#1959, QA).
	require.NoError(t, c.Delete(context.Background(), &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "kardinal-promoter-agent-index"}}))
	require.Eventually(t, func() bool {
		return c.DeleteAllOf(context.Background(), &v1alpha1.AgentIndex{}) == nil &&
			apierrors.IsNotFound(c.Get(context.Background(), client.ObjectKey{Name: "kardinal-promoter"}, &v1alpha1.AgentIndex{}))
	}, 30*time.Second, 500*time.Millisecond)
	_, err = r.Reconcile(context.Background(), reconcile.Request{})
	require.NoError(t, err)
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Name: "kardinal-promoter"}, &idx))
	assert.Equal(t, "kardinal-promoter", idx.Annotations["meta.helm.sh/release-name"])
	assert.Equal(t, releaseNS, idx.Annotations["meta.helm.sh/release-namespace"])
	out, err = helm("upgrade", "kardinal-promoter", chartPath(t), "--namespace", releaseNS, "--skip-crds")
	require.NoError(t, err, "helm upgrade adopts the recreated index and rebinds the guard:\n%s", out)
}
