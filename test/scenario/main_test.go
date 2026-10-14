// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package scenario

import (
	"os"
	"testing"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/steps"
)

// TestMain marks the process as a render Job (steps.RenderJobEnv), as the
// kardinal-render image does: the kubelet of the scenario World runs render
// Jobs in this process (World.runJobs). The controller's own step sequences
// never contain render-manifests, so the mark changes nothing for them.
func TestMain(m *testing.M) {
	if err := os.Setenv(steps.RenderJobEnv, "1"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
