// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/cmd/kardinal/internal/style"
)

// TestMCP_ConcurrentNamespaces: two agents' calls at once, one per
// namespace, on one server, with the client the commands build from the
// persistent flags (the namespace a tool passes). Each call reads its own
// namespace only: what it returns, and what its error hints list, never come
// from the other call's namespace; and under -race, no call reads the flag
// variables while another call's command has them set (#2229: the error
// rewrite ran outside the lock).
//
// Covers MCP-CONCURRENT-01.
func TestMCP_ConcurrentNamespaces(t *testing.T) {
	pipe := func(ns, name string) *v1alpha1.Pipeline {
		return &v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: v1alpha1.PipelineSpec{Environments: []v1alpha1.EnvironmentSpec{{Name: "test"}}}}
	}
	slow := &interceptor.Funcs{List: func(ctx context.Context, c sigs_client.WithWatch, list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
		time.Sleep(2 * time.Millisecond)
		return c.List(ctx, list, opts...)
	}}
	cs, c := mcpClient(t, mcpOptions{allowWrite: true}, slow, pipe("team-a", "a-one"), pipe("team-b", "b-secret"), pipe("default", "d-one"))
	// As the real client: the namespace is the persistent flag's, else the
	// kubeconfig's.
	setForTest(t, &newClient, func() (sigs_client.Client, string, error) {
		if globalNamespace != "" {
			return c, globalNamespace, nil
		}
		return c, "default", nil
	})
	var wg sync.WaitGroup
	errs := make(chan string, 400)
	for i := range 100 {
		for _, ns := range []string{"team-a", "team-b"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				tools := []string{"status", "history", "pause", "resume", "explain"}
				tool, args := tools[i%len(tools)], map[string]any{"namespace": ns, "pipeline": "ghost"}
				res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
				if err != nil {
					errs <- err.Error()
					return
				}
				text := res.Content[0].(*mcp.TextContent).Text
				other := map[string]string{"team-a": "b-secret", "team-b": "a-one"}[ns]
				if strings.Contains(text, other) || strings.Contains(text, "d-one") {
					errs <- ns + " " + tool + ": " + text
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	var bad []string
	for e := range errs {
		bad = append(bad, e)
	}
	require.Empty(t, bad, "%d calls saw another namespace; the first: %v", len(bad), first(bad))
	assert.Empty(t, globalNamespace)
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// TestMCP_CallsWriteNoGlobal (#2230): a tool call writes no package
// variable: the flags, the output, the style and the warnings stay what the
// server started with, the call's namespace reaching the command as an
// argument and its client label through the context.
//
// Covers MCP-CONCURRENT-01.
func TestMCP_CallsWriteNoGlobal(t *testing.T) {
	cs, _ := mcpClient(t, mcpOptions{allowWrite: true}, nil, mcpFixture()...)
	ns, kc, kctx, out, s, e := globalNamespace, globalKubeconfig, globalContext, globalOutput, sty, errOut
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"status", map[string]any{"pipeline": "app", "namespace": "team-b"}},
		{"why", map[string]any{"pipeline": "ghost", "namespace": "team-b"}},
		{"pause", map[string]any{"pipeline": "app", "namespace": "default"}},
	} {
		call(t, cs, tc.tool, tc.args)
		assert.Equal(t, ns, globalNamespace, tc.tool)
		assert.Equal(t, kc, globalKubeconfig, tc.tool)
		assert.Equal(t, kctx, globalContext, tc.tool)
		assert.Equal(t, out, globalOutput, tc.tool)
		assert.Equal(t, s, sty, tc.tool)
		assert.Equal(t, e, errOut, tc.tool)
	}
}

// TestHumanErrorIn: a not-found Pipeline's hint lists the namespace the
// command ran in, not the flags' default.
func TestHumanErrorIn(t *testing.T) {
	setForTest(t, &globalNamespace, "default")
	c := fake.NewClientBuilder().WithScheme(rootScheme).WithObjects(
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "a-one", Namespace: "team-a"}},
		&v1alpha1.Pipeline{ObjectMeta: metav1.ObjectMeta{Name: "d-one", Namespace: "default"}}).Build()
	notFound := apierrors.NewNotFound(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "pipelines"}, "ghost")
	got := humanErrorIn(notFound, c, "team-a").Error()
	assert.Contains(t, got, `pipeline "ghost" not found in namespace "team-a"`)
	assert.Contains(t, got, "a-one")
	assert.NotContains(t, got, "d-one")
	assert.Equal(t, "boom", humanErrorIn(errors.New("boom"), c, "team-a").Error(), "other errors as HumanError rewrites them")
}

