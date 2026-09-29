// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package examples

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sigyaml "sigs.k8s.io/yaml"

	kardinalcmd "github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/cmd"
)

// invocation is one `kardinal ...` command line from a fenced code block.
type invocation struct {
	where string // file:line
	args  []string
}

// shellWords splits one command line the way a shell would for the cases the
// READMEs and scripts use: single and double quotes, $(...) substitutions, and
// a command that ends at an unquoted comment, pipe, redirect (including its
// file descriptor, as in 2>&1), command separator, or the ) that closes the
// $(...) the command runs in.
func shellWords(line string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	flush := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	var quote rune
	depth := 0
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case depth > 0:
			cur.WriteRune(r)
			if r == '(' {
				depth++
			} else if r == ')' {
				depth--
			}
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == '$' && i+1 < len(rs) && rs[i+1] == '(':
			cur.WriteString("$(")
			i++
			depth = 1
			inWord = true
		case r == ' ' || r == '\t':
			flush()
		case r == '>' || r == '<':
			if inWord && strings.Trim(cur.String(), "0123456789") == "" {
				cur.Reset() // the file descriptor of 2>&1
				inWord = false
			}
			flush()
			return words
		case r == '#' && !inWord, r == '|', r == ';', r == '&', r == ')':
			flush()
			return words
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	flush()
	return words
}

// shellFence lists the code block languages that hold shell commands.
var shellFence = map[string]bool{"bash": true, "sh": true, "shell": true, "console": true}

