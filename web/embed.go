// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package web embeds the kardinal-ui React application into the controller binary.
// The UI is built into web/dist and embedded via go:embed. `make ui`, the
// Dockerfile and the CI frontend job all build it with npm (package-lock.json),
// and CI fails when the committed web/dist differs from a fresh build.
package web

import "embed"

// Assets holds the compiled React UI static files from web/dist/.
// Build with: make ui (runs npm ci && npm run build in web/).
//
//go:embed all:dist
var Assets embed.FS
