// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"fmt"
	"io"

	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
)

// cliEnv is how a command's output looks and where its warnings go: -o, the
// colour of the output stream, and stderr. A CLI command takes the
// package's flag variables (envOf with no env in ctx); a kardinal mcp tool
// call carries its own (JSON, plain, discarded) in its context, so calls
// and tests share no variable (#2230).
type cliEnv struct {
	output string
	sty    style.Styler
	errOut io.Writer
}

type cliEnvKey struct{}

// withEnv is ctx carrying e.
func withEnv(ctx context.Context, e cliEnv) context.Context {
	return context.WithValue(ctx, cliEnvKey{}, e)
}

// envOf is ctx's env, else the CLI's: -o, sty and errOut.
func envOf(ctx context.Context) cliEnv {
	if e, ok := ctx.Value(cliEnvKey{}).(cliEnv); ok {
		return e
	}
	return cliEnv{output: OutputFormat(), sty: sty, errOut: errOut}
}

// structured reports whether the env asks for json or yaml.
func (e cliEnv) structured() bool { return e.output == "json" || e.output == "yaml" }

// writeStructured writes v as the env asks: json or yaml.
func (e cliEnv) writeStructured(w io.Writer, v any) error {
	switch e.output {
	case "json":
		return WriteJSON(w, v)
	case "yaml":
		return WriteYAML(w, v)
	}
	return fmt.Errorf("-o %s is not json or yaml", e.output)
}

// mcpEnv is a kardinal mcp tool call's env: JSON, no colour, warnings
// discarded (stdout carries the protocol).
func mcpEnv() cliEnv { return cliEnv{output: "json", sty: style.Plain(), errOut: io.Discard} }

type identityKey struct{}

// withIdentity is ctx whose writes are made as id (a kardinal mcp server's
// test identity), instead of asking the API server.
func withIdentity(ctx context.Context, id func(context.Context, sigs_client.Client) (Identity, error)) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// callerIdentity is who the caller is: ctx's identity, else identityOf.
func callerIdentity(ctx context.Context, c sigs_client.Client) (Identity, error) {
	if id, ok := ctx.Value(identityKey{}).(func(context.Context, sigs_client.Client) (Identity, error)); ok {
		return id(ctx, c)
	}
	return identityOf(ctx, c)
}
