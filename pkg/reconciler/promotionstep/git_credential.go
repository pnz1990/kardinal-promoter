// Copyright 2026 The kardinal-promoter Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package promotionstep

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/rs/zerolog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/kubeevent"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

// ConditionGitCredentialMissing is True on a PromotionStep whose git-clone or
// git-push step the remote refused for lack of credentials (HTTP 401 or 403)
// while the Pipeline gave git none for its HTTP(S) remote. The Warning Event of the same reason is emitted when it
// turns True, so a step that keeps retrying emits one Event. It is False
// again once git has a token.
const ConditionGitCredentialMissing = "GitCredentialMissing"

// Reasons of ConditionGitCredentialMissing.
const (
	reasonSecretRefNotSet  = "SecretRefNotSet"
	reasonSecretNotFound   = "SecretNotFound"
	reasonSecretHasNoToken = "SecretHasNoToken"
	reasonSecretUnreadable = "SecretUnreadable"
	// reasonGitHubAppToken: the git Secret holds GitHub App credentials and
	// no installation token could be minted with them.
	reasonGitHubAppToken  = "GitHubAppTokenFailed"
	reasonCredentialFound = "CredentialFound"
)

// gitStepOps names what each step that uses the git token does with it.
var gitStepOps = map[string]string{"git-clone": "clone", "git-push": "push"}

// gitCredential is the token (or ssh key) for Pipeline spec.git and, when
// an HTTP(S) remote has none, why.
type gitCredential struct {
	token string
	// sshKey and knownHosts are the sshPrivateKey and knownHosts keys of the
	// Secret, for an ssh remote.
	sshKey, knownHosts []byte
	// reason is a ConditionGitCredentialMissing reason, or "" when git has a
	// token or needs none (ssh and file remotes, a password in the URL).
	reason string
	// secret is <namespace>/<name> of spec.git.secretRef.
	secret string
	// readErr is why the Secret could not be read (reasonSecretUnreadable).
	readErr error
}

// resolveGitCredential reads the token from the Secret that
// Pipeline spec.git.secretRef names, in the Pipeline's namespace
// (unsupportedConfig has already refused any other secretRef.namespace,
// C03-promotionstep-18). It never returns the token in an error or a note.
func (r *Reconciler) resolveGitCredential(ctx context.Context, log zerolog.Logger,
	pipeline *v1alpha1.Pipeline) gitCredential {
	needsToken := remoteNeedsToken(pipeline.Spec.Git.URL)
	ref := pipeline.Spec.Git.SecretRef
	if ref == nil || ref.Name == "" {
		if needsToken {
			return gitCredential{reason: reasonSecretRefNotSet}
		}
		return gitCredential{}
	}
	cred := gitCredential{secret: pipeline.Namespace + "/" + ref.Name}
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: pipeline.Namespace}, &secret); err != nil {
		log.Warn().Err(err).Str("secret", cred.secret).Msg("failed to read git secret — git operations may fail")
		if needsToken {
			cred.reason = reasonSecretUnreadable
			cred.readErr = err
			if apierrors.IsNotFound(err) {
				cred.reason = reasonSecretNotFound
			}
		}
		return cred
	}
	cred.sshKey, cred.knownHosts = secret.Data[secretKeySSHPrivateKey], secret.Data[secretKeyKnownHosts]
	// A Secret with GitHub App credentials gives git an installation token
	// of the App, minted (and cached until shortly before it expires) by
	// GitHubAppTokens. Only an HTTP(S) remote takes a token.
	app, isApp, appErr := scm.GitHubAppCredentialsFromData(secret.Data)
	if needsToken && (isApp || appErr != nil) {
		if appErr == nil {
			cred.token, appErr = r.githubAppTokens().Token(ctx, app)
		}
		if appErr != nil {
			log.Warn().Err(appErr).Str("secret", cred.secret).Msg("cannot get a GitHub App installation token for git")
			cred.reason, cred.readErr = reasonGitHubAppToken, appErr
		}
		return cred
	}
	// A token pasted with a trailing newline breaks every git and SCM call
	// (C06-scm-health-27).
	cred.token = strings.TrimSpace(string(secret.Data["token"]))
	if cred.token == "" && needsToken {
		cred.reason = reasonSecretHasNoToken
	}
	return cred
}

// The keys of the git Secret that authenticate an ssh remote.
const (
	secretKeySSHPrivateKey = "sshPrivateKey"
	secretKeyKnownHosts    = "knownHosts"
)

// githubAppTokens returns GitHubAppTokens, or a cache for github.com when it
// is nil (tests).
func (r *Reconciler) githubAppTokens() *scm.AppTokenCache {
	r.appTokensOnce.Do(func() {
		if r.GitHubAppTokens == nil {
			r.GitHubAppTokens = &scm.AppTokenCache{}
		}
	})
	return r.GitHubAppTokens
}

