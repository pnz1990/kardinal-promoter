// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// EnvSSHBase is the base of the git server's ssh clone URLs as the
// controller reaches it (ssh://git@host:port), and EnvSSHAddr the host:port
// of its ssh server as the test runner reaches it.
const (
	EnvSSHBase = "KARDINAL_E2E_GIT_SSH_BASE"
	EnvSSHAddr = "KARDINAL_E2E_GIT_SSH_ADDR"
)

// SSHKeys is a Server whose test runner, an instance admin, can add an ssh
// key to a user. Forgejo, Gitea and GitLab implement it.
type SSHKeys interface {
	// AddSSHKey adds the authorized_keys line key to user and returns a
	// function that removes it.
	AddSSHKey(ctx context.Context, user, title, key string) (remove func(context.Context) error, err error)
}

var (
	_ SSHKeys = (*forgejo)(nil)
	_ SSHKeys = (*gitlab)(nil)
)

// SSHCloneURL is the ssh URL the controller clones r from.
func SSHCloneURL(r Repo) (string, error) {
	base := strings.TrimRight(os.Getenv(EnvSSHBase), "/")
	if base == "" {
		return "", fmt.Errorf("%s is not set", EnvSSHBase)
	}
	return base + "/" + r.Owner + "/" + r.Name + ".git", nil
}

func (f *forgejo) AddSSHKey(ctx context.Context, user, title, key string) (func(context.Context) error, error) {
	var out struct {
		ID int `json:"id"`
	}
	if err := f.do(ctx, http.MethodPost, "/api/v1/admin/users/"+url.PathEscape(user)+"/keys",
		map[string]interface{}{"title": title, "key": key, "read_only": false}, &out); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		err := f.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v1/admin/users/%s/keys/%d", url.PathEscape(user), out.ID), nil, nil)
		if IsNotFound(err) {
			return nil
		}
		return err
	}, nil
}

func (g *gitlab) AddSSHKey(ctx context.Context, user, title, key string) (func(context.Context) error, error) {
	id, err := g.userID(ctx, user)
	if err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, fmt.Errorf("no GitLab user %s", user)
	}
	var out struct {
		ID int `json:"id"`
	}
	if err := g.do(ctx, http.MethodPost, fmt.Sprintf("/api/v4/users/%d/keys", id),
		map[string]interface{}{"title": title, "key": key}, &out); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error {
		err := g.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v4/users/%d/keys/%d", id, out.ID), nil, nil)
		if IsNotFound(err) {
			return nil
		}
		return err
	}, nil
}
