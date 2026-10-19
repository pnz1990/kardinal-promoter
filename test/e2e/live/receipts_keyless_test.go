//go:build e2e

// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package live

import (
	"context"
	"crypto/x509"
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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/fixtures"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// The identity Fulcio certifies for the controller: its ServiceAccount, as
// the cluster's issuer (kind's default) names it.
const (
	keylessIssuer   = "https://kubernetes.default.svc.cluster.local"
	keylessIdentity = "https://kubernetes.io/namespaces/" + framework.ControllerNamespace +
		"/serviceaccounts/" + framework.ControllerServiceAccount
)

// keylessTrustedRoot is the private sigstore's trusted root
// (components/sigstore.sh, from its TUF repository).
func keylessTrustedRoot(t *testing.T) string {
	t.Helper()
	f := os.Getenv("KARDINAL_E2E_TRUSTED_ROOT")
	if f == "" {
		t.Skip("KARDINAL_E2E_TRUSTED_ROOT is not set: the receipts-keyless suite (hack/e2e/up.sh receipts-keyless) sets it")
	}
	return f
}

// TestReceiptsKeyless_SignedPromotion promotes a version by digest with
// keyless receipts against a private sigstore in the cluster (sigstore
// scaffolding: Fulcio trusting the cluster's service account issuer, Rekor
// v2, a timestamp authority). The receipt is signed with a short-lived
// Fulcio certificate for the controller's ServiceAccount, logged in Rekor v2
// and timestamped. kardinal verify-receipt passes with --identity, --issuer
// and the trusted root, from the cluster and from the receipt's file alone
// (the timestamp proves the signing time without asking Rekor), and fails
// for another identity or issuer.
//
// Covers RECEIPT-LIVE-05.
func TestReceiptsKeyless_SignedPromotion(t *testing.T) {
	root := keylessTrustedRoot(t)
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest)
	e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
	rc := promotionReceipt(t, e, a.ns, bundle, "test", v1alpha1.ReceiptStored)

	require.NotNil(t, rc.Status.SignedBy)
	assert.Equal(t, v1alpha1.ReceiptSigningKeyless, rc.Status.SignedBy.Mode)
	assert.Equal(t, keylessIdentity, rc.Status.SignedBy.Identity)
	assert.Equal(t, keylessIssuer, rc.Status.SignedBy.Issuer)
	var logged bool
	for _, s := range rc.Status.Stored {
		if s.Sink == v1alpha1.ReceiptSinkRekor {
			logged = s.LogIndex != nil
		}
	}
	assert.True(t, logged, "logged in Rekor: %+v", rc.Status.Stored)
	var b struct {
		VerificationMaterial struct {
			TlogEntries []struct {
				KindVersion struct{ Kind, Version string } `json:"kindVersion"`
			} `json:"tlogEntries"`
			TimestampVerificationData struct {
				RFC3161Timestamps []json.RawMessage `json:"rfc3161Timestamps"`
			} `json:"timestampVerificationData"`
			Certificate *json.RawMessage `json:"certificate"`
		} `json:"verificationMaterial"`
	}
	require.NoError(t, json.Unmarshal([]byte(rc.Status.Bundle), &b))
	require.Len(t, b.VerificationMaterial.TlogEntries, 1)
	assert.Equal(t, "0.0.2", b.VerificationMaterial.TlogEntries[0].KindVersion.Version, "a Rekor v2 entry")
	assert.Len(t, b.VerificationMaterial.TimestampVerificationData.RFC3161Timestamps, 1, "timestamped")
	assert.NotNil(t, b.VerificationMaterial.Certificate, "a Fulcio certificate")

	ok := []string{"--identity", keylessIdentity, "--issuer", keylessIssuer, "--trusted-root", root}
	out := e.MustKardinal(t, a.ns, append([]string{"verify-receipt", pipelineName, "test"}, ok...)...)
	assert.Contains(t, out, "receipt "+rc.Name+": every check below that was made passed")

	// From the file alone: pinned to its namespace, Pipeline, environment,
	// subject and cluster (#1961).
	pins := filePins(t, e, a.ns, rc)
	file := filepath.Join(t.TempDir(), "receipt.sigstore.json")
	require.NoError(t, os.WriteFile(file, []byte(rc.Status.Bundle), 0o600))
	out = e.MustKardinal(t, a.ns, append(append([]string{"verify-receipt", "--file", file}, pins...), ok...)...)
	assert.Contains(t, out, "receipt "+rc.Name+": every check below that was made passed")
	signedEarly, signedEarlyPins = rc.Status.Bundle, pins

	for name, args := range map[string][]string{
		"another identity": {"--identity", "https://kubernetes.io/namespaces/default/serviceaccounts/default",
			"--issuer", keylessIssuer, "--trusted-root", root},
		"another issuer": {"--identity", keylessIdentity, "--issuer", "https://accounts.google.com", "--trusted-root", root},
	} {
		_, err := e.Kardinal(t, a.ns, append(append([]string{"verify-receipt", "--file", file}, pins...), args...)...)
		assert.Error(t, err, name)
	}
}

