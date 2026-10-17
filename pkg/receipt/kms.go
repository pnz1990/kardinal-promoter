// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package receipt

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// kmsLoader loads the KMS key of a reference with root, a context that
// lives as long as the cached key (kmsRoot): transport carries the KMS API
// calls (the controller's egress transport); a provider that does not speak
// HTTP applies the same guard to its own connections. timeout bounds each
// API call. It returns the key's signer for one signature, given that
// signature's context (a crypto.Signer keeps the context it was made with,
// so one is made per signature), and close, which releases the key's client
// (its connections).
type kmsLoader func(root context.Context, ref string, transport http.RoundTripper, timeout time.Duration) (signer kmsSignerFor, close func(), err error)

// kmsSignerFor is a KMS key's crypto.Signer for one signature, whose calls
// use ctx.
type kmsSignerFor func(ctx context.Context) (crypto.Signer, error)

// kmsChecks check a key reference of their prefix before any call (a GCP
// key must pin its version, an AWS endpoint must not be a credential
// address): a reference that fails one is refused at start and at signing.
var kmsChecks = map[string]func(ref string) error{}

func registerKMSCheck(prefix string, check func(string) error) { kmsChecks[prefix] = check }

// kmsProviders are the KMS providers this binary was built with, by
// reference prefix (awskms://, gcpkms://). Each registers from a file behind
// its build tag (kms_aws, kms_gcp: docs/receipts.md, Signing modes), so the
// default build carries no cloud SDK. kardinal calls the provider directly:
// sigstore's kms.Get is never used, so its CLI-plugin fallback, a
// subprocess, cannot run.
var kmsProviders = map[string]kmsLoader{}

func registerKMS(prefix string, l kmsLoader) { kmsProviders[prefix] = l }

// KMSProviders are the reference prefixes of the KMS providers built in.
func KMSProviders() []string {
	out := make([]string, 0, len(kmsProviders))
	for p := range kmsProviders {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func kmsLoaderFor(ref string) (kmsLoader, bool) {
	for p, l := range kmsProviders {
		if strings.HasPrefix(ref, p) {
			return l, true
		}
	}
	return nil, false
}

// KMSSupported reports whether the controller was built with the provider
// of ref.
func KMSSupported(ref string) error {
	if ref == "" {
		return errors.New("kms receipt signing needs a key reference")
	}
	if _, ok := kmsLoaderFor(ref); ok {
		for p, check := range kmsChecks {
			if strings.HasPrefix(ref, p) {
				return check(ref)
			}
		}
		return nil
	}
	scheme, _, _ := strings.Cut(ref, "://")
	built := "none"
	if p := KMSProviders(); len(p) > 0 {
		built = strings.Join(p, ", ")
	}
	return fmt.Errorf("the controller was not built with the %s KMS provider (key reference %s; built in: %s)", scheme, ref, built)
}

// kmsKey is a cached KMS key: its reference and call timeout.
type kmsKey struct {
	ref     string
	timeout time.Duration
}

type kmsEntry struct {
	signer kmsSignerFor
	close  func()
}

// kmsCache holds each KMS key loaded, once per reference: a key's client
// (a Cloud KMS gRPC connection and its goroutines, the AWS SDK's key cache)
// is built at the first signature and reused by every later one and every
// retry, instead of one per receipt. A load that fails is not cached, so
// the next signature tries again. CloseKMS releases them at shutdown.
var kmsCache = struct {
	sync.Mutex
	m map[kmsKey]kmsEntry
}{m: map[kmsKey]kmsEntry{}}

// kmsRoot is the context the cached keys' clients are built with: it lives
// until CloseKMS (the controller's shutdown), never as short as one
// signature's, whose cancellation would otherwise break every later one.
var kmsRoot, kmsRootCancel = context.WithCancel(context.Background())

// kmsSigner is the signing key of ref, loaded once.
func kmsSigner(ctx context.Context, ref string, transport http.RoundTripper, timeout time.Duration) (crypto.Signer, error) {
	l, ok := kmsLoaderFor(ref)
	if !ok {
		return nil, KMSSupported(ref)
	}
	if err := KMSSupported(ref); err != nil {
		return nil, err
	}
	k := kmsKey{ref: ref, timeout: timeout}
	kmsCache.Lock()
	defer kmsCache.Unlock()
	if e, ok := kmsCache.m[k]; ok {
		return e.signer(ctx)
	}
	signer, closeFn, err := l(kmsRoot, ref, transport, timeout)
	if err != nil {
		return nil, err
	}
	if closeFn == nil {
		closeFn = func() {}
	}
	// Cached once it has made a signer: a client whose first use fails (an
	// address the egress guard refuses, a key it may not read) is closed and
	// loaded again at the next signature.
	cs, err := signer(ctx)
	if err != nil {
		closeFn()
		return nil, err
	}
	kmsCache.m[k] = kmsEntry{signer: signer, close: closeFn}
	return cs, nil
}

// CloseKMS releases every KMS key loaded (the controller's shutdown); a
// later signature loads its key again.
func CloseKMS() {
	kmsCache.Lock()
	defer kmsCache.Unlock()
	for k, e := range kmsCache.m {
		e.close()
		delete(kmsCache.m, k)
	}
	kmsRootCancel()
	kmsRoot, kmsRootCancel = context.WithCancel(context.Background())
}
