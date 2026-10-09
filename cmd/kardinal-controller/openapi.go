// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	_ "embed"
	"net/http"
)

// openAPISpec is the OpenAPI 3.1 description of the controller's REST APIs:
// the UI API, the Bundle API and the SCM webhook health endpoint. It is
// generated from the request and response types and the route table in
// openapi_test.go (TestOpenAPISpecIsUpToDate) and published as
// docs/reference/openapi.json.
//
//go:embed openapi.json
var openAPISpec []byte

// openAPIPath is where both servers serve the spec.
const openAPIPath = "/api/v1/openapi.json"

// handleOpenAPI serves GET /api/v1/openapi.json.
func handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(openAPISpec)
}