// documentedInvocations returns every `kardinal` command in a shell code
// block of a Markdown file under manifestDirs.
func documentedInvocations(t *testing.T) []invocation {
	t.Helper()
	root := repoRoot(t)
	var out []invocation
	for _, dir := range manifestDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
				return err
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return fmt.Errorf("relative path of %s: %w", p, err)
			}
			f, err := os.Open(p)
			if err != nil {
				return fmt.Errorf("open %s: %w", p, err)
			}
			defer f.Close()

			sc := bufio.NewScanner(f)
			inFence, inBlock := false, false
			n, start := 0, 0
			var pending string
			for sc.Scan() {
				n++
				line := strings.TrimSpace(sc.Text())
				if strings.HasPrefix(line, "```") {
					// Only shell blocks hold commands; unlabeled blocks are
					// diagrams and sample output.
					inBlock = !inBlock
					inFence = inBlock && shellFence[strings.TrimPrefix(line, "```")]
					pending = ""
					continue
				}
				if !inFence {
					continue
				}
				if pending == "" {
					start = n
				}
				if strings.HasSuffix(line, `\`) {
					pending += strings.TrimSuffix(line, `\`) + " "
					continue
				}
				full := strings.TrimPrefix(pending+line, "$ ")
				pending = ""
				words := shellWords(full)
				if len(words) > 0 && words[0] == "kardinal" {
					out = append(out, invocation{where: fmt.Sprintf("%s:%d", filepath.ToSlash(rel), start), args: words[1:]})
				}
			}
			if err := sc.Err(); err != nil {
				return fmt.Errorf("read %s: %w", p, err)
			}
			return nil
		})
		require.NoError(t, err)
	}
	return out
}

// parseInvocation resolves the subcommand and parses its flags and positional
// arguments. It never runs the command.
func parseInvocation(args []string) error {
	root := kardinalcmd.NewRootCmd()
	sub, rest, err := root.Find(args)
	if err != nil {
		return fmt.Errorf("find command: %w", err)
	}
	if !sub.Runnable() {
		return fmt.Errorf("%q is not a runnable command", sub.CommandPath())
	}
	if err := sub.ParseFlags(rest); err != nil {
		return fmt.Errorf("%s: %w", sub.CommandPath(), err)
	}
	if err := sub.ValidateRequiredFlags(); err != nil {
		return fmt.Errorf("%s: %w", sub.CommandPath(), err)
	}
	if err := sub.ValidateArgs(sub.Flags().Args()); err != nil {
		return fmt.Errorf("%s: %w", sub.CommandPath(), err)
	}
	return nil
}

func TestDocumentedCLIInvocationsParse(t *testing.T) {
	invs := documentedInvocations(t)
	require.Greater(t, len(invs), 15, "too few kardinal invocations found; is the scan broken?")
	for _, inv := range invs {
		t.Run(inv.where, func(t *testing.T) {
			assert.NoError(t, parseInvocation(inv.args), "kardinal %s", strings.Join(inv.args, " "))
		})
	}
}

// cliCommand matches the kardinal CLI as a command word: kardinal, $KARDINAL
// (demo/scripts/validate.sh) or a quoted path ending in /kardinal. The command
// word must follow a separator, so prose inside strings does not match.
var cliCommand = regexp.MustCompile(`(?:^|[\s;|&(!{])((?:"[^"\s]*/)?(?:\$\{?KARDINAL\}?|kardinal)"?)\s`)

// checkCmd matches demo/scripts/validate.sh's check_cmd "<kardinal args>".
var checkCmd = regexp.MustCompile(`^\s*check_cmd "([^"]*)"`)

// wholeExpansion matches a word that is a single shell expansion.
var wholeExpansion = regexp.MustCompile(`^\$(?:\{?[A-Za-z_][A-Za-z0-9_]*\}?|[0-9@*#])$`)

// commandPosition reports whether a command may start after prefix: at the
// start of a line, after a separator or keyword, inside $(, or after
// timeout N.
func commandPosition(prefix string) bool {
	fields := strings.Fields(prefix)
	if n := len(fields); n >= 2 && strings.HasSuffix(fields[n-2], "timeout") && strings.Trim(fields[n-1], "0123456789") == "" {
		// timeout N kardinal ..., possibly as $(timeout N kardinal ...)
		if rest := strings.TrimSuffix(fields[n-2], "timeout"); rest != "" {
			fields = append(fields[:n-2], rest)
		} else {
			fields = fields[:n-2]
		}
	}
	if len(fields) == 0 {
		return true
	}
	switch last := fields[len(fields)-1]; last {
	case "if", "then", "do", "else", "elif", "while", "until", "!", "&&", "||", "|", ";", "{":
		return true
	default:
		return strings.HasSuffix(last, "(") || strings.HasSuffix(last, ";")
	}
}

// scriptInvocations returns the kardinal commands in one shell script. A word
// that is a single expansion ("$PIPELINE", "$2") becomes 1, which every flag
// type accepts; check_cmd "<args>" (demo/scripts/validate.sh) counts as a
// kardinal command with those arguments.
func scriptInvocations(where, script string) []invocation {
	var out []invocation
	lines := strings.Split(script, "\n")
	for i := 0; i < len(lines); i++ {
		start := i + 1
		line := lines[i]
		for strings.HasSuffix(strings.TrimSpace(line), `\`) && i+1 < len(lines) {
			i++
			line = strings.TrimSuffix(strings.TrimSpace(line), `\`) + " " + lines[i]
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		var calls [][]string
		for _, m := range cliCommand.FindAllStringSubmatchIndex(line, -1) {
			if commandPosition(line[:m[2]]) {
				calls = append(calls, shellWords(line[m[3]:]))
			}
		}
		if m := checkCmd.FindStringSubmatch(line); m != nil {
			calls = append(calls, shellWords(m[1]))
		}
		for _, args := range calls {
			for j, w := range args {
				if wholeExpansion.MatchString(w) {
					args[j] = "1"
				}
			}
			out = append(out, invocation{where: fmt.Sprintf("%s:%d", where, start), args: args})
		}
	}
	return out
}

// scriptedInvocations returns every kardinal command in the workflows' run:
// scripts and in the repository's shell scripts.
func scriptedInvocations(t *testing.T) []invocation {
	t.Helper()
	root := repoRoot(t)
	var out []invocation
	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	require.NoError(t, err)
	for _, p := range workflows {
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		var wf struct {
			Jobs map[string]struct {
				Steps []struct {
					Name string `json:"name"`
					Run  string `json:"run"`
				} `json:"steps"`
			} `json:"jobs"`
		}
		require.NoError(t, sigyaml.Unmarshal(data, &wf), p)
		for job, j := range wf.Jobs {
			for _, s := range j.Steps {
				where := fmt.Sprintf(".github/workflows/%s %s/%q", filepath.Base(p), job, s.Name)
				out = append(out, scriptInvocations(where, s.Run)...)
			}
		}
	}
	for _, pattern := range []string{"demo/scripts/*.sh", "hack/*.sh", "scripts/*.sh", ".github/actions/*/*.sh"} {
		files, err := filepath.Glob(filepath.Join(root, pattern))
		require.NoError(t, err)
		for _, p := range files {
			data, err := os.ReadFile(p)
			require.NoError(t, err)
			rel, err := filepath.Rel(root, p)
			require.NoError(t, err)
			out = append(out, scriptInvocations(filepath.ToSlash(rel), string(data))...)
		}
	}
	return out
}

// TestScriptedCLIInvocationsParse checks that every kardinal command the
// workflows and scripts run exists and is called with flags and arguments it
// accepts, so a CLI change cannot silently turn a check into a failure that
// looks like a product bug.
func TestScriptedCLIInvocationsParse(t *testing.T) {
	invs := scriptedInvocations(t)
	require.Greater(t, len(invs), 25, "too few kardinal invocations found; is the scan broken?")
	for _, inv := range invs {
		t.Run(inv.where, func(t *testing.T) {
			assert.NoError(t, parseInvocation(inv.args), "kardinal %s", strings.Join(inv.args, " "))
		})
	}
}

func TestScriptInvocationsFindCommandsOnly(t *testing.T) {
	script := `# kardinal get pipelines (a comment)
if OUT=$(kardinal create bundle "$PIPELINE" --image "$TEST_IMAGE" 2>&1); then
  echo "=== S10: kardinal get bundles ==="
fi
simulate() { kardinal policy simulate --pipeline "$P" --env prod --time "$1" --soak-minutes "$2" 2>&1; }
OUTPUT=$(timeout 30 $KARDINAL rollback app --env prod 2>&1)
echo "version: $("${BIN}/kardinal" version 2>&1 | head -1)"
helm install kardinal chart/ --namespace kardinal-system
kardinal get bundles app \
  -o json | jq .
  check_cmd "history app" "Bundle"
`
	var got [][]string
	for _, inv := range scriptInvocations("test", script) {
		got = append(got, inv.args)
	}
	assert.Equal(t, [][]string{
		{"create", "bundle", "1", "--image", "1"},
		{"policy", "simulate", "--pipeline", "1", "--env", "prod", "--time", "1", "--soak-minutes", "1"},
		{"rollback", "app", "--env", "prod"},
		{"version"},
		{"get", "bundles", "app", "-o", "json"},
		{"history", "app"},
	}, got)
	for _, args := range got {
		assert.NoError(t, parseInvocation(args), "kardinal %s", strings.Join(args, " "))
	}
}

func TestShellWordsAndParseCatchKnownMistakes(t *testing.T) {
	tests := []struct {
		line    string
		words   []string
		wantErr string
	}{
		{
			line:  `kardinal policy simulate --pipeline app --env prod --time "Saturday 3pm" # blocked`,
			words: []string{"kardinal", "policy", "simulate", "--pipeline", "app", "--env", "prod", "--time", "Saturday 3pm"},
		},
		{
			line:  `kardinal create bundle app --image "ghcr.io/o/a:sha-$(git rev-parse --short HEAD)" | tee out`,
			words: []string{"kardinal", "create", "bundle", "app", "--image", "ghcr.io/o/a:sha-$(git rev-parse --short HEAD)"},
		},
		{
			line:    "kardinal create bundle app --image ghcr.io/o/a:1 --commit abc123",
			wantErr: "unknown flag: --commit",
		},
		{
			line:    `kardinal override app --env prod --reason "fix"`,
			wantErr: "unknown flag: --env",
		},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			words := shellWords(tt.line)
			if tt.words != nil {
				assert.Equal(t, tt.words, words)
			}
			err := parseInvocation(words[1:])
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}
