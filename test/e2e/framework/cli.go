// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// ControllerSelector is the label selector of the controller's pods.
const ControllerSelector = "app.kubernetes.io/name=kardinal-promoter"

// CLI runs the kardinal CLI under test (KARDINAL_E2E_CLI) and captures what a
// user sees: stdout, stderr and the exit code. By default the CLI sees a
// kubeconfig that holds only the kind cluster, so no command can reach
// another cluster whatever the host's ~/.kube/config says.
type CLI struct {
	t *testing.T
	e *Env
	// Kubeconfig is the kind-only kubeconfig every run uses by default. Its
	// one context is named e.Context and has no namespace.
	Kubeconfig string
}

// CLIResult is one finished CLI run.
type CLIResult struct {
	Args   []string
	Stdout string
	Stderr string
	// Code is the exit code; -1 when the process did not exit normally.
	Code int
}

// Output is stdout followed by stderr.
func (r CLIResult) Output() string { return r.Stdout + r.Stderr }

// CLIOptions tune one run.
type CLIOptions struct {
	// Kubeconfig is the KUBECONFIG the CLI sees: "" is the kind-only file,
	// "/dev/null" is no kubeconfig at all. The host's default kubeconfig is
	// never used.
	Kubeconfig string
	// Env adds KEY=VALUE pairs to the CLI's environment.
	Env []string
	// Stdin is the CLI's standard input.
	Stdin string
	// Dir is the working directory (default: the test's current directory).
	Dir string
	// Bin runs another kardinal binary (default: KARDINAL_E2E_CLI).
	Bin string
}

// CLI returns a runner for the kardinal CLI under test.
func (e *Env) CLI(t *testing.T) *CLI {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	e.WriteKubeconfig(t, path, map[string]string{e.Context: ""}, e.Context)
	return &CLI{t: t, e: e, Kubeconfig: path}
}

// WriteKubeconfig writes a kubeconfig to path whose contexts all point at the
// kind cluster with the harness's credentials. contexts maps each context
// name to its namespace ("" for none); current is the current-context.
func (e *Env) WriteKubeconfig(t *testing.T, path string, contexts map[string]string, current string) {
	t.Helper()
	tls := e.Config.TLSClientConfig
	cfg := clientcmdapi.NewConfig()
	cfg.Clusters["kind"] = &clientcmdapi.Cluster{
		Server:                   e.Config.Host,
		CertificateAuthority:     tls.CAFile,
		CertificateAuthorityData: tls.CAData,
		TLSServerName:            tls.ServerName,
		InsecureSkipTLSVerify:    tls.Insecure,
	}
	cfg.AuthInfos["kind"] = &clientcmdapi.AuthInfo{
		ClientCertificate:     tls.CertFile,
		ClientCertificateData: tls.CertData,
		ClientKey:             tls.KeyFile,
		ClientKeyData:         tls.KeyData,
		Token:                 e.Config.BearerToken,
	}
	for name, ns := range contexts {
		cfg.Contexts[name] = &clientcmdapi.Context{Cluster: "kind", AuthInfo: "kind", Namespace: ns}
	}
	cfg.CurrentContext = current
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatalf("write kubeconfig %s: %v", path, err)
	}
}

// command builds the exec.Cmd of one run.
func (c *CLI) command(opts CLIOptions, args []string) *exec.Cmd {
	bin := opts.Bin
	if bin == "" {
		bin = c.e.cli
	}
	cmd := exec.Command(bin, args...)
	kubeconfig := opts.Kubeconfig
	if kubeconfig == "" {
		kubeconfig = c.Kubeconfig
	}
	// KUBECONFIG is always set, so client-go never falls back to
	// ~/.kube/config (the host's default context is not a test cluster).
	env := []string{"KUBECONFIG=" + kubeconfig}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "KUBECONFIG=") {
			env = append(env, kv)
		}
	}
	env = append(env, opts.Env...)
	cmd.Env = env
	cmd.Dir = opts.Dir
	if opts.Stdin != "" {
		cmd.Stdin = strings.NewReader(opts.Stdin)
	}
	return cmd
}

// Exec runs the CLI with exactly args (no -n or --context added).
func (c *CLI) Exec(opts CLIOptions, args ...string) CLIResult {
	c.t.Helper()
	cmd := c.command(opts, args)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := CLIResult{Args: args, Stdout: stdout.String(), Stderr: stderr.String(), Code: exitCode(err)}
	if r.Code == -1 {
		c.t.Fatalf("run kardinal %s: %v", strings.Join(args, " "), err)
	}
	c.t.Logf("$ kardinal %s  (exit %d)\n%s%s", strings.Join(args, " "), r.Code, r.Stdout, prefixLines("stderr: ", r.Stderr))
	return r
}

// Args prefixes args with -n ns (unless ns is "") and --context.
func (c *CLI) Args(ns string, args ...string) []string {
	full := []string{"--context", c.e.Context}
	if ns != "" {
		full = append([]string{"-n", ns}, full...)
	}
	return append(full, args...)
}

