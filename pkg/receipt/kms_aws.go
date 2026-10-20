// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build kms_aws

package receipt

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/config"
	awskms "github.com/sigstore/sigstore/pkg/signature/kms/aws"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
)

func init() {
	registerKMS(awskms.ReferenceScheme, loadAWSKMS)
	registerKMSCheck(awskms.ReferenceScheme, checkAWSRef)
}

// awsCredentialEndpoints are where the AWS SDK's default credential chain
// asks for credentials: instance metadata, the ECS task role and the EKS
// Pod Identity agent. They are link-local or metadata addresses, which the
// egress guard denies to the URLs users write; the SDK's own requests to
// them are not user input.
var awsCredentialEndpoints = map[netip.Addr]bool{
	netip.MustParseAddr("169.254.169.254"): true,
	netip.MustParseAddr("169.254.170.2"):   true,
	netip.MustParseAddr("169.254.170.23"):  true,
	netip.MustParseAddr("fd00:ec2::254"):   true,
	netip.MustParseAddr("fd00:ec2::23"):    true,
}

// awsTransport sends the credential chain's requests directly and every
// other request (KMS, STS) through the egress transport, with its guard and
// allowlist. Redirects are not followed (see loadAWSKMS), so a response
// cannot send a request to a credential endpoint.
type awsTransport struct{ egress, credentials http.RoundTripper }

func (t awsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if ip, err := netip.ParseAddr(strings.Trim(req.URL.Hostname(), "[]")); err == nil && awsCredentialEndpoints[ip.Unmap()] {
		return t.credentials.RoundTrip(req)
	}
	return t.egress.RoundTrip(req)
}

// loadAWSKMS loads an awskms:// key with the SDK's default configuration
// (region, credentials: IRSA, Pod Identity, instance role or the
// environment).
func loadAWSKMS(root context.Context, ref string, transport http.RoundTripper, timeout time.Duration) (kmsSignerFor, func(), error) {
	if transport == nil {
		transport = egress.NewTransport(http.ProxyFromEnvironment)
	}
	direct := http.DefaultTransport.(*http.Transport).Clone()
	direct.Proxy = nil
	client := &http.Client{
		Timeout:   timeout,
		Transport: awsTransport{egress: transport, credentials: direct},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	opts := []func(*config.LoadOptions) error{config.WithHTTPClient(client)}
	// The region of an ARN reference; otherwise the SDK's (AWS_REGION, which
	// IRSA and Pod Identity set, or the profile).
	if _, keyID, _, err := awskms.ParseReference(ref); err == nil {
		if a, err := arn.Parse(keyID); err == nil && a.Region != "" {
			opts = append(opts, config.WithRegion(a.Region))
		}
	}
	sv, err := awskms.LoadSignerVerifier(root, ref, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("load the AWS KMS key: %w", err)
	}
	signer := func(ctx context.Context) (crypto.Signer, error) {
		cs, _, err := sv.CryptoSigner(ctx, func(error) {})
		if err != nil {
			return nil, fmt.Errorf("the AWS KMS signer: %w", err)
		}
		return cs, nil
	}
	return signer, client.CloseIdleConnections, nil
}

// checkAWSRef refuses AWS_TLS_INSECURE_SKIP_VERIFY, and a reference whose
// endpoint is a credential address
// (instance metadata, the ECS or Pod Identity agent): requests there go
// directly, without the egress guard, so a KMS request must never be sent
// there, even by a reference the operator wrote.
func checkAWSRef(ref string) error {
	// sigstore replaces the HTTP client with one that skips TLS verification
	// when this is set: no egress guard, no certificate check. Checked at
	// every signature, as the key is loaded once.
	if os.Getenv("AWS_TLS_INSECURE_SKIP_VERIFY") == "1" {
		return errors.New("AWS_TLS_INSECURE_SKIP_VERIFY=1 would skip TLS verification and the egress guard for the KMS calls: unset it")
	}
	rest, ok := strings.CutPrefix(ref, awskms.ReferenceScheme)
	if !ok {
		return nil
	}
	endpoint, _, found := strings.Cut(rest, "/")
	if !found || endpoint == "" {
		return nil
	}
	host := endpoint
	if h, _, err := net.SplitHostPort(endpoint); err == nil {
		host = h
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil && awsCredentialEndpoints[ip.Unmap()] {
		return fmt.Errorf("AWS KMS key reference %s: the endpoint %s is a credential address, not a KMS endpoint", ref, endpoint)
	}
	return nil
}
