// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build kms_gcp

package receipt

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"regexp"
	"sync/atomic"
	"time"

	kmsapi "cloud.google.com/go/kms/apiv1"
	gcpkms "github.com/sigstore/sigstore/pkg/signature/kms/gcp"
	"google.golang.org/api/option"
	gtransport "google.golang.org/api/transport/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/egress"
)

func init() {
	registerKMS(gcpkms.ReferenceScheme, loadGCPKMS)
	registerKMSCheck(gcpkms.ReferenceScheme, checkGCPRef)
}

// gcpRef is a Cloud KMS key version reference: sigstore's form, with the
// version required.
var gcpRef = regexp.MustCompile(`^gcpkms://projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+/(cryptoKeyVersions|versions)/[^/]+$`)

// checkGCPRef refuses a reference without a pinned key version: sigstore
// would sign with the newest enabled version, re-read every 5 minutes, so
// whoever may create a version would change the signing key, and
// verifiers holding the pinned public key would start failing. Rotating
// the key is a configuration change.
func checkGCPRef(ref string) error {
	if !gcpRef.MatchString(ref) {
		return fmt.Errorf("the Cloud KMS key reference %s must pin a key version "+
			"(gcpkms://projects/P/locations/L/keyRings/R/cryptoKeys/K/cryptoKeyVersions/V): without one the signing key changes when a version is added", ref)
	}
	return nil
}

// gcpDial dials the Cloud KMS API (gRPC) with the egress guard and the
// controller-wide allowlist; tests replace it. Credentials (Workload
// Identity, the metadata server, GOOGLE_APPLICATION_CREDENTIALS) come from
// Google's default chain, whose metadata requests are not user input.
var gcpDial = egress.DialContext

// gcpTestOptions are extra client options for tests (an emulator's endpoint,
// no authentication).
var gcpTestOptions []option.ClientOption

// gcpEndpoint is the Cloud KMS API (the client library's default).
const gcpEndpoint = "cloudkms.googleapis.com:443"

// loadGCPKMS loads a gcpkms:// key. Cloud KMS speaks gRPC, so the HTTP
// transport is not used: its connections go through gcpDial.
func loadGCPKMS(root context.Context, ref string, _ http.RoundTripper, timeout time.Duration) (kmsSignerFor, func(), error) {
	// The last dial error: gRPC retries a refused dial until the call's
	// deadline and reports only that, so a guard refusal is added back.
	var dialErr atomic.Value
	dialer := gcpDial
	dial := func(ctx context.Context, addr string) (net.Conn, error) {
		c, err := dialer(ctx, "tcp", addr)
		if err != nil {
			dialErr.Store(err)
		}
		return c, err
	}
	// Every call within timeout: sigstore reads the key version with a
	// background context.
	deadline := func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		err := invoker(ctx, method, req, reply, cc, opts...)
		// The guard's refusal is final: not an Unavailable the client
		// library retries for a minute.
		if d, ok := dialErr.Load().(error); ok && err != nil && errors.Is(d, egress.ErrBlockedAddress) {
			return status.Error(codes.PermissionDenied, d.Error())
		}
		return err
	}
	withDial := func(err error) error {
		if d, ok := dialErr.Load().(error); ok {
			return fmt.Errorf("%w (connecting: %w)", err, d)
		}
		return err
	}
	// The gRPC connection is ours, so closing the key closes it: sigstore's
	// client has no Close, and a connection it dialled itself would go on
	// redialling (and keep its goroutines) after a key that failed its
	// first use was dropped.
	opts := append([]option.ClientOption{option.WithEndpoint(gcpEndpoint),
		option.WithScopes(kmsapi.DefaultAuthScopes()...),
		option.WithGRPCDialOption(grpc.WithContextDialer(dial)),
		option.WithGRPCDialOption(grpc.WithUnaryInterceptor(deadline)),
		option.WithGRPCDialOption(grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(math.MaxInt32)))}, gcpTestOptions...)
	conn, err := gtransport.Dial(root, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to Cloud KMS: %w", withDial(err))
	}
	closeConn := func() { _ = conn.Close() }
	sv, err := gcpkms.LoadSignerVerifier(root, ref, option.WithGRPCConn(conn))
	if err != nil {
		closeConn()
		return nil, nil, fmt.Errorf("load the Cloud KMS key: %w", withDial(err))
	}
	signer := func(ctx context.Context) (crypto.Signer, error) {
		cs, _, err := sv.CryptoSigner(ctx, func(error) {})
		if err != nil {
			return nil, fmt.Errorf("the Cloud KMS signer: %w", withDial(err))
		}
		return cs, nil
	}
	return signer, closeConn, nil
}
