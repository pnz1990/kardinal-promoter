// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// GitSecretName is the Secret hack/e2e/up.sh creates in ControllerNamespace
// with the git server token (key "token"). Namespace copies it into each test
// namespace, where Pipelines reference it as spec.git.secretRef.
const GitSecretName = "git-token"

var nonSlug = regexp.MustCompile(`[^a-z0-9-]+`)

// Namespace creates a namespace for the test, copies the git token Secret into
// it, and deletes it when the test ends. On failure it first writes
// diagnostics (see Diagnose). KARDINAL_E2E_KEEP=1 keeps the namespace.
func (e *Env) Namespace(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	nonce := make([]byte, 4)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("namespace nonce: %v", err)
	}
	name := namespaceFor(t.Name(), hex.EncodeToString(nonce))

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{"kardinal.io/e2e": "true"},
	}}
	if err := e.Client.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", name, err)
	}

	var src corev1.Secret
	err := e.Client.Get(ctx, types.NamespacedName{Namespace: ControllerNamespace, Name: GitSecretName}, &src)
	switch {
	case apierrors.IsNotFound(err):
		// Suites without a git server (none today) have no token to copy.
	case err != nil:
		t.Fatalf("read %s/%s: %v", ControllerNamespace, GitSecretName, err)
	default:
		cp := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: name, Name: GitSecretName},
			Data:       src.Data,
		}
		if err := e.Client.Create(ctx, cp); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatalf("copy git Secret into %s: %v", name, err)
		}
	}

	t.Cleanup(func() {
		if t.Failed() {
			e.Diagnose(t, name)
		}
		if os.Getenv(EnvKeep) == "1" {
			t.Logf("keeping namespace %s (%s=1)", name, EnvKeep)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := e.Client.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete namespace %s: %v", name, err)
		}
	})
	return name
}

// namespaceFor turns a test name into a namespace name: a readable prefix
// plus a hash of the full name and nonce, at most 63 characters. The nonce
// makes every run's name new, so a run never meets the previous run's
// namespace while it terminates (or its repo, with KARDINAL_E2E_KEEP=1).
func namespaceFor(testName, nonce string) string {
	sum := sha256.Sum256([]byte(testName + "\x00" + nonce))
	suffix := hex.EncodeToString(sum[:])[:8]
	base := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(testName), "-"), "-")
	if max := 63 - len("e2e--") - len(suffix); len(base) > max {
		base = strings.TrimRight(base[:max], "-")
	}
	return "e2e-" + base + "-" + suffix
}
