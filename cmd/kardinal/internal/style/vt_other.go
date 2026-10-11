// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build !windows

package style

import "io"

// enableColor reports whether w can show ANSI colour: every terminal outside
// Windows can.
func enableColor(io.Writer) bool { return true }
