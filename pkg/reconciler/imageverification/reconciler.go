// Copyright 2026 The kardinal-promoter Authors.
// Licensed under the Apache License, Version 2.0

// Package imageverification implements the ImageVerification reconciler:
// it checks the signatures of a Bundle's images in their registry, with
// sigstore-go, and a config Bundle's commit signature through the SCM API,
// and writes the verdict to ImageVerification.status. The Bundle's Graph
// creates the ImageVerification and mirrors its phase onto the root
// PromotionSteps (spec.live.imageVerification); they start only once it is
// Verified (docs/image-verification.md).
package imageverification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/rs/zerolog"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/kardinal-promoter/kardinal-promoter/api/v1alpha1"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/reconciler/objectgone"
	"github.com/kardinal-promoter/kardinal-promoter/pkg/scm"
)

const (
	// DefaultTimeout bounds the wait for a missing signature.
	DefaultTimeout = 10 * time.Minute
	// retryBase and retryMax space the checks that ended without a verdict.
	retryBase = 10 * time.Second
	retryMax  = 2 * time.Minute
)

// Reconciler verifies ImageVerifications.
type Reconciler struct {
	client.Client

	// Registry fetches signatures.
	Registry RegistryClient

	// SCM verifies commits; it must implement scm.CommitVerifier for a
	// policy with commits.requireSigned.
	SCM scm.SCMProvider

	// PublicGoodRoot returns the Sigstore public-good trusted root (for
	// keyless authorities without a trustedRootRef, and keys that require
	// a transparency log entry). Nil: such authorities fail.
	PublicGoodRoot func(ctx context.Context) (root.TrustedMaterial, error)

	// NowFn returns the current time; nil means time.Now.
	NowFn func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.NowFn != nil {
		return r.NowFn()
	}
	return time.Now()
}

var resource = v1alpha1.GroupVersion.WithResource("imageverifications").GroupResource()

// Reconcile checks one ImageVerification. It is idempotent: an image or
// commit already verified is not checked again, and a terminal phase never
// changes.
//
//	no startedAt       → record startedAt and the deadline
//	each image         → verified (kept), a signature that does not verify
//	                     (Failed), no signature yet or registry error (retry
//	                     until the deadline)
//	the commit         → verified, unsigned or unverified (Failed), SCM error (retry)
//	all verified       → Verified
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return objectgone.Reconcile(ctx, req, resource, r.reconcile)
}

func (r *Reconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := zerolog.Ctx(ctx).With().Str("imageverification", req.Name).Str("namespace", req.Namespace).Logger()
	var iv v1alpha1.ImageVerification
	if err := r.Get(ctx, req.NamespacedName, &iv); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get imageverification %s: %w", req.Name, err)
	}
	if terminal(iv.Status.Phase) || !iv.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	base := iv.DeepCopy()
	now := metav1.NewTime(r.now())
	if iv.Status.StartedAt == nil {
		timeout, err := policyTimeout(iv.Spec.Policy.Timeout)
		if err != nil {
			return r.fail(ctx, base, &iv, err.Error())
		}
		deadline := metav1.NewTime(now.Add(timeout))
		iv.Status.StartedAt, iv.Status.Deadline, iv.Status.Phase = &now, &deadline, v1alpha1.ImageVerificationPending
		iv.Status.Message = "checking signatures"
	}
	iv.Status.LastCheckedAt = &now

	waiting, failed := r.checkImages(ctx, log, &iv)
	if failed == "" {
		w, f := r.checkCommit(ctx, &iv)
		if f != "" {
			failed = f
		}
		if w != "" && waiting == "" {
			waiting = w
		}
	}
	switch {
	case failed != "":
		log.Info().Str("reason", failed).Msg("image verification failed")
		return r.fail(ctx, base, &iv, failed)
	case waiting == "":
		iv.Status.Phase, iv.Status.Message, iv.Status.Attempts = v1alpha1.ImageVerificationVerified, verifiedMessage(&iv), 0
		log.Info().Msg("images verified")
		return ctrl.Result{}, r.patch(ctx, base, &iv)
	case iv.Status.Deadline != nil && !r.now().Before(iv.Status.Deadline.Time):
		return r.fail(ctx, base, &iv, fmt.Sprintf("not verified within the %s timeout: %s",
			iv.Status.Deadline.Sub(iv.Status.StartedAt.Time), waiting))
	}
	iv.Status.Attempts++
	iv.Status.Message = waiting
	if err := r.patch(ctx, base, &iv); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.backoff(&iv)}, nil
}