// rekorIndex is rc's Rekor log index.
func rekorIndex(t *testing.T, rc *v1alpha1.PromotionReceipt) int64 {
	t.Helper()
	for _, s := range rc.Status.Stored {
		if s.Sink == v1alpha1.ReceiptSinkRekor && s.LogIndex != nil {
			return *s.LogIndex
		}
	}
	t.Fatalf("receipt %s has no Rekor entry: %+v", rc.Name, rc.Status.Stored)
	return 0
}

// faultProxy is the suite's hack/e2e/faultproxy admin API, through the API
// server's Service proxy.
func faultProxy(e *framework.Env, path string) string {
	return "/api/v1/namespaces/" + os.Getenv("KARDINAL_E2E_SIGSTORE_FAULTS") + "/services/http:faultproxy:admin/proxy" + path
}

// setFault sets how route (fulcio, rekor or tsa) answers: pass, down, slow
// (delay) or status (code).
func setFault(t *testing.T, e *framework.Env, route, mode string, params ...string) {
	t.Helper()
	req := e.Kube.CoreV1().RESTClient().Post().AbsPath(faultProxy(e, "/set")).Param("route", route).Param("mode", mode)
	for i := 0; i+1 < len(params); i += 2 {
		req = req.Param(params[i], params[i+1])
	}
	require.NoError(t, req.Do(context.Background()).Error(), "faultproxy %s %s", route, mode)
}

// faultCounts are route's answers since its last reset, by status.
func faultCounts(t *testing.T, e *framework.Env, route string) map[string]int {
	t.Helper()
	raw, err := e.Kube.CoreV1().RESTClient().Get().AbsPath(faultProxy(e, "/routes")).DoRaw(context.Background())
	require.NoError(t, err)
	var routes []struct {
		Name   string         `json:"name"`
		Counts map[string]int `json:"counts"`
	}
	require.NoError(t, json.Unmarshal(raw, &routes))
	for _, r := range routes {
		if r.Name == route {
			return r.Counts
		}
	}
	t.Fatalf("faultproxy has no route %s", route)
	return nil
}

func resetFault(t *testing.T, e *framework.Env, route string) {
	t.Helper()
	require.NoError(t, e.Kube.CoreV1().RESTClient().Post().AbsPath(faultProxy(e, "/reset")).Param("route", route).
		Do(context.Background()).Error())
}

// successes counts 2xx answers.
func successes(counts map[string]int) int {
	n := 0
	for k, v := range counts {
		if strings.HasPrefix(k, "2") {
			n += v
		}
	}
	return n
}

