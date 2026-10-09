// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package invariants

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework"
)

// Benign error-level controller log lines: each is an expected outcome
// under the faults the scale tests inject, not a defect. A line matches when
// its message, error and logger fields together match one pattern. Add one
// only with the reason in a comment.
var Benign = []*regexp.Regexp{
	// client-go's leader election: two replicas race to create the Lease
	// at startup; the loser retries and follows.
	regexp.MustCompile(`Error initially creating lease lock.*already exists`),
	// Leader election losing the Lease when the test kills the leader or
	// throttles the API server; the process exits and the other replica leads.
	regexp.MustCompile(`(?i)leader election lost|failed to renew lease|error retrieving resource lock`),
	// Optimistic concurrency: two writers of one object; controller-runtime
	// requeues the reconcile, which reads the new version.
	regexp.MustCompile(`the object has been modified; please apply your changes to the latest version`),
	// client-go's event recorder: an Event for an object in a namespace
	// being deleted (tests delete their namespaces) is refused and dropped.
	regexp.MustCompile(`Server rejected event.*unable to create new content in namespace .* because it is being terminated`),
	regexp.MustCompile(`Server rejected event.*namespaces "[^"]*" not found`),
}

// Line is one classified controller log line.
type Line struct {
	Pod     string `json:"pod"`
	Level   string `json:"level"`
	Logger  string `json:"logger,omitempty"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

// Block is a multi-line report the Go runtime prints: a data race or a
// panic, with its first lines.
type Block struct {
	Pod   string   `json:"pod"`
	Kind  string   `json:"kind"`
	Lines []string `json:"lines"`
}

// LogSummary is what Collector saw.
type LogSummary struct {
	Pods      []string `json:"pods"`
	Lines     int      `json:"lines"`
	Errors    int      `json:"errors"`
	Benign    int      `json:"benignErrors"`
	Warnings  int      `json:"warnings"`
	Races     []Block  `json:"races,omitempty"`
	Panics    []Block  `json:"panics,omitempty"`
	KroPanics []Block  `json:"kroPanics,omitempty"`
	// Unexpected groups the error lines no Benign pattern matches, by
	// message, with a count and one example.
	Unexpected []LogGroup `json:"unexpected,omitempty"`
	// StreamErrors are log streams that broke or could not start: their
	// lines were not checked.
	StreamErrors []string `json:"streamErrors,omitempty"`
	// ReconcileErrors groups controller-runtime "Reconciler error" lines by
	// controller and error, benign or not.
	ReconcileErrors []LogGroup `json:"reconcileErrors,omitempty"`
}

// LogGroup is one message and how often it appeared.
type LogGroup struct {
	Key     string `json:"key"`
	Count   int    `json:"count"`
	Example Line   `json:"example"`
}

// Collector streams the logs of every controller Pod (and the kro
// controller's, for panics) from when it started, including Pods created
// later (a killed leader's replacement), into dir and classifies each line.
type Collector struct {
	e      *framework.Env
	dir    string
	start  time.Time
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu         sync.Mutex
	seen       map[string]bool // pod UID/restart count streamed
	pods       []string
	sum        LogSummary
	unexpected map[string]*LogGroup
	reconcile  map[string]*LogGroup
}

// Collect starts a Collector writing raw logs under dir. Stop it with Stop.
func Collect(t *testing.T, e *framework.Env, dir string) *Collector {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("log dir: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Collector{e: e, dir: dir, start: time.Now().Add(-time.Second), cancel: cancel,
		seen: map[string]bool{}, unexpected: map[string]*LogGroup{}, reconcile: map[string]*LogGroup{}}
	c.wg.Add(1)
	go c.watch(ctx)
	t.Cleanup(c.Stop)
	return c
}

var podSets = []struct {
	ns, selector string
	kro          bool
}{
	{framework.ControllerNamespace, "app.kubernetes.io/name=" + framework.ControllerName, false},
	{"kro-system", "app.kubernetes.io/name=kro", true},
}

// watch lists the Pods every 2s and streams each new container instance.
func (c *Collector) watch(ctx context.Context) {
	defer c.wg.Done()
	for {
		for _, set := range podSets {
			pods, err := c.e.Kube.CoreV1().Pods(set.ns).List(ctx, metav1.ListOptions{LabelSelector: set.selector})
			if err != nil {
				continue
			}
			for i := range pods.Items {
				p := &pods.Items[i]
				for _, cs := range p.Status.ContainerStatuses {
					if cs.State.Running == nil && cs.State.Terminated == nil {
						continue
					}
					key := fmt.Sprintf("%s/%s/%d", p.UID, cs.Name, cs.RestartCount)
					c.mu.Lock()
					fresh := !c.seen[key]
					c.seen[key] = true
					if fresh {
						c.pods = append(c.pods, fmt.Sprintf("%s/%s#%d", p.Name, cs.Name, cs.RestartCount))
					}
					c.mu.Unlock()
					if fresh {
						c.wg.Add(1)
						go c.stream(ctx, set.ns, p.Name, cs.Name, cs.RestartCount, set.kro)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *Collector) stream(ctx context.Context, ns, pod, container string, restart int32, kro bool) {
	defer c.wg.Done()
	since := metav1.NewTime(c.start)
	var rc io.ReadCloser
	var err error
	// The API server refuses a log stream now and then under load; a Pod
	// deleted meanwhile has no logs left to read.
	for try := 0; try < 5; try++ {
		rc, err = c.e.Kube.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{
			Container: container, Follow: true, SinceTime: &since,
		}).Stream(ctx)
		if err == nil || apierrors.IsNotFound(err) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(try+1) * time.Second):
		}
	}
	switch {
	case err != nil && (apierrors.IsNotFound(err) || ctx.Err() != nil):
		return
	case err != nil:
		c.streamError(fmt.Sprintf("stream the logs of %s/%s (%s): %v", ns, pod, container, err))
		return
	}
	defer func() { _ = rc.Close() }()
	name := fmt.Sprintf("%s.%s.%s.%d.log", ns, pod, container, restart)
	f, err := os.Create(filepath.Join(c.dir, name))
	if err != nil {
		c.streamError(fmt.Sprintf("write the logs of %s/%s: %v", ns, pod, err))
		return
	}
	defer func() { _ = f.Close() }()
	if err := c.scan(io.TeeReader(rc, f), pod, kro); err != nil && ctx.Err() == nil &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, io.ErrUnexpectedEOF) {
		c.streamError(fmt.Sprintf("read the logs of %s/%s (%s): %v", ns, pod, container, err))
	}
}

// streamError records a log stream that broke: the lines after it were not
// checked.
func (c *Collector) streamError(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sum.StreamErrors = append(c.sum.StreamErrors, msg)
}

// The Go runtime's reports: a race starts with "WARNING: DATA RACE" and ends
// at a line of '='; a panic or fatal error runs to the end of the log.
var (
	raceStart  = regexp.MustCompile(`^WARNING: DATA RACE`)
	raceEnd    = regexp.MustCompile(`^={10,}`)
	panicStart = regexp.MustCompile(`^(panic: |fatal error: |runtime: out of memory)`)
)

const blockLines = 80

func (c *Collector) scan(r io.Reader, pod string, kro bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var block *Block
	for sc.Scan() {
		text := sc.Text()
		if block != nil {
			if len(block.Lines) < blockLines {
				block.Lines = append(block.Lines, text)
			}
			if block.Kind == "race" && raceEnd.MatchString(text) {
				c.addBlock(*block, kro)
				block = nil
			}
			continue
		}
		switch {
		case raceStart.MatchString(text):
			block = &Block{Pod: pod, Kind: "race", Lines: []string{text}}
			continue
		case panicStart.MatchString(text):
			block = &Block{Pod: pod, Kind: "panic", Lines: []string{text}}
			continue
		}
		if kro {
			continue
		}
		c.line(pod, text)
	}
	if block != nil {
		c.addBlock(*block, kro)
	}
	return sc.Err()
}

func (c *Collector) addBlock(b Block, kro bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case kro && b.Kind == "panic":
		c.sum.KroPanics = append(c.sum.KroPanics, b)
	case kro:
		// kro is built without -race.
	case b.Kind == "race":
		c.sum.Races = append(c.sum.Races, b)
	default:
		c.sum.Panics = append(c.sum.Panics, b)
	}
}

// line classifies one controller log line: zerolog's (level, message,
// error) and controller-runtime's zap JSON (level, msg, error, logger,
// controller).
func (c *Collector) line(pod, text string) {
	var raw map[string]interface{}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sum.Lines++
	if !strings.HasPrefix(text, "{") || json.Unmarshal([]byte(text), &raw) != nil {
		return
	}
	str := func(k string) string { s, _ := raw[k].(string); return s }
	l := Line{Pod: pod, Level: str("level"), Logger: str("logger"), Message: str("message"), Error: str("error")}
	if l.Message == "" {
		l.Message = str("msg")
	}
	switch l.Level {
	case "warn", "warning":
		c.sum.Warnings++
		return
	case "error", "fatal", "panic", "dpanic":
	default:
		return
	}
	c.sum.Errors++
	if strings.Contains(strings.ToLower(l.Message), "observed a panic") {
		// controller-runtime recovered a reconciler panic and logged it.
		c.sum.Panics = append(c.sum.Panics, Block{Pod: pod, Kind: "panic", Lines: []string{text}})
		return
	}
	if l.Message == "Reconciler error" {
		key := str("controller") + ": " + normalize(l.Error)
		g := c.reconcile[key]
		if g == nil {
			g = &LogGroup{Key: key, Example: l}
			c.reconcile[key] = g
		}
		g.Count++
	}
	all := l.Logger + " " + l.Message + " " + l.Error
	for _, re := range Benign {
		if re.MatchString(all) {
			c.sum.Benign++
			return
		}
	}
	key := l.Logger + ": " + l.Message + ": " + normalize(l.Error)
	g := c.unexpected[key]
	if g == nil {
		g = &LogGroup{Key: key, Example: l}
		c.unexpected[key] = g
	}
	g.Count++
}

// normalize drops what differs between two occurrences of one error:
// numbers, quoted names and hashes.
var (
	quoted = regexp.MustCompile(`"[^"]*"`)
	digits = regexp.MustCompile(`[0-9a-f]{7,}|[0-9]+`)
)

func normalize(s string) string {
	if len(s) > 300 {
		s = s[:300]
	}
	return digits.ReplaceAllString(quoted.ReplaceAllString(s, `"…"`), "N")
}

// Stop ends the streams and waits for them.
func (c *Collector) Stop() {
	c.cancel()
	c.wg.Wait()
}

// Summary returns what the Collector saw so far. It is safe while streaming.
func (c *Collector) Summary() LogSummary {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.sum
	s.Pods = append([]string(nil), c.pods...)
	s.Races = append([]Block(nil), c.sum.Races...)
	s.Panics = append([]Block(nil), c.sum.Panics...)
	s.KroPanics = append([]Block(nil), c.sum.KroPanics...)
	s.StreamErrors = append([]string(nil), c.sum.StreamErrors...)
	s.Unexpected = groups(c.unexpected)
	s.ReconcileErrors = groups(c.reconcile)
	return s
}

func groups(m map[string]*LogGroup) []LogGroup {
	out := make([]LogGroup, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}
