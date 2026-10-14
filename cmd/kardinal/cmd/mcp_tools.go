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
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/types"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
)

// mcpTool is one tool of kardinal mcp: its name, whether it writes, and
// how it is added to a server.
type mcpTool struct {
	name  string
	write bool
	add   func(s *mcp.Server, k *mcpServer)
}

// newMCPTool is a tool whose arguments are In (the input schema is In's)
// and whose handler h returns the tool's result, or an error that becomes
// a tool error (toolError).
func newMCPTool[In any](name, title, description string, write, destructive bool,
	h func(ctx context.Context, k *mcpServer, in In) (*mcp.CallToolResult, error)) mcpTool {
	ann := &mcp.ToolAnnotations{Title: title, ReadOnlyHint: !write, IdempotentHint: !write}
	if write {
		ann.DestructiveHint = &destructive
	}
	openWorld := false
	ann.OpenWorldHint = &openWorld
	return mcpTool{name: name, write: write, add: func(s *mcp.Server, k *mcpServer) {
		mcp.AddTool(s, &mcp.Tool{Name: name, Title: title, Description: description, Annotations: ann},
			func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
				res, err := h(ctx, k, in)
				if err != nil {
					return toolError(err), nil, nil
				}
				return res, nil, nil
			})
	}}
}

// addMCPTool adds t to s.
func addMCPTool(s *mcp.Server, k *mcpServer, t mcpTool) { t.add(s, k) }

// mcpWriteToolNames are the names of the write tools.
func mcpWriteToolNames() []string {
	var names []string
	for _, t := range mcpTools() {
		if t.write {
			names = append(names, t.name)
		}
	}
	return names
}

// The arguments the tools share.
type (
	mcpNamespace struct {
		Namespace string `json:"namespace,omitempty" jsonschema:"Kubernetes namespace (default: the kubeconfig context's)"`
	}
	mcpPipelineEnv struct {
		mcpNamespace
		Pipeline    string `json:"pipeline" jsonschema:"Pipeline name"`
		Environment string `json:"environment,omitempty" jsonschema:"Only this environment"`
	}
	mcpPipelineTarget struct {
		mcpNamespace
		Pipeline    string `json:"pipeline" jsonschema:"Pipeline name"`
		Environment string `json:"environment" jsonschema:"Environment name"`
	}
	mcpPipelineOnly struct {
		mcpNamespace
		Pipeline string `json:"pipeline" jsonschema:"Pipeline name"`
	}
)

// nsArgs are the arguments that set the namespace, when one is given.
func nsArgs(ns string) []string {
	if ns == "" {
		return nil
	}
	return []string{"--namespace", ns}
}

// envArg appends --env env when env is set.
func envArg(args []string, env string) []string {
	if env == "" {
		return args
	}
	return append(args, "--env", env)
}

// cli runs kardinal args and returns its JSON output as the result.
func (k *mcpServer) cli(ctx context.Context, offset int, args ...string) (*mcp.CallToolResult, error) {
	out, err := k.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return k.result(json.RawMessage(out), offset)
}

// cliText runs kardinal args and returns what it printed, as a string.
func (k *mcpServer) cliText(ctx context.Context, args ...string) (*mcp.CallToolResult, error) {
	out, err := k.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return k.result(string(out), 0)
}

// direct runs f with a client for namespace ns (the server's namespace when
// empty), under the lock the commands share.
func (k *mcpServer) direct(ns string, f func(c sigs_client.Client, ns string) (any, error)) (*mcp.CallToolResult, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	defer keepGlobals()()
	// The flags as the server got them, then the tool's namespace.
	root := NewRootCmd()
	if err := root.ParseFlags(k.opts.base); err != nil {
		return nil, fmt.Errorf("flags: %w", err)
	}
	if ns != "" {
		globalNamespace = ns
	}
	c, cns, err := buildClient()
	if err != nil {
		return nil, err
	}
	v, err := f(c, cns)
	if err != nil {
		return nil, err
	}
	return k.result(v, 0)
}

