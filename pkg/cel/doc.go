// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Package cel contains kro CEL library extensions used by the PolicyGate
// reconciler and by `kardinal policy simulate`.
//
// The evaluator and environment previously in this package have been moved
// into pkg/reconciler/policygate (see #130); the UI validate-cel endpoint uses
// policygate.ValidateExpression. This package now contains only:
//   - pkg/cel/library/ — kro CEL library extensions (JSON, Maps, Lists, Random)
//   - pkg/cel/conversion/ — type conversion utilities used by the libraries
package cel
