// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kardinal-promoter/kardinal-promoter/test/e2e/framework/gitserver"
)

// PRAuthor returns the login of the account that opened PR number on repo:
// the account whose token the controller's SCM provider used. Forgejo and
// Gitea only.
func (e *Env) PRAuthor(t *testing.T, repo gitserver.Repo, number int) string {
	t.Helper()
	if k := e.Git.Kind(); k != "forgejo" && k != "gitea" {
		t.Fatalf("PRAuthor: git server %s is not Forgejo or Gitea", k)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/pulls/%d",
		strings.TrimRight(os.Getenv(gitserver.EnvAPI), "/"), repo.Owner, repo.Name, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "token "+os.Getenv(gitserver.EnvToken))
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get PR #%d: %v", number, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var pr struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get PR #%d: HTTP %d", number, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		t.Fatalf("decode PR #%d: %v", number, err)
	}
	return pr.User.Login
}
