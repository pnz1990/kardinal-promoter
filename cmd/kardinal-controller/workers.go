// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

// Default workers per controller (--<controller>-workers, chart
// controller.workers). Measured with the scale suite (#1509, docs in
// docs/installation.md#controller-concurrency): PromotionSteps hold a worker
// through git and SCM round trips and gain the most; PRStatus polls are SCM
// round trips too; Bundles, PolicyGates and Pipelines are API-bound and
// gain little past a few workers.
const (
	defaultPromotionStepWorkers = 16
	defaultBundleWorkers        = 4
	defaultPRStatusWorkers      = 8
	defaultPolicyGateWorkers    = 8
	defaultPipelineWorkers      = 4
)
