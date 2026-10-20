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
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The commands quickstart prints for you to run next (approve, ship, port-
// forward, delete a namespace, uninstall a release) name the local context
// it checked: copied into a shell whose current context is another cluster,
// a command without it would run there (#1785).

// hintVerbs are, for kubectl and helm, the words after which a command is a
// command quickstart printed, not prose ("helm or kubectl would use it"); for
// kardinal, every subcommand of the CLI (kardinalVerbs).
var hintVerbs = map[string]map[string]bool{
	"kubectl": setOf("-n", "--namespace", "get", "apply", "delete", "patch", "describe", "port-forward", "create", "rollout",
		"exec", "logs", "edit", "label", "wait", "cluster-info"),
	"helm": setOf("uninstall", "install", "upgrade", "get", "list", "status", "rollback"),
}

func setOf(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// kardinalVerbs are the CLI's subcommands (TestQuickstartHints_Verbs keeps
// the list equal to the root command's).
var kardinalVerbs = setOf("approve", "audit", "completion", "create", "dashboard", "delete", "diff", "doctor", "explain",
	"freeze", "get", "help", "history", "hold", "inbox", "init", "logs", "mcp", "metrics", "override", "pause", "policy",
	"promote", "quickstart", "refresh", "reject", "resume", "rollback", "ship", "status", "unfreeze", "unhold",
	"validate", "verify", "verify-receipt", "version", "why")

// hintCommand is kubectl, helm or kardinal at the start of a word, and the
// word after it.
var hintCommand = regexp.MustCompile(`(^|[\s(\x60'"])(kubectl|helm|kardinal) (\S+)`)

// hinter adds the checked context, and the kubeconfig it was read from, to
// every command in the text quickstart prints.
type hinter struct {
	kctx string
	// kubeconfig is --kubeconfig, else $KUBECONFIG ("" for neither: the
	// default ~/.kube/config). A KUBECONFIG of several files becomes
	// KUBECONFIG=<files> before the command (envKubeconfig): --kubeconfig
	// takes one file.
	kubeconfig, envKubeconfig string
	verbs                     map[string]bool // kardinal's
}

func newHinter(kctx string) *hinter {
	return hinterFor(kctx, globalKubeconfig, os.Getenv("KUBECONFIG"))
}

// hinterFor is the hinter of context kctx read from the kubeconfig of flag
// (--kubeconfig) or env ($KUBECONFIG).
func hinterFor(kctx, flag, env string) *hinter {
	h := &hinter{kctx: kctx, kubeconfig: flag, verbs: kardinalVerbs}
	if flag == "" && env != "" {
		if files := filepath.SplitList(env); len(files) == 1 {
			h.kubeconfig = files[0]
		} else {
			h.envKubeconfig = env
		}
	}
	return h
}

// isCommand reports whether word after tool makes it a command.
func (h *hinter) isCommand(tool, word string) bool {
	if tool == "kardinal" {
		return h.verbs[word]
	}
	return hintVerbs[tool][word]
}

// text is s with the context flags after each command in it: --context (helm:
// --kube-context), and --kubeconfig when quickstart was run with it.
func (h *hinter) text(s string) string {
	return hintCommand.ReplaceAllStringFunc(s, func(m string) string {
		sub := hintCommand.FindStringSubmatch(m)
		lead, tool, word := sub[1], sub[2], sub[3]
		if !h.isCommand(tool, word) {
			return m
		}
		flag := "--context"
		if tool == "helm" {
			flag = "--kube-context"
		}
		flags := " " + flag + " " + shellWord(h.kctx)
		if h.kubeconfig != "" {
			flags += " --kubeconfig " + shellWord(h.kubeconfig)
		}
		if h.envKubeconfig != "" {
			lead += "KUBECONFIG=" + shellWord(h.envKubeconfig) + " "
		}
		return lead + tool + flags + " " + word
	})
}

// shellWord is s as one word for a POSIX shell.
func shellWord(s string) string {
	if regexp.MustCompile(`^[A-Za-z0-9._/:@%+=,-]+$`).MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writer is w with every command of what is written to it given the context
// flags. quickstart writes whole lines, so a command never spans two writes.
func (h *hinter) writer(w io.Writer) io.Writer { return &hintWriter{w: w, h: h} }

type hintWriter struct {
	w io.Writer
	h *hinter
}

func (hw *hintWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(hw.w, hw.h.text(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// err is e with the context flags in its message; errors.Is and errors.As
// still see e.
func (h *hinter) err(e error) error {
	if e == nil {
		return nil
	}
	msg := h.text(e.Error())
	if msg == e.Error() {
		return e
	}
	return &hintedError{msg: msg, err: e}
}

type hintedError struct {
	msg string
	err error
}

func (e *hintedError) Error() string { return e.msg }
func (e *hintedError) Unwrap() error { return e.err }
