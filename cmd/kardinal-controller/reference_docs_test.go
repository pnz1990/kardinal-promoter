// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kardinal-promoter/kardinal-promoter/hack/gen-reference-docs/refdocs"
	"github.com/kardinal-promoter/kardinal-promoter/test/kubetest"
)

// printHelpEnv makes this test binary run the controller's main with -help
// instead of the tests (TestControllerFlagsPageIsUpToDate).
const printHelpEnv = "KARDINAL_TEST_PRINT_CONTROLLER_HELP"

func TestMain(m *testing.M) {
	// No test reaches the ambient kubeconfig or the in-cluster config
	// (test/kubetest; make hygiene checks it is here).
	kubetest.Isolate()
	if os.Getenv(printHelpEnv) == "1" {
		printControllerHelp()
	}
	os.Exit(m.Run())
}

// printControllerHelp runs main with -help, as `kardinal-controller -help`
// does, and exits. The testing package registered its -test.* flags on
// flag.CommandLine, so main gets a flag set with every other flag registered
// so far (the init-time ones such as controller-runtime's -kubeconfig) and
// registers its own on it.
func printControllerHelp() {
	fs := flag.NewFlagSet("kardinal-controller", flag.ExitOnError)
	flag.CommandLine.VisitAll(func(f *flag.Flag) {
		if !strings.HasPrefix(f.Name, "test.") {
			fs.Var(f.Value, f.Name, f.Usage)
		}
	})
	flag.CommandLine = fs
	os.Args = []string{"kardinal-controller", "-help"}
	main() // -help: flag.Parse prints the usage and exits 0
	os.Exit(1)
}

// TestControllerFlagsPageIsUpToDate fails when
// docs/reference/controller-flags.md does not match the controller's flags;
// run: make reference-docs. It runs this test binary, which is the controller
// package, with -help, so a flag change in any package the controller imports
// is caught without building the controller again. The environment is empty:
// a flag's default can come from a variable (KARDINAL_*, GITHUB_APP_*, ...),
// and the page shows the built-in ones.
func TestControllerFlagsPageIsUpToDate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = []string{printHelpEnv + "=1"}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "controller -help: %s", out)
	require.Contains(t, string(out), "Usage of kardinal-controller:")

	got, err := os.ReadFile("../../docs/reference/controller-flags.md")
	require.NoError(t, err)
	assert.Equal(t, refdocs.ControllerFlagsPage(string(out)), string(got),
		"docs/reference/controller-flags.md is stale; run: make reference-docs")
}
