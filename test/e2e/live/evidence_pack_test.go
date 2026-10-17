//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// auditorRole and auditorClusterRole are the chart's auditor ClusterRoles of
// the e2e release: the namespaced kinds of the pack, and the cluster-scoped
// ones with the admission policies.
const (
	auditorRole        = framework.ControllerDeployment + "-auditor"
	auditorClusterRole = framework.ControllerDeployment + "-auditor-cluster"
)

// auditorKubeconfig writes a kubeconfig that reaches the cluster as user
// (impersonation) and binds user to the auditor roles and nothing else, as
// docs/compliance.md says: auditorRole in ns with a RoleBinding, or
// cluster-wide with a ClusterRoleBinding when ns is "", and
// auditorClusterRole with a ClusterRoleBinding.
func auditorKubeconfig(t *testing.T, e *framework.Env, ns, user string) string {
	t.Helper()
	ctx := context.Background()
	slug := strings.NewReplacer("@", "-", ":", "-", ".", "-").Replace(user)
	path := filepath.Join(t.TempDir(), "kubeconfig-"+slug)
	e.WriteKubeconfig(t, path, map[string]string{e.Context: ns}, e.Context)
	cfg, err := clientcmd.LoadFromFile(path)
	require.NoError(t, err)
	for _, ai := range cfg.AuthInfos {
		ai.Impersonate = user
	}
	require.NoError(t, clientcmd.WriteToFile(*cfg, path))
	subjects := []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: user}}
	clusterBinding := func(name, role string) {
		_, err := e.Kube.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role}, Subjects: subjects}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = e.Kube.RbacV1().ClusterRoleBindings().Delete(context.Background(), name, metav1.DeleteOptions{})
		})
		waitBoundNamed(t, e, "", name)
	}
	name := "auditor-" + slug
	if ns != "" {
		_, err = e.Kube.RbacV1().RoleBindings(ns).Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: auditorRole}, Subjects: subjects},
			metav1.CreateOptions{})
		require.NoError(t, err)
		waitBoundNamed(t, e, ns, name)
	} else {
		clusterBinding(name, auditorRole)
	}
	clusterBinding(name+"-cluster", auditorClusterRole)
	return path
}

// packOf extracts pack into a directory and returns it with the manifest.
func packOf(t *testing.T, pack string) (string, map[string]interface{}) {
	t.Helper()
	dir := t.TempDir()
	out, err := exec.Command("tar", "-xzf", pack, "-C", dir).CombinedOutput()
	require.NoError(t, err, string(out))
	dir = filepath.Join(dir, "evidence-pack")
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	require.NoError(t, err)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &m))
	return dir, m
}

// ndjson reads one file of an extracted pack.
func ndjson(t *testing.T, dir, name string) []map[string]interface{} {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err, name)
	var out []map[string]interface{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal(sc.Bytes(), &m))
		out = append(out, m)
	}
	return out
}

