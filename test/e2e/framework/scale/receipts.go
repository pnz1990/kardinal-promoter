// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scale

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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
