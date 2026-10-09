// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build !race

package tmplsafe_test

// raceSlowdown scales time bounds: the race detector slows code 5-10x.
const raceSlowdown = 1
