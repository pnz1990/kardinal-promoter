// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0
//
// Package scm contains the SCMProvider interface, its GitHub, GitLab,
// Forgejo/Gitea, Bitbucket Cloud and Azure DevOps implementations, the go-git
// client used by the promotion steps, and webhook parsing. Providers handle the
// PR lifecycle: open, label, comment, read status and reviews, close.
package scm