// mcpTools are kardinal mcp's tools, read tools first.
func mcpTools() []mcpTool {
	return []mcpTool{
		newMCPTool("why", "Why an environment waits",
			"Say, for an environment of a pipeline (or each one), in a sentence what it waits for, then the version, "+
				"the gate or PR, and the kardinal command that moves it on.",
			false, false, mcpWhy),
		newMCPTool("status", "Pipeline status",
			"The state of a pipeline's environments (which version runs where, what is promoting, what blocks), "+
				"or without a pipeline a summary of the controller and every pipeline.",
			false, false, mcpStatus),
		newMCPTool("inbox", "What waits for you",
			"The approvals, holds and failures that wait for the identity kardinal mcp runs as, across pipelines.",
			false, false, mcpInbox),
		newMCPTool("history", "Promotion history",
			"A pipeline's promotions and rollbacks, newest first, with their state, PR and duration.",
			false, false, mcpHistory),
		newMCPTool("diff", "Compare two versions",
			"Compare the images and provenance of two versions (Bundles), by version or Bundle name.",
			false, false, mcpDiff),
		newMCPTool("explain", "Steps and gates of a version",
			"Every promotion step and policy gate of the current version of a pipeline, with each gate's CEL "+
				"expression and result.",
			false, false, mcpExplain),
		newMCPTool("gates", "List policy gates",
			"The PolicyGate templates, or those attached to a pipeline: name, environment, expression and the "+
				"controller's syntax check.",
			false, false, mcpGates),
		newMCPTool("simulate", "Would a version be allowed in",
			"Pre-flight: evaluate the gates of an environment as the controller would, writing nothing. Says "+
				"whether a version (or, without one, any version that went through every upstream environment) "+
				"would be allowed in at a time (default now), and for each blocking gate what it waits for and "+
				"the next hour it would pass with the same inputs.",
			false, false, mcpSimulate),

		newMCPTool("ship", "Ship a version",
			"Create a Bundle (a version) of a pipeline from images: it promotes through the environments under "+
				"their gates. Returns the Bundle; follow it with status or why.",
			true, false, mcpShip),
		newMCPTool("promote", "Promote into an environment",
			"Promote the newest version Verified upstream into an environment, as a new Bundle; gates and "+
				"approvals apply.",
			true, false, mcpPromote),
		newMCPTool("rollback", "Roll back an environment",
			"Roll an environment back to the version Verified there before (or a named one), through a PR; "+
				"with hold, keep it there until released.",
			true, true, mcpRollback),
		newMCPTool("pause", "Pause a pipeline",
			"Pause a pipeline: nothing new promotes until it is resumed.",
			true, false, mcpPause),
		newMCPTool("resume", "Resume a pipeline",
			"Resume a paused pipeline.",
			true, false, mcpResume),
		newMCPTool("reject", "Reject a version",
			"Reject a version for good: it never promotes again and rollback never picks it. Needs a reason.",
			true, true, mcpReject),
		newMCPTool("approve", "Approve a version",
			"Record an approval (or a rejection) of the version waiting for an environment's approval gates, as "+
				"the identity kardinal mcp runs as. The version is required: nothing is recorded when another "+
				"version is waiting. The gate decides whether this identity's approval counts.",
			true, false, mcpApprove),
	}
}

// ─── read tools ─────────────────────────────────────────────────────────────

// mcpWhyAnswer is one of why's answers: the sentence and its details. Text
// kardinal did not write is labelled (whyAnswer.quoted).
type mcpWhyAnswer struct {
	Sentence any            `json:"sentence"`
	Details  []mcpWhyDetail `json:"details,omitempty"`
}

// mcpWhyDetail is one detail line of a why answer.
type mcpWhyDetail struct {
	Label string `json:"label"`
	Value any    `json:"value"`
}

func mcpWhy(_ context.Context, k *mcpServer, in mcpPipelineEnv) (*mcp.CallToolResult, error) {
	return k.direct(in.Namespace, func(c sigs_client.Client, ns string) (any, error) {
		answers, err := whyAnswers(context.Background(), c, ns, in.Pipeline, in.Environment)
		if err != nil {
			return nil, err
		}
		out := make([]mcpWhyAnswer, 0, len(answers))
		for _, a := range answers {
			wa := mcpWhyAnswer{Sentence: a.sentence}
			if a.quotedSentence {
				wa.Sentence = untrusted(a.sentence)
			}
			for _, l := range a.lines {
				d := mcpWhyDetail{Label: l.label, Value: l.value}
				if slices.Contains(a.quoted, l.label) {
					d.Value = untrusted(l.value)
				}
				wa.Details = append(wa.Details, d)
			}
			out = append(out, wa)
		}
		return out, nil
	})
}

type mcpStatusIn struct {
	mcpNamespace
	Pipeline      string `json:"pipeline,omitempty" jsonschema:"Pipeline name; without one, a summary of every pipeline"`
	AllNamespaces bool   `json:"allNamespaces,omitempty" jsonschema:"Without a pipeline: every namespace"`
}

