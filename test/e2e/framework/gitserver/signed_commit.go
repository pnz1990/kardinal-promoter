// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/go-git/go-billy/v5/memfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

// signingUser makes a Forgejo/Gitea user (name, email name@example.com)
// with a GPG key registered on the server and write access to r, and
// returns the user's token, client and key.
func signingUser(ctx context.Context, f *forgejo, r Repo, name string) (string, client, *openpgp.Entity, error) {
	token, err := f.CreateUser(ctx, name, []string{"write:repository", "write:user"})
	if err != nil {
		return "", client{}, nil, fmt.Errorf("create user %s: %w", name, err)
	}
	if err := f.AddCollaborator(ctx, r, name); err != nil {
		return "", client{}, nil, fmt.Errorf("add %s to %s: %w", name, r.Name, err)
	}
	entity, err := openpgp.NewEntity(name, "", name+"@example.com", nil)
	if err != nil {
		return "", client{}, nil, err
	}
	var pub bytes.Buffer
	w, err := armor.Encode(&pub, openpgp.PublicKeyType, nil)
	if err != nil {
		return "", client{}, nil, err
	}
	if err := entity.Serialize(w); err != nil {
		return "", client{}, nil, err
	}
	if err := w.Close(); err != nil {
		return "", client{}, nil, err
	}
	as := f.client
	as.headers = map[string]string{"Authorization": "token " + token}
	if err := as.do(ctx, http.MethodPost, "/api/v1/user/gpg_keys", map[string]string{"armored_public_key": pub.String()}, nil); err != nil {
		return "", client{}, nil, fmt.Errorf("register %s's GPG key: %w", name, err)
	}
	return token, as, entity, nil
}

// InstanceCommitAs makes the user as signingUser does and commits
// path=content to r's branch through the contents API as that user. The
// e2e servers sign API commits with their instance key for users with a
// public key (repository.signing CRUD_ACTIONS = pubkey): the commit is
// instance-signed. It returns the commit SHA.
func InstanceCommitAs(ctx context.Context, s Server, r Repo, name, path string, content []byte) (string, error) {
	f, err := asForgejo(s)
	if err != nil {
		return "", err
	}
	_, as, _, err := signingUser(ctx, f, r, name)
	if err != nil {
		return "", err
	}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	err = as.do(ctx, http.MethodPost, f.repoPath(r)+"/contents/"+path, map[string]string{
		"content": base64.StdEncoding.EncodeToString(content), "message": "API commit by " + name, "branch": r.Branch,
	}, &out)
	return out.Commit.SHA, err
}

// CommitAs makes a Forgejo/Gitea user (name, email name@example.com) with a
// GPG key registered on the server and write access to r, and pushes a
// commit of path=content to r's branch, signed with that key when sign is
// set (as a person signs), unsigned otherwise. It returns the commit SHA.
func CommitAs(ctx context.Context, s Server, r Repo, name, path string, content []byte, sign bool) (string, error) {
	f, err := asForgejo(s)
	if err != nil {
		return "", err
	}
	email := name + "@example.com"
	token, _, entity, err := signingUser(ctx, f, r, name)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/%s/%s.git", f.api, r.Owner, r.Name)
	auth := &githttp.BasicAuth{Username: name, Password: token}
	fs := memfs.New()
	repo, err := git.CloneContext(ctx, memory.NewStorage(), fs, &git.CloneOptions{URL: url, Auth: auth})
	if err != nil {
		return "", fmt.Errorf("clone %s: %w", url, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	file, err := fs.Create(path)
	if err != nil {
		return "", err
	}
	if _, err := file.Write(content); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if _, err := wt.Add(path); err != nil {
		return "", err
	}
	sig := &object.Signature{Name: name, Email: email, When: time.Now()}
	opts := &git.CommitOptions{Author: sig, Committer: sig}
	if sign {
		opts.SignKey = entity
	}
	h, err := wt.Commit("commit by "+name, opts)
	if err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	if err := repo.PushContext(ctx, &git.PushOptions{Auth: auth}); err != nil {
		return "", fmt.Errorf("push: %w", err)
	}
	return h.String(), nil
}
