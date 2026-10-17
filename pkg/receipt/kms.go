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
	"time"
)

// kmsLoader loads the signing key of a KMS key reference. transport carries
// the KMS API calls (the controller's egress transport); a provider that
// does not speak HTTP applies the same guard to its own connections. timeout
// bounds each API call.
type kmsLoader func(ctx context.Context, ref string, transport http.RoundTripper, timeout time.Duration) (crypto.Signer, error)

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
		return nil
	}
	scheme, _, _ := strings.Cut(ref, "://")
	built := "none"
	if p := KMSProviders(); len(p) > 0 {
		built = strings.Join(p, ", ")
	}
	return fmt.Errorf("the controller was not built with the %s KMS provider (key reference %s; built in: %s)", scheme, ref, built)
}

// kmsSigner is the signing key of ref.
func kmsSigner(ctx context.Context, ref string, transport http.RoundTripper, timeout time.Duration) (crypto.Signer, error) {
	l, ok := kmsLoaderFor(ref)
	if !ok {
		return nil, KMSSupported(ref)
	}
	return l(ctx, ref, transport, timeout)
}
