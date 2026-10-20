// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package agentcases

import (
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/test/kubetest"
)

// TestMain keeps every test of this package, and every process it starts,
// away from the ambient kubeconfig and the in-cluster config
// (test/kubetest; make hygiene checks it is here, in a file without build
// constraints so every tag set has it).
func TestMain(m *testing.M) { kubetest.Main(m) }
