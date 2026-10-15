// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Command changelog keeps docs/changelog.md's unreleased entries as one file
// per change, docs/changelog.d/<issue or PR>-<slug>.md, so two pull requests
// never edit the same lines of the changelog:
//
//	---
//	section: Added
//	---
//
//	- **Title** (#123) — what changed, for a user.
//
// The docs build renders them under [Unreleased]
// (hack/docs-changelog/changelog_fragments.py); a release writes them into
// docs/changelog.md and deletes them.
//
//	changelog lint                          every fragment is well formed
//	changelog unreleased                    the [Unreleased] section, from the fragments
//	changelog release vX.Y.Z [YYYY-MM-DD]   write the fragments in as vX.Y.Z and delete them
//	changelog check BASE [LABELS]           a user-facing change since BASE adds a fragment
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/hack/changelog/fragments"
)

func main() {
	if err := run(".", os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "changelog:", err)
		os.Exit(1)
	}
}

const usage = "usage: changelog lint | unreleased | release vX.Y.Z [YYYY-MM-DD] | check BASE [LABELS]"

func run(root string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "lint":
		fs, err := fragments.Read(root)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%d fragments in %s\n", len(fs), fragments.Dir)
		return err
	case "unreleased":
		fs, err := fragments.Read(root)
		if err != nil {
			return err
		}
		_, err = io.WriteString(stdout, fragments.Render(fs))
		return err
	case "release":
		if len(args) < 2 || len(args) > 3 {
			return errors.New(usage)
		}
		date := time.Now().UTC().Format("2006-01-02")
		if len(args) == 3 {
			date = args[2]
		}
		fs, err := fragments.Read(root)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(root, fragments.Changelog))
		if err != nil {
			return fmt.Errorf("read changelog: %w", err)
		}
		out, err := fragments.Release(string(data), fs, args[1], date)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, fragments.Changelog), []byte(out), 0o644); err != nil {
			return fmt.Errorf("write changelog: %w", err)
		}
		for _, f := range fs {
			if err := os.Remove(filepath.Join(root, fragments.Dir, f.File)); err != nil {
				return fmt.Errorf("delete fragment: %w", err)
			}
		}
		_, err = fmt.Fprintf(stdout, "wrote %d fragments into %s as %s and deleted them\n", len(fs), fragments.Changelog, args[1])
		return err
	case "check":
		if len(args) < 2 || len(args) > 3 {
			return errors.New(usage)
		}
		cmd := exec.Command("git", "diff", "--name-only", args[1]+"...HEAD")
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("git diff %s...HEAD: %w", args[1], err)
		}
		var labels []string
		if len(args) == 3 {
			labels = strings.Split(args[2], ",")
		}
		if _, err := fragments.Read(root); err != nil {
			return err
		}
		if why := fragments.Check(strings.Fields(string(out)), labels); why != "" {
			return errors.New(why)
		}
		_, err = fmt.Fprintln(stdout, "changelog: ok")
		return err
	}
	return errors.New(usage)
}