func mcpStatus(ctx context.Context, k *mcpServer, in mcpStatusIn) (*mcp.CallToolResult, error) {
	args := append([]string{"status"}, nsArgs(in.Namespace)...)
	if in.Pipeline != "" {
		args = append(args, in.Pipeline)
	} else if in.AllNamespaces {
		args = append(args, "--all-namespaces")
	}
	return k.cli(ctx, 0, append(args, "-o", "json")...)
}

type mcpInboxIn struct {
	mcpNamespace
	AllNamespaces bool `json:"allNamespaces,omitempty" jsonschema:"Every namespace the identity may read"`
	Offset        int  `json:"offset,omitempty" jsonschema:"The first item to return, from a previous page's nextOffset"`
}

func mcpInbox(ctx context.Context, k *mcpServer, in mcpInboxIn) (*mcp.CallToolResult, error) {
	args := append([]string{"inbox"}, nsArgs(in.Namespace)...)
	if in.AllNamespaces {
		args = append(args, "--all-namespaces")
	}
	return k.cli(ctx, in.Offset, append(args, "-o", "json")...)
}

type mcpHistoryIn struct {
	mcpPipelineEnv
	Limit  int `json:"limit,omitempty" jsonschema:"At most this many entries, newest first (default 20)"`
	Offset int `json:"offset,omitempty" jsonschema:"The first entry to return, from a previous page's nextOffset"`
}

func mcpHistory(ctx context.Context, k *mcpServer, in mcpHistoryIn) (*mcp.CallToolResult, error) {
	args := append([]string{"history", in.Pipeline}, nsArgs(in.Namespace)...)
	args = envArg(args, in.Environment)
	if in.Limit > 0 {
		args = append(args, "--limit", strconv.Itoa(in.Limit))
	}
	return k.cli(ctx, in.Offset, append(args, "-o", "json")...)
}

type mcpDiffIn struct {
	mcpNamespace
	A string `json:"a" jsonschema:"The first version, or Bundle name"`
	B string `json:"b" jsonschema:"The second version, or Bundle name"`
}

// mcpDiffSide is one version of a diff: its Bundle, images and provenance.
type mcpDiffSide struct {
	Bundle     string                     `json:"bundle"`
	Pipeline   string                     `json:"pipeline,omitempty"`
	Version    string                     `json:"version,omitempty"`
	Images     []v1alpha1.ImageRef        `json:"images,omitempty"`
	Config     *v1alpha1.ConfigRef        `json:"configRef,omitempty"`
	Provenance *v1alpha1.BundleProvenance `json:"provenance,omitempty"`
	Phase      string                     `json:"phase,omitempty"`
}

func mcpDiff(_ context.Context, k *mcpServer, in mcpDiffIn) (*mcp.CallToolResult, error) {
	return k.direct(in.Namespace, func(c sigs_client.Client, ns string) (any, error) {
		ctx := context.Background()
		side := func(ref string) (mcpDiffSide, error) {
			name, err := resolveBundleRef(ctx, c, ns, "", ref)
			if err != nil {
				return mcpDiffSide{}, fmt.Errorf("diff: %w", err)
			}
			var b v1alpha1.Bundle
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &b); err != nil {
				return mcpDiffSide{}, fmt.Errorf("get bundle %q: %w", name, err)
			}
			return mcpDiffSide{Bundle: b.Name, Pipeline: b.Spec.Pipeline, Version: createdOf(&b).Version,
				Images: b.Spec.Images, Config: b.Spec.ConfigRef, Provenance: b.Spec.Provenance, Phase: b.Status.Phase}, nil
		}
		a, err := side(in.A)
		if err != nil {
			return nil, err
		}
		b, err := side(in.B)
		if err != nil {
			return nil, err
		}
		var changed []string
		for _, p := range pairImages(a.Images, b.Images) {
			if imageTagCell(p.a) != imageTagCell(p.b) || imageDigest(p.a) != imageDigest(p.b) {
				changed = append(changed, p.repo)
			}
		}
		pa, pb := a.Provenance, b.Provenance
		if pa == nil {
			pa = &v1alpha1.BundleProvenance{}
		}
		if pb == nil {
			pb = &v1alpha1.BundleProvenance{}
		}
		if pa.CommitSHA != pb.CommitSHA {
			changed = append(changed, "provenance.commitSHA")
		}
		if pa.Author != pb.Author {
			changed = append(changed, "provenance.author")
		}
		return map[string]any{"a": a, "b": b, "changed": changed}, nil
	})
}

