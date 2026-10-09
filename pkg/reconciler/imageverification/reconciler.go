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
	"golang.org/x/sync/singleflight"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

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
	// reconcileTimeout bounds one reconcile (every registry and SCM call
	// in it); registryTimeout bounds the signature fetch of one image.
	reconcileTimeout = 2 * time.Minute
	registryTimeout  = 45 * time.Second
	// MaxConcurrentReconciles is how many ImageVerifications are checked at
	// once: a slow registry holds one worker, not every Bundle's.
	MaxConcurrentReconciles = 4
)

// LabelReferenceable must be "true" on every Secret an image policy names
// (key, trusted root, registry credentials), as for every Secret a
// kardinal resource names: the Secret's owner opts in to its use.
const LabelReferenceable = v1alpha1.LabelSecretReferenceable

// Status reasons (status.reason).
const (
	ReasonSecretNotReferenceable     = v1alpha1.ReasonSecretNotReferenceable
	ReasonSecretNotFound             = "SecretNotFound"
	ReasonInvalidPublicKey           = "InvalidPublicKey"
	ReasonInvalidTrustedRoot         = "InvalidTrustedRoot"
	ReasonInvalidRegistryCredentials = "InvalidRegistryCredentials"
	ReasonSignatureNotVerified       = "SignatureNotVerified"
	ReasonCommitNotVerified          = "CommitNotVerified"
	ReasonTimeout                    = "Timeout"
)

// reasonError is an error with a status reason. Its message never quotes
// Secret content.
type reasonError struct{ reason, msg string }

func (e *reasonError) Error() string { return e.msg }

func reasonOf(err error) string {
	var re *reasonError
	if errors.As(err, &re) {
		return re.reason
	}
	return ""
}

// Reconciler verifies ImageVerifications.
type Reconciler struct {
	client.Client

	// Registry fetches signatures.
	Registry RegistryClient

	// SCM verifies commits; it must implement scm.CommitVerifier for a
	// policy with commits.requireSigned.
	SCM scm.SCMProvider

	// SCMHost is the web host of the controller's SCM provider
	// (scm.WebHost). A commit in a repository on another host is refused:
	// the provider would be asked about a repository it does not serve.
	SCMHost string

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
	ctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
	defer cancel()
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
			return r.fail(ctx, base, &iv, "", err.Error())
		}
		deadline := metav1.NewTime(now.Add(timeout))
		iv.Status.StartedAt, iv.Status.Deadline, iv.Status.Phase = &now, &deadline, v1alpha1.ImageVerificationPending
		iv.Status.Message = "checking signatures"
	}
	iv.Status.LastCheckedAt = &now

	o := r.checkImages(ctx, log, &iv)
	if o.failed == "" {
		c := r.checkCommit(ctx, &iv)
		if c.failed != "" {
			o.failed, o.reason = c.failed, c.reason
		}
		if c.waiting != "" && o.waiting == "" {
			o.waiting, o.reason = c.waiting, c.reason
		}
	}
	switch {
	case o.failed != "":
		log.Info().Str("reason", o.reason).Str("message", o.failed).Msg("image verification failed")
		return r.fail(ctx, base, &iv, o.reason, o.failed)
	case o.waiting == "":
		iv.Status.Phase, iv.Status.Message, iv.Status.Reason, iv.Status.Attempts = v1alpha1.ImageVerificationVerified, verifiedMessage(&iv), "", 0
		log.Info().Msg("images verified")
		return ctrl.Result{}, r.patch(ctx, base, &iv)
	case iv.Status.Deadline != nil && !r.now().Before(iv.Status.Deadline.Time):
		reason := o.reason
		if reason == "" {
			reason = ReasonTimeout
		}
		return r.fail(ctx, base, &iv, reason, fmt.Sprintf("not verified within the %s timeout: %s",
			iv.Status.Deadline.Sub(iv.Status.StartedAt.Time), o.waiting))
	}
	iv.Status.Attempts++
	iv.Status.Message, iv.Status.Reason = o.waiting, o.reason
	if err := r.patch(ctx, base, &iv); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.backoff(&iv)}, nil
}

// outcome is what one check found: why it is still waiting ("" when it
// passed), why it failed ("" when it did not), and the status reason.
type outcome struct{ waiting, failed, reason string }

