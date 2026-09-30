// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package kubeevent records Kubernetes Events through the events.k8s.io/v1 API.
//
// The API server validates new-API Events more strictly than core/v1 Events:
// action is required, and a note longer than NoteLimit bytes is rejected, so the
// whole Event is lost (k8s.io/kubernetes pkg/apis/core/validation/events.go).
// Emit keeps every Event the reconcilers record inside those limits.
package kubeevent

import (
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
)

// NoteLimit is the longest note, in bytes, the API server accepts on an
// events.k8s.io/v1 Event.
const NoteLimit = 1024

// truncatedSuffix marks a note that Emit shortened.
const truncatedSuffix = "..."

// Emit records an Event on obj. It does nothing when rec is nil, so reconcilers
// built without a recorder (most unit tests) need no guard.
//
// action says what the controller did or failed to do; the API requires it.
// The note is shortened to NoteLimit bytes at a UTF-8 boundary, and it is passed
// as a format argument, so a '%' in it (a URL, a CEL expression) is kept as is.
func Emit(rec events.EventRecorder, obj runtime.Object, eventType, reason, action, note string) {
	if rec == nil {
		return
	}
	rec.Eventf(obj, nil, eventType, reason, action, "%s", Truncate(note))
}

// Truncate returns note unchanged when it fits in NoteLimit bytes. Otherwise it
// cuts note at a rune boundary and appends "...", keeping the result within
// NoteLimit bytes.
func Truncate(note string) string {
	if len(note) <= NoteLimit {
		return note
	}
	cut := NoteLimit - len(truncatedSuffix)
	for cut > 0 && !utf8.RuneStart(note[cut]) {
		cut--
	}
	return note[:cut] + truncatedSuffix
}
