// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package kindcontext decides which kube context the cluster e2e tests
// (test/e2e, build tag e2e) may use. They never use the current context: the
// context must be named explicitly and must be a kind cluster.
package kindcontext

import (
	"fmt"
	"strings"
)

// EnvVar names the kube context the tagged cluster tests may use.
const EnvVar = "KARDINAL_E2E_CONTEXT"

// FromEnv validates the value of KARDINAL_E2E_CONTEXT. ok is false when it is
// unset (the cluster tests skip); an error means it names a context that is
// not a kind cluster, which the tests refuse to touch.
func FromEnv(value string) (kubeContext string, ok bool, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, nil
	}
	if !strings.HasPrefix(value, "kind-") {
		return "", false, fmt.Errorf("%s=%q: refusing to run cluster e2e tests against a non-kind context", EnvVar, value)
	}
	return value, true, nil
}
