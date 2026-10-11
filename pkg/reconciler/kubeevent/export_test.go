// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package kubeevent

import (
	"time"

	"k8s.io/client-go/tools/events"
)

// LimitedAt is Limited with clock now, so a test decides how much the
// buckets refill.
func LimitedAt(rec events.EventRecorder, name string, normal, warning Limit, now func() time.Time) events.EventRecorder {
	l := Limited(rec, name, normal, warning)
	if lim, ok := l.(*limited); ok {
		lim.now = now
	}
	return l
}
