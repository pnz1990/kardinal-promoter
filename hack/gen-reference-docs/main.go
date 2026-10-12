// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// gen-reference-docs writes two generated reference pages:
//
//   - docs/reference/helm-values.md from chart/kardinal-promoter/values.yaml:
//     every value, its default and its "# --" comment;
//   - docs/reference/controller-flags.md from the controller's -help output:
//     every flag, its type, default and help.
//
// With --check it writes nothing and fails when a page differs from what it
// would write, so CI keeps them in sync.
//
//	go run ./hack/gen-reference-docs
//	go run ./hack/gen-reference-docs --check
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/hack/gen-reference-docs/refdocs"
)

// helpTimeout bounds the controller build and -help run: a go command that
// waits on a lock or a module download fails with a clear error instead of
// hanging.
const helpTimeout = 10 * time.Minute

func main() {
	values := flag.String("values", "chart/kardinal-promoter/values.yaml", "the chart's values.yaml")
	helmOut := flag.String("helm-out", "docs/reference/helm-values.md", "the Helm values page")
	flagsOut := flag.String("flags-out", "docs/reference/controller-flags.md", "the controller flags page")
	check := flag.Bool("check", false, "fail when a page is out of date instead of writing it")
	flag.Parse()

	src, err := os.ReadFile(*values)
	if err != nil {
		log.Fatal(err)
	}
	helm, err := refdocs.HelmValuesPage(src)
	if err != nil {
		log.Fatalf("%s: %v", *values, err)
	}
	help, err := controllerHelp(".")
	if err != nil {
		log.Fatalf("controller -help: %v", err)
	}
	flags := refdocs.ControllerFlagsPage(help)

	stale := false
	for path, want := range map[string]string{*helmOut: helm, *flagsOut: flags} {
		if *check {
			got, _ := os.ReadFile(path)
			if string(got) != want {
				fmt.Fprintf(os.Stderr, "%s is stale: run go run ./hack/gen-reference-docs\n", path)
				stale = true
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			log.Fatal(err)
		}
		log.Printf("wrote %s", path)
	}
	if stale {
		os.Exit(1)
	}
}

// ---- Controller flags ------------------------------------------------------

// controllerHelp runs the controller in dir with -help, with only the
// variables go needs, and returns what it printed.
func controllerHelp(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), helpTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/kardinal-controller", "-help")
	cmd.Dir = dir
	// Only what go needs: a flag's default can come from any variable
	// (KARDINAL_*, GITHUB_APP_*, ...), and the page shows the built-in ones.
	env := []string{}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if name == "PATH" || name == "HOME" || name == "TMPDIR" || strings.HasPrefix(name, "GO") || name == "CGO_ENABLED" {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("go run ./cmd/kardinal-controller -help did not finish in %s: %s", helpTimeout, out.String())
		}
		return "", fmt.Errorf("go run ./cmd/kardinal-controller -help: %w: %s", err, out.String())
	}
	return out.String(), nil
}
