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

// Package receipttest is a local Sigstore for receipt tests: a Fulcio that
// certifies any key for one identity, an RFC 3161 timestamp authority, and
// the trusted root of both. Never use it outside tests.
package receipttest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/digitorus/timestamp"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
)

// Sigstore is a local Fulcio and timestamp authority.
type Sigstore struct {
	// Fulcio and TSA are the servers' URLs.
	Fulcio, TSA string
	// Identity and Issuer are what Fulcio certifies.
	Identity, Issuer string
	// Root is the trusted root of both.
	Root *root.TrustedRoot

	servers []*httptest.Server
}

// Close stops the servers.
func (s *Sigstore) Close() {
	for _, srv := range s.servers {
		srv.Close()
	}
}

// NewSigstore starts a Fulcio certifying identity for issuer, and a
// timestamp authority.
func NewSigstore(identity, issuer string) (*Sigstore, error) {
	rootCert, rootKey, err := ca.GenerateRootCa()
	if err != nil {
		return nil, fmt.Errorf("root: %w", err)
	}
	fulcioCert, fulcioKey, err := ca.GenerateFulcioIntermediate(rootCert, rootKey)
	if err != nil {
		return nil, fmt.Errorf("fulcio: %w", err)
	}
	tsaRoot, tsaRootKey, err := ca.GenerateRootCa()
	if err != nil {
		return nil, fmt.Errorf("tsa root: %w", err)
	}
	tsaInter, tsaInterKey, err := ca.GenerateTSAIntermediate(tsaRoot, tsaRootKey)
	if err != nil {
		return nil, fmt.Errorf("tsa intermediate: %w", err)
	}
	tsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("tsa key: %w", err)
	}
	tsaLeaf, err := ca.GenerateTSALeafCert(time.Now().Add(-time.Minute), tsaKey, tsaInter, tsaInterKey)
	if err != nil {
		return nil, fmt.Errorf("tsa leaf: %w", err)
	}
	now := time.Now()
	tr, err := root.NewTrustedRoot(root.TrustedRootMediaType01,
		[]root.CertificateAuthority{&root.FulcioCertificateAuthority{
			Root: rootCert, Intermediates: []*x509.Certificate{fulcioCert},
			ValidityPeriodStart: now.Add(-time.Hour), ValidityPeriodEnd: now.Add(time.Hour),
		}}, nil,
		[]root.TimestampingAuthority{&root.SigstoreTimestampingAuthority{
			Root: tsaRoot, Intermediates: []*x509.Certificate{tsaInter}, Leaf: tsaLeaf,
			ValidityPeriodStart: now.Add(-time.Hour), ValidityPeriodEnd: now.Add(time.Hour),
		}}, nil)
	if err != nil {
		return nil, fmt.Errorf("trusted root: %w", err)
	}
	s := &Sigstore{Identity: identity, Issuer: issuer, Root: tr}
	fulcio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.certify(w, r, fulcioCert, fulcioKey)
	}))
	tsa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stamp(w, r, tsaLeaf, tsaKey)
	}))
	s.servers = []*httptest.Server{fulcio, tsa}
	s.Fulcio, s.TSA = fulcio.URL, tsa.URL
	return s, nil
}

// certify answers a Fulcio v2 signingCert request with a certificate for
// the request's public key. The token is not checked.
func (s *Sigstore) certify(w http.ResponseWriter, r *http.Request, parent *x509.Certificate, parentKey crypto.Signer) {
	var req struct {
		PublicKeyRequest struct {
			PublicKey struct {
				Content string `json:"content"`
			} `json:"publicKey"`
		} `json:"publicKeyRequest"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pub, err := cryptoutils.UnmarshalPEMToPublicKey([]byte(req.PublicKeyRequest.PublicKey.Content))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	leaf, err := ca.GenerateLeafCert(s.Identity, s.Issuer, time.Now().Add(-time.Minute), publicOnly{pub}, parent, parentKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	chain := []string{
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})),
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: parent.Raw})),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"signedCertificateEmbeddedSct": map[string]any{"chain": map[string]any{"certificates": chain}},
	})
}

// stamp answers an RFC 3161 timestamp request.
func stamp(w http.ResponseWriter, r *http.Request, leaf *x509.Certificate, key *ecdsa.PrivateKey) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req, err := timestamp.ParseRequest(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := (&timestamp.Timestamp{
		HashAlgorithm: req.HashAlgorithm, HashedMessage: req.HashedMessage, Time: time.Now(),
		Nonce: req.Nonce, Policy: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 2},
		ExtraExtensions: req.Extensions,
	}).CreateResponseWithOpts(leaf, key, crypto.SHA256)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/timestamp-reply")
	_, _ = w.Write(resp)
}

// publicOnly is a crypto.Signer that only has a public key: the test CA
// takes a signer to read the leaf's public key.
type publicOnly struct{ pub crypto.PublicKey }

func (p publicOnly) Public() crypto.PublicKey { return p.pub }

func (publicOnly) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("public key only")
}

// Key is a fresh ECDSA P-256 key: the private key as PKCS #8 PEM, and the
// public key as PEM.
func Key() (private, public []byte, err error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	pub, err := cryptoutils.MarshalPublicKeyToPEM(k.Public())
	if err != nil {
		return nil, nil, fmt.Errorf("marshal public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), pub, nil
}

// Token is an unsigned JWT with subject sub: the Fulcio client reads the
// subject to prove possession of the key; the local Fulcio does not check it.
func Token(sub string) string {
	enc := base64.RawURLEncoding
	payload, _ := json.Marshal(map[string]string{"sub": sub, "iss": "https://kubernetes.default.svc"})
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(payload) + ".x"
}