// checkImages verifies the images not verified yet. It returns why the
// verification is still waiting ("" when every image is verified) or why it
// failed ("" when nothing failed).
func (r *Reconciler) checkImages(ctx context.Context, log zerolog.Logger, iv *v1alpha1.ImageVerification) (waiting, failed string) {
	if len(iv.Spec.Images) == 0 {
		iv.Status.Images = nil
		return "", ""
	}
	results := make([]v1alpha1.ImageVerificationResult, len(iv.Spec.Images))
	prev := map[string]v1alpha1.ImageVerificationResult{}
	for _, res := range iv.Status.Images {
		prev[res.Image] = res
	}
	var authorities []authority
	var loadErr error
	for i, img := range iv.Spec.Images {
		ref := img.Repository + "@" + img.Digest
		if p, ok := prev[ref]; ok && p.Verified {
			results[i] = p
			continue
		}
		results[i] = v1alpha1.ImageVerificationResult{Image: ref}
		if authorities == nil && loadErr == nil {
			authorities, loadErr = r.authorities(ctx, iv)
		}
		if loadErr != nil {
			results[i].Message = loadErr.Error()
			if waiting == "" {
				waiting = loadErr.Error()
			}
			continue
		}
		opts, err := r.registryOptions(ctx, iv)
		if err != nil {
			results[i].Message = err.Error()
			if waiting == "" {
				waiting = err.Error()
			}
			continue
		}
		sigs, err := r.Registry.Signatures(ctx, img.Repository, img.Digest, opts)
		if err != nil {
			log.Warn().Err(err).Str("image", ref).Msg("registry error; retrying")
			results[i].Message = "registry: " + err.Error()
			if waiting == "" {
				waiting = fmt.Sprintf("%s: %s", ref, results[i].Message)
			}
			continue
		}
		if sigs.empty() {
			results[i].Message = "no signature found"
			if waiting == "" {
				waiting = fmt.Sprintf("%s: no signature found yet", ref)
			}
			continue
		}
		v := verifyImage(img.Digest, sigs, authorities)
		if v.verified {
			results[i] = v1alpha1.ImageVerificationResult{Image: ref, Verified: true, Authority: v.authority, Signer: v.signer}
			continue
		}
		results[i].Message = truncate(strings.Join(v.reasons, "; "))
		if failed == "" {
			failed = fmt.Sprintf("%s: no signature verifies against the policy's authorities: %s", ref, results[i].Message)
		}
	}
	iv.Status.Images = results
	return waiting, failed
}

// checkCommit verifies the config commit, once.
func (r *Reconciler) checkCommit(ctx context.Context, iv *v1alpha1.ImageVerification) (waiting, failed string) {
	c := iv.Spec.Commit
	if c == nil {
		return "", ""
	}
	if iv.Status.Commit != nil && iv.Status.Commit.Verified {
		return "", ""
	}
	cv, ok := r.SCM.(scm.CommitVerifier)
	if !ok || r.SCM == nil {
		iv.Status.Commit = &v1alpha1.CommitVerificationResult{Message: scm.ErrCommitVerificationUnsupported.Error()}
		return "", "commit " + c.SHA + ": " + scm.ErrCommitVerificationUnsupported.Error()
	}
	repo, err := scm.RepoFromURL(c.Repo)
	if err != nil {
		iv.Status.Commit = &v1alpha1.CommitVerificationResult{Message: err.Error()}
		return "", fmt.Sprintf("commit %s: %v", c.SHA, err)
	}
	sig, err := cv.VerifyCommit(ctx, repo, c.SHA)
	switch {
	case err != nil && (scm.IsPermanentError(err) || errors.Is(err, scm.ErrCommitVerificationUnsupported)):
		iv.Status.Commit = &v1alpha1.CommitVerificationResult{Message: err.Error()}
		return "", fmt.Sprintf("commit %s: %v", c.SHA, err)
	case err != nil:
		iv.Status.Commit = &v1alpha1.CommitVerificationResult{Message: "SCM: " + err.Error()}
		return fmt.Sprintf("commit %s: SCM: %v", c.SHA, err), ""
	case !sig.Verified:
		reason := sig.Reason
		if reason == "" {
			reason = "not verified"
		}
		iv.Status.Commit = &v1alpha1.CommitVerificationResult{Message: reason, Signer: sig.Signer}
		return "", fmt.Sprintf("commit %s of %s is not signed with a verified signature (%s)", c.SHA, repo, reason)
	}
	iv.Status.Commit = &v1alpha1.CommitVerificationResult{Verified: true, Signer: sig.Signer}
	return "", ""
}

