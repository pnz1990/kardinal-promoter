// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build race

package sharedbranch_test

// raceWriters is how many writers the test runs under the race detector,
// which slows go-git several times over (see writers below).
const raceWriters = 30
