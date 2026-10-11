// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

//go:build windows

package style

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// enableColor turns on virtual-terminal processing for w, a Windows console,
// so it reads ANSI colour codes; false when it cannot, and colour stays off.
func enableColor(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
