//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	iv "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/imageverification/signtest"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// Live tests of evidence with no way out of the cluster: the evidence-airgap
// suite (hack/e2e/up.sh evidence-airgap, components/airgap.sh).

const (
	airgapIssuer   = "https://token.actions.githubusercontent.com"
	airgapIdentity = "https://github.com/myorg/app/.github/workflows/release.yml@refs/heads/main"
)

// airgapProbe runs wget against url from a Pod in namespace ns (the probe
// image has busybox wget) and returns what it printed and whether it got an
// answer, any answer, within 5 seconds: a refused or TLS-failed connection
// is an answer, a timeout is not.
func airgapProbe(t *testing.T, e *framework.Env, ns, name, url string) (string, bool) {
	t.Helper()
	ctx := context.Background()
	image := os.Getenv("KARDINAL_E2E_AIRGAP_PROBE_IMAGE")
	require.NotEmpty(t, image, "KARDINAL_E2E_AIRGAP_PROBE_IMAGE: components/airgap.sh did not run")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{
			Name: "probe", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
			// kindnet enforces a NetworkPolicy about a second after the Pod
			// gets its IP: wait before probing.
			Command: []string{"sh", "-c", `sleep 3; if wget -T 5 -q -O /dev/null "$0" 2>/tmp/err; then echo ANSWERED ok; ` +
				`elif grep -qi "timed out" /tmp/err; then echo TIMEOUT; cat /tmp/err; else echo ANSWERED; cat /tmp/err; fi`, url},
		}}},
	}
	require.NoError(t, e.Client.Create(ctx, pod))
	t.Cleanup(func() { _ = e.Client.Delete(context.Background(), pod) })
	var logs string
	framework.Eventually(t, 2*time.Minute, "probe "+ns+"/"+name+" finished", func(ctx context.Context) (bool, string) {
		var got corev1.Pod
		if err := e.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false, err.Error()
		}
		if got.Status.Phase != corev1.PodSucceeded && got.Status.Phase != corev1.PodFailed {
			return false, string(got.Status.Phase)
		}
		b, err := e.Kube.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{}).DoRaw(ctx)
		if err != nil {
			return false, err.Error()
		}
		logs = string(b)
		return true, ""
	})
	t.Logf("probe %s/%s %s:\n%s", ns, name, url, logs)
	return logs, strings.HasPrefix(logs, "ANSWERED")
}