// TestAudit_EvidencePack follows docs/compliance.md: a version promoted
// through an approval and a gate override, then an evidence pack exported
// by a user bound only to the chart's auditor role. sha256sum -c passes on
// the extracted pack and fails once a file changes; the pack holds the
// version, its Approval, its overridden gate instance and the AuditEvents
// of the approval, the override and the promotion, the chart's admission
// policies, and names the auditor; nothing is forbidden or missing. A
// namespace auditor (the auditor role bound in the namespace, the
// auditor-cluster role cluster-wide) gets a full pack of its namespace and no
// record of another, and with -A is refused every namespaced kind.
//
// Covers EVIDENCE-PACK-LIVE-01.
func TestAudit_EvidencePack(t *testing.T) {
	t.Parallel()
	e := framework.New(t)
	c := e.CLI(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	g := framework.Gate(a.ns, "one-approver", "prod", "true", recheck)
	g.Spec.Approval = &v1alpha1.GateApprovalPolicy{Required: 1, AllowedGroups: []string{"release-managers"}}
	e.CreateGate(t, g)
	require.NoError(t, e.Client.Create(ctx, operatorHold(a.ns, "prod")))
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+fixtures.V2)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)

	alice := userKubeconfig(t, e, a.ns, "alice@example.com", "release-managers")
	r := c.Exec(framework.CLIOptions{Kubeconfig: alice}, c.Args(a.ns, "approve", bundle, "--env", "prod")...)
	require.Zero(t, r.Code, r.Output())
	a.overrideHold(t, bundle, "prod")
	e.WaitStepState(t, a.ns, pipelineName, bundle, "prod", "Verified", promoteTimeout)
	framework.Eventually(t, time.Minute, "the prod promotion's AuditEvent", func(ctx context.Context) (bool, string) {
		var evs v1alpha1.AuditEventList
		if err := e.Client.List(ctx, &evs, client.InNamespace(a.ns)); err != nil {
			return false, err.Error()
		}
		for _, ev := range evs.Items {
			if ev.Spec.BundleName == bundle && ev.Spec.Environment == "prod" && ev.Spec.Action == "PromotionSucceeded" {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d events", len(evs.Items))
	})

	// The auditor, bound cluster-wide to the auditor role only.
	const auditor = "auditor@example.com"
	pack := filepath.Join(t.TempDir(), "pack.tar.gz")
	r = c.Exec(framework.CLIOptions{Kubeconfig: auditorKubeconfig(t, e, "", auditor)},
		c.Args(a.ns, "audit", "evidence-pack", "--since", "1h", "-o", pack)...)
	require.Zero(t, r.Code, r.Output())
	assert.Regexp(t, `sha256:[0-9a-f]{64}  `+pack+` \(17 files, \d+ records\)`, r.Stderr)
	dir, m := packOf(t, pack)
	sums := exec.Command("sha256sum", "-c", "SHA256SUMS")
	sums.Dir = dir
	out, err := sums.CombinedOutput()
	require.NoError(t, err, "sha256sum -c passes on the pack as exported:\n%s", out)
	assert.Equal(t, auditor, m["exportedBy"])
	coverage := m["coverage"].(map[string]interface{})
	assert.Empty(t, coverage["forbidden"], "the auditor role reads everything the pack holds")
	assert.Empty(t, coverage["missing"])

	assert.Equal(t, []string{a.ns + "/" + bundle}, names(ndjson(t, dir, "bundles.ndjson")))
	approvals := ndjson(t, dir, "approvals.ndjson")
	require.Len(t, approvals, 1)
	assert.Equal(t, "alice@example.com", approvals[0]["spec"].(map[string]interface{})["user"])
	var overridden bool
	for _, gate := range ndjson(t, dir, "policygates.ndjson") {
		if ov, _ := gate["spec"].(map[string]interface{})["overrides"].([]interface{}); len(ov) > 0 {
			overridden = true
		}
	}
	assert.True(t, overridden, "the overridden gate instance and its override are in the pack")
	actions := map[string]bool{}
	for _, ev := range ndjson(t, dir, "auditevents.ndjson") {
		spec := ev["spec"].(map[string]interface{})
		if spec["bundleName"] == bundle {
			actions[fmt.Sprint(spec["action"])] = true
		}
	}
	for _, want := range []string{"ApprovalRecorded", "GateOverridden", "PromotionSucceeded"} {
		assert.True(t, actions[want], "the pack holds the version's %s AuditEvent (has %v)", want, actions)
	}
	admission, err := os.ReadFile(filepath.Join(dir, "admission.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(admission), "name: "+framework.ControllerDeployment+"-approvals\n", "the identity policies in force")

	// A changed file fails the check.
	f, err := os.OpenFile(filepath.Join(dir, "bundles.ndjson"), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("{}\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	sums = exec.Command("sha256sum", "-c", "SHA256SUMS")
	sums.Dir = dir
	out, err = sums.CombinedOutput()
	require.Error(t, err, "sha256sum -c fails once a file changed:\n%s", out)
	assert.Contains(t, string(out), "bundles.ndjson: FAILED")

	// A namespace auditor (auditorRole bound in the namespace,
	// auditorClusterRole cluster-wide): a full pack of that namespace, and
	// not one record of another, though other tests' namespaces hold some.
	pack = filepath.Join(t.TempDir(), "ns.tar.gz")
	nsAuditor := auditorKubeconfig(t, e, a.ns, "ns-auditor@example.com")
	r = c.Exec(framework.CLIOptions{Kubeconfig: nsAuditor}, c.Args(a.ns, "audit", "evidence-pack", "--since", "1h", "-o", pack)...)
	require.Zero(t, r.Code, r.Output())
	dir, m = packOf(t, pack)
	assert.Empty(t, m["coverage"].(map[string]interface{})["forbidden"], "the namespace auditor reads its whole pack")
	assert.Equal(t, []string{a.ns + "/" + bundle}, names(ndjson(t, dir, "bundles.ndjson")))
	for _, f := range []string{"bundles.ndjson", "approvals.ndjson", "policygates.ndjson", "verdicts.ndjson",
		"pipelines.ndjson", "policygate-templates.ndjson"} {
		for _, n := range names(ndjson(t, dir, f)) {
			assert.True(t, strings.HasPrefix(n, a.ns+"/"), "%s: %s is from another namespace", f, n)
		}
	}
	for _, ev := range ndjson(t, dir, "auditevents.ndjson") {
		assert.Equal(t, a.ns, ev["namespace"], "an AuditEvent from another namespace")
	}
	_, err = os.Stat(filepath.Join(dir, "changewindows.ndjson"))
	assert.NoError(t, err, "the cluster role reads the ChangeWindows")

	// Asked for every namespace, the same auditor is refused the namespaced
	// kinds: the namespace role grants them in its namespace only.
	pack = filepath.Join(t.TempDir(), "all.tar.gz")
	r = c.Exec(framework.CLIOptions{Kubeconfig: nsAuditor}, c.Args("", "audit", "evidence-pack", "-A", "--since", "1h", "-o", pack)...)
	require.Zero(t, r.Code, r.Output())
	_, m = packOf(t, pack)
	assert.ElementsMatch(t, []interface{}{"AuditEvent", "Approval", "Bundle", "ChangeRequest", "ChangeRequestTemplate", "Pipeline", "PolicyGate",
		"PolicyGateTemplate", "PromotionReceipt", "Verdict"},
		m["coverage"].(map[string]interface{})["forbidden"])
}

// names are the namespace/name of each record.
func names(recs []map[string]interface{}) []string {
	var out []string
	for _, r := range recs {
		md, _ := r["metadata"].(map[string]interface{})
		ns, _ := md["namespace"].(string)
		out = append(out, fmt.Sprint(ns, "/", md["name"]))
	}
	return out
}
