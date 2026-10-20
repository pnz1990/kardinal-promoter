//go:build envtest

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package helm

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// TestPolicyNamespacesDocsOnAPIServer (#2286) runs docs/policy-gates.md's
// "Sharing gates with other namespaces" on a real API server and kubectl:
//
//   - the command that lists the Pipelines a later release refuses prints
//     exactly the Pipelines whose PolicyNamespacesShared condition is False
//     (with Ready beside it, the case a filter on conditions[*] breaks on),
//     not one whose condition is absent or has no status at all;
//   - the example ValidatingAdmissionPolicy refuses adding, changing or
//     removing kardinal.io/policy-shareable on a Namespace to anyone outside
//     platform-admins, and lets them; other label changes pass.
//
// Covers GATE-POLNS-03.
func TestPolicyNamespacesDocsOnAPIServer(t *testing.T) {
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join(repoRoot(t), "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	sch := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(sch))
	require.NoError(t, v1alpha1.AddToScheme(sch))
	admin, err := client.New(cfg, client.Options{Scheme: sch})
	require.NoError(t, err)
	ctx := context.Background()

	// The query.
	for _, ns := range []string{"team-a", "team-b"} {
		require.NoError(t, admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	}
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	cond := func(typ string, status metav1.ConditionStatus, reason string) metav1.Condition {
		return metav1.Condition{Type: typ, Status: status, Reason: reason, Message: "-", LastTransitionTime: now}
	}
	ready := cond("Ready", metav1.ConditionTrue, "Valid")
	for _, p := range []struct {
		ns, name   string
		conditions []metav1.Condition
	}{
		{"team-a", "refused", []metav1.Condition{ready, cond("PolicyNamespacesShared", metav1.ConditionFalse, "NotShared")}},
		{"team-b", "unreadable", []metav1.Condition{cond("PolicyNamespacesShared", metav1.ConditionFalse, "PolicyNamespaceUnreadable"), ready}},
		{"team-a", "shared", []metav1.Condition{ready}},
		{"team-b", "new", nil},
	} {
		pl := &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: p.name, Namespace: p.ns},
			Spec: v1alpha1.PipelineSpec{Git: v1alpha1.PipelineGit{URL: "https://github.com/acme/gitops"},
				Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}}, PolicyNamespaces: []string{"shared-gates"}}}
		require.NoError(t, admin.Create(ctx, pl))
		if p.conditions != nil {
			pl.Status.Conditions = p.conditions
			require.NoError(t, admin.Status().Update(ctx, pl))
		}
	}
	user, err := env.ControlPlane.AddUser(envtest.User{Name: "docs", Groups: []string{"system:masters"}}, nil)
	require.NoError(t, err)
	kc, err := user.KubeConfig()
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kubeconfig"), kc, 0o600))
	kubectl, err := user.Kubectl()
	require.NoError(t, err)
	require.NoError(t, os.Symlink(kubectl.Path, filepath.Join(dir, "kubectl")))
	var query string
	for _, block := range sharingSection(t, "bash") {
		if strings.HasPrefix(block, "kubectl get pipelines -A") {
			query = strings.TrimSpace(block)
		}
	}
	require.NotEmpty(t, query, "the section's kubectl get pipelines command")
	cmd := exec.Command("bash", "-o", "pipefail", "-c", query)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"KUBECONFIG="+filepath.Join(dir, "kubeconfig"))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "%s\n%s", query, stderr.String())
	t.Logf("$ %s\n%s", query, out)
	assert.Equal(t, "team-a/refused\nteam-b/unreadable\n", string(out))
	assert.Empty(t, stderr.String())

	// The admission policy.
	blocks := sharingSection(t, "yaml")
	require.Len(t, blocks, 1, "the section's admission policy")
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(blocks[0]), 4096)
	for {
		var obj unstructured.Unstructured
		if err := dec.Decode(&obj.Object); err != nil {
			break
		}
		require.NoError(t, admin.Create(ctx, &obj), obj.GetKind())
	}
	as := func(groups ...string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: "someone", Groups: append([]string{"system:masters"}, groups...)}
		cl, err := client.New(c, client.Options{Scheme: sch})
		require.NoError(t, err)
		return cl
	}
	tenant, platform := as(), as("platform-admins")
	label := func(c client.Client, ns, key, value string) error {
		var n corev1.Namespace
		if err := c.Get(ctx, client.ObjectKey{Name: ns}, &n); err != nil {
			return err
		}
		if n.Labels == nil {
			n.Labels = map[string]string{}
		}
		if value == "" {
			delete(n.Labels, key)
		} else {
			n.Labels[key] = value
		}
		return c.Update(ctx, &n)
	}
	// Policies take effect a moment after they are created: until then the
	// tenant's label is taken off again.
	var last error
	require.Eventually(t, func() bool {
		last = label(tenant, "team-a", v1alpha1.LabelPolicyShareable, "true")
		if last == nil {
			require.NoError(t, label(platform, "team-a", v1alpha1.LabelPolicyShareable, ""))
			return false
		}
		return strings.Contains(last.Error(), "only platform-admins may set kardinal.io/policy-shareable")
	}, 30*time.Second, 200*time.Millisecond, "a tenant may not add the label")
	t.Logf("a tenant adding the label: %v", last)
	require.NoError(t, label(tenant, "team-a", "team", "a"), "other labels pass")
	require.NoError(t, label(platform, "team-a", v1alpha1.LabelPolicyShareable, "true"), "platform-admins may add it")
	assert.Error(t, label(tenant, "team-a", v1alpha1.LabelPolicyShareable, "false"), "a tenant may not change it")
	assert.Error(t, label(tenant, "team-a", v1alpha1.LabelPolicyShareable, ""), "a tenant may not remove it")
	require.NoError(t, label(tenant, "team-a", "team", "a2"), "other labels still pass")
	err = tenant.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-c",
		Labels: map[string]string{v1alpha1.LabelPolicyShareable: "true"}}})
	assert.Error(t, err, "a tenant may not create a Namespace with it")
	require.NoError(t, label(platform, "team-a", v1alpha1.LabelPolicyShareable, ""), "platform-admins may remove it")
}