// TestReceiptsKeyless_Outages: Fulcio refuses connections, Rekor answers
// 429, the timestamp authority answers 503, Fulcio answers slower (35s)
// than the signer's timeout (30s), each in turn (hack/e2e/faultproxy in
// front of each). Each time a version still reaches Verified on time; its
// receipt waits, Signed=False with SignFailed, and
// kardinal_receipts_failed_total{sink="signing"} rises. Once the service
// answers again the receipt is signed exactly once: the failing service
// answered one success for it, and its Rekor entry directly follows the
// previous receipt's, so no attempt signed twice.
//
// Covers RECEIPT-LIVE-06.
func TestReceiptsKeyless_Outages(t *testing.T) {
	root := keylessTrustedRoot(t)
	e := framework.New(t)
	a := newArgoApp(t, e, "test")
	a.apply(t, a.pipeline(nil))
	first := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest)
	e.WaitStepState(t, a.ns, pipelineName, first, "test", "Verified", promoteTimeout)
	last := rekorIndex(t, promotionReceipt(t, e, a.ns, first, "test", v1alpha1.ReceiptStored))
	images := []string{fixtures.V3, fixtures.V1, fixtures.V2, fixtures.V3}
	for i, sc := range []struct {
		name, route, mode string
		params            []string
	}{
		{"fulcio down", "fulcio", "down", nil},
		{"rekor 429", "rekor", "status", []string{"code", "429"}},
		{"tsa 503", "tsa", "status", []string{"code", "503"}},
		{"fulcio slower than the timeout", "fulcio", "slow", []string{"delay", "35s"}},
	} {
		t.Run(sc.name, func(t *testing.T) {
			t.Cleanup(func() { setFault(t, e, sc.route, "pass") })
			failed := signingFailures(t, e)
			resetFault(t, e, sc.route)
			setFault(t, e, sc.route, sc.mode, sc.params...)
			bundle := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+":"+images[i])
			e.WaitStepState(t, a.ns, pipelineName, bundle, "test", "Verified", promoteTimeout)
			var rc v1alpha1.PromotionReceipt
			framework.Eventually(t, 3*time.Minute, "the receipt waits for "+sc.route, func(ctx context.Context) (bool, string) {
				var list v1alpha1.PromotionReceiptList
				if err := e.Client.List(ctx, &list, client.InNamespace(a.ns),
					client.MatchingLabels{"kardinal.io/bundle": bundle, "kardinal.io/environment": "test"}); err != nil {
					return false, err.Error()
				}
				if len(list.Items) != 1 {
					return false, fmt.Sprintf("%d receipts", len(list.Items))
				}
				rc = list.Items[0]
				c := meta.FindStatusCondition(rc.Status.Conditions, "Signed")
				return c != nil && c.Status == metav1.ConditionFalse && c.Reason == "SignFailed" && rc.Status.Attempts >= 1,
					fmt.Sprintf("phase=%q attempts=%d conditions=%v", rc.Status.Phase, rc.Status.Attempts, rc.Status.Conditions)
			})
			assert.Empty(t, rc.Status.Bundle, "not signed during the outage")
			assert.Greater(t, signingFailures(t, e), failed, "kardinal_receipts_failed_total{sink=\"signing\"} rises")
			assert.Zero(t, successes(faultCounts(t, e, sc.route)), "no success from %s during the outage", sc.route)

			setFault(t, e, sc.route, "pass")
			got := promotionReceipt(t, e, a.ns, bundle, "test", v1alpha1.ReceiptStored)
			assert.Equal(t, 1, successes(faultCounts(t, e, sc.route)), "%s answered one success: %v", sc.route,
				faultCounts(t, e, sc.route))
			idx := rekorIndex(t, got)
			assert.Equal(t, last+1, idx, "signed once: the next entry in the log")
			last = idx
			file := filepath.Join(t.TempDir(), "receipt.sigstore.json")
			require.NoError(t, os.WriteFile(file, []byte(got.Status.Bundle), 0o600))
			e.MustKardinal(t, a.ns, append(append([]string{"verify-receipt", "--file", file}, filePins(t, e, a.ns, got)...),
				"--identity", keylessIdentity, "--issuer", keylessIssuer, "--trusted-root", root)...)
		})
	}
}

// signingFailures is the controller's kardinal_receipts_failed_total{sink="signing"}.
func signingFailures(t *testing.T, e *framework.Env) float64 {
	t.Helper()
	m, err := e.ControllerMetrics(context.Background())
	require.NoError(t, err)
	return m.Sum("kardinal_receipts_failed_total", map[string]string{"sink": "signing"})
}

// signedEarly is the bundle TestReceiptsKeyless_SignedPromotion signed, for
// TestReceiptsKeyless_VerifiesAfterCertificateExpiry: the suite's other
// tests run while its certificate's ten minutes pass.
var signedEarly string

// signedEarlyPins are signedEarly's verify-receipt --file pins.
var signedEarlyPins []string

// certificateNotAfter is the end of the validity of bundle's certificate.
func certificateNotAfter(t *testing.T, bundle string) time.Time {
	t.Helper()
	var b struct {
		VerificationMaterial struct {
			Certificate struct {
				RawBytes []byte `json:"rawBytes"`
			} `json:"certificate"`
		} `json:"verificationMaterial"`
	}
	require.NoError(t, json.Unmarshal([]byte(bundle), &b))
	cert, err := x509.ParseCertificate(b.VerificationMaterial.Certificate.RawBytes)
	require.NoError(t, err)
	return cert.NotAfter
}