// checkImages verifies the images not verified yet. A signature that does
// not verify (another signer's, an attestation, a malformed one) is not a
// verdict: the check waits for a verifying one until the deadline, and the
// reasons are in the message. Only a cosign signature made with a policy key
// for another digest fails at once.
func (r *Reconciler) checkImages(ctx context.Context, log zerolog.Logger, iv *v1alpha1.ImageVerification) outcome {
	var o outcome
	if len(iv.Spec.Images) == 0 {
		iv.Status.Images = nil
		return o
	}
	wait := func(msg, reason string) {
		if o.waiting == "" {
			o.waiting, o.reason = msg, reason
		}
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
			wait(loadErr.Error(), reasonOf(loadErr))
			continue
		}
		opts, err := r.registryOptions(ctx, iv)
		if err != nil {
			results[i].Message = err.Error()
			wait(err.Error(), reasonOf(err))
			continue
		}
		fetchCtx, cancel := context.WithTimeout(ctx, registryTimeout)
		sigs, err := r.Registry.Signatures(fetchCtx, img.Repository, img.Digest, opts)
		cancel()
		if err != nil {
			log.Warn().Err(err).Str("image", ref).Msg("registry error; retrying")
			results[i].Message = "registry: " + err.Error()
			wait(fmt.Sprintf("%s: %s", ref, results[i].Message), "")
			continue
		}
		if sigs.empty() {
			results[i].Message = "no signature found"
			wait(fmt.Sprintf("%s: no signature found yet", ref), "")
			continue
		}
		v := verifyImage(img.Digest, sigs, authorities)
		if v.verified {
			results[i] = v1alpha1.ImageVerificationResult{Image: ref, Verified: true, Authority: v.authority, Signer: v.signer}
			continue
		}
		results[i].Message = truncate(strings.Join(v.reasons, "; "))
		if v.wrongDigest && o.failed == "" {
			o.failed, o.reason = fmt.Sprintf("%s: %s", ref, results[i].Message), ReasonSignatureNotVerified
			continue
		}
		wait(fmt.Sprintf("%s: no signature verifies against the policy's authorities yet: %s", ref, results[i].Message),
			ReasonSignatureNotVerified)
	}
	iv.Status.Images = results
	return o
}

// fullSHA is a full SHA-1 or SHA-256 commit id.
var fullSHA = regexp.MustCompile(`^([a-f0-9]{40}|[a-f0-9]{64})$`)

// checkCommit verifies the config commit, once: the SCM must report a
// verified signature for exactly that commit, on the SCM host the
// controller serves, by an allowed signer when the policy lists them.
func (r *Reconciler) checkCommit(ctx context.Context, iv *v1alpha1.ImageVerification) outcome {
	c := iv.Spec.Commit
	if c == nil {
		return outcome{}
	}
	if iv.Status.Commit != nil && iv.Status.Commit.Verified {
		return outcome{}
	}
	failed := func(msg string) outcome {
		iv.Status.Commit = &v1alpha1.CommitVerificationResult{Message: msg}
		return outcome{failed: "commit " + c.SHA + ": " + msg, reason: ReasonCommitNotVerified}
	}
	if !fullSHA.MatchString(c.SHA) {
		return failed("not a full 40- or 64-character commit SHA")
	}
	cv, ok := r.SCM.(scm.CommitVerifier)
	if !ok || r.SCM == nil {
		return failed(scm.ErrCommitVerificationUnsupported.Error())
	}
	host, repo, err := scm.RepoIdentity(c.Repo)
	if err != nil {
		return failed(err.Error())
	}
	if r.SCMHost == "" || !strings.EqualFold(host, r.SCMHost) {
		return failed(fmt.Sprintf("repository %s is on %s, not on the controller's SCM host %q; "+
			"its commit signature cannot be checked", repo, host, r.SCMHost))
	}
	sig, err := cv.VerifyCommit(ctx, repo, c.SHA)
	switch {
	case err != nil && (scm.IsPermanentError(err) || errors.Is(err, scm.ErrCommitVerificationUnsupported)):
		return failed(err.Error())
	case err != nil:
		iv.Status.Commit = &v1alpha1.CommitVerificationResult{Message: "SCM: " + err.Error()}
		return outcome{waiting: fmt.Sprintf("commit %s: SCM: %v", c.SHA, err)}
	case !strings.EqualFold(sig.SHA, c.SHA):
		return failed(fmt.Sprintf("the SCM answered for commit %q, not %s", sig.SHA, c.SHA))
	case !sig.Verified:
		reason := sig.Reason
		if reason == "" {
			reason = "not verified"
		}
		iv.Status.Commit = &v1alpha1.CommitVerificationResult{Message: reason, Signer: sig.Signer}
		return outcome{failed: fmt.Sprintf("commit %s of %s is not signed with a verified signature (%s)", c.SHA, repo, reason),
			reason: ReasonCommitNotVerified}
	}
	if allowed := commitAllowedSigners(iv); len(allowed) > 0 && !signerAllowed(sig, allowed) {
		iv.Status.Commit = &v1alpha1.CommitVerificationResult{Message: "signer not allowed", Signer: sig.Signer}
		return outcome{failed: fmt.Sprintf("commit %s of %s is signed by %s (%s), who is not in commits.allowedSigners",
			c.SHA, repo, sig.Signer, strings.Join(sig.Identities, ", ")), reason: ReasonCommitNotVerified}
	}
	iv.Status.Commit = &v1alpha1.CommitVerificationResult{Verified: true, Signer: sig.Signer}
	return outcome{}
}