func mcpExplain(ctx context.Context, k *mcpServer, in mcpPipelineEnv) (*mcp.CallToolResult, error) {
	args := append([]string{"explain", in.Pipeline}, nsArgs(in.Namespace)...)
	return k.cli(ctx, 0, append(envArg(args, in.Environment), "-o", "json")...)
}

type mcpGatesIn struct {
	mcpNamespace
	Pipeline string `json:"pipeline,omitempty" jsonschema:"Only the gates attached to this pipeline"`
}

func mcpGates(ctx context.Context, k *mcpServer, in mcpGatesIn) (*mcp.CallToolResult, error) {
	args := append([]string{"policy", "list"}, nsArgs(in.Namespace)...)
	if in.Pipeline != "" {
		args = append(args, in.Pipeline)
	}
	return k.cliText(ctx, args...)
}

type mcpSimulateIn struct {
	mcpPipelineTarget
	Version          string   `json:"version,omitempty" jsonschema:"A version (image tag, chart version, config commit) or Bundle name of the pipeline; default: any version that went through every upstream environment"`
	Time             string   `json:"time,omitempty" jsonschema:"UTC time to evaluate at: 'Saturday 3pm', 'Tuesday 10:00' or RFC 3339; default now"`
	SoakMinutes      *int64   `json:"soakMinutes,omitempty" jsonschema:"Soak time upstream, in minutes; with a version, instead of its own"`
	PolicyNamespaces []string `json:"policyNamespaces,omitempty" jsonschema:"The controller's --policy-namespaces (default: the chart's)"`
}

// mcpSimulateResult is simulate's result: the report, and for each blocked
// gate what would let the version in.
type mcpSimulateResult struct {
	*simulateReport
	Allowed bool     `json:"allowed"`
	ToAllow []string `json:"toAllow,omitempty"`
}

func mcpSimulate(_ context.Context, k *mcpServer, in mcpSimulateIn) (*mcp.CallToolResult, error) {
	return k.direct(in.Namespace, func(c sigs_client.Client, ns string) (any, error) {
		opts := simulateOptions{Pipeline: in.Pipeline, Env: in.Environment, Version: in.Version, Time: in.Time,
			PolicyNamespaces: in.PolicyNamespaces, Now: time.Now().UTC()}
		if len(opts.PolicyNamespaces) == 0 {
			opts.PolicyNamespaces = slices.Clone(defaultPolicyNamespaces)
		}
		if in.SoakMinutes != nil {
			opts.SoakMinutes, opts.Soaked = *in.SoakMinutes, true
		}
		rep, err := policySimulate(context.Background(), c, ns, opts)
		if err != nil {
			return nil, err
		}
		out := mcpSimulateResult{simulateReport: rep, Allowed: !rep.Blocked}
		for _, g := range rep.Gates {
			if g.Pass {
				continue
			}
			if g.NextWindow != nil {
				out.ToAllow = append(out.ToAllow, fmt.Sprintf("gate %s passes by itself at %s with the same inputs",
					g.Name, g.NextWindow.Format(time.RFC3339)))
				continue
			}
			out.ToAllow = append(out.ToAllow, fmt.Sprintf("gate %s does not pass by itself within a week: "+
				"meet its rule (see its message and reason, or the why tool), or a person overrides it "+
				"(kardinal override %s %s --gate %s --reason ...)", g.Name, rep.Pipeline, rep.Environment, g.Name))
		}
		return out, nil
	})
}

// ─── write tools ────────────────────────────────────────────────────────────

type mcpShipIn struct {
	mcpPipelineOnly
	Images       []string `json:"images,omitempty" jsonschema:"Container image references; required for image and mixed Bundles"`
	Type         string   `json:"type,omitempty" jsonschema:"image (default), config or mixed"`
	ConfigCommit string   `json:"configCommit,omitempty" jsonschema:"Commit SHA of the config repository; required for config and mixed Bundles"`
	Commit       string   `json:"commit,omitempty" jsonschema:"Source commit SHA that produced the version (provenance)"`
	Author       string   `json:"author,omitempty" jsonschema:"Author or CI actor of the build (provenance)"`
	CIRunURL     string   `json:"ciRunURL,omitempty" jsonschema:"URL of the CI run that built it (provenance)"`
}

