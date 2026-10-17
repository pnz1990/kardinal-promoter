// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"flag"
	"fmt"
	"slices"
	"strings"
)

// removedFlagNames are the flags #1283 removed with the Graph health ref
// nodes and their reader RoleBindings.
var removedFlagNames = []string{"graph-reader-namespaces", "graph-reader-clusterrole"}

// removedReaderFlags registers the removed flags on fs, so a controller
// started with one fails with a message saying so instead of Go's "flag
// provided but not defined". It returns the check to run after fs.Parse.
func removedReaderFlags(fs *flag.FlagSet) func() error {
	for _, n := range removedFlagNames {
		fs.String(n, "", "Removed (#1283): Graphs have no health ref nodes, so kardinal binds no reader "+
			"RoleBindings. Setting it fails; remove it.")
	}
	return func() error {
		var set []string
		fs.Visit(func(f *flag.Flag) {
			if slices.Contains(removedFlagNames, f.Name) {
				set = append(set, "--"+f.Name)
			}
		})
		if len(set) == 0 {
			return nil
		}
		return fmt.Errorf("%s: removed (#1283): Graphs have no health ref nodes any more, so kardinal binds "+
			"no reader RoleBindings; remove the flag (Helm: graph.readerNamespaces). Health is still checked by "+
			"the PromotionStep reconciler", strings.Join(set, ", "))
	}
}