// remoteNeedsToken reports whether git needs the Pipeline token for remote:
// an HTTP(S) URL without a password. ssh and file remotes use their own
// transports, and go-git sends a password in the URL itself.
func remoteNeedsToken(remote string) bool {
	u, err := url.Parse(strings.TrimSpace(remote))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	if u.User == nil {
		return true
	}
	_, hasPassword := u.User.Password()
	return !hasPassword
}

// gitAuthText matches go-git's words for a refusal where the git client puts
// them, right after "git clone <url>: " or "git push <remote> <branch>: ".
// The response body comes after them, or after the status of an HTTP error
// other than 401, 403 and 404 ("HTTP 407 Proxy Authentication Required: "),
// so a body that uses the same words does not match.
var gitAuthText = regexp.MustCompile(`git (?:clone|push)(?: \S+){0,2}: (?:` +
	regexp.QuoteMeta(transport.ErrAuthenticationRequired.Error()) + `|` +
	regexp.QuoteMeta(transport.ErrAuthorizationFailed.Error()) + `)`)

// isGitAuthError reports whether err is the remote refusing a clone or push
// for lack of credentials: go-git's transport.ErrAuthenticationRequired (HTTP
// 401) or ErrAuthorizationFailed (403). The git client formats its errors
// with %s, so the text is matched as well (gitAuthText).
func isGitAuthError(err error) bool {
	if errors.Is(err, transport.ErrAuthenticationRequired) || errors.Is(err, transport.ErrAuthorizationFailed) {
		return true
	}
	return gitAuthText.MatchString(err.Error())
}

// note says what git is missing when step, which failed, is a step that uses
// the git token. It is "" for other steps and when git has a token.
func (c gitCredential) note(step string) string {
	op, ok := gitStepOps[step]
	if !ok {
		return ""
	}
	switch c.reason {
	case reasonSecretRefNotSet:
		return fmt.Sprintf("spec.git.secretRef is not set, so the %s has no credentials", op)
	case reasonSecretNotFound:
		return fmt.Sprintf("git Secret %s not found", c.secret)
	case reasonSecretHasNoToken:
		return fmt.Sprintf("git Secret %s has no token key", c.secret)
	case reasonSecretUnreadable:
		return fmt.Sprintf("git Secret %s could not be read: %v", c.secret, c.readErr)
	case reasonGitHubAppToken:
		return fmt.Sprintf("git Secret %s has GitHub App credentials, and no installation token could be minted: %v", c.secret, c.readErr)
	}
	return ""
}

// waitsForSecret reports whether the step should wait for the credential
// with no retry limit: spec.git.secretRef is not set, or its Secret does not
// exist or has no token key, so creating it or setting the secretRef fixes
// the step. A Secret that could not be read (Forbidden, a timeout) is not
// fixed that way, so its step keeps the retry limit.
func (c gitCredential) waitsForSecret() bool {
	switch c.reason {
	case reasonSecretRefNotSet, reasonSecretNotFound, reasonSecretHasNoToken:
		return true
	}
	return false
}

// markGitCredentialMissing sets ConditionGitCredentialMissing to True with
// note and reports whether it was not True before, i.e. whether the Warning
// Event is due once the status is written.
func markGitCredentialMissing(ps *v1alpha1.PromotionStep, reason, note string) bool {
	wasMissing := meta.IsStatusConditionTrue(ps.Status.Conditions, ConditionGitCredentialMissing)
	meta.SetStatusCondition(&ps.Status.Conditions, metav1.Condition{
		Type:               ConditionGitCredentialMissing,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            note,
		ObservedGeneration: ps.Generation,
		LastTransitionTime: metav1.NewTime(time.Now().UTC()),
	})
	return !wasMissing
}

// clearGitCredentialMissing sets ConditionGitCredentialMissing to False when
// it is True and git now has a token, and resets status.gitCredentialRetries,
// so the backoff starts over. The caller writes the status.
func clearGitCredentialMissing(ps *v1alpha1.PromotionStep, cred gitCredential) {
	if cred.reason != "" || !meta.IsStatusConditionTrue(ps.Status.Conditions, ConditionGitCredentialMissing) {
		return
	}
	ps.Status.GitCredentialRetries = 0
	meta.SetStatusCondition(&ps.Status.Conditions, metav1.Condition{
		Type:               ConditionGitCredentialMissing,
		Status:             metav1.ConditionFalse,
		Reason:             reasonCredentialFound,
		Message:            "git has a token",
		ObservedGeneration: ps.Generation,
		LastTransitionTime: metav1.NewTime(time.Now().UTC()),
	})
}

// emitGitCredentialMissing emits the one Warning Event for a step whose git
// credential is missing.
func (r *Reconciler) emitGitCredentialMissing(ps *v1alpha1.PromotionStep, step, note string) {
	kubeevent.Emit(r.Recorder, ps, corev1.EventTypeWarning, ConditionGitCredentialMissing, "Promote",
		fmt.Sprintf("env %s: step %s failed and git has no credentials: %s",
			ps.Spec.Environment, step, note))
}
