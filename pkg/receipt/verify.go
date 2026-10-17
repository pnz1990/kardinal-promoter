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
	"bytes"
	"crypto"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/sigstore/sigstore/pkg/cryptoutils"
	"github.com/sigstore/sigstore/pkg/signature"
)

// VerifyOptions say what a receipt must prove.
type VerifyOptions struct {
	// Key is the public key of key and kms receipts (PEM). Exactly one of
	// Key and Identity is set.
	Key []byte
	// Identity and Issuer are the certificate identity of keyless receipts:
	// the subject alternative name and the OIDC issuer, both required.
	Identity string
	Issuer   string
	// TrustedRoot holds the Fulcio, Rekor and timestamp authority keys:
	// required for keyless receipts, and for a key receipt's Rekor entry.
	TrustedRoot root.TrustedMaterial
	// Subject, when set, must be one of the statement's subjects: a
	// sha256:<hex> image digest or a git commit.
	Subject string
	// Environment, Namespace, Pipeline and ClusterID, when set, must be
	// those of resourceUri (kardinal://<cluster>/namespaces/<ns>/pipelines/
	// <pipeline>/environments/<env>), exactly: a receipt for another
	// tenant's pipeline, or another cluster's, is refused. The statement's
	// own pipeline must match its resourceUri.
	Environment string
	Namespace   string
	Pipeline    string
	ClusterID   string
	// RequireCluster refuses a receipt that names a cluster when ClusterID
	// is not set: outside the cluster, another cluster's receipt signed with
	// the same key would pass (verify-receipt --any-cluster opts out).
	RequireCluster bool
	// Require lists levels that must be present (KARDINAL_ is optional);
	// a leading ! means the level must be absent.
	Require []string
	// Statement, when set, must equal the signed payload byte for byte:
	// the PromotionReceipt's spec.statement in the cluster.
	Statement []byte
}

// Verified is a receipt that passed every check.
type Verified struct {
	Statement *Statement
	// Signer is the certificate identity, or "key" for a key receipt.
	Signer string
	// Logged is true when a Rekor entry was verified: its inclusion, against
	// the trusted root's log keys.
	Logged bool
	// LogNotChecked is true when the bundle has a Rekor entry that was not
	// verified: a key receipt verified without a trusted root.
	LogNotChecked bool
}

// Verify checks a receipt bundle against o. Every failure is an error: a
// caller exits non-zero on any.
func Verify(bundleJSON []byte, o VerifyOptions) (*Verified, error) {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return nil, fmt.Errorf("not a Sigstore bundle: %w", err)
	}
	if (len(o.Key) == 0) == (o.Identity == "") {
		return nil, errors.New("give either a public key or a certificate identity (--key, or --identity and --issuer)")
	}
	logged := b.HasInclusionPromise() || b.HasInclusionProof()
	// Whether the entry is verified: keyless always requires it; a key
	// receipt only with a trusted root holding the log's keys.
	logChecked := logged && (len(o.Key) == 0 || o.TrustedRoot != nil)
	var v *verify.Verifier
	var policy verify.PolicyBuilder
	signer := "key"
	switch {
	case len(o.Key) > 0:
		pub, err := cryptoutils.UnmarshalPEMToPublicKey(o.Key)
		if err != nil {
			return nil, fmt.Errorf("public key: %w", err)
		}
		v, err = keyVerifier(pub, o.TrustedRoot, logged)
		if err != nil {
			return nil, err
		}
		policy = verify.NewPolicy(verify.WithoutArtifactUnsafe(), verify.WithKey())
	default:
		if o.Issuer == "" {
			return nil, errors.New("a keyless receipt needs both --identity and --issuer: no default trusts any identity")
		}
		if o.TrustedRoot == nil {
			return nil, errors.New("a keyless receipt needs a trusted root (--trusted-root)")
		}
		id, err := verify.NewShortCertificateIdentity(o.Issuer, "", o.Identity, "")
		if err != nil {
			return nil, fmt.Errorf("certificate identity: %w", err)
		}
		opts := []verify.VerifierOption{verify.WithObserverTimestamps(1)}
		if logged {
			opts = append(opts, verify.WithTransparencyLog(1))
		}
		v, err = verify.NewVerifier(o.TrustedRoot, opts...)
		if err != nil {
			return nil, fmt.Errorf("verifier: %w", err)
		}
		policy = verify.NewPolicy(verify.WithoutArtifactUnsafe(), verify.WithCertificateIdentity(id))
		signer = o.Identity
	}
	res, err := v.Verify(&b, policy)
	if err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	if res.Statement == nil {
		return nil, errors.New("the bundle does not sign an in-toto statement")
	}
	env, err := b.Envelope()
	if err != nil {
		return nil, fmt.Errorf("envelope: %w", err)
	}
	payload, err := env.RawEnvelope().DecodeB64Payload()
	if err != nil {
		return nil, fmt.Errorf("envelope payload: %w", err)
	}
	if o.Statement != nil && !bytes.Equal(payload, o.Statement) {
		return nil, fmt.Errorf("the cluster's statement (%s) is not the signed one (%s): the PromotionReceipt was changed after it was signed",
			Digest(o.Statement), Digest(payload))
	}
	st, err := Parse(payload)
	if err != nil {
		return nil, err
	}
	if err := checkPredicate(st, o); err != nil {
		return nil, err
	}
	return &Verified{Statement: st, Signer: signer, Logged: logChecked, LogNotChecked: logged && !logChecked}, nil
}

