// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package cmd

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	sigs_client "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
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