// Run runs kardinal -n ns --context <kind> args.
func (c *CLI) Run(ns string, args ...string) CLIResult {
	c.t.Helper()
	return c.Exec(CLIOptions{}, c.Args(ns, args...)...)
}

// Must is Run that fails the test unless the CLI exits 0. It returns stdout.
func (c *CLI) Must(ns string, args ...string) string {
	c.t.Helper()
	r := c.Run(ns, args...)
	if r.Code != 0 {
		c.t.Fatalf("kardinal %s: exit %d\n%s", strings.Join(args, " "), r.Code, r.Output())
	}
	return r.Stdout
}

// Fail is Run that fails the test if the CLI exits 0.
func (c *CLI) Fail(ns string, args ...string) CLIResult {
	c.t.Helper()
	r := c.Run(ns, args...)
	if r.Code == 0 {
		c.t.Fatalf("kardinal %s: exit 0, want a failure\n%s", strings.Join(args, " "), r.Output())
	}
	return r
}

// CLIProcess is a CLI run in the background, for --watch and --follow.
type CLIProcess struct {
	t      *testing.T
	args   []string
	cmd    *exec.Cmd
	stdout lockedBuffer
	stderr lockedBuffer
	done   chan struct{}
	err    error
}

// Start starts kardinal with exactly args in the background. The process is
// killed when the test ends if it is still running.
func (c *CLI) Start(opts CLIOptions, args ...string) *CLIProcess {
	c.t.Helper()
	p := &CLIProcess{t: c.t, args: args, cmd: c.command(opts, args), done: make(chan struct{})}
	p.cmd.Stdout, p.cmd.Stderr = &p.stdout, &p.stderr
	if err := p.cmd.Start(); err != nil {
		c.t.Fatalf("start kardinal %s: %v", strings.Join(args, " "), err)
	}
	c.t.Logf("$ kardinal %s &", strings.Join(args, " "))
	go func() {
		p.err = p.cmd.Wait()
		close(p.done)
	}()
	c.t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

// Stdout is what the process has written to stdout so far.
func (p *CLIProcess) Stdout() string { return p.stdout.String() }

// WaitStdout waits until match accepts the stdout so far.
func (p *CLIProcess) WaitStdout(timeout time.Duration, what string, match func(stdout string) bool) string {
	p.t.Helper()
	var out string
	Eventually(p.t, timeout, what, func(context.Context) (bool, string) {
		out = p.Stdout()
		select {
		case <-p.done:
			return match(out), fmt.Sprintf("the CLI exited (%v); stdout:\n%s\nstderr:\n%s", p.err, out, p.stderr.String())
		default:
		}
		return match(out), "stdout so far:\n" + out
	})
	return out
}

// Exited reports whether the process has exited.
func (p *CLIProcess) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Interrupt sends SIGINT, as Ctrl-C does.
func (p *CLIProcess) Interrupt() {
	p.t.Helper()
	if err := p.cmd.Process.Signal(os.Interrupt); err != nil && !p.Exited() {
		p.t.Fatalf("interrupt kardinal %s: %v", strings.Join(p.args, " "), err)
	}
}

// Wait waits for the process to exit and returns its result.
func (p *CLIProcess) Wait(timeout time.Duration) CLIResult {
	p.t.Helper()
	select {
	case <-p.done:
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-p.done
		p.t.Fatalf("kardinal %s did not exit within %s; stdout:\n%s", strings.Join(p.args, " "), timeout, p.Stdout())
	}
	r := CLIResult{Args: p.args, Stdout: p.Stdout(), Stderr: p.stderr.String(), Code: exitCode(p.err)}
	p.t.Logf("$ kardinal %s  (exit %d)\n%s%s", strings.Join(p.args, " "), r.Code, r.Stdout, prefixLines("stderr: ", r.Stderr))
	return r
}

// lockedBuffer is a bytes.Buffer safe for one writer and concurrent readers.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// exitCode is the exit code of a finished command: 0 on success, the code of
// an *exec.ExitError, else -1.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func prefixLines(prefix, s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

// ControllerLogs returns the log lines the controller pods wrote since since.
func (e *Env) ControllerLogs(t *testing.T, since time.Time) string {
	t.Helper()
	return e.podLogs(t, ControllerSelector, since)
}

// VariantLogs returns the log lines the pods of controller variant v wrote
// since since.
func (e *Env) VariantLogs(t *testing.T, v *Variant, since time.Time) string {
	t.Helper()
	return e.podLogs(t, variantLabel+"="+v.Name, since)
}

// podLogs returns the log lines the pods in ControllerNamespace that match
// selector wrote since since, pod by pod in name order.
func (e *Env) podLogs(t *testing.T, selector string, since time.Time) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pods, err := e.Kube.CoreV1().Pods(ControllerNamespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		t.Fatalf("list controller pods: %v", err)
	}
	if len(pods.Items) == 0 {
		t.Fatalf("no controller pods (%s) in %s", selector, ControllerNamespace)
	}
	names := make([]string, 0, len(pods.Items))
	for _, p := range pods.Items {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	var out strings.Builder
	st := metav1.NewTime(since)
	for _, name := range names {
		raw, err := e.Kube.CoreV1().Pods(ControllerNamespace).GetLogs(name, &corev1.PodLogOptions{SinceTime: &st}).DoRaw(ctx)
		if err != nil {
			t.Fatalf("logs of %s/%s: %v", ControllerNamespace, name, err)
		}
		out.Write(raw)
	}
	return out.String()
}

// BranchHead returns the commit SHA at the head of repo's branch, read from
// the git server's API.
func (e *Env) BranchHead(t *testing.T, repo gitserver.Repo) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sha, err := e.Brancher(t).BranchHead(ctx, repo, repo.Branch)
	if err != nil || sha == "" {
		t.Fatalf("head of %s branch %s: %q, %v", repo.Name, repo.Branch, sha, err)
	}
	return sha
}

