// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// ReceiptStatement is a receipt statement for receipt name of pipeline in
// namespace ns, padded with gate messages to about size bytes (under
// MaxReceiptStatementBytes), and its digest: the fixture of the scale tests
// that write receipts (TestScale_Receipts, TestScale_EvidencePackVersions).
// It does not touch a testing.T, so writers' goroutines may call it.
func ReceiptStatement(ns, name, pipeline string, size int) (statement, digest string) {
	st := receipt.Statement{
		Type: receipt.StatementType, PredicateType: receipt.PredicateVSA,
		Subject: []receipt.Subject{{Name: ImageRepo, Digest: map[string]string{"sha256": fmt.Sprintf("%064x", len(name))}}},
		Predicate: receipt.Predicate{
			Verifier:     receipt.Verifier{ID: "https://kardinal.io/controller"},
			TimeVerified: time.Now().UTC().Format(time.RFC3339),
			ResourceURI:  receipt.ResourceURI("", ns, pipeline, "prod"), VerificationResult: "PASSED",
			VerifiedLevels: []string{receipt.LevelHealthy}, SlsaVersion: "1.0",
			Kardinal: receipt.Record{Receipt: name, Pipeline: pipeline,
				Version: receipt.Version{Bundle: name, BundleUID: name, Type: "image"}},
		},
	}
	msg := strings.Repeat("x", 1000)
	for {
		b, _ := json.Marshal(st) // plain structs: it cannot fail
		if len(b)+1100 > size || len(b)+1100 > v1alpha1.MaxReceiptStatementBytes {
			return string(b), receipt.Digest(b)
		}
		st.Predicate.Kardinal.Gates = append(st.Predicate.Kardinal.Gates,
			receipt.Gate{Name: fmt.Sprintf("gate-%03d", len(st.Predicate.Kardinal.Gates)), Result: "Passed", Message: msg})
	}
}

// ControllerClient is a client of the test cluster as the controller's
// ServiceAccount (impersonated): only it may write receipts, gate instances
// and a Bundle's status, so a scale test writes its fixtures of those as
// the controller does.
func ControllerClient(t *testing.T, e *framework.Env) client.Client {
	t.Helper()
	cfg := rest.CopyConfig(e.Config)
	cfg.QPS, cfg.Burst = 200, 400
	cfg.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + framework.ControllerNamespace + ":kardinal-promoter",
		Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + framework.ControllerNamespace, "system:authenticated"}}
	c, err := client.New(cfg, client.Options{Scheme: framework.Scheme()})
	if err != nil {
		t.Fatalf("controller client: %v", err)
	}
	return c
}

// receiptKeySecret is the Secret EnableReceipts signs receipts with.
const receiptKeySecret = "scale-receipt-key"

// EnableReceipts turns key-signed receipts on in the suite's release (helm
// upgrade --reuse-values, with a new P-256 key in receiptKeySecret) and off
// again when the test ends: the controller then signs and stores every
// PromotionReceipt written as it writes them (ControllerClient).
func EnableReceipts(t *testing.T, e *framework.Env) {
	t.Helper()
	ctx := context.Background()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("receipt key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("receipt key: %v", err)
	}
	if _, err := e.Kube.CoreV1().Secrets(framework.ControllerNamespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: receiptKeySecret},
		Data:       map[string][]byte{"cosign.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("receipt key Secret: %v", err)
	}
	rel := e.ExistingRelease(t, "kardinal-promoter", framework.ControllerNamespace)
	upgrade := func(sets string) error {
		run := e.Shell(t, `helm upgrade kardinal-promoter "$KARDINAL_E2E_CHART" --namespace kardinal-system --reuse-values `+sets+` --wait --timeout 5m`)
		if run.Code != 0 {
			return fmt.Errorf("helm upgrade %s: %s", sets, run.Output)
		}
		rel.WaitRolledOut(t, 5*time.Minute)
		return nil
	}
	if err := upgrade(`--set receipts.enabled=true --set receipts.signing.mode=key --set receipts.signing.key.secretRef.name=` + receiptKeySecret); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := upgrade(`--set receipts.enabled=false --set receipts.retention.maxAge=`); err != nil {
			t.Errorf("turn receipts off: %v", err)
		}
		_ = e.Kube.CoreV1().Secrets(framework.ControllerNamespace).Delete(context.Background(), receiptKeySecret, metav1.DeleteOptions{})
	})
}

// CountReceipts counts the receipts of namespace ns ("" every namespace) in
// phase ("" any).
func CountReceipts(ctx context.Context, e *framework.Env, ns, phase string) (int, error) {
	var list v1alpha1.PromotionReceiptList
	var opts []client.ListOption
	if ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := e.Client.List(ctx, &list, opts...); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range list.Items {
		if phase == "" || r.Status.Phase == phase {
			n++
		}
	}
	return n, nil
}
