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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/actor"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/lifecycle"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/changewindow"
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
			func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
				ctx = context.WithValue(ctx, mcpClientKey{}, mcpClientLabel(req))
				if v, ok := any(in).(interface{ check() error }); ok {
					if err := v.check(); err != nil {
						return toolError(fmt.Errorf("%s: %w", name, err)), nil, nil
					}
				}
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

// mcpClientKey is the context key of the label of the MCP client that
// called a tool (mcpClientLabel).
type mcpClientKey struct{}

// mcpMaxAsserted is the longest asserted field the actor stamp takes.
const mcpMaxAsserted = 128

// mcpClientLabel is the stamp's asserted client for a tool call:
// kardinal-mcp/<version>, and the name and version the client gave when it
// connected, as it gave them (asserted, never trusted), printable ASCII
// only and at most mcpMaxAsserted bytes.
func mcpClientLabel(req *mcp.CallToolRequest) string {
	label := "kardinal-mcp/" + CLIVersion
	if req != nil && req.Session != nil {
		if p := req.Session.InitializeParams(); p != nil && p.ClientInfo != nil {
			if c := strings.TrimSpace(p.ClientInfo.Name + "/" + p.ClientInfo.Version); c != "/" {
				label += " (" + strings.Trim(c, "/") + ")"
			}
		}
	}
	label = strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return -1
		}
		return r
	}, label)
	if len(label) > mcpMaxAsserted {
		label = label[:mcpMaxAsserted]
	}
	return label
}

// The patterns a tool's arguments must match before anything runs: the
// CRDs' own (an environment name; an image tag, which a version extends
// with "+" for chart versions), DNS-1123 for object names and namespaces.
// Nothing a tool is given is ever parsed as a command-line flag: the tools
// call the commands' functions with typed values.
var (
	mcpEnvPattern     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	mcpVersionPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]{0,252}$`)
	mcpImagePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:@-]{0,510}[A-Za-z0-9_]$|^[a-z0-9]$`)
)

// argErr is the error of argument field whose value v is not what it
// should be.
func argErr(field, v, want string) error {
	return fmt.Errorf("invalid %s %q: want %s", field, v, want)
}