func mcpShip(ctx context.Context, k *mcpServer, in mcpShipIn) (*mcp.CallToolResult, error) {
	args := append([]string{"ship", in.Pipeline}, nsArgs(in.Namespace)...)
	for _, img := range in.Images {
		args = append(args, "--image", img)
	}
	for flag, v := range map[string]string{"--type": in.Type, "--config-commit": in.ConfigCommit,
		"--commit": in.Commit, "--author": in.Author, "--ci-run-url": in.CIRunURL} {
		if v != "" {
			args = append(args, flag, v)
		}
	}
	return k.cli(ctx, 0, append(args, "-o", "json")...)
}

func mcpPromote(ctx context.Context, k *mcpServer, in mcpPipelineTarget) (*mcp.CallToolResult, error) {
	args := append([]string{"promote", in.Pipeline, in.Environment}, nsArgs(in.Namespace)...)
	return k.cli(ctx, 0, append(args, "-o", "json")...)
}

type mcpRollbackIn struct {
	mcpPipelineTarget
	To            string `json:"to,omitempty" jsonschema:"The version or Bundle to roll back to (default: the one Verified there before)"`
	Hold          bool   `json:"hold,omitempty" jsonschema:"Keep the environment on the rollback until it is released (needs reason)"`
	Reason        string `json:"reason,omitempty" jsonschema:"Why the environment is held (required with hold)"`
	HoldExpiresIn string `json:"holdExpiresIn,omitempty" jsonschema:"With hold: end it this long after (30m, 24h, 2d)"`
}

func mcpRollback(ctx context.Context, k *mcpServer, in mcpRollbackIn) (*mcp.CallToolResult, error) {
	args := append([]string{"rollback", in.Pipeline, in.Environment, "--yes"}, nsArgs(in.Namespace)...)
	if in.To != "" {
		args = append(args, "--to", in.To)
	}
	if in.Hold {
		args = append(args, "--hold")
	}
	if in.Reason != "" {
		args = append(args, "--reason", in.Reason)
	}
	if in.HoldExpiresIn != "" {
		args = append(args, "--hold-expires-in", in.HoldExpiresIn)
	}
	return k.cli(ctx, 0, append(args, "-o", "json")...)
}

func mcpPause(ctx context.Context, k *mcpServer, in mcpPipelineOnly) (*mcp.CallToolResult, error) {
	return k.cliText(ctx, append([]string{"pause", in.Pipeline}, nsArgs(in.Namespace)...)...)
}

func mcpResume(ctx context.Context, k *mcpServer, in mcpPipelineOnly) (*mcp.CallToolResult, error) {
	return k.cliText(ctx, append([]string{"resume", in.Pipeline}, nsArgs(in.Namespace)...)...)
}

type mcpRejectIn struct {
	mcpNamespace
	Version  string `json:"version" jsonschema:"The version, or Bundle name, to reject"`
	Reason   string `json:"reason" jsonschema:"Why it is rejected"`
	Pipeline string `json:"pipeline,omitempty" jsonschema:"Look for the version in this pipeline only"`
}

func mcpReject(ctx context.Context, k *mcpServer, in mcpRejectIn) (*mcp.CallToolResult, error) {
	args := append([]string{"reject", in.Version, "--reason", in.Reason, "--yes"}, nsArgs(in.Namespace)...)
	if in.Pipeline != "" {
		args = append(args, "--pipeline", in.Pipeline)
	}
	return k.cliText(ctx, args...)
}

type mcpApproveIn struct {
	mcpPipelineTarget
	Version  string `json:"version" jsonschema:"The version waiting for the environment (image tag, config commit or chart version); nothing is recorded when another one waits"`
	Decision string `json:"decision,omitempty" jsonschema:"approve (default) or reject"`
	Comment  string `json:"comment,omitempty" jsonschema:"Note shown with the decision"`
}

func mcpApprove(ctx context.Context, k *mcpServer, in mcpApproveIn) (*mcp.CallToolResult, error) {
	if in.Version == "" {
		return nil, fmt.Errorf("approve: version is required: name the version you reviewed")
	}
	args := append([]string{"approve", in.Pipeline, in.Environment, "--version", in.Version, "--yes"},
		nsArgs(in.Namespace)...)
	if in.Decision != "" {
		args = append(args, "--decision", in.Decision)
	}
	if in.Comment != "" {
		args = append(args, "--comment", in.Comment)
	}
	return k.cliText(ctx, args...)
}
