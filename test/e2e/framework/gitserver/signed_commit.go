// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

package gitserver

import (
	"bytes"
	"context"
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
	token, err := f.CreateUser(ctx, name, []string{"write:repository", "write:user"})
	if err != nil {
		return "", fmt.Errorf("create user %s: %w", name, err)
	}
	if err := f.AddCollaborator(ctx, r, name); err != nil {
		return "", fmt.Errorf("add %s to %s: %w", name, r.Name, err)
	}
	entity, err := openpgp.NewEntity(name, "", email, nil)
	if err != nil {
		return "", err
	}
	var pub bytes.Buffer
	w, err := armor.Encode(&pub, openpgp.PublicKeyType, nil)
	if err != nil {
		return "", err
	}
	if err := entity.Serialize(w); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	as := f.client
	as.headers = map[string]string{"Authorization": "token " + token}
	if err := as.do(ctx, http.MethodPost, "/api/v1/user/gpg_keys", map[string]string{"armored_public_key": pub.String()}, nil); err != nil {
		return "", fmt.Errorf("register %s's GPG key: %w", name, err)
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
