// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import "testing"

func TestForced(t *testing.T) {
	cases := []struct {
		spec    string
		attempt int
		want    string
	}{
		{"", 1, ""},
		{"inconclusive,pass", 1, "inconclusive"},
		{"inconclusive,pass", 2, "pass"},
		{"inconclusive,pass", 3, "pass"},
		{"fail", 0, ""},
	}
	for _, tc := range cases {
		if got := forced(tc.spec, tc.attempt); got != tc.want {
			t.Errorf("forced(%q, %d) = %q, want %q", tc.spec, tc.attempt, got, tc.want)
		}
	}
}