func keyVerifier(pub crypto.PublicKey, trusted root.TrustedMaterial, logged bool) (*verify.Verifier, error) {
	sv, err := signature.LoadVerifier(pub, crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("public key: %w", err)
	}
	keyMaterial := root.NewTrustedPublicKeyMaterial(func(string) (root.TimeConstrainedVerifier, error) {
		return root.NewExpiringKey(sv, time.Time{}, time.Time{}), nil
	})
	if logged && trusted != nil {
		v, err := verify.NewVerifier(root.TrustedMaterialCollection{keyMaterial, trusted},
			verify.WithTransparencyLog(1), verify.WithIntegratedTimestamps(1))
		if err != nil {
			return nil, fmt.Errorf("verifier: %w", err)
		}
		return v, nil
	}
	v, err := verify.NewVerifier(keyMaterial, verify.WithNoObserverTimestamps())
	if err != nil {
		return nil, fmt.Errorf("verifier: %w", err)
	}
	return v, nil
}

// checkPredicate checks the VSA: PASSED, the subject, the environment and
// the required levels.
func checkPredicate(st *Statement, o VerifyOptions) error {
	p := st.Predicate
	if p.VerificationResult != ResultPassed {
		return fmt.Errorf("verificationResult is %q, not %s", p.VerificationResult, ResultPassed)
	}
	if o.Subject != "" && !hasSubject(st, o.Subject) {
		return fmt.Errorf("the receipt is not about %s", o.Subject)
	}
	r, err := ParseResourceURI(p.ResourceURI)
	if err != nil {
		return err
	}
	if st.Predicate.Kardinal.Pipeline != r.Pipeline {
		return fmt.Errorf("the receipt's pipeline %q is not its resourceUri's (%s)", st.Predicate.Kardinal.Pipeline, p.ResourceURI)
	}
	if o.RequireCluster && o.ClusterID == "" && r.ClusterID != "" {
		return fmt.Errorf("the receipt is for cluster %s: pin it with --cluster %s, or give --any-cluster to accept any", r.ClusterID, r.ClusterID)
	}
	for _, c := range []struct{ what, want, got string }{
		{"cluster", o.ClusterID, r.ClusterID}, {"namespace", o.Namespace, r.Namespace},
		{"pipeline", o.Pipeline, r.Pipeline}, {"environment", o.Environment, r.Environment},
	} {
		if c.want != "" && c.want != c.got {
			return fmt.Errorf("the receipt is for %s, not %s %s", p.ResourceURI, c.what, c.want)
		}
	}
	for _, r := range o.Require {
		want := true
		if strings.HasPrefix(r, "!") {
			want, r = false, r[1:]
		}
		if !strings.HasPrefix(r, "KARDINAL_") && !strings.HasPrefix(r, "SLSA_") {
			r = "KARDINAL_" + r
		}
		if hasLevel(p.VerifiedLevels, r) != want {
			if want {
				return fmt.Errorf("level %s is required and not verified", r)
			}
			return fmt.Errorf("level %s must be absent and is present", r)
		}
	}
	return nil
}

// Resource is a parsed resourceUri.
type Resource struct {
	ClusterID, Namespace, Pipeline, Environment string
}

// ParseResourceURI parses kardinal://<cluster>/namespaces/<ns>/pipelines/
// <pipeline>/environments/<env>; anything else is an error.
func ParseResourceURI(uri string) (Resource, error) {
	rest, ok := strings.CutPrefix(uri, "kardinal://")
	parts := strings.Split(rest, "/")
	if !ok || len(parts) != 7 || parts[1] != "namespaces" || parts[3] != "pipelines" || parts[5] != "environments" {
		return Resource{}, fmt.Errorf("resourceUri %q is not kardinal://<cluster>/namespaces/<ns>/pipelines/<pipeline>/environments/<env>", uri)
	}
	// The cluster may be empty (a receipt without a cluster identity).
	for _, p := range []int{2, 4, 6} {
		if parts[p] == "" {
			return Resource{}, fmt.Errorf("resourceUri %q has an empty part", uri)
		}
	}
	return Resource{ClusterID: parts[0], Namespace: parts[2], Pipeline: parts[4], Environment: parts[6]}, nil
}

func hasSubject(st *Statement, want string) bool {
	hexPart := strings.TrimPrefix(want, "sha256:")
	for _, s := range st.Subject {
		if s.Digest[digestSHA256] == hexPart || s.Digest[digestGitCommit] == want {
			return true
		}
	}
	return false
}
