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

package receipt

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	httptransport "github.com/go-openapi/runtime/client"
	"github.com/go-openapi/strfmt"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	rekorclient "github.com/sigstore/rekor/pkg/generated/client"
	rekorutil "github.com/sigstore/rekor/pkg/util"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/sign"
	"github.com/sigstore/sigstore/pkg/cryptoutils"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// Signed is a signed statement.
type Signed struct {
	// Bundle is the Sigstore bundle, JSON.
	Bundle   []byte
	SignedBy v1alpha1.ReceiptSigner
	// LogIndex is the Rekor entry's index, nil without Rekor.
	LogIndex *int64
}

// KeySource returns the signing key of key mode: an ECDSA P-256 private
// key, and the identity to record (the Secret it came from).
type KeySource func(ctx context.Context) (crypto.Signer, string, error)

// Signer signs receipt statements. It is configured per controller
// (--receipt-signing-mode and the URLs); every network call it makes uses
// Transport and is bounded by Timeout.
type Signer struct {
	// Mode is keyless, key or kms.
	Mode string
	// Key is the key of key mode.
	Key KeySource
	// KMSRef is the key reference of kms mode (awskms://, gcpkms://, ...).
	KMSRef string
	// FulcioURL is the Fulcio instance of keyless mode.
	FulcioURL string
	// TokenFile is the projected ServiceAccount token (audience sigstore)
	// keyless mode presents to Fulcio. It is read on every signature: the
	// kubelet rotates it.
	TokenFile string
	// RekorURL, when set, logs every signature in that Rekor instance.
	RekorURL string
	// RekorVersion is the Rekor API: 1 (the default, rekor.sigstore.dev) or
	// 2 (rekor-tiles, the log20xx shards).
	RekorVersion uint32
	// TSAURL, when set, timestamps every signature with that RFC 3161
	// timestamp authority.
	TSAURL string
	// Transport carries every request; nil is http.DefaultTransport.
	Transport http.RoundTripper
	// Timeout bounds each request; zero is 30s.
	Timeout time.Duration
}

// Validate reports a configuration that cannot sign.
func (s *Signer) Validate() error {
	switch s.Mode {
	case v1alpha1.ReceiptSigningKeyless:
		if s.FulcioURL == "" || s.TokenFile == "" {
			return errors.New("keyless receipt signing needs a Fulcio URL and a token file")
		}
		if s.RekorURL == "" && s.TSAURL == "" {
			return errors.New("keyless receipt signing needs a Rekor or a timestamp authority URL: the certificate is short-lived, so the signing time must be proven")
		}
		// A Rekor v2 entry has no integrated time: it proves inclusion, not
		// when. Without a timestamp authority a keyless receipt could never
		// verify once its certificate expired, so it is refused here rather
		// than marked Signed.
		if s.RekorURL != "" && s.RekorVersion == 2 && s.TSAURL == "" {
			return errors.New("keyless receipt signing with Rekor v2 needs a timestamp authority URL: a Rekor v2 entry does not prove the signing time, and the certificate is short-lived")
		}
	case v1alpha1.ReceiptSigningKey:
		if s.Key == nil {
			return errors.New("key receipt signing needs a key Secret")
		}
	case v1alpha1.ReceiptSigningKMS:
		if err := KMSSupported(s.KMSRef); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown receipt signing mode %q (keyless, key or kms)", s.Mode)
	}
	return nil
}

func (s *Signer) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 30 * time.Second
}