// checkName checks an object name (DNS-1123 subdomain); "" passes unless
// required.
func checkName(field, v string, required bool) error {
	if v == "" {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	if len(validation.IsDNS1123Subdomain(v)) > 0 {
		return argErr(field, v, "a Kubernetes object name (lower case letters, digits, '-' and '.')")
	}
	return nil
}

// checkNamespace checks a namespace (DNS-1123 label); "" is the server's.
func checkNamespace(v string) error {
	if v != "" && len(validation.IsDNS1123Label(v)) > 0 {
		return argErr("namespace", v, "a namespace name (lower case letters, digits and '-')")
	}
	return nil
}

// checkEnv checks an environment name; "" passes unless required.
func checkEnv(v string, required bool) error {
	if v == "" {
		if required {
			return fmt.Errorf("environment is required")
		}
		return nil
	}
	if len(v) > 63 || !mcpEnvPattern.MatchString(v) {
		return argErr("environment", v, "an environment name (lower case letters, digits and '-')")
	}
	return nil
}

// checkVersion checks a version or Bundle name (an image tag, a chart
// version, a commit SHA or a Bundle's name); "" passes unless required.
func checkVersion(field, v string, required bool) error {
	if v == "" {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	if !mcpVersionPattern.MatchString(v) {
		return argErr(field, v, "a version (an image tag, chart version or commit) or a Bundle name")
	}
	return nil
}

// checkAll returns the first error of errs.
func checkAll(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// direct runs f, under the lock the commands share, as the commands run:
// with the flags the server was started with (its kubeconfig and context,
// which no tool can change), the tool's namespace when it gives one, JSON
// output and no colour. f writes the command's output to w, or returns a
// value; the result is the value, else what f wrote (JSON, or text).
func (k *mcpServer) direct(ctx context.Context, ns string, offset int,
	f func(ctx context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error)) (*mcp.CallToolResult, error) {
	// Nothing here writes a package variable: calls run at once (#2230).
	// The writes are stamped by the commands' own code (stampOf): the actor
	// is who the kubeconfig authenticates. What the client says about itself
	// goes in the stamp's asserted fields from ctx (mcpClientKey, assertedFor):
	// kardinal mcp and the agent that connected, unless KARDINAL_ACTOR_CLIENT
	// says otherwise.
	c, cns, err := k.opts.client()
	if err != nil {
		return nil, humanizedError{orig: err, human: HumanError(err)}
	}
	if ns != "" {
		cns = ns
	}
	var out bytes.Buffer
	v, err := f(ctx, &out, c, cns)
	if err != nil {
		// Rewritten with this call's client and namespace: the hints never
		// read the flags' namespace.
		return nil, humanizedError{orig: err, human: humanErrorIn(err, c, cns)}
	}
	if v == nil {
		if raw := bytes.TrimSpace(out.Bytes()); json.Valid(raw) && len(raw) > 0 {
			v = json.RawMessage(raw)
		} else {
			v = out.String()
		}
	}
	return k.result(v, offset)
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
		newMCPTool("verdicts", "Verdicts on a version",
			"The verdicts recorded on a version (the one that runs in an environment, or a named one): each "+
				"verifier, check, result, summary, evidence and findings, and whether a verify gate counted it.",
			false, false, mcpVerdicts),
		newMCPTool("freezes", "List freezes",
			"The freezes (agent freezes and stop-the-line): what each holds, where, whether it is active, until "+
				"when, who made it and why, and how many exemptions people granted.",
			false, false, mcpFreezes),

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
		newMCPTool("verify", "Record a verdict",
			"Record a verdict (pass, fail or inconclusive) for a check on the version that runs in an "+
				"environment, as the identity kardinal mcp runs as. The version is required: nothing is "+
				"recorded when another version runs there. A verify gate decides whether the verdict counts: "+
				"an agent's only where the gate lists it in verify.allowedAgents, never on its own party's work.",
			true, false, mcpVerify),
		newMCPTool("freeze", "Freeze agents, or stop the line",
			"Hold promotions until the freeze is lifted: holds agents (the versions agents created) or everyone "+
				"(stop-the-line), in every namespace or one. A person lifts it (kardinal unfreeze) or grants an "+
				"exemption (kardinal override --freeze); an agent can do neither here.",
			true, false, mcpFreeze),
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

func (in mcpPipelineEnv) check() error {
	return checkAll(checkNamespace(in.Namespace), checkName("pipeline", in.Pipeline, true), checkEnv(in.Environment, false))
}

func mcpWhy(ctx context.Context, k *mcpServer, in mcpPipelineEnv) (*mcp.CallToolResult, error) {
	return k.direct(ctx, in.Namespace, 0, func(ctx context.Context, _ io.Writer, c sigs_client.Client, ns string) (any, error) {
		answers, err := whyAnswers(ctx, c, ns, in.Pipeline, in.Environment)
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

func (in mcpStatusIn) check() error {
	return checkAll(checkNamespace(in.Namespace), checkName("pipeline", in.Pipeline, false))
}

func mcpStatus(ctx context.Context, k *mcpServer, in mcpStatusIn) (*mcp.CallToolResult, error) {
	return k.direct(ctx, in.Namespace, 0, func(_ context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		if in.Pipeline != "" {
			return nil, statusPipelineWriter(w, c, ns, in.Pipeline)
		}
		// As kardinal status: every namespace, unless one is named.
		scope, fallback := "", ns
		if in.Namespace != "" && !in.AllNamespaces {
			scope = ns
		}
		if in.AllNamespaces {
			fallback = ""
		}
		return nil, statusSummaryIn(w, c, defaultControllerNamespace, scope, fallback)
	})
}

type mcpInboxIn struct {
	mcpNamespace
	AllNamespaces bool `json:"allNamespaces,omitempty" jsonschema:"Every namespace the identity may read"`
}

func (in mcpInboxIn) check() error { return checkNamespace(in.Namespace) }

func mcpInbox(ctx context.Context, k *mcpServer, in mcpInboxIn) (*mcp.CallToolResult, error) {
	return k.direct(ctx, in.Namespace, 0, func(ctx context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		if in.AllNamespaces {
			ns = ""
		}
		return nil, inboxFn(ctx, w, c, ns)
	})
}

type mcpHistoryIn struct {
	mcpPipelineEnv
	Limit  int `json:"limit,omitempty" jsonschema:"At most this many entries, newest first (default 20)"`
	Offset int `json:"offset,omitempty" jsonschema:"The first entry to return, from a previous page's nextOffset"`
}

func (in mcpHistoryIn) check() error {
	if in.Limit < 0 || in.Offset < 0 {
		return fmt.Errorf("limit and offset must not be negative")
	}
	return in.mcpPipelineEnv.check()
}

func mcpHistory(ctx context.Context, k *mcpServer, in mcpHistoryIn) (*mcp.CallToolResult, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	return k.direct(ctx, in.Namespace, in.Offset, func(_ context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		return nil, historyFn(w, c, ns, in.Pipeline, in.Environment, limit)
	})
}

type mcpDiffIn struct {
	mcpNamespace
	A string `json:"a" jsonschema:"The first version, or Bundle name"`
	B string `json:"b" jsonschema:"The second version, or Bundle name"`
}

func (in mcpDiffIn) check() error {
	return checkAll(checkNamespace(in.Namespace), checkVersion("a", in.A, true), checkVersion("b", in.B, true))
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

func mcpDiff(ctx context.Context, k *mcpServer, in mcpDiffIn) (*mcp.CallToolResult, error) {
	return k.direct(ctx, in.Namespace, 0, func(ctx context.Context, _ io.Writer, c sigs_client.Client, ns string) (any, error) {
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
	return k.direct(ctx, in.Namespace, 0, func(_ context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		return nil, explainOnce(w, c, ns, in.Pipeline, in.Environment, false)
	})
}

type mcpGatesIn struct {
	mcpNamespace
	Pipeline string `json:"pipeline,omitempty" jsonschema:"Only the gates attached to this pipeline"`
}

func (in mcpGatesIn) check() error {
	return checkAll(checkNamespace(in.Namespace), checkName("pipeline", in.Pipeline, false))
}

func mcpGates(ctx context.Context, k *mcpServer, in mcpGatesIn) (*mcp.CallToolResult, error) {
	return k.direct(ctx, in.Namespace, 0, func(_ context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		return nil, policyListFn(w, c, ns, in.Pipeline, slices.Clone(defaultPolicyNamespaces))
	})
}

type mcpSimulateIn struct {
	mcpPipelineTarget
	Version          string   `json:"version,omitempty" jsonschema:"A version (image tag, chart version, config commit) or Bundle name of the pipeline; default: any version that went through every upstream environment"`
	Time             string   `json:"time,omitempty" jsonschema:"UTC time to evaluate at: 'Saturday 3pm', 'Tuesday 10:00' or RFC 3339; default now"`
	SoakMinutes      *int64   `json:"soakMinutes,omitempty" jsonschema:"Soak time upstream, in minutes; with a version, instead of its own"`
	PolicyNamespaces []string `json:"policyNamespaces,omitempty" jsonschema:"The controller's --policy-namespaces (default: platform-policies)"`
	AsAgent          string   `json:"asAgent,omitempty" jsonschema:"Simulate a version this agent (its AgentIdentity name) created: actor.*, agent.* and the environment's agents setting"`
}

func (in mcpSimulateIn) check() error {
	errs := []error{in.mcpPipelineTarget.check(), checkVersion("version", in.Version, false), checkName("asAgent", in.AsAgent, false)}
	for _, ns := range in.PolicyNamespaces {
		if ns == "" || len(validation.IsDNS1123Label(ns)) > 0 {
			errs = append(errs, argErr("policyNamespaces", ns, "namespace names"))
		}
	}
	return checkAll(errs...)
}

// mcpSimulateResult is simulate's result: the report, and for each blocked
// gate what would let the version in.
type mcpSimulateResult struct {
	*simulateReport
	Allowed bool     `json:"allowed"`
	ToAllow []string `json:"toAllow,omitempty"`
}

func mcpSimulate(ctx context.Context, k *mcpServer, in mcpSimulateIn) (*mcp.CallToolResult, error) {
	return k.direct(ctx, in.Namespace, 0, func(ctx context.Context, _ io.Writer, c sigs_client.Client, ns string) (any, error) {
		opts := simulateOptions{Pipeline: in.Pipeline, Env: in.Environment, Version: in.Version, Time: in.Time,
			PolicyNamespaces: in.PolicyNamespaces, AsAgent: in.AsAgent, Now: time.Now().UTC()}
		if len(opts.PolicyNamespaces) == 0 {
			opts.PolicyNamespaces = slices.Clone(defaultPolicyNamespaces)
		}
		if in.SoakMinutes != nil {
			opts.SoakMinutes, opts.Soaked = *in.SoakMinutes, true
		}
		rep, err := policySimulate(ctx, c, ns, opts)
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

func (in mcpShipIn) check() error {
	errs := []error{in.mcpPipelineOnly.check()}
	for _, img := range in.Images {
		if !mcpImagePattern.MatchString(img) || strings.Contains(img, "..") {
			errs = append(errs, argErr("images", img, "an image reference (registry/repository:tag or @digest)"))
		}
	}
	switch in.Type {
	case "", "image", "config", "mixed":
	default:
		errs = append(errs, argErr("type", in.Type, "image, config or mixed"))
	}
	return checkAll(errs...)
}

func mcpShip(ctx context.Context, k *mcpServer, in mcpShipIn) (*mcp.CallToolResult, error) {
	opts := createBundleOptions{Images: in.Images, Type: in.Type, ConfigCommit: in.ConfigCommit, Commit: in.Commit,
		Author: in.Author, CIRunURL: in.CIRunURL}
	if opts.Type == "" {
		opts.Type = "image"
	}
	return k.direct(ctx, in.Namespace, 0, func(ctx context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		return nil, shipFn(ctx, w, c, ns, in.Pipeline, opts, false, 0, false, false)
	})
}

func (in mcpPipelineTarget) check() error {
	return checkAll(checkNamespace(in.Namespace), checkName("pipeline", in.Pipeline, true), checkEnv(in.Environment, true))
}

func mcpPromote(ctx context.Context, k *mcpServer, in mcpPipelineTarget) (*mcp.CallToolResult, error) {
	return k.direct(ctx, in.Namespace, 0, func(_ context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		return nil, promoteFn(w, c, ns, in.Pipeline, in.Environment)
	})
}

type mcpRollbackIn struct {
	mcpPipelineTarget
	To            string `json:"to,omitempty" jsonschema:"The version or Bundle to roll back to (default: the one Verified there before)"`
	Hold          bool   `json:"hold,omitempty" jsonschema:"Keep the environment on the rollback until it is released (needs reason)"`
	Reason        string `json:"reason,omitempty" jsonschema:"Why the environment is held (required with hold)"`
	HoldExpiresIn string `json:"holdExpiresIn,omitempty" jsonschema:"With hold: end it this long after (30m, 24h, 2d)"`
}

func (in mcpRollbackIn) check() error {
	return checkAll(in.mcpPipelineTarget.check(), checkVersion("to", in.To, false))
}

func mcpRollback(ctx context.Context, k *mcpServer, in mcpRollbackIn) (*mcp.CallToolResult, error) {
	r := rollbackRequest{pipeline: in.Pipeline, env: in.Environment, to: in.To, hold: in.Hold, reason: in.Reason}
	if in.HoldExpiresIn != "" {
		d, err := parseDuration(in.HoldExpiresIn)
		if err != nil {
			return nil, fmt.Errorf("rollback: holdExpiresIn: %w", err)
		}
		r.expires = d
	}
	if err := r.check(); err != nil {
		return nil, err
	}
	return k.direct(ctx, in.Namespace, 0, func(ctx context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		return nil, rollbackDo(ctx, w, io.Discard, c, ns, r, nil)
	})
}

func (in mcpPipelineOnly) check() error {
	return checkAll(checkNamespace(in.Namespace), checkName("pipeline", in.Pipeline, true))
}

func mcpPause(ctx context.Context, k *mcpServer, in mcpPipelineOnly) (*mcp.CallToolResult, error) {
	return k.direct(ctx, in.Namespace, 0, func(_ context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		return nil, pauseFn(w, c, ns, in.Pipeline, "")
	})
}

func mcpResume(ctx context.Context, k *mcpServer, in mcpPipelineOnly) (*mcp.CallToolResult, error) {
	return k.direct(ctx, in.Namespace, 0, func(_ context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		return nil, resumeFn(w, c, ns, in.Pipeline, "")
	})
}

type mcpRejectIn struct {
	mcpNamespace
	Version  string `json:"version" jsonschema:"The version, or Bundle name, to reject"`
	Reason   string `json:"reason" jsonschema:"Why it is rejected"`
	Pipeline string `json:"pipeline,omitempty" jsonschema:"Look for the version in this pipeline only"`
}

func (in mcpRejectIn) check() error {
	if strings.TrimSpace(in.Reason) == "" {
		return fmt.Errorf("reason is required")
	}
	return checkAll(checkNamespace(in.Namespace), checkVersion("version", in.Version, true), checkName("pipeline", in.Pipeline, false))
}

func mcpReject(ctx context.Context, k *mcpServer, in mcpRejectIn) (*mcp.CallToolResult, error) {
	return k.direct(ctx, in.Namespace, 0, func(ctx context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		name, _, err := resolveBundleRefIn(ctx, c, refScope{ns: ns, pipeline: in.Pipeline, strict: true}, in.Version)
		if err != nil {
			return nil, fmt.Errorf("reject: %w", err)
		}
		return nil, rejectFn(ctx, w, c, ns, name, in.Reason, time.Now())
	})
}

type mcpApproveIn struct {
	mcpPipelineTarget
	Version  string            `json:"version" jsonschema:"The version waiting for the environment (image tag, config commit or chart version); nothing is recorded when another one waits"`
	Decision string            `json:"decision,omitempty" jsonschema:"approve (default) or reject"`
	Comment  string            `json:"comment,omitempty" jsonschema:"Note shown with the decision"`
	Fields   map[string]string `json:"fields,omitempty" jsonschema:"The values the gate's approval asks for (its approval.fields), by name; kardinal why lists them"`
}

func (in mcpApproveIn) check() error {
	if len(in.Fields) > lifecycle.MaxApprovalFields {
		return fmt.Errorf("fields: at most %d, not %d", lifecycle.MaxApprovalFields, len(in.Fields))
	}
	if err := checkVersion("version", in.Version, true); err != nil {
		return fmt.Errorf("%w: name the version you reviewed", err)
	}
	switch in.Decision {
	case "", "approve", "reject":
	default:
		return argErr("decision", in.Decision, "approve or reject")
	}
	return in.mcpPipelineTarget.check()
}

func mcpApprove(ctx context.Context, k *mcpServer, in mcpApproveIn) (*mcp.CallToolResult, error) {
	o := approveOptions{env: in.Environment, version: in.Version, decision: in.Decision, comment: in.Comment}
	names := make([]string, 0, len(in.Fields))
	for n := range in.Fields {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		o.fields = append(o.fields, n+"="+in.Fields[n])
	}
	if o.decision == "" {
		o.decision = "approve"
	}
	return k.direct(ctx, in.Namespace, 0, func(ctx context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		bundle, err := waitingBundle(ctx, c, ns, in.Pipeline, in.Environment)
		if err != nil {
			return nil, err
		}
		return nil, approveChecked(ctx, w, c, ns, bundle, o, nil, nil)
	})
}

// ─── verdicts ───────────────────────────────────────────────────────────────

var (
	// mcpCheckPattern is a Verdict's spec.check (the CRD's).
	mcpCheckPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	// mcpEvidencePattern is an evidence URL: http(s), no whitespace or
	// control characters, at most the CRD's 2048 bytes.
	mcpEvidencePattern = regexp.MustCompile(`^https?://[!-~]+$`)
)

// checkCheck checks a verify check name; "" passes unless required.
func checkCheck(v string, required bool) error {
	if v == "" {
		if required {
			return fmt.Errorf("check is required")
		}
		return nil
	}
	if len(v) > 63 || !mcpCheckPattern.MatchString(v) {
		return argErr("check", v, "a check name (lower case letters, digits and '-')")
	}
	return nil
}

type mcpVerdictsIn struct {
	mcpNamespace
	Pipeline    string `json:"pipeline" jsonschema:"Pipeline name"`
	Environment string `json:"environment,omitempty" jsonschema:"The environment: the version that runs there, and only its verdicts"`
	Version     string `json:"version,omitempty" jsonschema:"A version or Bundle name of the pipeline, instead of the one that runs in the environment"`
	Check       string `json:"check,omitempty" jsonschema:"Only verdicts for this check"`
	Offset      int    `json:"offset,omitempty" jsonschema:"The first verdict to return, from a previous page's nextOffset"`
}

func (in mcpVerdictsIn) check() error {
	errs := []error{checkNamespace(in.Namespace), checkName("pipeline", in.Pipeline, true), checkEnv(in.Environment, false),
		checkVersion("version", in.Version, false), checkCheck(in.Check, false)}
	if in.Environment == "" && in.Version == "" {
		errs = append(errs, fmt.Errorf("give environment (the version that runs there) or version"))
	}
	if in.Offset < 0 {
		errs = append(errs, fmt.Errorf("offset must not be negative"))
	}
	return checkAll(errs...)
}

// mcpVerdict is one verdict as the verdicts tool returns it. What the
// verifier wrote (summary, evidence, finding titles) is labelled untrusted.
type mcpVerdict struct {
	Name        string           `json:"name"`
	Bundle      string           `json:"bundle"`
	Environment string           `json:"environment"`
	Check       string           `json:"check"`
	Result      string           `json:"result"`
	Verifier    string           `json:"verifier"`
	Actor       *v1alpha1.Actor  `json:"actor,omitempty"`
	Summary     any              `json:"summary,omitempty"`
	EvidenceURL any              `json:"evidenceURL,omitempty"`
	Findings    []mcpVerdictFind `json:"findings,omitempty"`
	Counted     string           `json:"counted,omitempty"`
	CreatedAt   metav1.Time      `json:"createdAt"`
}

// mcpVerdictFind is one finding of a verdict.
type mcpVerdictFind struct {
	Severity string `json:"severity"`
	Title    any    `json:"title"`
}

// untrustedOr is s labelled untrusted, or nil when s is empty.
func untrustedOr(s string) any {
	if s == "" {
		return nil
	}
	return untrusted(s)
}

func mcpVerdicts(ctx context.Context, k *mcpServer, in mcpVerdictsIn) (*mcp.CallToolResult, error) {
	return k.direct(ctx, in.Namespace, in.Offset, func(ctx context.Context, _ io.Writer, c sigs_client.Client, ns string) (any, error) {
		var bundle string
		var err error
		if in.Version != "" {
			bundle, err = resolveBundleRef(ctx, c, ns, in.Pipeline, in.Version)
		} else {
			bundle, err = runningBundle(ctx, c, ns, in.Pipeline, in.Environment)
		}
		if err != nil {
			return nil, err
		}
		list, counted, err := verdictsCounted(ctx, c, ns, bundle, in.Environment, in.Check)
		if err != nil {
			return nil, err
		}
		out := make([]mcpVerdict, 0, len(list))
		for _, v := range list {
			mv := mcpVerdict{Name: v.Name, Bundle: v.Spec.Bundle, Environment: v.Spec.Environment, Check: v.Spec.Check,
				Result: v.Spec.Result, Verifier: actor.Describe(actor.OrUnknown(v.Spec.Actor, v.Spec.By)), Actor: v.Spec.Actor,
				Summary: untrustedOr(v.Spec.Summary), EvidenceURL: untrustedOr(v.Spec.EvidenceURL),
				Counted: counted[v.Name], CreatedAt: v.CreationTimestamp}
			for _, f := range v.Spec.Findings {
				mv.Findings = append(mv.Findings, mcpVerdictFind{Severity: f.Severity, Title: untrusted(f.Title)})
			}
			out = append(out, mv)
		}
		return out, nil
	})
}

type mcpVerifyIn struct {
	mcpPipelineTarget
	Version     string           `json:"version" jsonschema:"The version that runs in the environment, which you verified (image tag, config commit or chart version); nothing is recorded when another one runs there"`
	Check       string           `json:"check" jsonschema:"The check verified: a verify gate's verify.check"`
	Result      string           `json:"result" jsonschema:"pass, fail or inconclusive"`
	Summary     string           `json:"summary,omitempty" jsonschema:"What you found, in a sentence or two (required for fail and inconclusive)"`
	EvidenceURL string           `json:"evidenceURL,omitempty" jsonschema:"An http(s) link to the evidence (a CI run, a report)"`
	Findings    []mcpVerifyFound `json:"findings,omitempty" jsonschema:"What you found, each with a severity"`
}

// mcpVerifyFound is one finding the verify tool records.
type mcpVerifyFound struct {
	Severity string `json:"severity" jsonschema:"info, low, medium, high or critical"`
	Title    string `json:"title" jsonschema:"The finding in a line"`
}

func (in mcpVerifyIn) check() error {
	errs := []error{in.mcpPipelineTarget.check(), checkCheck(in.Check, true)}
	if err := checkVersion("version", in.Version, true); err != nil {
		errs = append(errs, fmt.Errorf("%w: name the version you verified", err))
	}
	switch in.Result {
	case "pass", "fail", "inconclusive":
	default:
		errs = append(errs, argErr("result", in.Result, "pass, fail or inconclusive"))
	}
	if len(in.Summary) > 1024 {
		errs = append(errs, fmt.Errorf("summary is longer than 1024 bytes"))
	}
	if in.EvidenceURL != "" && (len(in.EvidenceURL) > 2048 || !mcpEvidencePattern.MatchString(in.EvidenceURL)) {
		errs = append(errs, argErr("evidenceURL", in.EvidenceURL, "an http(s) URL without spaces"))
	}
	if len(in.Findings) > 32 {
		errs = append(errs, fmt.Errorf("at most 32 findings"))
	}
	for _, f := range in.Findings {
		switch f.Severity {
		case "info", "low", "medium", "high", "critical":
		default:
			errs = append(errs, argErr("findings.severity", f.Severity, "info, low, medium, high or critical"))
		}
		if strings.TrimSpace(f.Title) == "" || len(f.Title) > 256 {
			errs = append(errs, fmt.Errorf("findings.title: give a line of at most 256 bytes"))
		}
	}
	return checkAll(errs...)
}

func mcpVerify(ctx context.Context, k *mcpServer, in mcpVerifyIn) (*mcp.CallToolResult, error) {
	o := verifyOptions{env: in.Environment, check: in.Check, version: in.Version, summary: in.Summary, evidence: in.EvidenceURL,
		pass: in.Result == "pass", fail: in.Result == "fail", inconclusive: in.Result == "inconclusive"}
	for _, f := range in.Findings {
		o.findings = append(o.findings, f.Severity+":"+f.Title)
	}
	return k.direct(ctx, in.Namespace, 0, func(ctx context.Context, w io.Writer, c sigs_client.Client, ns string) (any, error) {
		bundle, err := runningBundle(ctx, c, ns, in.Pipeline, in.Environment)
		if err != nil {
			return nil, err
		}
		if err := checkVerifyVersion(ctx, c, ns, bundle, in.Environment, in.Version); err != nil {
			return nil, err
		}
		return nil, verifyFn(ctx, w, c, ns, bundle, o, time.Now())
	})
}

// ─── freezes ────────────────────────────────────────────────────────────────

// mcpFreezeItem is one freeze as the freezes tool returns it.
type mcpFreezeItem struct {
	Name       string `json:"name"`
	Holds      string `json:"holds"`
	Active     bool   `json:"active"`
	Since      string `json:"since,omitempty"`
	Until      string `json:"until,omitempty"`
	CreatedBy  string `json:"createdBy,omitempty"`
	Exemptions int    `json:"exemptions"`
	Reason     any    `json:"reason,omitempty"`
}

func mcpFreezes(ctx context.Context, k *mcpServer, _ struct{}) (*mcp.CallToolResult, error) {
	return k.direct(ctx, "", 0, func(ctx context.Context, _ io.Writer, c sigs_client.Client, _ string) (any, error) {
		freezes, err := lifecycle.ListFreezes(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("freezes: %w", err)
		}
		out := make([]mcpFreezeItem, 0, len(freezes))
		for _, cw := range freezes {
			f := mcpFreezeItem{Name: cw.Name, Holds: changewindow.FreezeScope(cw.Spec.Freeze), Active: changewindow.FreezeActive(&cw),
				Since: cw.Spec.Start.UTC().Format(time.RFC3339), Exemptions: len(cw.Status.Exemptions),
				Reason: untrustedOr(cw.Spec.Reason)}
			if cw.Spec.Freeze != nil {
				f.CreatedBy = cw.Spec.Freeze.CreatedBy
			}
			if !cw.Spec.End.IsZero() {
				f.Until = cw.Spec.End.UTC().Format(time.RFC3339)
			}
			out = append(out, f)
		}
		return out, nil
	})
}

type mcpFreezeIn struct {
	Holds     string `json:"holds" jsonschema:"agents (the versions agents created) or everyone (stop-the-line)"`
	Namespace string `json:"namespace,omitempty" jsonschema:"Only this namespace; empty: every namespace"`
	Reason    string `json:"reason" jsonschema:"Why: every held step shows it"`
	For       string `json:"for,omitempty" jsonschema:"End the freeze after this long (30m, 4h, 2d); empty: until a person lifts it"`
}

func (in mcpFreezeIn) check() error {
	errs := []error{checkNamespace(in.Namespace)}
	switch in.Holds {
	case v1alpha1.FreezeAgents, v1alpha1.FreezeEveryone:
	default:
		errs = append(errs, argErr("holds", in.Holds, "agents or everyone"))
	}
	if strings.TrimSpace(in.Reason) == "" {
		errs = append(errs, fmt.Errorf("reason is required: every held step shows it"))
	}
	if len(in.Reason) > 1024 {
		errs = append(errs, fmt.Errorf("reason is longer than 1024 bytes"))
	}
	if in.For != "" {
		if _, err := parseDuration(in.For); err != nil {
			errs = append(errs, argErr("for", in.For, "a duration such as 30m, 4h or 2d"))
		}
	}
	return checkAll(errs...)
}

func mcpFreeze(ctx context.Context, k *mcpServer, in mcpFreezeIn) (*mcp.CallToolResult, error) {
	var d time.Duration
	if in.For != "" {
		d, _ = parseDuration(in.For) // checked
	}
	return k.direct(ctx, "", 0, func(_ context.Context, w io.Writer, c sigs_client.Client, _ string) (any, error) {
		return nil, freezeFn(w, c, in.Holds, in.Namespace, in.Reason, d, time.Now())
	})
}
