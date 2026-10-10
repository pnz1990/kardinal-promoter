// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build !race

package sharedbranch_test

// raceWriters is 0 without the race detector: the test runs all its writers.
const raceWriters = 0