// Sign signs statement as a DSSE envelope and returns its Sigstore bundle.
func (s *Signer) Sign(ctx context.Context, statement []byte) (*Signed, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	opts := sign.BundleOptions{Context: ctx}
	// Every Fulcio, timestamp authority and Rekor request goes through rt,
	// which records a server's Retry-After for the reconciler's retry.
	rt := newRetryAfterTransport(s.Transport)
	var keypair sign.Keypair
	signedBy := v1alpha1.ReceiptSigner{Mode: s.Mode}
	switch s.Mode {
	case v1alpha1.ReceiptSigningKeyless:
		kp, err := sign.NewEphemeralKeypair(nil)
		if err != nil {
			return nil, fmt.Errorf("ephemeral key: %w", err)
		}
		token, err := os.ReadFile(s.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("read the sigstore token: %w", err)
		}
		keypair = kp
		opts.CertificateProvider = sign.NewFulcio(&sign.FulcioOptions{
			BaseURL: s.FulcioURL, Timeout: s.timeout(), Retries: 1, Transport: rt,
		})
		opts.CertificateProviderOptions = &sign.CertificateProviderOptions{IDToken: strings.TrimSpace(string(token))}
	case v1alpha1.ReceiptSigningKey:
		key, identity, err := s.Key(ctx)
		if err != nil {
			return nil, err
		}
		kp, err := newKeypair(key)
		if err != nil {
			return nil, err
		}
		keypair, signedBy.Identity, signedBy.KeyHint = kp, identity, string(kp.hint)
	case v1alpha1.ReceiptSigningKMS:
		cs, err := kmsSigner(ctx, s.KMSRef, s.Transport, s.timeout())
		if err != nil {
			return nil, fmt.Errorf("kms key %s: %w", s.KMSRef, err)
		}
		kp, err := newKeypair(cs)
		if err != nil {
			return nil, fmt.Errorf("kms key %s: %w", s.KMSRef, err)
		}
		keypair, signedBy.Identity, signedBy.KeyHint = kp, s.KMSRef, string(kp.hint)
	}
	if s.TSAURL != "" {
		opts.TimestampAuthorities = []*sign.TimestampAuthority{sign.NewTimestampAuthority(&sign.TimestampAuthorityOptions{
			URL: s.TSAURL, Timeout: s.timeout(), Retries: 1, Transport: rt,
		})}
	}
	if s.RekorURL != "" {
		switch s.RekorVersion {
		case 0, 1:
			rc, err := s.rekorClient(rt)
			if err != nil {
				return nil, err
			}
			opts.TransparencyLogs = []sign.Transparency{sign.NewRekor(&sign.RekorOptions{
				BaseURL: s.RekorURL, Timeout: s.timeout(), Client: rc.Entries,
			})}
		case 2:
			w, err := newRekorV2Writer(s.RekorURL, rt, s.timeout())
			if err != nil {
				return nil, err
			}
			opts.TransparencyLogs = []sign.Transparency{sign.NewRekor(&sign.RekorOptions{
				BaseURL: s.RekorURL, Timeout: s.timeout(), ClientV2: w, Version: 2,
			})}
		default:
			return nil, fmt.Errorf("unknown Rekor API version %d (1 or 2)", s.RekorVersion)
		}
	}
	pb, err := sign.Bundle(&sign.DSSEData{Data: statement, PayloadType: PayloadType}, keypair, opts)
	if err != nil {
		err = fmt.Errorf("sign receipt: %w", err)
		if d := rt.longest(); d > 0 {
			return nil, &RetryAfterError{After: d, Err: err}
		}
		return nil, err
	}
	b, err := bundle.NewBundle(pb)
	if err != nil {
		return nil, fmt.Errorf("sign receipt: %w", err)
	}
	raw, err := b.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal receipt bundle: %w", err)
	}
	out := &Signed{Bundle: raw, SignedBy: signedBy}
	if c := pb.GetVerificationMaterial().GetCertificate(); c != nil {
		if cert, err := x509.ParseCertificate(c.GetRawBytes()); err == nil {
			if sum, err := certificate.SummarizeCertificate(cert); err == nil {
				out.SignedBy.Identity, out.SignedBy.Issuer = sum.SubjectAlternativeName, sum.Issuer
			}
		}
	}
	for _, e := range pb.GetVerificationMaterial().GetTlogEntries() {
		i := e.GetLogIndex()
		out.LogIndex = &i
	}
	return out, nil
}

// rekorClient is a Rekor v1 client on rt, s.Transport's wrapper (rekor's
// own constructor builds its transport, so it cannot be egress-guarded).
func (s *Signer) rekorClient(rt http.RoundTripper) (*rekorclient.Rekor, error) {
	u, err := url.Parse(s.RekorURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid Rekor URL %q", s.RekorURL)
	}
	if u.Path == "" {
		u.Path = rekorclient.DefaultBasePath
	}
	if rt == nil {
		rt = http.DefaultTransport
	}
	t := httptransport.NewWithClient(u.Host, u.Path, []string{u.Scheme}, &http.Client{Transport: rt, Timeout: s.timeout()})
	registry := strfmt.Default
	registry.Add("signedCheckpoint", &rekorutil.SignedNote{}, rekorutil.SignedCheckpointValidator)
	return rekorclient.New(t, registry), nil
}

// keypair is a sign.Keypair over an ECDSA P-256 crypto.Signer: a key from
// a Secret or a KMS key.
type keypair struct {
	signer crypto.Signer
	hint   []byte
}

func newKeypair(s crypto.Signer) (*keypair, error) {
	pub, ok := s.Public().(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("receipt signing keys must be ECDSA P-256")
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("marshal public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return &keypair{signer: s, hint: []byte(base64.StdEncoding.EncodeToString(sum[:]))}, nil
}

func (k *keypair) GetHashAlgorithm() protocommon.HashAlgorithm {
	return protocommon.HashAlgorithm_SHA2_256
}

func (k *keypair) GetSigningAlgorithm() protocommon.PublicKeyDetails {
	return protocommon.PublicKeyDetails_PKIX_ECDSA_P256_SHA_256
}

func (k *keypair) GetHint() []byte { return k.hint }

func (k *keypair) GetKeyAlgorithm() string { return "ECDSA" }

func (k *keypair) GetPublicKey() crypto.PublicKey { return k.signer.Public() }

func (k *keypair) GetPublicKeyPem() (string, error) {
	b, err := cryptoutils.MarshalPublicKeyToPEM(k.signer.Public())
	if err != nil {
		return "", fmt.Errorf("marshal public key: %w", err)
	}
	return string(b), nil
}

func (k *keypair) SignData(_ context.Context, data []byte) ([]byte, []byte, error) {
	sum := sha256.Sum256(data)
	sig, err := k.signer.Sign(rand.Reader, sum[:], crypto.SHA256)
	if err != nil {
		return nil, nil, fmt.Errorf("sign: %w", err)
	}
	return sig, sum[:], nil
}

// ParsePrivateKey reads an ECDSA P-256 private key, PEM (PKCS #8 or SEC 1).
// An encrypted key is refused: the controller has no passphrase.
func ParsePrivateKey(raw []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("the receipt key is not PEM")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("the receipt key is a %q PEM block; want an unencrypted PRIVATE KEY or EC PRIVATE KEY", block.Type)
	}
	if err != nil {
		return nil, errors.New("the receipt key cannot be parsed")
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("receipt signing keys must be ECDSA P-256")
	}
	return ec, nil
}