// authorities loads the policy's keys and trusted roots. Secrets are read
// on every check, so a rotated key applies at once.
func (r *Reconciler) authorities(ctx context.Context, iv *v1alpha1.ImageVerification) ([]authority, error) {
	var out []authority
	for _, a := range iv.Spec.Policy.Authorities {
		switch {
		case a.Key != nil:
			pem, err := r.secretKey(ctx, iv.Namespace, a.Key.SecretRef)
			if err != nil {
				return nil, fmt.Errorf("authority %s: %w", a.Name, err)
			}
			v, err := loadKey(pem)
			if err != nil {
				return nil, fmt.Errorf("authority %s: %w", a.Name, err)
			}
			au := authority{name: a.Name, key: v, requireTlog: a.Key.RequireTransparencyLog}
			if au.requireTlog {
				if au.tlogs, err = r.publicGood(ctx); err != nil {
					return nil, fmt.Errorf("authority %s: %w", a.Name, err)
				}
			}
			out = append(out, au)
		case a.Keyless != nil:
			k := a.Keyless
			if k.SubjectRegExp != "" {
				if _, err := regexp.Compile(k.SubjectRegExp); err != nil {
					return nil, fmt.Errorf("authority %s: subjectRegExp: %w", a.Name, err)
				}
			}
			id, err := verify.NewShortCertificateIdentity(k.Issuer, "", k.Subject, k.SubjectRegExp)
			if err != nil {
				return nil, fmt.Errorf("authority %s: %w", a.Name, err)
			}
			var tm root.TrustedMaterial
			if k.TrustedRootRef != nil {
				raw, err := r.secretKey(ctx, iv.Namespace, *k.TrustedRootRef)
				if err != nil {
					return nil, fmt.Errorf("authority %s: %w", a.Name, err)
				}
				if tm, err = root.NewTrustedRootFromJSON(raw); err != nil {
					return nil, fmt.Errorf("authority %s: trusted root: %w", a.Name, err)
				}
			} else if tm, err = r.publicGood(ctx); err != nil {
				return nil, fmt.Errorf("authority %s: %w", a.Name, err)
			}
			out = append(out, authority{name: a.Name, trusted: tm, identity: &id})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the policy has no authority to verify images with")
	}
	return out, nil
}

func (r *Reconciler) publicGood(ctx context.Context) (root.TrustedMaterial, error) {
	if r.PublicGoodRoot == nil {
		return nil, fmt.Errorf("no Sigstore public-good trusted root is configured; set trustedRootRef")
	}
	tm, err := r.PublicGoodRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch the Sigstore public-good trusted root: %w", err)
	}
	return tm, nil
}

// secretKey reads one key of a Secret in ns.
func (r *Reconciler) secretKey(ctx context.Context, ns string, ref v1alpha1.SecretKeyName) ([]byte, error) {
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
		return nil, fmt.Errorf("read secret %s: %w", ref.Name, err)
	}
	v, ok := s.Data[ref.Key]
	if !ok || len(v) == 0 {
		return nil, fmt.Errorf("secret %s has no key %q", ref.Name, ref.Key)
	}
	return v, nil
}