// TestAssertedFor: the stamp's asserted client is the tool call's (ctx),
// unless KARDINAL_ACTOR_CLIENT names one; outside a call it is the
// environment's.
func TestAssertedFor(t *testing.T) {
	env := v1alpha1.AssertedActor{Model: "m"}
	setForTest(t, &assertedOf, func() v1alpha1.AssertedActor { return env })
	ctx := context.WithValue(context.Background(), mcpClientKey{}, "kardinal-mcp/v1 (agent/1)")
	assert.Equal(t, v1alpha1.AssertedActor{Model: "m", Client: "kardinal-mcp/v1 (agent/1)"}, assertedFor(ctx))
	assert.Equal(t, v1alpha1.AssertedActor{Model: "m"}, assertedFor(context.Background()))
	env.Client = "ci-bot"
	assert.Equal(t, "ci-bot", assertedFor(ctx).Client, "the environment's client wins")
}

// lockedBuffer is a bytes.Buffer safe for concurrent writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestMCP_PanicEndsOneCall (#2241 QA): a tool call that panics ends as a
// tool error, its stack in the server's log and not in the result, while
// the calls running next to it finish, and the server goes on serving.
//
// Covers MCP-CONCURRENT-01.
func TestMCP_PanicEndsOneCall(t *testing.T) {
	inBoom := func(opts []sigs_client.ListOption) bool {
		lo := (&sigs_client.ListOptions{}).ApplyOptions(opts)
		return lo.Namespace == "boom"
	}
	funcs := &interceptor.Funcs{
		Get: func(ctx context.Context, c sigs_client.WithWatch, key sigs_client.ObjectKey, obj sigs_client.Object, opts ...sigs_client.GetOption) error {
			if key.Namespace == "boom" {
				panic("boom in one call")
			}
			time.Sleep(time.Millisecond)
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c sigs_client.WithWatch, list sigs_client.ObjectList, opts ...sigs_client.ListOption) error {
			if inBoom(opts) {
				panic("boom in one call")
			}
			time.Sleep(time.Millisecond)
			return c.List(ctx, list, opts...)
		},
	}
	log := &lockedBuffer{}
	cs, _ := mcpClient(t, mcpOptions{log: log}, funcs, mcpFixture()...)
	var wg sync.WaitGroup
	results := make([]*mcp.CallToolResult, 20)
	errs := make([]error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ns := "default"
			if i%4 == 0 {
				ns = "boom"
			}
			results[i], errs[i] = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "status",
				Arguments: map[string]any{"pipeline": "app", "namespace": ns}})
		}()
	}
	wg.Wait()
	for i := range 20 {
		require.NoError(t, errs[i], "call %d: the transport survives", i)
		text := results[i].Content[0].(*mcp.TextContent).Text
		if i%4 == 0 {
			assert.True(t, results[i].IsError, text)
			assert.Contains(t, text, "status: internal error; any change it made before it stopped was not undone")
			assert.Contains(t, text, "before retrying")
			assert.NotContains(t, text, "goroutine", "no stack to the client")
			assert.NotContains(t, text, "boom in one call", "no panic value to the client")
			continue
		}
		assert.False(t, results[i].IsError, "call %d next to the panics: %s", i, text)
		assert.Contains(t, text, `"pipeline":"app"`)
	}
	assert.Contains(t, log.String(), "kardinal mcp: tool status panicked: boom in one call")
	assert.Contains(t, log.String(), "goroutine", "the stack in the log")
	_, body := call(t, cs, "status", map[string]any{"pipeline": "app"})
	assert.Contains(t, fmt.Sprint(body), "app", "the server goes on serving")
}

// TestMCPProcess: what kardinal mcp sets when it starts, for every call:
// JSON output, no colour, warnings discarded, and no .kardinal.yaml.
func TestMCPProcess(t *testing.T) {
	setForTest(t, &globalOutput, "")
	setForTest(t, &errOut, io.Writer(os.Stderr))
	setForTest(t, &ignoreRepoFile, false)
	keepStyle(t)
	mcpProcess()
	assert.Equal(t, "json", globalOutput)
	assert.Equal(t, style.Plain(), sty)
	assert.Equal(t, io.Discard, errOut)
	assert.True(t, ignoreRepoFile)
}

// TestNoticeDefault_Concurrent (#2230): kardinal mcp builds a client per
// call, and the namespace default's notice is said once whatever the
// number of calls at once (go test -race).
func TestNoticeDefault_Concurrent(t *testing.T) {
	setForTest(t, &noticed, map[string]bool{})
	var buf lockedBuffer
	setForTest(t, &errOut, io.Writer(&buf))
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			noticeDefault("namespace", "team-a", "KARDINAL_NAMESPACE")
		}()
	}
	wg.Wait()
	assert.Equal(t, "using namespace team-a (from KARDINAL_NAMESPACE)\n", buf.String())
}
