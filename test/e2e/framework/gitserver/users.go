// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Users is a Server whose test runner is an instance admin and can make
// users with tokens of chosen scopes: a token kardinal is not documented to
// work with, or another identity to rotate the controller's token to.
type Users interface {
	// CreateUser creates user name with a token limited to scopes and
	// returns the token. DeleteUser removes the user and its tokens.
	CreateUser(ctx context.Context, name string, scopes []string) (token string, err error)
	DeleteUser(ctx context.Context, name string) error
	// AddCollaborator gives user write access to r.
	AddCollaborator(ctx context.Context, r Repo, user string) error
}

var (
	_ Users = (*forgejo)(nil)
	_ Users = (*gitlab)(nil)
)

func randomPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "Aa1-" + hex.EncodeToString(b), nil
}

func basic(user, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + password))
}

// CreateUser creates the user as admin, then the token as the user: Forgejo
// and Gitea create tokens only with the owner's basic auth.
func (f *forgejo) CreateUser(ctx context.Context, name string, scopes []string) (string, error) {
	password, err := randomPassword()
	if err != nil {
		return "", err
	}
	if err := f.do(ctx, http.MethodPost, "/api/v1/admin/users", map[string]interface{}{
		"username": name, "email": name + "@example.com", "password": password,
		"must_change_password": false, "visibility": "public",
	}, nil); err != nil {
		return "", err
	}
	as := f.client
	as.headers = map[string]string{"Authorization": "Basic " + basic(name, password)}
	var tok struct {
		SHA1 string `json:"sha1"`
	}
	err = as.do(ctx, http.MethodPost, "/api/v1/users/"+url.PathEscape(name)+"/tokens", map[string]interface{}{
		"name": fmt.Sprintf("e2e-%d", time.Now().UnixNano()), "scopes": scopes,
	}, &tok)
	return tok.SHA1, err
}

func (f *forgejo) DeleteUser(ctx context.Context, name string) error {
	err := f.do(ctx, http.MethodDelete, "/api/v1/admin/users/"+url.PathEscape(name)+"?purge=true", nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

func (f *forgejo) AddCollaborator(ctx context.Context, r Repo, user string) error {
	return f.do(ctx, http.MethodPut, f.repoPath(r)+"/collaborators/"+url.PathEscape(user),
		map[string]string{"permission": "write"}, nil)
}

// CreateUser creates the user and its token as admin.
func (g *gitlab) CreateUser(ctx context.Context, name string, scopes []string) (string, error) {
	password, err := randomPassword()
	if err != nil {
		return "", err
	}
	var u struct {
		ID int `json:"id"`
	}
	if err := g.do(ctx, http.MethodPost, "/api/v4/users", map[string]interface{}{
		"username": name, "name": name, "email": name + "@example.com", "password": password,
		"skip_confirmation": true,
	}, &u); err != nil {
		return "", err
	}
	var tok struct {
		Token string `json:"token"`
	}
	err = g.do(ctx, http.MethodPost, fmt.Sprintf("/api/v4/users/%d/personal_access_tokens", u.ID), map[string]interface{}{
		"name": fmt.Sprintf("e2e-%d", time.Now().UnixNano()), "scopes": scopes,
		"expires_at": time.Now().AddDate(0, 0, 2).Format("2006-01-02"),
	}, &tok)
	return tok.Token, err
}

func (g *gitlab) userID(ctx context.Context, name string) (int, error) {
	var users []struct {
		ID int `json:"id"`
	}
	if err := g.do(ctx, http.MethodGet, "/api/v4/users?username="+url.QueryEscape(name), nil, &users); err != nil {
		return 0, err
	}
	if len(users) == 0 {
		return 0, nil
	}
	return users[0].ID, nil
}

func (g *gitlab) DeleteUser(ctx context.Context, name string) error {
	id, err := g.userID(ctx, name)
	if err != nil || id == 0 {
		return err
	}
	err = g.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v4/users/%d?hard_delete=true", id), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// AddCollaborator makes user a Developer of the project. Developers can push
// to unprotected branches and open MRs.
func (g *gitlab) AddCollaborator(ctx context.Context, r Repo, user string) error {
	id, err := g.userID(ctx, user)
	if err != nil {
		return err
	}
	if id == 0 {
		return fmt.Errorf("no GitLab user %s", user)
	}
	return g.do(ctx, http.MethodPost, g.projectPath(r)+"/members",
		map[string]interface{}{"user_id": id, "access_level": 30}, nil)
}
