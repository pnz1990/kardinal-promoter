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
	reasonCredentialFound  = "CredentialFound"
)

// gitStepOps names what each step that uses the git token does with it.
var gitStepOps = map[string]string{"git-clone": "clone", "git-push": "push"}

// gitCredential is the token for Pipeline spec.git and, when an HTTP(S)
// remote has none, why.
type gitCredential struct {
	token string
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
	// A token pasted with a trailing newline breaks every git and SCM call
	// (C06-scm-health-27).
	cred.token = strings.TrimSpace(string(secret.Data["token"]))
	if cred.token == "" && needsToken {
		cred.reason = reasonSecretHasNoToken
	}
	return cred
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

// isGitAuthError reports whether err is the remote refusing a clone or push
// for lack of credentials: go-git's transport.ErrAuthenticationRequired (HTTP
// 401) or ErrAuthorizationFailed (403). The git client formats its errors
// with %s, so the text is matched as well.
func isGitAuthError(err error) bool {
	if errors.Is(err, transport.ErrAuthenticationRequired) || errors.Is(err, transport.ErrAuthorizationFailed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, transport.ErrAuthenticationRequired.Error()) ||
		strings.Contains(msg, transport.ErrAuthorizationFailed.Error())
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
