// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"os/exec"
	"strings"
	"testing"
)

// Kubectl runs kubectl against the test cluster (--context is always set)
// with stdin as its input, and returns its combined output. It fails the
// test on a non-zero exit.
func (e *Env) Kubectl(t *testing.T, namespace, stdin string, args ...string) string {
	t.Helper()
	full := append([]string{"--context", e.Context}, args...)
	if namespace != "" {
		full = append([]string{"-n", namespace}, full...)
	}
	cmd := exec.Command("kubectl", full...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	t.Logf("$ kubectl %s\n%s", strings.Join(full, " "), out)
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