// registryOptions builds the registry access of iv.
func (r *Reconciler) registryOptions(ctx context.Context, iv *v1alpha1.ImageVerification) (RegistryOptions, error) {
	p := iv.Spec.Policy
	opts := RegistryOptions{Insecure: p.InsecureRegistries, SignatureRepository: p.SignatureRepository}
	if p.RegistrySecretRef != nil {
		raw, err := r.secretKey(ctx, iv.Namespace, v1alpha1.SecretKeyName{Name: p.RegistrySecretRef.Name, Key: corev1.DockerConfigJsonKey})
		if err != nil {
			return opts, err
		}
		kc, err := dockerConfigKeychain(raw)
		if err != nil {
			return opts, fmt.Errorf("secret %s: %w", p.RegistrySecretRef.Name, err)
		}
		opts.Keychain = kc
	}
	return opts, nil
}

// dockerConfigKeychain is a keychain from a .dockerconfigjson.
func dockerConfigKeychain(raw []byte) (authn.Keychain, error) {
	var cfg struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse .dockerconfigjson: %w", err)
	}
	kc := staticKeychain{}
	for host, a := range cfg.Auths {
		host = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://"), "/")
		kc[host] = authn.FromConfig(authn.AuthConfig{Username: a.Username, Password: a.Password, Auth: a.Auth})
	}
	return kc, nil
}

// staticKeychain resolves a registry host to its credentials.
type staticKeychain map[string]authn.Authenticator

func (k staticKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	if a, ok := k[target.RegistryStr()]; ok {
		return a, nil
	}
	return authn.Anonymous, nil
}

func (r *Reconciler) fail(ctx context.Context, base, iv *v1alpha1.ImageVerification, msg string) (ctrl.Result, error) {
	iv.Status.Phase, iv.Status.Message = v1alpha1.ImageVerificationFailed, truncate(msg)
	return ctrl.Result{}, r.patch(ctx, base, iv)
}

// patch writes iv's status, optimistic-locked on base's resourceVersion: a
// reconcile from a stale copy gets a conflict (and is retried) instead of
// overwriting a newer verdict; the CRD also refuses to change a terminal
// phase.
func (r *Reconciler) patch(ctx context.Context, base, iv *v1alpha1.ImageVerification) error {
	if err := r.Status().Patch(ctx, iv, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("patch imageverification %s status: %w", iv.Name, err)
	}
	return nil
}

func (r *Reconciler) backoff(iv *v1alpha1.ImageVerification) time.Duration {
	d := retryBase
	for i := 1; i < iv.Status.Attempts && d < retryMax; i++ {
		d *= 2
	}
	if d > retryMax {
		d = retryMax
	}
	if iv.Status.Deadline != nil {
		if left := iv.Status.Deadline.Sub(r.now()); left > 0 && left < d {
			d = left
		}
	}
	return d
}

func verifiedMessage(iv *v1alpha1.ImageVerification) string {
	var parts []string
	for _, res := range iv.Status.Images {
		parts = append(parts, fmt.Sprintf("%s signed by %s", res.Image, res.Signer))
	}
	if iv.Status.Commit != nil && iv.Status.Commit.Verified {
		parts = append(parts, fmt.Sprintf("commit %s signed by %s", iv.Spec.Commit.SHA, iv.Status.Commit.Signer))
	}
	return truncate("verified: " + strings.Join(parts, "; "))
}

func policyTimeout(s string) (time.Duration, error) {
	if s == "" || s == "0" {
		return DefaultTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("spec.policy.timeout %q is not a positive duration", s)
	}
	return d, nil
}

func terminal(phase string) bool {
	return phase == v1alpha1.ImageVerificationVerified || phase == v1alpha1.ImageVerificationFailed
}

func truncate(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// PublicGoodRoot returns a function that fetches the Sigstore public-good
// trusted root through TUF on first use and keeps it, refreshed daily.
func PublicGoodRoot(fetch func() (root.TrustedMaterial, error)) func(context.Context) (root.TrustedMaterial, error) {
	var (
		mu  sync.Mutex
		got root.TrustedMaterial
	)
	return func(context.Context) (root.TrustedMaterial, error) {
		mu.Lock()
		defer mu.Unlock()
		if got != nil {
			return got, nil
		}
		tm, err := fetch()
		if err != nil {
			return nil, err
		}
		got = tm
		return got, nil
	}
}

// SetupWithManager registers the reconciler.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ImageVerification{}).
		Complete(r)
}
