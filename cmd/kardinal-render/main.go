// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Command kardinal-render renders one layout: branch environment. It runs
// only as the Job of a RenderRun (the RenderRun reconciler creates it), never
// in the controller: see pkg/renderjob and docs/rendered-manifests.md.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/kardinal-promoter/kardinal-promoter/pkg/renderjob"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	code := renderjob.Main(ctx)
	stop()
	os.Exit(code)
}
