// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build !race

package enterprise

// raceEnabled: the race detector slows admission tenfold, so latency
// bounds are not checked under it.
const raceEnabled = false