func commitAllowedSigners(iv *v1alpha1.ImageVerification) []string {
	if iv.Spec.Policy.Commits == nil {
		return nil
	}
	return iv.Spec.Policy.Commits.AllowedSigners
}

// signerAllowed reports whether one of the identities the SCM reported for
// the signature (login, email, key ID; "web-flow" or "gitlab-system" for a
// platform signature) is allowed. Emails and logins compare case-insensitively.
func signerAllowed(sig scm.CommitSignature, allowed []string) bool {
	for _, id := range sig.Identities {
		for _, a := range allowed {
			if id != "" && strings.EqualFold(strings.TrimSpace(a), id) {
				return true
			}
		}
	}
	return false
}

// authorities loads the policy's keys and trusted roots. Secrets are read
// on every check, so a rotated key applies at once. Errors name the Secret
// and key, never their content.
func (r *Reconciler) authorities(ctx context.Context, iv *v1alpha1.ImageVerification) ([]authority, error) {
	var out []authority
	for _, a := range iv.Spec.Policy.Authorities {
		switch {
		case a.Key != nil:
			ref := a.Key.SecretRef
			pem, err := r.secretKey(ctx, iv.Namespace, ref)
			if err != nil {
				return nil, wrapReason(err, "authority %s", a.Name)
			}
			v, err := loadKey(pem)
			if err != nil {
				return nil, &reasonError{ReasonInvalidPublicKey, fmt.Sprintf(
					"authority %s: secret %s key %s is not a PEM public key", a.Name, ref.Name, ref.Key)}
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
			subjectRE := ""
			if k.SubjectRegExp != "" {
				// Anchored: sigstore-go matches the expression anywhere in
				// the subject, so "github.com/org/app" would accept
				// "https://evil.example/github.com/org/app".
				subjectRE = "^(?:" + k.SubjectRegExp + ")$"
				if _, err := regexp.Compile(subjectRE); err != nil {
					return nil, fmt.Errorf("authority %s: subjectRegExp: %w", a.Name, err)
				}
			}
			id, err := verify.NewShortCertificateIdentity(k.Issuer, "", k.Subject, subjectRE)
			if err != nil {
				return nil, fmt.Errorf("authority %s: %w", a.Name, err)
			}
			var tm root.TrustedMaterial
			if k.TrustedRootRef != nil {
				ref := *k.TrustedRootRef
				raw, err := r.secretKey(ctx, iv.Namespace, ref)
				if err != nil {
					return nil, wrapReason(err, "authority %s", a.Name)
				}
				if tm, err = root.NewTrustedRootFromJSON(raw); err != nil {
					return nil, &reasonError{ReasonInvalidTrustedRoot, fmt.Sprintf(
						"authority %s: secret %s key %s is not a valid Sigstore trusted root", a.Name, ref.Name, ref.Key)}
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

// wrapReason prefixes err's message and keeps its reason.
func wrapReason(err error, format string, args ...interface{}) error {
	prefix := fmt.Sprintf(format, args...)
	var re *reasonError
	if errors.As(err, &re) {
		return &reasonError{re.reason, prefix + ": " + re.msg}
	}
	return fmt.Errorf("%s: %w", prefix, err)
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

// secretKey reads one key of a Secret in ns. The Secret must be labelled
// kardinal.io/referenceable: "true".
func (r *Reconciler) secretKey(ctx context.Context, ns string, ref v1alpha1.SecretKeyName) ([]byte, error) {
	var s corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &reasonError{ReasonSecretNotFound, fmt.Sprintf("secret %s not found", ref.Name)}
		}
		return nil, fmt.Errorf("read secret %s: %w", ref.Name, err)
	}
	if s.Labels[LabelReferenceable] != "true" {
		return nil, &reasonError{ReasonSecretNotReferenceable, fmt.Sprintf(
			"%s: secret %s does not have the label %s: \"true\"", ReasonSecretNotReferenceable, ref.Name, LabelReferenceable)}
	}
	v, ok := s.Data[ref.Key]
	if !ok || len(v) == 0 {
		return nil, &reasonError{ReasonSecretNotFound, fmt.Sprintf("secret %s has no key %q", ref.Name, ref.Key)}
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
			return opts, wrapReason(err, "registrySecretRef")
		}
		kc, err := dockerConfigKeychain(raw)
		if err != nil {
			return opts, &reasonError{ReasonInvalidRegistryCredentials, fmt.Sprintf(
				"registrySecretRef: secret %s is not a valid .dockerconfigjson", p.RegistrySecretRef.Name)}
		}
		opts.Keychain = kc
	}
	return opts, nil
}

// dockerConfigKeychain is a keychain from a .dockerconfigjson. Its errors
// never quote the content.
func dockerConfigKeychain(raw []byte) (authn.Keychain, error) {
	var cfg struct {
		Auths map[string]struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, errors.New("not valid JSON")
	}
	kc := staticKeychain{}
	for host, a := range cfg.Auths {
		kc[registryKey(host)] = authn.FromConfig(authn.AuthConfig{Username: a.Username, Password: a.Password, Auth: a.Auth})
	}
	return kc, nil
}

// registryKey is the keychain key of a .dockerconfigjson auths entry or a
// registry host: the host lowercased, without scheme, path or :443, and
// index.docker.io for Docker Hub, whose usual entry is
// "https://index.docker.io/v1/" (go-containerregistry names the Docker Hub
// registry index.docker.io).
func registryKey(host string) string {
	h := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), "https://"), "http://")
	h, _, _ = strings.Cut(h, "/")
	h = strings.TrimSuffix(h, ":443")
	switch h {
	case "docker.io", "registry-1.docker.io", "index.docker.io":
		return "index.docker.io"
	}
	return h
}

// staticKeychain resolves a registry host to its credentials.
type staticKeychain map[string]authn.Authenticator

func (k staticKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	if a, ok := k[registryKey(target.RegistryStr())]; ok {
		return a, nil
	}
	return authn.Anonymous, nil
}

func (r *Reconciler) fail(ctx context.Context, base, iv *v1alpha1.ImageVerification, reason, msg string) (ctrl.Result, error) {
	iv.Status.Phase, iv.Status.Reason, iv.Status.Message = v1alpha1.ImageVerificationFailed, reason, truncate(msg)
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

// Public-good trusted root refresh.
const (
	// PublicGoodRootTTL is how long a fetched trusted root is used before it
	// is fetched again.
	PublicGoodRootTTL = 24 * time.Hour
	// publicGoodWait bounds how long a reconcile waits for a fetch; the
	// fetch itself is bounded by its HTTP client (PublicGoodFetchTimeout).
	publicGoodWait = 30 * time.Second
	// PublicGoodFetchTimeout bounds each TUF HTTP request.
	PublicGoodFetchTimeout = 20 * time.Second
	// publicGoodRetry spaces fetch attempts after one failed.
	publicGoodRetry = 5 * time.Minute
)

// PublicGoodRoot returns a function that fetches the Sigstore public-good
// trusted root through TUF on first use and again once it is older than
// PublicGoodRootTTL. One fetch runs at a time (singleflight), outside any
// lock, and a caller waits for it at most publicGoodWait (or its context).
// When a refresh fails, the previous root keeps being used and the fetch is
// retried after publicGoodRetry.
func PublicGoodRoot(fetch func() (root.TrustedMaterial, error)) func(context.Context) (root.TrustedMaterial, error) {
	return newPublicGoodCache(fetch, time.Now).get
}

type publicGoodCache struct {
	fetch func() (root.TrustedMaterial, error)
	now   func() time.Time
	sf    singleflight.Group

	mu        sync.Mutex
	got       root.TrustedMaterial
	fetchedAt time.Time
	failedAt  time.Time
}

func newPublicGoodCache(fetch func() (root.TrustedMaterial, error), now func() time.Time) *publicGoodCache {
	return &publicGoodCache{fetch: fetch, now: now}
}

func (c *publicGoodCache) get(ctx context.Context) (root.TrustedMaterial, error) {
	c.mu.Lock()
	got, fresh := c.got, c.got != nil && c.now().Sub(c.fetchedAt) < PublicGoodRootTTL
	backingOff := !c.failedAt.IsZero() && c.now().Sub(c.failedAt) < publicGoodRetry
	c.mu.Unlock()
	if fresh || (got != nil && backingOff) {
		return got, nil
	}
	ch := c.sf.DoChan("root", func() (interface{}, error) {
		tm, err := c.fetch()
		c.mu.Lock()
		defer c.mu.Unlock()
		if err != nil {
			c.failedAt = c.now()
			return nil, err
		}
		c.got, c.fetchedAt, c.failedAt = tm, c.now(), time.Time{}
		return tm, nil
	})
	timer := time.NewTimer(publicGoodWait)
	defer timer.Stop()
	select {
	case res := <-ch:
		if res.Err != nil {
			if got != nil {
				return got, nil
			}
			return nil, res.Err
		}
		return res.Val.(root.TrustedMaterial), nil
	case <-timer.C:
	case <-ctx.Done():
	}
	if got != nil {
		return got, nil
	}
	return nil, fmt.Errorf("the Sigstore public-good trusted root is still being fetched (TUF); retrying")
}

// SetupWithManager registers the reconciler.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ImageVerification{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: MaxConcurrentReconciles}).
		Complete(r)
}