// offline runs the kardinal CLI with args in a network namespace of its own
// (only a loopback): no address outside the host process is reachable. It
// uses an unprivileged user namespace, or sudo where those are refused (a
// GitHub runner's AppArmor).
func offline(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cli := os.Getenv(framework.EnvCLI)
	require.NotEmpty(t, cli, framework.EnvCLI)
	var cmd *exec.Cmd
	if exec.Command("unshare", "-rn", "true").Run() == nil {
		cmd = exec.Command("unshare", append([]string{"-rn", cli}, args...)...)
	} else {
		require.NoError(t, exec.Command("sudo", "-n", "unshare", "-n", "true").Run(), "neither unshare -rn nor sudo unshare -n works here")
		cmd = exec.Command("sudo", append([]string{"-n", "--preserve-env=HOME", "unshare", "-n", cli}, args...)...)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	t.Logf("$ (offline) kardinal %s\n%s", strings.Join(args, " "), out.String())
	return out.String(), err
}

// TestAirgap_EgressBlocked checks the suite's premise: a Pod in the
// controller namespace cannot reach an address outside the cluster (the
// node's kubelet, which a Pod elsewhere reaches) but reaches the in-cluster
// registry. If this fails, the suite's other tests prove nothing.
//
// Covers EVIDENCE-AIRGAP-01.
func TestAirgap_EgressBlocked(t *testing.T) {
	e := framework.New(t)
	node := os.Getenv("KARDINAL_E2E_AIRGAP_NODE_IP")
	require.NotEmpty(t, node, "KARDINAL_E2E_AIRGAP_NODE_IP: components/airgap.sh did not run")
	kubelet := "https://" + node + ":10250/healthz"
	reg := "http://" + strings.SplitN(framework.NewRegistry(t).Repository("x"), "/", 2)[0] + "/v2/"
	ns := e.Namespace(t)

	_, answered := airgapProbe(t, e, ns, "outside-kubelet", kubelet)
	require.True(t, answered, "the control: a Pod outside the controller namespace reaches the node's kubelet")
	out, answered := airgapProbe(t, e, framework.ControllerNamespace, "airgap-kubelet", kubelet)
	assert.False(t, answered, "the controller namespace cannot reach the node's kubelet:\n%s", out)
	out, answered = airgapProbe(t, e, framework.ControllerNamespace, "airgap-registry", reg)
	assert.True(t, answered, "the controller namespace reaches the in-cluster registry:\n%s", out)
}

// TestAirgap_Evidence promotes a version with no way out of the cluster:
//   - its signature (a key) and its SLSA provenance (keyless, from a
//     private Sigstore: the test's own Fulcio, Rekor and TSA, whose
//     trusted_root.json is a Secret the authority's trustedRootRef names)
//     are read from the in-cluster registry and verified, with no TUF fetch;
//   - each environment gets a key-signed PromotionReceipt, stored in the
//     cluster and pushed to the in-cluster registry;
//   - kardinal verify-receipt passes on the receipt's file in a network
//     namespace with only a loopback (where the same CLI cannot reach the
//     cluster), and kardinal audit evidence-pack's receipts.ndjson holds the
//     receipt, which verifies there too, with the pack's checksums;
//   - a keyless authority with no trustedRootRef (the public-good root,
//     fetched through TUF) cannot be loaded: its version never starts and
//     fails at its timeout.
//
// Covers EVIDENCE-AIRGAP-02, EVIDENCE-AIRGAP-03, EVIDENCE-AIRGAP-04.
func TestAirgap_Evidence(t *testing.T) {
	e := framework.New(t)
	ctx := context.Background()
	a := newArgoApp(t, e, "test", "prod")
	sigs := newSignatures(t, a.ns)
	release, err := signtest.NewKey()
	require.NoError(t, err)
	pem, err := release.PublicPEM()
	require.NoError(t, err)
	vs, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	root, err := signtest.TrustedRootJSON(vs)
	require.NoError(t, err)
	require.NoError(t, e.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sigstore", Namespace: a.ns, Labels: map[string]string{"kardinal.io/referenceable": "true"}},
		Data:       map[string][]byte{"trusted_root.json": root},
	}))

	policy := keyPolicy(t, e, a.ns, sigs, pem, "3m")
	policy.Authorities = append(policy.Authorities, v1alpha1.SignatureAuthority{Name: "ci",
		Keyless: &v1alpha1.KeylessAuthority{Issuer: airgapIssuer, Subject: airgapIdentity,
			TrustedRootRef: &v1alpha1.SecretKeyName{Name: "sigstore", Key: "trusted_root.json"}}})
	policy.Attestations = []v1alpha1.AttestationPolicy{{Name: "provenance", PredicateType: iv.PredicateSLSAProvenance,
		Authorities: []string{"ci"}, Required: true}}
	p := a.pipeline(nil)
	p.Spec.ImageVerification = policy
	a.apply(t, p)
	waitPipelineValid(t, e, a.ns, pipelineName)
	sig, err := release.Bundle(fixtures.Image, fixtures.V2Digest)
	require.NoError(t, err)
	sigs.pushBundle(t, fixtures.V2Digest, sig)
	prov, err := signtest.KeylessAttest(vs, airgapIdentity, airgapIssuer, fixtures.Image, fixtures.V2Digest, iv.PredicateSLSAProvenance,
		map[string]any{
			"buildDefinition": map[string]any{"buildType": "https://actions.github.io/buildtypes/workflow/v1"},
			"runDetails":      map[string]any{"builder": map[string]any{"id": "https://github.com/actions/runner"}},
		})
	require.NoError(t, err)
	sigs.pushBundle(t, fixtures.V2Digest, prov)

	byDigest := fixtures.Image + "@" + fixtures.V2Digest
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", byDigest)
	got := waitImageVerification(t, e, a.ns, bundle, "Verified")
	require.Len(t, got.Status.Attestations, 1)
	att := got.Status.Attestations[0]
	assert.True(t, att.Verified, att.Message)
	assert.Equal(t, "ci", att.Authority, "verified by the private Sigstore's authority")
	assert.Equal(t, airgapIdentity, att.Signer)
	for _, env := range []string{"test", "prod"} {
		e.WaitStepState(t, a.ns, pipelineName, bundle, env, "Verified", promoteTimeout)
	}

	// Receipts: signed and stored with no network beyond the cluster.
	pub := receiptPublicKey(t, e)
	cluster := clusterIdentity(t, e)
	rc := promotionReceipt(t, e, a.ns, bundle, "prod", v1alpha1.ReceiptStored)
	assert.Equal(t, v1alpha1.ReceiptSigningKey, rc.Status.SignedBy.Mode)
	sinks := map[string]bool{}
	for _, s := range rc.Status.Stored {
		sinks[s.Sink] = true
	}
	assert.True(t, sinks[v1alpha1.ReceiptSinkCRD] && sinks[v1alpha1.ReceiptSinkOCI], "stored in %+v", rc.Status.Stored)

	dir := t.TempDir()
	file := filepath.Join(dir, "receipt.sigstore.json")
	require.NoError(t, os.WriteFile(file, []byte(rc.Status.Bundle), 0o600))
	pinned := []string{"verify-receipt", "--file", file, "-n", a.ns, "--pipeline", pipelineName, "--env", "prod",
		"--subject", fixtures.V2Digest, "--cluster", cluster, "--key", pub}
	out, err := offline(t, pinned...)
	require.NoError(t, err, out)
	assert.Contains(t, out, "VERIFIED receipt")
	// The namespace really is offline: the CLI cannot reach the cluster.
	kubeconfig := filepath.Join(dir, "kubeconfig")
	e.WriteKubeconfig(t, kubeconfig, map[string]string{e.Context: a.ns}, e.Context)
	out, err = offline(t, "--kubeconfig", kubeconfig, "--context", e.Context, "-n", a.ns, "verify-receipt", pipelineName, "prod", "--key", pub)
	require.Error(t, err, "from the cluster, offline, verify-receipt fails:\n%s", out)
	// The same pins with another environment fail offline too.
	wrong := append([]string(nil), pinned...)
	wrong[8] = "test"
	out, err = offline(t, wrong...)
	require.Error(t, err, "a prod receipt presented as test's fails:\n%s", out)

	// The evidence pack, exported online, checked offline.
	pack := filepath.Join(dir, "pack.tar.gz")
	c := e.CLI(t)
	r := c.Exec(framework.CLIOptions{}, c.Args(a.ns, "audit", "evidence-pack", "--since", "1h", "-o", pack)...)
	require.Equal(t, 0, r.Code, r.Output())
	pdir, _ := packOf(t, pack)
	sum := exec.Command("unshare", "-rn", "sha256sum", "-c", "SHA256SUMS")
	if exec.Command("unshare", "-rn", "true").Run() != nil {
		sum = exec.Command("sha256sum", "-c", "SHA256SUMS")
	}
	sum.Dir = pdir
	sumOut, err := sum.CombinedOutput()
	require.NoError(t, err, string(sumOut))
	var packed string
	for _, m := range ndjson(t, pdir, "receipts.ndjson") {
		md, _ := m["metadata"].(map[string]interface{})
		if md["name"] == rc.Name {
			st, _ := m["status"].(map[string]interface{})
			packed, _ = st["bundle"].(string)
		}
	}
	require.NotEmpty(t, packed, "receipts.ndjson holds %s with its bundle", rc.Name)
	fromPack := filepath.Join(dir, "from-pack.sigstore.json")
	require.NoError(t, os.WriteFile(fromPack, []byte(packed), 0o600))
	pinned[2] = fromPack
	out, err = offline(t, pinned...)
	require.NoError(t, err, out)
	assert.Contains(t, out, "VERIFIED receipt")

	// The public-good root cannot be fetched: fail closed.
	b := newArgoApp(t, e, "test")
	pg := keyPolicy(t, e, b.ns, newSignatures(t, b.ns), pem, "1m")
	pg.Authorities = []v1alpha1.SignatureAuthority{{Name: "public", Keyless: &v1alpha1.KeylessAuthority{Issuer: airgapIssuer, Subject: airgapIdentity}}}
	bp := b.pipeline(nil)
	bp.Spec.ImageVerification = pg
	b.apply(t, bp)
	waitPipelineValid(t, e, b.ns, pipelineName)
	blocked := e.CreateBundle(t, b.ns, pipelineName, "--image", byDigest)
	failed := waitImageVerification(t, e, b.ns, blocked, "Failed")
	t.Logf("public-good: reason=%s message=%s", failed.Status.Reason, failed.Status.Message)
	assert.Contains(t, failed.Status.Message, "authority public", "the message names the authority it could not load")
	e.NoStep(t, b.ns, pipelineName, blocked, "test", 5*time.Second)
	e.WaitBundlePhase(t, b.ns, blocked, "Failed", time.Minute)
	assert.Contains(t, fmt.Sprint(e.DeploymentImage(t, b.ns, fixtures.Workload("test"))), fixtures.V1, "test still runs V1")
}
