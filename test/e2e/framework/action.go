// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

// ActionRun is the result of RunAction.
type ActionRun struct {
	// Outputs are the action's outputs, as a later step reads them.
	Outputs map[string]string
	// Log is the steps' stdout and stderr.
	Log string
	// Err is the first step's failure, nil when every step succeeded.
	Err error
}

type actionFile struct {
	Inputs map[string]struct {
		Default string `json:"default"`
	} `json:"inputs"`
	Outputs map[string]struct {
		Value string `json:"value"`
	} `json:"outputs"`
	Runs struct {
		Using string `json:"using"`
		Steps []struct {
			ID    string            `json:"id"`
			Name  string            `json:"name"`
			Shell string            `json:"shell"`
			Run   string            `json:"run"`
			Env   map[string]string `json:"env"`
		} `json:"steps"`
	} `json:"runs"`
}

var expr = regexp.MustCompile(`\$\{\{\s*([^}]*?)\s*\}\}`)

// RunAction runs the composite GitHub Action in dir (with its action.yml)
// on this host the way a GitHub runner runs its bash steps: the step's run
// script under `bash --noprofile --norc -eo pipefail`, with the step env,
// GITHUB_ACTION_PATH and GITHUB_OUTPUT set, after substituting the
// ${{ inputs.* }}, ${{ env.* }} and ${{ steps.*.outputs.* }} expressions.
// env is the calling step's environment (secrets and the GITHUB_* context
// variables a runner sets); the host's PATH and HOME are added. It fails the
// test when the action uses anything else, so a test never passes against a
// half-run action. Scratch files live under the artifacts directory.
func RunAction(t *testing.T, dir string, inputs, env map[string]string) ActionRun {
	t.Helper()
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "action.yml"))
	if err != nil {
		t.Fatalf("read the action: %v", err)
	}
	var a actionFile
	if err := yaml.Unmarshal(raw, &a); err != nil {
		t.Fatalf("parse %s/action.yml: %v", dir, err)
	}
	if a.Runs.Using != "composite" {
		t.Fatalf("%s: runs.using is %q; RunAction runs composite actions only", dir, a.Runs.Using)
	}
	for name := range inputs {
		if _, ok := a.Inputs[name]; !ok {
			t.Fatalf("%s has no input %q", dir, name)
		}
	}

	work := scratchDir(t)
	steps := map[string]map[string]string{}
	eval := func(s string) string {
		return expr.ReplaceAllStringFunc(s, func(m string) string {
			ref := expr.FindStringSubmatch(m)[1]
			parts := strings.Split(ref, ".")
			switch {
			case len(parts) == 2 && parts[0] == "inputs":
				in, ok := a.Inputs[parts[1]]
				if !ok {
					t.Fatalf("%s refers to the undeclared input %q", dir, parts[1])
				}
				if v, set := inputs[parts[1]]; set {
					return v
				}
				return in.Default
			case len(parts) == 2 && parts[0] == "env":
				return env[parts[1]]
			case len(parts) == 4 && parts[0] == "steps" && parts[2] == "outputs":
				return steps[parts[1]][parts[3]]
			}
			t.Fatalf("%s uses the expression %q, which RunAction does not evaluate", dir, m)
			return ""
		})
	}

	var log strings.Builder
	var runErr error
	for i, s := range a.Runs.Steps {
		if s.Shell != "bash" || s.Run == "" {
			t.Fatalf("%s step %d (%s): only run steps with shell bash are supported", dir, i, s.Name)
		}
		script := filepath.Join(work, fmt.Sprintf("step-%d.sh", i))
		output := filepath.Join(work, fmt.Sprintf("step-%d.output", i))
		if err := os.WriteFile(script, []byte(eval(s.Run)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		vars := map[string]string{"PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME"), "TMPDIR": work}
		for k, v := range env {
			vars[k] = v
		}
		for k, v := range s.Env {
			vars[k] = eval(v)
		}
		vars["GITHUB_ACTION_PATH"] = dir
		vars["GITHUB_OUTPUT"] = output
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-eo", "pipefail", script)
		for k, v := range vars {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		out, err := cmd.CombinedOutput()
		cancel()
		fmt.Fprintf(&log, "--- step %d (%s)\n%s", i, s.Name, out)
		if err != nil {
			runErr = fmt.Errorf("step %d (%s): %w", i, s.Name, err)
			break
		}
		if s.ID != "" {
			steps[s.ID] = readOutputs(t, output)
		}
	}
	res := ActionRun{Outputs: map[string]string{}, Log: log.String(), Err: runErr}
	if runErr == nil {
		for name, o := range a.Outputs {
			res.Outputs[name] = eval(o.Value)
		}
	}
	t.Logf("action %s: %v\n%s", filepath.Base(dir), runErr, res.Log)
	return res
}

// readOutputs parses a GITHUB_OUTPUT file: name=value lines and
// name<<DELIMITER multi-line values.
func readOutputs(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if name, delim, ok := strings.Cut(line, "<<"); ok && !strings.Contains(name, "=") {
			var val []string
			for sc.Scan() && sc.Text() != delim {
				val = append(val, sc.Text())
			}
			out[name] = strings.Join(val, "\n")
			continue
		}
		if name, val, ok := strings.Cut(line, "="); ok {
			out[name] = val
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// scratchDir is an empty directory for this test under the artifacts
// directory, removed when the test passes.
func scratchDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.Abs(filepath.Join(artifactsDir(), "scratch"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	d, err := os.MkdirTemp(base, strings.NewReplacer("/", "-").Replace(t.Name())+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			_ = os.RemoveAll(d)
		}
	})
	return d
}
