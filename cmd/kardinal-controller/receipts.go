// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
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

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/receipt"
	receiptrecon "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionreceipt"
	psreconciler "github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/promotionstep"
)

// publicRekor is the public-good Rekor: a receipt logged there publishes
// its statement's names, emails and pipeline names.
const publicRekor = "rekor.sigstore.dev"

// receiptFlags configure promotion receipts (docs/receipts.md). Chart
// values: receipts.*.
type receiptFlags struct {
	enabled     bool
	mode        string
	keySecret   string
	keyKey      string
	kmsKey      string
	fulcioURL   string
	tokenFile   string
	rekorURL    string
	rekorPublic bool
	tsaURL      string
	ociSecret   string
	ociRepo     string
	oci         bool
	ociInsecure bool
	clusterID   string
}

func bindReceiptFlags(fs *flag.FlagSet) *receiptFlags {
	f := &receiptFlags{}
	fs.BoolVar(&f.enabled, "receipts", false,
		"Create a signed PromotionReceipt for every promotion that reaches Verified (docs/receipts.md). Chart value: receipts.enabled.")
	fs.StringVar(&f.mode, "receipt-signing-mode", v1alpha1.ReceiptSigningKeyless,
		"How receipts are signed: keyless (Fulcio, with the controller's projected ServiceAccount token), key (an ECDSA P-256 key "+
			"in --receipt-key-secret) or kms (--receipt-kms-key).")
	fs.StringVar(&f.keySecret, "receipt-key-secret", "", "Secret in the controller namespace holding the receipt signing key (mode key).")
	fs.StringVar(&f.keyKey, "receipt-key-secret-key", "cosign.key", "Key of --receipt-key-secret holding the PEM private key.")
	fs.StringVar(&f.kmsKey, "receipt-kms-key", "", "KMS key reference (mode kms), for a provider the controller was built with.")
	fs.StringVar(&f.fulcioURL, "receipt-fulcio-url", "https://fulcio.sigstore.dev", "Fulcio of keyless mode.")
	fs.StringVar(&f.tokenFile, "receipt-token-file", "/var/run/secrets/sigstore/token",
		"Projected ServiceAccount token (audience sigstore) keyless mode presents to Fulcio.")
	fs.StringVar(&f.rekorURL, "receipt-rekor-url", "", "Rekor that logs every receipt signature; empty logs nothing.")
	fs.BoolVar(&f.rekorPublic, "receipt-rekor-public", false,
		"Allow --receipt-rekor-url to be the public-good Rekor, which publishes each receipt's names, emails and pipeline names.")
	fs.StringVar(&f.tsaURL, "receipt-tsa-url", "", "RFC 3161 timestamp authority that timestamps every receipt signature.")
	fs.BoolVar(&f.oci, "receipt-oci", false, "Push every receipt as an OCI referrer of each of its images.")
	fs.StringVar(&f.ociSecret, "receipt-oci-secret", "",
		"kubernetes.io/dockerconfigjson Secret in the controller namespace with push credentials for --receipt-oci.")
	fs.StringVar(&f.ociRepo, "receipt-oci-repository", "",
		"Push receipt referrers to this repository instead of each image's own (as cosign's COSIGN_REPOSITORY).")
	fs.BoolVar(&f.ociInsecure, "receipt-oci-insecure", false, "Push receipt referrers over plain HTTP (an in-cluster registry).")
	fs.StringVar(&f.clusterID, "receipt-cluster-id", "",
		"Cluster identity in receipts' resourceUri; empty reads the kube-system Namespace UID.")
	return f
}

// signer is the receipt Signer the flags configure, or an error naming
// the flag to fix.
func (f *receiptFlags) signer(c sigs_client.Reader, ns string, transport http.RoundTripper) (*receipt.Signer, error) {
	if f.rekorURL != "" {
		u, err := url.Parse(f.rekorURL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("--receipt-rekor-url %q is not a URL", f.rekorURL)
		}
		if strings.EqualFold(u.Hostname(), publicRekor) && !f.rekorPublic {
			return nil, errors.New("--receipt-rekor-url is the public-good Rekor, which publishes every receipt's names, " +
				"emails and pipeline names: set --receipt-rekor-public (receipts.sinks.rekor.public) to allow it")
		}
	}
	s := &receipt.Signer{Mode: f.mode, KMSRef: f.kmsKey, FulcioURL: f.fulcioURL, TokenFile: f.tokenFile,
		RekorURL: f.rekorURL, TSAURL: f.tsaURL, Transport: transport}
	if f.mode == v1alpha1.ReceiptSigningKey && f.keySecret != "" {
		s.Key = receiptrecon.SecretKey(c, ns, f.keySecret, f.keyKey)
	}
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("--receipt-signing-mode %s: %w", f.mode, err)
	}
	return s, nil
}

// clusterIdentity is --receipt-cluster-id, else the kube-system Namespace
// UID, stable for the cluster's life.
func (f *receiptFlags) clusterIdentity(ctx context.Context, c sigs_client.Reader) (string, error) {
	if f.clusterID != "" {
		return f.clusterID, nil
	}
	var ns corev1.Namespace
	if err := c.Get(ctx, types.NamespacedName{Name: "kube-system"}, &ns); err != nil {
		return "", fmt.Errorf("read the kube-system Namespace for the receipt cluster identity (or set --receipt-cluster-id): %w", err)
	}
	return string(ns.UID), nil
}

// receiptConfig is the PromotionStep reconciler's receipt configuration.
func (f *receiptFlags) receiptConfig(clusterID string) *psreconciler.ReceiptConfig {
	return &psreconciler.ReceiptConfig{ClusterID: clusterID, ControllerVersion: ControllerVersion,
		ChartVersion: os.Getenv("KARDINAL_CHART_VERSION")}
}

// ociSink is the OCI sink the flags configure, nil without --receipt-oci.
func (f *receiptFlags) ociSink(ns string, transport http.RoundTripper) *receiptrecon.OCISink {
	if !f.oci {
		return nil
	}
	sink := &receiptrecon.OCISink{Registry: receipt.Registry{Transport: transport, Insecure: f.ociInsecure}, SecretNamespace: ns}
	sink.SecretName, sink.Repository = f.ociSecret, f.ociRepo
	return sink
}

// receiptTransport is the egress-guarded transport of every receipt call.
func receiptTransport() http.RoundTripper {
	return egress.NewTransport(http.ProxyFromEnvironment)
}
