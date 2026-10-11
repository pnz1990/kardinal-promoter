// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
)

// completionTimeout bounds the cluster reads of one Tab press: a slow or
// unreachable cluster completes nothing rather than hang the shell.
const completionTimeout = 3 * time.Second

// argKind is what a positional argument or flag names, for completion.
type argKind int

const (
	argNone argKind = iota
	argPipeline
	argEnv
	argBundle
	argVersion
	argGate
)

// argKindOf is what the token of a command's Use names: <pipeline>,
// [<pipeline>], [pipeline] and name (get pipelines) a Pipeline; <env> and
// [<env>] one of its environments; <bundle>, <bundle-a>, <bundle-b> and
// <name> (delete bundle) a Bundle.
func argKindOf(cmdName, token string) argKind {
	t := strings.Trim(token, "[]<>")
	switch {
	case t == "pipeline", t == "name" && cmdName == "pipelines":
		return argPipeline
	case t == "env":
		return argEnv
	case strings.HasPrefix(t, "bundle"), t == "name" && cmdName == "bundle":
		return argBundle
	}
	return argNone
}

// flagKinds are the flags that name something completion can list.
var flagKinds = map[string]argKind{
	"pipeline": argPipeline,
	"env":      argEnv,
	"stage":    argEnv,
	"bundle":   argBundle,
	"to":       argBundle,
	"version":  argVersion,
	"gate":     argGate,
}

// addNameCompletion gives every command under root completion of the
// names its arguments and flags take, read from the cluster: Pipelines,
// their environments, Bundles (by name, with their version and phase as the
// description), version tags and gates. A command that already completes
// its arguments keeps its own.
func addNameCompletion(root *cobra.Command) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if c == root || !c.Runnable() {
			return
		}
		var kinds []argKind
		for _, tok := range strings.Fields(c.Use)[1:] {
			if strings.HasPrefix(tok, "--") || strings.HasPrefix(tok, "[--") {
				break
			}
			kinds = append(kinds, argKindOf(c.Name(), tok))
		}
		if c.ValidArgsFunction == nil && slices.ContainsFunc(kinds, func(k argKind) bool { return k != argNone }) {
			c.ValidArgsFunction = func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
				if len(args) >= len(kinds) {
					return nil, cobra.ShellCompDirectiveNoFileComp
				}
				return completeNames(cmd, kinds[len(args)], args), cobra.ShellCompDirectiveNoFileComp
			}
		}
		for name, kind := range flagKinds {
			if c.Flags().Lookup(name) == nil {
				continue
			}
			k := kind
			_ = c.RegisterFlagCompletionFunc(name, func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
				return completeNames(cmd, k, args), cobra.ShellCompDirectiveNoFileComp
			})
		}
	}
	walk(root)
}

// completionPipeline is the Pipeline the command line names so far: its
// --pipeline flag, else its first argument when that is a Pipeline.
func completionPipeline(cmd *cobra.Command, args []string) string {
	if f := cmd.Flags().Lookup("pipeline"); f != nil && f.Value.String() != "" {
		return f.Value.String()
	}
	toks := strings.Fields(cmd.Use)
	if len(args) > 0 && len(toks) > 1 && argKindOf(cmd.Name(), toks[1]) == argPipeline {
		return args[0]
	}
	return ""
}

// completionEnv is the environment the command line names so far: --env
// (or --stage), else the argument after the Pipeline.
func completionEnv(cmd *cobra.Command, args []string) string {
	for _, name := range []string{"env", "stage"} {
		if f := cmd.Flags().Lookup(name); f != nil && f.Value.String() != "" {
			return f.Value.String()
		}
	}
	toks := strings.Fields(cmd.Use)
	if len(args) > 1 && len(toks) > 2 && argKindOf(cmd.Name(), toks[2]) == argEnv {
		return args[1]
	}
	return ""
}

// completeNames lists the names of kind from the cluster, for cmd with args
// so far. Any error (no cluster, no rights, a timeout) completes nothing.
func completeNames(cmd *cobra.Command, kind argKind, args []string) []string {
	if kind == argNone {
		return nil
	}
	c, ns, err := buildClient()
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), completionTimeout)
	defer cancel()
	pipeline := completionPipeline(cmd, args)
	switch kind {
	case argPipeline:
		var list v1alpha1.PipelineList
		if err := c.List(ctx, &list, sigs_client.InNamespace(ns)); err != nil {
			return nil
		}
		out := make([]string, 0, len(list.Items))
		for _, p := range list.Items {
			out = append(out, p.Name+"\t"+strings.Join(pipelineEnvNames(&p), ", "))
		}
		return out
	case argEnv:
		if pipeline == "" {
			return nil
		}
		p, err := getPipeline(ctx, c, ns, pipeline)
		if err != nil {
			return nil
		}
		return pipelineEnvNames(p)
	case argBundle, argVersion:
		var list v1alpha1.BundleList
		if err := c.List(ctx, &list, sigs_client.InNamespace(ns)); err != nil {
			return nil
		}
		bundles := list.Items
		slices.SortFunc(bundles, func(a, b v1alpha1.Bundle) int { return lifecycle.CompareCreation(&b, &a) })
		var out []string
		seen := map[string]bool{}
		for i := range bundles {
			b := &bundles[i]
			if pipeline != "" && b.Spec.Pipeline != pipeline {
				continue
			}
			version := createdOf(b).Version
			if kind == argVersion {
				if version != "" && !seen[version] {
					seen[version] = true
					out = append(out, fmt.Sprintf("%s\t%s, %s", version, b.Name, b.Status.Phase))
				}
				continue
			}
			out = append(out, fmt.Sprintf("%s\t%s", b.Name, strings.TrimPrefix(strings.TrimSpace(version+", "+b.Status.Phase), ", ")))
		}
		return out
	case argGate:
		var gates v1alpha1.PolicyGateList
		opts := []sigs_client.ListOption{sigs_client.InNamespace(ns)}
		if pipeline != "" {
			opts = append(opts, sigs_client.MatchingLabels{"kardinal.io/pipeline": pipeline})
		}
		if err := c.List(ctx, &gates, opts...); err != nil {
			return nil
		}
		env := completionEnv(cmd, args)
		var out []string
		for _, g := range gates.Items {
			if g.Labels["kardinal.io/bundle"] == "" { // a template, not an instance
				continue
			}
			if env != "" && g.Labels["kardinal.io/environment"] != env {
				continue
			}
			if name := gateDisplayName(g); !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
		slices.Sort(out)
		return out
	}
	return nil
}