// Path is the kardinal binary under test.
func (c *CLI) Path() string { return c.e.cli }

// Kubectl runs kubectl with the CLI's kind-only kubeconfig and --context, as
// a user following the CLI's hints would. It returns the combined output.
func (c *CLI) Kubectl(dir string, args ...string) (string, error) {
	c.t.Helper()
	full := append([]string{"--kubeconfig", c.Kubeconfig, "--context", c.e.Context}, args...)
	cmd := exec.Command("kubectl", full...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	c.t.Logf("$ kubectl %s\n%s", strings.Join(args, " "), out)
	return string(out), err
}

// cliForwarding is kubectl port-forward's line for the local port it chose.
var cliForwarding = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+) ->`)

// PortForward runs kubectl port-forward to port of target ("svc/<name>" or
// "pod/<name>") in ns, with the CLI's kind-only kubeconfig, for the rest of
// the test. It returns http://127.0.0.1:<local port>.
func (c *CLI) PortForward(ns, target string, port int) string {
	c.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", c.Kubeconfig, "--context", c.e.Context,
		"-n", ns, "port-forward", "--address", "127.0.0.1", target, fmt.Sprintf("0:%d", port))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		c.t.Fatalf("port-forward: %v", err)
	}
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		c.t.Fatalf("start kubectl port-forward: %v", err)
	}
	done := make(chan struct{})
	c.t.Cleanup(func() {
		cancel()
		<-done
	})
	found := make(chan string, 1)
	go func() {
		defer close(done)
		sc := bufio.NewScanner(stdout)
		sent := false
		for sc.Scan() {
			if m := cliForwarding.FindStringSubmatch(sc.Text()); m != nil && !sent {
				found <- m[1]
				sent = true
			}
		}
		_ = cmd.Wait()
	}()
	select {
	case p := <-found:
		return "http://127.0.0.1:" + p
	case <-done:
		c.t.Fatalf("kubectl port-forward %s -n %s exited: %s", target, ns, stderr.String())
	case <-time.After(30 * time.Second):
		c.t.Fatalf("kubectl port-forward %s -n %s: no local port after 30s: %s", target, ns, stderr.String())
	}
	return ""
}

// BuildCLI builds ./cmd/kardinal from this checkout with ldflags into a
// temporary directory and returns the binary's path. VCS stamping is off, so
// the version comes from ldflags as in a release build.
func BuildCLI(t *testing.T, ldflags string) string {
	t.Helper()
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("BuildCLI: no go.mod above the test directory")
		}
		root = parent
	}
	bin := filepath.Join(t.TempDir(), "kardinal")
	cmd := exec.Command("go", "build", "-buildvcs=false", "-ldflags", ldflags, "-o", bin, "./cmd/kardinal")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/kardinal: %v\n%s", err, out)
	}
	return bin
}

// cellGap separates table columns: tabwriter pads with at least three
// spaces, and a cell holds at most single spaces ("podinfo [PAUSED]").
var cellGap = regexp.MustCompile(`\s{2,}`)

// ParseTable parses the first table in out (a header line, then rows until a
// blank line) into one map per row, keyed by column header.
func ParseTable(out string) []map[string]string {
	var header []string
	var rows []map[string]string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, " ")
		if line == "" {
			if header != nil {
				break
			}
			continue
		}
		cells := cellGap.Split(strings.TrimSpace(line), -1)
		if header == nil {
			header = cells
			continue
		}
		row := map[string]string{}
		for i, h := range header {
			if i < len(cells) {
				row[h] = cells[i]
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// TableRow returns the row of ParseTable(out) whose column col is value, or
// nil.
func TableRow(out, col, value string) map[string]string {
	for _, r := range ParseTable(out) {
		if r[col] == value {
			return r
		}
	}
	return nil
}
