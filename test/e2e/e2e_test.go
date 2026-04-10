// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package e2e contains end-to-end journey tests for kardinal-promoter.
//
// Each test function corresponds to one journey in docs/aide/definition-of-done.md.
// The project is complete when all five journey tests pass against a real kind cluster.
//
// Run individual journeys:
//
//	make test-e2e-journey-1
//	make test-e2e-journey-2
//	...
//
// Run all journeys (creates and destroys kind cluster):
//
//	make test-e2e
package e2e