// offline runs the CLI in a network namespace of its own, with no network
// at all: unshare -rn where unprivileged user namespaces are allowed, sudo
// otherwise (GitHub's Ubuntu runners restrict them).
func offline(t *testing.T, cli string, args ...string) (string, error) {
	t.Helper()
	var prefix []string
	switch {
	case exec.Command("unshare", "-rn", "true").Run() == nil:
		prefix = []string{"unshare", "-rn", "--"}
	case exec.Command("sudo", "-n", "unshare", "-n", "true").Run() == nil:
		prefix = []string{"sudo", "-n", "unshare", "-n", "--"}
	default:
		t.Fatal("neither unshare -rn nor sudo -n unshare -n works on this host: no network namespace for the offline verifier")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, prefix[0], append(append(prefix[1:], cli), args...)...)
	cmd.Env = append(os.Environ(), "KUBECONFIG=/dev/null")
	out, err := cmd.CombinedOutput()
	t.Logf("$ %s %s (offline)\n%s", cli, strings.Join(args, " "), out)
	require.NoError(t, ctx.Err(), "the offline verifier answered, not hung")
	return string(out), err
}

// TestReceiptsKeyless_VerifiesAfterCertificateExpiry: Fulcio's
// certificates live ten minutes. Once a receipt's certificate has expired
// it still verifies, on the timestamp authority's proof of when it was
// signed, and it verifies offline too: in a network namespace with no
// network, with the trusted root on disk and the bundle's own timestamp and
// Rekor inclusion proof. Without the trusted root the offline verifier, which
// would fetch the public-good root with TUF, fails with a message rather
// than hanging.
//
// Covers RECEIPT-LIVE-07.
func TestReceiptsKeyless_VerifiesAfterCertificateExpiry(t *testing.T) {
	root := keylessTrustedRoot(t)
	e := framework.New(t)
	bundle, pins := signedEarly, signedEarlyPins
	if bundle == "" {
		a := newArgoApp(t, e, "test")
		a.apply(t, a.pipeline(nil))
		b := e.CreateBundle(t, a.ns, pipelineName, "--image", fixtures.Image+"@"+fixtures.V2Digest)
		e.WaitStepState(t, a.ns, pipelineName, b, "test", "Verified", promoteTimeout)
		rc := promotionReceipt(t, e, a.ns, b, "test", v1alpha1.ReceiptStored)
		bundle, pins = rc.Status.Bundle, filePins(t, e, a.ns, rc)
	}
	notAfter := certificateNotAfter(t, bundle)
	require.Less(t, time.Until(notAfter), 15*time.Minute, "a short-lived certificate")
	if wait := time.Until(notAfter) + 5*time.Second; wait > 0 {
		t.Logf("waiting %s for the certificate to expire (%s)", wait.Round(time.Second), notAfter.Format(time.RFC3339))
		time.Sleep(wait)
	}
	file := filepath.Join(t.TempDir(), "receipt.sigstore.json")
	require.NoError(t, os.WriteFile(file, []byte(bundle), 0o600))
	// -n is in the pins: the offline verifier has no cluster to default it.
	args := append(append([]string{"verify-receipt", "--file", file}, pins...), "--identity", keylessIdentity, "--issuer", keylessIssuer)
	out := e.MustKardinal(t, "", append(args, "--trusted-root", root)...)
	assert.Contains(t, out, "every check below that was made passed", "verified after the certificate expired")

	out, err := offline(t, os.Getenv(framework.EnvCLI), append(args, "--trusted-root", root)...)
	require.NoError(t, err, out)
	assert.Contains(t, out, "every check below that was made passed", "verified with no network")
	out, err = offline(t, os.Getenv(framework.EnvCLI), args...)
	require.Error(t, err, "the public-good root cannot be fetched offline")
	assert.NotEmpty(t, strings.TrimSpace(out), "it says why")
}

// filePins are the verify-receipt --file pins of rc: its namespace, Pipeline,
// environment, subject (from its signed statement: an image digest, or the
// Bundle spec's for a version by tag) and cluster.
func filePins(t *testing.T, e *framework.Env, ns string, rc *v1alpha1.PromotionReceipt) []string {
	t.Helper()
	st, err := receipt.Parse([]byte(rc.Spec.Statement))
	require.NoError(t, err)
	require.NotEmpty(t, st.Subject)
	return []string{"-n", ns, "--pipeline", st.Predicate.Kardinal.Pipeline, "--env", st.Predicate.Kardinal.Change.Environment,
		"--subject", "sha256:" + st.Subject[0].Digest["sha256"], "--cluster", clusterIdentity(t, e)}
}
